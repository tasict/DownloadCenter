// Package notify delivers events: built-in channels (Telegram, Discord,
// LINE, Slack, ntfy, Gotify, Bark, QTS Notification Center), signed
// webhooks, declarative adapters, and the chat command endpoint.
package notify

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"downloadcenter/internal/api"
	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/netutil"
	"downloadcenter/internal/qts"
	"downloadcenter/internal/store"
)

// Retry delays after the first failed attempt (INTEGRATION.md §4).
var retryDelays = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour}

// maxFailures consecutive failed attempts disable a channel.
const maxFailures = 20

// Service is the notification service.
type Service struct {
	m    *core.Manager
	au   *auth.Service
	db   *store.DB
	data string

	now     func() time.Time
	client  func(guard bool) *http.Client
	isAdmin func(user string) bool

	mu      sync.Mutex
	lists   map[string]listCtx // command numbering per caller
	confirm map[string]string  // pending /del confirmations per caller
	pollers map[string]chan struct{}

	kick   chan struct{}
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// New creates the service without starting the worker (tests).
func New(m *core.Manager, au *auth.Service, data string) *Service {
	s := &Service{
		m: m, au: au, db: m.DB(), data: data,
		now:     time.Now,
		lists:   map[string]listCtx{},
		confirm: map[string]string{},
		pollers: map[string]chan struct{}{},
		kick:    make(chan struct{}, 1),
		stopCh:  make(chan struct{}),
	}
	s.client = s.defaultClient
	s.isAdmin = s.ownerAdmin
	s.db.Exec(`CREATE TABLE IF NOT EXISTS notify_digest (channel_id TEXT NOT NULL, event_id INTEGER NOT NULL, PRIMARY KEY (channel_id, event_id))`)
	s.db.Exec(`CREATE TABLE IF NOT EXISTS notify_digest_sent (delivery TEXT PRIMARY KEY, event_ids TEXT NOT NULL)`)
	return s
}

// Register adds the notification routes and starts the worker; the
// returned func stops it.
func Register(srv *api.Server, m *core.Manager, au *auth.Service, data string) (stop func()) {
	s := New(m, au, data)
	s.routes(srv)
	s.Start()
	return s.Stop
}

// Start runs the event subscriber, the delivery loop and the chat pollers.
func (s *Service) Start() {
	ch, cancel := s.m.Subscribe()
	s.wg.Add(3)
	go func() {
		defer s.wg.Done()
		defer cancel()
		for {
			select {
			case <-s.stopCh:
				return
			case e, ok := <-ch:
				if !ok {
					return
				}
				s.safe(func() { s.Dispatch(e) })
			}
		}
	}()
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-s.stopCh:
				return
			case <-t.C:
			case <-s.kick:
			}
			s.safe(func() { s.ProcessDeliveries() })
		}
	}()
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		s.safe(s.reconcilePollers)
		for {
			select {
			case <-s.stopCh:
				return
			case <-t.C:
			}
			s.safe(func() { s.flushDigests() })
			s.safe(s.reconcilePollers)
		}
	}()
}

// Stop ends every goroutine.
func (s *Service) Stop() {
	select {
	case <-s.stopCh:
		return
	default:
	}
	close(s.stopCh)
	s.mu.Lock()
	for id, c := range s.pollers {
		close(c)
		delete(s.pollers, id)
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Service) safe(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("notify: panic: %v", r)
		}
	}()
	fn()
}

func (s *Service) wake() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// ownerAdmin reports whether a channel owner is an administrator.
func (s *Service) ownerAdmin(user string) bool {
	return auth.IsAdmin(user)
}

// onList reports whether a channel owner may still use Download Center.
func (s *Service) onList(user string) bool {
	return s.au.Allowed(user, s.isAdmin(user))
}

// defaultClient is the outbound HTTP client: internal addresses are refused
// for non-admin owners; the proxy is the one chosen for notifications.
func (s *Service) defaultClient(guard bool) *http.Client {
	return netutil.Client(10*time.Second, guard, s.m.NotifyProxy())
}

func (s *Service) uiURL() string {
	base := strings.TrimRight(s.m.Settings().ExternalURL, "/")
	if base == "" {
		return ""
	}
	return base + "/DownloadCenter/"
}

// --- dispatch ---

// Dispatch queues an event for every channel that wants it.
func (s *Service) Dispatch(e core.Event) {
	if e.Type == "" {
		return
	}
	for _, ch := range s.loadChannels(`enabled = 1`) {
		if !s.wants(ch, e) {
			continue
		}
		if ch.Digest > 0 && e.Type != "notify.disabled" {
			s.db.X(`INSERT OR IGNORE INTO notify_digest (channel_id, event_id) VALUES (?, ?)`, ch.ID, e.ID)
			continue
		}
		s.queue(ch, e.ID, newDeliveryID(), s.releaseTime(ch))
	}
	s.wake()
}

// wants applies the event filter and the owner's visibility.
func (s *Service) wants(ch *Channel, e core.Event) bool {
	if !s.onList(ch.Owner) {
		return false
	}
	if e.Type == "notify.disabled" {
		return e.Owner == ch.Owner
	}
	if len(ch.Events) > 0 {
		ok := false
		for _, t := range ch.Events {
			if t == e.Type || t == "*" {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	admin := s.isAdmin(ch.Owner)
	return core.Visible(e, ch.Owner, admin, admin && ch.Scope == "all")
}

// releaseTime is when a delivery may go out (the end of quiet hours).
func (s *Service) releaseTime(ch *Channel) int64 {
	now := s.now()
	if end, quiet := QuietUntil(ch.Quiet, now); quiet {
		return end.Unix()
	}
	return now.Unix()
}

// QuietUntil reports whether t falls in the "HH:MM-HH:MM" window (local
// time, may span midnight) and when the window ends.
func QuietUntil(spec string, t time.Time) (time.Time, bool) {
	a, b, ok := strings.Cut(strings.TrimSpace(spec), "-")
	if !ok {
		return time.Time{}, false
	}
	start, ok1 := parseHM(a)
	end, ok2 := parseHM(b)
	if !ok1 || !ok2 || start == end {
		return time.Time{}, false
	}
	m := t.Hour()*60 + t.Minute()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	at := func(d time.Time, mins int) time.Time { return d.Add(time.Duration(mins) * time.Minute) }
	if start < end {
		if m >= start && m < end {
			return at(day, end), true
		}
		return time.Time{}, false
	}
	// Spans midnight, e.g. 22:00-07:00
	if m >= start {
		return at(day.AddDate(0, 0, 1), end), true
	}
	if m < end {
		return at(day, end), true
	}
	return time.Time{}, false
}

func parseHM(s string) (int, bool) {
	h, mi, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return 0, false
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(mi)
	if err1 != nil || err2 != nil || hh < 0 || hh > 24 || mm < 0 || mm > 59 {
		return 0, false
	}
	return hh*60 + mm, true
}

func (s *Service) queue(ch *Channel, eventID int64, delivery string, at int64) {
	s.db.X(`INSERT INTO deliveries (channel_id, event_id, delivery, attempt, next_at, status, time) VALUES (?, ?, ?, 0, ?, 'pending', ?)`,
		ch.ID, eventID, delivery, at, s.now().Unix())
}

func newDeliveryID() string { return strings.TrimPrefix(auth.RandomID("d", 24), "d") }

// flushDigests turns collected events into one summary per channel when
// the channel's digest interval has passed since the oldest one.
func (s *Service) flushDigests() {
	for _, ch := range s.loadChannels(`enabled = 1 AND digest > 0`) {
		rows, err := s.db.Query(`SELECT event_id FROM notify_digest WHERE channel_id = ? ORDER BY event_id`, ch.ID)
		if err != nil {
			continue
		}
		var ids []int64
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		if len(ids) == 0 {
			continue
		}
		first := s.eventByID(ids[0])
		if first != nil {
			if ts, err := time.Parse(time.RFC3339, first.Time); err == nil && s.now().Sub(ts) < time.Duration(ch.Digest)*time.Minute {
				continue
			}
		}
		d := "digest-" + newDeliveryID()
		b, _ := json.Marshal(ids)
		s.db.X(`INSERT INTO notify_digest_sent (delivery, event_ids) VALUES (?, ?)`, d, string(b))
		s.db.X(`DELETE FROM notify_digest WHERE channel_id = ? AND event_id <= ?`, ch.ID, ids[len(ids)-1])
		s.queue(ch, -1, d, s.releaseTime(ch))
	}
	s.wake()
}

func (s *Service) eventByID(id int64) *core.Event {
	if id <= 0 {
		return nil
	}
	ev := s.m.Events(id-1, 1)
	if len(ev) == 1 && ev[0].ID == id {
		return &ev[0]
	}
	return nil
}

// --- delivery ---

type delivery struct {
	ID       int64
	Channel  string
	EventID  int64
	Delivery string
	Attempt  int
}

// ProcessDeliveries sends every due delivery.
func (s *Service) ProcessDeliveries() {
	now := s.now().Unix()
	rows, err := s.db.Query(`SELECT id, channel_id, event_id, delivery, attempt FROM deliveries WHERE status = 'pending' AND next_at <= ? ORDER BY id LIMIT 100`, now)
	if err != nil {
		return
	}
	var due []delivery
	for rows.Next() {
		var d delivery
		rows.Scan(&d.ID, &d.Channel, &d.EventID, &d.Delivery, &d.Attempt)
		due = append(due, d)
	}
	rows.Close()
	for _, d := range due {
		select {
		case <-s.stopCh:
			return
		default:
		}
		s.deliver(d)
	}
}

func (s *Service) deliver(d delivery) {
	ch := s.channel(d.Channel)
	if ch == nil || !ch.Enabled {
		s.db.X(`UPDATE deliveries SET status = 'cancelled' WHERE id = ?`, d.ID)
		return
	}
	var msg *Message
	switch {
	case d.EventID == -1:
		msg = s.digestMessage(ch, d.Delivery)
	default:
		e := s.eventByID(d.EventID)
		if e == nil {
			s.db.X(`UPDATE deliveries SET status = 'cancelled', error = 'event expired' WHERE id = ?`, d.ID)
			return
		}
		msg = s.message(ch, *e)
	}
	if msg == nil {
		s.db.X(`UPDATE deliveries SET status = 'cancelled' WHERE id = ?`, d.ID)
		return
	}
	msg.Delivery = d.Delivery
	start := time.Now()
	status, err := s.send(ch, msg)
	dur := time.Since(start).Milliseconds()
	attempt := d.Attempt + 1
	if err == nil {
		s.db.X(`UPDATE deliveries SET status = 'ok', attempt = ?, http_status = ?, duration_ms = ?, error = '', time = ? WHERE id = ?`,
			attempt, status, dur, s.now().Unix(), d.ID)
		s.db.X(`UPDATE channels SET fail_count = 0 WHERE id = ? AND fail_count != 0`, ch.ID)
		s.trim(ch.ID)
		return
	}
	errText := truncate(err.Error(), 300)
	final := attempt > len(retryDelays)
	if final {
		s.db.X(`UPDATE deliveries SET status = 'failed', attempt = ?, http_status = ?, duration_ms = ?, error = ?, time = ? WHERE id = ?`,
			attempt, status, dur, errText, s.now().Unix(), d.ID)
	} else {
		next := s.now().Add(retryDelays[attempt-1]).Unix()
		s.db.X(`UPDATE deliveries SET attempt = ?, http_status = ?, duration_ms = ?, error = ?, next_at = ?, time = ? WHERE id = ?`,
			attempt, status, dur, errText, next, s.now().Unix(), d.ID)
	}
	s.db.X(`UPDATE channels SET fail_count = fail_count + 1 WHERE id = ?`, ch.ID)
	var fails int
	s.db.QueryRow(`SELECT fail_count FROM channels WHERE id = ?`, ch.ID).Scan(&fails)
	if fails >= maxFailures {
		s.db.X(`UPDATE channels SET enabled = 0 WHERE id = ?`, ch.ID)
		s.db.X(`UPDATE deliveries SET status = 'cancelled' WHERE channel_id = ? AND status = 'pending'`, ch.ID)
		s.reconcilePollers()
		log.Printf("notify: channel %s disabled after %d failures", ch.ID, fails)
		s.m.Emit(core.Event{Type: "notify.disabled", Owner: ch.Owner, Data: map[string]any{"channel": ch.Name, "service": ch.Service, "error": errText}})
	}
	s.trim(ch.ID)
}

// trim keeps the last 50 finished deliveries of a channel.
func (s *Service) trim(channelID string) {
	s.db.X(`DELETE FROM deliveries WHERE channel_id = ? AND status != 'pending' AND id NOT IN
		(SELECT id FROM deliveries WHERE channel_id = ? AND status != 'pending' ORDER BY id DESC LIMIT 50)`, channelID, channelID)
}

func (s *Service) digestMessage(ch *Channel, d string) *Message {
	var raw string
	if s.db.QueryRow(`SELECT event_ids FROM notify_digest_sent WHERE delivery = ?`, d).Scan(&raw) != nil {
		return nil
	}
	var ids []int64
	json.Unmarshal([]byte(raw), &ids)
	var events []core.Event
	for _, id := range ids {
		if e := s.eventByID(id); e != nil {
			events = append(events, *e)
		}
	}
	if len(events) == 0 {
		return nil
	}
	lines := make([]string, 0, len(events))
	for i, e := range events {
		if i >= 30 {
			lines = append(lines, fmt.Sprintf("…還有 %d 則", len(events)-30))
			break
		}
		lines = append(lines, "• "+Title(e))
	}
	return &Message{
		Type:   "digest",
		Title:  fmt.Sprintf("Download Center 彙整：%d 則通知", len(events)),
		Body:   strings.Join(lines, "\n"),
		Events: events,
		Vars:   Vars{"event.type": "digest", "event.title": fmt.Sprintf("Download Center 彙整：%d 則通知", len(events)), "event.body": strings.Join(lines, "\n"), "nas.name": qts.Hostname(), "ui_url": s.uiURL(), "owner": ch.Owner},
	}
}

// Message is what a channel sends for one event (or a digest).
type Message struct {
	Type     string
	Title    string
	Body     string
	Event    *core.Event
	Events   []core.Event
	Vars     Vars
	Buttons  []Button
	Delivery string
}

// Button is a chat action (Telegram inline keyboard, LINE quick reply).
type Button struct {
	Label   string `json:"label"`
	Command string `json:"command"`
}

// Text is the plain-text form of a message.
func (m *Message) Text() string {
	if m.Body == "" {
		return m.Title
	}
	return m.Title + "\n" + m.Body
}

func (s *Service) message(ch *Channel, e core.Event) *Message {
	msg := &Message{Type: e.Type, Title: Title(e), Body: Body(e), Event: &e, Vars: EventVars(e, s.uiURL(), nil)}
	if ch.Template != "" {
		msg.Body = Render(ch.Template, msg.Vars, nil)
	}
	if e.Task != nil && ch.Operate {
		short := e.Task.ID
		if len(short) > 8 {
			short = short[:8]
		}
		switch e.Type {
		case "task.failed":
			msg.Buttons = []Button{{"重試", "/retry " + short}}
		case "task.added", "task.started", "task.resumed":
			msg.Buttons = []Button{{"暫停", "/pause " + short}}
		case "task.paused":
			msg.Buttons = []Button{{"繼續", "/resume " + short}}
		}
	}
	return msg
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
