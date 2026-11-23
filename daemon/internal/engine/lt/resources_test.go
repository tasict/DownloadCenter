package lt

import (
	"testing"
	"time"

	"downloadcenter/internal/engine"
)

// finishedSecs is the seeding time dc-bt measures the seeding time limit by.
func finishedSecs(t *testing.T, e *Engine, ref string) int64 {
	t.Helper()
	var r struct {
		FinishedSecs int64 `json:"finished_secs"`
	}
	if err := e.call("status", map[string]any{"infohash": ref}, &r); err != nil {
		t.Fatal(err)
	}
	return r.FinishedSecs
}

// A torrent waiting for a seeding slot is paused in dc-bt: the time does not
// count towards its seeding time, and the time it seeded survives a restart.
func TestRealSeedTimeExcludesPause(t *testing.T) {
	d := utScratch(t)
	e, dir := utStart(t, "")
	a := utRandom(3 * utPiece)
	tor, hash := utTorrent(t, "Clock", []utFile{{"a.bin", a}}, utSeeder(t, a))
	utPut(t, d.dl, "Clock/a.bin", a)
	ref, err := e.Add(hash, engine.AddRequest{Torrent: tor, Dir: d.dl, Check: true})
	if err != nil {
		t.Fatal(err)
	}
	e.Resume(ref)
	utSeeding(t, e, ref)
	time.Sleep(3 * time.Second)
	e.Pause(ref)
	time.Sleep(1500 * time.Millisecond) // a pause takes effect on the next tick
	before := finishedSecs(t, e, ref)
	if before < 2 {
		t.Fatalf("seeded for %d s after 3 s", before)
	}
	time.Sleep(4 * time.Second)
	if paused := finishedSecs(t, e, ref); paused != before {
		t.Fatalf("seeding time went on while paused: %d -> %d", before, paused)
	}
	e.Resume(ref)
	time.Sleep(3 * time.Second)
	after := finishedSecs(t, e, ref)
	if after < before+2 {
		t.Fatalf("seeding time stood still after resuming: %d -> %d", before, after)
	}

	// After a restart the seeding time goes on from where it was
	e.SaveState()
	e.Stop()
	e2, _ := utStart(t, dir)
	ref2, err := e2.Add(hash, engine.AddRequest{Torrent: tor, Dir: d.dl})
	if err != nil {
		t.Fatal(err)
	}
	if got := finishedSecs(t, e2, ref2); got < after {
		t.Fatalf("seeding time after a restart: %d, was %d", got, after)
	}
}

// tuned reads what dc-bt says its session is tuned for.
func tuned(t *testing.T, e *Engine) (profile string, session map[string]float64) {
	t.Helper()
	var v struct {
		Hardware struct {
			Profile string             `json:"memory_profile"`
			Session map[string]float64 `json:"session"`
		} `json:"hardware"`
	}
	if err := e.call("version", nil, &v); err != nil {
		t.Fatal(err)
	}
	return v.Hardware.Profile, v.Hardware.Session
}

// dc-bt 1.8 takes the low memory profile with the settings, at start and
// when they change, and libtorrent has the values.
func TestRealMemoryProfile(t *testing.T) {
	bin := dcbtBin(t)
	dir := t.TempDir()
	e := newEngine(bin, dir)
	g := engine.Global{PortFrom: 26891, PortTo: 26899, MaxConn: 100, MemoryProfile: "low"}
	e.ApplyGlobal(g)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Stop() })
	p, s := tuned(t, e)
	low := map[string]float64{"active_checking": 1, "alert_queue_size": 2000, "max_peer_recv_buffer_size": 256 << 10,
		"send_buffer_watermark": 128 << 10, "max_peerlist_size": 500, "file_pool_size": 10, "connections_limit": 100}
	if p != "low" {
		t.Fatalf("profile %q", p)
	}
	for k, want := range low {
		if s[k] != want {
			t.Errorf("low: %s = %v, want %v", k, s[k], want)
		}
	}
	if s["max_queued_disk_bytes"] > 1<<20 || s["checking_mem_usage"] > 256 {
		t.Errorf("low: disk queue %v, checking %v", s["max_queued_disk_bytes"], s["checking_mem_usage"])
	}
	// Back to normal: 1.7's values
	g.MemoryProfile, g.MaxConn = "normal", 300
	if err := e.ApplyGlobal(g); err != nil {
		t.Fatal(err)
	}
	p, s = tuned(t, e)
	normal := map[string]float64{"active_checking": 2, "alert_queue_size": 10000, "max_peer_recv_buffer_size": 2 << 20,
		"send_buffer_watermark": 500 << 10, "max_peerlist_size": 3000, "file_pool_size": 40, "connections_limit": 300}
	if p != "normal" {
		t.Fatalf("profile %q", p)
	}
	for k, want := range normal {
		if s[k] != want {
			t.Errorf("normal: %s = %v, want %v", k, s[k], want)
		}
	}
}

// same compares what dcd uses of a merged changed answer with a full one.
func same(t *testing.T, what string, merged, full map[string]*engine.Status) {
	t.Helper()
	if len(merged) != len(full) {
		t.Fatalf("%s: %d torrents merged, %d in full", what, len(merged), len(full))
	}
	for ref, f := range full {
		m := merged[ref]
		if m == nil {
			t.Fatalf("%s: %s missing", what, ref)
		}
		if m.State != f.State || m.Seeding != f.Seeding || m.Completed != f.Completed || m.Total != f.Total ||
			m.Bitfield != f.Bitfield || m.Dir != f.Dir || m.Moving != f.Moving || m.Verifying != f.Verifying ||
			m.Uploaded != f.Uploaded || m.Name != f.Name || m.ErrorMsg != f.ErrorMsg {
			t.Fatalf("%s: %s differs:\nmerged %+v\nfull   %+v", what, ref, m, f)
		}
	}
}

// fullStatus asks dc-bt 1.7's way, which leaves the changed answers alone.
func fullStatus(t *testing.T, e *Engine) map[string]*engine.Status {
	t.Helper()
	var res struct {
		Torrents []rawStatus `json:"torrents"`
	}
	if err := e.call("status_all", map[string]any{"files": false}, &res); err != nil {
		t.Fatal(err)
	}
	out := map[string]*engine.Status{}
	for i := range res.Torrents {
		s := e.convert(&res.Torrents[i])
		out[s.Ref] = s
	}
	return out
}

// The statuses merged from changed answers are what full answers say,
// through a download, its completion, the hand-over, pause, resume and
// removal; a torrent that seeds without peers is not in the changed answers.
func TestRealChangedStatus(t *testing.T) {
	d := utScratch(t)
	e, _ := utStart(t, "")
	check := func(what string) {
		t.Helper()
		merged, err := e.StatusAllFiles(false)
		if err != nil {
			t.Fatal(err)
		}
		same(t, what, merged, fullStatus(t, e))
	}
	// An idle seed: its data is there, no tracker, no peers
	idle := utRandom(2 * utPiece)
	itor, ihash := utTorrent(t, "Idle", []utFile{{"i.bin", idle}}, "")
	utPut(t, d.stage, "Idle/i.bin", idle)
	iref, err := e.Add(ihash, engine.AddRequest{Torrent: itor, Dir: d.stage, Check: true})
	if err != nil {
		t.Fatal(err)
	}
	e.Resume(iref)
	utSeeding(t, e, iref)
	check("idle seed")

	// A download from a seeder, polled the way dcd polls
	data := utRandom(6 * utPiece)
	tor, hash := utTorrent(t, "Show", []utFile{{"a.bin", data[:3*utPiece]}, {"b.bin", data[3*utPiece:]}}, utSeeder(t, data))
	ref, err := e.Add(hash, engine.AddRequest{Torrent: tor, Dir: d.stage})
	if err != nil {
		t.Fatal(err)
	}
	e.Resume(ref)
	// (the data changes between two answers: compared once it is complete)
	utWait(t, "download", 30*time.Second, func() bool {
		if _, err := e.StatusAllFiles(false); err != nil {
			t.Fatal(err)
		}
		st, _ := e.Status(ref)
		return st != nil && st.Seeding && st.Completed == st.Total
	})
	time.Sleep(1500 * time.Millisecond)
	check("complete")

	// Handed over to the destination
	if st := utMove(t, e, ref, d.dl, "Show"); st.MoveError != "" {
		t.Fatalf("hand-over: %s", st.MoveError)
	}
	time.Sleep(1500 * time.Millisecond)
	check("handed over")

	e.Pause(ref)
	time.Sleep(1500 * time.Millisecond)
	check("paused")
	e.Resume(ref)
	time.Sleep(1500 * time.Millisecond)
	check("resumed")

	// The idle seed is not among the changed torrents any more
	var res struct {
		Torrents []rawStatus `json:"torrents"`
		Present  []string    `json:"present"`
		Full     bool        `json:"full"`
	}
	time.Sleep(1500 * time.Millisecond)
	e.call("status_all", map[string]any{"changed": true, "files": false}, &res)
	for _, r := range res.Torrents {
		if r.Infohash == ihash {
			t.Fatalf("an idle seed in a changed answer (full %v): %+v", res.Full, r)
		}
	}
	if len(res.Present) != 2 || res.Full {
		t.Fatalf("present %v full %v", res.Present, res.Full)
	}

	if err := e.Remove(ref); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	check("removed")
	if st, _ := e.StatusAllFiles(false); st[ref] != nil {
		t.Fatal("a removed torrent is still reported")
	}
}
