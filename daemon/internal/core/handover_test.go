package core

import (
	"os"
	"path/filepath"
	"testing"

	"downloadcenter/internal/engine"
)

// notPrivateBT answers like dc-bt for a torrent whose data is outside the
// private folders and that it no longer knows, and when it is asked to take
// over data it cannot (refuse).
type notPrivateBT struct {
	fakeBT
	refuse bool
}

func (n *notPrivateBT) Add(hash string, r engine.AddRequest) (string, error) {
	n.added = append(n.added, r)
	if !isStagingDir(r.Dir) && !inStaging(r.Dir) || r.AdoptRequired && n.refuse {
		return "", engine.ErrFolderNotPrivate
	}
	return hash, nil
}

// Every torrent, an administrator's into an ordinary shared folder too, is
// handed over by the engine (untrusted_dir) from the private staging folder,
// with the files given to the account that should own them; once handed
// over it is never run from the destination again.
func TestEveryTorrentHandedOver(t *testing.T) {
	m := testManager(t)
	vol, download := scratchNAS(t)
	f := &fakeBT{}
	m.Engines["libtorrent"] = f
	share := filepath.Join(vol, "Share")
	os.MkdirAll(share, 0777)
	os.WriteFile(m.torrentPath(stageHash), []byte("d4:infod4:name6:Sintelee"), 0600)
	for _, c := range []struct {
		owner, move string
		uid         int
	}{{"admin", "", 0}, {"admin", download, testUID}, {testUser, "", testUID}} {
		temp := share
		if c.owner == testUser {
			temp = download
		}
		task := &Task{Hash: stageHash, Owner: c.owner, Kind: KindBT, Name: "Sintel", IsFolder: true, Engine: "libtorrent",
			TempDir: temp, MoveDir: c.move, State: StDownloading, CreatedAt: 1, EngineRef: stageHash}
		m.stageTorrent(task, c.owner == "admin")
		m.mu.Lock()
		req, err := m.buildAdd(task)
		m.mu.Unlock()
		if err != nil || !req.UntrustedDir || req.Dir != filepath.Join(vol, TempDirName, stageHash) || req.OwnerUID != c.uid {
			t.Errorf("%s into %q: dir %q untrusted %v owner %d (%v)", c.owner, task.finalDir(), req.Dir, req.UntrustedDir, req.OwnerUID, err)
		}
	}

	// Handed over to an ordinary shared folder: the destination is the data's
	// place, the private folder goes, the folder it seeds from stays
	task := &Task{Hash: stageHash, Owner: "admin", Kind: KindBT, Name: "Sintel", IsFolder: true, Engine: "libtorrent",
		TempDir: share, State: StDownloading, Size: 100, CreatedAt: 1, EngineRef: stageHash}
	m.stageTorrent(task, true)
	m.live[stageHash] = task
	os.MkdirAll(filepath.Join(task.WorkDir, "Sintel"), 0755)
	os.MkdirAll(task.WorkDir+".seed", 0700)
	apply(m, task, seedingStatus(task.WorkDir, "Sintel"), f)
	if len(f.moves) != 1 || f.moves[0][1] != share {
		t.Fatalf("hand-over asked: %v", f.moves)
	}
	apply(m, task, seedingStatus(share, "Sintel"), f)
	if task.DataPath != filepath.Join(share, "Sintel") || pathExists(task.WorkDir) || !pathExists(task.WorkDir+".seed") {
		t.Fatalf("after the hand-over: data %q work kept %v seed kept %v", task.DataPath, pathExists(task.WorkDir), pathExists(task.WorkDir+".seed"))
	}
	os.MkdirAll(filepath.Join(share, "Sintel"), 0755)
	if d, _ := FolderOf(task); d != filepath.Join(share, "Sintel") {
		t.Errorf("open folder: %q", d)
	}
	// The engine lost it (no resume data, a retry): it takes the data in the
	// destination over again into a private folder and goes on seeding
	np := &notPrivateBT{fakeBT: fakeBT{adopt: true, took: true}}
	m.Engines["libtorrent"] = np
	m.mu.Lock()
	task.EngineRef = ""
	m.addToEngine(task, np)
	m.mu.Unlock()
	if len(np.added) == 0 {
		t.Fatalf("not added: %s %q", task.State, task.ErrorMsg)
	}
	r := np.added[len(np.added)-1]
	if task.State != StSeeding || task.EngineRef == "" || !r.AdoptRequired || r.AdoptFrom != share || r.AdoptRoot != "Sintel" ||
		r.Dir != filepath.Join(vol, TempDirName, stageHash) || task.DataPath != filepath.Join(share, "Sintel") {
		t.Errorf("handed-over torrent the engine lost: state %s %q, add %+v", task.State, task.ErrorMsg, r)
	}
	// Nothing could be taken over: its seeding is over, not an error
	np.refuse = true
	m.mu.Lock()
	task.EngineRef = ""
	m.addToEngine(task, np)
	m.mu.Unlock()
	if task.State != StDone || task.ErrorCode != "" {
		t.Errorf("handed-over torrent whose data cannot be taken over: state %s %q", task.State, task.ErrorMsg)
	}
	// An engine that cannot take data over: the same
	task.State, task.EngineRef = StSeeding, ""
	old := &notPrivateBT{}
	m.Engines["libtorrent"] = old
	m.mu.Lock()
	m.addToEngine(task, old)
	m.mu.Unlock()
	if task.State != StDone || len(old.added) == 0 || old.added[0].AdoptFrom != "" {
		t.Errorf("engine without adopt_data: state %s, adds %+v", task.State, old.added)
	}
}

// pendingBT is still checking the data in the destination for its first
// adds.
type pendingBT struct {
	fakeBT
	left int
}

func (p *pendingBT) Add(hash string, r engine.AddRequest) (string, error) {
	p.added = append(p.added, r)
	if p.left > 0 {
		p.left--
		return "", engine.ErrPending
	}
	return hash, nil
}

// While the engine checks the data in the destination, the task waits for
// it (added again later): nothing fails, the log says so once.
func TestAdoptPending(t *testing.T) {
	m := testManager(t)
	_, download := scratchNAS(t)
	f := &pendingBT{fakeBT: fakeBT{adopt: true, took: true}, left: 2}
	m.Engines["libtorrent"] = f
	os.MkdirAll(filepath.Join(download, "Sintel"), 0755)
	os.WriteFile(m.torrentPath(stageHash), []byte("d4:infod4:name6:Sintelee"), 0600)
	task := &Task{Hash: stageHash, Owner: testUser, Kind: KindBT, Name: "Sintel", IsFolder: true, Engine: "libtorrent",
		TempDir: download, State: StQueued, Size: 100, CreatedAt: 1}
	m.stageTorrent(task, false)
	m.live[stageHash] = task
	for i := 0; i < 3; i++ {
		m.mu.Lock()
		m.addToEngine(task, f)
		m.mu.Unlock()
		if i < 2 && (task.EngineRef != "" || task.State != StQueued || task.Options.AdoptFrom == "") {
			t.Fatalf("add %d: ref %q state %s adopt %q", i, task.EngineRef, task.State, task.Options.AdoptFrom)
		}
	}
	if task.EngineRef != stageHash || task.DataPath != filepath.Join(download, "Sintel") || len(f.added) != 3 {
		t.Fatalf("ref %q data %q adds %d", task.EngineRef, task.DataPath, len(f.added))
	}
	n := 0
	for _, l := range m.TaskLog(stageHash, 50) {
		if l.Msg == "Checking the files already in the destination folder" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("logged %d times", n)
	}
}

// A torrent added while data of its name sits at the destination: the
// engine is asked to take it over; when it does, the data stays where it is
// (no second copy under another name, nothing carried out of the user's
// folder), and when it does not the torrent downloads as new.
func TestAdoptOnAdd(t *testing.T) {
	for _, took := range []bool{true, false} {
		m := testManager(t)
		vol, download := scratchNAS(t)
		f := &fakeBT{adopt: true, took: took}
		m.Engines["libtorrent"] = f
		os.MkdirAll(filepath.Join(download, "Sintel"), 0755)
		os.WriteFile(filepath.Join(download, "Sintel", "a.mp4"), []byte("DATA"), 0644)
		os.WriteFile(m.torrentPath(stageHash), []byte("d4:infod4:name6:Sintelee"), 0600)
		task := &Task{Hash: stageHash, Owner: testUser, Kind: KindBT, Name: "Sintel", IsFolder: true, Engine: "libtorrent",
			TempDir: download, State: StQueued, Size: 100, CreatedAt: 1}
		m.stageTorrent(task, false)
		if task.Options.AdoptFrom != download {
			t.Fatalf("adopt from %q", task.Options.AdoptFrom)
		}
		m.live[stageHash] = task
		m.mu.Lock()
		m.addToEngine(task, f)
		m.mu.Unlock()
		if len(f.added) == 0 {
			t.Fatalf("not added: %s %q", task.State, task.ErrorMsg)
		}
		r := f.added[0]
		work := filepath.Join(vol, TempDirName, stageHash)
		if r.Dir != work || r.AdoptFrom != download || r.AdoptRoot != "Sintel" || r.AdoptRequired {
			t.Fatalf("add %+v", r)
		}
		if task.Options.AdoptFrom != "" {
			t.Error("the request to take over stays")
		}
		if !took {
			if task.DataPath != "" || task.Options.Adopted || !task.InTemp() {
				t.Errorf("not taken over: data %q adopted %v", task.DataPath, task.Options.Adopted)
			}
			continue
		}
		if task.DataPath != filepath.Join(download, "Sintel") || !task.Options.Adopted || task.InTemp() {
			t.Fatalf("taken over: data %q adopted %v", task.DataPath, task.Options.Adopted)
		}
		// Added again (after a restart): taken over again, never carried
		m.mu.Lock()
		task.EngineRef = ""
		m.addToEngine(task, f)
		m.mu.Unlock()
		if len(f.added) < 2 {
			t.Fatalf("not added again: %s %q", task.State, task.ErrorMsg)
		}
		r = f.added[1]
		if !r.AdoptRequired || r.AdoptFrom != download || r.Dir != work || !pathExists(filepath.Join(download, "Sintel", "a.mp4")) {
			t.Fatalf("added again %+v", r)
		}
		// Complete: it seeds where it is, nothing is moved
		apply(m, task, seedingStatus(download, "Sintel"), f)
		if len(f.moves) != 0 || task.State != StSeeding || task.DataPath != filepath.Join(download, "Sintel") {
			t.Fatalf("complete: moves %v state %s data %q", f.moves, task.State, task.DataPath)
		}
	}
}

// A torrent that was adopted where its data sat (versions up to 1.3) and is
// not complete yet moves its data into the private staging folder first.
func TestAdoptedTorrentStaged(t *testing.T) {
	m := testManager(t)
	vol, _ := scratchNAS(t)
	m.Engines["libtorrent"] = &fakeBT{}
	share := filepath.Join(vol, "Share")
	os.MkdirAll(filepath.Join(share, "Sintel (1)"), 0777)
	os.WriteFile(filepath.Join(share, "Sintel (1)", "a.mp4"), []byte("PARTIAL"), 0644)
	os.WriteFile(m.torrentPath(stageHash), []byte("d4:infod5:filesld6:lengthi1e4:pathl5:a.mp4eee4:name6:Sintel12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaaee"), 0600)
	task := &Task{Hash: stageHash, Owner: "admin", Kind: KindBT, Name: "Sintel", IsFolder: true, Engine: "libtorrent",
		TempDir: share, DataPath: filepath.Join(share, "Sintel (1)"), State: StDownloading, CreatedAt: 1}
	m.live[stageHash] = task
	m.mu.Lock()
	req, err := m.buildAdd(task)
	m.mu.Unlock()
	work := filepath.Join(vol, TempDirName, stageHash)
	if err != nil || req.Dir != work || task.DataPath != "" || task.Options.Root != "Sintel (1)" || !req.Check {
		t.Fatalf("dir %q data %q root %q check %v (%v)", req.Dir, task.DataPath, task.Options.Root, req.Check, err)
	}
	if b, _ := os.ReadFile(filepath.Join(work, "Sintel", "a.mp4")); string(b) != "PARTIAL" {
		t.Error("data not carried under the torrent's own name")
	}
	if pathExists(filepath.Join(share, "Sintel (1)")) {
		t.Error("the emptied folder is left at the destination")
	}
}

// Handed-over files have a second name (the engine's seeding copy): the
// preview opens them for the user they were given to, and for an
// administrator's task whoever they belong to.
func TestPreviewHandedOverFiles(t *testing.T) {
	m := testManager(t)
	_, download := scratchNAS(t)
	seed := t.TempDir()
	for _, c := range []struct{ owner string }{{testUser}, {"admin"}} {
		data := filepath.Join(download, "Sintel-"+c.owner)
		os.MkdirAll(data, 0775)
		copyAt := filepath.Join(seed, "a-"+c.owner+".mp4")
		os.WriteFile(copyAt, []byte("DATA"), 0644)
		os.Chown(copyAt, testUID, testUID)
		os.Link(copyAt, filepath.Join(data, "a.mp4"))
		task := &Task{Hash: "h", Owner: c.owner, Kind: KindBT, State: StSeeding, TempDir: download, DataPath: data}
		f, err := m.OpenTaskFile(task, filepath.Join(data, "a.mp4"))
		if err != nil {
			t.Errorf("%s: %v", c.owner, err)
			continue
		}
		f.Close()
	}
}

// pendingRemoves is pendingBT that records what is removed.
type pendingRemoves struct {
	pendingBT
	removed []string
}

func (p *pendingRemoves) Remove(ref string) error {
	p.removed = append(p.removed, ref)
	return nil
}

// A task deleted while the engine still checks its data in the destination
// stops that check: the engine has no reference for it yet, so it is told
// by the torrent's hash.
func TestAdoptPendingRemoved(t *testing.T) {
	m := testManager(t)
	_, download := scratchNAS(t)
	f := &pendingRemoves{pendingBT: pendingBT{fakeBT: fakeBT{adopt: true, took: true}, left: 100}}
	m.Engines["libtorrent"] = f
	os.MkdirAll(filepath.Join(download, "Sintel"), 0755)
	os.WriteFile(m.torrentPath(stageHash), []byte("d4:infod4:name6:Sintelee"), 0600)
	task := &Task{Hash: stageHash, Owner: testUser, Kind: KindBT, Name: "Sintel", IsFolder: true, Engine: "libtorrent",
		TempDir: download, State: StQueued, Size: 100, CreatedAt: 1}
	m.stageTorrent(task, false)
	m.live[stageHash] = task
	m.mu.Lock()
	m.addToEngine(task, f)
	m.mu.Unlock()
	if task.EngineRef != "" || len(f.added) != 1 {
		t.Fatalf("ref %q adds %d", task.EngineRef, len(f.added))
	}
	if err := m.Remove(stageHash, false, false); err != nil {
		t.Fatal(err)
	}
	if len(f.removed) != 1 || f.removed[0] != stageHash {
		t.Fatalf("removed from the engine: %q", f.removed)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.adopting[stageHash] {
		t.Error("still marked as being checked")
	}
}
