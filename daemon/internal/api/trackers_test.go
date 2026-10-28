package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/engine"
	"downloadcenter/internal/i18n"
	"downloadcenter/internal/qts"
	"downloadcenter/internal/trackers"
)

type listPublic struct{ list []string }

func (p listPublic) Current() []string { return p.list }
func (p listPublic) Cached() []string  { return p.list }
func (p listPublic) Tick(bool)         {}

// trackerServer has a torrent engine, a temporary folder and a public list
// of two trackers.
func trackerServer(t *testing.T) *Server {
	s := fixServer(t)
	st := s.M.Settings()
	st.TempDir = t.TempDir()
	s.M.SaveSettings(st)
	s.M.Engines["libtorrent"] = identEngine{}
	s.M.Public = listPublic{[]string{"udp://p1.example.org:1/announce", "udp://p2.example.org:1/announce"}}
	return s
}

func bob() *auth.Principal { return &auth.Principal{User: "bob", Via: "session"} }
func trkAdmin() *auth.Principal {
	return &auth.Principal{User: "admin", Admin: true, Via: "session", AllTasks: true}
}

func magnetOf(t *testing.T, s *Server, owner, hash string) string {
	if _, err := s.M.AddMagnet("magnet:?xt=urn:btih:"+hash, core.AddOptions{Owner: owner, Admin: true, AutoRemove: "default"}); err != nil {
		t.Fatal(err)
	}
	return hash
}

func postTrackers(s *Server, p *auth.Principal, id string, body any) (int, map[string]any) {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/x", bytes.NewReader(b))
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	s.addTrackers(rec, req, p)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func errCode(out map[string]any) (string, string) {
	e, _ := out["error"].(map[string]any)
	c, _ := e["code"].(string)
	m, _ := e["message"].(string)
	return c, m
}

func TestAddTrackersEndpoint(t *testing.T) {
	s := trackerServer(t)
	id := magnetOf(t, s, "bob", "1111111111111111111111111111111111111111")
	two := []string{"udp://a.example.org:6969/announce", "http://b.example.org/announce"}
	if code, out := postTrackers(s, bob(), id, map[string]any{"trackers": two}); code != 200 || out["added"] != 2.0 || out["public_added"] != false {
		t.Fatalf("add: %d %v", code, out)
	}
	if code, out := postTrackers(s, bob(), id, map[string]any{"trackers": two}); code != 200 || out["added"] != 0.0 {
		t.Fatalf("again: %d %v", code, out)
	}
	if code, out := postTrackers(s, bob(), id, map[string]any{"public": true}); code != 200 || out["public_added"] != true {
		t.Fatalf("public: %d %v", code, out)
	}
	code, out := postTrackers(s, bob(), id, map[string]any{"trackers": []string{"udp://c.example.org:1/announce", "wss://tracker.example.org/announce"}})
	if c, m := errCode(out); code != 400 || c != "tracker_invalid" || m != "Not a valid tracker address: wss://tracker.example.org/announce" {
		t.Fatalf("wss: %d %v", code, out)
	}
	if len(s.M.Live(id).Options.Trackers) != 2 {
		t.Fatal("a refused request added trackers")
	}
	code, out = postTrackers(s, bob(), id, map[string]any{"trackers": []string{"http://127.0.0.1:8080/announce"}})
	if c, _ := errCode(out); code != 400 || c != "tracker_not_allowed" {
		t.Fatalf("loopback for a regular user: %d %v", code, out)
	}
	if code, out := postTrackers(s, trkAdmin(), id, map[string]any{"trackers": []string{"http://127.0.0.1:6969/announce"}}); code != 200 || out["added"] != 1.0 {
		t.Fatalf("loopback for an administrator: %d %v", code, out)
	}
	if code, out := postTrackers(s, bob(), id, map[string]any{}); code != 400 {
		t.Fatalf("nothing to add: %d %v", code, out)
	}
	// Someone else's task does not exist for bob
	other := magnetOf(t, s, "carol", "2222222222222222222222222222222222222222")
	if code, _ := postTrackers(s, bob(), other, map[string]any{"public": true}); code != 404 {
		t.Fatalf("other owner: %d", code)
	}
	// A private torrent
	priv := []byte("d8:announce31:https://pt.example.com/announce4:infod6:lengthi1e4:name1:p12:piece lengthi16384e6:pieces20:" + strings.Repeat("a", 20) + "7:privatei1eee")
	res, err := s.M.AddTorrent(priv, core.AddOptions{Owner: "bob", Admin: true, AutoRemove: "default"})
	if err != nil {
		t.Fatal(err)
	}
	code, out = postTrackers(s, bob(), res.ID, map[string]any{"public": true})
	if c, m := errCode(out); code != 409 || c != "private_torrent" || m != "Private torrents only use their own trackers" {
		t.Fatalf("private: %d %v", code, out)
	}
}

func TestBulkPublicTrackers(t *testing.T) {
	s := trackerServer(t)
	a := magnetOf(t, s, "bob", "1111111111111111111111111111111111111111")
	b := magnetOf(t, s, "bob", "2222222222222222222222222222222222222222")
	c := magnetOf(t, s, "carol", "3333333333333333333333333333333333333333")
	priv := []byte("d8:announce31:https://pt.example.com/announce4:infod6:lengthi1e4:name1:p12:piece lengthi16384e6:pieces20:" + strings.Repeat("a", 20) + "7:privatei1eee")
	res, _ := s.M.AddTorrent(priv, core.AddOptions{Owner: "bob", Admin: true, AutoRemove: "default"})
	body, _ := json.Marshal(map[string]any{"ids": []string{a, b, c, res.ID}, "action": "add_public_trackers"})
	rec := httptest.NewRecorder()
	s.bulk(rec, httptest.NewRequest("POST", "/x", bytes.NewReader(body)), bob())
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	// carol's task is not bob's to see: neither counted nor skipped
	if rec.Code != 200 || out["count"] != 2.0 || out["skipped"] != 1.0 {
		t.Fatalf("bulk: %d %v", rec.Code, out)
	}
	if len(s.M.Live(c).Options.PublicTrackers) != 0 || len(s.M.Live(a).Options.PublicTrackers) != 2 {
		t.Fatal("public list on the wrong tasks")
	}
}

func TestTaskTrackerFields(t *testing.T) {
	s := trackerServer(t)
	id := magnetOf(t, s, "bob", "1111111111111111111111111111111111111111")
	j, _ := json.Marshal(s.taskJSON(bob(), s.M.Live(id), false))
	for _, k := range []string{`"peers_known":0`, `"no_peers_since":0`, `"peer_searches":0`, `"metadata_since":0`} {
		if !strings.Contains(string(j), k) {
			t.Errorf("torrent without %s: %s", k, j)
		}
	}
	j, _ = json.Marshal(s.taskJSON(bob(), &core.Task{Hash: strings.Repeat("9", 40), Kind: core.KindHTTP}, false))
	if strings.Contains(string(j), "peers_known") || strings.Contains(string(j), "metadata_since") {
		t.Errorf("URL task with torrent fields: %s", j)
	}
	in := s.M.TrackerInfo(s.M.Live(id))
	if in.Count != 0 || in.PublicMissing != 2 {
		t.Fatalf("tracker info %+v", in)
	}
}

// The public list: reading it, refreshing it, and how settings report it.
func TestPublicTrackerEndpoints(t *testing.T) {
	s := trackerServer(t)
	dir := t.TempDir()
	feed := filepath.Join(dir, "trackers.json")
	os.WriteFile(feed, []byte(`{"schema":1,"trackers":["udp://f1.example.org:1/announce"]}`), 0644)
	os.WriteFile(filepath.Join(s.WebDir, "trackers_feed"), []byte("file://"+feed+"\n"), 0644)
	db := s.M.DB()
	s.Public = trackers.New(db, s.WebDir, "test")
	rec := httptest.NewRecorder()
	s.getSettings(rec, httptest.NewRequest("GET", "/x", nil), trkAdmin())
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	pl, _ := out["public_tracker_list"].(map[string]any)
	if pl["source"] != "builtin" || pl["trackers"] != nil {
		t.Fatalf("settings before: %v", pl)
	}
	in, err := s.Public.Refresh(context.Background())
	if err != nil || in.Source != "feed" || in.Count != 1 {
		t.Fatalf("refresh %+v %v", in, err)
	}
}

// The English of a refused tracker is the template the translations use.
func TestTrackerMessages(t *testing.T) {
	keys, err := os.ReadFile("../i18n/backend-keys.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{
		TrackerMessage("wss://x", trackers.ErrInvalid), TrackerMessage("http://127.0.0.1/", trackers.ErrNotAllowed),
		core.ErrPrivateTrackers.Error(), "Private torrents only use their own trackers", core.ErrTooManyTrackers.Error(),
	} {
		tr := i18n.T("TCH", msg)
		if tr == msg {
			t.Errorf("%q is not translated", msg)
		}
	}
	for _, k := range []string{"Not a valid tracker address: {0}", "This tracker address is not allowed: {0}",
		"No peers for {0} minutes; looking for peers again", "Added {0} trackers", "Added the public tracker list ({0})",
		"This is a private torrent; the trackers added to it were removed"} {
		if !strings.Contains(string(keys), strconv.Quote(k)) {
			t.Errorf("backend-keys.json lacks %q", k)
		}
	}
}

func TestTooManyTrackersError(t *testing.T) {
	if status, code, msg := coreError(core.ErrTooManyTrackers); status != 400 || code != "too_many_trackers" || msg != core.ErrTooManyTrackers.Error() {
		t.Errorf("%d %s %q", status, code, msg)
	}
}

// finderEngine is a torrent engine that knows peers and trackers.
type finderEngine struct{ identEngine }

func (finderEngine) Caps() engine.Caps                  { return engine.Caps{Torrents: true, PeerRefresh: true} }
func (finderEngine) RefreshPeers(string) (bool, error)  { return true, nil }
func (finderEngine) SetTrackers(string, []string) error { return nil }
func (finderEngine) SetGuard(string, bool) error        { return nil }
func (finderEngine) AddTrackers(string, []string) error { return nil }
func (finderEngine) Status(ref string) (*engine.Status, error) {
	return &engine.Status{Ref: ref, State: engine.Active, IsMetadata: true, Availability: -1, DHTUp: true}, nil
}
func (finderEngine) Sources(string) ([]engine.TrackerState, error) {
	return []engine.TrackerState{
		{URL: "udp://p1.example.org:1/announce", Contacted: true, Seeds: 0, Leechers: 3},
		{URL: "https://pt.example.com/announce?passkey=x", Contacted: true, Fails: 2, Message: "Unregistered torrent", Seeds: -1, Leechers: -1},
		{URL: "udp://p2.example.org:1/announce", Seeds: -1, Leechers: -1},
	}, nil
}

// urlEngine takes URL downloads.
type urlEngine struct{ identEngine }

func (urlEngine) Name() string      { return "builtin" }
func (urlEngine) Caps() engine.Caps { return engine.Caps{URLs: true} }

// healthServer answers as the regular user dchbob, whom QTS lets use the app.
func healthServer(t *testing.T) *Server {
	t.Cleanup(qts.StubAppriv(func(args ...string) (string, int, error) {
		if args[0] == "-C" {
			return "Permission Allow", 0, nil
		}
		return "", 0, nil
	}))
	s := trackerServer(t)
	s.M.Engines["libtorrent"] = finderEngine{}
	s.M.URL = urlEngine{}
	s.DevUser = "dchbob"
	return s
}

func TestTaskHealth(t *testing.T) {
	s := healthServer(t)
	id := magnetOf(t, s, "dchbob", "1111111111111111111111111111111111111111")
	s.M.AddTrackers(id, nil, true)
	code, out := call(s, "GET", "/tasks/"+id, "")
	h, _ := out["health"].(map[string]any)
	if code != 200 || h == nil {
		t.Fatalf("health: %d %v", code, out)
	}
	// Not in the engine yet (never polled): nothing found so far, no
	// trackers to show (core's TestAskTrackers covers their states)
	trs, ok := h["trackers"].([]any)
	if !ok || len(trs) != 0 || h["level"] != "ok" || h["waiting_secs"] != 0.0 || h["ever_connected"] != false ||
		h["likely_dead_secs"] != 3*86400.0 || h["no_sharing_secs"] != 6*3600.0 || h["availability"] != -1.0 || h["running"] != false {
		t.Fatalf("health %v", h)
	}
	if d, _ := h["dht"].(map[string]any); d == nil || d["available"] != false || d["found"] != 0.0 {
		t.Errorf("dht %v", h["dht"])
	}
	if task := out["task"].(map[string]any); task["source_health"] != nil {
		t.Errorf("source_health before the first poll %v", task["source_health"])
	}
	// The merged sources keep their name
	if _, ok := out["sources"]; !ok {
		t.Error("sources (merged links) missing")
	}
	// URL tasks have none
	res, err := s.M.AddURL("https://files.example.com/a.iso", core.AddOptions{Owner: "dchbob", Admin: true, AutoRemove: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if _, out := call(s, "GET", "/tasks/"+res.ID, ""); out["health"] != nil || out["task"].(map[string]any)["source_health"] != nil {
		t.Fatalf("URL task: %v", out)
	}
}

func TestFindPeersEndpoint(t *testing.T) {
	s := healthServer(t)
	id := magnetOf(t, s, "dchbob", "1111111111111111111111111111111111111111")
	// Not polled yet: the engine is not looking
	if code, out := call(s, "POST", "/tasks/"+id+"/find-peers", ""); code != 200 || out["refreshed"] != false || out["reason"] != "not_running" {
		t.Fatalf("not running: %d %v", code, out)
	}
	other := magnetOf(t, s, "carol", "2222222222222222222222222222222222222222")
	if code, _ := call(s, "POST", "/tasks/"+other+"/find-peers", ""); code != 404 {
		t.Fatalf("other owner: %d", code)
	}
	res, err := s.M.AddURL("https://files.example.com/a.iso", core.AddOptions{Owner: "dchbob", Admin: true, AutoRemove: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if code, out := call(s, "POST", "/tasks/"+res.ID+"/find-peers", ""); code != 400 {
		t.Fatalf("URL task: %d %v", code, out)
	}
	// A token that may only read
	value, err := s.Auth.CreateToken(&auth.Token{Owner: "dchbob", Name: "ro", Scopes: []string{"tasks:read"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := call(s, "POST", "/tasks/"+id+"/find-peers", "", "Authorization", "Bearer "+value); code != 403 {
		t.Fatalf("tasks:read token: %d", code)
	}
}

// The thresholds are torrent settings.
func TestHealthThresholdSettings(t *testing.T) {
	s := fixServer(t)
	s.DevUser = "admin"
	_, out := call(s, "GET", "/settings", "")
	tor := out["settings"].(map[string]any)["torrent"].(map[string]any)
	if tor["no_sharing_hours"] != 6.0 || tor["likely_dead_days"] != 3.0 {
		t.Fatalf("defaults %v", tor)
	}
	// PUT /settings needs real shared folders; the ranges are core's (TestHealthSettings)
	st := s.M.Settings()
	st.Torrent.NoSharingHours, st.Torrent.LikelyDeadDays = 500, 2
	s.M.SaveSettings(st)
	_, out = call(s, "GET", "/settings", "")
	tor = out["settings"].(map[string]any)["torrent"].(map[string]any)
	if tor["no_sharing_hours"] != 168.0 || tor["likely_dead_days"] != 2.0 {
		t.Fatalf("saved %v", tor)
	}
}
