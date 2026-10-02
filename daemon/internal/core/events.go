package core

import (
	"encoding/json"
	"sync"
	"time"
)

// Event is one entry of the append-only event table. Every consumer
// (channels, webhooks, the stream, polling) reads the same events, filtered
// to what the subscriber's owner may see.
type Event struct {
	ID    int64          `json:"id"`
	Type  string         `json:"type"`
	Time  string         `json:"time"`
	Owner string         `json:"owner,omitempty"`
	Task  *EventTask     `json:"task,omitempty"`
	Data  map[string]any `json:"data,omitempty"`
	ts    int64
}

// EventTask is the task summary carried by task events.
type EventTask struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Size     int64  `json:"size"`
	Folder   string `json:"folder"`
	Duration int64  `json:"duration_s"`
}

// AdminOnly event types are delivered to administrators only.
var AdminOnly = map[string]bool{
	"engine.down": true, "engine.up": true, "security.token_created": true, "security.token_rejected": true,
	"schedule.changed": false, "disk.low": false, "queue.idle": false,
}

type bus struct {
	mu   sync.Mutex
	subs map[int]chan Event
	next int
}

// Subscribe returns a channel receiving every new event, and a cancel func.
func (m *Manager) Subscribe() (<-chan Event, func()) {
	m.bus.mu.Lock()
	defer m.bus.mu.Unlock()
	if m.bus.subs == nil {
		m.bus.subs = map[int]chan Event{}
	}
	id := m.bus.next
	m.bus.next++
	ch := make(chan Event, 256)
	m.bus.subs[id] = ch
	return ch, func() {
		m.bus.mu.Lock()
		if c, ok := m.bus.subs[id]; ok {
			delete(m.bus.subs, id)
			close(c)
		}
		m.bus.mu.Unlock()
	}
}

// Emit stores an event and fans it out to subscribers.
func (m *Manager) Emit(e Event) Event {
	now := time.Now()
	e.ts = now.Unix()
	e.Time = now.Format(time.RFC3339)
	data, _ := json.Marshal(struct {
		Task *EventTask     `json:"task,omitempty"`
		Data map[string]any `json:"data,omitempty"`
	}{e.Task, e.Data})
	taskID := ""
	if e.Task != nil {
		taskID = e.Task.ID
	}
	if res, err := m.db.X(`INSERT INTO events (type, time, owner, task_hash, data) VALUES (?, ?, ?, ?, ?)`, e.Type, e.ts, e.Owner, taskID, string(data)); err == nil {
		e.ID, _ = res.LastInsertId()
	}
	m.bus.mu.Lock()
	for _, ch := range m.bus.subs {
		select {
		case ch <- e:
		default: // slow consumer: it can catch up from the table
		}
	}
	m.bus.mu.Unlock()
	return e
}

// TaskEvent emits an event about a task.
func (m *Manager) TaskEvent(typ string, t *Task, data map[string]any) {
	dur := t.ActiveSecs
	m.Emit(Event{Type: typ, Owner: t.Owner, Task: &EventTask{
		ID: t.Hash, Name: t.Name, Kind: kindPublic(t), Size: t.Size, Folder: m.DisplayPath(t.Owner, t.finalDir()), Duration: dur,
	}, Data: data})
}

func kindPublic(t *Task) string {
	if t.Kind == KindBT {
		if t.IsMagnet() {
			return "magnet"
		}
		return "torrent"
	}
	return "url"
}

// Events returns stored events after id (oldest first).
func (m *Manager) Events(after int64, limit int) []Event {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := m.db.Query(`SELECT id, type, time, owner, data FROM events WHERE id > ? ORDER BY id LIMIT ?`, after, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var data string
		rows.Scan(&e.ID, &e.Type, &e.ts, &e.Owner, &data)
		e.Time = time.Unix(e.ts, 0).Format(time.RFC3339)
		var d struct {
			Task *EventTask     `json:"task"`
			Data map[string]any `json:"data"`
		}
		json.Unmarshal([]byte(data), &d)
		e.Task, e.Data = d.Task, d.Data
		out = append(out, e)
	}
	return out
}

// LastEventID is the id of the newest event.
func (m *Manager) LastEventID() int64 {
	var id int64
	m.db.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM events`).Scan(&id)
	return id
}

// Visible reports whether an event may be shown to a user.
func Visible(e Event, user string, admin bool, allTasks bool) bool {
	if AdminOnly[e.Type] {
		return admin
	}
	if e.Owner == "" {
		return true
	}
	if admin && allTasks {
		return true
	}
	return e.Owner == user
}
