package update

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"downloadcenter/internal/release"
)

// Whether a release is a pre-release follows from its signed version, not
// from the feed's flag.
func TestPrereleaseFromVersion(t *testing.T) {
	f := newFixture(t)
	f.add("1.0.0", 1, false)
	f.add("1.1.0", 1, false)
	f.add("1.2.0-beta.1", 1, false) // the feed says it is not a pre-release
	s, _ := f.service("1.0.0")
	if err := s.Check(); err != nil {
		t.Fatal(err)
	}
	if v := s.Status(); v.Latest == nil || v.Latest.Version != "1.1.0" {
		t.Fatalf("latest %+v", v.Latest)
	}
}

// A feed that hides a database change does not get the database replaced
// without the administrator's consent: the signed layout decides.
func TestRestoreNeedsConsent(t *testing.T) {
	f := newFixture(t)
	f.add("0.9.0", 1, false)
	f.add("1.0.0", 2, false)
	for i := range f.feed.Releases {
		f.feed.Releases[i].Schema = 2 // the feed's word: no database change
	}
	b, _ := json.Marshal(f.feed)
	os.WriteFile(f.feedFn, b, 0644)
	s, launched := f.service("1.0.0")
	s.schema = 2
	os.MkdirAll(s.backupDir(), 0700)
	os.WriteFile(filepath.Join(s.backupDir(), "dc-0.9.0-s1-1700000000.db"), []byte("old db"), 0600)
	s.Check()
	if err := s.Install("0.9.0", false); err != nil {
		t.Fatal(err)
	}
	if j := wait(t, s, "install"); j.Phase != "failed" {
		t.Fatalf("job %+v", j)
	}
	if len(*launched) != 0 {
		t.Fatal("installed with a database restore nobody agreed to")
	}
}

// Versions that are not versions are not offered or installed, backups
// with other names are not used, and nothing in the installer script is
// read as shell code.
func TestInstallerScriptValues(t *testing.T) {
	f := newFixture(t)
	f.add("1.0.0", 1, false)
	s, _ := f.service("1.0.0")
	f.feed.Releases = append(f.feed.Releases, release.FeedRelease{Version: "9.9.9$(id)"})
	b, _ := json.Marshal(f.feed)
	os.WriteFile(f.feedFn, b, 0644)
	s.Check()
	for _, r := range s.feed.Releases {
		if !release.ValidVersion(r.Version) {
			t.Errorf("kept %q", r.Version)
		}
	}
	if err := s.Install("9.9.9$(id)", false); err != ErrNotFound {
		t.Errorf("install of a made-up version: %v", err)
	}
	os.MkdirAll(s.backupDir(), 0700)
	os.WriteFile(filepath.Join(s.backupDir(), "dc-$(touch x)-s1-1.db"), nil, 0600)
	if len(s.backups()) != 0 {
		t.Error("a backup with a foreign name is used")
	}

	work := t.TempDir()
	mark := filepath.Join(work, "PWNED")
	evil := "$(touch " + mark + ")`touch " + mark + "`'\"\n" + "; touch " + mark
	st := state{From: evil, To: evil, Restore: filepath.Join(work, "dc-"+evil+".db"), Started: 1}
	sc := s.script(st, filepath.Join(work, "pkg.qpkg"))
	if !strings.HasPrefix(sc, "#!/bin/sh\n") || !strings.Contains(sc, "umask 022\n") || !strings.Contains(sc, "unset TMPDIR\n") || !strings.Contains(sc, "\ncd /\n") {
		t.Errorf("script head:\n%s", sc)
	}
	if out, err := exec.Command("/bin/sh", "-n", "-c", sc).CombinedOutput(); err != nil {
		t.Fatalf("syntax: %v %s", err, out)
	}
	// Run what does not touch the package or the service
	var safe []string
	for _, l := range strings.Split(sc, "\n") {
		if strings.Contains(l, "DownloadCenter.sh") || strings.Contains(l, "getcfg") || strings.HasPrefix(l, "sh ") || strings.HasPrefix(l, "exec ") {
			continue
		}
		safe = append(safe, l)
	}
	cmd := exec.Command("/bin/sh", "-c", strings.Join(safe, "\n"))
	cmd.Dir = work
	out, _ := cmd.CombinedOutput()
	if _, err := os.Stat(mark); err == nil {
		t.Fatalf("a value ran as shell code:\n%s\n%s", sc, out)
	}
	if !strings.Contains(string(out), evil) {
		t.Errorf("values not printed as they are: %q", out)
	}
}

// Databases put aside by going back are deleted after 30 days, the newest
// two kept.
func TestPruneReplaced(t *testing.T) {
	f := newFixture(t)
	s, _ := f.service("1.0.0")
	os.MkdirAll(s.backupDir(), 0700)
	now := time.Now()
	ages := []int{40, 50, 60, 1}
	for _, d := range ages {
		os.WriteFile(filepath.Join(s.backupDir(), fmt.Sprintf("dc-replaced-%d.db", now.Add(-time.Duration(d)*24*time.Hour).Unix())), nil, 0600)
	}
	s.pruneReplaced(now)
	left, _ := filepath.Glob(filepath.Join(s.backupDir(), "dc-replaced-*.db"))
	if len(left) != 2 {
		t.Errorf("left %v", left)
	}
	want := fmt.Sprintf("dc-replaced-%d.db", now.Add(-40*24*time.Hour).Unix())
	found := false
	for _, l := range left {
		if filepath.Base(l) == want {
			found = true
		}
	}
	if !found {
		t.Errorf("the second newest is gone: %v", left)
	}
}
