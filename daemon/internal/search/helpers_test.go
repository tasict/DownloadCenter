package search

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeNet answers requests to some host names in the process. Any other
// host is refused, unless it is listed in through: those go on to the
// real, guarded transport (the guard tests). No test reaches the network.
type fakeNet struct {
	mu      sync.Mutex
	hosts   map[string]http.Handler
	through map[string]bool
	seen    []*http.Request
}

func newFakeNet() *fakeNet {
	return &fakeNet{hosts: map[string]http.Handler{}, through: map[string]bool{}}
}

func (n *fakeNet) handle(host string, h http.HandlerFunc) { n.hosts[host] = h }

func (n *fakeNet) requests() []*http.Request {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]*http.Request(nil), n.seen...)
}

type fakeRT struct {
	n    *fakeNet
	next http.RoundTripper
}

func (rt fakeRT) RoundTrip(req *http.Request) (*http.Response, error) {
	h := rt.n.hosts[req.URL.Host]
	if h == nil {
		if rt.n.through[req.URL.Host] {
			return rt.next.RoundTrip(req)
		}
		// The address in the error must never reach a caller or the log
		return nil, errors.New("test network: no host " + req.URL.String())
	}
	rt.n.mu.Lock()
	rt.n.seen = append(rt.n.seen, req.Clone(req.Context()))
	rt.n.mu.Unlock()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	resp := rec.Result()
	resp.Request = req
	return resp, nil
}

func (n *fakeNet) fetcher() *fetcher {
	f := newFetcher("9.9.9", nil)
	f.wrap = func(next http.RoundTripper) http.RoundTripper { return fakeRT{n: n, next: next} }
	return f
}

// newTestService is a service over a store in a temporary directory with
// every built-in source switched off and requests going to n.
func newTestService(t *testing.T, n *fakeNet) *Service {
	t.Helper()
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	off := false
	for _, p := range Builtins() {
		if err := st.Update(p.ID, &off, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	return newService(st, n.fetcher())
}

// addPlugin installs and enables a plugin.
func addPlugin(t *testing.T, s *Service, def string, settings map[string]string) *Plugin {
	t.Helper()
	p, err := ParseCustom([]byte(def))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.Install(p, true, false, settings); err != nil {
		t.Fatal(err)
	}
	return p
}

// jsonSource is a json plugin on host whose answer lists {"t","h","u"}.
func jsonSource(id, host string) string {
	return `{"schema":1,"id":"` + id + `","name":"` + strings.ToUpper(id) + `","type":"json",
		"request":{"url":"https://` + host + `/s?q={query}&key={setting:key}","headers":{"X-Api-Key":"{setting:key}"}},
		"settings":[{"id":"key","label":"Key","secret":true}],
		"response":{"items":"items","fields":{"title":"t","infohash":"h","torrent":"u","page":"p"}}}`
}

// waitDone polls a search until it is done.
func waitDone(t *testing.T, s *Service, owner, id string) *JobView {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		v, err := s.View(owner, id)
		if err != nil {
			t.Fatal(err)
		}
		if v.Done {
			return v
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("search did not finish")
	return nil
}

// A small valid .torrent.
const sampleTorrent = "d4:infod6:lengthi1e4:name5:a.iso12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaaee"
