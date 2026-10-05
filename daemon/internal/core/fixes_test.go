package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// cookieResolver answers like a file-host resolver.
type cookieResolver struct{ r *Resolved }

func (c cookieResolver) Match(string) (string, bool) { return "cookies", true }
func (c cookieResolver) Resolve(owner, accountID, raw, proxy string) (*Resolved, error) {
	return c.r, nil
}

func urlManager(t *testing.T) *Manager {
	t.Helper()
	m := testManager(t)
	m.URL = &fakeBT{}
	s := m.Settings()
	s.TempDir = t.TempDir()
	if err := m.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	return m
}

// A cookie account keeps the link and only adds its cookies: the download
// must carry them.
func TestCookieAccountSendsCookies(t *testing.T) {
	m := urlManager(t)
	link := "https://files.example.com/a.iso"
	m.Hosters = cookieResolver{&Resolved{URL: link, Headers: []string{"Cookie: sid=1"}}}
	res, err := m.AddURL(link, AddOptions{Owner: "admin", Admin: true, AutoRemove: "default"})
	if err != nil {
		t.Fatal(err)
	}
	o := m.live[res.ID].Options
	if o.Hoster != "cookies" || o.Direct != link || len(o.DirectHeaders) != 1 || o.DirectHeaders[0] != "Cookie: sid=1" {
		t.Errorf("cookies not kept: hoster %q direct %q headers %v", o.Hoster, o.Direct, o.DirectHeaders)
	}
	// A result that neither changes the link nor adds anything is a plain download
	plain := "https://files.example.com/b.iso"
	m.Hosters = cookieResolver{&Resolved{URL: plain}}
	res, err = m.AddURL(plain, AddOptions{Owner: "admin", Admin: true, AutoRemove: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if o := m.live[res.ID].Options; o.Hoster != "" || o.Direct != "" {
		t.Errorf("plain link taken as a file host: %q %q", o.Hoster, o.Direct)
	}
}

// seedingBT records the seeding targets it is given.
type seedingBT struct {
	fakeBT
	calls []string
}

func (s *seedingBT) SetSeeding(ref string, ratio float64, minutes int) error {
	s.calls = append(s.calls, ref)
	return nil
}

// Changed seeding targets reach the torrents that are already in the engine.
func TestSeedingTargetsReachRunningTorrents(t *testing.T) {
	m := testManager(t)
	bt := &seedingBT{}
	m.Engines["libtorrent"] = bt
	m.live[stageHash] = &Task{Hash: stageHash, Kind: KindBT, Engine: "libtorrent", EngineRef: stageHash, State: StSeeding, CreatedAt: 1}
	s := m.Settings()
	s.Torrent.SeedRatio = s.Torrent.SeedRatio + 1
	if err := m.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if len(bt.calls) != 1 || bt.calls[0] != stageHash {
		t.Fatalf("seeding targets sent to %v", bt.calls)
	}
	// Other settings leave the targets alone
	s.DiskLowMB++
	m.SaveSettings(s)
	if len(bt.calls) != 1 {
		t.Errorf("seeding targets sent again: %v", bt.calls)
	}
}

func scheduleEvents(m *Manager) []Event {
	var out []Event
	for _, e := range m.Events(0, 500) {
		if e.Type == "schedule.changed" {
			out = append(out, e)
		}
	}
	return out
}

// schedule.changed is sent when the schedule switches the mode, once per
// switch, and once when the settings change it.
func TestScheduleChangedOnSwitch(t *testing.T) {
	m := testManager(t)
	s := m.Settings()
	s.Schedule.Enabled = true
	for i := range s.Schedule.Days {
		s.Schedule.Days[i] = strings.Repeat("0", 24)
	}
	if err := m.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	tickOnce := func() {
		m.mu.Lock()
		m.schedule(time.Now())
		m.mu.Unlock()
	}
	tickOnce()
	if ev := scheduleEvents(m); len(ev) != 1 || ev[0].Data["by"] != "settings" {
		t.Fatalf("after saving: %v", ev)
	}
	// The mode the last tick saw was full: the schedule switches it off
	m.mu.Lock()
	m.schedMode = "full"
	m.mu.Unlock()
	tickOnce()
	tickOnce()
	ev := scheduleEvents(m)
	if len(ev) != 2 || ev[1].Data["by"] != "schedule" || ev[1].Data["mode"] != "off" {
		t.Errorf("after the switch: %v", ev)
	}
}

// Links to .torrent files are fetched as torrents; anything else is not,
// and regular users cannot reach local addresses this way.
func TestFetchTorrentLink(t *testing.T) {
	const tor = "d4:infod6:lengthi1e4:name1:a12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaaee"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(tor))
	}))
	defer srv.Close()
	m := testManager(t)
	if b := m.FetchTorrentLink(srv.URL+"/x.torrent", "", true); string(b) != tor {
		t.Errorf("torrent link: %q", b)
	}
	if b := m.FetchTorrentLink(srv.URL+"/x.iso", "", true); b != nil {
		t.Error("a plain file was taken for a torrent")
	}
	if b := m.FetchTorrentLink(srv.URL+"/x.torrent", "", false); b != nil {
		t.Error("a regular user reached a local address")
	}
}

// The public address is the NAS's: dcd adds the package path, so a pasted
// address of the Download Center page is cut back.
func TestExternalURLNormalised(t *testing.T) {
	m := testManager(t)
	for in, want := range map[string]string{
		"https://nas.example.com/DownloadCenter/": "https://nas.example.com",
		"https://nas.example.com:8081/":           "https://nas.example.com:8081",
		"https://nas.example.com":                 "https://nas.example.com",
		"":                                        "",
	} {
		s := m.Settings()
		s.ExternalURL = in
		if err := m.SaveSettings(s); err != nil {
			t.Fatal(err)
		}
		if got := m.Settings().ExternalURL; got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

// queueManager has three torrent slots, three torrents downloading (a, b and
// bob's c), two waiting (d, e) and one URL slot held by u behind a paused p.
func queueManager(t *testing.T) *Manager {
	t.Helper()
	m := testManager(t)
	m.Engines["libtorrent"] = &fakeBT{}
	m.URL = &fakeBT{}
	s := m.Settings()
	s.BT.MaxNum, s.HTTP.MaxNum = 3, 1
	if err := m.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	add := func(id, owner, kind, state string, pos int, paused bool) {
		m.live[id] = &Task{Hash: id, Name: "task " + id, Owner: owner, Kind: kind, State: state, Position: pos, EngineRef: id, UserPaused: paused, CreatedAt: int64(pos)}
	}
	add("a", "alice", KindBT, StDownloading, 1, false)
	add("b", "alice", KindBT, StDownloading, 2, false)
	add("c", "bob", KindBT, StDownloading, 3, false)
	add("d", "alice", KindBT, StQueued, 4, false)
	add("p", "alice", KindHTTP, StPaused, 5, true)
	add("e", "alice", KindBT, StQueued, 6, false)
	add("u", "alice", KindHTTP, StDownloading, 7, false)
	return m
}

func order(m *Manager) string {
	var ids []string
	for _, t := range m.queueOrder() {
		ids = append(ids, t.Hash)
	}
	return strings.Join(ids, "")
}

func hashes(ts []*Task) string {
	var ids []string
	for _, t := range ts {
		ids = append(ids, t.Hash)
	}
	return strings.Join(ids, "")
}

func TestMoveNear(t *testing.T) {
	m := queueManager(t)
	// A waiting torrent before the last one downloading takes its slot
	started, stopped, err := m.MoveNear([]string{"e"}, "c", false, nil)
	if err != nil || order(m) != "abecdpu" || hashes(started) != "e" || hashes(stopped) != "c" {
		t.Fatalf("before c: %v %s started %s stopped %s", err, order(m), hashes(started), hashes(stopped))
	}
	// Back behind the slots: c gets its slot back
	if started, stopped, _ = m.MoveNear([]string{"e"}, "d", true, nil); order(m) != "abcdepu" || hashes(started) != "c" || hashes(stopped) != "e" {
		t.Fatalf("after d: %s %s %s", order(m), hashes(started), hashes(stopped))
	}
	// A block keeps its order; tasks of another kind keep their slots
	if _, _, err = m.MoveNear([]string{"u", "a"}, "e", true, nil); err != nil || order(m) != "bcdeaup" {
		t.Fatalf("block: %v %s", err, order(m))
	}
	if _, _, err = m.MoveNear([]string{"a"}, "a", true, nil); err != ErrBadPosition {
		t.Errorf("anchor is the task itself: %v", err)
	}
}

// A regular user cannot use, or learn about, other users' tasks
func TestMoveNearVisibility(t *testing.T) {
	m := queueManager(t)
	mine := func(o *Task) bool { return o.Owner == "alice" }
	if _, _, err := m.MoveNear([]string{"d"}, "c", false, mine); err != ErrNotFound {
		t.Errorf("anchor of another user: %v", err)
	}
	// Bob's torrent loses its slot, but alice is not told which task it was
	started, stopped, err := m.MoveNear([]string{"e"}, "a", false, mine)
	if err != nil || hashes(started) != "e" || hashes(stopped) != "" || order(m) != "eabcdpu" {
		t.Errorf("%v started %s stopped %s order %s", err, hashes(started), hashes(stopped), order(m))
	}
}

func TestQueueRanks(t *testing.T) {
	m := queueManager(t)
	r := m.QueueRanks()
	if r["d"] != 1 || r["e"] != 2 || r["a"] != 0 || r["p"] != 0 || len(r) != 2 {
		t.Errorf("ranks %v", r)
	}
}
