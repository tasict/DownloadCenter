// Package safefs works on files as root inside folders that other accounts
// can change, without ever following a link. A path is opened one component
// at a time from "/" with O_NOFOLLOW, and every change is made relative to a
// directory that is already open (mkdirat, renameat, unlinkat, fchown on an
// open descriptor). A component swapped for a link after a check therefore
// cannot lead an operation anywhere else: it fails instead.
package safefs

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// Linux values the syscall package does not export for every architecture
// (the same on amd64, arm64 and arm).
const (
	oPath       = 0x200000 // O_PATH
	atFdCwd     = -100     // AT_FDCWD
	atRemoveDir = 0x200    // AT_REMOVEDIR
	atEmptyPath = 0x1000   // AT_EMPTY_PATH
	maxDepth    = 256
)

var (
	// ErrLink: a component of the path is a link (or not a folder where a
	// folder is expected).
	ErrLink = errors.New("a link or a file is in the place of a folder")
	// ErrOwner: a folder that only root may change belongs to someone else.
	ErrOwner = errors.New("folder not owned by root")
	// ErrNotRegular: not a regular file.
	ErrNotRegular = errors.New("not a regular file")
	// ErrName: not a single path component.
	ErrName = errors.New("invalid file name")
	// ErrDepth: folders nested too deeply.
	ErrDepth = errors.New("folders nested too deeply")
)

// Dir is a folder opened without following any link.
type Dir struct {
	f    *os.File
	path string
}

// Path is the path the folder was opened as. No component of it was a link
// at that time.
func (d *Dir) Path() string { return d.path }

// Close releases the folder.
func (d *Dir) Close() error {
	if d == nil || d.f == nil {
		return nil
	}
	return d.f.Close()
}

func (d *Dir) fd() int { return int(d.f.Fd()) }

// fstatOPath is fstat of an O_PATH descriptor (tests make it fail as on
// kernels before 3.6).
var fstatOPath = syscall.Fstat

// trustedUID is the one account besides root whose folders OpenRootDir
// accepts: root itself (tests not run by root trust their own account).
var trustedUID uint32

func openat(dirfd int, name string, flags int, perm uint32) (int, error) {
	for {
		fd, err := syscall.Openat(dirfd, name, flags|syscall.O_CLOEXEC, perm)
		if err != syscall.EINTR {
			return fd, err
		}
	}
}

func validName(n string) bool {
	return n != "" && n != "." && n != ".." && !strings.ContainsAny(n, "/\x00")
}

// linkErr turns what openat answers for a link (or a non-folder) where a
// folder was expected into ErrLink.
func linkErr(err error) error {
	if err == syscall.ELOOP || err == syscall.ENOTDIR {
		return ErrLink
	}
	return err
}

// OpenDir opens the folder at the absolute path p. No component may be a
// link.
func OpenDir(p string) (*Dir, error) { return open(p, false) }

// OpenRootDir opens the folder at the absolute path p like OpenDir; every
// component, the last one included, must also belong to root, so that no
// other account can have changed what the path names.
func OpenRootDir(p string) (*Dir, error) { return open(p, true) }

func open(p string, rootOwned bool) (*Dir, error) {
	if !filepath.IsAbs(p) {
		return nil, ErrName
	}
	p = filepath.Clean(p)
	fd, err := openat(atFdCwd, "/", syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	d := &Dir{f: os.NewFile(uintptr(fd), "/"), path: "/"}
	if rootOwned {
		if err := d.rootOwned(); err != nil {
			d.Close()
			return nil, err
		}
	}
	for _, c := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
		if c == "" {
			continue
		}
		nd, err := d.Sub(c)
		d.Close()
		if err != nil {
			return nil, err
		}
		d = nd
		if rootOwned {
			if err := d.rootOwned(); err != nil {
				d.Close()
				return nil, err
			}
		}
	}
	return d, nil
}

func (d *Dir) rootOwned() error {
	st, err := d.Stat()
	if err != nil {
		return err
	}
	if st.Uid != 0 && st.Uid != trustedUID {
		return ErrOwner
	}
	return nil
}

// Sub opens the folder name in d without following a link.
func (d *Dir) Sub(name string) (*Dir, error) {
	if !validName(name) {
		return nil, ErrName
	}
	fd, err := openat(d.fd(), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, linkErr(err)
	}
	return &Dir{f: os.NewFile(uintptr(fd), name), path: filepath.Join(d.path, name)}, nil
}

// Walk opens the folder rel (a relative path, no "..") below d, one
// component at a time.
func (d *Dir) Walk(rel string) (*Dir, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return nil, ErrName
	}
	cur := d
	for _, c := range strings.Split(rel, "/") {
		if c == "" {
			continue
		}
		nd, err := cur.Sub(c)
		if cur != d {
			cur.Close()
		}
		if err != nil {
			return nil, err
		}
		cur = nd
	}
	if cur == d {
		return nil, ErrName
	}
	return cur, nil
}

// Mkdir opens the folder name in d, creating it with perm when it does not
// exist (created reports that). An existing entry must be a real folder.
func (d *Dir) Mkdir(name string, perm uint32) (sub *Dir, created bool, err error) {
	if !validName(name) {
		return nil, false, ErrName
	}
	err = syscall.Mkdirat(d.fd(), name, perm)
	if err != nil && err != syscall.EEXIST {
		return nil, false, err
	}
	created = err == nil
	sub, err = d.Sub(name)
	return sub, created, err
}

// Stat is the status of the folder itself.
func (d *Dir) Stat() (*syscall.Stat_t, error) {
	var st syscall.Stat_t
	if err := syscall.Fstat(d.fd(), &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// Chmod sets the folder's permission bits.
func (d *Dir) Chmod(mode uint32) error { return syscall.Fchmod(d.fd(), mode) }

// Chown gives the folder itself to uid:gid.
func (d *Dir) Chown(uid, gid int) error { return syscall.Fchown(d.fd(), uid, gid) }

// Lstat is the status of name in d; a link is reported as a link.
func (d *Dir) Lstat(name string) (*syscall.Stat_t, error) {
	if !validName(name) {
		return nil, ErrName
	}
	var st syscall.Stat_t
	fd, err := openat(d.fd(), name, oPath|syscall.O_NOFOLLOW, 0)
	if err == nil {
		err = fstatOPath(fd, &st)
		syscall.Close(fd)
		if err == nil {
			return &st, nil
		}
	}
	if err != syscall.EBADF && err != syscall.ELOOP {
		return nil, err
	}
	// Kernels before 3.6 cannot fstat an O_PATH descriptor (and before
	// 2.6.39 do not know O_PATH): the name is looked up in the open folder
	// through /proc, its last component not followed
	if err := syscall.Lstat("/proc/self/fd/"+strconv.Itoa(d.fd())+"/"+name, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// Exists reports whether d has an entry called name (of any kind).
func (d *Dir) Exists(name string) bool {
	_, err := d.Lstat(name)
	return err == nil
}

// Names lists the entries of d.
func (d *Dir) Names() ([]string, error) {
	if _, err := d.f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return d.f.Readdirnames(-1)
}

// OpenFile opens the regular file name in d without following a link (flag
// as for os.OpenFile; O_NOFOLLOW is added). Anything but a regular file is
// refused.
func (d *Dir) OpenFile(name string, flag int, perm uint32) (*os.File, error) {
	if !validName(name) {
		return nil, ErrName
	}
	// O_NONBLOCK: opening a FIFO someone put there must not hang
	fd, err := openat(d.fd(), name, flag|syscall.O_NOFOLLOW|syscall.O_NOCTTY|syscall.O_NONBLOCK, perm)
	if err != nil {
		if err == syscall.ELOOP {
			return nil, ErrNotRegular
		}
		return nil, err
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		syscall.Close(fd)
		return nil, ErrNotRegular
	}
	syscall.SetNonblock(fd, false)
	return os.NewFile(uintptr(fd), filepath.Join(d.path, name)), nil
}

// ReadFile reads the regular file name in d, at most max bytes.
func (d *Dir) ReadFile(name string, max int64) ([]byte, error) {
	f, err := d.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, errors.New("file too large")
	}
	return b, nil
}

// Rename moves name in d to newName in to (renameat: neither name is
// followed if it is a link).
func (d *Dir) Rename(name string, to *Dir, newName string) error {
	if !validName(name) || !validName(newName) {
		return ErrName
	}
	return syscall.Renameat(d.fd(), name, to.fd(), newName)
}

func unlinkat(dirfd int, name string, flags int) error {
	p, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, e := syscall.Syscall(syscall.SYS_UNLINKAT, uintptr(dirfd), uintptr(unsafe.Pointer(p)), uintptr(flags))
	if e != 0 {
		return e
	}
	return nil
}

// Remove removes the file, link or empty folder name from d.
func (d *Dir) Remove(name string) error {
	if !validName(name) {
		return ErrName
	}
	err := unlinkat(d.fd(), name, 0)
	if err == syscall.EISDIR || err == syscall.EPERM {
		if e2 := unlinkat(d.fd(), name, atRemoveDir); e2 != syscall.ENOTDIR {
			return e2
		}
	}
	return err
}

// RemoveEmpty removes name from d only when it is an empty folder.
func (d *Dir) RemoveEmpty(name string) error {
	if !validName(name) {
		return ErrName
	}
	return unlinkat(d.fd(), name, atRemoveDir)
}

// RemoveAll removes name from d and, for a folder, everything below it. A
// link is removed itself; nothing is ever followed. A missing name is not
// an error.
func (d *Dir) RemoveAll(name string) error { return d.removeAll(name, 0) }

func (d *Dir) removeAll(name string, depth int) error {
	if !validName(name) {
		return ErrName
	}
	err := unlinkat(d.fd(), name, 0)
	if err == nil || err == syscall.ENOENT {
		return nil
	}
	if err != syscall.EISDIR && err != syscall.EPERM {
		return err
	}
	if depth >= maxDepth {
		return ErrDepth
	}
	sub, err := d.Sub(name)
	if err != nil {
		if err == syscall.ENOENT {
			return nil
		}
		return err
	}
	names, err := sub.Names()
	var first error
	if err != nil {
		first = err
	}
	for _, n := range names {
		if e := sub.removeAll(n, depth+1); e != nil && first == nil {
			first = e
		}
	}
	sub.Close()
	if e := unlinkat(d.fd(), name, atRemoveDir); e != nil && e != syscall.ENOENT && first == nil {
		first = e
	}
	return first
}

// ChownAll gives name in d to uid:gid, and everything below it when it is a
// folder (recursive). Links and special files are left alone, and so is a
// regular file with more than one name that does not belong to uid already:
// it is also known elsewhere. Every change goes through a descriptor of
// what was checked, never through a name.
func (d *Dir) ChownAll(name string, uid, gid int, recursive bool) error {
	return d.chown(name, uid, gid, recursive, 0)
}

func (d *Dir) chown(name string, uid, gid int, recursive bool, depth int) error {
	if !validName(name) {
		return ErrName
	}
	fd, err := openat(d.fd(), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err == nil {
		sub := &Dir{f: os.NewFile(uintptr(fd), name), path: filepath.Join(d.path, name)}
		defer sub.Close()
		var first error
		if recursive {
			if depth >= maxDepth {
				return ErrDepth
			}
			names, err := sub.Names()
			if err != nil {
				first = err
			}
			for _, n := range names {
				if e := sub.chown(n, uid, gid, true, depth+1); e != nil && first == nil {
					first = e
				}
			}
		}
		// The folder itself last: until then only root may enter a new one
		if e := sub.Chown(uid, gid); e != nil && first == nil {
			first = e
		}
		return first
	}
	if err == syscall.ELOOP {
		return nil // a link: left as it is
	}
	if err != syscall.ENOTDIR {
		return err
	}
	pfd, err := openat(d.fd(), name, oPath|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if err == syscall.ELOOP {
			return nil // a link (a kernel without O_PATH says so)
		}
		return err
	}
	defer syscall.Close(pfd)
	var st syscall.Stat_t
	if err := fstatOPath(pfd, &st); err != nil {
		if err != syscall.EBADF {
			return err
		}
		// A kernel before 3.6 (no fstat of an O_PATH descriptor): the file
		// itself is opened, without following a link, and changed through
		// that descriptor
		rfd, err := openat(d.fd(), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
		if err != nil {
			if err == syscall.ELOOP {
				return nil
			}
			return err
		}
		defer syscall.Close(rfd)
		if err := syscall.Fstat(rfd, &st); err != nil {
			return err
		}
		if !chownable(&st, uid) {
			return nil
		}
		return syscall.Fchown(rfd, uid, gid)
	}
	if !chownable(&st, uid) {
		return nil
	}
	return syscall.Fchownat(pfd, "", uid, gid, atEmptyPath)
}

// chownable: a regular file that is not also known by another name, unless
// it belongs to uid already.
func chownable(st *syscall.Stat_t, uid int) bool {
	return st.Mode&syscall.S_IFMT == syscall.S_IFREG && (uint64(st.Nlink) == 1 || int(st.Uid) == uid)
}

// FdPath is the path the kernel reports for an open file (what it really
// is now, links resolved).
func FdPath(f *os.File) (string, error) {
	return os.Readlink("/proc/self/fd/" + strconv.Itoa(int(f.Fd())))
}

// --- path helpers ---

// OpenParent opens the folder holding the absolute path p and returns it
// with p's last component.
func OpenParent(p string) (*Dir, string, error) {
	p = filepath.Clean(p)
	name := filepath.Base(p)
	if !validName(name) {
		return nil, "", ErrName
	}
	d, err := OpenDir(filepath.Dir(p))
	if err != nil {
		return nil, "", err
	}
	return d, name, nil
}

// MkdirAll opens the folder at the absolute path p, creating the
// components that are missing with perm. No existing component may be a
// link.
func MkdirAll(p string, perm uint32) (*Dir, error) {
	if !filepath.IsAbs(p) {
		return nil, ErrName
	}
	p = filepath.Clean(p)
	fd, err := openat(atFdCwd, "/", syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	d := &Dir{f: os.NewFile(uintptr(fd), "/"), path: "/"}
	for _, c := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
		if c == "" {
			continue
		}
		nd, _, err := d.Mkdir(c, perm)
		d.Close()
		if err != nil {
			return nil, err
		}
		d = nd
	}
	return d, nil
}

// RemoveAllPath removes the absolute path p (see Dir.RemoveAll); a link
// anywhere on the way to it makes it fail without removing anything.
func RemoveAllPath(p string) error {
	d, name, err := OpenParent(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer d.Close()
	return d.RemoveAll(name)
}

// ChownPath is Dir.ChownAll for the absolute path p.
func ChownPath(p string, uid, gid int, recursive bool) error {
	d, name, err := OpenParent(p)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.ChownAll(name, uid, gid, recursive)
}
