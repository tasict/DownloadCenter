package core

import (
	"fmt"
	"testing"
	"time"

	"downloadcenter/internal/engine"
)

// limitBT records the per-task limits the scheduler applies.
type limitBT struct {
	*finderBT
	up map[string]int64
}

func (l *limitBT) SetLimits(ref string, down, up int64) error {
	l.up[ref] = up
	return nil
}

func seedHash(i int) string { return fmt.Sprintf("%040x", 0xa0+i) }

// seedTask is a torrent that finished and seeds.
func seedTask(m *Manager, i int) *Task {
	h := seedHash(i)
	t := &Task{Hash: h, Owner: "dc-test-nobody", Kind: KindBT, Engine: "libtorrent", EngineRef: h, State: StSeeding,
		Position: i, CreatedAt: int64(i), Source: "x.torrent", Size: 100, DoneBytes: 100, FinishedAt: 1}
	m.live[h] = t
	return t
}

func seeding(hash string, up int64) *engine.Status {
	return &engine.Status{Ref: hash, State: engine.Active, Seeding: true, Total: 100, Completed: 100, UpRate: up}
}

func setMaxSeeding(m *Manager, n int) {
	s := m.Settings()
	s.Torrent.MaxSeeding = n
	m.SaveSettings(s)
}

func schedNow(m *Manager, now time.Time) {
	m.mu.Lock()
	m.schedule(now)
	m.mu.Unlock()
}

// runningSeeds lists which of tasks 1..n run, as "1 2 3".
func runningSeeds(m *Manager, n int) (run, waiting string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := 1; i <= n; i++ {
		t := m.live[seedHash(i)]
		if m.running[t.Hash] {
			run += fmt.Sprint(i)
		}
		if t.SeedWaiting {
			waiting += fmt.Sprint(i)
		}
		if t.State != StSeeding && t.State != StDone {
			panic("state " + t.State)
		}
	}
	return
}

func TestSeedingLimit(t *testing.T) {
	m, _, now := clockManager(t)
	setMaxSeeding(m, 3)
	for i := 1; i <= 4; i++ {
		seedTask(m, i)
	}
	schedNow(m, *now)
	if run, wait := runningSeeds(m, 4); run != "123" || wait != "4" {
		t.Fatalf("limit 3: running %q waiting %q", run, wait)
	}
	// A download slot is not a seeding slot
	if s := m.Settings(); s.BT.MaxNum != 5 {
		t.Fatalf("bt.max_num %d", s.BT.MaxNum)
	}

	// The first one reaches its target: the waiting one starts
	m.mu.Lock()
	m.live[seedHash(1)].State = StDone
	m.running[seedHash(1)] = false
	m.mu.Unlock()
	schedNow(m, *now)
	if run, wait := runningSeeds(m, 4); run != "234" || wait != "" {
		t.Fatalf("after the first finished: running %q waiting %q", run, wait)
	}

	// No limit: everything seeds, as in 1.4.0
	m.mu.Lock()
	m.live[seedHash(1)].State = StSeeding
	m.mu.Unlock()
	seedTask(m, 5)
	setMaxSeeding(m, 0)
	schedNow(m, *now)
	if run, wait := runningSeeds(m, 5); run != "12345" || wait != "" {
		t.Fatalf("no limit: running %q waiting %q", run, wait)
	}

	// 3 again (two wait), then raised to 10: saving kicks the loop, and
	// the next pass starts both
	setMaxSeeding(m, 3)
	schedNow(m, *now)
	if run, wait := runningSeeds(m, 5); run != "123" || wait != "45" {
		t.Fatalf("limit 3 of 5: running %q waiting %q", run, wait)
	}
	for len(m.kick) > 0 {
		<-m.kick
	}
	setMaxSeeding(m, 10)
	if len(m.kick) != 1 {
		t.Fatal("saving the settings does not run the loop")
	}
	schedNow(m, *now)
	if run, wait := runningSeeds(m, 5); run != "12345" || wait != "" {
		t.Fatalf("raised to 10: running %q waiting %q", run, wait)
	}

	// Paused by its user: no seeding slot, not waiting either
	setMaxSeeding(m, 1)
	m.mu.Lock()
	m.live[seedHash(1)].UserPaused = true
	m.mu.Unlock()
	schedNow(m, *now)
	m.mu.Lock()
	t1 := m.live[seedHash(1)]
	if t1.State != StPaused || t1.SeedWaiting || !m.running[seedHash(2)] || m.running[seedHash(3)] {
		t.Fatalf("paused by its user: %s waiting %v, 2 runs %v, 3 runs %v", t1.State, t1.SeedWaiting, m.running[seedHash(2)], m.running[seedHash(3)])
	}
	m.mu.Unlock()
}

// A seeding torrent that has not uploaded for 30 minutes keeps seeding
// without a slot; when it uploads again, the last one to hold a slot waits.
func TestSeedIdleYieldsSlot(t *testing.T) {
	m, f, now := clockManager(t)
	setMaxSeeding(m, 1)
	a, b := seedTask(m, 1), seedTask(m, 2)
	schedNow(m, *now)
	if run, wait := runningSeeds(m, 2); run != "1" || wait != "2" {
		t.Fatalf("start: running %q waiting %q", run, wait)
	}
	apply(m, a, seeding(a.Hash, 0), f)
	schedNow(m, *now)
	*now = now.Add(29 * time.Minute)
	apply(m, a, seeding(a.Hash, 0), f)
	schedNow(m, *now)
	if run, _ := runningSeeds(m, 2); run != "1" {
		t.Fatalf("yielded before 30 minutes: running %q", run)
	}
	*now = now.Add(time.Minute)
	apply(m, a, seeding(a.Hash, 0), f)
	schedNow(m, *now)
	if run, wait := runningSeeds(m, 2); run != "12" || wait != "" {
		t.Fatalf("after 30 minutes without uploading: running %q waiting %q", run, wait)
	}
	// b seeds and uploads; then a uploads again: it is first, b waits again
	apply(m, b, seeding(b.Hash, 500), f)
	apply(m, a, seeding(a.Hash, 1000), f)
	schedNow(m, *now)
	if run, wait := runningSeeds(m, 2); run != "1" || wait != "2" {
		t.Fatalf("a uploads again: running %q waiting %q", run, wait)
	}
	// A restart counts again: the time is not stored
	m.mu.Lock()
	m.seedIdle = map[string]time.Time{}
	m.mu.Unlock()
	apply(m, a, seeding(a.Hash, 0), f)
	schedNow(m, *now)
	*now = now.Add(20 * time.Minute)
	apply(m, a, seeding(a.Hash, 0), f)
	schedNow(m, *now)
	if run, _ := runningSeeds(m, 2); run != "1" {
		t.Fatalf("20 minutes after a restart: running %q", run)
	}
}

// Waiting torrents get no share of the upload limit; one that downloads more
// files leaves the seeding queue.
func TestSeedWaitingUploadShare(t *testing.T) {
	m, f, now := clockManager(t)
	l := &limitBT{finderBT: f, up: map[string]int64{}}
	m.Engines["libtorrent"] = l
	s := m.Settings()
	s.BT.MaxUp = 300 // KB/s
	s.Torrent.MaxSeeding = 3
	m.SaveSettings(s)
	for i := 1; i <= 5; i++ {
		seedTask(m, i)
	}
	schedNow(m, *now)
	if run, wait := runningSeeds(m, 5); run != "123" || wait != "45" {
		t.Fatalf("running %q waiting %q", run, wait)
	}
	for i := 1; i <= 3; i++ {
		if got := l.up[seedHash(i)]; got != 100*1024 {
			t.Fatalf("seed %d gets %d B/s, want 100 KB/s", i, got)
		}
	}
	// More files chosen on a waiting torrent: it downloads, the download
	// queue decides, and it no longer waits to seed
	m.mu.Lock()
	t5 := m.live[seedHash(5)]
	t5.State, t5.SeedWaiting = StDownloading, false
	m.mu.Unlock()
	schedNow(m, *now)
	m.mu.Lock()
	defer m.mu.Unlock()
	if t5.SeedWaiting || !m.running[t5.Hash] || t5.State != StDownloading {
		t.Fatalf("downloading again: waiting %v running %v %s", t5.SeedWaiting, m.running[t5.Hash], t5.State)
	}
}

// A torrent that waited to seed and then finished, failed or moves no longer
// says it waits.
func TestSeedWaitingCleared(t *testing.T) {
	m, _, now := clockManager(t)
	setMaxSeeding(m, 1)
	for i := 1; i <= 4; i++ {
		seedTask(m, i)
	}
	schedNow(m, *now)
	if _, wait := runningSeeds(m, 4); wait != "234" {
		t.Fatalf("waiting %q", wait)
	}
	m.mu.Lock()
	for i, st := range map[int]string{2: StDone, 3: StError, 4: StMoving} {
		m.live[seedHash(i)].State = st
	}
	m.mu.Unlock()
	schedNow(m, *now)
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := 2; i <= 4; i++ {
		if m.live[seedHash(i)].SeedWaiting {
			t.Fatalf("task %d (%s) still waits to seed", i, m.live[seedHash(i)].State)
		}
	}
}
