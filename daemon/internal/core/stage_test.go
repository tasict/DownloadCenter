package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"downloadcenter/internal/engine"
)

// fakeBT is a torrent engine that records the moves and forgets asked of it.
type fakeBT struct {
	moves  [][3]string // ref, dir, root
	forgot []string
	noMove bool
}

func (f *fakeBT) Name() string { return "libtorrent" }
func (f *fakeBT) Caps() engine.Caps {
	return engine.Caps{Torrents: true, MoveWhileSeeding: !f.noMove}
}
func (f *fakeBT) Start() error                                         { return nil }
func (f *fakeBT) Stop() error                                          { return nil }
func (f *fakeBT) Health() error                                        { return nil }
func (f *fakeBT) Version() string                                      { return "test" }
func (f *fakeBT) ApplyGlobal(engine.Global) error                      { return nil }
func (f *fakeBT) Add(hash string, r engine.AddRequest) (string, error) { return hash, nil }
func (f *fakeBT) Forget(ref string)                                    { f.forgot = append(f.forgot, ref) }
func (f *fakeBT) Restart(engine.Global) error                          { return nil }
func (f *fakeBT) Pause(string) error                                   { return nil }
func (f *fakeBT) Resume(string) error                                  { return nil }
func (f *fakeBT) Remove(string) error                                  { return nil }
func (f *fakeBT) SetFiles(string, []int, map[int]int) error            { return nil }
func (f *fakeBT) SetLimits(string, int64, int64) error                 { return nil }
func (f *fakeBT) SetSequential(string, bool) error                     { return nil }
func (f *fakeBT) AddTrackers(string, []string) error                   { return nil }
func (f *fakeBT) ReplaceURIs(string, []string) error                   { return nil }
func (f *fakeBT) SetSeeding(string, float64, int) error                { return nil }
func (f *fakeBT) StatusAll() (map[string]*engine.Status, error)        { return nil, nil }
func (f *fakeBT) Status(string) (*engine.Status, error)                { return nil, engine.ErrNotFound }
func (f *fakeBT) Peers(string) ([]engine.Peer, error)                  { return nil, nil }
func (f *fakeBT) Trackers(string) ([]string, error)                    { return nil, nil }
func (f *fakeBT) SaveState() error                                     { return nil }
func (f *fakeBT) MoveStorage(ref, dir, root string) error {
	f.moves = append(f.moves, [3]string{ref, dir, root})
	return nil
}

const stageHash = "08ada5a7a6183aae1e09d831df6748d566095a10"

// stageSetup returns a manager with the fake engine and a torrent task that
// downloads "Sintel/a.mp4" into temp and moves it to move ("" = stay in temp).
func stageSetup(t *testing.T, withMove bool) (*Manager, *fakeBT, *Task, string, string) {
	t.Helper()
	m := testManager(t)
	f := &fakeBT{}
	m.Engines["libtorrent"] = f
	dir := t.TempDir()
	temp, move := filepath.Join(dir, "Download"), ""
	os.MkdirAll(temp, 0755)
	if withMove {
		move = filepath.Join(dir, "Movies")
		os.MkdirAll(move, 0755)
	}
	task := &Task{Hash: stageHash, Owner: "dc-test-nobody", Kind: KindBT, Name: "Sintel", IsFolder: true, Engine: "libtorrent",
		State: StDownloading, TempDir: temp, MoveDir: move, Size: 100, CreatedAt: 1}
	m.stageTorrent(task, "Sintel")
	os.WriteFile(m.torrentPath(stageHash), []byte("d4:infod4:name6:Sintelee"), 0600)
	task.EngineRef = stageHash
	m.live[stageHash] = task
	return m, f, task, temp, move
}

func seedingStatus(dir, root string) *engine.Status {
	return &engine.Status{Ref: stageHash, State: engine.Active, Seeding: true, Total: 100, Completed: 100, Dir: dir,
		Files: []engine.File{{Index: 0, Path: root + "/a.mp4", Size: 100, Completed: 100, Selected: true}}}
}

func apply(m *Manager, task *Task, st *engine.Status, e engine.Engine) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.applyStatus(task, st, e, 1)
}

func taskEvents(m *Manager) string {
	rows, err := m.db.Query(`SELECT type FROM events WHERE task_hash = ? ORDER BY id`, stageHash)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

func TestStagedTorrent(t *testing.T) {
	m, f, task, temp, move := stageSetup(t, true)
	work := m.workDirFor(temp, stageHash)
	if task.WorkDir != work || task.DataPath != "" || !task.InTemp() {
		t.Fatalf("new torrent not staged: work %q data %q", task.WorkDir, task.DataPath)
	}
	if req, err := m.buildAdd(task); err != nil || req.Dir != work {
		t.Fatalf("engine dir %q (%v), want %q", req.Dir, err, work)
	}
	os.MkdirAll(filepath.Join(work, "Sintel"), 0755)
	os.WriteFile(filepath.Join(work, "Sintel", "a.mp4"), []byte("x"), 0644)

	// Data complete: moved to the destination, not reported yet
	st := seedingStatus(work, "Sintel")
	apply(m, task, st, f)
	if len(f.moves) != 1 || f.moves[0] != [3]string{stageHash, move, ""} {
		t.Fatalf("moves %v", f.moves)
	}
	if task.State != StMoving || task.Options.MoveDst != move || taskEvents(m) != "" {
		t.Fatalf("state %s dst %q events %q", task.State, task.Options.MoveDst, taskEvents(m))
	}
	// Still moving: nothing happens
	st.Moving = true
	apply(m, task, st, f)
	if len(f.moves) != 1 || task.State != StMoving {
		t.Fatal("a running move was disturbed")
	}
	// The engine moved it: seeding from the destination
	os.Rename(filepath.Join(work, "Sintel"), filepath.Join(move, "Sintel"))
	st.Moving, st.Dir = false, move
	apply(m, task, st, f)
	if task.State != StSeeding || task.DataPath != filepath.Join(move, "Sintel") || task.FinishedAt == 0 || task.InTemp() {
		t.Fatalf("after move: state %s data %q", task.State, task.DataPath)
	}
	if pathExists(work) {
		t.Error("temp folder left behind")
	}
	if ev := taskEvents(m); ev != "task.completed,task.moved" {
		t.Errorf("events %q", ev)
	}
	if req, _ := m.buildAdd(task); req.Dir != move {
		t.Errorf("engine dir after move %q", req.Dir)
	}
	// Seeding ends: finished where it is, no second move
	st.State = engine.Complete
	apply(m, task, st, f)
	if task.State != StDone || len(f.forgot) != 1 || len(f.moves) != 1 || task.DataPath != filepath.Join(move, "Sintel") {
		t.Fatalf("after seeding: state %s forgot %v moves %v data %q", task.State, f.forgot, f.moves, task.DataPath)
	}
}

func TestStagedTorrentConflictAndFailure(t *testing.T) {
	m, f, task, temp, _ := stageSetup(t, false)
	work := task.WorkDir
	// Something called Sintel appeared in the destination meanwhile
	os.MkdirAll(filepath.Join(temp, "Sintel"), 0755)
	st := seedingStatus(work, "Sintel")
	apply(m, task, st, f)
	if len(f.moves) != 1 || f.moves[0] != [3]string{stageHash, temp, "Sintel (1)"} || task.Options.Root != "Sintel (1)" {
		t.Fatalf("moves %v root %q", f.moves, task.Options.Root)
	}
	if req, _ := m.buildAdd(task); req.Root != "Sintel (1)" {
		t.Errorf("re-add root %q", req.Root)
	}
	// The move fails: the task stops with the data left in its temp folder
	st.MoveError = "File exists: " + temp + "/Sintel (1)/a.mp4"
	apply(m, task, st, f)
	if task.State != StError || task.ErrorCode != "move" || len(f.forgot) != 1 || task.Options.MoveDst != "" {
		t.Fatalf("after failure: state %s code %s forgot %v", task.State, task.ErrorCode, f.forgot)
	}
	if task.Options.Root != "" {
		t.Errorf("root %q kept although the rename did not happen", task.Options.Root)
	}
	if !task.InTemp() || task.SaveDir() != work {
		t.Errorf("failed move must keep the temp folder: %q", task.SaveDir())
	}
}

func TestStagedTorrentAcrossVolumes(t *testing.T) {
	m, f, task, _, move := stageSetup(t, true)
	work := task.WorkDir
	old := sameVolume
	sameVolume = func(a, b string) bool { return !strings.HasPrefix(a, work) }
	defer func() { sameVolume = old }()
	via := m.workDirFor(move, stageHash)
	st := seedingStatus(work, "Sintel")
	apply(m, task, st, f)
	if len(f.moves) != 1 || f.moves[0] != [3]string{stageHash, via, ""} {
		t.Fatalf("first move %v, want into %s", f.moves, via)
	}
	// Copied to the destination's volume: renamed into place from there
	os.MkdirAll(work, 0755)
	st.Dir = via
	apply(m, task, st, f)
	if len(f.moves) != 2 || f.moves[1] != [3]string{stageHash, move, ""} || task.WorkDir != via || task.State != StMoving {
		t.Fatalf("second move %v work %q", f.moves, task.WorkDir)
	}
	if pathExists(work) {
		t.Error("first temp folder left behind")
	}
	st.Dir = move
	apply(m, task, st, f)
	if task.State != StSeeding || task.DataPath != filepath.Join(move, "Sintel") {
		t.Fatalf("after move: state %s data %q", task.State, task.DataPath)
	}
}

func TestStagedTorrentWithoutEngineMove(t *testing.T) {
	m, f, task, temp, move := stageSetup(t, true)
	work := task.WorkDir
	f.noMove = true
	apply(m, task, seedingStatus(work, "Sintel"), f)
	if len(f.moves) != 0 || task.WorkDir != "" || task.TempDir != work || task.MoveDir != move || task.State != StSeeding {
		t.Fatalf("fallback: work %q temp %q move %q state %s", task.WorkDir, task.TempDir, task.MoveDir, task.State)
	}
	if ev := taskEvents(m); ev != "task.completed" {
		t.Errorf("events %q", ev)
	}
	// New torrents stay in place when the engine cannot move them
	n := &Task{Hash: stageHash, Kind: KindBT, TempDir: temp, MoveDir: move}
	m.stageTorrent(n, "Other")
	if n.WorkDir != "" {
		t.Errorf("staged without engine support: %q", n.WorkDir)
	}
}

func TestStageTorrentExistingData(t *testing.T) {
	m := testManager(t)
	m.Engines["libtorrent"] = &fakeBT{}
	dir := t.TempDir()
	temp, move := filepath.Join(dir, "Download"), filepath.Join(dir, "Movies")
	os.MkdirAll(filepath.Join(move, "Sintel"), 0755)
	os.MkdirAll(filepath.Join(temp, "Old"), 0755)
	n := &Task{Hash: stageHash, Kind: KindBT, TempDir: temp, MoveDir: move}
	m.stageTorrent(n, "Sintel")
	if n.WorkDir != "" || n.DataPath != filepath.Join(move, "Sintel") || !n.Options.Check {
		t.Errorf("data at the destination: work %q data %q check %v", n.WorkDir, n.DataPath, n.Options.Check)
	}
	n = &Task{Hash: stageHash, Kind: KindBT, TempDir: temp, MoveDir: move}
	m.stageTorrent(n, "Old")
	if n.WorkDir != "" || n.DataPath != "" || !n.Options.Check || n.SaveDir() != temp {
		t.Errorf("data in the temporary location: work %q data %q", n.WorkDir, n.DataPath)
	}
}

func TestAdoptOfficialTemp(t *testing.T) {
	of := "/share/CACHEDEV1_DATA/Download/" + OfficialTempName + "/admin/Sintel.torrent." + stageHash
	task := &Task{Kind: KindBT, State: StDownloading, TempDir: of}
	task.Options.Official = true
	if !adoptOfficialTemp(task) || task.WorkDir != of || task.TempDir != "/share/CACHEDEV1_DATA/Download" {
		t.Fatalf("work %q temp %q", task.WorkDir, task.TempDir)
	}
	if adoptOfficialTemp(task) {
		t.Error("adopted twice")
	}
	done := &Task{Kind: KindBT, State: StDone, TempDir: of}
	done.Options.Official = true
	if adoptOfficialTemp(done) {
		t.Error("finished task adopted")
	}
}

func TestFolderOf(t *testing.T) {
	dir := t.TempDir()
	temp, move := filepath.Join(dir, "Download"), filepath.Join(dir, "Movies")
	work := filepath.Join(temp, TempDirName, stageHash)
	os.MkdirAll(filepath.Join(work, "Show"), 0755)
	os.MkdirAll(move, 0755)
	os.WriteFile(filepath.Join(work, "film.mkv"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(move, "done.iso"), []byte("x"), 0644)
	bt := func(name, data string, folder bool) *Task {
		return &Task{Kind: KindBT, Name: name, TempDir: temp, MoveDir: move, WorkDir: work, DataPath: data, IsFolder: folder}
	}
	cases := []struct {
		what      string
		t         *Task
		dir, file string
	}{
		{"downloading folder torrent", bt("Show", "", true), filepath.Join(work, "Show"), ""},
		{"downloading single-file torrent", bt("film.mkv", "", false), work, "film.mkv"},
		{"not started yet", bt("Other", "", true), work, ""},
		{"seeding single file at its destination", bt("done.iso", filepath.Join(move, "done.iso"), false), move, "done.iso"},
		{"data gone from its destination", bt("gone", filepath.Join(move, "gone"), true), move, ""},
		{"temp folder not made yet", &Task{Kind: KindBT, Name: "x", TempDir: temp, MoveDir: move, WorkDir: filepath.Join(temp, TempDirName, "none")}, move, ""},
		{"URL download in progress", &Task{Kind: KindHTTP, Name: "film.mkv", TempDir: temp, WorkDir: work}, work, "film.mkv"},
		{"finished URL download", &Task{Kind: KindHTTP, Name: "done.iso", State: StDone, TempDir: temp, MoveDir: move, WorkDir: work, DataPath: filepath.Join(move, "done.iso")}, move, "done.iso"},
	}
	for _, c := range cases {
		if d, f := FolderOf(c.t); d != c.dir || f != c.file {
			t.Errorf("%s: %q %q, want %q %q", c.what, d, f, c.dir, c.file)
		}
	}
}
