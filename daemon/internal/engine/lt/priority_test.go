package lt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"downloadcenter/internal/engine"
	"downloadcenter/internal/torrent"
)

// The real dc-bt takes file priorities with the add, and a plain selection
// afterwards clears them.
func TestRealPriorities(t *testing.T) {
	bin := dcbtBin(t)
	// a.mkv, a padding file, b.mkv, c.srt: two pieces of 16 KiB
	tor := []byte("d4:infod5:filesl" +
		"d6:lengthi16000e4:pathl5:a.mkvee" +
		"d4:attr1:p6:lengthi384e4:pathl4:.pad3:384ee" +
		"d6:lengthi100e4:pathl5:b.mkvee" +
		"d6:lengthi10e4:pathl5:c.srtee" +
		"e4:name5:Movie12:piece lengthi16384e6:pieces40:" + strings.Repeat("x", 40) + "ee")
	meta, err := torrent.Parse(tor)
	if err != nil || len(meta.Files) != 4 || !meta.Files[1].Pad {
		t.Fatalf("test torrent: %v %+v", err, meta)
	}
	dir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	e := newEngine(bin, dir)
	e.ApplyGlobal(engine.Global{PortFrom: 26891, PortTo: 26899})
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	ref, err := e.Add(meta.InfoHash, engine.AddRequest{Torrent: tor, Dir: filepath.Join(dir, "data"), Paused: true,
		Priorities: map[int]int{0: 7, 2: 4, 3: 4}})
	if err != nil {
		t.Fatal(err)
	}
	// want: one priority per file, -1 = not checked (libtorrent decides the
	// padding file's itself)
	prios := func(want []int) {
		t.Helper()
		var got []int
		for i := 0; i < 50; i++ {
			st, err := e.StatusAll()
			if err != nil {
				t.Fatal(err)
			}
			got = got[:0]
			if s := st[ref]; s != nil {
				for _, f := range s.Files {
					got = append(got, f.Priority)
				}
			}
			same := len(got) == len(want)
			for j := 0; same && j < len(want); j++ {
				same = want[j] < 0 || got[j] == want[j]
			}
			if same {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("priorities %v, want %v", got, want)
	}
	prios([]int{7, -1, 4, 4})
	// Back to plain downloads: the levels dc-bt was given are gone
	if err := e.SetFiles(ref, []int{0, 2, 3}, map[int]int{0: 1, 2: 1, 3: 1}); err != nil {
		t.Fatal(err)
	}
	prios([]int{4, -1, 4, 4})
	// Levels again, then a selection that leaves a file out
	e.SetFiles(ref, []int{0, 2, 3}, map[int]int{0: 4, 2: 7, 3: 1})
	prios([]int{4, -1, 7, 1})
	e.SetFiles(ref, []int{0, 2}, map[int]int{0: 1, 2: 1})
	prios([]int{4, -1, 4, 0})
}
