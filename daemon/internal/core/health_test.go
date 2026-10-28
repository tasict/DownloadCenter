package core

import (
	"testing"
	"time"

	"downloadcenter/internal/engine"
)

// poll moves the clock by d and applies st as a poll that took d.
func poll(m *Manager, task *Task, st *engine.Status, e engine.Engine, d time.Duration, now *time.Time) {
	*now = now.Add(d)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.applyStatus(task, st, e, int64(d/time.Second))
}

// runFor polls a torrent every 10 minutes for d.
func runFor(m *Manager, task *Task, st func() *engine.Status, e engine.Engine, d time.Duration, now *time.Time) {
	for left := d; left > 0; left -= 10 * time.Minute {
		poll(m, task, st(), e, min(left, 10*time.Minute), now)
	}
}

func likelyDeadEvents(m *Manager, hash string) int {
	n := 0
	m.db.QueryRow(`SELECT COUNT(*) FROM events WHERE task_hash = ? AND type = 'task.likely_dead'`, hash).Scan(&n)
	return n
}

// reload stores a task and reads it back as dcd does when it starts.
func reload(t *testing.T, m *Manager, task *Task) *Task {
	if err := m.saveTask(task); err != nil {
		t.Fatal(err)
	}
	back, err := m.loadTask(task.Hash)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.live[task.Hash] = back
	delete(m.stall, task.Hash)
	m.mu.Unlock()
	return back
}

func TestSourceHealthLevels(t *testing.T) {
	const ns, ld = 6 * 3600, 3 * 86400
	hour, day := int64(3600), int64(86400)
	trackers := func(answered, seedsKnown int, anySeeds, unreg bool) *TrackerSummary {
		return &TrackerSummary{Total: 4, Answered: answered, SeedsKnown: seedsKnown, AnySeeds: anySeeds, Unregistered: unreg}
	}
	for _, c := range []struct {
		name    string
		h       *Health
		private bool
		conns   int
		level   string
		reason  string
	}{
		{"connected", &Health{EverConnected: true}, false, 3, HealthOK, ""},
		{"a few minutes", &Health{StalledSecs: 300}, false, 0, HealthOK, ""},
		{"nobody shares, DHT looked", &Health{StalledSecs: 7 * hour, DHTSecs: 7 * hour}, false, 0, HealthNoSharing, "no_seeders"},
		{"found but unreachable", &Health{StalledSecs: 7 * hour, DHTSecs: 7 * hour, DHTFound: 5}, false, 0, HealthSearching, ""},
		{"found but unreachable for days", &Health{StalledSecs: 3 * day, DHTSecs: 3 * day, DHTFound: 5}, false, 0, HealthLikelyDead, "no_seeders"},
		{"not enough evidence", &Health{StalledSecs: 7 * hour}, false, 0, HealthSearching, ""},
		{"not enough evidence for days", &Health{StalledSecs: 3 * day}, false, 0, HealthLikelyDead, "no_seeders"},
		{"private tracker says gone", &Health{StalledSecs: 900, Trackers: trackers(0, 0, false, true)}, true, 0, HealthLikelyDead, "unregistered"},
		{"gone, but another one has seeders", &Health{StalledSecs: 7 * hour, Trackers: trackers(1, 1, true, true)}, false, 0, HealthSearching, ""},
		{"peers lack pieces", &Health{IncompleteSecs: 6 * hour}, false, 3, HealthNoSharing, "incomplete"},
		{"peers lack pieces for days", &Health{IncompleteSecs: 3 * day}, false, 3, HealthLikelyDead, "incomplete"},
		{"private: DHT does not count", &Health{StalledSecs: 7 * hour, DHTFound: 9}, true, 0, HealthSearching, ""},
		{"private: its tracker counts nobody", &Health{StalledSecs: 7 * hour, Trackers: trackers(1, 1, false, false)}, true, 0, HealthNoSharing, "no_seeders"},
		{"tracker answers without counts", &Health{StalledSecs: 7 * hour, Trackers: trackers(2, 0, false, false)}, true, 0, HealthSearching, ""},
		{"a tracker reports seeders", &Health{StalledSecs: 3 * day, DHTSecs: 3 * day, Trackers: trackers(1, 1, true, false)}, false, 0, HealthSearching, ""},
	} {
		level, reason := sourceHealth(c.h, ns, ld, c.private, c.conns)
		if level != c.level || reason != c.reason {
			t.Errorf("%s: %s %q, want %s %q", c.name, level, reason, c.level, c.reason)
		}
	}
}

func TestHealthSettings(t *testing.T) {
	m := testManager(t)
	s := m.Settings()
	if s.Torrent.NoSharingHours != 6 || s.Torrent.LikelyDeadDays != 3 {
		t.Fatalf("defaults %d %d", s.Torrent.NoSharingHours, s.Torrent.LikelyDeadDays)
	}
	for _, c := range []struct{ h, d, wantH, wantD int }{{0, 0, 1, 1}, {200, 40, 168, 30}, {-3, 31, 1, 30}, {168, 1, 168, 1}} {
		s.Torrent.NoSharingHours, s.Torrent.LikelyDeadDays = c.h, c.d
		m.SaveSettings(s)
		got := m.Settings().Torrent
		if got.NoSharingHours != c.wantH || got.LikelyDeadDays != c.wantD {
			t.Errorf("%d h %d d saved as %d %d", c.h, c.d, got.NoSharingHours, got.LikelyDeadDays)
		}
	}
	// A week of hours is longer than a day: the hours count for both
	if ns, ld := healthLimits(BTSettings{NoSharingHours: 168, LikelyDeadDays: 1}); ns != 168*3600 || ld != ns {
		t.Fatalf("limits %d %d", ns, ld)
	}
}

// Changing the thresholds judges the time already counted again.
func TestHealthThresholdChange(t *testing.T) {
	m, f, now := clockManager(t)
	task := magnetTask(m, hashA, 1, StMetadata)
	dht := func() *engine.Status { st := noPeers(hashA); st.DHTUp = true; return st }
	runFor(m, task, dht, f, 3*time.Hour, now)
	if task.SourceHealth != HealthSearching {
		t.Fatalf("3 hours: %s", task.SourceHealth)
	}
	s := m.Settings()
	s.Torrent.NoSharingHours, s.Torrent.LikelyDeadDays = 2, 1
	m.SaveSettings(s)
	poll(m, task, dht(), f, time.Second, now)
	if task.SourceHealth != HealthNoSharing {
		t.Fatalf("after lowering the threshold: %s", task.SourceHealth)
	}
}

// Paused time does not count, a restart keeps the count, a restarted
// engine's old DHT reply is not counted again.
func TestHealthCountsRunningTime(t *testing.T) {
	m, f, now := clockManager(t)
	task := magnetTask(m, hashA, 1, StMetadata)
	paused := func() *engine.Status { return &engine.Status{Ref: hashA, State: engine.Paused, IsMetadata: true} }
	runFor(m, task, func() *engine.Status { return noPeers(hashA) }, f, 2*time.Hour, now)
	runFor(m, task, paused, f, 3*24*time.Hour, now)
	if h := task.Options.Health; h.StalledSecs != 2*3600 || task.SourceHealth != HealthSearching {
		t.Fatalf("after a pause: %d s, %s", h.StalledSecs, task.SourceHealth)
	}
	// Two days, then dcd restarts and it runs another day
	runFor(m, task, func() *engine.Status { return noPeers(hashA) }, f, 46*time.Hour, now)
	task = reload(t, m, task)
	if task.Options.Health.StalledSecs != 2*86400 {
		t.Fatalf("after a restart: %d s", task.Options.Health.StalledSecs)
	}
	runFor(m, task, func() *engine.Status { return noPeers(hashA) }, f, 24*time.Hour, now)
	if h := task.Options.Health; h.StalledSecs != 3*86400 || task.SourceHealth != HealthLikelyDead {
		t.Fatalf("3 days: %d s, %s", h.StalledSecs, task.SourceHealth)
	}

	// DHT replies: counted once, by time
	b := magnetTask(m, hashB, 2, StMetadata)
	reply := func(at int64, peers int) func() *engine.Status {
		return func() *engine.Status {
			st := noPeers(hashB)
			st.DHTUp, st.DHTReplyAt, st.DHTPeers = true, at, peers
			return st
		}
	}
	runFor(m, b, reply(now.Unix(), 5), f, time.Hour, now)
	if h := b.Options.Health; h.DHTFound != 5 || h.DHTSecs != 3600 {
		t.Fatalf("DHT reply: %+v", h)
	}
	// Connected for a moment (the NAS itself, a fake peer): the same period
	st := noPeers(hashB)
	st.Connections = 1
	poll(m, b, st, f, time.Second, now)
	if h := b.Options.Health; h.StalledSecs != 3601 || h.EverConnected {
		t.Fatalf("a moment's connection: %+v", h)
	}
	// Connected for a minute: a new period
	poll(m, b, st, f, time.Minute, now)
	if h := b.Options.Health; h.StalledSecs != 0 || h.DHTFound != 0 || !h.EverConnected || h.LastConnectedAt != now.Unix() {
		t.Fatalf("connected: %+v", h)
	}
	// The engine restarted: it reports no reply, then the old one is not new
	old := b.Options.Health.DHTReplyAt
	runFor(m, b, reply(0, 0), f, time.Hour, now)
	runFor(m, b, reply(old, 5), f, time.Hour, now)
	if h := b.Options.Health; h.DHTFound != 0 {
		t.Fatalf("an old reply counted again: %+v", h)
	}
	runFor(m, b, reply(now.Unix(), 2), f, 10*time.Minute, now)
	if h := b.Options.Health; h.DHTFound != 2 {
		t.Fatalf("a new reply: %+v", h)
	}
}

// Data from a check that just ended is no progress; data while running is.
func TestHealthProgress(t *testing.T) {
	m, f, now := clockManager(t)
	task := magnetTask(m, hashA, 1, StDownloading)
	stalled := func(done int64) *engine.Status {
		return &engine.Status{Ref: hashA, State: engine.Active, Total: 100, Completed: done}
	}
	runFor(m, task, func() *engine.Status { return stalled(10) }, f, time.Hour, now)
	poll(m, task, &engine.Status{Ref: hashA, State: engine.Active, Verifying: true, Total: 100}, f, time.Second, now)
	poll(m, task, stalled(10), f, time.Second, now)
	if h := task.Options.Health; h.StalledSecs < 3600 {
		t.Fatalf("a check reset the count: %+v", h)
	}
	poll(m, task, stalled(20), f, time.Second, now)
	if h := task.Options.Health; h.StalledSecs != 0 || h.LastDataAt != now.Unix() {
		t.Fatalf("progress: %+v", h)
	}
	// Peers that lack pieces
	lacking := func() *engine.Status {
		st := stalled(20)
		st.Connections, st.Availability = 3, 0.82
		return st
	}
	// The first 10 minutes the connection is not a minute old yet
	runFor(m, task, lacking, f, 6*time.Hour+10*time.Minute, now)
	if task.SourceHealth != HealthNoSharing || task.Options.Health.IncompleteSecs != 6*3600 {
		t.Fatalf("lacking pieces: %s %+v", task.SourceHealth, task.Options.Health)
	}
	// Seeding: no health any more
	poll(m, task, &engine.Status{Ref: hashA, State: engine.Active, Seeding: true, Total: 100, Completed: 100}, f, time.Second, now)
	if task.Options.Health != nil || task.SourceHealth != "" {
		t.Fatalf("seeding: %+v %s", task.Options.Health, task.SourceHealth)
	}
}

func TestLikelyDeadEvent(t *testing.T) {
	m, f, now := clockManager(t)
	task := magnetTask(m, hashA, 1, StMetadata)
	runFor(m, task, func() *engine.Status { return noPeers(hashA) }, f, 4*24*time.Hour, now)
	if n := likelyDeadEvents(m, hashA); n != 1 || logCount(m, hashA, "No sources for 72 hours") != 1 {
		t.Fatalf("%d events", n)
	}
	task = reload(t, m, task)
	runFor(m, task, func() *engine.Status { return noPeers(hashA) }, f, time.Hour, now)
	if n := likelyDeadEvents(m, hashA); n != 1 {
		t.Fatalf("after a restart: %d events", n)
	}
	// Back to ok, then dead again: a second event
	st := noPeers(hashA)
	st.Connections = 1
	poll(m, task, st, f, time.Second, now)
	poll(m, task, st, f, time.Minute, now)
	if task.SourceHealth != HealthOK || task.Options.Health.LikelyNotified {
		t.Fatalf("connected: %s %+v", task.SourceHealth, task.Options.Health)
	}
	runFor(m, task, func() *engine.Status { return noPeers(hashA) }, f, 3*24*time.Hour, now)
	if n := likelyDeadEvents(m, hashA); n != 2 {
		t.Fatalf("dead again: %d events", n)
	}
}

// Only torrents without sources for 10 minutes, or whose peers lack
// pieces, have their trackers looked at.
func TestAskTrackers(t *testing.T) {
	m, f, now := clockManager(t)
	a := magnetTask(m, hashA, 1, StMetadata)
	b := magnetTask(m, hashB, 2, StDownloading)
	const hashC = "3333333333333333333333333333333333333333"
	c := magnetTask(m, hashC, 3, StDownloading)
	f.sources[hashA] = []engine.TrackerState{
		{URL: "http://a.example/announce", Contacted: true, Seeds: 0, Leechers: 2},
		{URL: "udp://b.example:6969/announce", Contacted: true, Fails: 3, Message: "Unregistered torrent", Seeds: -1, Leechers: -1},
		{URL: "udp://c.example:6969/announce", Seeds: -1, Leechers: -1},
	}
	runFor(m, a, func() *engine.Status { return noPeers(hashA) }, f, 9*time.Minute, now)
	m.askTrackers()
	if len(f.asked) != 0 {
		t.Fatalf("asked after 9 minutes: %v", f.asked)
	}
	poll(m, a, noPeers(hashA), f, time.Minute, now)
	poll(m, b, &engine.Status{Ref: hashB, State: engine.Active, Connections: 4, DownRate: 9, Total: 100, Completed: 5}, f, time.Minute, now)
	poll(m, c, &engine.Status{Ref: hashC, State: engine.Active, Connections: 2, Availability: 0.5, Total: 100, Completed: 5}, f, time.Minute, now)
	poll(m, c, &engine.Status{Ref: hashC, State: engine.Active, Connections: 2, Availability: 0.5, Total: 100, Completed: 5}, f, time.Minute, now)
	m.askTrackers()
	if len(f.asked) != 2 || f.asked[0] == hashB || f.asked[1] == hashB {
		t.Fatalf("asked %v", f.asked)
	}
	s := a.Options.Health.Trackers
	if s == nil || s.Total != 3 || s.Answered != 1 || s.SeedsKnown != 1 || s.AnySeeds || !s.Unregistered {
		t.Fatalf("summary %+v", s)
	}
	poll(m, a, noPeers(hashA), f, time.Second, now)
	if a.SourceHealth != HealthLikelyDead {
		t.Fatalf("a tracker says unregistered: %s", a.SourceHealth)
	}
	a.Options.PublicTrackers = []string{"udp://c.example:6969/announce"}
	src, err := m.HealthOf(hashA)
	if err != nil || len(src.Trackers) != 3 || src.Trackers[0].Public || !src.Trackers[2].Public || src.Trackers[1].State != TrackerUnregistered || src.Trackers[2].State != TrackerNotContacted ||
		src.Trackers[0].Host != "a.example" || src.DHT == nil || src.Level != HealthLikelyDead || src.Reason != "unregistered" {
		t.Fatalf("sources %+v %v", src, err)
	}
}

func TestFindPeers(t *testing.T) {
	m, f, now := clockManager(t)
	task := magnetTask(m, hashA, 1, StMetadata)
	runFor(m, task, func() *engine.Status { return noPeers(hashA) }, f, 30*time.Minute, now)
	// The automatic search ran at 30 minutes
	*now = now.Add(2 * time.Minute)
	searches := task.PeerSearches
	f.refreshed = nil
	done, reason, _, err := m.FindPeers(hashA)
	if err != nil || !done || reason != "" || len(f.refreshed) != 1 || task.PeerSearches != searches+1 || logCount(m, hashA, "Looked for peers again") != 1 {
		t.Fatalf("find peers: %v %q %v %d", done, reason, err, len(f.refreshed))
	}
	*now = now.Add(10 * time.Second)
	done, reason, wait, _ := m.FindPeers(hashA)
	if done || reason != "too_soon" || wait != 50 || len(f.refreshed) != 1 {
		t.Fatalf("again 10 s later: %v %q %d", done, reason, wait)
	}
	// The next automatic search counts from this one
	runFor(m, task, func() *engine.Status { return noPeers(hashA) }, f, 10*time.Minute, now)
	if len(f.refreshed) != 1 {
		t.Fatalf("automatic search right after: %d", len(f.refreshed))
	}
	// Connected: never interrupted
	st := noPeers(hashA)
	st.Connections = 2
	poll(m, task, st, f, time.Minute, now)
	poll(m, task, st, f, time.Minute, now)
	if done, reason, _, _ := m.FindPeers(hashA); done || reason != "connected" || len(f.refreshed) != 1 {
		t.Fatalf("connected: %v %q", done, reason)
	}
	// Paused
	poll(m, task, &engine.Status{Ref: hashA, State: engine.Paused, IsMetadata: true}, f, time.Minute, now)
	if done, reason, _, _ := m.FindPeers(hashA); done || reason != "not_running" {
		t.Fatalf("paused: %v %q", done, reason)
	}
	// URL tasks
	m.live["4444444444444444444444444444444444444444"] = &Task{Hash: "4444444444444444444444444444444444444444", Kind: KindHTTP, Engine: "builtin", EngineRef: "x", State: StDownloading}
	if _, _, _, err := m.FindPeers("4444444444444444444444444444444444444444"); err != ErrUnsupported {
		t.Fatalf("URL task: %v", err)
	}
}

// Some trackers of the public list report a seeder for any torrent: one
// of them alone does not count, the torrent's own trackers do.
func TestSeedClaims(t *testing.T) {
	pub := []string{"udp://p1.example.org:1/announce", "udp://p2.example.org:1/announce"}
	tr := func(url string, seeds int) engine.TrackerState {
		return engine.TrackerState{URL: url, Contacted: true, Seeds: seeds, Leechers: 1}
	}
	for _, c := range []struct {
		name string
		trs  []engine.TrackerState
		any  bool
	}{
		{"one public tracker", []engine.TrackerState{tr(pub[0], 1), tr(pub[1], 0)}, false},
		{"two public trackers", []engine.TrackerState{tr(pub[0], 1), tr(pub[1], 3)}, true},
		{"its own tracker", []engine.TrackerState{tr("http://own.example.com/announce", 1), tr(pub[1], 0)}, true},
		{"nobody", []engine.TrackerState{tr(pub[0], 0), tr("http://own.example.com/announce", 0)}, false},
	} {
		s := summarize(c.trs, pub, 1)
		if s.AnySeeds != c.any || s.SeedsKnown != len(c.trs) {
			t.Errorf("%s: %+v", c.name, s)
		}
	}
}
