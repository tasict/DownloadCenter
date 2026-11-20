package search

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// items is a json answer of jsonSource.
func items(rs ...string) string {
	return `{"items":[` + strings.Join(rs, ",") + `]}`
}

func jsonItem(title, hash, torrent string) string {
	return fmt.Sprintf(`{"t":%q,"h":%q,"u":%q}`, title, hash, torrent)
}

// spec: results come in source by source; a source that times out fails
func TestSearchArrivesInTurn(t *testing.T) {
	n := newFakeNet()
	release := make(chan struct{})
	n.handle("fast.example.org", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(items(jsonItem("Ubuntu fast", hashA, ""))))
	})
	n.handle("later.example.org", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Write([]byte(items(jsonItem("Ubuntu later", "", "https://later.example.org/u.torrent"))))
	})
	n.handle("dead.example.org", func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	s := newTestService(t, n)
	s.fetch.timeout = 2 * time.Second
	addPlugin(t, s, jsonSource("fast", "fast.example.org"), nil)
	addPlugin(t, s, jsonSource("later", "later.example.org"), nil)
	addPlugin(t, s, jsonSource("dead", "dead.example.org"), nil)

	id, err := s.Start("alice", true, Query{Text: "ubuntu"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var v *JobView
	for i := 0; i < 200; i++ {
		v, _ = s.View("alice", id)
		if v.Total == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if v.Done || v.Total != 1 || v.Results[0].Title != "Ubuntu fast" {
		t.Fatalf("first answer: %+v", v)
	}
	states := map[string]string{}
	for _, src := range v.Sources {
		states[src.ID] = src.State
	}
	if states["fast"] != "done" || states["later"] != "running" || states["dead"] != "running" {
		t.Errorf("states %v", states)
	}
	close(release)
	v = waitDone(t, s, "alice", id)
	if v.Total != 2 || v.Query != "ubuntu" || v.Category != "all" {
		t.Fatalf("%+v", v)
	}
	for _, src := range v.Sources {
		switch src.ID {
		case "fast", "later":
			if src.State != "done" || src.Count != 1 {
				t.Errorf("%+v", src)
			}
		case "dead":
			if src.State != "failed" || src.Error != ReasonTimeout {
				t.Errorf("%+v", src)
			}
		}
	}
	// The result with a .torrent has a code; its address is public
	for _, r := range v.Results {
		if r.Title == "Ubuntu later" && (r.Ref == "" || r.TorrentURL != "https://later.example.org/u.torrent") {
			t.Errorf("%+v", r)
		}
	}
}

func TestSearchRules(t *testing.T) {
	n := newFakeNet()
	var calls atomic.Int32
	n.handle("src.example.org", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(items(jsonItem("Debian", hashA, ""))))
	})
	s := newTestService(t, n)
	clock := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	s.now = func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return clock }
	advance := func(d time.Duration) { clockMu.Lock(); clock = clock.Add(d); clockMu.Unlock() }

	// spec: no source enabled
	if _, err := s.Start("alice", true, Query{Text: "debian"}, nil); !errors.Is(err, ErrNoSources) {
		t.Fatalf("no sources: %v", err)
	}
	addPlugin(t, s, jsonSource("src", "src.example.org"), nil)
	// spec: a one-character keyword asks nothing
	for _, q := range []string{"d", "  d ", strings.Repeat("x", 201), "a\x00b"} {
		if _, err := s.Start("alice", true, Query{Text: q}, nil); !errors.Is(err, ErrBadQuery) {
			t.Errorf("%q: %v", q, err)
		}
	}
	if _, err := s.Start("alice", true, Query{Text: "debian", Category: "games"}, nil); !errors.Is(err, ErrBadCategory) {
		t.Errorf("bad category: %v", err)
	}
	if _, err := s.Start("alice", true, Query{Text: "debian"}, []string{"other"}); !errors.Is(err, ErrNoSources) {
		t.Errorf("unknown source: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("%d requests for refused searches", calls.Load())
	}

	id, err := s.Start("alice", true, Query{Text: "debian"}, []string{"src"})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, s, "alice", id)
	// spec: another account (an administrator too) does not see it
	if _, err := s.View("admin", id); !errors.Is(err, ErrNotFound) {
		t.Errorf("other account: %v", err)
	}
	if err := s.Cancel("bob", id); !errors.Is(err, ErrNotFound) {
		t.Errorf("other account cancels: %v", err)
	}
	// spec: kept 30 minutes after it finished
	advance(29 * time.Minute)
	if _, err := s.View("alice", id); err != nil {
		t.Errorf("after 29 minutes: %v", err)
	}
	advance(2 * time.Minute)
	if _, err := s.View("alice", id); !errors.Is(err, ErrNotFound) {
		t.Errorf("after 31 minutes: %v", err)
	}

	// spec: 20 searches a minute per account
	advance(time.Hour)
	var ids []string
	for i := 0; i < startsPerMinute; i++ {
		id, err := s.Start("bob", true, Query{Text: "debian"}, nil)
		if err != nil {
			t.Fatalf("search %d: %v", i+1, err)
		}
		ids = append(ids, id)
		waitDone(t, s, "bob", id)
	}
	if _, err := s.Start("bob", true, Query{Text: "debian"}, nil); !errors.Is(err, ErrRateLimited) {
		t.Errorf("21st search: %v", err)
	}
	if _, err := s.Start("alice", true, Query{Text: "debian"}, nil); err != nil {
		t.Errorf("another account: %v", err)
	}
	// At most three kept per account
	kept := 0
	for _, id := range ids {
		if _, err := s.View("bob", id); err == nil {
			kept++
		}
	}
	if kept != keepPerOwner {
		t.Errorf("%d searches kept", kept)
	}
	advance(time.Minute)
	if _, err := s.Start("bob", true, Query{Text: "debian"}, nil); err != nil {
		t.Errorf("a minute later: %v", err)
	}
}

// spec: a new search, or cancelling, aborts the requests still running
func TestSearchCancel(t *testing.T) {
	n := newFakeNet()
	var aborted atomic.Int32
	n.handle("slow.example.org", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			aborted.Add(1)
		case <-time.After(10 * time.Second):
		}
	})
	s := newTestService(t, n)
	addPlugin(t, s, jsonSource("slow", "slow.example.org"), nil)
	first, _ := s.Start("alice", true, Query{Text: "first"}, nil)
	time.Sleep(50 * time.Millisecond)
	second, err := s.Start("alice", true, Query{Text: "second"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	v := waitDone(t, s, "alice", first)
	if v.Sources[0].State != "cancelled" {
		t.Errorf("first search: %+v", v.Sources)
	}
	time.Sleep(50 * time.Millisecond)
	if err := s.Cancel("alice", second); err != nil {
		t.Fatal(err)
	}
	v = waitDone(t, s, "alice", second)
	if v.Sources[0].State != "cancelled" || aborted.Load() != 2 {
		t.Errorf("second search: %+v, %d aborted", v.Sources, aborted.Load())
	}
}

// spec: a catalogue is kept an hour, then asked again conditionally
func TestCatalogueCache(t *testing.T) {
	n := newFakeNet()
	var calls atomic.Int32
	var lastIfNone atomic.Value
	lastIfNone.Store("")
	n.handle("torrent.fedoraproject.org", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		lastIfNone.Store(r.Header.Get("If-None-Match"))
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Write(readData(t, "fedora.xml"))
	})
	s := newTestService(t, n)
	clock := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	s.now = func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return clock }
	on := true
	s.store.Update("fedora", &on, nil, nil)

	search := func(q string) *JobView {
		t.Helper()
		id, err := s.Start("alice", true, Query{Text: q}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return waitDone(t, s, "alice", id)
	}
	if v := search("fedora kde"); v.Total != 2 || calls.Load() != 1 {
		t.Fatalf("first: %d results, %d requests", v.Total, calls.Load())
	}
	clockMu.Lock()
	clock = clock.Add(10 * time.Minute)
	clockMu.Unlock()
	if v := search("cosmic"); v.Total != 4 || calls.Load() != 1 {
		t.Fatalf("10 minutes later: %d results, %d requests", v.Total, calls.Load())
	}
	clockMu.Lock()
	clock = clock.Add(time.Hour)
	clockMu.Unlock()
	if v := search("cinnamon"); v.Total != 1 || calls.Load() != 2 || lastIfNone.Load() != `"v1"` {
		t.Fatalf("an hour later: %d results, %d requests, If-None-Match %v", v.Total, calls.Load(), lastIfNone.Load())
	}
	// A change of the source forgets the catalogue
	off := false
	s.store.Update("fedora", &off, nil, nil)
	s.store.Update("fedora", &on, nil, nil)
	if v := search("budgie"); v.Total != 1 || calls.Load() != 3 || lastIfNone.Load() != "" {
		t.Fatalf("after a change: %d results, %d requests", v.Total, calls.Load())
	}
}

// spec: a .torrent address with the API key in it, or on the server the
// administrator set, is replaced by a code; the code gives the .torrent
func TestTorrentLinks(t *testing.T) {
	n := newFakeNet()
	jackett := readData(t, "jackett.xml")
	n.handle("192.168.1.20:9117", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/results/torznab"):
			if r.URL.Query().Get("apikey") != "SECRETKEY" {
				w.Write([]byte(`<error code="100" description="Invalid API Key"/>`))
				return
			}
			w.Write(jackett)
		case strings.Contains(r.URL.RawQuery, "path=Q2ZESjhP"):
			w.Write([]byte(sampleTorrent))
		case strings.Contains(r.URL.RawQuery, "path=U2ludGVs"):
			w.Write([]byte("<html>Login</html>"))
		}
	})
	n.handle("pub.example.org", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/s":
			w.Write([]byte(items(jsonItem("Secret page", hashA, "https://pub.example.org/t.torrent?k=PKEY"))))
		}
	})
	s := newTestService(t, n)
	tz := &Plugin{Schema: 1, ID: "jackett", Name: "Jackett", Type: TypeTorznab}
	if err := s.store.Install(tz, true, false, map[string]string{"url": "http://192.168.1.20:9117/api/v2.0/indexers/all/results/torznab/", "apikey": "SECRETKEY"}); err != nil {
		t.Fatal(err)
	}
	id, err := s.Start("alice", true, Query{Text: "ubuntu"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	v := waitDone(t, s, "alice", id)
	if v.Total != 3 {
		t.Fatalf("%+v", v)
	}
	body, _ := jsonOf(v)
	if strings.Contains(body, "SECRETKEY") || strings.Contains(body, "192.168.1.20") {
		t.Errorf("address of the server in %s", body)
	}
	byTitle := map[string]Result{}
	for _, r := range v.Results {
		byTitle[r.Title] = r
	}
	u := byTitle["Ubuntu 24.04 Desktop amd64"]
	if u.Ref == "" || u.TorrentURL != "" || u.Page != "https://tracker.example.org/details/1234" {
		t.Errorf("%+v", u)
	}
	tf, err := s.Torrent(context.Background(), "alice", true, id, u.Ref)
	if err != nil || string(tf.Data) != sampleTorrent || len(tf.InfoHash) != 40 {
		t.Fatalf("torrent: %v", err)
	}
	// spec: what the source gives is not a .torrent
	if _, err := s.Torrent(context.Background(), "alice", true, id, byTitle["Sintel 4K"].Ref); reason(err) != ReasonNotTorrent {
		t.Errorf("html: %v", err)
	}
	// Only the owner, only known codes
	if _, err := s.Torrent(context.Background(), "bob", false, id, u.Ref); !errors.Is(err, ErrNotFound) {
		t.Errorf("other account: %v", err)
	}
	if _, err := s.Torrent(context.Background(), "alice", true, id, "0123"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown code: %v", err)
	}

	// A plugin's secret in a page or .torrent address hides both
	addPlugin(t, s, jsonSource("pub", "pub.example.org"), map[string]string{"key": "PKEY"})
	id, _ = s.Start("alice", true, Query{Text: "secret"}, []string{"pub"})
	v = waitDone(t, s, "alice", id)
	if r := v.Results[0]; r.TorrentURL != "" || r.Ref == "" {
		t.Errorf("%+v", r)
	}

	// spec: a wrong key is the source's failure, with a fixed reason
	s.store.Update("jackett", nil, map[string]string{"apikey": "WRONG"}, nil)
	id, _ = s.Start("alice", true, Query{Text: "ubuntu"}, []string{"jackett"})
	v = waitDone(t, s, "alice", id)
	if v.Sources[0].State != "failed" || v.Sources[0].Error != ReasonRefused {
		t.Errorf("%+v", v.Sources)
	}
}

// spec: a source that redirects the download to a magnet link
func TestTorrentMagnetRedirect(t *testing.T) {
	n := newFakeNet()
	n.handle("192.168.1.21:9696", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api") {
			w.Write(readData(t, "prowlarr.xml"))
			return
		}
		w.Header().Set("Location", "magnet:?xt=urn:btih:"+hashA+"&dn=evil&xs=http://evil.example.org/&tr=udp%3A%2F%2Ft.example.org%3A1")
		w.WriteHeader(http.StatusMovedPermanently)
	})
	s := newTestService(t, n)
	tz := &Plugin{Schema: 1, ID: "prowlarr", Name: "Prowlarr", Type: TypeTorznab}
	s.store.Install(tz, true, false, map[string]string{"url": "http://192.168.1.21:9696/3/api", "apikey": "PROWLARRKEY"})
	id, _ := s.Start("alice", true, Query{Text: "tom sawyer"}, nil)
	v := waitDone(t, s, "alice", id)
	ref := ""
	for _, r := range v.Results {
		if strings.HasPrefix(r.Title, "The Adventures") {
			ref = r.Ref
		}
	}
	tf, err := s.Torrent(context.Background(), "alice", true, id, ref)
	if err != nil || tf.Data != nil ||
		tf.Magnet != "magnet:?xt=urn:btih:"+hashA+"&dn=The+Adventures+of+Tom+Sawyer+%28audiobook%29&tr=udp%3A%2F%2Ft.example.org%3A1" {
		t.Fatalf("%+v %v", tf, err)
	}
}

// spec: the keyword, the key and the address stay out of the log and the
// reasons
func TestNothingSensitiveLogged(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	n := newFakeNet()
	n.handle("err.example.org", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`<error code="201" description="Incorrect parameter: q=private keyword apikey=SECRETKEY at ` + html.EscapeString(r.URL.String()) + `"/>`))
	})
	s := newTestService(t, n)
	// Fails to connect: the test network's error holds the whole address
	s.store.Install(&Plugin{Schema: 1, ID: "gone", Name: "Gone", Type: TypeTorznab}, true, false,
		map[string]string{"url": "http://gone.example.org:9117/api", "apikey": "SECRETKEY"})
	s.store.Install(&Plugin{Schema: 1, ID: "err", Name: "Err", Type: TypeTorznab}, true, false,
		map[string]string{"url": "http://err.example.org/api", "apikey": "SECRETKEY"})
	addPlugin(t, s, jsonSource("api", "nowhere.example.org"), map[string]string{"key": "SECRETKEY"})
	id, err := s.Start("alice", true, Query{Text: "private keyword"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	v := waitDone(t, s, "alice", id)
	reasons := ""
	for _, src := range v.Sources {
		if src.State != "failed" {
			t.Errorf("%+v", src)
		}
		reasons += src.Error + "\n"
	}
	out := buf.String() + reasons
	for _, bad := range []string{"private", "keyword", "SECRETKEY", "example.org", "http", "?"} {
		if strings.Contains(out, bad) {
			t.Errorf("%q in:\n%s", bad, out)
		}
	}
	for _, id := range []string{"gone", "err", "api"} {
		if !strings.Contains(buf.String(), "source "+id+" failed") {
			t.Errorf("no log line for %s:\n%s", id, buf.String())
		}
	}
}

// A Torznab server's rate limit (Prowlarr: HTTP 429 with an <error>) is a
// busy source
func TestTorznabRateLimit(t *testing.T) {
	src := Source{Plugin: &Plugin{Schema: 1, ID: "p", Name: "P", Type: TypeTorznab}, Settings: map[string]string{"apikey": "K"}}
	body := []byte(`<error code="900" description="Request limit reached"/>`)
	if _, err := interpret(src, &response{Status: 429, Body: body}, "x"); reason(err) != ReasonBusy {
		t.Errorf("429: %v", err)
	}
	// Jackett: a refused key comes with HTTP 200
	body = []byte(`<error code="100" description="Invalid API Key"/>`)
	if _, err := interpret(src, &response{Status: 200, Body: body}, "x"); reason(err) != ReasonRefused {
		t.Errorf("200: %v", err)
	}
}
