package lt

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"downloadcenter/internal/engine"
)

// fakeServer answers dc-bt commands on a unix socket and records them.
type fakeServer struct {
	ln   net.Listener
	reqs chan map[string]any
	resp func(req map[string]any) (any, string)
}

func newFake(t *testing.T, dir string, resp func(map[string]any) (any, string)) *fakeServer {
	os.MkdirAll(filepath.Join(dir, "run"), 0700)
	ln, err := net.Listen("unix", filepath.Join(dir, "run", "dc-bt.sock"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeServer{ln: ln, reqs: make(chan map[string]any, 100), resp: resp}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					var req map[string]any
					json.Unmarshal(sc.Bytes(), &req)
					f.reqs <- req
					// an event line first, which the adapter must ignore
					c.Write([]byte(`{"event":"state_changed","infohash":"x","state":"downloading"}` + "\n"))
					res, errs := f.resp(req)
					out := map[string]any{"id": req["id"], "ok": errs == "", "result": res}
					if errs != "" {
						out["error"] = errs
					}
					b, _ := json.Marshal(out)
					c.Write(append(b, '\n'))
				}
			}(c)
		}
	}()
	return f
}

func TestAdapter(t *testing.T) {
	dir := t.TempDir()
	status := map[string]any{
		"infohash": "08ada5a7a6183aae1e09d831df6748d566095a10", "name": "Sintel", "save_path": "/x",
		"state": "seeding", "paused": false, "complete": false, "has_metadata": true, "error": "",
		"total_wanted": 1000, "total_wanted_done": 1000, "all_time_upload": 500, "all_time_download": 1000,
		"down_rate": 0, "up_rate": 20, "peers": 3, "seeds": 1, "pieces": "f0", "num_pieces": 4, "piece_length": 256,
		"files": []any{map[string]any{"index": 0, "path": "Sintel/a.mp4", "size": 1000, "done": 1000, "priority": 4}},
	}
	dcbt := "1.0"
	f := newFake(t, dir, func(req map[string]any) (any, string) {
		switch req["cmd"] {
		case "version":
			return map[string]any{"dcbt": dcbt, "libtorrent": "2.0.15"}, ""
		case "add":
			return map[string]any{"infohash": "08ada5a7a6183aae1e09d831df6748d566095a10", "resumed": true}, ""
		case "status_all":
			return map[string]any{"torrents": []any{status}}, ""
		case "pause":
			if req["infohash"] == "missing" {
				return nil, "torrent not found"
			}
			return map[string]any{}, ""
		}
		return map[string]any{}, ""
	})
	defer f.ln.Close()
	e := newEngine("/bin/true", dir)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	if e.Version() != "2.0.15" {
		t.Fatalf("version %q", e.Version())
	}
	<-f.reqs // version
	// dc-bt 1.0 cannot move data while seeding; 1.1 can
	if e.Caps().MoveWhileSeeding {
		t.Fatal("dc-bt 1.0 must not claim move_while_seeding")
	}
	dcbt = "1.1"
	if err := e.Health(); err != nil {
		t.Fatal(err)
	}
	<-f.reqs
	if !e.Caps().MoveWhileSeeding {
		t.Fatal("dc-bt 1.1 can move data while seeding")
	}
	if err := e.ApplyGlobal(engine.Global{PortFrom: 16951, PortTo: 16959, DHT: true, Proxy: engine.Proxy{Type: "socks5", Host: "p", Port: 1080, ApplyPeers: true, Force: true}}); err != nil {
		t.Fatal(err)
	}
	r := <-f.reqs
	s := r["settings"].(map[string]any)
	if s["listen_from"].(float64) != 16951 || s["proxy"].(map[string]any)["type"] != "socks5" || s["proxy"].(map[string]any)["force"] != true {
		t.Fatalf("settings %v", s)
	}
	ref, err := e.Add("08ada5a7a6183aae1e09d831df6748d566095a10", engine.AddRequest{Torrent: []byte("d4:infod4:name1:xee"), Dir: "/x", Select: []int{0, 2}, Root: "x (1)", Paused: true, SeedTime: -1})
	if err != nil || ref != "08ada5a7a6183aae1e09d831df6748d566095a10" {
		t.Fatal(ref, err)
	}
	r = <-f.reqs
	if r["torrent_b64"] == nil || r["paused"] != true || len(r["select"].([]any)) != 2 || r["seed_time"].(float64) != -1 || r["root"] != "x (1)" {
		t.Fatalf("add %v", r)
	}
	st, err := e.StatusAll()
	if err != nil {
		t.Fatal(err)
	}
	<-f.reqs
	x := st[ref]
	if x == nil || x.State != engine.Active || !x.Seeding || x.Total != 1000 || len(x.Files) != 1 || !x.Files[0].Selected {
		t.Fatalf("status %+v", x)
	}
	// Resumed add: upload carried in the resume data is not counted twice
	if x.Uploaded != 0 {
		t.Fatalf("uploaded %d, want 0 after resumed add", x.Uploaded)
	}
	status["all_time_upload"] = 800
	st, _ = e.StatusAll()
	<-f.reqs
	if st[ref].Uploaded != 300 {
		t.Fatalf("uploaded %d, want 300", st[ref].Uploaded)
	}
	status["complete"] = true
	st, _ = e.StatusAll()
	<-f.reqs
	if st[ref].State != engine.Complete {
		t.Fatalf("complete state %v", st[ref].State)
	}
	status["complete"], status["paused"], status["error"] = false, false, "disk full"
	st, _ = e.StatusAll()
	<-f.reqs
	if st[ref].State != engine.Error || st[ref].ErrorMsg != "disk full" {
		t.Fatalf("error state %+v", st[ref])
	}
	// Unknown torrents: pause is a no-op, the error maps to ErrNotFound
	if err := e.Pause("missing"); err != nil {
		t.Fatal(err)
	}
	<-f.reqs
	// File priorities: plain selection sends no levels, levels are passed
	e.SetFiles(ref, []int{0}, map[int]int{0: 1})
	r = <-f.reqs
	if _, ok := r["priorities"]; ok {
		t.Fatalf("plain selection sent priorities: %v", r)
	}
	e.SetFiles(ref, []int{0, 1}, map[int]int{0: 7, 1: 1})
	r = <-f.reqs
	if p := r["priorities"].(map[string]any); p["0"].(float64) != 7 || p["1"].(float64) != 1 {
		t.Fatalf("priorities %v", r)
	}
	if !e.Caps().Socks5Peers || e.Caps().URLs {
		t.Fatal("caps")
	}
	// Moving the data while seeding
	if err := e.MoveStorage(ref, "/dst", "Sintel (1)"); err != nil {
		t.Fatal(err)
	}
	r = <-f.reqs
	if r["cmd"] != "move" || r["infohash"] != ref || r["save_path"] != "/dst" || r["root"] != "Sintel (1)" {
		t.Fatalf("move %v", r)
	}
	status["error"], status["moving"] = "", true
	st, _ = e.StatusAll()
	<-f.reqs
	if !st[ref].Moving || st[ref].MoveError != "" {
		t.Fatalf("moving %+v", st[ref])
	}
	status["moving"], status["move_error"] = false, "File exists: /dst/Sintel/a.mp4"
	st, _ = e.StatusAll()
	<-f.reqs
	if st[ref].Moving || st[ref].MoveError != "File exists: /dst/Sintel/a.mp4" {
		t.Fatalf("move error %+v", st[ref])
	}
}

func TestSettingsHTTPProxy(t *testing.T) {
	s := settingsJSON(engine.Global{Proxy: engine.Proxy{Type: "http", Host: "h", Port: 3128, ApplyPeers: true, ApplyTrackers: true}}, "")
	px := s["proxy"].(map[string]any)
	if px["peers"] != false || px["trackers"] != true {
		t.Fatalf("http proxy must only carry trackers: %v", px)
	}
	s = settingsJSON(engine.Global{}, "")
	if s["proxy"].(map[string]any)["type"] != "none" || s["listen_from"] != 16891 {
		t.Fatalf("defaults %v", s)
	}
}

func TestOwnIdentity(t *testing.T) {
	for v, want := range map[string][2]string{
		"2.0.15.0": {"-LT20F0-", "libtorrent/2.0.15.0"},
		"2.0.9":    {"-LT2090-", "libtorrent/2.0.9"},
		"2.1.10.1": {"-LT21A1-", "libtorrent/2.1.10.1"},
		"":         {"", ""},
		"dev":      {"", ""},
	} {
		if id, agent := ownIdentity(v); id != want[0] || agent != want[1] {
			t.Errorf("%q -> %q %q", v, id, agent)
		}
	}
}

// The default identity is libtorrent's own; another one is sent as set.
func TestSettingsIdentity(t *testing.T) {
	s := settingsJSON(engine.Global{}, "2.0.15.0")
	if s["peer_id_prefix"] != "-LT20F0-" || s["user_agent"] != "libtorrent/2.0.15.0" {
		t.Errorf("default: %v %v", s["peer_id_prefix"], s["user_agent"])
	}
	s = settingsJSON(engine.Global{PeerID: "-TR2940-", PeerAgent: "Transmission/2.94"}, "2.0.15.0")
	if s["peer_id_prefix"] != "-TR2940-" || s["user_agent"] != "Transmission/2.94" {
		t.Errorf("transmission: %v %v", s["peer_id_prefix"], s["user_agent"])
	}
}

// The version is read from the binary before dc-bt first starts.
func TestBinVersion(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "dc-bt")
	os.WriteFile(bin, []byte("#!/bin/sh\necho \"dc-bt 1.3 libtorrent 2.0.15.0\"\n"), 0755)
	e := newEngine(bin, t.TempDir())
	e.binVersion()
	if id, _ := e.OwnIdentity(); id != "-LT20F0-" {
		t.Errorf("identity %q", id)
	}
}

// A new client identity needs dc-bt to start again: running torrents keep
// the peer id they were added with.
func TestNewIdentity(t *testing.T) {
	dir := t.TempDir()
	f := newFake(t, dir, func(req map[string]any) (any, string) {
		if req["cmd"] == "version" {
			return map[string]any{"dcbt": "1.3", "libtorrent": "2.0.15.0"}, ""
		}
		return map[string]any{}, ""
	})
	defer f.ln.Close()
	e := newEngine("/bin/true", dir)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	if err := e.ApplyGlobal(engine.Global{DHT: true}); err != nil {
		t.Fatalf("first settings: %v", err)
	}
	if err := e.ApplyGlobal(engine.Global{DHT: false}); err != nil {
		t.Fatalf("same identity: %v", err)
	}
	err := e.ApplyGlobal(engine.Global{PeerID: "-TR2940-", PeerAgent: "Transmission/2.94"})
	if err == nil || !strings.Contains(err.Error(), "restart required") {
		t.Fatalf("new identity: %v", err)
	}
	// The running dc-bt keeps the identity it started with until then
	var live map[string]any
	for len(f.reqs) > 0 {
		if r := <-f.reqs; r["cmd"] == "apply_settings" {
			live = r["settings"].(map[string]any)
		}
	}
	if live == nil || live["peer_id_prefix"] != "-LT20F0-" || live["user_agent"] != "libtorrent/2.0.15.0" {
		t.Errorf("sent to the running dc-bt: %v", live)
	}
	// Until dc-bt has started again
	if err := e.ApplyGlobal(engine.Global{PeerID: "-TR2940-", PeerAgent: "Transmission/2.94"}); err == nil {
		t.Fatal("restart forgotten")
	}
	b, _ := os.ReadFile(e.settingsPath())
	if !strings.Contains(string(b), "-TR2940-") {
		t.Errorf("settings file: %s", b)
	}
}
