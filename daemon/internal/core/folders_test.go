package core

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"downloadcenter/internal/qts"
)

func TestNaturalLess(t *testing.T) {
	in := []string{"Season 10", "x", "season 2", "file1", "B", "10", "File1", "Season 1", "file01", "a", "9"}
	sort.Slice(in, func(i, j int) bool { return NaturalLess(in[i], in[j]) })
	want := []string{"9", "10", "a", "B", "File1", "file01", "file1", "Season 1", "season 2", "Season 10", "x"}
	if !reflect.DeepEqual(in, want) {
		t.Errorf("got %q, want %q", in, want)
	}
	if NaturalLess("a", "a") {
		t.Error("a name must not sort before itself")
	}
}

func TestValidFolderName(t *testing.T) {
	for _, n := range []string{"Movies", "新資料夾", "a.b", "Season 2", "x-y_z (1)"} {
		if !ValidFolderName(n) {
			t.Errorf("%q refused", n)
		}
	}
	for _, n := range []string{"", ".hidden", "@sys", "#recycle", "end.", "end ", "a/b", `a\b`, "a:b", "a*b", "a\x01b", strings.Repeat("a", 256)} {
		if ValidFolderName(n) {
			t.Errorf("%q accepted", n)
		}
	}
}

func TestOwnerFor(t *testing.T) {
	if u := ownerFor("joe", false, "/anywhere/file"); u != "joe" {
		t.Errorf("a regular user's data goes to %q", u)
	}
	if u := ownerFor("admin", true, "/nowhere/file"); u != "" {
		t.Errorf("an administrator's data outside the homes goes to %q", u)
	}
}

// Links inside a share must not lead the pickers, or downloads, out of the
// shared folders. Runs on a NAS with a writable share.
func TestFolderLinks(t *testing.T) {
	var sh qts.Share
	for _, s := range qts.Shares() {
		if Writable(s.Path) {
			sh = s
			break
		}
	}
	if sh.Path == "" || os.Geteuid() != 0 {
		t.Skip("needs a writable shared folder")
	}
	base := filepath.Join(sh.Path, TempDirName)
	made := !pathExists(base)
	if err := os.MkdirAll(base, 0777); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "test-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		os.RemoveAll(dir)
		if made {
			os.Remove(base)
		}
	}()
	os.Mkdir(filepath.Join(dir, "sub"), 0777)
	os.Mkdir(filepath.Join(dir, ".hidden"), 0777)
	os.WriteFile(filepath.Join(dir, "file"), nil, 0666)
	if err := os.Symlink("/etc", filepath.Join(dir, "out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sh.Path, filepath.Join(dir, "in")); err != nil {
		t.Fatal(err)
	}
	display := sh.Name + "/" + TempDirName + "/" + filepath.Base(dir)
	m := &Manager{}
	real, err := m.ResolvePath("", display)
	if err != nil || real != dir {
		t.Fatalf("resolve %s = %q, %v", display, real, err)
	}
	if p, err := m.ResolvePath("", display+"/out"); err == nil {
		t.Errorf("a link out of the shares resolved to %s", p)
	}
	if p, err := m.ResolvePath("", display+"/in"); err != nil || p != sh.Path {
		t.Errorf("a link to a share resolved to %q, %v", p, err)
	}
	ls, err := ListFolders(real, display)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range ls {
		names = append(names, f.Name)
		if f.Path != display+"/"+f.Name || !f.Writable || !f.Choosable {
			t.Errorf("entry %+v", f)
		}
	}
	if !reflect.DeepEqual(names, []string{"in", "sub"}) {
		t.Errorf("listed %q, want [in sub]", names)
	}
	p, err := m.MakeFolder("", display, "New 2")
	if err != nil || p != display+"/New 2" || !pathExists(filepath.Join(dir, "New 2")) {
		t.Errorf("make folder = %q, %v", p, err)
	}
	if _, err := m.MakeFolder("", display, "New 2"); !errors.Is(err, ErrExists) {
		t.Errorf("same name again: %v", err)
	}
	if _, err := m.MakeFolder("", display, ".x"); !errors.Is(err, ErrBadName) {
		t.Errorf("hidden name: %v", err)
	}
	if _, err := m.MakeFolder("", display+"/out", "x"); !errors.Is(err, ErrNoFolder) {
		t.Errorf("through a link out of the shares: %v", err)
	}
}
