package core

import (
	"testing"

	"downloadcenter/internal/engine"
)

// progress runs the save pass of the loop at nowU.
func progress(m *Manager, nowU int64) int64 {
	before := m.taskWrites.Load()
	m.mu.Lock()
	m.saveProgress(nowU)
	m.mu.Unlock()
	return m.taskWrites.Load() - before
}

// Tasks are written when something about them changed: never for a torrent
// that seeds without peers, at most every minute for one that uploads, at
// once for what a person did, and all of them when dcd stops.
func TestTaskWrites(t *testing.T) {
	m, _, _ := clockManager(t)
	idle, busy := seedTask(m, 1), seedTask(m, 2)
	m.saveTask(idle)
	m.saveTask(busy)
	now := int64(1_800_000_000)

	// Ten minutes: the idle seed is never written, the busy one every 60 s
	n := int64(0)
	for s := int64(1); s <= 600; s++ {
		if s%10 == 0 {
			m.mu.Lock()
			busy.UpTotal += 1000
			m.mu.Unlock()
		}
		n += progress(m, now+s)
	}
	// (nothing had changed at the first pass, so 61, 121 … 541 s)
	if n != 9 {
		t.Fatalf("ten minutes: %d writes, want 9 (the uploading seed once a minute)", n)
	}

	// The uploading seed is due at 601 s (its last minute)
	progress(m, now+601)
	// A person pauses the idle seed: written on the next pass
	m.mu.Lock()
	idle.UserPaused = true
	idle.State = StPaused
	m.markDirty(idle)
	m.mu.Unlock()
	if n = progress(m, now+630); n != 1 {
		t.Fatalf("paused: %d writes", n)
	}
	// Marked again without a change: nothing to write
	m.mu.Lock()
	m.markDirty(idle)
	m.mu.Unlock()
	if n = progress(m, now+631); n != 0 {
		t.Fatalf("dirty without a change: %d writes", n)
	}

	// A running download is written every 15 s while it progresses
	dl := seedTask(m, 3)
	m.mu.Lock()
	dl.State, dl.DoneBytes = StDownloading, 10
	m.mu.Unlock()
	n = 0
	for s := int64(632); s < 692; s++ {
		m.mu.Lock()
		dl.DoneBytes++
		m.mu.Unlock()
		n += progress(m, now+s)
	}
	if n != 4 {
		t.Fatalf("a minute of downloading: %d writes, want 4", n)
	}

	// Stopping writes every task
	before := m.taskWrites.Load()
	m.Stop()
	if got := m.taskWrites.Load() - before; got != 3 {
		t.Fatalf("stop wrote %d tasks, want 3", got)
	}
}

// A task read at start is written again only once something changed.
func TestLoadedTaskNotRewritten(t *testing.T) {
	m, _, _ := clockManager(t)
	s := seedTask(m, 1)
	m.saveTask(s)
	m.mu.Lock()
	delete(m.live, s.Hash)
	m.mu.Unlock()
	m.sigMu.Lock()
	m.taskSig = map[string]uint64{}
	m.sigMu.Unlock()
	loaded, err := m.loadTask(s.Hash)
	if err != nil {
		t.Fatal(err)
	}
	m.loadedTask(loaded)
	m.mu.Lock()
	m.live[loaded.Hash] = loaded
	m.mu.Unlock()
	if n := progress(m, 1_800_000_000); n != 0 {
		t.Fatalf("an unchanged task read at start was written %d times", n)
	}
}

// A file list is written when it changed, not on every poll.
func TestFileListWrites(t *testing.T) {
	m, _, _ := clockManager(t)
	task := seedTask(m, 1)
	files := []engine.File{{Index: 0, Path: "a/x.bin", Size: 10, Completed: 5, Selected: true},
		{Index: 1, Path: "a/y.bin", Size: 10, Completed: 0, Selected: true}}
	sync := func() int64 {
		before := m.fileWrites.Load()
		m.mu.Lock()
		m.syncFiles(task, files)
		m.mu.Unlock()
		return m.fileWrites.Load() - before
	}
	if sync() != 1 || sync() != 0 || sync() != 0 {
		t.Fatal("an unchanged file list was written again")
	}
	files[0].Completed = 10
	if sync() != 1 {
		t.Fatal("progress of a file not written")
	}
	if got := m.Files(task.Hash); len(got) != 2 || got[0].Done != 10 {
		t.Fatalf("stored %+v", got)
	}
	// Written elsewhere (a priority set by a person): the next list goes in
	m.forgetFiles(task.Hash)
	if sync() != 1 {
		t.Fatal("not written after a change elsewhere")
	}
}
