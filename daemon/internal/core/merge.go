package core

import (
	"errors"
	"os"
	"time"

	"downloadcenter/internal/torrent"
)

// Source is another torrent of the same content folded into a task.
type Source struct {
	Hash   string `json:"hash"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
	Added  int64  `json:"added_at"`
}

// Sources lists a task's sources (the task's own torrent first).
func (m *Manager) Sources(hash string) []Source {
	rows, err := m.db.Query(`SELECT source_hash, kind, source, name, active, added_at FROM task_sources WHERE task_hash = ? ORDER BY added_at`, hash)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		var s Source
		var a int
		rows.Scan(&s.Hash, &s.Kind, &s.Source, &s.Name, &a, &s.Added)
		s.Active = a != 0
		out = append(out, s)
	}
	return out
}

// addContentSource records a torrent with the same files (different
// infohash) as another source of an existing task. Only one source
// downloads at a time; the manager switches when the active one stalls.
func (m *Manager) addContentSource(taskHash string, b []byte, meta *torrent.Meta, o AddOptions) (*AddResult, error) {
	t := m.Live(taskHash)
	if t == nil || t.Kind != KindBT {
		return nil, ErrNotFound
	}
	if !o.Admin && t.Owner != o.Owner {
		return nil, ErrNotOwner
	}
	cur, err := os.ReadFile(m.torrentPath(m.activeSource(t)))
	if err != nil {
		return nil, ErrNotFound
	}
	other, err := torrent.Parse(cur)
	if err != nil || other.Name != meta.Name || !sameFiles(other, meta) {
		return nil, errors.New("The file lists differ, so they cannot be merged")
	}
	// A private torrent finds peers only through its own tracker, which
	// counts what it seeds: it never shares a task with another torrent
	if meta.Private || other.Private || m.TorrentPrivate(taskHash) {
		return nil, ErrPrivate
	}
	if err := os.WriteFile(m.torrentPath(meta.InfoHash), b, 0600); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if len(m.Sources(taskHash)) == 0 {
		m.db.X(`INSERT OR IGNORE INTO task_sources (task_hash, source_hash, kind, source, name, active, added_at) VALUES (?,?,?,?,?,1,?)`,
			taskHash, taskHash, "torrent", t.Source, t.Name, t.CreatedAt)
	}
	m.db.X(`INSERT OR IGNORE INTO task_sources (task_hash, source_hash, kind, source, name, active, added_at) VALUES (?,?,?,?,?,0,?)`,
		taskHash, meta.InfoHash, "torrent", meta.Name+".torrent", meta.Name, now)
	m.Log(taskHash, "Added another torrent with the same content as a backup source")
	m.TaskEvent("task.merged", t, map[string]any{"source": meta.InfoHash, "content": true})
	return &AddResult{ID: taskHash, Name: t.Name, Merged: true}, nil
}

// activeSource is the infohash whose .torrent the task currently uses.
func (m *Manager) activeSource(t *Task) string {
	var h string
	m.db.QueryRow(`SELECT source_hash FROM task_sources WHERE task_hash = ? AND active = 1`, t.Hash).Scan(&h)
	if h == "" {
		return t.Hash
	}
	return h
}

// switchStalledSources moves content-merged tasks whose active source has
// not transferred data for the configured time to the next source.
func (m *Manager) switchStalledSources() {
	stall := int64(m.Settings().Torrent.StallMinutes) * 60
	now := time.Now().Unix()
	for _, t := range m.List() {
		if t.Kind != KindBT || t.State != StDownloading {
			continue
		}
		srcs := m.Sources(t.Hash)
		if len(srcs) < 2 {
			continue
		}
		last := t.Options.LastActive
		if last == 0 {
			last = t.StartedAt
		}
		if now-last < stall {
			continue
		}
		idx := 0
		for i, s := range srcs {
			if s.Active {
				idx = i
			}
		}
		next := srcs[(idx+1)%len(srcs)]
		m.switchSource(t.Hash, srcs[idx].Hash, next.Hash)
	}
}

func (m *Manager) switchSource(taskHash, from, to string) {
	b, err := os.ReadFile(m.torrentPath(to))
	if err != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.live[taskHash]
	if !ok {
		return
	}
	if e := m.engineOf(t); e != nil && t.EngineRef != "" {
		e.Remove(t.EngineRef)
	}
	m.db.X(`UPDATE task_sources SET active = CASE WHEN source_hash = ? THEN 1 ELSE 0 END WHERE task_hash = ?`, to, taskHash)
	// The task's own torrent file is what the engine adds: swap it in
	if from == taskHash {
		cur, _ := os.ReadFile(m.torrentPath(taskHash))
		os.WriteFile(m.torrentPath(taskHash+".orig"), cur, 0600)
	}
	if to == taskHash {
		if orig, err := os.ReadFile(m.torrentPath(taskHash + ".orig")); err == nil {
			b = orig
		}
	}
	os.WriteFile(m.torrentPath(taskHash), b, 0600)
	t.EngineRef = ""
	t.Options.Check = true
	t.Options.LastActive = time.Now().Unix()
	m.markDirty(t)
	m.Log(taskHash, "Current source stalled; switched to another torrent with the same content (rechecking downloaded data)")
	m.TaskEvent("task.source_switched", t, map[string]any{"from": from, "to": to})
}
