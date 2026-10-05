package core

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"downloadcenter/internal/torrent"
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

// testTorrent is a one-file torrent announcing to tracker; private sets the
// BEP 27 flag and salt (the "source" key) changes the info hash only.
func testTorrent(tracker string, private bool, salt string) []byte {
	info := "d6:lengthi16384e4:name5:movie12:piece lengthi16384e6:pieces20:" + strings.Repeat("a", 20)
	if private {
		info += "7:privatei1e"
	}
	if salt != "" {
		info += "6:source" + strconv.Itoa(len(salt)) + ":" + salt
	}
	return []byte("d8:announce" + strconv.Itoa(len(tracker)) + ":" + tracker + "4:info" + info + "ee")
}

func btManager(t *testing.T) *Manager {
	m := urlManager(t)
	m.Engines["libtorrent"] = &fakeBT{}
	return m
}

func infoHash(t *testing.T, b []byte) string {
	meta, err := torrent.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return meta.InfoHash
}

// Another link of a private torrent adds no trackers; a public one still does.
func TestPrivateTorrentNotMerged(t *testing.T) {
	m := btManager(t)
	o := AddOptions{Owner: "admin", Admin: true, AutoRemove: "default"}
	priv := testTorrent("https://pt.example.com/announce?passkey=1", true, "")
	if _, err := m.AddTorrent(priv, o); err != nil {
		t.Fatal(err)
	}
	id := infoHash(t, priv)
	res, err := m.AddTorrent(testTorrent("https://pt.example.com/announce?passkey=2", true, ""), o)
	var dup *DupError
	if !errors.As(err, &dup) || dup.ID != id || res == nil || !res.Duplicate {
		t.Fatalf("same private torrent: %v %+v", err, res)
	}
	res, err = m.AddMagnet("magnet:?xt=urn:btih:"+id+"&tr=udp%3A%2F%2Fpublic.example.com%3A1337", o)
	if !errors.As(err, &dup) || res.Merged {
		t.Fatalf("magnet of a private torrent: %v %+v", err, res)
	}
	if tr := m.Live(id).Options.Trackers; len(tr) != 0 {
		t.Errorf("trackers added to a private torrent: %v", tr)
	}
	pub := testTorrent("https://a.example.com/announce", false, "")
	m.AddTorrent(pub, o)
	res, err = m.AddTorrent(testTorrent("https://b.example.com/announce", false, ""), o)
	if err != nil || !res.Merged || len(m.Live(infoHash(t, pub)).Options.Trackers) != 1 {
		t.Errorf("public torrent: %v %+v", err, res)
	}
}

// A private torrent never becomes another source, nor gets one.
func TestPrivateNotContentSource(t *testing.T) {
	m := btManager(t)
	o := AddOptions{Owner: "admin", Admin: true, AutoRemove: "default"}
	pub := testTorrent("https://a.example.com/announce", false, "")
	m.AddTorrent(pub, o)
	o.ContentOf = infoHash(t, pub)
	if _, err := m.AddTorrent(testTorrent("https://pt.example.com/announce", true, "PT"), o); err != ErrPrivate {
		t.Errorf("private as a source: %v", err)
	}
	o.ContentOf = ""
	priv := testTorrent("https://pt.example.com/announce", true, "")
	m.AddTorrent(priv, o)
	o.ContentOf = infoHash(t, priv)
	if _, err := m.AddTorrent(testTorrent("https://a.example.com/announce", false, "other"), o); err != ErrPrivate {
		t.Errorf("source for a private task: %v", err)
	}
	if len(m.Sources(infoHash(t, priv))) != 0 || len(m.Sources(infoHash(t, pub))) != 0 {
		t.Error("sources recorded")
	}
	if !m.TorrentPrivate(infoHash(t, priv)) || m.TorrentPrivate(infoHash(t, pub)) {
		t.Error("TorrentPrivate")
	}
}

// The duplicate check says when a private torrent is involved.
func TestCheckPrivate(t *testing.T) {
	m := btManager(t)
	o := AddOptions{Owner: "admin", Admin: true, AutoRemove: "default"}
	priv := testTorrent("https://pt.example.com/announce", true, "")
	m.AddTorrent(priv, o)
	r := m.Check("admin", true, []CheckItem{
		{Source: "a.torrent", Torrent: priv},
		{Source: "magnet:?xt=urn:btih:" + infoHash(t, priv)},
		{Source: "b.torrent", Torrent: testTorrent("https://a.example.com/announce", false, "x")},
		{Source: "c.torrent", Torrent: testTorrent("https://a.example.com/announce", false, "")},
	}, "")
	if r[0].Status != "same_torrent" || !r[0].Private || r[1].Status != "same_torrent" || !r[1].Private {
		t.Errorf("same private torrent: %+v %+v", r[0], r[1])
	}
	if r[2].Status != "same_content" || !r[2].Private {
		t.Errorf("same files as a private task: %+v", r[2])
	}
	if r[3].Status != "same_content" || !r[3].Private {
		t.Errorf("public torrent, private task: %+v", r[3])
	}
	m2 := btManager(t)
	m2.AddTorrent(testTorrent("https://a.example.com/announce", false, ""), o)
	r = m2.Check("admin", true, []CheckItem{{Source: "p.torrent", Torrent: testTorrent("https://pt.example.com/announce", true, "")}}, "")
	if r[0].Status != "same_content" || !r[0].Private {
		t.Errorf("private torrent, public task: %+v", r[0])
	}
	r = m2.Check("admin", true, []CheckItem{{Source: "q.torrent", Torrent: testTorrent("https://a.example.com/announce", false, "y")}}, "")
	if r[0].Private {
		t.Errorf("public only: %+v", r[0])
	}
}
