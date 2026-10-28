package trackers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"downloadcenter/internal/store"
)

func TestValid(t *testing.T) {
	for _, c := range []struct {
		url string
		ok  bool
	}{
		{"udp://tracker.opentrackr.org:1337/announce", true},
		{"http://tracker.example.org/announce", true},
		{"https://tracker.example.org:443/announce?passkey=x", true},
		{"HTTP://tracker.example.org/announce", true},
		{"udp://tracker.example.org/announce", false}, // udp needs a port
		{"wss://tracker.example.org/announce", false},
		{"magnet:?xt=urn:btih:x", false},
		{"http:///announce", false},
		{"http://tracker.example.org:0/announce", false},
		{"http://tracker.example.org:70000/announce", false},
		{"http://tracker.example.org/ announce", false},
		{"http://tracker.example.org/\nannounce", false},
		{"", false},
		{"http://t.example.org/" + strings.Repeat("a", 500), false},
	} {
		if err := Valid(c.url); (err == nil) != c.ok {
			t.Errorf("Valid(%q) = %v", c.url, err)
		}
	}
}

func TestAllowed(t *testing.T) {
	for _, c := range []struct {
		url string
		ok  bool
	}{
		{"http://127.0.0.1:8080/announce", false},
		{"udp://[::1]:6969/announce", false},
		{"http://localhost:6969/announce", false},
		{"http://169.254.1.1/announce", false},
		{"http://0.0.0.0/announce", false},
		{"http://192.168.1.10:6969/announce", true}, // the LAN is fine
		{"udp://tracker.example.org:6969/announce", true},
	} {
		if err := Allowed(c.url); (err == nil) != c.ok {
			t.Errorf("Allowed(%q) = %v", c.url, err)
		}
	}
}

func TestParseFeed(t *testing.T) {
	feed := func(schema int, list ...string) []byte {
		b, _ := json.Marshal(Feed{Schema: schema, Trackers: list})
		return b
	}
	list, err := ParseFeed(feed(1, "udp://a.example.org:1/announce", "udp://10.0.0.1:6969/announce", "http://127.0.0.1/announce",
		"http://[fe80::1]/announce", "http://localhost/announce", "wss://b.example.org/", "udp://a.example.org:1/announce", "http://c.example.org/announce"))
	if err != nil || len(list) != 2 || list[0] != "udp://a.example.org:1/announce" || list[1] != "http://c.example.org/announce" {
		t.Fatalf("parse %q %v", list, err)
	}
	if _, err := ParseFeed(feed(2, "udp://a.example.org:1/announce")); err == nil || !strings.Contains(err.Error(), "schema 2") {
		t.Fatalf("schema 2: %v", err)
	}
	if _, err := ParseFeed(feed(1, "udp://10.0.0.1:6969/announce")); err == nil {
		t.Fatal("a list without usable trackers must fail")
	}
	if _, err := ParseFeed(append(feed(1, "udp://a.example.org:1/announce"), make([]byte, MaxFeedSize)...)); err == nil {
		t.Fatal("too large")
	}
	var many []string
	for i := 0; i < 150; i++ {
		many = append(many, fmt.Sprintf("udp://t%d.example.org:1/announce", i))
	}
	if list, _ := ParseFeed(feed(1, many...)); len(list) != MaxTrackers {
		t.Fatalf("%d trackers kept", len(list))
	}
}

func TestBuiltin(t *testing.T) {
	b := Builtin()
	if len(b) < 10 {
		t.Fatalf("built-in list has %d trackers", len(b))
	}
	if len(Clean(strings.Split(builtinText, "\n"))) != len(b) {
		t.Fatal("built-in list must pass the feed rules")
	}
	for _, line := range strings.Split(builtinText, "\n") {
		if line = strings.TrimSpace(line); line != "" && !public(line) {
			t.Errorf("built-in tracker %q does not pass the feed rules", line)
		}
	}
}

func newService(t *testing.T, data string, url string) (*Service, *store.DB) {
	db, err := store.Open(filepath.Join(data, "dc.db"))
	if err != nil {
		t.Fatal(err)
	}
	if url != "" {
		os.WriteFile(filepath.Join(data, "trackers_feed"), []byte(url+"\n"), 0600)
	}
	return New(db, data, "1.3.0"), db
}

func TestService(t *testing.T) {
	data := t.TempDir()
	var hits atomic.Int32
	var body atomic.Value
	body.Store(`{"schema":1,"trackers":["udp://a.example.org:1/announce","udp://b.example.org:1/announce"]}`)
	fail := atomic.Bool{}
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if fail.Load() {
			http.Error(w, "down", 503)
			return
		}
		w.Write([]byte(body.Load().(string)))
	}))
	defer srv.Close()
	s, db := newService(t, data, srv.URL)
	s.Lookup = func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("203.0.113.9")}, nil }
	now := time.Unix(1_800_000_000, 0)
	s.Now = func() time.Time { return now }
	wait := func(n int32) {
		for i := 0; i < 100 && hits.Load() < n; i++ {
			time.Sleep(20 * time.Millisecond)
		}
		for i := 0; i < 100; i++ {
			s.mu.Lock()
			r := s.running
			s.mu.Unlock()
			if !r {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	// Nothing is fetched until the list is used
	if in := s.Peek(false); in.Source != "builtin" || in.Count != len(Builtin()) || hits.Load() != 0 {
		t.Fatalf("peek %+v, %d fetches", in, hits.Load())
	}
	s.Tick(false)
	if hits.Load() != 0 {
		t.Fatal("Tick without auto-attach fetched")
	}
	// First use: the built-in list now, a fetch in the background, which fails
	if l := s.Current(); len(l) != len(Builtin()) {
		t.Fatalf("current %d", len(l))
	}
	wait(1)
	if in := s.Peek(false); in.Source != "builtin" || in.Error == "" || in.CheckedAt != now.Unix() {
		t.Fatalf("after a failed fetch %+v", in)
	}
	// Within the hour: no new attempt
	now = now.Add(30 * time.Minute)
	s.Current()
	if hits.Load() != 1 {
		t.Fatalf("%d fetches within an hour", hits.Load())
	}
	// After the hour: fetched, used from the next call on
	fail.Store(false)
	now = now.Add(31 * time.Minute)
	s.Current()
	wait(2)
	in := s.Peek(true)
	if in.Source != "feed" || in.Count != 2 || in.UpdatedAt != now.Unix() || in.Error != "" {
		t.Fatalf("after fetching %+v", in)
	}
	// Fresh: no fetch for a day
	now = now.Add(23 * time.Hour)
	s.Current()
	s.Tick(true)
	if hits.Load() != 2 {
		t.Fatalf("%d fetches for a fresh list", hits.Load())
	}
	// Auto-attach on: once a day
	now = now.Add(2 * time.Hour)
	s.Tick(true)
	wait(3)
	if hits.Load() != 3 {
		t.Fatalf("tick: %d fetches", hits.Load())
	}
	// A bad list keeps the one in use
	body.Store(`{"schema":2,"trackers":["udp://c.example.org:1/announce"]}`)
	in, err := s.Refresh(context.Background())
	if err == nil || in.Count != 2 || !strings.Contains(in.Error, "schema 2") {
		t.Fatalf("refresh with schema 2: %+v %v", in, err)
	}
	// Kept across a restart
	s2 := New(db, data, "1.3.0")
	if in := s2.Peek(true); in.Source != "feed" || in.Count != 2 {
		t.Fatalf("after restart %+v", in)
	}
}

func TestMissing(t *testing.T) {
	if m := Missing([]string{"a", "b", "c"}, []string{"b", "x"}); len(m) != 2 || m[0] != "a" || m[1] != "c" {
		t.Fatalf("missing %q", m)
	}
}

func TestDropInternal(t *testing.T) {
	lookup := func(ctx context.Context, host string) ([]net.IP, error) {
		switch host {
		case "lan.example.org":
			return []net.IP{net.ParseIP("192.168.1.1")}, nil
		case "loop.example.org":
			return []net.IP{net.ParseIP("203.0.113.5"), net.ParseIP("127.0.0.1")}, nil
		case "gone.example.org":
			return nil, errors.New("no such host")
		}
		return []net.IP{net.ParseIP("203.0.113.9")}, nil
	}
	got := DropInternal(context.Background(), []string{"udp://ok.example.org:1/announce", "udp://lan.example.org:1/announce",
		"http://loop.example.org/announce", "udp://gone.example.org:1/announce", "udp://203.0.113.7:1/announce"}, lookup)
	if strings.Join(got, " ") != "udp://ok.example.org:1/announce udp://gone.example.org:1/announce udp://203.0.113.7:1/announce" {
		t.Fatalf("kept %q", got)
	}
	// Numeric forms of private addresses never make it into a list
	if l := Clean([]string{"udp://2130706433:1/announce", "udp://0xc0a80101:1/announce", "udp://ok.example.org:1/announce"}); len(l) != 1 {
		t.Fatalf("clean kept %q", l)
	}
}
