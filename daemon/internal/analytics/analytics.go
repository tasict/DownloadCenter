// Package analytics sends anonymous usage statistics of one installation to
// Google Analytics (GA4 Measurement Protocol): once a day, counts and states
// only. Every parameter name comes from a fixed table or is built from a
// member of a fixed set, and every value is a number (or a version string
// checked against a pattern), so file names, links, accounts and addresses
// cannot reach the payload. Nothing is sent before an administrator has seen
// the choice (the usage notice or Settings › About and updates).
package analytics

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/netutil"
	"downloadcenter/internal/notify"
	"downloadcenter/internal/qts"
	"downloadcenter/internal/store"
)

// Set at build time from .secret/analytics.env (build.sh). Without them
// nothing is sent; data/analytics_dryrun still logs what would be sent.
var (
	MeasurementID string
	APISecret     string
)

const (
	endpoint      = "https://www.google-analytics.com/mp/collect"
	debugEndpoint = "https://www.google-analytics.com/debug/mp/collect"
	sendEvery     = 24 * time.Hour
	// Measurement Protocol limits
	maxEvents     = 25 // per request
	maxNameLen    = 40 // event and parameter names
	maxPropName   = 24 // user property names
	maxPropValue  = 36 // user property values
	maxUIPerPost  = 50 // one UI report may add at most this much to one counter
	uiMinInterval = 20 * time.Second
)

// Meta keys
const (
	kID      = "analytics_id"      // random install id, the GA client_id
	kOff     = "analytics_off"     // "1" = turned off
	kAck     = "analytics_ack"     // "1" = an administrator has seen the choice
	kSent    = "analytics_sent"    // unix time of the last report
	kVersion = "analytics_version" // version in the last report (install / upgrade)
	kCounts  = "analytics_counts"  // counters since the last report (JSON)
)

var (
	nameRe    = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	codeRe    = regexp.MustCompile(`^[a-z0-9_]{1,24}$`)
	versionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+[-+.a-z0-9]{0,16}$`)
	propRe    = regexp.MustCompile(`^[A-Za-z0-9 ._+-]{1,36}$`)
)

// UI counters the web UI may report (without the "ui_" prefix). Anything
// else in a report is dropped.
var uiKeys = set(
	"session", "layout_desktop", "layout_phone", "layout_embedded", "theme_light", "theme_dark", "theme_auto",
	"details", "tab_info", "tab_files", "tab_preview", "tab_peers", "tab_log",
	"sort_queue", "sort_status", "sort_progress", "sort_eta", "sort_elapsed",
	"set_dl", "set_sched", "set_users", "set_acct", "set_token", "set_notify", "set_import", "set_about",
	"add_paste", "add_text", "add_drop", "add_pick", "add_clip", "add_merge",
	"pick_mode", "bulk", "queue_move", "stream_on", "preview_play", "preview_subs", "subs_enc", "open_folder", "open_on_phone",
	"token_agent", "agent_copy_claude", "agent_copy_other", "skill_download",
)

// UI languages (lang_<code>)
var uiLangs = set("TCH", "SCH", "ENG", "JPN", "KOR", "GER", "FRE", "SPA", "ITA", "POR", "RUS", "DUT", "THA")

var schemes = set("http", "https", "ftp", "ftps", "sftp", "scp")

// Counter groups a report carries: task adds and results, the web UI, and
// the uses of tokens, the REST API, the V4 API and chat commands.
var groups = set("add", "res", "ui", "tok", "api", "v4", "chat")

func set(v ...string) map[string]bool {
	m := make(map[string]bool, len(v))
	for _, s := range v {
		m[s] = true
	}
	return m
}

// Service collects and sends the statistics of this installation.
type Service struct {
	m       *core.Manager
	db      *store.DB
	data    string
	version string
	arch    string
	dryRun  bool
	debug   bool
	// Post sends one request body; tests replace it.
	Post func(u string, body []byte) error

	on     atomic.Bool // the switch (kOff), cached: token requests count on every call
	mu     sync.Mutex
	counts map[string]int64
	dirty  bool
	uiLast map[string]time.Time
	stop   chan struct{}
}

// New loads the counters kept since the last report.
func New(m *core.Manager, db *store.DB, data, version, arch string) *Service {
	s := &Service{m: m, db: db, data: data, version: version, arch: arch, counts: map[string]int64{}, uiLast: map[string]time.Time{}, stop: make(chan struct{})}
	s.Post = s.post
	if _, err := os.Stat(filepath.Join(data, "analytics_dryrun")); err == nil {
		s.dryRun = true
	}
	if _, err := os.Stat(filepath.Join(data, "analytics_debug")); err == nil {
		s.debug = true
	}
	s.on.Store(db.Meta(kOff) != "1")
	if c := db.Meta(kCounts); c != "" {
		json.Unmarshal([]byte(c), &s.counts)
	}
	if db.Meta(kID) == "" {
		b := make([]byte, 16)
		rand.Read(b)
		db.SetMeta(kID, hex.EncodeToString(b))
	}
	return s
}

// Enabled reports the switch (on unless an administrator turned it off).
func (s *Service) Enabled() bool { return s.on.Load() }

// configured: there is somewhere to send to (or the dry run logs it).
func (s *Service) configured() bool { return s.dryRun || (MeasurementID != "" && APISecret != "") }

// SetEnabled records an administrator's choice; turning it off drops what
// was collected.
func (s *Service) SetEnabled(on bool) {
	s.db.SetMeta(kAck, "1")
	s.on.Store(on)
	if on {
		s.db.SetMeta(kOff, "")
		return
	}
	s.db.SetMeta(kOff, "1")
	s.mu.Lock()
	s.counts, s.dirty = map[string]int64{}, false
	s.mu.Unlock()
	s.db.SetMeta(kCounts, "")
}

// add counts key (already validated) while the statistics are on.
func (s *Service) add(key string, n int64) {
	if n <= 0 || len(key) > maxNameLen || !nameRe.MatchString(key) || !s.Enabled() {
		return
	}
	s.mu.Lock()
	s.counts[key] += n
	s.dirty = true
	s.mu.Unlock()
}

// hooked are the groups other packages count through api.Count.
var hooked = set("tok", "api", "v4", "chat")

// count is api.Counter: one use of a token, API endpoint or chat command.
func (s *Service) count(key string) {
	if g, _, _ := strings.Cut(key, "_"); hooked[g] {
		s.add(key, 1)
	}
}

// UI adds the counters one browser reported; user rate-limits reports.
func (s *Service) UI(user string, counts map[string]int64) {
	if !s.Enabled() {
		return
	}
	s.mu.Lock()
	if time.Since(s.uiLast[user]) < uiMinInterval {
		s.mu.Unlock()
		return
	}
	s.uiLast[user] = time.Now()
	s.mu.Unlock()
	for k, n := range counts {
		ok := uiKeys[k]
		if strings.HasPrefix(k, "lang_") {
			ok, k = uiLangs[k[5:]], strings.ToLower(k)
		}
		if !ok {
			continue
		}
		if n > maxUIPerPost {
			n = maxUIPerPost
		}
		s.add("ui_"+k, n)
	}
}

// watch counts task events.
func (s *Service) watch() {
	ch, cancel := s.m.Subscribe()
	defer cancel()
	for {
		select {
		case <-s.stop:
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			s.event(e)
		}
	}
}

func (s *Service) event(e core.Event) {
	if e.Task == nil {
		return
	}
	src, _ := e.Data["source"].(string)
	switch e.Type {
	case "task.added":
		switch src {
		case "magnet", "torrent", "import":
			s.add("add_"+src, 1)
		}
		t := s.m.Live(e.Task.ID)
		if t == nil {
			return
		}
		if src == "url" {
			sc, _, _ := strings.Cut(strings.ToLower(t.Source), "://")
			if schemes[sc] {
				s.add("add_"+sc, 1)
			} else {
				s.add("add_other", 1)
			}
			if h := t.Options.Hoster; h != "" {
				s.add(hosterKey(h), 1)
			}
		}
		if src != "import" {
			s.add("add_via_"+via(t.Caller), 1)
		}
	case "task.merged":
		s.add("add_merged", 1)
	case "task.completed":
		s.add("res_done", 1)
		if e.Task.Kind == "url" {
			s.add("res_done_url", 1)
		} else {
			s.add("res_done_bt", 1)
		}
		s.add("res_mb", e.Task.Size>>20)
	case "task.failed":
		s.add("res_failed", 1)
		if ed, ok := e.Data["error"].(map[string]any); ok {
			if c, _ := ed["code"].(string); codeRe.MatchString(c) {
				s.add("res_err_"+c, 1)
			}
		}
	case "task.seeding_finished":
		s.add("res_seeded", 1)
	}
}

// via names the entry point of a task without its free-text parts (token
// names, V4 caller parameters).
func via(caller string) string {
	switch {
	case caller == "" || caller == "Download Center":
		return "ui"
	case strings.HasPrefix(caller, "API: "):
		return "api"
	case caller == "Chat":
		return "chat"
	}
	return "v4"
}

// hosterKey names the counter of a task added through a file-hosting
// service: the services with accounts and Google Drive's public links by
// name, anything else as other.
func hosterKey(h string) string {
	if codeRe.MatchString(h) && (h == "gdrive" || contains(core.AccountKinds, h)) {
		return "add_hoster_" + h
	}
	return "add_hoster_other"
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// Run sends a report once a day and keeps the counters on disk.
func (s *Service) Run() {
	go s.watch()
	t := time.NewTimer(10 * time.Minute)
	for {
		select {
		case <-s.stop:
			t.Stop()
			s.save()
			return
		case <-t.C:
		}
		s.save()
		last, _ := strconv.ParseInt(s.db.Meta(kSent), 10, 64)
		if s.Enabled() && s.db.Meta(kAck) == "1" && s.configured() && time.Since(time.Unix(last, 0)) >= sendEvery {
			if err := s.Send(); err != nil {
				log.Printf("analytics: %v", err)
			}
		}
		t.Reset(30 * time.Minute)
	}
}

// Stop ends Run and saves the counters.
func (s *Service) Stop() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
}

func (s *Service) save() {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return
	}
	b, _ := json.Marshal(s.counts)
	s.dirty = false
	s.mu.Unlock()
	s.db.SetMeta(kCounts, string(b))
}

// --- the report ---

type event struct {
	Name   string         `json:"name"`
	Params map[string]any `json:"params"`
}

type payload struct {
	ClientID       string                    `json:"client_id"`
	UserProperties map[string]map[string]any `json:"user_properties,omitempty"`
	Events         []event                   `json:"events"`
}

// Send reports the counters since the last report and the current state.
func (s *Service) Send() error {
	s.mu.Lock()
	counts := make(map[string]int64, len(s.counts))
	for k, v := range s.counts {
		counts[k] = v
	}
	s.mu.Unlock()
	now := time.Now()
	reqs := s.build(counts, s.snapshot(), now)
	for _, p := range reqs {
		b, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if s.dryRun {
			log.Printf("analytics (dry run): %s", b)
			continue
		}
		u := endpoint
		if s.debug {
			u = debugEndpoint
		}
		if err := s.Post(u+"?measurement_id="+url.QueryEscape(MeasurementID)+"&api_secret="+url.QueryEscape(APISecret), b); err != nil {
			return err
		}
	}
	// Counted after the snapshot was taken stays for the next report
	s.mu.Lock()
	for k, v := range counts {
		if s.counts[k] -= v; s.counts[k] <= 0 {
			delete(s.counts, k)
		}
	}
	s.dirty = true
	s.mu.Unlock()
	s.save()
	s.db.SetMeta(kSent, strconv.FormatInt(now.Unix(), 10))
	s.db.SetMeta(kVersion, s.version)
	return nil
}

// build turns counters and state into requests within the protocol limits.
func (s *Service) build(counts map[string]int64, state map[string]int64, now time.Time) []payload {
	sid := now.Unix()
	base := func() map[string]any { return map[string]any{"engagement_time_msec": 1, "session_id": sid} }
	var evs []event
	evs = append(evs, event{Name: "heartbeat", Params: base()})
	prev := s.db.Meta(kVersion)
	switch {
	case prev == "":
		evs = append(evs, event{Name: "install", Params: base()})
	case prev != s.version && versionRe.MatchString(prev):
		p := base()
		p["from_version"] = prev
		evs = append(evs, event{Name: "upgrade", Params: p})
	}
	// One event per counter: GA reports break them down by two registered
	// dimensions (group, key) and one metric (n) instead of a custom
	// definition per counter.
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		g, _, _ := strings.Cut(k, "_")
		if groups[g] && counts[k] > 0 {
			evs = append(evs, counter("usage", g, k, counts[k], base))
		}
	}
	keys = keys[:0]
	for k := range state {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		evs = append(evs, counter("state", "st", k, state[k], base))
	}
	props := map[string]map[string]any{}
	prop := func(k, v string) {
		if len(k) <= maxPropName && propRe.MatchString(v) && len(v) <= maxPropValue {
			props[k] = map[string]any{"value": v}
		}
	}
	prop("app_version", s.version)
	prop("arch", s.arch)
	prop("qts_version", qts.Firmware())
	prop("nas_model", qts.GetCfg("System", "Model", "", ""))
	var out []payload
	for len(evs) > 0 {
		n := min(len(evs), maxEvents)
		out = append(out, payload{ClientID: s.db.Meta(kID), UserProperties: props, Events: evs[:n]})
		evs = evs[n:]
	}
	return out
}

// counter is one counted name and its value; the name is checked again so
// nothing but a fixed identifier can travel as a string.
func counter(name, group, key string, n int64, base func() map[string]any) event {
	p := base()
	if len(key) <= 100 && nameRe.MatchString(key) {
		p["group"], p["key"], p["n"] = group, key, n
	}
	return event{Name: name, Params: p}
}

// snapshot describes what is set up: numbers and switches only.
func (s *Service) snapshot() map[string]int64 {
	st := s.m.Settings()
	out := map[string]int64{}
	b := func(k string, v bool) {
		if v {
			out[k] = 1
		} else {
			out[k] = 0
		}
	}
	count := func(k, q string, args ...any) {
		var n int64
		if s.db.QueryRow(q, args...).Scan(&n) == nil {
			out[k] = n
		}
	}
	count("st_users", `SELECT COUNT(*) FROM users`)
	count("st_admins", `SELECT COUNT(*) FROM users WHERE role = 'admin'`)
	count("st_tasks", `SELECT COUNT(*) FROM tasks WHERE removed_at = 0`)
	count("st_tasks_bt", `SELECT COUNT(*) FROM tasks WHERE removed_at = 0 AND kind = ?`, core.KindBT)
	count("st_tokens", `SELECT COUNT(*) FROM tokens`)
	now := time.Now().Unix()
	count("st_tokens_1d", `SELECT COUNT(*) FROM tokens WHERE last_used_at >= ?`, now-86400)
	count("st_tokens_30d", `SELECT COUNT(*) FROM tokens WHERE last_used_at >= ?`, now-30*86400)
	count("st_token_owners", `SELECT COUNT(DISTINCT owner) FROM tokens`)
	// scopes is a JSON array of strings: the quoted name matches one scope only
	for _, sc := range auth.AllScopes {
		count("st_tok_"+strings.ReplaceAll(sc, ":", "_"), `SELECT COUNT(*) FROM tokens WHERE scopes LIKE ?`, "%\""+sc+"\"%")
	}
	count("st_channels", `SELECT COUNT(*) FROM channels`)
	count("st_chat_ops", `SELECT COUNT(*) FROM channels WHERE operate = 1`)
	for _, d := range notify.Services {
		if codeRe.MatchString(d.ID) {
			count("st_ch_"+d.ID, `SELECT COUNT(*) FROM channels WHERE service = ?`, d.ID)
		}
	}
	for _, k := range core.AccountKinds {
		if codeRe.MatchString(k) {
			count("st_acct_"+k, `SELECT COUNT(*) FROM accounts WHERE kind = ?`, k)
		}
	}
	b("st_schedule", st.Schedule.Enabled)
	lim := false
	for _, l := range []core.TypeLimits{st.HTTP, st.FTP, st.BT} {
		lim = lim || l.MaxDown > 0 || l.MaxUp > 0
	}
	b("st_speed_limit", lim)
	b("st_move_default", st.MoveDir != "")
	switch st.AutoRemove {
	case "completed":
		out["st_auto_remove"] = 1
	case "seeded":
		out["st_auto_remove"] = 2
	default:
		out["st_auto_remove"] = 0
	}
	out["st_proxies"] = int64(len(st.Proxy.Profiles))
	b("st_proxy_bt", st.Proxy.BT != "")
	b("st_proxy_users", st.Proxy.RequireForUsers)
	b("st_v4_takeover", st.V4Takeover)
	b("st_encrypt", st.Torrent.Encrypt)
	b("st_dht", st.Torrent.DHT)
	b("st_upnp", st.Torrent.UPnP)
	out["st_peer_mode"] = int64(st.Torrent.PeerMode)
	b("st_bt_engine", s.m.Engines["libtorrent"] != nil)
	if s.m.URL != nil {
		c := s.m.URL.Caps()
		b("st_url_ftp", c.FTP)
		b("st_url_sftp", c.SFTP)
		b("st_url_scp", c.SCP)
	}
	return out
}

// post sends a request through the proxy notifications use.
func (s *Service) post(u string, body []byte) error {
	proxy := ""
	if st := s.m.Settings(); st.Proxy.NotifyProfile != "" {
		proxy = s.m.ProxyURL(st.Proxy.NotifyProfile)
	}
	cl := netutil.Client(20*time.Second, false, proxy)
	req, err := http.NewRequest("POST", u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "DownloadCenter/"+s.version)
	resp, err := cl.Do(req)
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if s.debug {
		log.Printf("analytics (validation): %s", rb)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("send: HTTP %d", resp.StatusCode)
	}
	return nil
}
