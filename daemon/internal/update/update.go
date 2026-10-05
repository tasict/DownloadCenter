// Package update keeps Download Center current from its GitHub releases. It
// reads updates.json (the release index the project site publishes), and when
// an administrator asks, downloads the package for this architecture, checks
// it against the signed SHA256SUMS, backs up the database and runs the QPKG
// installer detached from dcd (the installer stops the package, dcd with it).
// After the restart the new dcd records whether the update took.
package update

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"downloadcenter/internal/core"
	"downloadcenter/internal/netutil"
	"downloadcenter/internal/release"
	"downloadcenter/internal/store"
)

const (
	// DefaultFeed is the release index on the project site.
	DefaultFeed = "https://tasict.github.io/DownloadCenter/updates.json"
	// officialFiles is where the packages of the official feed must come from.
	officialFiles = "https://github.com/tasict/DownloadCenter/releases/download/"
	// PublicKey is tools/release-key.pub; a test keeps the two equal.
	PublicKey = "dcrelease-ed25519 Wz1hu8WDbdJwmbUh+EQ0R/aP0aXmqPp3vHk9gLtK/Hk="

	checkEvery  = 12 * time.Hour
	maxPackage  = 512 << 20
	keepBackups = 8
)

// Errors of Install, mapped to API errors by the handlers.
var (
	ErrBusy            = errors.New("an update is already running")
	ErrNoFeed          = errors.New("no release information yet")
	ErrNotFound        = errors.New("no such release")
	ErrSame            = errors.New("that version is installed")
	ErrNoPackage       = errors.New("no package for this architecture")
	ErrNoBackup        = errors.New("going back needs a database backup from that version, and there is none")
	ErrRestoreRequired = errors.New("going back needs the database backup restored")
)

// Job is the update in progress. Phase is "" when idle, then download,
// verify, backup, install; failed with Error (a code) and Detail.
type Job struct {
	Phase  string `json:"phase"`
	Target string `json:"target,omitempty"`
	Done   int64  `json:"done,omitempty"`
	Total  int64  `json:"total,omitempty"`
	Error  string `json:"error,omitempty"`
	Detail string `json:"detail,omitempty"`
	At     int64  `json:"at,omitempty"` // when it failed
}

// Last is the outcome of the most recent update, recorded at the next start.
type Last struct {
	From     string `json:"from"`
	To       string `json:"to"`
	OK       bool   `json:"ok"`
	Restored bool   `json:"restored,omitempty"`
	At       int64  `json:"at"`
	Log      string `json:"log,omitempty"`
}

// Entry is a release as the settings page shows it.
type Entry struct {
	Version      string `json:"version"`
	Date         string `json:"date"`
	Prerelease   bool   `json:"prerelease"`
	Notes        string `json:"notes"`
	URL          string `json:"url"`
	Schema       int    `json:"schema"`
	Relation     string `json:"relation"` // newer, current or older
	Size         int64  `json:"size"`
	Installable  bool   `json:"installable"`
	NeedsRestore bool   `json:"needs_restore"`
	BackupAt     int64  `json:"backup_at,omitempty"` // the backup a downgrade would restore
}

// View is GET /update.
type View struct {
	Current    string  `json:"current"`
	Arch       string  `json:"arch"`
	Schema     int     `json:"schema"`
	AutoCheck  bool    `json:"auto_check"`
	Prerelease bool    `json:"prerelease"`
	Skip       string  `json:"skip"`
	CheckedAt  int64   `json:"checked_at"`
	CheckError string  `json:"check_error,omitempty"`
	Feed       string  `json:"feed,omitempty"` // set when a custom feed is in use
	Latest     *Entry  `json:"latest"`
	Available  bool    `json:"available"`
	Releases   []Entry `json:"releases"`
	Job        Job     `json:"job"`
	Last       *Last   `json:"last"`
}

// Service is the updater of one installation.
type Service struct {
	m                         *core.Manager
	db                        *store.DB
	root, data, version, arch string
	feedURL                   string
	official                  bool
	pub                       ed25519.PublicKey
	schema                    int // database layout of this build (store.SchemaVersion)
	// Launch starts the update script detached; tests replace it.
	Launch func(script string) error

	mu       sync.Mutex
	feed     *release.Feed
	checked  time.Time
	checkErr string
	job      Job
	last     *Last
	stop     chan struct{}
}

// New prepares the updater. A file data/update_feed (first line: a URL,
// http(s) or file://) replaces the official feed, for testing; packages from
// such a feed still need a valid signature.
func New(m *core.Manager, db *store.DB, root, data, version string) *Service {
	s := &Service{m: m, db: db, root: root, data: data, version: version, feedURL: DefaultFeed, official: true, schema: store.SchemaVersion, stop: make(chan struct{})}
	s.pub, _ = release.ParsePublic([]byte(PublicKey))
	s.Launch = launch
	// Development only: with data/update_dryrun the install script is written but not run
	if _, err := os.Stat(filepath.Join(data, "update_dryrun")); err == nil {
		s.Launch = func(script string) error { log.Printf("update: dry run, not running %s", script); return nil }
	}
	s.arch = archOf(root)
	if b, err := os.ReadFile(filepath.Join(data, "update_feed")); err == nil {
		if u := strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0]); u != "" {
			s.feedURL, s.official = u, false
		}
	}
	var f release.Feed
	if c := db.Meta("update_feed_cache"); c != "" && json.Unmarshal([]byte(c), &f) == nil {
		s.feed = &f
	}
	if t, _ := strconv.ParseInt(db.Meta("update_checked"), 10, 64); t > 0 {
		s.checked = time.Unix(t, 0)
	}
	var l Last
	if c := db.Meta("update_last"); c != "" && json.Unmarshal([]byte(c), &l) == nil {
		s.last = &l
	}
	s.finish()
	return s
}

// archOf reads bin/arch (written by build.sh; arm-x41 and arm-x31 share one
// dcd, so the CPU alone cannot tell them apart).
func archOf(root string) string {
	if b, err := os.ReadFile(filepath.Join(root, "bin", "arch")); err == nil {
		if a := strings.TrimSpace(string(b)); a != "" {
			return a
		}
	}
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "arm_64"
	}
	return ""
}

func (s *Service) metaBool(key string, def bool) bool {
	switch s.db.Meta(key) {
	case "1":
		return true
	case "0":
		return false
	}
	return def
}

// Settings changes the update preferences (nil leaves a value as it is).
func (s *Service) Settings(auto, pre *bool, skip *string) {
	b := func(v bool) string {
		if v {
			return "1"
		}
		return "0"
	}
	if auto != nil {
		s.db.SetMeta("update_auto", b(*auto))
	}
	if pre != nil {
		s.db.SetMeta("update_pre", b(*pre))
	}
	if skip != nil {
		s.db.SetMeta("update_skip", *skip)
	}
}

// --- fetching ---

func (s *Service) client(timeout time.Duration) *http.Client {
	proxy := ""
	if s.m != nil {
		proxy = s.m.ProxyURL(s.m.Settings().Proxy.NotifyProfile)
	}
	return netutil.Client(timeout, false, proxy)
}

func (s *Service) open(u string, timeout time.Duration) (io.ReadCloser, int64, error) {
	if p, ok := strings.CutPrefix(u, "file://"); ok && !s.official {
		f, err := os.Open(p)
		if err != nil {
			return nil, 0, err
		}
		st, _ := f.Stat()
		return f, st.Size(), nil
	}
	if !strings.HasPrefix(u, "https://") && (s.official || !strings.HasPrefix(u, "http://")) {
		return nil, 0, fmt.Errorf("refusing %s", u)
	}
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "DownloadCenter/"+s.version)
	resp, err := s.client(timeout).Do(req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	return resp.Body, resp.ContentLength, nil
}

func (s *Service) get(u string, limit int64) ([]byte, error) {
	rc, _, err := s.open(u, time.Minute)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = fmt.Errorf("%s is too large", u)
	}
	return b, err
}

// Check reads the release index now.
func (s *Service) Check() error {
	b, err := s.get(s.feedURL, 4<<20)
	var f release.Feed
	if err == nil {
		err = json.Unmarshal(b, &f)
	}
	if err == nil && f.Schema != 1 {
		err = fmt.Errorf("unknown updates.json schema %d", f.Schema)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checked = time.Now()
	s.db.SetMeta("update_checked", strconv.FormatInt(s.checked.Unix(), 10))
	if err != nil {
		s.checkErr = err.Error()
		return err
	}
	s.feed, s.checkErr = &f, ""
	s.db.SetMeta("update_feed_cache", string(b))
	return nil
}

// Run checks every 12 hours while automatic checks are on.
func (s *Service) Run() {
	t := time.NewTimer(2 * time.Minute)
	for {
		select {
		case <-s.stop:
			t.Stop()
			return
		case <-t.C:
		}
		s.mu.Lock()
		due := time.Since(s.checked) >= checkEvery
		s.mu.Unlock()
		if due && s.metaBool("update_auto", true) {
			if err := s.Check(); err != nil {
				log.Printf("update check: %v", err)
			}
		}
		t.Reset(30 * time.Minute)
	}
}

// Stop ends Run.
func (s *Service) Stop() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
}

// --- status ---

// Status is what the settings page and the toolbar need.
func (s *Service) Status() View {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := View{Current: s.version, Arch: s.arch, Schema: s.schema, AutoCheck: s.metaBool("update_auto", true),
		Prerelease: s.metaBool("update_pre", false), Skip: s.db.Meta("update_skip"), CheckError: s.checkErr, Job: s.job, Last: s.last, Releases: []Entry{}}
	if !s.checked.IsZero() {
		v.CheckedAt = s.checked.Unix()
	}
	if !s.official {
		v.Feed = s.feedURL
	}
	if s.feed == nil {
		return v
	}
	for _, r := range s.feed.Releases {
		if r.Prerelease && !v.Prerelease && r.Version != s.version {
			continue
		}
		v.Releases = append(v.Releases, s.entry(r))
	}
	if release.ValidVersion(s.version) {
		for i := range v.Releases {
			if v.Releases[i].Relation == "newer" && v.Releases[i].Installable {
				e := v.Releases[i]
				v.Latest = &e
				break
			}
		}
		v.Available = v.Latest != nil && v.Latest.Version != v.Skip
	}
	return v
}

func (s *Service) entry(r release.FeedRelease) Entry {
	e := Entry{Version: r.Version, Date: r.Date, Prerelease: r.Prerelease, Notes: r.Notes, URL: r.URL, Schema: r.Schema}
	if e.Schema < 1 {
		e.Schema = 1
	}
	switch c := release.Compare(r.Version, s.version); {
	case r.Version == s.version:
		e.Relation = "current"
	case c > 0:
		e.Relation = "newer"
	default:
		e.Relation = "older"
	}
	if a, ok := r.Assets[s.arch]; ok && s.arch != "" {
		e.Size = a.Size
		e.Installable = e.Relation != "current" && (!s.official || strings.HasPrefix(a.URL, officialFiles))
	}
	if e.Relation == "older" && e.Schema < s.schema {
		e.NeedsRestore = true
		if _, at := s.backupFor(e.Schema); at > 0 {
			e.BackupAt = at
		}
	}
	return e
}

// --- database backups: data/backups/dc-<version>-s<schema>-<unix>.db ---

func (s *Service) backupDir() string { return filepath.Join(s.data, "backups") }

type backup struct {
	path   string
	schema int
	at     int64
}

func (s *Service) backups() []backup {
	ents, _ := os.ReadDir(s.backupDir())
	var out []backup
	for _, e := range ents {
		n := e.Name()
		if !strings.HasPrefix(n, "dc-") || !strings.HasSuffix(n, ".db") {
			continue
		}
		f := strings.Split(strings.TrimSuffix(n, ".db"), "-")
		if len(f) < 4 || !strings.HasPrefix(f[len(f)-2], "s") {
			continue
		}
		sc, err1 := strconv.Atoi(strings.TrimPrefix(f[len(f)-2], "s"))
		at, err2 := strconv.ParseInt(f[len(f)-1], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, backup{filepath.Join(s.backupDir(), n), sc, at})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].at > out[j].at })
	return out
}

// backupFor is the newest backup an older build with this schema can read.
func (s *Service) backupFor(schema int) (string, int64) {
	for _, b := range s.backups() {
		if b.schema <= schema {
			return b.path, b.at
		}
	}
	return "", 0
}

func (s *Service) makeBackup() error {
	if err := os.MkdirAll(s.backupDir(), 0700); err != nil {
		return err
	}
	name := fmt.Sprintf("dc-%s-s%d-%d.db", safeName(s.version), s.schema, time.Now().Unix())
	if err := s.db.Backup(filepath.Join(s.backupDir(), name)); err != nil {
		return err
	}
	all := s.backups()
	for i := keepBackups; i < len(all); i++ {
		os.Remove(all[i].path)
	}
	return nil
}

func safeName(v string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.' {
			return r
		}
		return '_'
	}, v)
}

// --- installing ---

// Install starts updating (or going back) to a version. Going back across a
// database change needs restore: the backup made when that layout was last
// in use replaces the database (changes made since then are lost).
func (s *Service) Install(version string, restore bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job.Phase != "" && s.job.Phase != "failed" {
		return ErrBusy
	}
	if s.feed == nil {
		return ErrNoFeed
	}
	var r *release.FeedRelease
	for i := range s.feed.Releases {
		if s.feed.Releases[i].Version == version {
			r = &s.feed.Releases[i]
		}
	}
	if r == nil {
		return ErrNotFound
	}
	if version == s.version {
		return ErrSame
	}
	e := s.entry(*r)
	if !e.Installable {
		return ErrNoPackage
	}
	if e.NeedsRestore && e.BackupAt == 0 {
		return ErrNoBackup
	}
	if e.NeedsRestore && !restore {
		return ErrRestoreRequired
	}
	s.job = Job{Phase: "download", Target: version, Total: r.Assets[s.arch].Size}
	go s.run(*r, r.Assets[s.arch])
	return nil
}

func (s *Service) setJob(f func(j *Job)) {
	s.mu.Lock()
	f(&s.job)
	s.mu.Unlock()
}

func (s *Service) fail(code string, err error) {
	log.Printf("update: %s: %v", code, err)
	at := time.Now().Unix()
	s.setJob(func(j *Job) { j.Phase, j.Error, j.Detail, j.At = "failed", code, err.Error(), at })
}

type progress struct {
	s *Service
	n int64
}

func (p *progress) Write(b []byte) (int, error) {
	p.n += int64(len(b))
	n := p.n
	p.s.setJob(func(j *Job) { j.Done = n })
	return len(b), nil
}

func (s *Service) run(r release.FeedRelease, a release.FeedAsset) {
	dir := filepath.Join(s.data, "updates")
	os.MkdirAll(dir, 0700)
	old, _ := filepath.Glob(filepath.Join(dir, "*.qpkg*"))
	for _, f := range old {
		os.Remove(f)
	}
	if s.official && (!strings.HasPrefix(r.SumsURL, officialFiles) || !strings.HasPrefix(r.SigURL, officialFiles)) {
		s.fail("download", errors.New("release files outside the project's releases"))
		return
	}
	// The signed list, and release.json next to it (the database layout of that build)
	sums, err := s.get(r.SumsURL, 1<<20)
	var sig []byte
	if err == nil {
		sig, err = s.get(r.SigURL, 1<<20)
	}
	if err != nil {
		s.fail("download", err)
		return
	}
	if err := release.Verify(s.pub, sums, sig); err != nil {
		s.fail("signature", err)
		return
	}
	list, err := release.ParseSums(sums)
	if err != nil {
		s.fail("signature", err)
		return
	}
	schema := 1
	if want := list[release.InfoName]; want != "" {
		b, err := s.get(strings.TrimSuffix(r.SumsURL, release.SumsName)+release.InfoName, 1<<20)
		if err != nil {
			s.fail("download", err)
			return
		}
		var info release.Info
		if h, _, _ := release.HashReader(strings.NewReader(string(b))); h != want {
			s.fail("hash", errors.New(release.InfoName+" does not match "+release.SumsName))
			return
		}
		if json.Unmarshal(b, &info) == nil && info.Schema > 0 {
			schema = info.Schema
		}
		if info.Version != "" && info.Version != r.Version {
			s.fail("hash", fmt.Errorf("%s is for %s", release.InfoName, info.Version))
			return
		}
	}
	want := list[a.Name]
	if want == "" || a.Name != release.AssetName(r.Version, s.arch) {
		s.fail("hash", fmt.Errorf("%s is not in %s", a.Name, release.SumsName))
		return
	}
	// The package
	part := filepath.Join(dir, a.Name+".part")
	h, err := s.fetch(a.URL, part)
	if err != nil {
		os.Remove(part)
		s.fail("download", err)
		return
	}
	s.setJob(func(j *Job) { j.Phase = "verify" })
	if h != want {
		os.Remove(part)
		s.fail("hash", fmt.Errorf("%s: SHA-256 %s, %s says %s", a.Name, h, release.SumsName, want))
		return
	}
	pkg := filepath.Join(dir, a.Name)
	if err := os.Rename(part, pkg); err != nil {
		s.fail("download", err)
		return
	}
	// Going back across a database change: restore the backup from that layout
	restore := ""
	if schema < s.schema {
		p, _ := s.backupFor(schema)
		if p == "" {
			s.fail("backup", ErrNoBackup)
			return
		}
		restore = p
	}
	s.setJob(func(j *Job) { j.Phase = "backup" })
	if err := s.makeBackup(); err != nil {
		s.fail("backup", err)
		return
	}
	st := state{From: s.version, To: r.Version, Restore: restore, Started: time.Now().Unix()}
	sb, _ := json.Marshal(st)
	if err := os.WriteFile(filepath.Join(dir, "state.json"), sb, 0600); err != nil {
		s.fail("install", err)
		return
	}
	script := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(script, []byte(s.script(st, pkg)), 0700); err != nil {
		s.fail("install", err)
		return
	}
	s.setJob(func(j *Job) { j.Phase = "install" })
	log.Printf("update: installing %s (from %s)", r.Version, s.version)
	if err := s.Launch(script); err != nil {
		os.Remove(filepath.Join(dir, "state.json"))
		s.fail("install", err)
	}
}

// Packages download in parts over several connections when the server takes
// ranges: a slow route to a CDN limits each connection, not the line (one
// connection to GitHub's release files can be a hundred times slower than
// the line).
var (
	partSize    int64 = 1 << 20
	partWorkers       = 6
	partTries         = 3
	partBackoff       = time.Second // before the second try; twice that before the third
)

// errNoRanges: the server answered a range with the whole file.
var errNoRanges = errors.New("the server stopped taking ranges")

// fetch downloads the package into part and returns its SHA-256.
func (s *Service) fetch(u, part string) (string, error) {
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if strings.HasPrefix(u, "file://") {
		return s.whole(u, f)
	}
	if !strings.HasPrefix(u, "https://") && (s.official || !strings.HasPrefix(u, "http://")) {
		return "", fmt.Errorf("refusing %s", u)
	}
	h, err := s.parts(u, f)
	if err == errNoRanges {
		if err = f.Truncate(0); err != nil {
			return "", err
		}
		return s.whole(u, f)
	}
	return h, err
}

// whole downloads the package in one piece.
func (s *Service) whole(u string, f *os.File) (string, error) {
	rc, size, err := s.open(u, 30*time.Minute)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	return s.stream(rc, size, f)
}

// stream writes one answer to f from its start, hashing it on the way.
func (s *Service) stream(r io.Reader, size int64, f *os.File) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	s.setJob(func(j *Job) {
		j.Done = 0
		if size > 0 {
			j.Total = size
		}
	})
	h, n, err := release.HashReader(io.TeeReader(io.LimitReader(r, maxPackage+1), io.MultiWriter(f, &progress{s: s})))
	if err == nil && n > maxPackage {
		err = errors.New("the package is too large")
	}
	return h, err
}

// parts asks for the first range; a server that takes ranges gets the rest
// from several workers, one part at a time each, so a slow connection does
// not hold up the end. One answered with the whole file is read in one piece.
// The parts go straight to where the first one was redirected (GitHub's
// signed address), back to the original address once that fails: the
// signature may have run out.
func (s *Service) parts(u string, f *os.File) (string, error) {
	c := s.client(10 * time.Minute)
	resp, err := s.rangeGet(context.Background(), c, u, 0, partSize-1)
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
		return s.stream(resp.Body, resp.ContentLength, f)
	}
	total, first, err := rangeOf(resp, 0, partSize-1)
	if err == nil && total > maxPackage {
		err = errors.New("the package is too large")
	}
	if err != nil {
		resp.Body.Close()
		return "", err
	}
	s.setJob(func(j *Job) { j.Total, j.Done = total, 0 })
	var src atomic.Value
	src.Store(resp.Request.URL.String())
	var done atomic.Int64
	add := func(n int64) {
		v := done.Add(n)
		s.setJob(func(j *Job) { j.Done = v })
	}
	err = readPart(resp.Body, f, 0, first, add)
	resp.Body.Close()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var next atomic.Int64
	next.Store(first)
	var wg sync.WaitGroup
	var once sync.Once
	var ferr error
	for w := int64(0); w < min(int64(partWorkers), (total-first+partSize-1)/partSize); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				start := next.Add(partSize) - partSize
				if start >= total {
					return
				}
				if err := s.part(ctx, c, u, &src, f, start, min(start+partSize, total)-1, total, add); err != nil {
					once.Do(func() { ferr = err; cancel() })
					return
				}
			}
		}()
	}
	wg.Wait()
	if ferr != nil {
		return "", ferr
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	h, n, err := release.HashReader(f)
	if err == nil && n != total {
		err = fmt.Errorf("downloaded %d of %d bytes", n, total)
	}
	return h, err
}

// part downloads bytes start-end from src, trying again a few times; after a
// failure src is the original address u again.
func (s *Service) part(ctx context.Context, c *http.Client, u string, src *atomic.Value, f *os.File, start, end, total int64, add func(int64)) error {
	var err error
	for try := 0; try < partTries; try++ {
		if err != nil {
			src.Store(u)
		}
		if try > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(try) * partBackoff):
			}
		}
		var resp *http.Response
		if resp, err = s.rangeGet(ctx, c, src.Load().(string), start, end); err != nil {
			continue
		}
		if resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			return errNoRanges
		}
		var size int64
		if size, _, err = rangeOf(resp, start, end); err == nil && size != total {
			err = fmt.Errorf("the package changed size (%d, was %d)", size, total)
		}
		if err != nil {
			resp.Body.Close()
			continue
		}
		var got int64
		err = readPart(resp.Body, f, start, end-start+1, func(n int64) { got += n; add(n) })
		resp.Body.Close()
		if err == nil {
			return nil
		}
		add(-got)
	}
	return err
}

func (s *Service) rangeGet(ctx context.Context, c *http.Client, u string, start, end int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "DownloadCenter/"+s.version)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	return resp, nil
}

// rangeOf checks a 206 answer for bytes start-end (or up to the end of a
// smaller file) and returns the file size and the length of the part.
func rangeOf(resp *http.Response, start, end int64) (total, n int64, err error) {
	var a, b int64
	cr := resp.Header.Get("Content-Range")
	if resp.StatusCode != http.StatusPartialContent {
		return 0, 0, fmt.Errorf("HTTP %d for a range", resp.StatusCode)
	}
	if _, err := fmt.Sscanf(cr, "bytes %d-%d/%d", &a, &b, &total); err != nil || a != start || b < a || b >= total || (b != end && b != total-1) {
		return 0, 0, fmt.Errorf("unexpected Content-Range %q for bytes %d-%d", cr, start, end)
	}
	return total, b - a + 1, nil
}

// readPart writes exactly n bytes of r to f at off.
func readPart(r io.Reader, f *os.File, off, n int64, add func(int64)) error {
	buf := make([]byte, 32<<10)
	for n > 0 {
		m, err := io.ReadFull(r, buf[:min(int64(len(buf)), n)])
		if m > 0 {
			if _, werr := f.WriteAt(buf[:m], off); werr != nil {
				return werr
			}
			off += int64(m)
			n -= int64(m)
			add(int64(m))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

type state struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Restore string `json:"restore,omitempty"`
	Started int64  `json:"started"`
}

// q quotes a path for sh.
func q(p string) string { return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'" }

// script stops the package, restores the database when asked, runs the
// installer and makes sure the service runs again whatever happened.
func (s *Service) script(st state, pkg string) string {
	db := filepath.Join(s.data, "dc.db")
	var b strings.Builder
	fmt.Fprintf(&b, "#!/bin/sh\n# Written by dcd: installs Download Center %s over %s, detached from dcd\n", st.To, st.From)
	fmt.Fprintf(&b, "exec >> %s 2>&1\n", q(filepath.Join(s.data, "logs", "update.log")))
	fmt.Fprintf(&b, "echo \"$(date '+%%Y-%%m-%%d %%H:%%M:%%S') update %s -> %s\"\n", st.From, st.To)
	fmt.Fprintf(&b, "%s stop\n", q(filepath.Join(s.root, "DownloadCenter.sh")))
	if st.Restore != "" {
		fmt.Fprintf(&b, "mv %s %s 2>/dev/null\n", q(db), q(filepath.Join(s.backupDir(), fmt.Sprintf("dc-replaced-%d.db", st.Started))))
		fmt.Fprintf(&b, "rm -f %s %s\n", q(db+"-wal"), q(db+"-shm"))
		fmt.Fprintf(&b, "cp %s %s && chmod 600 %s && echo \"restored the database from %s\"\n", q(st.Restore), q(db), q(db), filepath.Base(st.Restore))
	}
	fmt.Fprintf(&b, "sh %s\n", q(pkg))
	b.WriteString("rc=$?\n")
	b.WriteString("echo \"$(date '+%Y-%m-%d %H:%M:%S') installer exit code $rc\"\n")
	b.WriteString("root=$(/sbin/getcfg DownloadCenter Install_Path -f /etc/config/qpkg.conf)\n")
	b.WriteString("[ -x \"$root/DownloadCenter.sh\" ] && \"$root/DownloadCenter.sh\" start\n")
	fmt.Fprintf(&b, "rm -f %s\n", q(pkg))
	return b.String()
}

func launch(script string) error {
	cmd := exec.Command("/bin/sh", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// finish records the outcome of an update this start completes.
func (s *Service) finish() {
	p := filepath.Join(s.data, "updates", "state.json")
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	os.Remove(p)
	var st state
	if json.Unmarshal(b, &st) != nil {
		return
	}
	l := &Last{From: st.From, To: st.To, OK: st.To == s.version, Restored: st.Restore != "" && st.To == s.version, At: time.Now().Unix()}
	if !l.OK {
		l.Log = tail(filepath.Join(s.data, "logs", "update.log"), 20)
	}
	s.last = l
	lb, _ := json.Marshal(l)
	s.db.SetMeta("update_last", string(lb))
	log.Printf("update %s -> %s: ok=%v", st.From, st.To, l.OK)
}

func tail(path string, lines int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	ls := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(ls) > lines {
		ls = ls[len(ls)-lines:]
	}
	return strings.Join(ls, "\n")
}
