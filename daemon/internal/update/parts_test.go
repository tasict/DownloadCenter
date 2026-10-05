package update

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"downloadcenter/internal/release"
)

// smallParts makes the parts small enough for a test package.
func smallParts(t *testing.T) {
	size, workers, tries, backoff := partSize, partWorkers, partTries, partBackoff
	partSize, partWorkers, partTries, partBackoff = 64<<10, 4, 3, 10*time.Millisecond
	t.Cleanup(func() { partSize, partWorkers, partTries, partBackoff = size, workers, tries, backoff })
}

// served publishes version v with body as its package, served at srv; the
// signed list holds the hash of want (body itself unless a test tampers).
func (f *fixture) served(v string, body, want []byte, srv *httptest.Server) {
	f.add(v, 1, false)
	d := filepath.Join(f.dir, v)
	pkg := release.AssetName(v, "x86_64")
	os.WriteFile(filepath.Join(d, pkg), want, 0644)
	hp, _, _ := release.HashFile(filepath.Join(d, pkg))
	hi, _, _ := release.HashFile(filepath.Join(d, release.InfoName))
	sums := release.FormatSums(map[string]string{pkg: hp, release.InfoName: hi})
	os.WriteFile(filepath.Join(d, release.SumsName), sums, 0644)
	os.WriteFile(filepath.Join(d, release.SigName), release.Sign(f.priv, sums), 0644)
	r := &f.feed.Releases[0]
	r.Assets["x86_64"] = release.FeedAsset{Name: pkg, URL: srv.URL + "/" + pkg, Size: int64(len(body)), SHA256: hp}
	b, _ := json.Marshal(f.feed)
	os.WriteFile(f.feedFn, b, 0644)
}

// server answers like a release file host; mode changes what it does with a
// range request: "ranges", "none" (always the whole file), "late" (the whole
// file after the first range), "flaky" (the third part fails once),
// "broken" (the third part always fails), "signed" (see ServeHTTP).
type server struct {
	body              []byte
	mode              string
	requests, ranges  atomic.Int64
	busy, most        atomic.Int64
	redirects, signed atomic.Int64
	mu                sync.Mutex
	failed            map[string]int
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// "signed": the package redirects to a signed address that stops working
	// after three requests, like GitHub's when its signature runs out
	if s.mode == "signed" {
		if !strings.HasPrefix(r.URL.Path, "/signed") {
			s.redirects.Add(1)
			http.Redirect(w, r, "/signed/"+itoa(s.redirects.Load()), http.StatusFound)
			return
		}
		if s.signed.Add(1) > 3 && r.URL.Path == "/signed/1" {
			http.Error(w, "signature expired", 403)
			return
		}
	}
	n := s.requests.Add(1)
	if b := s.busy.Add(1); b > s.most.Load() {
		s.most.Store(b)
	}
	defer s.busy.Add(-1)
	rg := r.Header.Get("Range")
	if rg != "" {
		s.ranges.Add(1)
	}
	third := "bytes=" + itoa(2*partSize) + "-"
	switch {
	case s.mode == "none", s.mode == "late" && n > 1:
		w.Write(s.body)
		return
	case s.mode == "flaky" && strings.HasPrefix(rg, third):
		s.mu.Lock()
		s.failed[rg]++
		first := s.failed[rg] == 1
		s.mu.Unlock()
		if first {
			http.Error(w, "busy", 500)
			return
		}
	case s.mode == "broken" && strings.HasPrefix(rg, third):
		http.Error(w, "gone", 500)
		return
	}
	time.Sleep(5 * time.Millisecond) // long enough for the parts to overlap
	http.ServeContent(w, r, "pkg", time.Time{}, bytes.NewReader(s.body))
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func body(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return b
}

func install(t *testing.T, mode string, tamper bool) (*server, Job, *Service) {
	smallParts(t)
	b := body(int(partSize)*5 + 1234)
	h := &server{body: b, mode: mode, failed: map[string]int{}}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	f := newFixture(t)
	f.add("1.0.0", 1, false)
	want := b
	if tamper {
		want = append([]byte{}, b...)
		want[len(want)/2] ^= 1
	}
	f.served("1.1.0", b, want, srv)
	s, _ := f.service("1.0.0")
	if err := s.Check(); err != nil {
		t.Fatal(err)
	}
	if err := s.Install("1.1.0", false); err != nil {
		t.Fatal(err)
	}
	j := wait(t, s, "install")
	return h, j, s
}

func TestPackageInParts(t *testing.T) {
	h, j, s := install(t, "ranges", false)
	if j.Phase != "install" {
		t.Fatalf("job %+v", j)
	}
	if h.ranges.Load() != 6 || h.most.Load() < 2 {
		t.Errorf("%d range requests, at most %d at once", h.ranges.Load(), h.most.Load())
	}
	got, _ := os.ReadFile(filepath.Join(s.data, "updates", release.AssetName("1.1.0", "x86_64")))
	if !bytes.Equal(got, h.body) {
		t.Errorf("package differs: %d bytes, want %d", len(got), len(h.body))
	}
	if j.Done != int64(len(h.body)) || j.Total != int64(len(h.body)) {
		t.Errorf("progress %d / %d", j.Done, j.Total)
	}
}

// A part that fails once is asked again.
func TestPartTriedAgain(t *testing.T) {
	h, j, _ := install(t, "flaky", false)
	if j.Phase != "install" || h.ranges.Load() != 7 {
		t.Fatalf("job %+v after %d ranges", j, h.ranges.Load())
	}
}

// A part that keeps failing ends the download, with the time it failed.
func TestPartFails(t *testing.T) {
	_, j, _ := install(t, "broken", false)
	if j.Phase != "failed" || j.Error != "download" || j.At == 0 {
		t.Fatalf("job %+v", j)
	}
}

// A server that does not take ranges is read in one piece.
func TestNoRanges(t *testing.T) {
	h, j, _ := install(t, "none", false)
	if j.Phase != "install" || h.requests.Load() != 1 {
		t.Fatalf("job %+v after %d requests", j, h.requests.Load())
	}
}

// One that stops taking them half way is read again in one piece.
func TestRangesStop(t *testing.T) {
	h, j, _ := install(t, "late", false)
	if j.Phase != "install" || h.requests.Load() < 3 {
		t.Fatalf("job %+v after %d requests", j, h.requests.Load())
	}
}

// The parts put together are checked against the signed list.
func TestPartsTampered(t *testing.T) {
	_, j, s := install(t, "ranges", true)
	if j.Phase != "failed" || j.Error != "hash" {
		t.Fatalf("job %+v", j)
	}
	if left, _ := filepath.Glob(filepath.Join(s.data, "updates", "*.qpkg*")); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}

// The parts use the signed address of the first, then the original one
// again once the signature has run out.
func TestSignedAddress(t *testing.T) {
	h, j, s := install(t, "signed", false)
	if j.Phase != "install" {
		t.Fatalf("job %+v", j)
	}
	got, _ := os.ReadFile(filepath.Join(s.data, "updates", release.AssetName("1.1.0", "x86_64")))
	if !bytes.Equal(got, h.body) {
		t.Fatal("package differs")
	}
	// one redirect for the first part, then one more after the signature ran out
	if r := h.redirects.Load(); r < 2 || r > 4 {
		t.Errorf("%d redirects", r)
	}
}
