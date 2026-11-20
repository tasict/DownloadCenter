package safefs

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// user is the account a scratch tree belongs to: no NAS account has it.
const user = 60123

func uidOf(t *testing.T, p string) uint32 {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Lstat(p, &st); err != nil {
		t.Fatal(err)
	}
	return st.Uid
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// tree makes root/home (owned by user) and root/outside/secret (root's).
// Not run by root, home stays the test's own.
func tree(t *testing.T) (home, outside, secret string) {
	root := t.TempDir()
	home = filepath.Join(root, "home")
	outside = filepath.Join(root, "outside")
	must(t, os.MkdirAll(home, 0777))
	must(t, os.MkdirAll(outside, 0700))
	secret = filepath.Join(outside, "secret")
	must(t, os.WriteFile(secret, []byte("SECRET"), 0600))
	if os.Geteuid() == 0 {
		must(t, os.Chown(home, user, user))
	}
	return
}

func TestOpenDirRefusesLinks(t *testing.T) {
	home, outside, _ := tree(t)
	must(t, os.Symlink(outside, filepath.Join(home, "Download")))
	for _, p := range []string{filepath.Join(home, "Download"), filepath.Join(home, "Download", "x")} {
		if d, err := OpenDir(p); err == nil || !errors.Is(err, ErrLink) && !os.IsNotExist(err) {
			d.Close()
			t.Errorf("%s: %v, want a refusal", p, err)
		}
	}
	if _, err := OpenDir(filepath.Join(home, "Download")); !errors.Is(err, ErrLink) {
		t.Errorf("last component a link: %v", err)
	}
	if _, err := MkdirAll(filepath.Join(home, "Download", "new"), 0755); !errors.Is(err, ErrLink) {
		t.Errorf("MkdirAll through a link: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); err == nil {
		t.Error("MkdirAll created a folder through the link")
	}
	d, err := OpenDir(home)
	must(t, err)
	defer d.Close()
	if d.Path() != filepath.Clean(home) {
		t.Errorf("path %q", d.Path())
	}
}

func TestOpenRootDir(t *testing.T) {
	home, outside, _ := tree(t)
	d, err := OpenRootDir(outside)
	must(t, err)
	d.Close()
	link := filepath.Join(filepath.Dir(outside), "l")
	must(t, os.Symlink(outside, link))
	if _, err := OpenRootDir(link); !errors.Is(err, ErrLink) {
		t.Errorf("a link: %v", err)
	}
	needRoot(t)
	if _, err := OpenRootDir(home); !errors.Is(err, ErrOwner) {
		t.Errorf("a user's folder: %v", err)
	}
	if _, err := OpenRootDir(filepath.Join(home, "x")); !errors.Is(err, ErrOwner) {
		t.Errorf("below a user's folder: %v", err)
	}
}

func TestRemoveAllNeverFollows(t *testing.T) {
	home, outside, secret := tree(t)
	work := filepath.Join(home, "work")
	must(t, os.MkdirAll(filepath.Join(work, "sub"), 0777))
	must(t, os.WriteFile(filepath.Join(work, "sub", "f"), []byte("x"), 0644))
	must(t, os.Symlink(outside, filepath.Join(work, "sub", "dirlink")))
	must(t, os.Symlink(secret, filepath.Join(work, "filelink")))
	must(t, RemoveAllPath(work))
	if _, err := os.Lstat(work); !os.IsNotExist(err) {
		t.Errorf("work still there: %v", err)
	}
	if b, err := os.ReadFile(secret); err != nil || string(b) != "SECRET" {
		t.Errorf("outside file: %q %v", b, err)
	}
	// A link on the way: nothing is removed
	must(t, os.Symlink(outside, filepath.Join(home, "via")))
	if err := RemoveAllPath(filepath.Join(home, "via", "secret")); !errors.Is(err, ErrLink) {
		t.Errorf("through a link: %v", err)
	}
	if _, err := os.Stat(secret); err != nil {
		t.Errorf("outside file removed through a link: %v", err)
	}
}

func TestChownAll(t *testing.T) {
	needRoot(t)
	home, outside, secret := tree(t)
	data := filepath.Join(home, "data")
	must(t, os.MkdirAll(filepath.Join(data, "sub"), 0755))
	must(t, os.Chown(data, 0, 0))
	plain := filepath.Join(data, "sub", "movie.mp4")
	must(t, os.WriteFile(plain, []byte("x"), 0644))
	hard := filepath.Join(data, "sub", "hard.mp4")
	must(t, os.Link(secret, hard))
	link := filepath.Join(data, "link")
	must(t, os.Symlink(outside, link))
	must(t, ChownPath(data, user, user, true))
	if uidOf(t, plain) != user || uidOf(t, data) != user || uidOf(t, filepath.Join(data, "sub")) != user {
		t.Error("the task's own files were not handed over")
	}
	if uidOf(t, secret) != 0 {
		t.Errorf("a hard link handed over a file known elsewhere (uid %d)", uidOf(t, secret))
	}
	if uidOf(t, link) != 0 || uidOf(t, outside) != 0 {
		t.Error("a link or its target changed owner")
	}
	// The top itself a link: nothing changes
	must(t, os.Symlink(outside, filepath.Join(home, "top")))
	must(t, ChownPath(filepath.Join(home, "top"), user, user, true))
	if uidOf(t, outside) != 0 || uidOf(t, secret) != 0 {
		t.Error("followed a link at the top")
	}
}

func TestOpenFile(t *testing.T) {
	home, _, secret := tree(t)
	d, err := OpenDir(home)
	must(t, err)
	defer d.Close()
	must(t, os.Symlink(secret, filepath.Join(home, "l")))
	if _, err := d.OpenFile("l", os.O_RDONLY, 0); !errors.Is(err, ErrNotRegular) {
		t.Errorf("link: %v", err)
	}
	must(t, syscall.Mkfifo(filepath.Join(home, "fifo"), 0666))
	if _, err := d.OpenFile("fifo", os.O_RDONLY, 0); !errors.Is(err, ErrNotRegular) {
		t.Errorf("fifo: %v", err)
	}
	if _, err := d.OpenFile("../outside/secret", os.O_RDONLY, 0); !errors.Is(err, ErrName) {
		t.Errorf("a path: %v", err)
	}
	f, err := d.OpenFile("new", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	must(t, err)
	f.WriteString("ok")
	f.Close()
	if b, err := d.ReadFile("new", 10); err != nil || string(b) != "ok" {
		t.Errorf("read back %q %v", b, err)
	}
	if p, err := func() (string, error) {
		f, err := d.OpenFile("new", os.O_RDONLY, 0)
		if err != nil {
			return "", err
		}
		defer f.Close()
		return FdPath(f)
	}(); err != nil || p != filepath.Join(home, "new") {
		t.Errorf("fd path %q %v", p, err)
	}
}

func TestRenameAndRemove(t *testing.T) {
	home, outside, secret := tree(t)
	a, err := MkdirAll(filepath.Join(home, "a"), 0755)
	must(t, err)
	defer a.Close()
	b, created, err := a.Mkdir("b", 0700)
	must(t, err)
	defer b.Close()
	if !created || a.Exists("c") {
		t.Error("mkdir")
	}
	must(t, os.WriteFile(filepath.Join(home, "a", "f"), []byte("x"), 0644))
	must(t, a.Rename("f", b, "g"))
	if _, err := os.Stat(filepath.Join(home, "a", "b", "g")); err != nil {
		t.Error(err)
	}
	must(t, os.Symlink(outside, filepath.Join(home, "a", "l")))
	if err := a.RemoveEmpty("l"); err == nil {
		t.Error("RemoveEmpty removed a link")
	}
	must(t, a.Remove("l"))
	if _, err := os.Stat(secret); err != nil {
		t.Error("removing a link touched its target")
	}
	if err := a.RemoveEmpty("b"); err == nil {
		t.Error("RemoveEmpty removed a folder with a file")
	}
	if names, err := b.Names(); err != nil || len(names) != 1 || names[0] != "g" {
		t.Errorf("names %v %v", names, err)
	}
	if w, err := (&Dir{}).Walk(""); err == nil || w != nil {
		t.Error("empty walk")
	}
	w, err := a.Walk("b")
	must(t, err)
	w.Close()
	if _, err := a.Walk("../a"); !errors.Is(err, ErrName) {
		t.Errorf("walk up: %v", err)
	}
}

// The same rules hold where the kernel cannot fstat an O_PATH descriptor.
func TestOldKernel(t *testing.T) {
	old := fstatOPath
	fstatOPath = func(int, *syscall.Stat_t) error { return syscall.EBADF }
	defer func() { fstatOPath = old }()
	t.Run("RemoveAll", TestRemoveAllNeverFollows)
	t.Run("ChownAll", TestChownAll)
	t.Run("OpenFile", TestOpenFile)
	t.Run("RenameAndRemove", TestRenameAndRemove)
	home, outside, _ := tree(t)
	os.Symlink(outside, filepath.Join(home, "l"))
	d, err := OpenDir(home)
	must(t, err)
	defer d.Close()
	if st, err := d.Lstat("l"); err != nil || st.Mode&syscall.S_IFMT != syscall.S_IFLNK {
		t.Errorf("lstat of a link: %v", err)
	}
}
