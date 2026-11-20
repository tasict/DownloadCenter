package core

import (
	"testing"
	"time"

	"downloadcenter/internal/engine"
)

// listingBT counts how often it is asked for the file lists.
type listingBT struct {
	fakeBT
	withFiles, without int
	files              []engine.File
}

func (l *listingBT) StatusAllFiles(files bool) (map[string]*engine.Status, error) {
	if files {
		l.withFiles++
	} else {
		l.without++
	}
	return map[string]*engine.Status{}, nil
}

// The engines are asked for the tasks' complete file lists at most every
// ten seconds, and right away when someone looks at a task's files; the
// rest of the status still comes every second.
func TestFileListsEveryTenSeconds(t *testing.T) {
	m := testManager(t)
	m.Dev = true
	l := &listingBT{}
	m.Engines["libtorrent"] = l
	now := time.Unix(1_800_000_000, 0)
	m.clock = func() time.Time { return now }
	m.lastMin = time.Now()
	for i := 0; i < 25; i++ {
		m.tick()
		now = now.Add(time.Second)
		m.lastMin = time.Now()
	}
	if l.withFiles != 3 || l.without != 22 {
		t.Errorf("file lists %d times, plain status %d times in 25 s", l.withFiles, l.without)
	}
	// Someone looks at a task's files: they come from the engine right away
	task := &Task{Hash: stageHash, Kind: KindBT, Engine: "libtorrent", EngineRef: stageHash, State: StDownloading, CreatedAt: 1}
	m.live[stageHash] = task
	l.files = []engine.File{{Index: 0, Path: "Sintel/a.mp4", Size: 10, Completed: 7, Selected: true}}
	m.RefreshFiles(stageHash)
	if f := m.Files(stageHash); len(f) != 1 || f[0].Done != 7 {
		t.Errorf("files after a look: %+v", f)
	}
	if l.withFiles != 3 {
		t.Error("a look at one task's files asked for every task's")
	}
}

func (l *listingBT) Status(ref string) (*engine.Status, error) {
	return &engine.Status{Ref: ref, State: engine.Active, Files: l.files}, nil
}
