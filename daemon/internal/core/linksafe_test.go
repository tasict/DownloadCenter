package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"downloadcenter/internal/engine"
	"downloadcenter/internal/qts"
)

// testUID is the account of the scratch home: no NAS account has it.
const (
	testUser = "dc-test-user"
	testUID  = 60123
)

// scratchNAS replaces the NAS's folders and accounts with a scratch tree:
// <root>/vol is the volume root (root's), <root>/vol/homes the home folders,
// <root>/vol/homes/dc-test-user/Download the user's (uid 60123). No shared
// folder of the NAS is seen.
func scratchNAS(t *testing.T) (vol, download string) {
	t.Helper()
	vol = filepath.Join(t.TempDir(), "vol")
	homes := filepath.Join(vol, "homes")
	home := filepath.Join(homes, testUser)
	download = filepath.Join(home, "Download")
	if err := os.MkdirAll(download, 0777); err != nil {
		t.Fatal(err)
	}
	os.Chmod(vol, 0777) // like a QTS volume root
	for _, p := range []string{home, download} {
		os.Chown(p, testUID, testUID)
	}
	oh, os2, op, ol, ob, oi := homesRoot, shareList, publicDir, lookupUser, stagingBase, isStagingBase
	homesRoot = func() string { return homes }
	shareList = func() []qts.Share { return nil }
	publicDir = func() string { return filepath.Join(vol, "Public") }
	lookupUser = func(n string) (int, int, bool) {
		if n == testUser {
			return testUID, testUID, true
		}
		return 0, 0, false
	}
	stagingBase = func(dir string) (string, error) {
		if inside(dir, vol) {
			return vol, nil
		}
		return "", errNoStaging
	}
	isStagingBase = func(p string) bool { return filepath.Clean(p) == vol }
	t.Cleanup(func() {
		homesRoot, shareList, publicDir, lookupUser, stagingBase, isStagingBase = oh, os2, op, ol, ob, oi
	})
	return vol, download
}

func statOf(t *testing.T, p string) *syscall.Stat_t {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Lstat(p, &st); err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return &st
}

func victimDir(t *testing.T) (dir, file string) {
	dir = filepath.Join(t.TempDir(), "victim")
	os.MkdirAll(dir, 0700)
	file = filepath.Join(dir, "target.conf")
	os.WriteFile(file, []byte("ORIGINAL"), 0600)
	return
}

func intact(t *testing.T, file string) {
	t.Helper()
	if b, err := os.ReadFile(file); err != nil || string(b) != "ORIGINAL" {
		t.Errorf("%s changed: %q %v", file, b, err)
	}
	if st := statOf(t, file); st.Uid != rootUID {
		t.Errorf("%s now belongs to %d", file, st.Uid)
	}
}

// A regular user's task never works inside the user's tree: a link the
// user put where versions up to 1.3 kept the temporary folder is not used,
// and the engine gets a root-owned private folder on the same volume.
func TestUserTaskWorksInPrivateFolder(t *testing.T) {
	m := testManager(t)
	vol, download := scratchNAS(t)
	victim, file := victimDir(t)
	src := "http://example.invalid/x.bin"
	hash := URLHash(src)
	os.MkdirAll(filepath.Join(download, TempDirName), 0777)
	os.Symlink(victim, filepath.Join(download, TempDirName, hash))
	if got := m.UserDownloadDir(testUser); got != download {
		t.Fatalf("download dir %q", got)
	}
	task := &Task{Hash: hash, Owner: testUser, Kind: KindHTTP, Source: src, TempDir: download, State: StQueued, CreatedAt: 1}
	task.WorkDir = m.workDirFor(true, download, "", hash)
	req, err := m.buildAdd(task)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(vol, TempDirName, hash)
	if req.Dir != want {
		t.Fatalf("engine folder %q, want %q", req.Dir, want)
	}
	for _, p := range []string{filepath.Join(vol, TempDirName), want} {
		if st := statOf(t, p); st.Uid != rootUID || st.Mode&0777 != 0700 || st.Mode&syscall.S_IFMT != syscall.S_IFDIR {
			t.Errorf("%s: uid %d mode %o", p, st.Uid, st.Mode)
		}
	}
	if names, _ := os.ReadDir(victim); len(names) != 1 {
		t.Errorf("victim folder touched: %v", names)
	}
	intact(t, file)
	if d := m.DisplayPath(testUser, filepath.Join(want, "x.bin")); d != TempDirName+"/"+hash+"/x.bin" {
		t.Errorf("display %q", d)
	}
	if d, _ := FolderOf(task); d != download {
		t.Errorf("open folder of a staged task: %q", d)
	}

	// A link or someone else's folder in its place: the task stops
	os.RemoveAll(filepath.Join(vol, TempDirName))
	os.Symlink(victim, filepath.Join(vol, TempDirName))
	if _, err := m.buildAdd(task); !errors.Is(err, errWorkUnsafe) {
		t.Errorf("a link for the staging root: %v", err)
	}
	intact(t, file)
	needRoot(t)
	os.Remove(filepath.Join(vol, TempDirName))
	os.Mkdir(filepath.Join(vol, TempDirName), 0700)
	os.Mkdir(want, 0700)
	os.Chown(want, testUID, testUID)
	if _, err := m.buildAdd(task); !errors.Is(err, errWorkUnsafe) {
		t.Errorf("another account's folder: %v", err)
	}
}

// Handing data over to its owner never changes a file that a hard link
// makes known elsewhere, nor follows a link.
func TestChownSkipsHardLinks(t *testing.T) {
	needRoot(t)
	_, download := scratchNAS(t)
	_, outside := victimDir(t)
	data := filepath.Join(download, "Movie")
	os.Mkdir(data, 0755)
	own := filepath.Join(data, "a.mkv")
	os.WriteFile(own, []byte("x"), 0644)
	os.Link(outside, filepath.Join(data, "movie.mp4"))
	os.Symlink(filepath.Dir(outside), filepath.Join(data, "dir"))
	if err := chownPath(data, testUser, true); err != nil {
		t.Fatal(err)
	}
	intact(t, outside)
	if statOf(t, own).Uid != testUID || statOf(t, data).Uid != testUID {
		t.Error("the task's own files were not handed over")
	}
}

// Deleting a finished task with its data after the user swapped their
// Download folder for a link deletes nothing through the link.
func TestDeleteAfterSwap(t *testing.T) {
	m := testManager(t)
	_, download := scratchNAS(t)
	other := filepath.Join(t.TempDir(), "Projects")
	os.MkdirAll(filepath.Join(other, "Reports"), 0755)
	victim := filepath.Join(other, "Reports", "target.conf")
	os.WriteFile(victim, []byte("ORIGINAL"), 0600)
	os.MkdirAll(filepath.Join(download, "Reports"), 0755)
	src := "http://example.invalid/Reports"
	hash := URLHash(src)
	m.live[hash] = &Task{Hash: hash, Owner: testUser, Kind: KindHTTP, Source: src, TempDir: download, State: StDone,
		DataPath: filepath.Join(download, "Reports"), CreatedAt: 1}
	os.Rename(download, download+".old")
	os.Symlink(other, download)
	if err := m.Remove(hash, true, false); err != nil {
		t.Fatal(err)
	}
	intact(t, victim)
}

// Removing an unfinished task whose old temporary folder the user swapped
// for a link removes nothing through it.
func TestRemoveTempThroughLink(t *testing.T) {
	m := testManager(t)
	_, download := scratchNAS(t)
	src := "http://example.invalid/y.bin"
	hash := URLHash(src)
	elsewhere := t.TempDir()
	os.MkdirAll(filepath.Join(elsewhere, hash), 0755)
	victim := filepath.Join(elsewhere, hash, "target.conf")
	os.WriteFile(victim, []byte("ORIGINAL"), 0600)
	os.Symlink(elsewhere, filepath.Join(download, TempDirName))
	m.live[hash] = &Task{Hash: hash, Owner: testUser, Kind: KindHTTP, Source: src, TempDir: download, State: StDownloading,
		WorkDir: filepath.Join(download, TempDirName, hash), CreatedAt: 1}
	if err := m.Remove(hash, false, false); err != nil {
		t.Fatal(err)
	}
	intact(t, victim)
}

// The daily sweep of old temporary folders never sweeps a folder a link
// leads to.
func TestCleanTempFoldersThroughLink(t *testing.T) {
	m := testManager(t)
	vol, download := scratchNAS(t)
	elsewhere := t.TempDir()
	victim := filepath.Join(elsewhere, "target.conf")
	os.WriteFile(victim, []byte("ORIGINAL"), 0600)
	old := time.Now().Add(-30 * 24 * time.Hour)
	os.Chtimes(victim, old, old)
	os.Symlink(elsewhere, filepath.Join(download, TempDirName))
	// A stale private folder and one a live task owns
	stale := filepath.Join(vol, TempDirName, "stale")
	os.MkdirAll(stale, 0700)
	os.Chtimes(stale, old, old)
	ownedWork := filepath.Join(vol, TempDirName, "owned")
	os.MkdirAll(ownedWork, 0700)
	os.Chtimes(ownedWork, old, old)
	m.live["x"] = &Task{Hash: "x", Kind: KindHTTP, WorkDir: ownedWork}
	m.cleanTempFolders()
	intact(t, victim)
	if pathExists(stale) || !pathExists(ownedWork) {
		t.Errorf("stale kept %v, owned removed %v", pathExists(stale), !pathExists(ownedWork))
	}
}

// A task that versions up to 1.3 left working inside the user's tree has
// what its engine wrote moved into the private folder; links, hard links
// and the user's own files stay behind and go with the old folder.
func TestMigrateOldWork(t *testing.T) {
	m := testManager(t)
	vol, download := scratchNAS(t)
	_, outside := victimDir(t)
	src := "http://example.invalid/movie.mp4"
	hash := URLHash(src)
	old := filepath.Join(download, TempDirName, hash)
	os.MkdirAll(filepath.Join(old, "sub"), 0775)
	os.WriteFile(filepath.Join(old, "movie.mp4"), []byte("PARTIAL"), 0644)
	os.WriteFile(filepath.Join(old, ".movie.mp4.dcdl"), []byte("CTRL"), 0644)
	os.WriteFile(filepath.Join(old, "sub", "b.bin"), []byte("B"), 0644)
	os.Symlink(outside, filepath.Join(old, "evil.conf"))
	os.Link(outside, filepath.Join(old, "hard.conf"))
	os.WriteFile(filepath.Join(old, "planted"), []byte("P"), 0644)
	os.Chown(filepath.Join(old, "planted"), testUID, testUID)
	task := &Task{Hash: hash, Owner: testUser, Kind: KindHTTP, Source: src, Name: "movie.mp4", TempDir: download, WorkDir: old, State: StQueued, CreatedAt: 1}
	m.live[hash] = task
	req, err := m.buildAdd(task)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(vol, TempDirName, hash)
	if req.Dir != want || task.WorkDir != want {
		t.Fatalf("engine folder %q work %q", req.Dir, task.WorkDir)
	}
	for _, n := range []string{"movie.mp4", ".movie.mp4.dcdl"} {
		if _, err := os.Stat(filepath.Join(want, n)); err != nil {
			t.Errorf("%s not carried: %v", n, err)
		}
	}
	for _, n := range []string{"evil.conf", "hard.conf", "planted", "sub/b.bin"} {
		if _, err := os.Lstat(filepath.Join(want, n)); err == nil {
			t.Errorf("%s carried into the private folder", n)
		}
	}
	if pathExists(old) {
		t.Error("old folder left behind")
	}
	intact(t, outside)
	if m.oldWork(task) {
		t.Error("still counted as old")
	}

	// The old folder a link: the task stops, nothing moves through it
	victim, file := victimDir(t)
	src2 := "http://example.invalid/other.bin"
	h2 := URLHash(src2)
	os.MkdirAll(filepath.Join(download, TempDirName), 0777)
	os.Symlink(victim, filepath.Join(download, TempDirName, h2))
	t2 := &Task{Hash: h2, Owner: testUser, Kind: KindHTTP, Source: src2, TempDir: download,
		WorkDir: filepath.Join(download, TempDirName, h2), State: StQueued, CreatedAt: 1}
	if _, err := m.buildAdd(t2); !errors.Is(err, errWorkUnsafe) {
		t.Errorf("old folder a link: %v", err)
	}
	intact(t, file)

	// A torrent is taken out of the engine first, under every reference it
	// may have there, before its files move; then it is checked again
	f := &holdingBT{held: map[string]bool{stageHash: true, "oldref": true}}
	m.Engines["libtorrent"] = f
	bt := &Task{Hash: stageHash, Owner: testUser, Kind: KindBT, Name: "Sintel", Engine: "libtorrent", TempDir: download,
		WorkDir: filepath.Join(download, TempDirName, stageHash), State: StDownloading, CreatedAt: 1, EngineRef: "oldref"}
	os.MkdirAll(filepath.Join(bt.WorkDir, "Sintel"), 0775)
	os.WriteFile(filepath.Join(bt.WorkDir, "Sintel", "a.mp4"), []byte("x"), 0644)
	os.WriteFile(m.torrentPath(stageHash), []byte("d4:infod5:filesld6:lengthi1e4:pathl5:a.mp4eee4:name6:Sintel12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaaee"), 0600)
	f.onRemove = func() {
		if !pathExists(filepath.Join(bt.WorkDir, "Sintel", "a.mp4")) {
			t.Error("files moved while the engine still had the torrent")
		}
	}
	req, err = m.buildAdd(bt)
	if err != nil || req.Dir != filepath.Join(vol, TempDirName, stageHash) || !bt.Options.Check || len(f.held) != 0 {
		t.Fatalf("torrent: dir %q err %v check %v still held %v", req.Dir, err, bt.Options.Check, f.held)
	}
	if _, err := os.Stat(filepath.Join(req.Dir, "Sintel", "a.mp4")); err != nil {
		t.Error("torrent data not carried")
	}
	// An engine that does not let go: nothing moves
	f2 := &holdingBT{held: map[string]bool{stageHash: true}, stuck: true}
	m.Engines["libtorrent"] = f2
	bt2 := &Task{Hash: stageHash, Owner: testUser, Kind: KindBT, Name: "Sintel", Engine: "libtorrent", TempDir: download,
		WorkDir: filepath.Join(download, TempDirName, stageHash), State: StDownloading, CreatedAt: 1}
	os.MkdirAll(filepath.Join(bt2.WorkDir, "Sintel"), 0775)
	os.WriteFile(filepath.Join(bt2.WorkDir, "Sintel", "b.mp4"), []byte("x"), 0644)
	if _, err := m.buildAdd(bt2); !errors.Is(err, errMigrate) || !pathExists(filepath.Join(bt2.WorkDir, "Sintel", "b.mp4")) {
		t.Errorf("engine kept the torrent: %v", err)
	}
}

// holdingBT is a torrent engine that knows which references it holds.
type holdingBT struct {
	fakeBT
	held     map[string]bool
	stuck    bool
	onRemove func()
}

func (h *holdingBT) Remove(ref string) error {
	if h.onRemove != nil {
		h.onRemove()
	}
	if !h.stuck {
		delete(h.held, ref)
	}
	return nil
}

func (h *holdingBT) Status(ref string) (*engine.Status, error) {
	if h.held[ref] {
		return &engine.Status{Ref: ref, State: engine.Active}, nil
	}
	return nil, engine.ErrNotFound
}

// The finished download reaches the user's folder and becomes theirs; a
// Download folder swapped for a link meanwhile stops the move instead.
func TestFinalMoveIntoUserFolder(t *testing.T) {
	m := testManager(t)
	vol, download := scratchNAS(t)
	src := "http://example.invalid/movie.mp4"
	hash := URLHash(src)
	work := filepath.Join(vol, TempDirName, hash)
	task := &Task{Hash: hash, Owner: testUser, Kind: KindHTTP, Source: src, Name: "movie.mp4", TempDir: download, WorkDir: work,
		State: StDownloading, CreatedAt: 1}
	if _, err := m.prepareWork(task); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(work, "movie.mp4"), []byte("DATA"), 0644)
	os.WriteFile(filepath.Join(work, ".movie.mp4.dcdl"), []byte("CTRL"), 0644)
	m.live[hash] = task
	waitMoved := func() {
		for i := 0; i < 200; i++ {
			m.mu.Lock()
			moving := m.moving[hash]
			m.mu.Unlock()
			if !moving {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("move did not end")
	}
	victim, file := victimDir(t)
	os.Rename(download, download+".real")
	os.Symlink(victim, download)
	m.mu.Lock()
	m.startMove(task, work, task.finalDir(), true)
	m.mu.Unlock()
	waitMoved()
	if task.State != StError || !pathExists(filepath.Join(work, "movie.mp4")) {
		t.Fatalf("move through a link: state %s %q", task.State, task.ErrorMsg)
	}
	if names, _ := os.ReadDir(victim); len(names) != 1 {
		t.Errorf("moved through the link: %v", names)
	}
	intact(t, file)
	os.Remove(download)
	os.Rename(download+".real", download)
	m.mu.Lock()
	m.startMove(task, work, task.finalDir(), true)
	m.mu.Unlock()
	waitMoved()
	got := filepath.Join(download, "movie.mp4")
	if task.State != StDone || task.DataPath != got {
		t.Fatalf("state %s data %q (%s)", task.State, task.DataPath, task.ErrorMsg)
	}
	if pathExists(work) {
		t.Error("private folder left behind")
	}
	needRoot(t)
	if st := statOf(t, got); st.Uid != testUID {
		t.Errorf("not handed to the user: uid %d", st.Uid)
	}
}

// Preview opens only the task's own files, never through a link, never a
// hard link to someone else's file.
func TestOpenTaskFile(t *testing.T) {
	m := testManager(t)
	_, download := scratchNAS(t)
	_, outside := victimDir(t)
	data := filepath.Join(download, "notes.txt")
	os.WriteFile(data, []byte("mine"), 0644)
	os.Chown(data, testUID, testUID)
	task := &Task{Hash: "h", Owner: testUser, Kind: KindHTTP, State: StDone, TempDir: download, DataPath: data}
	f, err := m.OpenTaskFile(task, data)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	// Hard link of someone else's file: refused
	os.Remove(data)
	os.Link(outside, data)
	if _, err := m.OpenTaskFile(task, data); !errors.Is(err, ErrOutsideTask) {
		t.Errorf("hard link to another file: %v", err)
	}
	os.Remove(data)
	os.Symlink(outside, data)
	if _, err := m.OpenTaskFile(task, data); !errors.Is(err, ErrOutsideTask) {
		t.Errorf("data swapped for a link: %v", err)
	}
	os.Remove(data)
	os.WriteFile(data, []byte("mine"), 0644)
	os.Rename(download, download+".real")
	os.Symlink(filepath.Dir(outside), download)
	if _, err := m.OpenTaskFile(task, filepath.Join(download, filepath.Base(outside))); !errors.Is(err, ErrOutsideTask) {
		t.Errorf("folder swapped for a link: %v", err)
	}
	if _, err := m.OpenTaskFile(task, outside); !errors.Is(err, ErrOutsideTask) {
		t.Errorf("outside the task: %v", err)
	}
	// Hard link of the user's own file: fine
	needRoot(t)
	os.Remove(download)
	os.Rename(download+".real", download)
	os.Chown(data, testUID, testUID)
	os.Link(data, filepath.Join(download, "again.txt"))
	if f, err := m.OpenTaskFile(task, filepath.Join(download, "again.txt")); err != nil {
		t.Errorf("own hard link: %v", err)
	} else {
		f.Close()
	}
}

// Deleting a torrent with its data deletes the torrent's own files and
// the folders they leave empty, nothing else in its folder.
func TestRemoveTorrentKeepsForeignFiles(t *testing.T) {
	m := testManager(t)
	m.Engines["libtorrent"] = &fakeBT{}
	temp := t.TempDir()
	needShareTree(t, temp)
	st := m.Settings()
	st.TempDir = temp
	m.SaveSettings(st)
	movies := filepath.Join(temp, "Movies")
	os.MkdirAll(filepath.Join(movies, "sub"), 0755)
	keep := filepath.Join(movies, "keep.txt")
	os.WriteFile(keep, []byte("mine"), 0644)
	tor := []byte("d4:infod5:filesld6:lengthi1e4:pathl5:a.mkveed6:lengthi1e4:pathl3:sub5:b.mkveee4:name6:Movies" +
		"12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaaee")
	res, err := m.AddTorrent(tor, AddOptions{Owner: "admin", Admin: true, AutoRemove: "default"})
	if err != nil {
		t.Fatal(err)
	}
	task := m.live[res.ID]
	if task.DataPath != "" || task.WorkDir == "" {
		t.Fatalf("a folder of the same name was adopted: data %q work %q", task.DataPath, task.WorkDir)
	}
	// Pretend it finished there
	os.WriteFile(filepath.Join(movies, "a.mkv"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(movies, "sub", "b.mkv"), []byte("x"), 0644)
	task.State, task.DataPath, task.WorkDir = StDone, movies, ""
	if err := m.Remove(res.ID, true, false); err != nil {
		t.Fatal(err)
	}
	if !pathExists(keep) {
		t.Error("a file that is not the torrent's was deleted")
	}
	if pathExists(filepath.Join(movies, "a.mkv")) || pathExists(filepath.Join(movies, "sub")) {
		t.Error("the torrent's own files or emptied folders are left")
	}
}

// A Download folder that is a link within the home is recorded as the real
// folder; one that leads out of the home is not used; a missing one is made
// and handed to the user.
func TestUserDownloadDir(t *testing.T) {
	m := testManager(t)
	_, download := scratchNAS(t)
	home := filepath.Dir(download)
	os.Remove(download)
	if got := m.UserDownloadDir(testUser); got != download {
		t.Errorf("made %q", got)
	}
	madeUID := statOf(t, download).Uid
	os.Remove(download)
	os.Mkdir(filepath.Join(home, "Files"), 0777)
	os.Symlink("Files", download)
	if got := m.UserDownloadDir(testUser); got != filepath.Join(home, "Files") {
		t.Errorf("link within the home: %q", got)
	}
	os.Remove(download)
	os.Symlink(t.TempDir(), download)
	if got := m.UserDownloadDir(testUser); got != home {
		t.Errorf("link out of the home: %q", got)
	}
	needRoot(t)
	if madeUID != testUID {
		t.Errorf("made folder not handed to the user: uid %d", madeUID)
	}
}

// The staging folder lives at the root of the volume of the user's tree,
// and is refused when a shared folder would reach it. Only looks at the
// folders (nothing is created).
func TestVolumeStagingBase(t *testing.T) {
	homes := filepath.Join(t.TempDir(), "homes")
	os.MkdirAll(filepath.Join(homes, "u", "Download"), 0777)
	oh, os2 := homesRoot, shareList
	defer func() { homesRoot, shareList = oh, os2 }()
	homesRoot = func() string { return homes }
	shareList = func() []qts.Share { return nil }
	root, err := mountRoot(homes)
	if err != nil {
		// The scratch folders are on the file system of "/": never a
		// staging base
		if b, err := volumeStagingBase(filepath.Join(homes, "u", "Download")); err == nil {
			t.Errorf("a folder on the root file system gave the base %q", b)
		}
		t.Skip("needs scratch folders on a file system of their own")
	}
	if b, err := volumeStagingBase(filepath.Join(homes, "u", "Download")); err != nil || b != root {
		t.Errorf("base %q %v, want %q", b, err, root)
	}
	shareList = func() []qts.Share { return []qts.Share{{Name: "all", Path: root}} }
	if _, err := volumeStagingBase(filepath.Join(homes, "u", "Download")); err == nil {
		t.Error("a shared folder at the volume root was accepted")
	}
	if _, err := volumeStagingBase(t.TempDir()); err == nil {
		t.Error("a folder in no shared folder was accepted")
	}
}

// anyStaging gives every folder a scratch private staging base: for tests
// of other things whose task becomes a regular user's.
func anyStaging(t *testing.T) {
	base := t.TempDir()
	ob, oi := stagingBase, isStagingBase
	stagingBase = func(string) (string, error) { return base, nil }
	isStagingBase = func(p string) bool { return filepath.Clean(p) == base }
	t.Cleanup(func() { stagingBase, isStagingBase = ob, oi })
}

// A development instance never sweeps the NAS's shared folders; the
// installed package does.
func TestDevSkipsShareHousekeeping(t *testing.T) {
	for _, dev := range []bool{true, false} {
		m := testManager(t)
		m.Dev = dev
		share := t.TempDir()
		os.Chmod(share, 0777)
		stale := filepath.Join(share, TempDirName, "0123456789abcdef0123456789abcdef01234567")
		os.MkdirAll(stale, 0775)
		old := time.Now().Add(-30 * 24 * time.Hour)
		os.Chtimes(stale, old, old)
		os2, oh := shareList, homesRoot
		homesRoot = func() string { return filepath.Join(share, "no-homes") }
		shareList = func() []qts.Share { return []qts.Share{{Name: "Scratch", Path: share}} }
		m.minute()
		shareList, homesRoot = os2, oh
		if pathExists(stale) != dev {
			t.Errorf("dev %v: old temporary folder kept %v", dev, pathExists(stale))
		}
	}
}

// An administrator's task in an ordinary shared folder works in the private
// staging folder of its destination's volume as well; one that still works
// in the shared folder's own @DownloadCenterTemp moves there first.
func TestAdminTaskWorksInPrivateFolder(t *testing.T) {
	m := testManager(t)
	vol, _ := scratchNAS(t)
	m.URL = supportsAll{}
	share := filepath.Join(vol, "Share")
	os.MkdirAll(share, 0777)
	st := m.Settings()
	st.TempDir = share
	m.SaveSettings(st)
	r, err := m.AddURL("https://example.org/new.iso", AddOptions{Owner: "admin", Admin: true, AllTasks: true})
	if err != nil {
		t.Fatal(err)
	}
	task := m.live[r.ID]
	m.mu.Lock()
	req, err := m.buildAdd(task)
	m.mu.Unlock()
	want := filepath.Join(vol, TempDirName, r.ID)
	if err != nil || req.Dir != want || task.WorkDir != want {
		t.Fatalf("new task works in %q (%v)", req.Dir, err)
	}
	if s := statOf(t, want); s.Uid != rootUID || s.Mode&0777 != 0700 {
		t.Errorf("staging mode %o", s.Mode)
	}

	// Made by an earlier version in the shared folder
	src := "https://example.org/old.iso"
	hash := URLHash(src)
	old := filepath.Join(share, TempDirName, hash)
	os.MkdirAll(old, 0775)
	os.WriteFile(filepath.Join(old, "old.iso"), []byte("PARTIAL"), 0644)
	os.WriteFile(filepath.Join(old, ".old.iso.dcdl"), []byte("CTRL"), 0644)
	ot := &Task{Hash: hash, Owner: "admin", Kind: KindHTTP, Source: src, Name: "old.iso", TempDir: share, WorkDir: old, State: StQueued, CreatedAt: 1}
	m.live[hash] = ot
	m.mu.Lock()
	req, err = m.buildAdd(ot)
	m.mu.Unlock()
	nw := filepath.Join(vol, TempDirName, hash)
	if err != nil || req.Dir != nw || ot.WorkDir != nw {
		t.Fatalf("old task works in %q (%v)", req.Dir, err)
	}
	if b, _ := os.ReadFile(filepath.Join(nw, "old.iso")); string(b) != "PARTIAL" || !pathExists(filepath.Join(nw, ".old.iso.dcdl")) {
		t.Error("partial data not carried")
	}
	if pathExists(old) {
		t.Error("old folder left in the shared folder")
	}
}

// The daily sweep removes what the old shared-folder temporary folders
// still hold only when it is nothing of a task in the list, and never
// through a link.
func TestSweepOldShareTemp(t *testing.T) {
	m := testManager(t)
	vol, _ := scratchNAS(t)
	share := filepath.Join(vol, "Share")
	os.MkdirAll(share, 0777)
	shareList = func() []qts.Share { return []qts.Share{{Name: "Share", Path: share}} }
	old := time.Now().Add(-30 * 24 * time.Hour)
	known := "1111111111111111111111111111111111111111"
	for _, n := range []string{known, "2222222222222222222222222222222222222222"} {
		p := filepath.Join(share, TempDirName, n)
		os.MkdirAll(p, 0775)
		os.WriteFile(filepath.Join(p, "x"), []byte("x"), 0644)
		os.Chtimes(p, old, old)
	}
	_, file := victimDir(t)
	link := filepath.Join(share, TempDirName, "3333333333333333333333333333333333333333")
	os.Symlink(filepath.Dir(file), link)
	m.live[known] = &Task{Hash: known, Kind: KindHTTP, WorkDir: filepath.Join(vol, TempDirName, known)}
	m.cleanTempFolders()
	if !pathExists(filepath.Join(share, TempDirName, known, "x")) {
		t.Error("removed what a task in the list left")
	}
	if pathExists(filepath.Join(share, TempDirName, "2222222222222222222222222222222222222222")) {
		t.Error("an unknown old folder was kept")
	}
	intact(t, file)
}

// Deleting a finished download with its data deletes the downloaded file
// only: a folder someone put in its place under the same name stays.
func TestDeleteDataOnlyTheFile(t *testing.T) {
	m := testManager(t)
	_, download := scratchNAS(t)
	needShareTree(t, download)
	for _, swapped := range []bool{false, true} {
		src := fmt.Sprintf("http://example.invalid/report-%v.pdf", swapped)
		hash := URLHash(src)
		data := filepath.Join(download, fmt.Sprintf("report-%v.pdf", swapped))
		os.WriteFile(data, []byte("x"), 0644)
		if swapped {
			os.Rename(data, data+".old")
			os.MkdirAll(data, 0777)
			os.WriteFile(filepath.Join(data, "target.conf"), []byte("ORIGINAL"), 0600)
		}
		m.live[hash] = &Task{Hash: hash, Owner: testUser, Kind: KindHTTP, Source: src, TempDir: download, State: StDone,
			DataPath: data, CreatedAt: 1}
		if err := m.Remove(hash, true, false); err != nil {
			t.Fatal(err)
		}
		if swapped {
			intact(t, filepath.Join(data, "target.conf"))
		} else if pathExists(data) {
			t.Error("the downloaded file was not deleted")
		}
	}
}

// Moving an old temporary folder carries only the task's own files: its
// data and control file (a URL task), the files of its file list (a
// torrent). Anything else, even root's, stays behind.
func TestMigrateOnlyTheTasksFiles(t *testing.T) {
	m := testManager(t)
	vol, download := scratchNAS(t)
	src := "http://example.invalid/movie.mp4"
	hash := URLHash(src)
	old := filepath.Join(download, TempDirName, hash)
	os.MkdirAll(filepath.Join(old, "sub"), 0775)
	for _, n := range []string{"movie.mp4", ".movie.mp4.dcdl", "other.bin", "sub/x.bin"} {
		os.WriteFile(filepath.Join(old, n), []byte("x"), 0644)
	}
	task := &Task{Hash: hash, Owner: testUser, Kind: KindHTTP, Source: src, Name: "movie.mp4", TempDir: download, WorkDir: old,
		State: StQueued, CreatedAt: 1}
	m.live[hash] = task
	m.mu.Lock()
	_, err := m.buildAdd(task)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	nw := filepath.Join(vol, TempDirName, hash)
	for n, want := range map[string]bool{"movie.mp4": true, ".movie.mp4.dcdl": true, "other.bin": false, "sub/x.bin": false} {
		if pathExists(filepath.Join(nw, n)) != want {
			t.Errorf("%s carried: %v", n, !want)
		}
	}

	// A torrent: the files of its list only
	m.Engines["libtorrent"] = &fakeBT{}
	bold := filepath.Join(download, TempDirName, stageHash)
	os.MkdirAll(filepath.Join(bold, "Sintel"), 0775)
	for _, n := range []string{"Sintel/a.mp4", "Sintel/extra.sh", "loose.bin"} {
		os.WriteFile(filepath.Join(bold, n), []byte("x"), 0644)
	}
	os.WriteFile(m.torrentPath(stageHash), []byte("d4:infod4:name6:Sintelee"), 0600)
	bt := &Task{Hash: stageHash, Owner: testUser, Kind: KindBT, Name: "Sintel", Engine: "libtorrent", TempDir: download,
		WorkDir: bold, State: StDownloading, CreatedAt: 1}
	m.live[stageHash] = bt
	m.db.Tx(func(tx *sqlTx) error {
		return m.storeFiles(tx, stageHash, []FileRow{{Index: 0, Path: "Sintel/a.mp4", Size: 1, Priority: 1}})
	})
	m.mu.Lock()
	_, err = m.buildAdd(bt)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	bnw := filepath.Join(vol, TempDirName, stageHash)
	for n, want := range map[string]bool{"Sintel/a.mp4": true, "Sintel/extra.sh": false, "loose.bin": false} {
		if pathExists(filepath.Join(bnw, n)) != want {
			t.Errorf("torrent %s carried: %v", n, !want)
		}
	}
}
