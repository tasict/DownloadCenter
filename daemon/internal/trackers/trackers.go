// Package trackers keeps the public tracker list: a list of public trackers
// that Download Center adds to torrents when the user asks for it (or the
// administrator turned on adding it to new torrents). The list comes from
// the product site's trackers.json, which a GitHub Pages workflow builds
// every day from newTrackon's list of stable trackers; a snapshot of that
// list is built in for when the site was never reached.
package trackers

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"downloadcenter/internal/netutil"
	"downloadcenter/internal/store"
)

// FeedURL is where dcd reads the list.
const FeedURL = "https://tasict.github.io/DownloadCenter/trackers.json"

const (
	MaxFeedSize = 256 << 10 // bytes of trackers.json
	MaxTrackers = 100       // trackers taken from it
	MaxURL      = 512       // characters of one tracker address

	staleAfter = 24 * time.Hour // the list is fetched again after this long
	minGap     = time.Hour      // between two attempts, failed ones included
	metaKey    = "public_trackers"
)

//go:embed builtin.txt
var builtinText string

// Feed is trackers.json.
type Feed struct {
	Schema    int      `json:"schema"`
	Generated int64    `json:"generated,omitempty"`
	Source    string   `json:"source,omitempty"`
	Trackers  []string `json:"trackers"`
}

// ErrInvalid is a tracker address that is not an http, https or udp URL.
var ErrInvalid = errors.New("Not a valid tracker address")

// ErrNotAllowed is a tracker address on the NAS itself (regular users).
var ErrNotAllowed = errors.New("This tracker address is not allowed")

// Valid checks the form of a tracker address: http, https or udp, a host,
// a port for udp, at most MaxURL characters and nothing that is not part
// of a URL.
func Valid(raw string) error {
	if raw == "" || len(raw) > MaxURL {
		return ErrInvalid
	}
	for _, c := range raw {
		if c <= ' ' || c == 0x7f {
			return ErrInvalid
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Hostname() == "" {
		return ErrInvalid
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	case "udp":
		if u.Port() == "" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return ErrInvalid
		}
	}
	return nil
}

// Allowed refuses a tracker whose host is localhost or a literal address
// on the NAS itself (netutil.Forbidden). Names are not resolved here: dc-bt
// checks the addresses of regular users' torrents when it connects.
func Allowed(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return ErrInvalid
	}
	if netutil.CheckHost(context.Background(), u.Hostname(), true) != nil {
		return ErrNotAllowed
	}
	return nil
}

// public reports an address fit for a public list: no literal loopback,
// link-local, private, unspecified or multicast address, no localhost.
func public(raw string) bool {
	if Valid(raw) != nil {
		return false
	}
	u, _ := url.Parse(raw)
	h := u.Hostname()
	if strings.EqualFold(strings.TrimSuffix(h, "."), "localhost") {
		return false
	}
	if ip := netutil.HostIP(h); ip != nil {
		return !(ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() ||
			ip.IsUnspecified() || ip.IsMulticast())
	}
	return true
}

// Clean keeps the addresses fit for a public list, without duplicates, at
// most MaxTrackers of them.
func Clean(list []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range list {
		t = strings.TrimSpace(t)
		if seen[t] || !public(t) {
			continue
		}
		seen[t] = true
		out = append(out, t)
		if len(out) == MaxTrackers {
			break
		}
	}
	return out
}

// ParseFeed reads trackers.json: schema 1, at most MaxFeedSize, the usable
// addresses (see Clean); a list without any is an error.
func ParseFeed(b []byte) ([]string, error) {
	if len(b) > MaxFeedSize {
		return nil, errors.New("trackers.json is too large")
	}
	var f Feed
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("trackers.json: %v", err)
	}
	if f.Schema != 1 {
		return nil, fmt.Errorf("unknown trackers.json schema %d", f.Schema)
	}
	list := Clean(f.Trackers)
	if len(list) == 0 {
		return nil, errors.New("trackers.json has no usable tracker")
	}
	return list, nil
}

// Builtin is the list built into this version.
func Builtin() []string {
	return Clean(strings.Split(builtinText, "\n"))
}

// Info is the state of the list.
type Info struct {
	Trackers  []string `json:"trackers,omitempty"`
	Count     int      `json:"count"`
	Source    string   `json:"source"`     // "feed" (from the site) or "builtin"
	UpdatedAt int64    `json:"updated_at"` // when the list in use was fetched (0: built in)
	CheckedAt int64    `json:"checked_at"` // the last attempt
	Error     string   `json:"error,omitempty"`
}

type cached struct {
	Trackers  []string `json:"trackers,omitempty"`
	UpdatedAt int64    `json:"updated_at,omitempty"`
	CheckedAt int64    `json:"checked_at,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// Service fetches the list when it is needed and keeps it in meta
// "public_trackers", so it survives restarts.
type Service struct {
	db      *store.DB
	url     string
	custom  bool // data/trackers_feed replaced the URL
	version string
	// Lookup resolves the names of a fetched list (see DropInternal)
	Lookup Lookup
	// Proxy returns the proxy URL for the request (the notifications
	// proxy), "" for none.
	Proxy func() string
	Now   func() time.Time

	mu      sync.Mutex
	c       cached
	running bool       // a background fetch is under way
	fetchMu sync.Mutex // one fetch at a time
}

// New loads the saved list. A file data/trackers_feed (first line: a URL,
// http(s) or file://) replaces the site's list, for testing.
func New(db *store.DB, data, version string) *Service {
	s := &Service{db: db, url: FeedURL, version: version, Now: time.Now, Lookup: DefaultLookup, Proxy: func() string { return "" }}
	if b, err := os.ReadFile(filepath.Join(data, "trackers_feed")); err == nil {
		if u := strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0]); u != "" {
			s.url, s.custom = u, true
			log.Printf("trackers: public tracker list from %s (data/trackers_feed), not from the site", u)
		}
	}
	if v := db.Meta(metaKey); v != "" {
		json.Unmarshal([]byte(v), &s.c)
	}
	return s
}

func (s *Service) info(withList bool) Info {
	in := Info{Source: "builtin", CheckedAt: s.c.CheckedAt, Error: s.c.Error}
	list := s.c.Trackers
	if len(list) > 0 {
		in.Source, in.UpdatedAt = "feed", s.c.UpdatedAt
	} else {
		list = Builtin()
	}
	in.Count = len(list)
	if withList {
		in.Trackers = append([]string{}, list...)
	}
	return in
}

// Current is the list to use now. When it is more than a day old (or
// built in) and the last attempt is more than an hour ago, it is fetched
// again in the background; this call does not wait for that.
func (s *Service) Current() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	if (s.c.UpdatedAt == 0 || now.Sub(time.Unix(s.c.UpdatedAt, 0)) >= staleAfter) && now.Sub(time.Unix(s.c.CheckedAt, 0)) >= minGap {
		s.startLocked()
	}
	return s.info(true).Trackers
}

// Info reports the list (with Current's background update).
func (s *Service) Info() Info {
	list := s.Current()
	s.mu.Lock()
	defer s.mu.Unlock()
	in := s.info(false)
	in.Trackers = list
	return in
}

// Peek reports the list without fetching anything (settings, task details).
func (s *Service) Peek(withList bool) Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info(withList)
}

// Tick runs from the manager's housekeeping: while new torrents get the
// list, it is fetched once a day.
func (s *Service) Tick(auto bool) {
	if !auto {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Now().Sub(time.Unix(s.c.CheckedAt, 0)) >= staleAfter {
		s.startLocked()
	}
}

func (s *Service) startLocked() {
	if s.running {
		return
	}
	s.running = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		err := s.fetch(ctx)
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		if err != nil {
			log.Printf("trackers: %v", err)
		}
	}()
}

// Refresh fetches the list now, whatever its age (an administrator's
// "Update now"); a failure keeps the list in use.
func (s *Service) Refresh(ctx context.Context) (Info, error) {
	err := s.fetch(ctx)
	return s.Peek(true), err
}

// fetch reads the list and keeps it; one fetch at a time.
func (s *Service) fetch(ctx context.Context) error {
	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()
	b, err := s.get(ctx)
	var list []string
	if err == nil {
		list, err = ParseFeed(b)
	}
	if err == nil {
		if list = DropInternal(ctx, list, s.Lookup); len(list) == 0 {
			err = errors.New("trackers.json has no usable tracker")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now().Unix()
	s.c.CheckedAt = now
	if err != nil {
		s.c.Error = err.Error()
	} else {
		s.c.Trackers, s.c.UpdatedAt, s.c.Error = list, now, ""
	}
	if b, e := json.Marshal(s.c); e == nil {
		s.db.SetMeta(metaKey, string(b))
	}
	return err
}

func (s *Service) get(ctx context.Context) ([]byte, error) {
	var r io.ReadCloser
	if p, ok := strings.CutPrefix(s.url, "file://"); ok && s.custom {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		r = f
	} else {
		if !strings.HasPrefix(s.url, "https://") && !(s.custom && strings.HasPrefix(s.url, "http://")) {
			return nil, fmt.Errorf("refusing %s", s.url)
		}
		req, err := http.NewRequestWithContext(ctx, "GET", s.url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "DownloadCenter/"+s.version)
		resp, err := netutil.Client(30*time.Second, false, s.Proxy()).Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode/100 != 2 {
			resp.Body.Close()
			return nil, fmt.Errorf("%s: HTTP %d", s.url, resp.StatusCode)
		}
		r = resp.Body
	}
	defer r.Close()
	b, err := io.ReadAll(io.LimitReader(r, MaxFeedSize+1))
	if err == nil && len(b) > MaxFeedSize {
		err = errors.New("trackers.json is too large")
	}
	return b, err
}

// Missing lists the trackers of list that have is lacks.
func Missing(list, have []string) []string {
	got := map[string]bool{}
	for _, t := range have {
		got[t] = true
	}
	var out []string
	for _, t := range list {
		if !got[t] {
			out = append(out, t)
		}
	}
	return out
}

// Cached is the list in use, without fetching anything.
func (s *Service) Cached() []string { return s.Peek(true).Trackers }

// Check trims a list of tracker addresses given by a user, drops empty
// lines and duplicates, and checks each (Valid; Allowed unless admin). On
// failure it returns the address at fault with ErrInvalid or ErrNotAllowed.
func Check(list []string, admin bool) ([]string, string, error) {
	var out []string
	seen := map[string]bool{}
	for _, u := range list {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		if err := Valid(u); err != nil {
			return nil, u, err
		}
		if !admin {
			if err := Allowed(u); err != nil {
				return nil, u, err
			}
		}
		out = append(out, u)
	}
	return out, "", nil
}

// Lookup resolves a host name (DefaultLookup; tests replace it).
type Lookup func(ctx context.Context, host string) ([]net.IP, error)

// DefaultLookup is the system resolver.
func DefaultLookup(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

// DropInternal removes the trackers whose name resolves to an address no
// public tracker has (loopback, link-local, private, unspecified, multicast
// or the NAS's own): a list from the internet must not make torrents
// announce inside the LAN. Names that do not resolve now are kept.
func DropInternal(ctx context.Context, list []string, lookup Lookup) []string {
	drop := make([]bool, len(list))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, raw := range list {
		u, err := url.Parse(raw)
		if err != nil || netutil.HostIP(u.Hostname()) != nil {
			continue // literal addresses were checked already
		}
		wg.Add(1)
		go func(i int, host string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			c, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			ips, err := lookup(c, host)
			if err != nil {
				return
			}
			for _, ip := range ips {
				if internal(ip) {
					drop[i] = true
					return
				}
			}
		}(i, u.Hostname())
	}
	wg.Wait()
	out := []string{}
	for i, u := range list {
		if !drop[i] {
			out = append(out, u)
		}
	}
	return out
}

func internal(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() ||
		ip.IsUnspecified() || ip.IsMulticast() || netutil.Forbidden(ip)
}
