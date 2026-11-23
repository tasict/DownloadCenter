package lt

import (
	"sync"
	"testing"
	"time"

	"downloadcenter/internal/engine"
)

const (
	refA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	refB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	refC = "cccccccccccccccccccccccccccccccccccccccc"
)

func rawTorrent(ref, pieces string, up int) map[string]any {
	t := map[string]any{"infohash": ref, "name": ref[:4], "save_path": "/x", "state": "seeding", "has_metadata": true,
		"total_wanted": 100, "total_wanted_done": 100, "up_rate": up, "num_pieces": 8, "piece_length": 16,
		"files": []any{map[string]any{"index": 0, "path": ref[:4], "size": 100, "done": 100, "priority": 4}}}
	if pieces != "" {
		t["pieces"] = pieces
	}
	return t
}

// dc-bt 1.8 answers only what changed: the adapter keeps the others, their
// bitfield when the pieces did not change, never a stale file list, drops
// what is gone, and asks for everything every minute, after a new
// connection and when a torrent it never had a status of is present.
func TestChangedStatus(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var answer func(req map[string]any) map[string]any
	f := newFake(t, dir, func(req map[string]any) (any, string) {
		switch req["cmd"] {
		case "version":
			return map[string]any{"dcbt": "1.8", "libtorrent": "2.0.15"}, ""
		case "status_all":
			mu.Lock()
			defer mu.Unlock()
			return answer(req), ""
		}
		return map[string]any{}, ""
	})
	defer f.ln.Close()
	e := newEngine("/bin/true", dir)
	now := time.Unix(1_800_000_000, 0)
	e.now = func() time.Time { return now }
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	<-f.reqs // version
	set := func(fn func(req map[string]any) map[string]any) {
		mu.Lock()
		answer = fn
		mu.Unlock()
	}
	poll := func(files bool) (map[string]*engine.Status, map[string]any) {
		t.Helper()
		st, err := e.StatusAllFiles(files)
		if err != nil {
			t.Fatal(err)
		}
		return st, <-f.reqs
	}

	// First poll: everything, file lists included
	set(func(map[string]any) map[string]any {
		return map[string]any{"torrents": []any{rawTorrent(refA, "ff", 0), rawTorrent(refB, "0f", 5)},
			"present": []any{refA, refB}, "full": true}
	})
	st, req := poll(false)
	if req["changed"] != true || req["full"] != true || req["files"] != true {
		t.Fatalf("first request %v", req)
	}
	if len(st) != 2 || st[refA].Bitfield != "ff" || len(st[refA].Files) != 1 {
		t.Fatalf("first answer %+v", st)
	}

	// Only b changed, without its pieces: a keeps its status (no file list),
	// b keeps its bitfield
	set(func(map[string]any) map[string]any {
		return map[string]any{"torrents": []any{rawTorrent(refB, "", 7)}, "present": []any{refA, refB}, "full": false}
	})
	st, req = poll(false)
	if req["full"] != false || req["files"] != false {
		t.Fatalf("second request %v", req)
	}
	if st[refA] == nil || st[refA].Bitfield != "ff" || len(st[refA].Files) != 0 || st[refA].State != engine.Active {
		t.Fatalf("unchanged a %+v", st[refA])
	}
	if st[refB].Bitfield != "0f" || st[refB].UpRate != 7 {
		t.Fatalf("changed b %+v", st[refB])
	}
	// The file lists asked for every 10 s come for what changed only
	st, req = poll(true)
	if req["files"] != true || req["full"] != false || len(st[refA].Files) != 0 || len(st[refB].Files) != 1 {
		t.Fatalf("files: request %v, a %d files, b %d files", req, len(st[refA].Files), len(st[refB].Files))
	}

	// b is gone
	set(func(map[string]any) map[string]any {
		return map[string]any{"torrents": []any{}, "present": []any{refA}, "full": false}
	})
	if st, _ = poll(false); len(st) != 1 || st[refA] == nil {
		t.Fatalf("after b left: %v", st)
	}

	// c is present but was never answered: ask for everything again at once
	set(func(req map[string]any) map[string]any {
		if req["full"] == true {
			return map[string]any{"torrents": []any{rawTorrent(refA, "ff", 0), rawTorrent(refC, "01", 0)},
				"present": []any{refA, refC}, "full": true}
		}
		return map[string]any{"torrents": []any{}, "present": []any{refA, refC}, "full": false}
	})
	st, req = poll(false)
	if req["full"] != false {
		t.Fatalf("not a full request first: %v", req)
	}
	if req = <-f.reqs; req["full"] != true || st[refC] == nil || st[refC].Bitfield != "01" {
		t.Fatalf("missing c: request %v, c %+v", req, st[refC])
	}

	// A minute later: everything again
	set(func(req map[string]any) map[string]any {
		return map[string]any{"torrents": []any{rawTorrent(refA, "ff", 0), rawTorrent(refC, "03", 0)},
			"present": []any{refA, refC}, "full": req["full"] == true}
	})
	now = now.Add(59 * time.Second)
	if _, req = poll(false); req["full"] != false {
		t.Fatalf("full before a minute: %v", req)
	}
	now = now.Add(time.Second)
	if _, req = poll(false); req["full"] != true || req["files"] != true {
		t.Fatalf("not full after a minute: %v", req)
	}

	// A new connection: everything again
	e.disconnect()
	if err := e.connect(); err != nil {
		t.Fatal(err)
	}
	if _, req = poll(false); req["full"] != true {
		t.Fatalf("not full after a new connection: %v", req)
	}
}

// dc-bt 1.7 is asked for everything every time, as before.
func TestChangedOld(t *testing.T) {
	dir := t.TempDir()
	f := newFake(t, dir, func(req map[string]any) (any, string) {
		switch req["cmd"] {
		case "version":
			return map[string]any{"dcbt": "1.7", "libtorrent": "2.0.15"}, ""
		case "status_all":
			return map[string]any{"torrents": []any{rawTorrent(refA, "ff", 0)}}, ""
		}
		return map[string]any{}, ""
	})
	defer f.ln.Close()
	e := newEngine("/bin/true", dir)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	<-f.reqs
	for i := 0; i < 2; i++ {
		st, err := e.StatusAllFiles(false)
		if err != nil || st[refA] == nil {
			t.Fatal(st, err)
		}
		if req := <-f.reqs; req["changed"] != nil || req["full"] != nil {
			t.Fatalf("dc-bt 1.7 asked %v", req)
		}
	}
}

// The memory profile goes to dc-bt with the settings.
func TestSettingsMemoryProfile(t *testing.T) {
	if s := settingsJSON(engine.Global{MemoryProfile: "low"}, ""); s["memory_profile"] != "low" {
		t.Fatalf("low: %v", s["memory_profile"])
	}
	if s := settingsJSON(engine.Global{MemoryProfile: "normal"}, ""); s["memory_profile"] != "normal" {
		t.Fatalf("normal: %v", s["memory_profile"])
	}
}

// A torrent dcd just sent a command for, one that moves and one being
// checked are asked for one by one when a changed answer leaves them out.
func TestChangedWatched(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	single := map[string]map[string]any{}
	asked := []string{}
	f := newFake(t, dir, func(req map[string]any) (any, string) {
		mu.Lock()
		defer mu.Unlock()
		switch req["cmd"] {
		case "version":
			return map[string]any{"dcbt": "1.8", "libtorrent": "2.0.15"}, ""
		case "status":
			ref := req["infohash"].(string)
			asked = append(asked, ref)
			return single[ref], ""
		case "status_all":
			if req["full"] == true {
				a, b := rawTorrent(refA, "ff", 0), rawTorrent(refB, "ff", 0)
				b["moving"] = true
				return map[string]any{"torrents": []any{a, b}, "present": []any{refA, refB}, "full": true}, ""
			}
			return map[string]any{"torrents": []any{}, "present": []any{refA, refB}, "full": false}, ""
		}
		return map[string]any{}, ""
	})
	defer f.ln.Close()
	e := newEngine("/bin/true", dir)
	now := time.Unix(1_800_000_000, 0)
	e.now = func() time.Time { return now }
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	drain := func() {
		for len(f.reqs) > 0 {
			<-f.reqs
		}
	}
	reset := func() []string {
		mu.Lock()
		defer mu.Unlock()
		got := asked
		asked = nil
		return got
	}
	mu.Lock()
	moved := rawTorrent(refB, "ff", 0)
	moved["save_path"] = "/dest"
	single[refB] = moved
	single[refA] = rawTorrent(refA, "ff", 0)
	mu.Unlock()
	e.StatusAllFiles(false) // full: b is moving
	drain()
	reset()
	// b moves: asked for by itself, and its new folder is what dcd gets
	st, _ := e.StatusAllFiles(false)
	if got := reset(); len(got) != 1 || got[0] != refB || st[refB].Dir != "/dest" || st[refB].Moving {
		t.Fatalf("moving torrent: asked %v, b %+v", got, st[refB])
	}
	// b no longer moves, a was not touched: nobody is asked for
	if e.StatusAllFiles(false); len(reset()) != 0 {
		t.Fatal("an idle torrent was asked for")
	}
	// dcd pauses a: asked for by itself for 5 seconds
	e.Pause(refA)
	drain()
	reset()
	e.StatusAllFiles(false)
	if got := reset(); len(got) != 1 || got[0] != refA {
		t.Fatalf("after a command: asked %v", got)
	}
	now = now.Add(6 * time.Second)
	if e.StatusAllFiles(false); len(reset()) != 0 {
		t.Fatal("still asked for after 5 seconds")
	}
}
