// Package dl is the built-in URL engine: segmented HTTP/HTTPS downloads
// with Go's net/http, FTP/FTPS through the firmware's curl, resumable from a
// control file kept next to the data. The engine reference of a task is its
// task hash, so re-adding after a restart is idempotent.
package dl

import (
	"errors"
	"sync"

	"downloadcenter/internal/engine"
)

// Engine runs URL downloads: dcd keeps pieces, control files, connections
// per task and rate limits; dc-dl (libcurl) moves the bytes.
type Engine struct {
	// MaxConns is the number of connections per HTTP task (others use one).
	MaxConns int
	// Logf records a line in the task log; ref is the task hash.
	Logf func(ref, msg string)

	sc    *sidecar
	mu    sync.Mutex
	tasks map[string]*task
}

// New returns the engine; bin is the dc-dl executable, dataDir the data/
// directory of the package.
func New(bin, dataDir string) *Engine {
	return &Engine{MaxConns: 4, tasks: map[string]*task{}, sc: &sidecar{bin: bin, dataDir: dataDir}}
}

func (e *Engine) Name() string { return "builtin" }

// Caps follow the protocols dc-dl reported; without dc-dl nothing works.
func (e *Engine) Caps() engine.Caps {
	c := engine.Caps{HTTPProxy: true, Socks5URLs: true}
	v := e.sc.versions()
	if v == nil {
		return c
	}
	for _, p := range v.Protocols {
		switch p {
		case "https":
			c.URLs = true
		case "ftp":
			c.FTP = true
		case "sftp":
			c.SFTP = true
		case "scp":
			c.SCP = true
		}
	}
	return c
}

// Supports reports whether URLs of a scheme can be downloaded now.
func (e *Engine) Supports(scheme string) bool {
	c := e.Caps()
	switch scheme {
	case "http", "https":
		return c.URLs
	case "ftp", "ftps":
		return c.FTP
	case "sftp":
		return c.SFTP
	case "scp":
		return c.SCP
	}
	return false
}

// Version reports dc-dl and libcurl.
func (e *Engine) Version() string {
	if v := e.sc.versions(); v != nil {
		return "dc-dl " + v.DCDL + ", curl " + v.Curl
	}
	return ""
}

// Start starts dc-dl.
func (e *Engine) Start() error {
	return e.sc.ensure()
}

// Stop pauses every running task and saves its control file.
func (e *Engine) Stop() error {
	for _, t := range e.all() {
		t.stop(engine.Paused)
	}
	e.sc.stop()
	return nil
}

func (e *Engine) Health() error                     { return e.sc.ensure() }
func (e *Engine) ApplyGlobal(g engine.Global) error { return nil }
func (e *Engine) Restart(g engine.Global) error     { return nil }

func (e *Engine) all() []*task {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]*task, 0, len(e.tasks))
	for _, t := range e.tasks {
		out = append(out, t)
	}
	return out
}

func (e *Engine) get(ref string) *task {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.tasks[ref]
}

func (e *Engine) logf(ref, msg string) {
	if e.Logf != nil {
		e.Logf(ref, msg)
	}
}

// Add creates the task (or returns the existing one) and picks up the
// progress of an earlier run from the control file in r.Dir.
func (e *Engine) Add(hash string, r engine.AddRequest) (string, error) {
	if hash == "" {
		return "", errors.New("dl: task hash required")
	}
	if len(r.URIs) == 0 || r.Dir == "" {
		return "", errors.New("dl: URL and folder required")
	}
	e.mu.Lock()
	if _, ok := e.tasks[hash]; ok {
		e.mu.Unlock()
		return hash, nil
	}
	t := newTask(e, hash, r)
	e.tasks[hash] = t
	e.mu.Unlock()
	t.load()
	if !r.Paused {
		t.resume()
	}
	return hash, nil
}

// Forget drops a finished or failed task; its files stay.
func (e *Engine) Forget(ref string) {
	if t := e.get(ref); t != nil {
		t.stop(engine.Paused)
		e.mu.Lock()
		delete(e.tasks, ref)
		e.mu.Unlock()
	}
}

// Remove stops a task and returns once nothing writes to its files any more.
func (e *Engine) Remove(ref string) error {
	e.Forget(ref)
	return nil
}

func (e *Engine) Pause(ref string) error {
	if t := e.get(ref); t != nil {
		t.stop(engine.Paused)
	}
	return nil
}

func (e *Engine) Resume(ref string) error {
	if t := e.get(ref); t != nil {
		t.resume()
	}
	return nil
}

func (e *Engine) SetLimits(ref string, down, up int64) error {
	if t := e.get(ref); t != nil {
		t.lim.set(down)
	}
	return nil
}

// ReplaceURIs points a task at new mirrors (an expired file-hosting link);
// a running download reconnects and continues from the control file.
func (e *Engine) ReplaceURIs(ref string, uris []string) error {
	t := e.get(ref)
	if t == nil {
		return engine.ErrNotFound
	}
	if len(uris) == 0 {
		return errors.New("dl: no URL")
	}
	t.mu.Lock()
	t.uris = append([]string(nil), uris...)
	running := t.runDone != nil
	t.mu.Unlock()
	if running {
		t.stop(engine.Paused)
		t.resume()
	}
	return nil
}

func (e *Engine) SetFiles(ref string, sel []int, prio map[int]int) error {
	return engine.ErrUnsupported
}
func (e *Engine) SetSequential(ref string, on bool) error         { return engine.ErrUnsupported }
func (e *Engine) AddTrackers(ref string, trackers []string) error { return engine.ErrUnsupported }
func (e *Engine) SetSeeding(ref string, ratio float64, minutes int) error {
	return engine.ErrUnsupported
}

// StatusAll fails while dc-dl is down: the core then reports the engine
// down and calls Start until it is back.
func (e *Engine) StatusAll() (map[string]*engine.Status, error) {
	if err := e.sc.ensure(); err != nil {
		return nil, err
	}
	out := map[string]*engine.Status{}
	for _, t := range e.all() {
		out[t.ref] = t.status()
	}
	return out, nil
}

func (e *Engine) Status(ref string) (*engine.Status, error) {
	t := e.get(ref)
	if t == nil {
		return nil, engine.ErrNotFound
	}
	return t.status(), nil
}

func (e *Engine) Peers(ref string) ([]engine.Peer, error) { return nil, nil }
func (e *Engine) Trackers(ref string) ([]string, error)   { return nil, nil }

// SaveState writes the control file of every task.
func (e *Engine) SaveState() error {
	for _, t := range e.all() {
		t.saveControl()
	}
	return nil
}
