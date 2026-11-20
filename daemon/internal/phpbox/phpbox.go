// Package phpbox runs search plugins written for other download managers
// (search modules packed as .dlm, add-ons packed as .addon). They are PHP
// programs: phpbox runs them with QTS's built-in PHP inside a sandbox (own
// network, mount, PID, IPC and UTS namespaces, a minimal read-only root, an
// unused uid without groups, resource limits) whose only way out is the
// relay, an HTTP proxy in dcd that applies netutil's strict rules. Without
// a working sandbox nothing runs: there is no other way to run a plugin.
//
// The PHP that implements the plugins' interfaces (shim/) is Download
// Center's own.
package phpbox

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

//go:embed shim/*.php
var shims embed.FS

// PHPPath is QTS's built-in PHP, the only PHP plugins run with.
const PHPPath = "/usr/local/apache/bin/php"

// phpPath is PHPPath (tests point it elsewhere).
var phpPath = PHPPath

// Availability codes and their reasons (fixed English sentences).
const (
	CodeUnchecked   = "unchecked"
	CodePHPMissing  = "php_missing"
	CodePHPBroken   = "php_broken"
	CodeNoIsolation = "no_isolation"
	CodeCheckFailed = "check_failed"

	ReasonUnchecked   = "The NAS has not been checked yet"
	ReasonPHPMissing  = "QTS's built-in PHP was not found at /usr/local/apache/bin/php"
	ReasonPHPNoStart  = "QTS's built-in PHP at /usr/local/apache/bin/php does not start"
	ReasonPHPNoCurl   = "QTS's built-in PHP lacks the curl extension"
	ReasonNoIsolation = "This system cannot isolate programs from the NAS"
	ReasonCheckFailed = "The isolation self-check did not pass"
)

// Limits of a run.
const (
	wallTime    = 30 * time.Second // whole run, killed after it
	maxRunning  = 2                // runs at the same time
	positiveFor = 10 * time.Minute // a passed check is trusted this long
	maxAnswer   = 1 << 20          // bytes of the shim's answer
	maxStdout   = 64 << 10         // bytes of output kept (the rest is read and dropped)
	maxResults  = 200              // results taken from an answer
	boxUID      = 59999            // the uid and gid of the first slot, 59998 the second's
	memoryLimit = 768 << 20        // address space of the plugin's process
)

// minUID is the lowest uid a plugin may run as: 59990 to 59999 belong to
// no NAS account.
const minUID = boxUID - 9

// slotUID is the uid and gid a plugin runs as in a slot (0 or 1) by
// default: two runs at the same time do not share a uid, so they share no
// process limit or keyring.
func slotUID(slot int) uint32 { return uint32(boxUID - slot) }

// uidOf is the uid of a slot of this Runtime (see Config.UIDBase).
func (r *Runtime) uidOf(slot int) uint32 {
	if b := r.cfg.UIDBase; b >= minUID+maxRunning-1 && b <= boxUID {
		return b - uint32(slot)
	}
	return slotUID(slot)
}

// Errors of a run.
var (
	ErrTimeout = errors.New("The plugin did not finish in time")
	ErrFailed  = errors.New("The plugin failed")
	ErrOutput  = errors.New("The plugin's answer is too large or cannot be read")
)

// Unavailable is the error of a run on a NAS that cannot run plugins.
type Unavailable struct{ Code, Reason string }

func (e *Unavailable) Error() string { return e.Reason }

// Config is what a Runtime needs from dcd.
type Config struct {
	// Dir is a directory of dcd's own (data/phpbox); the box's root is
	// mounted on an empty directory in it, inside the box's own mount
	// namespace, so nothing shows outside.
	Dir string
	// Version goes into the User-Agent of the requests the shims make.
	Version string
	// Proxy is the upstream proxy for a target ("http://host:port"), ""
	// for direct: core.Manager.ProxyFor("", target, false), a regular
	// user's rule whoever runs the plugin. Errors mean no connection.
	Proxy func(target string) (string, error)
	// UIDBase is the uid of the first slot (0: 59999); the second slot's is
	// one less. Both must be in the range no NAS account has (59990 to
	// 59999), else the default applies. Each uid may have one process
	// only, so two Runtimes in one system need their own (tests do).
	UIDBase uint32
}

// Plugin is an installed plugin: its format, the directory with its files
// (see Package.Extract), the PHP file to load and its class.
type Plugin struct {
	Format  string
	Dir     string
	Entry   string
	Class   string
	Account bool // the plugin takes an account
}

// Account is a user name and password for a plugin.
type Account struct{ Username, Password string }

// Query is a search.
type Query struct {
	Keyword  string
	Category string // all, video, audio, software, books, other
	Limit    int
	Account  *Account
}

// Result is one result as the plugin gave it. Nothing in it is checked
// beyond types and lengths: it comes from a third-party program.
type Result struct {
	Title    string
	Download string // magnet link or .torrent address
	Page     string
	Hash     string
	Category string
	Date     string
	Size     int64 // -1 unknown
	Seeds    int64 // -1 unknown
	Leechs   int64 // -1 unknown
}

// Output is what a run gave besides its results. Error and Log come from
// the plugin and PHP: untrusted text that may contain the keyword or the
// account; never log them as they are.
type Output struct {
	Results  []Result
	Verified *bool
	Error    string
	Log      string
	Relay    RelayStats
	Duration time.Duration
}

// Runtime runs plugins. Availability is decided by a real run of the
// self-check in the sandbox: until one passes, nothing else runs.
type Runtime struct {
	cfg   Config
	relay *relay
	slots chan int // the free slots: maxRunning runs at a time
	ca    caCache  // the merged CA file
	now   func() time.Time
	wall  time.Duration

	checkMu sync.Mutex // one check at a time
	mu      sync.Mutex
	ok      bool
	code    string
	reason  string
	at      time.Time // of the last passed check
	php     string    // the PHP binary that check ran

	// Helper starts and plugin runs: tests count them to see that nothing
	// ran
	starts  atomic.Int32
	plugins atomic.Int32
	// Added to the uid the self-check expects (tests make it fail)
	skewUID int
}

// New makes a Runtime; nothing runs before Check.
func New(cfg Config) *Runtime {
	slots := make(chan int, maxRunning)
	for i := 0; i < maxRunning; i++ {
		slots <- i
	}
	return &Runtime{cfg: cfg, relay: newRelay(cfg.Proxy), slots: slots, now: time.Now,
		wall: wallTime, code: CodeUnchecked, reason: ReasonUnchecked}
}

// Available is the result of the last check, without checking: false with
// CodeUnchecked before the first one.
func (r *Runtime) Available() (ok bool, code, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ok, r.code, r.reason
}

// Check finds out whether plugins can run, running the self-check in the
// sandbox: always after a failed check (an administrator may have turned
// QTS's web server on since), and after a passed one once it is older
// than 10 minutes or PHP's files changed. dcd calls it at start and when
// a plugin is inspected or installed; runs call it as needed.
func (r *Runtime) Check(ctx context.Context) (ok bool, code, reason string) {
	r.checkMu.Lock()
	defer r.checkMu.Unlock()
	lay, code, reason := discover()
	r.mu.Lock()
	fresh := r.ok && lay != nil && lay.php == r.php && r.now().Sub(r.at) < positiveFor
	r.mu.Unlock()
	if fresh {
		return true, "", ""
	}
	if lay != nil {
		code, reason = r.probe(ctx, lay)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ok, r.code, r.reason = code == "", code, reason
	if r.ok {
		r.at, r.php = r.now(), lay.php
	}
	return r.ok, r.code, r.reason
}

// ready checks before a run; the layout to run with, or why not.
func (r *Runtime) ready(ctx context.Context) (*layout, error) {
	if ok, code, reason := r.Check(ctx); !ok {
		return nil, &Unavailable{Code: code, Reason: reason}
	}
	lay, code, reason := discover()
	if lay == nil {
		return nil, &Unavailable{Code: code, Reason: reason}
	}
	r.mu.Lock()
	same := lay.php == r.php
	r.mu.Unlock()
	if !same {
		return nil, &Unavailable{Code: CodeCheckFailed, Reason: ReasonCheckFailed}
	}
	return lay, nil
}

// request is what a shim reads on stdin.
type request struct {
	Mode      string   `json:"mode"`
	Entry     string   `json:"entry,omitempty"`
	Class     string   `json:"class,omitempty"`
	Account   bool     `json:"account,omitempty"`
	Keyword   string   `json:"keyword,omitempty"`
	Category  string   `json:"category,omitempty"`
	Limit     int      `json:"limit,omitempty"`
	Username  string   `json:"username,omitempty"`
	Password  string   `json:"password,omitempty"`
	UserAgent string   `json:"user_agent"`
	CAPath    string   `json:"capath,omitempty"`
	CAInfo    string   `json:"cainfo,omitempty"` // the merged CA file in the box
	Paths     []string `json:"paths,omitempty"`  // probe: paths that must not exist
	Direct    string   `json:"direct,omitempty"` // probe: an address to try without the relay
}

// answer is what a shim writes to fd 3. Types are strict: a result with a
// field of another type is dropped.
type answer struct {
	OK       bool              `json:"ok"`
	Results  []json.RawMessage `json:"results"`
	Verified *bool             `json:"verified"`
	Error    string            `json:"error"`
	Probe    json.RawMessage   `json:"probe"`
}

type rawResult struct {
	Title    string      `json:"title"`
	Download string      `json:"download"`
	Size     json.Number `json:"size"`
	Date     string      `json:"date"`
	Page     string      `json:"page"`
	Hash     string      `json:"hash"`
	Seeds    json.Number `json:"seeds"`
	Leechs   json.Number `json:"leechs"`
	Category string      `json:"category"`
}

func cut(s string, max int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	if len(s) > max {
		s = s[:max]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

func count(n json.Number) int64 {
	f, err := n.Float64()
	if err != nil || f < 0 || f > 1<<62 {
		return -1
	}
	return int64(f)
}

// parseAnswer reads the shim's answer.
func parseAnswer(b []byte) (*answer, []Result, error) {
	var a answer
	d := json.NewDecoder(bytes.NewReader(b))
	if err := d.Decode(&a); err != nil {
		return nil, nil, ErrOutput
	}
	var out []Result
	for _, raw := range a.Results {
		if len(out) == maxResults {
			break
		}
		var r rawResult
		rd := json.NewDecoder(bytes.NewReader(raw))
		rd.UseNumber()
		if rd.Decode(&r) != nil {
			continue
		}
		out = append(out, Result{Title: cut(r.Title, 1000), Download: cut(r.Download, 4096), Page: cut(r.Page, 2048),
			Hash: cut(r.Hash, 100), Category: cut(r.Category, 100), Date: cut(r.Date, 100),
			Size: count(r.Size), Seeds: count(r.Seeds), Leechs: count(r.Leechs)})
	}
	a.Error = cut(a.Error, 1000)
	return &a, out, nil
}

// runOut is what the sandbox gave back.
type runOut struct {
	uid       uint32 // the slot's uid PHP ran as
	cancelled bool   // the caller gave up before the run started
	answer    []byte
	overflow  bool
	log       string
	timedOut  bool
	relay     RelayStats
	stage     string // how far the box got: start, setup, exec, done
	err       error  // the box could not be made
}

// Search runs a plugin's search.
func (r *Runtime) Search(ctx context.Context, p Plugin, q Query) (*Output, error) {
	req := request{Mode: "search", Keyword: q.Keyword, Category: q.Category, Limit: q.Limit}
	if req.Limit <= 0 || req.Limit > 100 {
		req.Limit = 100
	}
	if q.Account != nil {
		req.Username, req.Password = q.Account.Username, q.Account.Password
	}
	return r.runPlugin(ctx, p, req)
}

// Verify asks a plugin whether an account works.
func (r *Runtime) Verify(ctx context.Context, p Plugin, a Account) (bool, *Output, error) {
	out, err := r.runPlugin(ctx, p, request{Mode: "verify", Username: a.Username, Password: a.Password})
	if err != nil {
		return false, out, err
	}
	if out.Verified == nil {
		return false, out, ErrFailed
	}
	return *out.Verified, out, nil
}

func (r *Runtime) runPlugin(ctx context.Context, p Plugin, req request) (*Output, error) {
	var shim string
	switch p.Format {
	case FormatDLM:
		shim = "dlm.php"
	case FormatAddon:
		shim = "addon.php"
	default:
		return nil, ErrFailed
	}
	if _, ok := cleanName(p.Entry); !ok || !identRe.MatchString(p.Class) {
		return nil, ErrFailed
	}
	lay, err := r.ready(ctx)
	if err != nil {
		return nil, err
	}
	req.Entry, req.Class, req.Account = p.Entry, p.Class, p.Account
	r.plugins.Add(1)
	start := r.now()
	ro := r.run(ctx, lay, p.Dir, shim, false, req)
	out := &Output{Log: ro.log, Relay: ro.relay, Duration: r.now().Sub(start)}
	switch {
	case ro.cancelled:
		return out, ctx.Err()
	case ro.err != nil:
		// The box itself failed: check again before the next run
		r.mu.Lock()
		r.ok, r.code, r.reason = false, CodeCheckFailed, ReasonCheckFailed
		r.mu.Unlock()
		return out, &Unavailable{Code: CodeCheckFailed, Reason: ReasonCheckFailed}
	case ro.timedOut:
		return out, ErrTimeout
	case ro.overflow:
		return out, ErrOutput
	case len(ro.answer) == 0:
		return out, ErrFailed
	}
	a, results, err := parseAnswer(ro.answer)
	if err != nil {
		return out, err
	}
	out.Results, out.Verified, out.Error = results, a.Verified, a.Error
	if !a.OK {
		return out, ErrFailed
	}
	return out, nil
}

// userAgent is what the shims send.
func (r *Runtime) userAgent() string {
	v := r.cfg.Version
	if v == "" {
		v = "dev"
	}
	return "DownloadCenter/" + v + " (+https://github.com/tasict/DownloadCenter)"
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
