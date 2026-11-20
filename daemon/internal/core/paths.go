package core

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"downloadcenter/internal/qts"
	"downloadcenter/internal/safefs"
	"downloadcenter/internal/torrent"
)

// TempDirName is the folder where downloads stay until they are complete:
// at the root of a volume (see workDirFor). Versions up to 1.3 also made it
// at the root of shared folders and in home Download folders.
const TempDirName = "@DownloadCenterTemp"

// OfficialTempName is the official package's temporary folder.
const OfficialTempName = "@DownloadStationTempFiles"

var ErrFolder = errors.New("folder_not_allowed")

func (t *Task) finalDir() string {
	if t.MoveDir != "" {
		return t.MoveDir
	}
	return t.TempDir
}

// DisplayPath turns a real path into the form shown to a viewer:
// "Share/sub", "home/sub" for the viewer's own home, "homes/<user>/sub".
func (m *Manager) DisplayPath(viewer, p string) string {
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	if hr := homesRoot(); hr != "" && (p == hr || strings.HasPrefix(p, hr+"/")) {
		rest := strings.TrimPrefix(strings.TrimPrefix(p, hr), "/")
		user, sub, _ := strings.Cut(rest, "/")
		if user == viewer {
			if sub == "" {
				return "home"
			}
			return "home/" + sub
		}
		return strings.TrimSuffix("homes/"+rest, "/")
	}
	if sh, ok := qts.ShareOf(p); ok {
		rest := strings.TrimPrefix(strings.TrimPrefix(p, sh.Path), "/")
		if rest == "" {
			return sh.Name
		}
		return sh.Name + "/" + rest
	}
	// A private staging folder is shown without the volume it is on
	if inStaging(p) {
		return p[strings.Index(p, "/"+TempDirName+"/")+1:]
	}
	return p
}

// ResolvePath turns a display path ("Share/sub", "/Share/sub", "home/sub",
// "homes/u/sub" or a real /share path) into a real directory path.
func (m *Manager) ResolvePath(viewer, d string) (string, error) {
	d = strings.TrimSpace(d)
	if d == "" {
		return "", ErrFolder
	}
	for _, part := range strings.Split(d, "/") {
		if part == ".." {
			return "", ErrFolder
		}
	}
	var real string
	if strings.HasPrefix(d, "/share/") {
		real = d
	} else {
		d = strings.Trim(d, "/")
		first, rest, _ := strings.Cut(d, "/")
		switch {
		case first == "home":
			h := homeDir(viewer)
			if h == "" {
				return "", ErrFolder
			}
			real = filepath.Join(h, rest)
		case first == "homes":
			hr := homesRoot()
			if hr == "" {
				return "", ErrFolder
			}
			real = filepath.Join(hr, rest)
		default:
			for _, sh := range qts.Shares() {
				if strings.EqualFold(sh.Name, first) {
					real = filepath.Join(sh.Path, rest)
				}
			}
		}
	}
	if real == "" {
		return "", ErrFolder
	}
	// Links are followed before the share check: dcd writes as admin, so a
	// link inside a share must not lead it anywhere else
	ev, err := filepath.EvalSymlinks(real)
	if err != nil {
		return "", ErrFolder
	}
	real = filepath.Clean(ev)
	if _, ok := qts.ShareOf(real); !ok {
		return "", ErrFolder
	}
	st, err := os.Stat(real)
	if err != nil || !st.IsDir() {
		return "", ErrFolder
	}
	return real, nil
}

// UserDownloadDir is where a regular user's tasks go: the home Download
// folder, or Public when the account has no home folder. It is created and
// handed to the user when missing. A Download that is a link within the
// home gives the real folder it leads to, which is what tasks record: the
// engines and the moves never follow a link the user controls.
func (m *Manager) UserDownloadDir(user string) string {
	h := homeDir(user)
	if h == "" {
		return publicDir()
	}
	d := filepath.Join(h, "Download")
	hd, err := safefs.OpenDir(h)
	if err != nil {
		return h
	}
	defer hd.Close()
	st, err := hd.Lstat("Download")
	switch {
	case err != nil && os.IsNotExist(err):
		sub, created, err := hd.Mkdir("Download", 0755)
		if err != nil {
			return h
		}
		if created {
			if uid, gid, ok := lookupUser(user); ok {
				sub.Chown(uid, gid)
			}
		}
		sub.Close()
		return d
	case err != nil:
		return h
	case st.Mode&syscall.S_IFMT == syscall.S_IFDIR:
		return d
	case st.Mode&syscall.S_IFMT == syscall.S_IFLNK:
		real, err := filepath.EvalSymlinks(d)
		if err != nil || !inside(real, h) || real == h {
			return h
		}
		if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
			return h
		}
		return real
	}
	return h
}

// chownPath gives a path (recursively) to a QTS account, without following
// links (safefs.ChownPath).
func chownPath(p, user string, recursive bool) error {
	uid, gid, ok := lookupUser(user)
	if !ok {
		return fmt.Errorf("unknown user %s", user)
	}
	return safefs.ChownPath(p, uid, gid, recursive)
}

// uniqueDir returns dst, or "dst (n)" when it exists. Unlike uniquePath it
// keeps a folder's name whole ("Some.Show.S01 (1)").
func uniqueDir(dst string) string {
	if _, err := os.Lstat(dst); err != nil {
		return dst
	}
	for i := 1; i < 10000; i++ {
		c := fmt.Sprintf("%s (%d)", dst, i)
		if _, err := os.Lstat(c); err != nil {
			return c
		}
	}
	return dst
}

func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// sameVolume reports whether two folders are on one file system, so data
// moves between them by renaming. A missing folder counts as its nearest
// existing parent. A variable so that tests can pretend otherwise.
var sameVolume = func(a, b string) bool {
	da, ok := deviceOf(a)
	db, ok2 := deviceOf(b)
	return ok && ok2 && da == db
}

func deviceOf(p string) (uint64, bool) {
	var st syscall.Stat_t
	for p = filepath.Clean(p); ; p = filepath.Dir(p) {
		if err := syscall.Stat(p, &st); err == nil {
			return uint64(st.Dev), true
		}
		if p == "/" || p == "." {
			return 0, false
		}
	}
}

// uniquePath returns dst, or "dst (n)" variants when it exists.
func uniquePath(dst string) string {
	if _, err := os.Lstat(dst); err != nil {
		return dst
	}
	dir, base := filepath.Split(dst)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if strings.HasSuffix(stem, ".tar") {
		ext = ".tar" + ext
		stem = strings.TrimSuffix(stem, ".tar")
	}
	for i := 1; i < 10000; i++ {
		c := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		if _, err := os.Lstat(c); err != nil {
			return c
		}
	}
	return dst
}

// moveInto moves src (file or folder) into dir, renaming on conflicts.
// Same volume: rename; otherwise copy then remove. Returns the new path.
// Nothing on the way is followed if it is a link: both folders are opened
// one component at a time and the move is made between the open folders.
func moveInto(src, dir string) (string, error) {
	sd, name, err := safefs.OpenParent(src)
	if err != nil {
		return "", err
	}
	defer sd.Close()
	return moveEntry(sd, name, dir)
}

// moveEntry moves name from the open folder sd into dir (see moveInto).
func moveEntry(sd *safefs.Dir, name, dir string) (string, error) {
	st, err := sd.Lstat(name)
	if err != nil {
		return "", err
	}
	isDir := st.Mode&syscall.S_IFMT == syscall.S_IFDIR
	dd, err := safefs.MkdirAll(dir, 0755)
	if err != nil {
		return "", err
	}
	defer dd.Close()
	got, err := renameUnique(sd, name, dd, name, isDir)
	if err == nil {
		return filepath.Join(dir, got), nil
	}
	if err != syscall.EXDEV {
		return "", err
	}
	// Copy under a temporary name first: an interrupted copy never looks
	// like a finished file
	part := ".dc-part-" + name
	dd.RemoveAll(part)
	if err := copyEntry(sd, name, dd, part, 0); err != nil {
		dd.RemoveAll(part)
		return "", err
	}
	got, err = renameUnique(dd, part, dd, name, isDir)
	if err != nil {
		dd.RemoveAll(part)
		return "", err
	}
	sd.RemoveAll(name)
	return filepath.Join(dir, got), nil
}

// renameUnique renames name in from to want in to, or to "want (n)" when
// that is taken (see uniqueName), and returns the name used.
func renameUnique(from *safefs.Dir, name string, to *safefs.Dir, want string, isDir bool) (string, error) {
	for i := 0; i < 10000; i++ {
		c := uniqueName(want, i, isDir)
		if to.Exists(c) {
			continue
		}
		err := from.Rename(name, to, c)
		switch err {
		case nil:
			return c, nil
		case syscall.EEXIST, syscall.ENOTEMPTY, syscall.ENOTDIR, syscall.EISDIR:
			continue // taken meanwhile
		}
		return "", err
	}
	return "", syscall.EEXIST
}

// uniqueName is name for n = 0 and "name (n)" variants after it, as
// uniquePath (files) and uniqueDir (folders) make them.
func uniqueName(name string, n int, isDir bool) string {
	if n == 0 {
		return name
	}
	if isDir {
		return fmt.Sprintf("%s (%d)", name, n)
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if strings.HasSuffix(stem, ".tar") {
		ext = ".tar" + ext
		stem = strings.TrimSuffix(stem, ".tar")
	}
	return fmt.Sprintf("%s (%d)%s", stem, n, ext)
}

// copyEntry copies name from sd to dst in dd. Links and special files are
// not copied; new files are created, never opened if they exist.
func copyEntry(sd *safefs.Dir, name string, dd *safefs.Dir, dst string, depth int) error {
	if depth > 64 {
		return safefs.ErrDepth
	}
	st, err := sd.Lstat(name)
	if err != nil {
		return err
	}
	switch st.Mode & syscall.S_IFMT {
	case syscall.S_IFDIR:
		in, err := sd.Sub(name)
		if err != nil {
			return err
		}
		defer in.Close()
		out, created, err := dd.Mkdir(dst, (st.Mode&0777)|0700)
		if err != nil {
			return err
		}
		defer out.Close()
		if !created {
			return syscall.EEXIST
		}
		names, err := in.Names()
		if err != nil {
			return err
		}
		for _, n := range names {
			if err := copyEntry(in, n, out, n, depth+1); err != nil {
				return err
			}
		}
		return nil
	case syscall.S_IFREG:
		in, err := sd.OpenFile(name, os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := dd.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, st.Mode&0777)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		tv := []syscall.Timeval{syscall.NsecToTimeval(st.Mtim.Nano()), syscall.NsecToTimeval(st.Mtim.Nano())}
		syscall.Futimes(int(out.Fd()), tv)
		return out.Close()
	}
	return nil // never follow links out of a task folder
}

// FreeSpace returns the free bytes of the volume holding p.
func FreeSpace(p string) int64 {
	var s syscall.Statfs_t
	for p != "/" && p != "" {
		if err := syscall.Statfs(p, &s); err == nil {
			return int64(s.Bavail) * int64(s.Bsize)
		}
		p = filepath.Dir(p)
	}
	return -1
}

// FolderOf is where "open folder" takes a task: the folder its data is in
// right now (the temporary folder while it downloads), with the file to
// select when the data is a single file. A folder that does not exist yet
// gives way to the nearest one that does.
func FolderOf(t *Task) (dir, file string) {
	p := t.DataPath
	if p == "" {
		name := t.Name
		if t.Kind != KindBT && t.Options.OutName != "" {
			name = t.Options.OutName
		}
		if name != "" {
			p = filepath.Join(t.SaveDir(), name)
		}
	}
	// A private staging folder cannot be opened in File Station: its
	// destination stands in for it
	if p != "" && browsable(p) {
		if st, err := os.Stat(p); err == nil {
			if st.IsDir() {
				return p, ""
			}
			return filepath.Dir(p), filepath.Base(p)
		}
	}
	for _, d := range []string{t.SaveDir(), t.finalDir()} {
		if !browsable(d) {
			continue
		}
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d, ""
		}
	}
	return t.TempDir, ""
}

// browsable reports a path outside the private staging folders.
func browsable(p string) bool { return !inStaging(p) }

// inStaging reports a path in a private staging folder
// (<volume root>/@DownloadCenterTemp/...).
func inStaging(p string) bool {
	i := strings.Index(p, "/"+TempDirName+"/")
	return i > 0 && isStagingBase(p[:i])
}

// inside reports whether p is dir or below it.
func inside(p, dir string) bool {
	p, dir = filepath.Clean(p), filepath.Clean(dir)
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// ownTemp reports a folder inside one of our @DownloadCenterTemp folders.
func ownTemp(work string) bool {
	return work != "" && strings.Contains(work, "/"+TempDirName+"/")
}

// removeTempDir deletes a task's temp folder and an empty
// @DownloadCenterTemp parent. A link on the way stops it: nothing is
// removed through a link.
func removeTempDir(work string) {
	if !ownTemp(work) {
		return
	}
	work = filepath.Clean(work)
	tmp := filepath.Dir(work)
	d, err := safefs.OpenDir(tmp)
	if err != nil {
		return
	}
	d.RemoveAll(filepath.Base(work))
	d.Close()
	// A volume's private staging folder stays: other tasks are made in it
	if base := filepath.Dir(tmp); !isStagingBase(base) {
		if p, err := safefs.OpenDir(base); err == nil {
			p.RemoveEmpty(TempDirName)
			p.Close()
		}
	}
}

// leaveTemp cleans up after a torrent's data moved out of its temporary
// folder: ours goes entirely, the official package's only where empty.
func leaveTemp(work string) {
	if ownTemp(work) {
		removeTempDir(work)
		return
	}
	d, name, err := safefs.OpenParent(work)
	if err != nil {
		return
	}
	removeEmptyDirs(d, name, 0)
	d.Close()
}

// removeEmptyDirs removes name in d and the folders below it that hold no
// files, without following links.
func removeEmptyDirs(d *safefs.Dir, name string, depth int) {
	sub, err := d.Sub(name)
	if err != nil {
		return
	}
	if names, err := sub.Names(); err == nil && depth < 64 {
		for _, n := range names {
			if st, err := sub.Lstat(n); err == nil && st.Mode&syscall.S_IFMT == syscall.S_IFDIR {
				removeEmptyDirs(sub, n, depth+1)
			}
		}
	}
	sub.Close()
	d.RemoveEmpty(name)
}

// removePartFiles deletes what libtorrent keeps of a torrent beside its
// data, ".<infohash>.parts" in the save folder (the pieces it shares with
// unselected files), once the torrent has left the engine for good. A v2
// torrent's part file is named after its truncated SHA-256 info hash;
// content-merged tasks have one per source.
func (m *Manager) removePartFiles(hash string, dirs ...string) {
	hashes := []string{hash}
	for _, s := range m.Sources(hash) {
		hashes = append(hashes, s.Hash)
	}
	var names []string
	for _, h := range hashes {
		names = append(names, h)
		if b, err := os.ReadFile(m.torrentPath(h)); err == nil {
			if meta, err := torrent.Parse(b); err == nil && len(meta.InfoHashV2) >= 40 {
				names = append(names, meta.InfoHashV2[:40])
			}
		}
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		d, err := safefs.OpenDir(dir)
		if err != nil {
			continue
		}
		for _, n := range names {
			p := "." + n + ".parts"
			if st, err := d.Lstat(p); err == nil && st.Mode&syscall.S_IFMT == syscall.S_IFREG {
				d.Remove(p)
			}
		}
		d.Close()
	}
}
