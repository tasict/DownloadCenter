package update

import (
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"downloadcenter/internal/release"
	"downloadcenter/internal/store"
)

// The public key compiled into dcd must be the one releases are signed with.
func TestPublicKeyMatchesRepository(t *testing.T) {
	b, err := os.ReadFile("../../../tools/release-key.pub")
	if err != nil {
		t.Skip("tools/release-key.pub not found")
	}
	if strings.TrimSpace(string(b)) != PublicKey {
		t.Fatal("update.PublicKey differs from tools/release-key.pub")
	}
	if _, err := release.ParsePublic([]byte(PublicKey)); err != nil {
		t.Fatal(err)
	}
}

// fixture builds a signed release in dir and a feed for it.
type fixture struct {
	t      *testing.T
	dir    string
	priv   ed25519.PrivateKey
	pub    ed25519.PublicKey
	feed   release.Feed
	feedFn string
}

func newFixture(t *testing.T) *fixture {
	pubFile, privFile, _ := release.GenerateKey()
	pub, _ := release.ParsePublic(pubFile)
	priv, _ := release.ParsePrivate(privFile)
	f := &fixture{t: t, dir: t.TempDir(), priv: priv, pub: pub, feed: release.Feed{Schema: 1, Releases: []release.FeedRelease{}}}
	f.feedFn = filepath.Join(f.dir, "updates.json")
	return f
}

// add publishes version v (schema sc) with a package for x86_64 only, newest first.
func (f *fixture) add(v string, sc int, pre bool) {
	d := filepath.Join(f.dir, v)
	os.MkdirAll(d, 0755)
	pkg := release.AssetName(v, "x86_64")
	os.WriteFile(filepath.Join(d, pkg), []byte("package "+v), 0644)
	info, _ := json.Marshal(release.Info{Version: v, Schema: sc})
	os.WriteFile(filepath.Join(d, release.InfoName), info, 0644)
	hp, _, _ := release.HashFile(filepath.Join(d, pkg))
	hi, _, _ := release.HashFile(filepath.Join(d, release.InfoName))
	sums := release.FormatSums(map[string]string{pkg: hp, release.InfoName: hi})
	os.WriteFile(filepath.Join(d, release.SumsName), sums, 0644)
	os.WriteFile(filepath.Join(d, release.SigName), release.Sign(f.priv, sums), 0644)
	r := release.FeedRelease{Version: v, Tag: "v" + v, Date: "2026-10-03", Prerelease: pre, Schema: sc, Notes: "notes " + v,
		SumsURL: "file://" + filepath.Join(d, release.SumsName), SigURL: "file://" + filepath.Join(d, release.SigName),
		Assets: map[string]release.FeedAsset{"x86_64": {Name: pkg, URL: "file://" + filepath.Join(d, pkg), Size: int64(len("package " + v)), SHA256: hp}}}
	f.feed.Releases = append([]release.FeedRelease{r}, f.feed.Releases...)
	b, _ := json.Marshal(f.feed)
	os.WriteFile(f.feedFn, b, 0644)
}

// service makes an installation of version cur that reads the fixture's feed.
func (f *fixture) service(cur string) (*Service, *[]string) {
	root := filepath.Join(f.t.TempDir(), "DownloadCenter")
	data := filepath.Join(root, "data")
	os.MkdirAll(filepath.Join(root, "bin"), 0755)
	os.MkdirAll(filepath.Join(data, "logs"), 0700)
	os.WriteFile(filepath.Join(root, "bin", "arch"), []byte("x86_64\n"), 0644)
	os.WriteFile(filepath.Join(data, "update_feed"), []byte("file://"+f.feedFn+"\n"), 0600)
	db, err := store.Open(filepath.Join(data, "dc.db"))
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { db.Close() })
	s := New(nil, db, root, data, cur)
	s.pub = f.pub
	var launched []string
	s.Launch = func(script string) error { launched = append(launched, script); return nil }
	return s, &launched
}

func wait(t *testing.T, s *Service, phase string) Job {
	for i := 0; i < 200; i++ {
		j := s.Status().Job
		if j.Phase == phase || j.Phase == "failed" {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job never reached %s: %+v", phase, s.Status().Job)
	return Job{}
}

func TestStatusAndUpgrade(t *testing.T) {
	f := newFixture(t)
	f.add("1.0.0", 1, false)
	f.add("1.1.0", 1, false)
	f.add("1.2.0-beta.1", 1, true)
	s, launched := f.service("1.0.0")
	if err := s.Check(); err != nil {
		t.Fatal(err)
	}
	v := s.Status()
	if !v.Available || v.Latest == nil || v.Latest.Version != "1.1.0" {
		t.Fatalf("latest = %+v, available %v", v.Latest, v.Available)
	}
	if len(v.Releases) != 2 {
		t.Fatalf("pre-release shown without the channel: %+v", v.Releases)
	}
	pre := true
	s.Settings(nil, &pre, nil)
	if v = s.Status(); v.Latest.Version != "1.2.0-beta.1" {
		t.Fatalf("with pre-releases latest = %s", v.Latest.Version)
	}
	skip := "1.2.0-beta.1"
	s.Settings(nil, nil, &skip)
	if s.Status().Available {
		t.Fatal("a skipped version is still offered")
	}

	if err := s.Install("1.1.0", false); err != nil {
		t.Fatal(err)
	}
	if j := wait(t, s, "install"); j.Phase != "install" {
		t.Fatalf("job = %+v", j)
	}
	if len(*launched) != 1 {
		t.Fatal("installer not launched")
	}
	sc, _ := os.ReadFile((*launched)[0])
	for _, want := range []string{"DownloadCenter.sh' stop", "DownloadCenter_1.1.0_x86_64.qpkg", "DownloadCenter.sh\" start"} {
		if !strings.Contains(string(sc), want) {
			t.Errorf("script lacks %q:\n%s", want, sc)
		}
	}
	if strings.Contains(string(sc), "restored") {
		t.Error("an upgrade restores the database")
	}
	if len(s.backups()) != 1 {
		t.Error("no database backup before the update")
	}
	// The new build starts: the update is recorded as done
	s2 := New(nil, s.db, s.root, s.data, "1.1.0")
	if l := s2.Status().Last; l == nil || !l.OK || l.To != "1.1.0" {
		t.Fatalf("last = %+v", l)
	}
}

func TestRejectsTamperedPackage(t *testing.T) {
	f := newFixture(t)
	f.add("1.0.0", 1, false)
	f.add("1.1.0", 1, false)
	os.WriteFile(filepath.Join(f.dir, "1.1.0", release.AssetName("1.1.0", "x86_64")), []byte("package 1.1.X"), 0644)
	s, launched := f.service("1.0.0")
	s.Check()
	if err := s.Install("1.1.0", false); err != nil {
		t.Fatal(err)
	}
	if j := wait(t, s, "install"); j.Phase != "failed" || j.Error != "hash" {
		t.Fatalf("tampered package: %+v", j)
	}
	if len(*launched) != 0 {
		t.Fatal("tampered package launched")
	}
}

func TestRejectsForeignSignature(t *testing.T) {
	f := newFixture(t)
	f.add("1.0.0", 1, false)
	f.add("1.1.0", 1, false)
	s, launched := f.service("1.0.0")
	other, _, _ := release.GenerateKey()
	s.pub, _ = release.ParsePublic(other)
	s.Check()
	s.Install("1.1.0", false)
	if j := wait(t, s, "install"); j.Phase != "failed" || j.Error != "signature" {
		t.Fatalf("foreign signature: %+v", j)
	}
	if len(*launched) != 0 {
		t.Fatal("unsigned package launched")
	}
}

func TestDowngradeAcrossSchema(t *testing.T) {
	// This build writes layout 2; 0.9.0 wrote layout 1
	f := newFixture(t)
	f.add("0.9.0", 1, false)
	f.add("1.0.0", 2, false)
	s, launched := f.service("1.0.0")
	s.schema = 2
	s.Check()
	var old *Entry
	for _, e := range s.Status().Releases {
		if e.Version == "0.9.0" {
			e := e
			old = &e
		}
	}
	if old == nil || !old.NeedsRestore || old.BackupAt != 0 {
		t.Fatalf("0.9.0 entry = %+v", old)
	}
	if err := s.Install("0.9.0", true); err != ErrNoBackup {
		t.Fatalf("downgrade without a backup: %v", err)
	}
	// A backup from that layout exists: it must be asked for, then restored
	os.MkdirAll(s.backupDir(), 0700)
	bk := filepath.Join(s.backupDir(), "dc-0.9.0-s1-1700000000.db")
	os.WriteFile(bk, []byte("old db"), 0600)
	if err := s.Install("0.9.0", false); err != ErrRestoreRequired {
		t.Fatalf("downgrade without consent: %v", err)
	}
	if err := s.Install("0.9.0", true); err != nil {
		t.Fatal(err)
	}
	if j := wait(t, s, "install"); j.Phase != "install" {
		t.Fatalf("job = %+v", j)
	}
	sc, _ := os.ReadFile((*launched)[0])
	if !strings.Contains(string(sc), filepath.Base(bk)) || !strings.Contains(string(sc), "restored the database") {
		t.Fatalf("script does not restore %s:\n%s", bk, sc)
	}
	// Going back within the same layout needs no restore
	f.add("1.1.0", 2, false)
	s3, _ := f.service("1.1.0")
	s3.schema = 2
	s3.Check()
	for _, e := range s3.Status().Releases {
		if e.Version == "1.0.0" && e.NeedsRestore {
			t.Error("same-layout downgrade asks for a restore")
		}
	}
}

func TestOfficialFeedOnlyGitHub(t *testing.T) {
	f := newFixture(t)
	f.add("1.0.0", 1, false)
	f.add("1.1.0", 1, false)
	s, _ := f.service("1.0.0")
	s.Check()
	s.official = true // as if the feed were the official one: file:// packages are not offered
	if v := s.Status(); v.Available {
		t.Fatal("official feed offered a package outside GitHub releases")
	}
	if err := s.Install("1.1.0", false); err != ErrNoPackage {
		t.Fatalf("install outside GitHub releases: %v", err)
	}
}
