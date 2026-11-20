package search

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"downloadcenter/internal/netutil"
	"downloadcenter/internal/torrent"
)

// Limits of the searches.
const (
	minKeyword      = 2
	maxKeyword      = 200
	parallel        = 6                // sources asked at the same time by one search
	keepFinished    = 30 * time.Minute // a finished search stays readable this long
	keepPerOwner    = 3
	keepTotal       = 200
	startsPerMinute = 20
	feedFresh       = time.Hour // a catalogue is used this long without asking its source
	defaultTest     = "ubuntu"
	// .torrent files of results: fetched at most this many at a time per
	// account; a search keeps what it fetched (up to keepTorrents files and
	// keepTorrentBytes) and a failure for failedFor
	torrentsAtOnce   = 2
	keepTorrents     = 8
	keepTorrentBytes = 8 << 20
	failedFor        = time.Minute
)

var (
	ErrBadQuery    = errors.New("Search for 2 to 200 characters")
	ErrBadCategory = errors.New("Unknown category")
	ErrNoSources   = errors.New("No search source is enabled")
	ErrRateLimited = errors.New("Too many requests")
	ErrNotFound    = errors.New("Search not found")
)

// Service runs the searches. Searches live in memory only, each belongs to
// the account that started it; nothing is asked of a source unless someone
// searches, tests a source or installs a plugin from an address.
type Service struct {
	store *Store
	fetch *fetcher
	now   func() time.Time

	mu       sync.Mutex
	jobs     map[string]*job
	seq      uint64                 // order of the searches started
	starts   map[string][]time.Time // owner -> searches started in the last minute
	fetching map[string]int         // owner -> .torrent files being fetched

	feedMu sync.Mutex
	feeds  map[string]*feed // source id -> catalogue of a source matched on the NAS

	// code runs the plugins from another NAS (nil: they never run)
	code    codeRunner
	recheck atomic.Int64 // Unix time of the last background check of code
}

// NewService runs searches over the sources of st with f.
func newService(st *Store, f *fetcher) *Service {
	s := &Service{store: st, fetch: f, now: time.Now, jobs: map[string]*job{}, starts: map[string][]time.Time{}, feeds: map[string]*feed{},
		fetching: map[string]int{}}
	st.changed = s.dropFeed
	return s
}

// feed is a catalogue kept for searches matched on the NAS.
type feed struct {
	mu       sync.Mutex // one request at a time per source
	key      string     // the address it came from (other settings, other catalogue)
	results  []Result
	at       time.Time // fetched or confirmed unchanged
	etag     string
	modified string
}

type job struct {
	id, owner string
	q         Query
	seq       uint64
	cancel    context.CancelFunc

	mu       sync.Mutex
	finished time.Time // zero while running
	sources  []*jobSource
	refs     map[string]link
	got      map[string]*fetched // ref -> its .torrent, once fetched
}

// fetched is the outcome of fetching a result's .torrent.
type fetched struct {
	file *TorrentFile
	err  error
	at   time.Time
	size int
}

type jobSource struct {
	id, name string
	state    string // running | done | failed | cancelled
	count    int
	reason   string
	results  []Result
}

// link is what a result code stands for: a .torrent address of a source.
type link struct {
	url, source, title string
}

// JobView is a search as its owner reads it. Source errors are the fixed
// failure reasons (untranslated).
type JobView struct {
	ID       string         `json:"id"`
	Query    string         `json:"q"`
	Category string         `json:"category"`
	Done     bool           `json:"done"`
	Sources  []SourceStatus `json:"sources"`
	Results  []Result       `json:"results"`
	Total    int            `json:"total"`
}

// SourceStatus is how one source of a search is doing.
type SourceStatus struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
	Count int    `json:"count"`
	Error string `json:"error,omitempty"`
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Start begins a search for owner (admin: an administrator, or a token of
// one): the keyword (2 to 200 characters), a category, and optionally the
// ids of the sources to ask (else every source that is ready, may be used
// by the caller and has the category; a source the caller may not use is
// as unknown). The account's search still running is cancelled. Checks
// come first: a refused search changes nothing.
func (s *Service) Start(owner string, admin bool, q Query, sources []string) (string, error) {
	q.Text = strings.TrimSpace(q.Text)
	if n := utf8.RuneCountInString(q.Text); n < minKeyword || n > maxKeyword || !printable(q.Text) {
		return "", ErrBadQuery
	}
	if !ValidCategory(q.Category) {
		return "", ErrBadCategory
	}
	if q.Category == "" {
		q.Category = CategoryAll
	}
	want := map[string]bool{}
	for _, id := range sources {
		want[id] = true
	}
	var srcs []Source
	for _, src := range s.store.Sources() {
		if src.Ready() && src.Visible(admin) && s.runnable(src) && src.Plugin.Asks(q.Category) && (len(want) == 0 || want[src.Plugin.ID]) {
			srcs = append(srcs, src)
		}
	}
	if len(srcs) == 0 {
		return "", ErrNoSources
	}
	s.mu.Lock()
	now := s.now()
	s.pruneLocked(now)
	recent := s.starts[owner][:0]
	for _, t := range s.starts[owner] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= startsPerMinute {
		s.starts[owner] = recent
		s.mu.Unlock()
		return "", ErrRateLimited
	}
	s.starts[owner] = append(recent, now)
	for _, o := range s.jobs {
		if o.owner == owner {
			o.cancel()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.seq++
	j := &job{id: randomHex(12), owner: owner, q: q, seq: s.seq, cancel: cancel, refs: map[string]link{}, got: map[string]*fetched{}}
	for _, src := range srcs {
		j.sources = append(j.sources, &jobSource{id: src.Plugin.ID, name: src.Plugin.Name, state: "running"})
	}
	s.jobs[j.id] = j
	s.capLocked(owner)
	s.mu.Unlock()
	go s.run(ctx, j, srcs)
	return j.id, nil
}

// pruneLocked forgets searches finished more than keepFinished ago.
func (s *Service) pruneLocked(now time.Time) {
	for id, j := range s.jobs {
		j.mu.Lock()
		old := !j.finished.IsZero() && now.Sub(j.finished) > keepFinished
		j.mu.Unlock()
		if old {
			delete(s.jobs, id)
		}
	}
	for o, l := range s.starts {
		if len(l) == 0 || now.Sub(l[len(l)-1]) >= time.Minute {
			delete(s.starts, o)
		}
	}
}

// capLocked keeps at most keepPerOwner searches of owner and keepTotal in
// all, dropping the oldest (cancelled if they still run).
func (s *Service) capLocked(owner string) {
	drop := func(keep int, of func(*job) bool) {
		var list []*job
		for _, j := range s.jobs {
			if of(j) {
				list = append(list, j)
			}
		}
		for len(list) > keep {
			oldest := 0
			for i, j := range list {
				if j.seq < list[oldest].seq {
					oldest = i
				}
			}
			list[oldest].cancel()
			delete(s.jobs, list[oldest].id)
			list = append(list[:oldest], list[oldest+1:]...)
		}
	}
	drop(keepPerOwner, func(j *job) bool { return j.owner == owner })
	drop(keepTotal, func(*job) bool { return true })
}

func (s *Service) run(ctx context.Context, j *job, srcs []Source) {
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, src := range srcs {
		wg.Add(1)
		go func(i int, src Source) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				j.set(i, nil, ctx.Err())
				return
			}
			defer func() { <-sem }()
			rs, err := s.query(ctx, src, j.q)
			if err == nil {
				rs = s.links(j, src, rs)
			} else if ctx.Err() == nil {
				logFailure(src.Plugin.ID, err)
			}
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			j.set(i, rs, err)
		}(i, src)
	}
	wg.Wait()
	j.mu.Lock()
	j.finished = s.now()
	j.mu.Unlock()
	j.cancel()
}

// set records the outcome of source i.
func (j *job) set(i int, rs []Result, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	js := j.sources[i]
	switch {
	case errors.Is(err, context.Canceled):
		js.state = "cancelled"
	case err != nil:
		js.state, js.reason = "failed", reasonOf(err)
	default:
		js.state, js.count, js.results = "done", len(rs), rs
	}
}

func reasonOf(err error) string {
	var f *Failure
	if errors.As(err, &f) {
		return f.Reason
	}
	return ReasonConnect
}

// logFailure writes a source's failure: its id, our fixed reason and what
// the fetch could tell in our own words, never text a source or a plugin
// wrote.
func logFailure(id string, err error) {
	var f *Failure
	if errors.As(err, &f) {
		reason := f.Reason
		if f.template != "" {
			reason = f.template
		}
		if f.Detail != "" {
			log.Printf("search: source %s failed: %s (%s)", id, reason, f.Detail)
		} else {
			log.Printf("search: source %s failed: %s", id, reason)
		}
		return
	}
	log.Printf("search: source %s failed: %s", id, reasonOf(err))
}

// query asks one source and returns its results for q.
func (s *Service) query(ctx context.Context, src Source, q Query) ([]Result, error) {
	p := src.Plugin
	if p.RunsCode() {
		rs, err := s.codeSearch(ctx, src, q)
		if err != nil {
			return nil, err
		}
		return p.Select(rs, q), nil
	}
	call, err := p.BuildRequest(q, src.Settings)
	if errors.Is(err, ErrEmptyQuery) {
		return []Result{}, nil
	}
	if err != nil {
		return nil, fail(ReasonConnect, "no request")
	}
	var rs []Result
	if p.Match == "local" {
		rs, err = s.catalogue(ctx, src, call)
	} else {
		rs, err = s.ask(ctx, src, call, q)
	}
	if err != nil {
		return nil, err
	}
	return p.Select(rs, q), nil
}

func accept(p *Plugin) string {
	if p.Type == TypeJSON {
		return "application/json"
	}
	return "application/rss+xml, application/atom+xml, application/xml;q=0.9, text/xml;q=0.8"
}

// ask makes the request of a search and reads the answer.
func (s *Service) ask(ctx context.Context, src Source, call *Call, q Query) ([]Result, error) {
	resp, err := s.fetch.fetch(ctx, request{url: call.URL, headers: call.Headers, accept: accept(src.Plugin), limit: limitSearch})
	if err != nil {
		return nil, err
	}
	return interpret(src, resp, q.Text)
}

// interpret reads an answer: results, or the source's failure. A Torznab
// <error> counts whatever the HTTP status.
func interpret(src Source, resp *response, keyword string) ([]Result, error) {
	p := src.Plugin
	if resp.Magnet != "" {
		return nil, fail(ReasonUnreadable, "redirected to a magnet link")
	}
	ok := resp.Status/100 == 2
	// A rate limit is "busy", whatever error text comes with it
	if !ok && (p.Type == TypeJSON || resp.Status == http.StatusTooManyRequests) {
		return nil, statusFailure(resp.Status, len(p.SecretValues(src.Settings)) > 0)
	}
	rs, err := p.Results(resp.Body, resp.URL)
	var te *TorznabError
	switch {
	case errors.As(err, &te):
		// The server's text may repeat the request: take out the key, the
		// keyword and each of its words
		redact := append(p.SecretValues(src.Settings), keyword)
		for _, w := range strings.Fields(keyword) {
			if utf8.RuneCountInString(w) >= 3 {
				redact = append(redact, w)
			}
		}
		return nil, torznabReason(te, redact)
	case !ok:
		return nil, statusFailure(resp.Status, len(p.SecretValues(src.Settings)) > 0)
	case err != nil:
		return nil, fail(ReasonUnreadable, p.Type)
	}
	return rs, nil
}

// catalogue is the whole catalogue of a source matched on the NAS: kept
// for an hour, then asked again with If-None-Match / If-Modified-Since.
func (s *Service) catalogue(ctx context.Context, src Source, call *Call) ([]Result, error) {
	s.feedMu.Lock()
	f := s.feeds[src.Plugin.ID]
	if f == nil {
		f = &feed{}
		s.feeds[src.Plugin.ID] = f
	}
	s.feedMu.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	now := s.now()
	have := f.key == call.URL && f.results != nil
	if have && now.Sub(f.at) < feedFresh {
		return f.results, nil
	}
	rq := request{url: call.URL, headers: call.Headers, accept: accept(src.Plugin), limit: limitSearch}
	if have {
		rq.etag, rq.modified = f.etag, f.modified
	}
	resp, err := s.fetch.fetch(ctx, rq)
	if err != nil {
		return nil, err
	}
	if have && resp.Status == http.StatusNotModified {
		f.at = now
		return f.results, nil
	}
	rs, err := interpret(src, resp, "")
	if err != nil {
		return nil, err
	}
	f.key, f.results, f.at, f.etag, f.modified = call.URL, rs, now, resp.ETag, resp.Modified
	return rs, nil
}

// dropFeed forgets a source's catalogue (its definition or settings
// changed).
func (s *Service) dropFeed(id string) {
	s.feedMu.Lock()
	delete(s.feeds, id)
	s.feedMu.Unlock()
}

// torrentRule is how the .torrent address of a result is checked: the
// strict rules (public unicast addresses, ports 80 and 443), except on the
// host of the administrator's Torznab server, which keeps the usual guard
// on any port.
func torrentRule(src Source) *netutil.Rule {
	server := ""
	if src.Plugin.Type == TypeTorznab {
		server = src.Plugin.Host(src.Settings)
	}
	return &netutil.Rule{Strict: func(host string) bool {
		return server == "" || !strings.EqualFold(strings.TrimSuffix(host, "."), server)
	}}
}

// links gives each result with a .torrent address a code to fetch it
// through dcd, shows the address only when it holds none of the source's
// secrets, is not on the server the administrator set for it and passes
// the rule it will be fetched with as far as can be told without
// resolving, and drops a page address that fails the secret and server
// tests. A .torrent or tracker address that holds a secret is dropped
// altogether unless it is on the source's own host (the host of a plugin
// file's request, or the administrator's Torznab server): a source's
// answer cannot send the key elsewhere. Results left with nothing to add
// are dropped.
func (s *Service) links(j *job, src Source, rs []Result) []Result {
	secrets := src.Plugin.SecretValues(src.Settings)
	server := ""
	if src.Plugin.Type == TypeTorznab {
		server = src.Plugin.Host(src.Settings)
	}
	own := ""
	if src.Plugin.code == nil {
		own = src.Plugin.Host(src.Settings)
	}
	keyed := func(raw string) bool {
		if !containsSecret(raw, secrets) {
			return false
		}
		u, err := url.Parse(raw)
		return err != nil || own == "" || !strings.EqualFold(strings.TrimSuffix(u.Hostname(), "."), own)
	}
	rule := torrentRule(src)
	fetchable := func(raw string) bool {
		u, err := url.Parse(raw)
		return err == nil && rule.CheckLiteral(u.Hostname(), port(u)) == nil
	}
	public := func(raw string) bool {
		if server != "" {
			if u, err := url.Parse(raw); err != nil || strings.EqualFold(u.Hostname(), server) {
				return false
			}
		}
		return !containsSecret(raw, secrets)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	out := rs[:0]
	for i := range rs {
		r := &rs[i]
		r.TorrentURL, r.Ref = "", ""
		if r.Torrent != "" && keyed(r.Torrent) {
			r.Torrent, r.TorrentSource = "", ""
		}
		if len(secrets) > 0 && len(r.Trackers) > 0 {
			var kept []string
			for _, tr := range r.Trackers {
				if !keyed(tr) {
					kept = append(kept, tr)
				}
			}
			if len(kept) != len(r.Trackers) {
				r.Trackers = kept
				if r.Magnet != "" {
					r.Magnet = torrent.MagnetURI(r.InfoHash, r.Title, r.Trackers)
				}
			}
		}
		if r.InfoHash == "" && r.Torrent == "" {
			continue
		}
		if r.Torrent != "" {
			r.Ref = randomHex(16)
			j.refs[r.Ref] = link{url: r.Torrent, source: src.Plugin.ID, title: r.Title}
			if public(r.Torrent) && fetchable(r.Torrent) {
				r.TorrentURL = r.Torrent
			}
		}
		if r.Page != "" && !public(r.Page) {
			r.Page = ""
		}
		out = append(out, *r)
	}
	return out
}

func containsSecret(s string, secrets []string) bool {
	l := strings.ToLower(s)
	for _, v := range secrets {
		if v == "" {
			continue
		}
		for _, form := range []string{v, url.QueryEscape(v), url.PathEscape(v)} {
			if strings.Contains(l, strings.ToLower(form)) {
				return true
			}
		}
	}
	return false
}

// get is a search of owner (ErrNotFound for anyone else's).
func (s *Service) get(owner, id string) (*job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(s.now())
	j := s.jobs[id]
	if j == nil || j.owner != owner {
		return nil, ErrNotFound
	}
	return j, nil
}

// View is a search of owner with the results so far, merged across its
// sources.
func (s *Service) View(owner, id string) (*JobView, error) {
	j, err := s.get(owner, id)
	if err != nil {
		return nil, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	v := &JobView{ID: j.id, Query: j.q.Text, Category: j.q.Category, Done: !j.finished.IsZero(), Sources: []SourceStatus{}}
	var lists [][]Result
	for _, js := range j.sources {
		v.Sources = append(v.Sources, SourceStatus{ID: js.id, Name: js.name, State: js.state, Count: js.count, Error: js.reason})
		lists = append(lists, js.results)
	}
	v.Results = Merge(lists)
	v.Total = len(v.Results)
	return v, nil
}

// Cancel stops a search of owner: requests still running are aborted.
func (s *Service) Cancel(owner, id string) error {
	j, err := s.get(owner, id)
	if err != nil {
		return err
	}
	j.cancel()
	return nil
}

// TorrentFile is what a result code gives: the .torrent, or the magnet
// link the source sent instead (rebuilt by dcd).
type TorrentFile struct {
	Data     []byte
	InfoHash string
	Magnet   string
}

// Torrent fetches the .torrent of a result of owner's search (admin: an
// administrator, or a token of one), while its source is still on and may
// be used by the caller. Errors are ErrNotFound, ErrRateLimited (more than
// torrentsAtOnce at a time) or a *Failure, always with ReasonNotTorrent:
// what went wrong with the address is not told. The search keeps what was
// fetched, so asking again costs nothing.
func (s *Service) Torrent(ctx context.Context, owner string, admin bool, id, ref string) (*TorrentFile, error) {
	j, err := s.get(owner, id)
	if err != nil {
		return nil, err
	}
	j.mu.Lock()
	l, ok := j.refs[ref]
	j.mu.Unlock()
	if !ok {
		return nil, ErrNotFound
	}
	src, ok := s.store.Get(l.source)
	if !ok || !src.Enabled || !src.Visible(admin) {
		return nil, ErrNotFound
	}
	now := s.now()
	j.mu.Lock()
	if g := j.got[ref]; g != nil && (g.err == nil || now.Sub(g.at) < failedFor) {
		j.mu.Unlock()
		return g.file, g.err
	}
	j.mu.Unlock()
	s.mu.Lock()
	if s.fetching[owner] >= torrentsAtOnce {
		s.mu.Unlock()
		return nil, ErrRateLimited
	}
	s.fetching[owner]++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.fetching[owner]--; s.fetching[owner] <= 0 {
			delete(s.fetching, owner)
		}
		s.mu.Unlock()
	}()
	file, err := s.fetchTorrent(ctx, src, l)
	if err != nil {
		var f *Failure
		if errors.As(err, &f) {
			logFailure(src.Plugin.ID, f)
		}
		err = fail(ReasonNotTorrent, "")
		if ctx.Err() != nil {
			// Not the source's doing: may be asked again at once
			return nil, err
		}
	}
	j.keep(ref, &fetched{file: file, err: err, at: now})
	return file, err
}

// fetchTorrent gets the .torrent at a result's address with the rule for
// result addresses.
func (s *Service) fetchTorrent(ctx context.Context, src Source, l link) (*TorrentFile, error) {
	resp, err := s.fetch.fetch(ctx, request{url: l.url, accept: "application/x-bittorrent, */*;q=0.5", limit: limitTorrent, rule: torrentRule(src)})
	if err != nil {
		return nil, err
	}
	if resp.Magnet != "" {
		hash, trackers := parseMagnet(resp.Magnet)
		if hash == "" {
			return nil, fail(ReasonNotTorrent, "magnet without infohash")
		}
		return &TorrentFile{Magnet: torrent.MagnetURI(hash, l.title, trackers), InfoHash: hash}, nil
	}
	if resp.Status/100 != 2 {
		return nil, fail(ReasonNotTorrent, "status "+strconv.Itoa(resp.Status))
	}
	meta, err := torrent.Parse(resp.Body)
	if err != nil {
		return nil, fail(ReasonNotTorrent, "not a torrent")
	}
	return &TorrentFile{Data: resp.Body, InfoHash: meta.InfoHash}, nil
}

// keep remembers what fetching a ref gave, within the search's limits
// (the oldest files make room for new ones).
func (j *job) keep(ref string, g *fetched) {
	if g.file != nil {
		g.size = len(g.file.Data)
	}
	if g.size > keepTorrentBytes/2 {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.got[ref] = g
	for {
		n, size, oldest := 0, 0, ""
		for r, x := range j.got {
			n++
			size += x.size
			if oldest == "" || x.at.Before(j.got[oldest].at) {
				oldest = r
			}
		}
		if n <= keepTorrents && size <= keepTorrentBytes {
			return
		}
		delete(j.got, oldest)
	}
}

// Test asks a source once with its test keyword (or "ubuntu") and returns
// the number of results; its catalogue, if any, is fetched anew and not
// kept.
func (s *Service) Test(ctx context.Context, src Source) (int, error) {
	if !src.Plugin.Complete(src.Settings) {
		return 0, fail(ReasonNeedsSetup, "")
	}
	q := Query{Text: src.Plugin.TestQuery, Category: CategoryAll}
	if q.Text == "" {
		q.Text = defaultTest
	}
	if src.Plugin.RunsCode() {
		rs, err := s.codeSearch(ctx, src, q)
		if err != nil {
			logFailure(src.Plugin.ID, err)
			return 0, err
		}
		return len(src.Plugin.Select(rs, q)), nil
	}
	call, err := src.Plugin.BuildRequest(q, src.Settings)
	if err != nil {
		return 0, fail(ReasonConnect, "no request")
	}
	rs, err := s.ask(ctx, src, call, q)
	if err != nil {
		logFailure(src.Plugin.ID, err)
		return 0, err
	}
	return len(src.Plugin.Select(rs, q)), nil
}

// FetchPlugin gets a plugin file from an https address.
func (s *Service) FetchPlugin(ctx context.Context, raw string) ([]byte, error) {
	if u, err := url.Parse(strings.TrimSpace(raw)); err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return nil, invalid(ReasonPluginHTTPS)
	}
	resp, err := s.fetch.fetch(ctx, request{url: strings.TrimSpace(raw), accept: "application/json", limit: limitPlugin})
	var f *Failure
	if errors.As(err, &f) && f.Reason == ReasonTooLarge {
		return nil, invalid("The plugin is larger than 64 KiB")
	}
	if err != nil {
		return nil, err
	}
	if resp.Magnet != "" {
		return nil, fail(ReasonUnreadable, "")
	}
	if resp.Status/100 != 2 {
		return nil, statusFailure(resp.Status, false)
	}
	return resp.Body, nil
}

// Stop cancels every search.
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		j.cancel()
	}
}

// Available reports whether any source is ready to search for an
// administrator.
func (s *Service) Available() bool { return s.AvailableFor(true) }

// AvailableFor reports whether a caller has a source to search.
func (s *Service) AvailableFor(admin bool) bool {
	for _, src := range s.store.Sources() {
		if src.Ready() && src.Visible(admin) && s.runnable(src) {
			return true
		}
	}
	return false
}
