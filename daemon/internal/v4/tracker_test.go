package v4

import (
	"path/filepath"
	"testing"

	"downloadcenter/internal/api"
	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/engine"
	"downloadcenter/internal/store"
)

// btStub is a torrent engine that never gets called (tasks are not started).
type btStub struct{ engine.Engine }

func (btStub) Name() string                    { return "libtorrent" }
func (btStub) Version() string                 { return "2.0.15.0" }
func (btStub) Caps() engine.Caps               { return engine.Caps{Torrents: true} }
func (btStub) ApplyGlobal(engine.Global) error { return nil }

type stubPublic struct{}

func (stubPublic) Current() []string { return []string{"udp://p.example.org:1/announce"} }
func (stubPublic) Cached() []string  { return []string{"udp://p.example.org:1/announce"} }
func (stubPublic) Tick(bool)         {}

func trackerService(t *testing.T) *service {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "dc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	m := core.New(db, dir)
	st := m.Settings()
	st.TempDir = t.TempDir()
	m.SaveSettings(st)
	m.Engines["libtorrent"] = btStub{}
	m.Public = stubPublic{}
	return &service{srv: api.New(m, auth.New(db), dir, "test"), m: m}
}

func TestAddTracker(t *testing.T) {
	s := trackerService(t)
	o := core.AddOptions{Owner: "bob", Admin: true, AutoRemove: "default"}
	a, _ := s.m.AddMagnet("magnet:?xt=urn:btih:1111111111111111111111111111111111111111", o)
	o.Owner = "carol"
	c, _ := s.m.AddMagnet("magnet:?xt=urn:btih:3333333333333333333333333333333333333333", o)
	bob := &auth.Principal{User: "bob", Via: "session"}
	call := func(p params) result {
		return s.addTracker(&call{s: s, p: p, who: bob})
	}
	if res := call(params{"hash": {a.ID}, "public": {"1"}}); res["added"] != 1 || res["skipped"] != 0 {
		t.Fatalf("public list: %v", res)
	}
	if res := call(params{"hash": {a.ID}, "url": {"udp://x.example.org:1/announce"}}); res["added"] != 1 {
		t.Fatalf("one tracker: %v", res)
	}
	// Refused addresses skip the task
	if res := call(params{"hash": {a.ID}, "url": {"http://127.0.0.1:8080/announce"}}); res["added"] != 0 || res["skipped"] != 1 {
		t.Fatalf("loopback: %v", res)
	}
	if res := call(params{"hash": {a.ID}}); res["error"] != errParameter {
		t.Fatalf("nothing to add: %v", res)
	}
	if res := call(params{"hash": {"4444444444444444444444444444444444444444"}, "public": {"1"}}); res["error"] != errTaskNotFound {
		t.Fatalf("unknown: %v", res)
	}
	if res := call(params{"hash": {c.ID}, "public": {"1"}}); res["error"] != errNotOwner {
		t.Fatalf("someone else's: %v", res)
	}
	if len(s.m.Live(c.ID).Options.PublicTrackers) != 0 {
		t.Fatal("changed someone else's task")
	}
}

func TestConfigPublicTrackers(t *testing.T) {
	s := trackerService(t)
	admin := &auth.Principal{User: "admin", Admin: true, Via: "session", AllTasks: true}
	get := func() any { return s.configShape(&call{s: s, who: admin})["bt"].(map[string]any)["public_trackers"] }
	if get() != 0 {
		t.Fatal("off by default")
	}
	s.configSet(&call{s: s, who: admin, p: params{"bt_public_trackers": {"1"}}})
	if get() != 1 {
		t.Fatal("not turned on")
	}
	// The official form does not send it: unchanged
	s.configSet(&call{s: s, who: admin, p: params{"bt_dht": {"1"}, "bt_max_num": {"4"}}})
	if get() != 1 || !s.m.Settings().Torrent.PublicTrackers {
		t.Fatal("the official form turned it off")
	}
}
