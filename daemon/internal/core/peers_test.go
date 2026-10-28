package core

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"downloadcenter/internal/engine"
)

// finderBT is the fake torrent engine with dc-bt 1.4's PeerFinder.
type finderBT struct {
	fakeBT
	busy      bool // RefreshPeers finds a connection
	refreshed []string
	set       map[string][]string
	added     map[string][]string
	reqs      []engine.AddRequest
	guarded   map[string]bool
	calls     []string // SetGuard and AddTrackers, in order
	sources   map[string][]engine.TrackerState
	asked     []string // Sources, in order
}

func newFinder() *finderBT {
	return &finderBT{set: map[string][]string{}, added: map[string][]string{}, guarded: map[string]bool{}, sources: map[string][]engine.TrackerState{}}
}

func (f *finderBT) Caps() engine.Caps {
	c := f.fakeBT.Caps()
	c.PeerRefresh = true
	return c
}
func (f *finderBT) RefreshPeers(ref string) (bool, error) {
	f.refreshed = append(f.refreshed, ref)
	return !f.busy, nil
}
func (f *finderBT) SetTrackers(ref string, tr []string) error { f.set[ref] = tr; return nil }
func (f *finderBT) Sources(ref string) ([]engine.TrackerState, error) {
	f.asked = append(f.asked, ref)
	return f.sources[ref], nil
}
func (f *finderBT) SetGuard(ref string, on bool) error {
	f.guarded[ref] = on
	f.calls = append(f.calls, "guard")
	return nil
}
func (f *finderBT) AddTrackers(ref string, tr []string) error {
	f.added[ref] = append(f.added[ref], tr...)
	f.calls = append(f.calls, "trackers")
	return nil
}
func (f *finderBT) Add(hash string, r engine.AddRequest) (string, error) {
	f.reqs = append(f.reqs, r)
	return hash, nil
}

type fakePublic struct{ list []string }

func (p *fakePublic) Current() []string { return p.list }
func (p *fakePublic) Cached() []string  { return p.list }
func (p *fakePublic) Tick(bool)         {}

const (
	hashA = "1111111111111111111111111111111111111111"
	hashB = "2222222222222222222222222222222222222222"
)

// clockManager is a manager with the finder engine and a clock the test moves.
func clockManager(t *testing.T) (*Manager, *finderBT, *time.Time) {
	m := urlManager(t)
	f := newFinder()
	m.Engines["libtorrent"] = f
	now := time.Unix(1_800_000_000, 0)
	m.clock = func() time.Time { return now }
	return m, f, &now
}

func magnetTask(m *Manager, hash string, pos int, state string) *Task {
	t := &Task{Hash: hash, Owner: "dc-test-nobody", Kind: KindBT, Engine: "libtorrent", EngineRef: hash, State: state,
		Position: pos, CreatedAt: int64(pos), Source: "magnet:?xt=urn:btih:" + hash,
		Options: TaskOptions{Magnet: "magnet:?xt=urn:btih:" + hash}}
	m.live[hash] = t
	return t
}

func noPeers(hash string) *engine.Status {
	return &engine.Status{Ref: hash, State: engine.Active, IsMetadata: true}
}

func logCount(m *Manager, hash, prefix string) int {
	n := 0
	for _, l := range m.TaskLog(hash, 1000) {
		if strings.HasPrefix(l.Msg, prefix) {
			n++
		}
	}
	return n
}

func TestLookForPeersAgain(t *testing.T) {
	m, f, now := clockManager(t)
	task := magnetTask(m, hashA, 1, StMetadata)
	step := func(d time.Duration, st *engine.Status) {
		*now = now.Add(d)
		apply(m, task, st, f)
	}
	step(0, noPeers(hashA))
	if task.Options.MetaSince != now.Unix() || task.NoPeersSince != now.Unix() {
		t.Fatalf("waiting since %d / %d", task.Options.MetaSince, task.NoPeersSince)
	}
	step(14*time.Minute, noPeers(hashA))
	if len(f.refreshed) != 0 {
		t.Fatal("refreshed before 15 minutes")
	}
	step(time.Minute, noPeers(hashA))
	if len(f.refreshed) != 1 || task.PeerSearches != 1 || logCount(m, hashA, "No peers for 15 minutes") != 1 {
		t.Fatalf("after 15 minutes: %d refreshes, %d searches", len(f.refreshed), task.PeerSearches)
	}
	step(29*time.Minute, noPeers(hashA))
	step(time.Minute, noPeers(hashA))
	if len(f.refreshed) != 2 {
		t.Fatalf("30 minutes later: %d refreshes", len(f.refreshed))
	}
	// A connection that lasts a minute starts over; a moment's does not
	st := noPeers(hashA)
	st.Connections = 1
	step(time.Second, st)
	if task.NoPeersSince == 0 {
		t.Fatal("a moment's connection started over")
	}
	step(time.Minute, st)
	if task.NoPeersSince != 0 || task.PeerSearches != 0 {
		t.Fatalf("connected: %d %d", task.NoPeersSince, task.PeerSearches)
	}
	step(time.Minute, noPeers(hashA))
	step(14*time.Minute, noPeers(hashA))
	if len(f.refreshed) != 2 {
		t.Fatal("the wait did not start over")
	}
	step(time.Minute, noPeers(hashA))
	if len(f.refreshed) != 3 {
		t.Fatalf("15 minutes after the reset: %d", len(f.refreshed))
	}
	// Paused for an hour: 15 minutes again after resuming
	step(10*time.Minute, &engine.Status{Ref: hashA, State: engine.Paused, IsMetadata: true})
	step(time.Hour, &engine.Status{Ref: hashA, State: engine.Paused, IsMetadata: true})
	step(time.Minute, noPeers(hashA))
	step(14*time.Minute, noPeers(hashA))
	if len(f.refreshed) != 3 {
		t.Fatal("refreshed less than 15 minutes after resuming")
	}
	step(time.Minute, noPeers(hashA))
	if len(f.refreshed) != 4 {
		t.Fatal("not refreshed 15 minutes after resuming")
	}
	// The engine finds a connection meanwhile: no count, a wait before the next try
	f.busy = true
	step(30*time.Minute, noPeers(hashA))
	step(time.Second, noPeers(hashA))
	if len(f.refreshed) != 5 || task.PeerSearches != 1 {
		t.Fatalf("busy: %d calls, %d searches", len(f.refreshed), task.PeerSearches)
	}
	// Connected but no data: never interrupted
	f.busy = false
	f.refreshed = nil
	for i := 0; i < 30; i++ {
		st := &engine.Status{Ref: hashA, State: engine.Active, Connections: 2, Total: 100}
		step(time.Minute, st)
	}
	// Seeding with nobody downloading: left alone
	for i := 0; i < 60; i++ {
		step(time.Minute, &engine.Status{Ref: hashA, State: engine.Active, Seeding: true, Total: 100, Completed: 100})
	}
	if len(f.refreshed) != 0 {
		t.Fatalf("refreshed a connected or seeding torrent %d times", len(f.refreshed))
	}
}

// Twelve hours without peers: 8 searches, at most 2 log lines.
func TestLookForPeersLog(t *testing.T) {
	m, f, now := clockManager(t)
	task := magnetTask(m, hashA, 1, StMetadata)
	for i := 0; i <= 12*60; i++ {
		apply(m, task, noPeers(hashA), f)
		*now = now.Add(time.Minute)
	}
	if len(f.refreshed) != 8 || logCount(m, hashA, "No peers for") != 2 {
		t.Fatalf("%d searches, %d log lines", len(f.refreshed), logCount(m, hashA, "No peers for"))
	}
}

func TestStalledTorrentYieldsSlot(t *testing.T) {
	m, f, now := clockManager(t)
	s := m.Settings()
	s.BT.MaxNum = 1
	m.SaveSettings(s)
	a := magnetTask(m, hashA, 1, StMetadata)
	b := magnetTask(m, hashB, 2, StQueued)
	sched := func() {
		m.mu.Lock()
		m.schedule(*now)
		m.mu.Unlock()
	}
	sched()
	if !m.running[hashA] || m.running[hashB] {
		t.Fatal("the first magnet holds the slot")
	}
	apply(m, a, noPeers(hashA), f)
	*now = now.Add(14 * time.Minute)
	apply(m, a, noPeers(hashA), f)
	sched()
	if m.running[hashB] {
		t.Fatal("yielded before 15 minutes")
	}
	*now = now.Add(time.Minute)
	apply(m, a, noPeers(hashA), f)
	sched()
	if !m.running[hashA] || !m.running[hashB] || a.State != StMetadata {
		t.Fatalf("after 15 minutes: a %v (%s), b %v", m.running[hashA], a.State, m.running[hashB])
	}
	apply(m, b, noPeers(hashB), f)
	if r := m.QueueRanks(); len(r) != 0 {
		t.Fatalf("queue ranks %v", r)
	}
	// Queue moves agree with the scheduler: b keeps its slot in front of a
	started, stopped, err := m.MoveNear([]string{hashB}, hashA, false, nil)
	if err != nil || len(started)+len(stopped) != 0 {
		t.Fatalf("move: %v %v %v", started, stopped, err)
	}
	m.mu.Lock()
	b.Position, a.Position = 2, 1
	m.mu.Unlock()
	// a connects: it is first in the queue, b waits again
	b.State = StDownloading
	st := noPeers(hashA)
	st.Connections = 1
	apply(m, a, st, f)
	*now = now.Add(time.Minute)
	apply(m, a, st, f)
	sched()
	if !m.running[hashA] || m.running[hashB] || b.State != StQueued {
		t.Fatalf("after a connects: a %v, b %v (%s)", m.running[hashA], m.running[hashB], b.State)
	}
	// b paused in the engine: it starts over too
	apply(m, b, &engine.Status{Ref: hashB, State: engine.Paused, IsMetadata: true}, f)
	if m.stall[hashB] != nil && !m.stall[hashB].since.IsZero() {
		t.Fatal("a paused torrent is not waiting for peers")
	}
}

func TestPrivateMagnetDropsExtraTrackers(t *testing.T) {
	m, f, _ := clockManager(t)
	own := "https://pt.example.com/announce"
	link := "magnet:?xt=urn:btih:" + hashA + "&tr=" + own
	a := magnetTask(m, hashA, 1, StMetadata)
	a.Options.Magnet, a.Source = link, link
	a.Options.Trackers = []string{"udp://merged.example.org:1/announce"}
	a.Options.PublicTrackers = []string{"udp://public.example.org:1/announce"}
	apply(m, a, noPeers(hashA), f)
	if a.Options.MetaSince == 0 {
		t.Fatal("meta_since not set")
	}
	apply(m, a, &engine.Status{Ref: hashA, State: engine.Active, Private: true, Total: 100}, f)
	if got := f.set[hashA]; len(got) != 1 || got[0] != own {
		t.Fatalf("set_trackers %q", got)
	}
	if a.Options.MetaSince != 0 || a.Options.Trackers != nil || a.Options.PublicTrackers != nil ||
		logCount(m, hashA, "This is a private torrent") != 1 {
		t.Fatalf("after the metadata: %+v", a.Options)
	}
	// Later polls change nothing
	apply(m, a, &engine.Status{Ref: hashA, State: engine.Active, Private: true, Total: 100}, f)
	if logCount(m, hashA, "This is a private torrent") != 1 {
		t.Fatal("logged twice")
	}

	// Public torrent: the trackers stay
	b := magnetTask(m, hashB, 2, StMetadata)
	b.Options.PublicTrackers = []string{"udp://public.example.org:1/announce"}
	apply(m, b, &engine.Status{Ref: hashB, State: engine.Active, Total: 100}, f)
	if f.set[hashB] != nil || len(b.Options.PublicTrackers) != 1 {
		t.Fatal("public torrent lost its trackers")
	}
	// Private, but the extra trackers are its own (after the dialog's probe)
	c := magnetTask(m, "3333333333333333333333333333333333333333", 3, StMetadata)
	c.Options.Magnet = "magnet:?xt=urn:btih:" + c.Hash + "&tr=" + own
	c.Options.Trackers = []string{own}
	apply(m, c, &engine.Status{Ref: c.Hash, State: engine.Active, Private: true, Total: 100}, f)
	if f.set[c.Hash] != nil || c.Options.Trackers != nil || logCount(m, c.Hash, "This is a private torrent") != 0 {
		t.Fatal("own trackers are not extra ones")
	}
}

func TestAddTrackers(t *testing.T) {
	m, f, _ := clockManager(t)
	pub := &fakePublic{list: []string{"udp://p1.example.org:1/announce", "udp://p2.example.org:1/announce"}}
	m.Public = pub
	o := AddOptions{Owner: "admin", Admin: true, AutoRemove: "default"}
	own := "udp://own.example.org:1/announce"
	res, err := m.AddMagnet("magnet:?xt=urn:btih:"+hashA+"&tr="+own, o)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.live[res.ID].EngineRef = res.ID
	m.mu.Unlock()
	got, err := m.AddTrackers(hashA, []string{"udp://new.example.org:1/announce", " ", own, "udp://new.example.org:1/announce"}, false)
	if err != nil || got.Added != 1 || got.PublicAdded {
		t.Fatalf("add %+v %v", got, err)
	}
	if got, _ := m.AddTrackers(hashA, []string{"udp://new.example.org:1/announce"}, false); got.Added != 0 {
		t.Fatal("added a tracker twice")
	}
	got, err = m.AddTrackers(hashA, nil, true)
	if err != nil || !got.PublicAdded || len(m.Live(hashA).Options.PublicTrackers) != 2 || len(f.added[hashA]) != 3 {
		t.Fatalf("public list %+v %v %q", got, err, f.added[hashA])
	}
	if got, _ := m.AddTrackers(hashA, nil, true); got.PublicAdded {
		t.Fatal("public list added twice")
	}
	if in := m.TrackerInfo(m.Live(hashA)); in.Count != 4 || in.PublicMissing != 0 {
		t.Fatalf("tracker info %+v", in)
	}
	pub.list = append(pub.list, "udp://p3.example.org:1/announce")
	if in := m.TrackerInfo(m.Live(hashA)); in.PublicMissing != 1 {
		t.Fatalf("after the list grew %+v", in)
	}
	if got, _ := m.AddTrackers(hashA, nil, true); !got.PublicAdded || len(m.Live(hashA).Options.PublicTrackers) != 3 {
		t.Fatal("the new tracker of the list was not added")
	}
	if n := logCount(m, hashA, "Added 1 trackers"); n != 1 {
		t.Fatalf("log lines %d", n)
	}
	// Private torrents, URL tasks, unknown tasks
	priv := testTorrent("https://pt.example.com/announce", true, "")
	m.AddTorrent(priv, o)
	if _, err := m.AddTrackers(infoHash(t, priv), []string{"udp://x.example.org:1/announce"}, false); err != ErrPrivateTrackers {
		t.Fatalf("private: %v", err)
	}
	m.mu.Lock()
	m.live[hashB] = &Task{Hash: hashB, Kind: KindHTTP, Owner: "admin"}
	m.mu.Unlock()
	if _, err := m.AddTrackers(hashB, nil, true); err != ErrUnsupported {
		t.Fatalf("URL task: %v", err)
	}
	if _, err := m.AddTrackers("4444444444444444444444444444444444444444", nil, true); err != ErrNotFound {
		t.Fatalf("unknown: %v", err)
	}
}

func TestAutoPublicTrackers(t *testing.T) {
	m, f, _ := clockManager(t)
	m.Public = &fakePublic{list: []string{"udp://p1.example.org:1/announce", "udp://own.example.org:1/announce"}}
	o := AddOptions{Owner: "admin", Admin: true, AutoRemove: "default"}
	// Off by default
	m.AddMagnet("magnet:?xt=urn:btih:"+hashA, o)
	if len(m.Live(hashA).Options.PublicTrackers) != 0 {
		t.Fatal("public list added while off")
	}
	s := m.Settings()
	s.Torrent.PublicTrackers = true
	m.SaveSettings(s)
	m.AddMagnet("magnet:?xt=urn:btih:"+hashB+"&tr=udp://own.example.org:1/announce", o)
	if p := m.Live(hashB).Options.PublicTrackers; len(p) != 1 || p[0] != "udp://p1.example.org:1/announce" {
		t.Fatalf("magnet got %q", p)
	}
	// Another link of the same torrent: merged, the list is not added again
	m.AddMagnet("magnet:?xt=urn:btih:"+hashB+"&tr=udp://p1.example.org:1/announce", o)
	if tr := m.Live(hashB).Options.Trackers; len(tr) != 0 {
		t.Fatalf("merged a tracker of the list again: %q", tr)
	}
	priv := testTorrent("https://pt.example.com/announce", true, "")
	m.AddTorrent(priv, o)
	if p := m.Live(infoHash(t, priv)).Options.PublicTrackers; len(p) != 0 {
		t.Fatalf("private torrent got %q", p)
	}
	pubT := testTorrent("https://a.example.com/announce", false, "")
	m.AddTorrent(pubT, o)
	if p := m.Live(infoHash(t, pubT)).Options.PublicTrackers; len(p) != 2 {
		t.Fatalf("torrent got %q", p)
	}
	// The engine gets them, and a regular user's torrent is guarded
	m.mu.Lock()
	m.live[infoHash(t, pubT)].Owner = "dc-test-nobody"
	req, err := m.buildAdd(m.live[infoHash(t, pubT)])
	m.mu.Unlock()
	if err != nil || len(req.Trackers) != 2 || !req.Guard {
		t.Fatalf("engine request %+v %v", req, err)
	}
	// So is the add dialog's probe of a regular user
	m.ProbeMagnet("magnet:?xt=urn:btih:5555555555555555555555555555555555555555", "dc-test-nobody", false)
	if r := f.reqs[len(f.reqs)-1]; !r.MetadataOnly || !r.Guard {
		t.Fatalf("probe %+v", r)
	}
}

// The public list's names come from the internet: a torrent with it is
// guarded whoever owns it, before the first announce to them. Changes of
// role or list reach the engine within a minute.
func TestGuardFollowsListAndRole(t *testing.T) {
	m, f, _ := clockManager(t)
	m.Public = &fakePublic{list: []string{"udp://p1.example.org:1/announce"}}
	task := magnetTask(m, hashA, 1, StMetadata)
	task.Owner = "dc-test-nobody"
	m.mu.Lock()
	req, _ := m.buildAdd(task)
	m.guards[hashA] = false // as if added before its owner lost the role
	m.mu.Unlock()
	if !req.Guard {
		t.Fatal("a regular user's torrent is guarded")
	}
	m.syncGuards()
	if !f.guarded[hashA] || !m.guards[hashA] {
		t.Fatal("the guard did not follow the owner's role")
	}
	// Public list on a task that has no guard: guard first, then the trackers
	m.mu.Lock()
	m.guards[hashA] = false
	m.mu.Unlock()
	f.calls, f.guarded = nil, map[string]bool{}
	if _, err := m.AddTrackers(hashA, nil, true); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.calls, ",") != "guard,trackers" || !f.guarded[hashA] {
		t.Fatalf("calls %q", f.calls)
	}
	m.mu.Lock()
	req, _ = m.buildAdd(m.live[hashA])
	m.mu.Unlock()
	if !req.Guard {
		t.Fatal("a torrent with the public list is guarded")
	}
}

func TestAddedTrackerLimit(t *testing.T) {
	m, _, _ := clockManager(t)
	magnetTask(m, hashA, 1, StMetadata)
	var many []string
	for i := 0; i < MaxAddedTrackers; i++ {
		many = append(many, "udp://t"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+itoa(i)+".example.org:1/announce")
	}
	if got, err := m.AddTrackers(hashA, many, false); err != nil || got.Added != MaxAddedTrackers {
		t.Fatalf("up to the limit: %+v %v", got, err)
	}
	if _, err := m.AddTrackers(hashA, []string{"udp://one-more.example.org:1/announce"}, false); err != ErrTooManyTrackers {
		t.Fatalf("over the limit: %v", err)
	}
	// Another link of the same torrent adds none beyond it either
	m.mu.Lock()
	m.mergeTrackers(m.live[hashA], []string{"udp://merged.example.org:1/announce"}, "")
	n := len(m.live[hashA].Options.Trackers)
	m.mu.Unlock()
	if n != MaxAddedTrackers {
		t.Fatalf("%d trackers after a merge", n)
	}
}

// A regular user's probe never shows someone else's task.
func TestProbeOtherOwner(t *testing.T) {
	m, _, _ := clockManager(t)
	task := magnetTask(m, hashA, 1, StDownloading)
	task.Owner = "carol"
	if _, _, err := m.ProbeMagnet("magnet:?xt=urn:btih:"+hashA, "bob", false); err != ErrOtherOwner {
		t.Fatalf("someone else's task: %v", err)
	}
	if _, _, err := m.ProbeMagnet("magnet:?xt=urn:btih:"+hashA, "carol", false); err == ErrOtherOwner {
		t.Fatal("the owner's own task")
	}
	if _, _, err := m.ProbeMagnet("magnet:?xt=urn:btih:"+hashA, "admin", true); err == ErrOtherOwner {
		t.Fatal("an administrator sees every task")
	}
}

func itoa(n int) string { return fmt.Sprint(n) }
