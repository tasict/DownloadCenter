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
	"downloadcenter/internal/torrent"
)

// TempDirName is the folder at the root of a share where downloads stay
// until they are complete.
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
	if hr := qts.HomesRoot(); hr != "" && (p == hr || strings.HasPrefix(p, hr+"/")) {
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
			h := qts.HomeDir(viewer)
			if h == "" {
				return "", ErrFolder
			}
			real = filepath.Join(h, rest)
		case first == "homes":
			hr := qts.HomesRoot()
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
// handed to the user when missing.
func (m *Manager) UserDownloadDir(user string) string {
	if h := qts.HomeDir(user); h != "" {
		d := filepath.Join(h, "Download")
		if st, err := os.Lstat(d); err != nil {
			os.Mkdir(d, 0755)
			chownPath(d, user, false)
		} else if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
			// The engines write as admin: never follow a link the user
			// controls out of the home folder
			if real, err := filepath.EvalSymlinks(d); err != nil || !inside(real, h) || real == h {
				return h
			}
			return d
		}
		return d
	}
	return qts.PublicDir()
}

// workDirFor returns where a task downloads: @DownloadCenterTemp/<hash> at
// the root of the temporary location's share (inside home/Download for home
// folders).
func (m *Manager) workDirFor(tempDir, hash string) string {
	root := tempDir
	if hr := qts.HomesRoot(); hr != "" && strings.HasPrefix(tempDir, hr+"/") {
		rest := strings.TrimPrefix(tempDir, hr+"/")
		user, sub, _ := strings.Cut(rest, "/")
		root = filepath.Join(hr, user)
		if strings.HasPrefix(sub, "Download") {
			root = filepath.Join(hr, user, "Download")
		}
	} else if sh, ok := qts.ShareOf(tempDir); ok {
		root = sh.Path
	}
	return filepath.Join(root, TempDirName, hash)
}

// chownPath gives a path (recursively) to a QTS account.
func chownPath(p, user string, recursive bool) error {
	uid, gid, ok := qts.Lookup(user)
	if !ok {
		return fmt.Errorf("unknown user %s", user)
	}
	if !recursive {
		return os.Lchown(p, uid, gid)
	}
	return filepath.Walk(p, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		os.Lchown(path, uid, gid)
		return nil
	})
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
func moveInto(src, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	dst := uniquePath(filepath.Join(dir, filepath.Base(src)))
	if err := os.Rename(src, dst); err == nil {
		return dst, nil
	} else if !isCrossDevice(err) {
		return "", err
	}
	// Copy under a temporary name first: an interrupted copy never looks
	// like a finished file
	part := filepath.Join(dir, ".dc-part-"+filepath.Base(dst))
	os.RemoveAll(part)
	if err := copyTree(src, part); err != nil {
		os.RemoveAll(part)
		return "", err
	}
	dst = uniquePath(dst)
	if err := os.Rename(part, dst); err != nil {
		os.RemoveAll(part)
		return "", err
	}
	os.RemoveAll(src)
	return dst, nil
}

func isCrossDevice(err error) bool {
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Err == syscall.EXDEV
	}
	return false
}

func copyTree(src, dst string) error {
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case st.Mode()&os.ModeSymlink != 0:
		return nil // never follow links out of a task folder
	case st.IsDir():
		if err := os.MkdirAll(dst, st.Mode().Perm()|0700); err != nil {
			return err
		}
		ents, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range ents {
			if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return nil
	default:
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, st.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		os.Chtimes(dst, st.ModTime(), st.ModTime())
		return out.Close()
	}
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
	if p != "" {
		if st, err := os.Stat(p); err == nil {
			if st.IsDir() {
				return p, ""
			}
			return filepath.Dir(p), filepath.Base(p)
		}
	}
	for _, d := range []string{t.SaveDir(), t.finalDir()} {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d, ""
		}
	}
	return t.TempDir, ""
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
// @DownloadCenterTemp parent.
func removeTempDir(work string) {
	if !ownTemp(work) {
		return
	}
	os.RemoveAll(work)
	os.Remove(filepath.Dir(work))
}

// leaveTemp cleans up after a torrent's data moved out of its temporary
// folder: ours goes entirely, the official package's only where empty.
func leaveTemp(work string) {
	if ownTemp(work) {
		removeTempDir(work)
		return
	}
	removeEmptyDirs(work)
}

// removeEmptyDirs removes dir and the folders below it that hold no files.
func removeEmptyDirs(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if e.IsDir() {
			removeEmptyDirs(filepath.Join(dir, e.Name()))
		}
	}
	os.Remove(dir)
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
		for _, n := range names {
			p := filepath.Join(dir, "."+n+".parts")
			if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() {
				os.Remove(p)
			}
		}
	}
}
