package core

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"downloadcenter/internal/engine"
	"downloadcenter/internal/qts"
	"downloadcenter/internal/safefs"
	"downloadcenter/internal/torrent"
)

// The NAS's folders and accounts as the core sees them (tests replace them).
var (
	homesRoot     = qts.HomesRoot
	shareList     = qts.Shares
	publicDir     = qts.PublicDir
	lookupUser    = qts.Lookup
	stagingBase   = volumeStagingBase
	isStagingBase = isMountRoot
)

// Errors of temporary folders that cannot be used safely; a task stops with
// one of these rather than let root write through someone else's link.
var (
	errNoStaging  = errors.New("No private temporary folder can be made for this download on the volume of its destination")
	errWorkUnsafe = errors.New("The temporary folder of this task was replaced by a link or by a folder of another account; it is not used")
	errDataUnsafe = errors.New("The folder of this task's files was replaced by a link; it is not used")
	errMigrate    = errors.New("The partly downloaded files could not be moved to the private temporary folder")
)

// homeDir is the real path of a user's home folder ("" when there is none).
func homeDir(user string) string {
	root := homesRoot()
	if root == "" || user == "" || strings.ContainsAny(user, "/\x00") || user == "." || user == ".." {
		return ""
	}
	p := filepath.Join(root, user)
	if st, err := os.Lstat(p); err == nil && st.IsDir() {
		return p
	}
	return ""
}

// userTree reports a folder regular users can write to: the home folders
// and the Public share. Root never works by path in there.
func userTree(dir string) bool {
	if dir == "" {
		return false
	}
	if hr := homesRoot(); hr != "" && inside(dir, hr) {
		return true
	}
	if pd := publicDir(); pd != "" && inside(dir, pd) {
		return true
	}
	return false
}

// userDest reports a task whose data ends up where regular users can
// write: a regular user's, and any whose temporary location or destination
// is in a tree regular users can write to (the home folders, Public).
func userDest(admin bool, tempDir, moveDir string) bool {
	return !admin || userTree(tempDir) || userTree(moveDir)
}

// workDirFor returns where a task downloads: <volume root>/@DownloadCenterTemp/<hash>,
// a folder only root may enter, on the file system of its destination (the
// data reaches it by renaming or by hard links) and out of reach of every
// shared folder. A volume whose root is itself a shared folder (an external
// drive) has no such place: there the task of an administrator that ends
// up outside the users' trees uses @DownloadCenterTemp/<hash> at the root of
// that shared folder, private all the same. "" when there is no folder to
// be had (userData: see userDest).
func (m *Manager) workDirFor(userData bool, tempDir, moveDir, hash string) string {
	dest := moveDir
	if dest == "" {
		dest = tempDir
	}
	if base, err := stagingBase(dest); err == nil {
		return filepath.Join(base, TempDirName, hash)
	}
	if sh, ok := qts.ShareOf(dest); ok && !userData && isMountRoot(sh.Path) {
		return filepath.Join(sh.Path, TempDirName, hash)
	}
	return ""
}

// volumeStagingBase is the root of the file system that holds dir's shared
// folder (the homes share for home folders). It is refused when a shared
// folder is that root or lies above it: the staging folder would then be
// reachable through the share.
func volumeStagingBase(dir string) (string, error) {
	anchor := ""
	if hr := homesRoot(); hr != "" && inside(dir, hr) {
		anchor = hr
	} else if sh, ok := qts.ShareOf(dir); ok {
		anchor = sh.Path
	}
	if anchor == "" {
		return "", errNoStaging
	}
	root, err := mountRoot(anchor)
	if err != nil {
		return "", errNoStaging
	}
	for _, sh := range shareList() {
		if inside(root, sh.Path) {
			return "", errNoStaging
		}
	}
	if hr := homesRoot(); hr != "" && inside(root, hr) {
		return "", errNoStaging
	}
	return root, nil
}

// mountRoot is the top folder of the file system holding p.
func mountRoot(p string) (string, error) {
	p = filepath.Clean(p)
	var st syscall.Stat_t
	if err := syscall.Lstat(p, &st); err != nil {
		return "", err
	}
	for p != "/" {
		parent := filepath.Dir(p)
		var ps syscall.Stat_t
		if err := syscall.Lstat(parent, &ps); err != nil {
			return "", err
		}
		if ps.Dev != st.Dev {
			return p, nil
		}
		p = parent
	}
	return "", errNoStaging
}

func isMountRoot(p string) bool {
	r, err := mountRoot(p)
	return err == nil && r == filepath.Clean(p)
}

// isStagingDir reports a private staging folder (<volume root>/@DownloadCenterTemp/<hash>).
func isStagingDir(work string) bool {
	parent := filepath.Dir(filepath.Clean(work))
	return filepath.Base(parent) == TempDirName && isStagingBase(filepath.Dir(parent))
}

// openWork opens a task's temporary folder, creating it (and its
// @DownloadCenterTemp parent) when create, and checks that no other account
// can have changed it. A staging folder must belong to root on a chain of
// folders that all belong to root, and be private (0700). A folder at the
// root of a shared folder must belong to root, be reached without a link and
// not be writable by other accounts. Other folders (the official package's
// temporary folders of imported tasks) only must be reached without a link.
func openWork(work string, create bool) (*safefs.Dir, error) {
	work = filepath.Clean(work)
	parent := filepath.Dir(work)
	if filepath.Base(parent) != TempDirName {
		return safefs.OpenDir(work)
	}
	base := filepath.Dir(parent)
	private := isStagingBase(base)
	var d *safefs.Dir
	var err error
	mode := uint32(0775)
	if private {
		d, err = safefs.OpenRootDir(base)
		mode = 0700
	} else {
		d, err = safefs.OpenDir(base)
	}
	if err != nil {
		return nil, err
	}
	t, err := ownedDir(d, TempDirName, mode, private, create)
	d.Close()
	if err != nil {
		return nil, err
	}
	w, err := ownedDir(t, filepath.Base(work), mode, private, create)
	t.Close()
	return w, err
}

// ownedDir opens name in d (creating it when create): it must belong to
// root and not be writable by other accounts (the group may write only when
// it is root's); a private one must be root's alone, and is made so.
func ownedDir(d *safefs.Dir, name string, mode uint32, private, create bool) (*safefs.Dir, error) {
	var sub *safefs.Dir
	var err error
	if create {
		sub, _, err = d.Mkdir(name, mode)
	} else {
		sub, err = d.Sub(name)
	}
	if err != nil {
		return nil, err
	}
	st, err := sub.Stat()
	if err == nil && (st.Uid != 0 || !private && (st.Mode&0002 != 0 || st.Mode&0020 != 0 && st.Gid != 0)) {
		err = safefs.ErrOwner
	}
	if err == nil && private && st.Mode&0077 != 0 {
		err = sub.Chmod(0700)
	}
	if err != nil {
		sub.Close()
		return nil, err
	}
	return sub, nil
}

// oldWork reports a task that still has one of the temporary folders
// versions up to 1.3 made: inside a user's tree
// (home/Download/@DownloadCenterTemp/<hash>) or at the root of a shared
// folder, where other accounts can change what the path leads to.
func (m *Manager) oldWork(t *Task) bool {
	if t.WorkDir == "" || t.Options.Official || !ownTemp(t.WorkDir) || isStagingDir(t.WorkDir) {
		return false
	}
	want := m.workDirFor(m.userData(t), t.TempDir, t.MoveDir, t.Hash)
	return filepath.Clean(want) != filepath.Clean(t.WorkDir)
}

// prepareWork returns t's temporary folder for the engine, created and
// checked (openWork). A task whose temporary folder is still an old one
// (oldWork) first has its data moved to its private folder. Caller holds
// m.mu.
func (m *Manager) prepareWork(t *Task) (string, error) {
	if t.Options.Official && t.WorkDir != "" {
		// Imported: the official temporary folder, never moved by the import
		d, err := safefs.OpenDir(t.WorkDir)
		if err != nil && !os.IsNotExist(err) {
			return "", errWorkUnsafe
		}
		d.Close()
		return t.WorkDir, nil
	}
	if t.WorkDir == "" || m.oldWork(t) {
		want := m.workDirFor(m.userData(t), t.TempDir, t.MoveDir, t.Hash)
		if want == "" {
			return "", errNoStaging
		}
		if t.WorkDir == "" {
			t.WorkDir = want
			m.markDirty(t)
		} else if err := m.migrateWork(t, want); err != nil {
			return "", err
		}
	}
	d, err := openWork(t.WorkDir, true)
	if err != nil && os.IsNotExist(err) {
		// An empty @DownloadCenterTemp was removed meanwhile: make it again
		d, err = openWork(t.WorkDir, true)
	}
	if err != nil {
		if errors.Is(err, safefs.ErrLink) || errors.Is(err, safefs.ErrOwner) {
			return "", errWorkUnsafe
		}
		return "", fmt.Errorf("Cannot create temporary folder: %v", err)
	}
	d.Close()
	return t.WorkDir, nil
}

// migrateWork moves what a task downloaded so far from its old temporary
// folder inside a user's tree into its private folder, then points the task
// there. Only what an engine wrote is carried: regular files with a single
// name that belong to root. Links, special files, files of other accounts
// and hard links stay behind and go with the old folder. Interrupted, it
// carries the rest the next time. Caller holds m.mu.
func (m *Manager) migrateWork(t *Task, newWork string) error {
	old := t.WorkDir
	if t.Kind == KindBT {
		// The engine must not have the files open while they move: out of
		// it, and confirmed gone (else tried again at the next add)
		if e := m.engineOf(t); e != nil {
			for _, ref := range []string{t.EngineRef, t.Hash, engineGID(e, t.Hash)} {
				if ref == "" {
					continue
				}
				e.Remove(ref)
				if st, err := e.Status(ref); err == nil && st.State != engine.Removed {
					return errMigrate
				}
			}
		}
	}
	src, err := safefs.OpenDir(old)
	switch {
	case err != nil && os.IsNotExist(err):
		// Nothing downloaded yet
	case err != nil:
		return errWorkUnsafe
	default:
		dst, derr := openWork(newWork, true)
		if derr != nil {
			src.Close()
			return errWorkUnsafe
		}
		err = carry(src, dst, m.ownFiles(t).keep, "", 0)
		src.Close()
		dst.Close()
		if err != nil {
			return errMigrate
		}
	}
	t.WorkDir = newWork
	if t.Kind == KindBT {
		t.Options.Check = true
	}
	m.markDirty(t)
	m.saveTask(t)
	removeTempDir(old)
	m.Log(t.Hash, "Moved the partly downloaded files to a private temporary folder")
	return nil
}

// owned is what of a temporary folder belongs to a task, as paths relative
// to the folder: a URL task's file and its control file, a torrent's files
// (its file list) and its part file. inner is the same for a torrent's
// files relative to its top folder.
type owned struct {
	files, dirs, innerFiles, innerDirs map[string]bool
}

func (o *owned) keep(rel string, dir bool) bool {
	if dir {
		return o.dirs[rel]
	}
	return o.files[rel]
}

func (o *owned) keepInner(rel string, dir bool) bool {
	if dir {
		return o.innerDirs[rel]
	}
	return o.innerFiles[rel]
}

// ownFiles lists what of its temporary folder belongs to t (see owned).
func (m *Manager) ownFiles(t *Task) *owned {
	o := &owned{files: map[string]bool{}, dirs: map[string]bool{}, innerFiles: map[string]bool{}, innerDirs: map[string]bool{}}
	add := func(files, dirs map[string]bool, p string) {
		p = filepath.Clean(p)
		if p == "." || p == ".." || filepath.IsAbs(p) || strings.HasPrefix(p, "../") {
			return
		}
		files[p] = true
		for d := filepath.Dir(p); d != "."; d = filepath.Dir(d) {
			dirs[d] = true
		}
	}
	if t.Kind == KindBT {
		var paths []string
		for _, f := range m.Files(t.Hash) {
			paths = append(paths, f.Path)
		}
		if len(paths) == 0 {
			if b, err := os.ReadFile(m.torrentPath(t.Hash)); err == nil {
				if meta, err := torrent.Parse(b); err == nil {
					for _, f := range meta.Files {
						paths = append(paths, f.Path)
					}
				}
			}
		}
		for _, p := range paths {
			add(o.files, o.dirs, p)
			if _, rest, ok := strings.Cut(filepath.Clean(p), "/"); ok {
				add(o.innerFiles, o.innerDirs, rest)
			}
		}
		add(o.files, o.dirs, "."+t.Hash+".parts")
		return o
	}
	names := []string{t.Name, t.Options.OutName}
	for _, f := range m.Files(t.Hash) {
		names = append(names, filepath.Base(f.Path))
	}
	for _, n := range names {
		if validName(n) {
			add(o.files, o.dirs, n)
			add(o.files, o.dirs, "."+n+".dcdl")
			add(o.files, o.dirs, n+".aria2")
		}
	}
	return o
}

// carry moves, from src to dst (see migrateWork), the regular files root
// wrote that keep says belong to the task (rel: the path below the folder
// carried), and the folders holding them. A file that turns out to be
// anything else once moved is deleted again.
func carry(src, dst *safefs.Dir, keep func(rel string, dir bool) bool, prefix string, depth int) error {
	if depth > 64 {
		return safefs.ErrDepth
	}
	names, err := src.Names()
	if err != nil {
		return err
	}
	for _, n := range names {
		st, err := src.Lstat(n)
		if err != nil {
			continue
		}
		rel := path.Join(prefix, n)
		switch st.Mode & syscall.S_IFMT {
		case syscall.S_IFDIR:
			if !keep(rel, true) {
				continue
			}
			s2, err := src.Sub(n)
			if err != nil {
				continue
			}
			d2, _, err := dst.Mkdir(n, 0755)
			if err != nil {
				s2.Close()
				return err
			}
			err = carry(s2, d2, keep, rel, depth+1)
			s2.Close()
			d2.Close()
			if err != nil {
				return err
			}
		case syscall.S_IFREG:
			if !keep(rel, false) || !engineFile(st) || dst.Exists(n) {
				continue
			}
			if err := src.Rename(n, dst, n); err != nil {
				return err
			}
			// Renamed by name: make sure it is still what was checked
			if st2, err := dst.Lstat(n); err != nil || st2.Mode&syscall.S_IFMT != syscall.S_IFREG || !engineFile(st2) {
				dst.Remove(n)
			}
		}
	}
	return nil
}

// engineFile reports a regular file as the engines leave it: root's, with a
// single name.
func engineFile(st *syscall.Stat_t) bool {
	return st.Uid == 0 && uint64(st.Nlink) == 1
}

// ErrOutsideTask: a file that is not the task's own (outside its folders,
// reached through a link, or another name of someone else's file).
var ErrOutsideTask = errors.New("not a file of this task")

// OpenTaskFile opens p, a file of t's data, for reading. p must lie in the
// folders the task recorded (a finished torrent: in its data only), and is
// opened one component at a time without following a link; a regular file
// with more than one name must belong to the account the task's files go
// to. A file that does not exist (yet) gives an os.IsNotExist error.
func (m *Manager) OpenTaskFile(t *Task, p string) (*os.File, error) {
	p = filepath.Clean(p)
	roots := []string{t.TempDir, t.WorkDir, t.DataPath}
	if t.DataPath != "" {
		roots = append(roots, filepath.Dir(t.DataPath))
	}
	if t.Kind == KindBT && t.State == StDone && t.DataPath != "" {
		roots = []string{t.DataPath}
	}
	ok := false
	for _, r := range roots {
		if r != "" && inside(p, r) {
			ok = true
		}
	}
	if !ok {
		return nil, ErrOutsideTask
	}
	d, name, err := safefs.OpenParent(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, err
		}
		return nil, ErrOutsideTask
	}
	defer d.Close()
	f, err := d.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, err
		}
		return nil, ErrOutsideTask
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		f.Close()
		return nil, err
	}
	// A file with another name too (a torrent's handed-over files have their
	// seeding copy) must be the owner's: else it could be someone else's
	// file the user linked there. Only administrators see administrators'
	// tasks, so theirs need no such check.
	if uint64(st.Nlink) > 1 && !m.isAdminOwner(t.Owner) {
		uid, _, found := lookupUser(t.Owner)
		if !found || st.Uid != uint32(uid) {
			f.Close()
			return nil, ErrOutsideTask
		}
	}
	if real, err := safefs.FdPath(f); err != nil || real != p {
		f.Close()
		return nil, ErrOutsideTask
	}
	return f, nil
}

// handOwner is the account a torrent's handed-over files go to (uid, gid):
// a regular user's own, an administrator's that land in someone's home
// that user's (see ownerFor). 0 leaves it to the engine: the owner of the
// folder they are handed to.
func (m *Manager) handOwner(t *Task) (int, int) {
	u := ownerFor(t.Owner, m.isAdminOwner(t.Owner), t.finalDir())
	if u == "" {
		return 0, 0
	}
	uid, gid, ok := lookupUser(u)
	if !ok {
		return 0, 0
	}
	return uid, gid
}

// userData reports a task whose data ends up where regular users can write
// (userDest, with its owner's role now).
func (m *Manager) userData(t *Task) bool {
	return userDest(m.isAdminOwner(t.Owner), t.TempDir, t.MoveDir)
}

// stageInPlace gives a torrent that still downloads where its data sits
// outside the private folders a private staging folder, and moves what the
// engine wrote so far into it under the torrent's own name: one added by
// 0.9.x (no temporary folder), or one whose data was found at its
// destination when it was added (versions up to 1.3). The engine only runs
// torrents from private folders. Caller holds m.mu.
func (m *Manager) stageInPlace(t *Task) error {
	want := m.workDirFor(m.userData(t), t.TempDir, t.MoveDir, t.Hash)
	if want == "" {
		return errNoStaging
	}
	if e := m.engineOf(t); e != nil {
		for _, ref := range []string{t.EngineRef, t.Hash} {
			if ref == "" {
				continue
			}
			e.Remove(ref)
			if st, err := e.Status(ref); err == nil && st.State != engine.Removed {
				return errMigrate
			}
		}
	}
	srcDir, name := t.TempDir, t.Name
	if t.DataPath != "" {
		srcDir, name = filepath.Dir(t.DataPath), filepath.Base(t.DataPath)
	}
	if name != "" && validName(name) && validName(t.Name) {
		src, err := safefs.OpenDir(srcDir)
		if err != nil {
			return errDataUnsafe
		}
		defer src.Close()
		dst, err := openWork(want, true)
		if err != nil {
			return errWorkUnsafe
		}
		defer dst.Close()
		if st, err := src.Lstat(name); err == nil {
			switch st.Mode & syscall.S_IFMT {
			case syscall.S_IFDIR:
				s2, err := src.Sub(name)
				if err != nil {
					return errDataUnsafe
				}
				d2, _, err := dst.Mkdir(t.Name, 0755)
				if err == nil {
					err = carry(s2, d2, m.ownFiles(t).keepInner, "", 0)
					d2.Close()
				}
				s2.Close()
				if err != nil {
					return errMigrate
				}
				removeEmptyDirs(src, name, 0)
			case syscall.S_IFREG:
				if engineFile(st) && !dst.Exists(t.Name) && src.Rename(name, dst, t.Name) == nil {
					if st2, err := dst.Lstat(t.Name); err != nil || !engineFile(st2) || st2.Mode&syscall.S_IFMT != syscall.S_IFREG {
						dst.Remove(t.Name)
					}
				}
			}
		}
	}
	// Handed back under the name it had there
	if name != "" && name != t.Name && t.Options.Root == "" {
		t.Options.Root = name
	}
	t.WorkDir, t.DataPath = want, ""
	t.Options.Check = true
	m.markDirty(t)
	m.saveTask(t)
	m.Log(t.Hash, "Moved the partly downloaded files to a private temporary folder")
	return nil
}

// validName: a single path component.
func validName(n string) bool {
	return n != "" && n != "." && n != ".." && !strings.ContainsAny(n, "/\x00")
}
