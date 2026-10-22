package core

import (
	"bytes"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"downloadcenter/internal/engine"
)

func TestMainVideo(t *testing.T) {
	f := func(i int, p string, size int64) prioFile {
		return prioFile{Index: i, Path: p, Size: size, Wanted: true}
	}
	cases := []struct {
		name  string
		files []prioFile
		want  int
	}{
		{"largest video", []prioFile{f(0, "M/sample.mkv", 50), f(1, "M/movie.mkv", 4000), f(2, "M/movie.srt", 1)}, 1},
		{"no video", []prioFile{f(0, "D/a.pdf", 50), f(1, "D/b.iso", 4000)}, -1},
		{"single file", []prioFile{f(0, "movie.mp4", 4000)}, -1},
		{"one file wanted", []prioFile{f(0, "M/movie.mkv", 4000), {Index: 1, Path: "M/x.txt", Size: 1}}, -1},
		{"largest not wanted", []prioFile{{Index: 0, Path: "M/movie.mkv", Size: 4000}, f(1, "M/sample.mkv", 50), f(2, "M/movie.srt", 1)}, 1},
		{"tie", []prioFile{f(0, "M/x.txt", 1), f(1, "M/e2.mp4", 700), f(2, "M/e1.mp4", 700)}, 1},
		{"tie, lower index later", []prioFile{f(5, "M/e2.mp4", 700), f(3, "M/e1.mp4", 700)}, 3},
		{"case", []prioFile{f(0, "M/MOVIE.MKV", 4000), f(1, "M/Sample.Mp4", 50)}, 0},
		{"pad does not count", []prioFile{f(0, "M/movie.mkv", 4000), {Index: 1, Path: "M/.pad/24", Size: 24, Wanted: true, Pad: true}}, -1},
		{"other extension", []prioFile{f(0, "M/movie.mkv.part", 4000), f(1, "M/clip.webm", 20)}, 1},
	}
	for _, c := range cases {
		if got := mainVideo(c.files); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
	pr, mf := autoPriorities([]prioFile{f(0, "M/movie.mkv", 4000), {Index: 1, Path: "M/.pad/24", Size: 24, Wanted: true, Pad: true},
		f(2, "M/sample.mkv", 50), {Index: 3, Path: "M/extra.mkv", Size: 9000}})
	if !reflect.DeepEqual(pr, map[string]int{"0": 7, "2": 4}) || mf.Path != "M/movie.mkv" {
		t.Errorf("priorities %v, main %+v", pr, mf)
	}
}

type tfile struct {
	path string
	size int
	pad  bool
}

// multiTorrent is a torrent called name holding files (one path element each).
func multiTorrent(name string, files ...tfile) []byte {
	info := "d5:filesl"
	for _, f := range files {
		info += "d"
		if f.pad {
			info += "4:attr1:p"
		}
		info += "6:lengthi" + strconv.Itoa(f.size) + "e4:pathl" + strconv.Itoa(len(f.path)) + ":" + f.path + "ee"
	}
	info += "e4:name" + strconv.Itoa(len(name)) + ":" + name + "12:piece lengthi16384e6:pieces20:" + strings.Repeat("a", 20) + "e"
	tr := "https://t.example.com/announce"
	return []byte("d8:announce" + strconv.Itoa(len(tr)) + ":" + tr + "4:info" + info + "e")
}

var movieFiles = []tfile{{"movie.mkv", 3000, false}, {".pad", 24, true}, {"sample.MKV", 100, false}, {"movie.srt", 10, false}}

func filePrios(m *Manager, hash string) []int {
	var out []int
	for _, f := range m.Files(hash) {
		out = append(out, f.Priority)
	}
	return out
}

func mainVideoLogs(m *Manager, hash string) []string {
	var out []string
	for _, l := range m.TaskLog(hash, 50) {
		if strings.HasPrefix(l.Msg, "Set the largest video to high priority: ") {
			out = append(out, l.Msg)
		}
	}
	return out
}

func TestAutoPriorityTorrent(t *testing.T) {
	m := btManager(t)
	f := m.Engines["libtorrent"].(*fakeBT)
	o := AddOptions{Owner: "admin", Admin: true, AutoRemove: "default"}
	b := multiTorrent("Movie", movieFiles...)
	res, err := m.AddTorrent(b, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := filePrios(m, res.ID); !reflect.DeepEqual(got, []int{7, 1, 4, 4}) {
		t.Fatalf("file priorities %v", got)
	}
	if l := mainVideoLogs(m, res.ID); len(l) != 1 || l[0] != "Set the largest video to high priority: Movie/movie.mkv" {
		t.Fatalf("log %v", l)
	}
	// The engine gets the levels with the add
	req, err := m.buildAdd(m.live[res.ID])
	if err != nil || !reflect.DeepEqual(req.Priorities, map[int]int{0: 7, 2: 4, 3: 4}) {
		t.Fatalf("add priorities %v (%v)", req.Priorities, err)
	}
	// The same torrent again only merges trackers
	m.AddTorrent(b, o)
	if l := mainVideoLogs(m, res.ID); len(l) != 1 || len(f.prios) != 0 {
		t.Fatalf("merge changed priorities: log %v, engine %v", l, f.prios)
	}
	// A backup source with the same content keeps what the task has
	if err := m.SetFiles(res.ID, map[int]int{0: 4}); err != nil {
		t.Fatal(err)
	}
	o.ContentOf = res.ID
	if r, err := m.AddTorrent(bytes.Replace(b, []byte(strings.Repeat("a", 20)), []byte(strings.Repeat("b", 20)), 1), o); err != nil || !r.Merged {
		t.Fatalf("content source: %v", err)
	}
	o.ContentOf = ""
	if got := filePrios(m, res.ID); !reflect.DeepEqual(got, []int{4, 1, 4, 4}) || len(mainVideoLogs(m, res.ID)) != 1 {
		t.Fatalf("content source changed priorities %v", got)
	}

	// Largest video not chosen: the largest chosen one
	o.Select = []int{2, 3}
	res, _ = m.AddTorrent(multiTorrent("Movie2", movieFiles...), o)
	if got := filePrios(m, res.ID); !reflect.DeepEqual(got, []int{0, 0, 7, 4}) {
		t.Fatalf("file priorities without the largest video %v", got)
	}
	// One file to download, or no video: left alone
	o.Select = []int{0}
	res, _ = m.AddTorrent(multiTorrent("Movie3", movieFiles...), o)
	if got := filePrios(m, res.ID); !reflect.DeepEqual(got, []int{1, 0, 0, 0}) || m.live[res.ID].Options.Priorities != nil || len(mainVideoLogs(m, res.ID)) != 0 {
		t.Fatalf("single file %v %v", got, m.live[res.ID].Options.Priorities)
	}
	o.Select = nil
	res, _ = m.AddTorrent(multiTorrent("Docs", tfile{"a.pdf", 3000, false}, tfile{"b.txt", 10, false}), o)
	if got := filePrios(m, res.ID); !reflect.DeepEqual(got, []int{1, 1}) || len(mainVideoLogs(m, res.ID)) != 0 {
		t.Fatalf("no video %v", got)
	}
	if req, _ := m.buildAdd(m.live[res.ID]); req.Priorities != nil {
		t.Fatalf("no saved levels, but the add carries %v", req.Priorities)
	}
}

func TestAutoPriorityMagnet(t *testing.T) {
	m := btManager(t)
	f := m.Engines["libtorrent"].(*fakeBT)
	o := AddOptions{Owner: "admin", Admin: true, AutoRemove: "default"}
	res, err := m.AddMagnet("magnet:?xt=urn:btih:"+stageHash+"&dn=Movie", o)
	if err != nil {
		t.Fatal(err)
	}
	task := m.live[res.ID]
	if !task.Options.AutoPrio {
		t.Fatal("magnet without its file list must wait to pick the main video")
	}
	task.EngineRef = res.ID
	apply(m, task, &engine.Status{Ref: res.ID, State: engine.Active, IsMetadata: true}, f)
	if len(f.prios) != 0 || !task.Options.AutoPrio {
		t.Fatalf("decided before the file list: %v", f.prios)
	}
	st := &engine.Status{Ref: res.ID, State: engine.Active, Name: "Movie", Total: 3110, Files: []engine.File{
		{Index: 0, Path: "Movie/movie.mkv", Size: 3000, Selected: true, Priority: 4},
		{Index: 1, Path: "Movie/.pad/24", Size: 24, Pad: true},
		{Index: 2, Path: "Movie/sample.mkv", Size: 100, Selected: true, Priority: 4},
		{Index: 3, Path: "Movie/movie.srt", Size: 10, Selected: true, Priority: 4},
	}}
	apply(m, task, st, f)
	if len(f.prios) != 1 || !reflect.DeepEqual(f.prios[0], map[int]int{0: 7, 2: 4, 3: 4}) || task.Options.AutoPrio {
		t.Fatalf("engine priorities %v, pending %v", f.prios, task.Options.AutoPrio)
	}
	if got := filePrios(m, res.ID); !reflect.DeepEqual(got, []int{7, 0, 4, 4}) {
		t.Fatalf("file priorities %v", got)
	}
	if l := mainVideoLogs(m, res.ID); len(l) != 1 {
		t.Fatalf("log %v", l)
	}
	apply(m, task, st, f)
	if len(f.prios) != 1 {
		t.Fatalf("decided twice: %v", f.prios)
	}
	// What the user sets afterwards stays
	if err := m.SetFiles(res.ID, map[int]int{0: 4}); err != nil {
		t.Fatal(err)
	}
	apply(m, task, st, f)
	if got := filePrios(m, res.ID); !reflect.DeepEqual(got, []int{4, 0, 4, 4}) || len(f.prios) != 2 {
		t.Fatalf("user's priorities %v, engine %v", got, f.prios)
	}
	if req, _ := m.buildAdd(task); req.Priorities[0] != 4 {
		t.Fatalf("add after the user's change %v", req.Priorities)
	}
}

// Imported tasks keep the official package's choice.
func TestAutoPriorityNotImported(t *testing.T) {
	m := btManager(t)
	b := multiTorrent("Movie", movieFiles...)
	hash := infoHash(t, b)
	if err := m.ImportTask(ImportSpec{Hash: hash, Owner: "admin", Kind: KindBT, Source: "Movie.torrent", TempDir: t.TempDir(), Torrent: b}); err != nil {
		t.Fatal(err)
	}
	task := m.live[hash]
	if got := filePrios(m, hash); !reflect.DeepEqual(got, []int{1, 1, 1, 1}) || task.Options.Priorities != nil || task.Options.AutoPrio {
		t.Fatalf("imported torrent %v %v", got, task.Options.Priorities)
	}
	mg := "1111111111111111111111111111111111111111"
	m.ImportTask(ImportSpec{Hash: mg, Owner: "admin", Kind: KindBT, Source: "magnet:?xt=urn:btih:" + mg, TempDir: t.TempDir()})
	if task := m.live[mg]; task == nil || task.Options.AutoPrio {
		t.Fatalf("imported magnet %+v", task)
	}
}
