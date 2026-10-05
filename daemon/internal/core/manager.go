// Package core is the task manager: it owns the task table, talks to the
// engines through the engine layer, and implements what the engines lack —
// per-type queues and speed limits, the 7x24 schedule, timed pauses,
// temporary folders, move-on-complete, ownership, auto-removal and events.
package core

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"downloadcenter/internal/engine"
	"downloadcenter/internal/qts"
	"downloadcenter/internal/store"
	"downloadcenter/internal/torrent"
)

// Resolver turns file-hosting share links into direct URLs.
type Resolver interface {
	Match(rawURL string) (service string, ok bool)
	Resolve(owner, accountID, rawURL, proxy string) (*Resolved, error)
}

// Resolved is a direct download for the URL engine.
type Resolved struct {
	URL       string
	Name      string
	Size      int64
	Headers   []string
	ExpiresAt int64
	Account   string
}

// Manager is the task manager.
type Manager struct {
	db      *store.DB
	DataDir string

	smu      sync.RWMutex
	settings Settings

	URL     engine.Engine            // URL engine (built-in)
	Engines map[string]engine.Engine // torrent engines by name ("libtorrent")

	Hosters Resolver

	mu         sync.Mutex
	live       map[string]*Task
	applied    map[string][2]int64
	running    map[string]bool // engine reports the task as active
	lastSave   map[string]int64
	dirty      map[string]bool
	moving     map[string]bool
	down       map[string]bool // engines currently unreachable
	restarting map[string]bool // engines restarted on purpose (no engine.down event)
	resolving  map[string]bool // file-hosting links being resolved
	health     sync.Map        // engine name -> down (bool), readable without mu
	inMinute   atomic.Bool     // housekeeping pass running
	busy       bool            // something is downloading (for queue.idle)
	schedMode  string          // schedule mode at the last tick (for schedule.changed)
	lastTick   time.Time
	lastMin    time.Time
	lastDay    time.Time
	kick       chan struct{}
	bus        bus
	stopCh     chan struct{}
	wg         sync.WaitGroup
}

// New creates the manager; Start loads tasks and starts the loop.
func New(db *store.DB, dataDir string) *Manager {
	m := &Manager{
		db: db, DataDir: dataDir,
		Engines:    map[string]engine.Engine{},
		live:       map[string]*Task{},
		applied:    map[string][2]int64{},
		running:    map[string]bool{},
		lastSave:   map[string]int64{},
		dirty:      map[string]bool{},
		moving:     map[string]bool{},
		down:       map[string]bool{},
		restarting: map[string]bool{},
		resolving:  map[string]bool{},
		kick:       make(chan struct{}, 1),
		stopCh:     make(chan struct{}),
	}
	m.loadSettings()
	os.MkdirAll(m.TorrentDir(), 0700)
	return m
}

// DB exposes the database to the API layers.
func (m *Manager) DB() *store.DB { return m.db }

// TorrentDir holds the .torrent of every torrent task (<hash>.torrent).
func (m *Manager) TorrentDir() string { return filepath.Join(m.DataDir, "torrents") }

func (m *Manager) torrentPath(hash string) string {
	return filepath.Join(m.TorrentDir(), hash+".torrent")
}

// Start loads the task table and starts the loop.
func (m *Manager) Start() error {
	ts, err := m.loadTasks(`removed_at = 0`)
	if err != nil {
		return err
	}
	var resume []*Task
	parts := map[string]string{}
	m.mu.Lock()
	for _, t := range ts {
		if adoptOfficialTemp(t) {
			m.saveTask(t)
		}
		if t.State == StMoving && !t.staged() {
			// Interrupted move: resume it (the engine result is gone,
			// re-adding would download everything again). A torrent's
			// move runs in the engine and is followed by the loop.
			resume = append(resume, t)
		}
		if t.Kind == KindBT && t.State == StDone {
			parts[t.Hash] = t.SaveDir()
		}
		m.live[t.Hash] = t
	}
	for _, t := range resume {
		m.resumeMove(t)
	}
	m.mu.Unlock()
	// Versions up to 1.0.2 left the part files of finished torrents behind;
	// checked at every start
	m.wg.Add(2)
	go func() {
		defer m.wg.Done()
		for h, dir := range parts {
			m.removePartFiles(h, dir)
		}
	}()
	go m.loop()
	return nil
}

// adoptOfficialTemp stages a torrent that 0.9.x imported into the official
// temporary folder: it leaves that folder once complete, like every other
// download. The temporary location becomes the root of that share.
func adoptOfficialTemp(t *Task) bool {
	if t.Kind != KindBT || !t.Options.Official || t.WorkDir != "" || t.DataPath != "" || t.State == StMoving || t.State == StDone {
		return false
	}
	i := strings.Index(t.TempDir, "/"+OfficialTempName+"/")
	if i <= 0 {
		return false
	}
	t.WorkDir, t.TempDir = t.TempDir, t.TempDir[:i]
	return true
}

// resumeMove continues a move interrupted by a restart. Caller holds m.mu.
func (m *Manager) resumeMove(t *Task) {
	src, dst, urlTask := t.Options.MoveSrc, t.Options.MoveDst, t.Kind != KindBT
	if src == "" || dst == "" {
		// Moves started by older versions: fall back to the engine
		t.State = StDownloading
		return
	}
	if _, err := os.Stat(src); err == nil {
		if urlTask {
			if ents, _ := os.ReadDir(src); len(ents) > 0 {
				m.startMove(t, src, dst, true)
				return
			}
		} else {
			m.startMove(t, src, dst, false)
			return
		}
	}
	// The source is gone: the move itself finished before the restart
	name := t.Options.OutName
	if name == "" {
		name = t.Name
	}
	if p := filepath.Join(dst, name); name != "" {
		if _, err := os.Stat(p); err == nil {
			t.DataPath = p
			if urlTask {
				removeTempDir(src)
			}
			m.finish(t)
			return
		}
	}
	m.fail(t, "move", "Download Center restarted while moving files; the files being moved cannot be found")
}

// Stop ends the loop and saves every task.
func (m *Manager) Stop() {
	close(m.stopCh)
	m.wg.Wait()
	m.mu.Lock()
	for _, t := range m.live {
		m.saveTask(t)
	}
	m.mu.Unlock()
}

// Kick runs the loop now.
func (m *Manager) Kick() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

func (m *Manager) loop() {
	defer m.wg.Done()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	m.tick()
	for {
		select {
		case <-m.stopCh:
			return
		case <-t.C:
		case <-m.kick:
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("core: tick panic: %v", r)
				}
			}()
			m.tick()
		}()
	}
}

// engineOf returns the engine a task runs in.
func (m *Manager) engineOf(t *Task) engine.Engine {
	if t.Kind != KindBT {
		return m.URL
	}
	if e := m.Engines[t.Engine]; e != nil {
		return e
	}
	// Tasks of an engine that is gone (aria2 before 0.9.1) move to libtorrent
	return m.BTEngine()
}

// BTEngine is the engine torrents go to (nil when dc-bt is missing).
func (m *Manager) BTEngine() engine.Engine {
	if e := m.Engines["libtorrent"]; e != nil {
		return e
	}
	return nil
}

func (m *Manager) tick() {
	now := time.Now()
	elapsed := int64(1)
	if !m.lastTick.IsZero() {
		elapsed = int64(now.Sub(m.lastTick).Seconds() + 0.5)
		if elapsed < 0 || elapsed > 30 {
			elapsed = 1
		}
	}
	m.lastTick = now

	// Poll every engine once
	statuses := map[engine.Engine]map[string]*engine.Status{}
	seen := map[engine.Engine]bool{}
	for _, e := range append([]engine.Engine{m.URL}, m.engineList()...) {
		if e == nil || seen[e] {
			continue
		}
		seen[e] = true
		st, err := e.StatusAll()
		if err != nil {
			m.engineDown(e, err)
			continue
		}
		m.engineUp(e)
		statuses[e] = st
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.live {
		if t.State == StDone || m.moving[t.Hash] {
			continue
		}
		e := m.engineOf(t)
		sts, ok := statuses[e]
		if !ok {
			continue // engine down
		}
		if t.State == StError && t.EngineRef == "" {
			continue
		}
		if t.Options.RetryAt > now.Unix() && t.EngineRef == "" {
			continue
		}
		st := sts[t.EngineRef]
		if st == nil || st.State == engine.Removed {
			if t.State == StError {
				continue
			}
			m.addToEngine(t, e)
			continue
		}
		m.applyStatus(t, st, e, elapsed)
	}
	m.schedule(now)
	nowU := now.Unix()
	for h, t := range m.live {
		if m.dirty[h] || nowU-m.lastSave[h] >= 15 && (t.State == StDownloading || t.State == StSeeding || t.State == StChecking || t.State == StMetadata) {
			m.saveTask(t)
			m.lastSave[h] = nowU
			delete(m.dirty, h)
		}
	}
	if now.Sub(m.lastMin) >= time.Minute {
		m.lastMin = now
		if m.inMinute.CompareAndSwap(false, true) {
			go func() {
				defer m.inMinute.Store(false)
				m.minute()
			}()
		}
	}
}

func (m *Manager) engineList() []engine.Engine {
	names := make([]string, 0, len(m.Engines))
	for n := range m.Engines {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []engine.Engine
	for _, n := range names {
		out = append(out, m.Engines[n])
	}
	return out
}

func (m *Manager) engineDown(e engine.Engine, err error) {
	m.mu.Lock()
	was := m.down[e.Name()]
	m.down[e.Name()] = true
	m.health.Store(e.Name(), true)
	quiet := m.restarting[e.Name()]
	m.mu.Unlock()
	if quiet {
		return
	}
	if !was {
		log.Printf("core: engine %s is down: %v", e.Name(), err)
		m.Emit(Event{Type: "engine.down", Data: map[string]any{"engine": e.Name(), "error": err.Error()}})
	}
	// Try to bring it back (the adapter reuses or restarts the process)
	if serr := e.Start(); serr == nil {
		log.Printf("core: engine %s restarted", e.Name())
	}
}

func (m *Manager) engineUp(e engine.Engine) {
	m.mu.Lock()
	was := m.down[e.Name()]
	delete(m.down, e.Name())
	m.health.Store(e.Name(), false)
	m.mu.Unlock()
	if was {
		m.Emit(Event{Type: "engine.up", Data: map[string]any{"engine": e.Name()}})
	}
}

// EngineHealth reports the state of every engine.
func (m *Manager) EngineHealth() map[string]string {
	// Read without m.mu: health checks must answer even when an engine
	// call holds the manager lock
	out := map[string]string{}
	for _, e := range append([]engine.Engine{m.URL}, m.engineList()...) {
		if e == nil {
			continue
		}
		if v, ok := m.health.Load(e.Name()); ok && v.(bool) {
			out[e.Name()] = "down"
		} else {
			out[e.Name()] = "up"
		}
	}
	return out
}

func (m *Manager) markDirty(t *Task) { m.dirty[t.Hash] = true }

// addToEngine (re)creates the engine task of t, paused; schedule() decides
// whether it runs. Caller holds m.mu.
func (m *Manager) addToEngine(t *Task, e engine.Engine) {
	if e == nil {
		return
	}
	req, err := m.buildAdd(t)
	if err == errResolving {
		return
	}
	if err != nil {
		m.fail(t, "add", err.Error())
		return
	}
	req.Paused = true
	if fr, ok := e.(interface{ SetFastResume(string, string) }); ok && t.Options.ResumeFile != "" && req.Check {
		// Imported official task: let libtorrent try its 1.2 resume data
		fr.SetFastResume(t.Hash, t.Options.ResumeFile)
	}
	ref, err := e.Add(t.Hash, req)
	if err != nil && strings.Contains(err.Error(), "not unique") {
		// The deterministic gid is still known to the engine: reuse it
		if st, serr := e.Status(engineGID(e, t.Hash)); serr == nil {
			ref, err = st.Ref, nil
		}
	}
	if err != nil {
		m.fail(t, "add", err.Error())
		return
	}
	t.EngineRef = ref
	t.Engine = e.Name()
	t.UpBase = t.UpTotal
	t.Options.Check = false
	delete(m.applied, t.Hash)
	delete(m.running, t.Hash)
	m.markDirty(t)
}

type gidder interface{ GIDFor(hash string) string }

func engineGID(e engine.Engine, hash string) string {
	if g, ok := e.(gidder); ok {
		return g.GIDFor(hash)
	}
	return hash
}

// buildAdd prepares the engine request for t.
func (m *Manager) buildAdd(t *Task) (engine.AddRequest, error) {
	s := m.Settings()
	req := engine.AddRequest{MaxDown: t.Options.MaxDown, MaxUp: t.Options.MaxUp}
	if t.Kind == KindBT {
		req.Dir = t.SaveDir()
		if t.InTemp() {
			if err := os.MkdirAll(req.Dir, 0775); err != nil {
				return req, fmt.Errorf("Cannot create temporary folder: %v", err)
			}
		}
		req.Root = t.Options.Root
		if b, err := os.ReadFile(m.torrentPath(t.Hash)); err == nil {
			req.Torrent = b
		} else if t.IsMagnet() {
			req.Magnet = t.Source
		} else if t.Options.Magnet != "" {
			req.Magnet = t.Options.Magnet
		} else {
			return req, errors.New("The .torrent file of this task cannot be found")
		}
		req.Select = t.Options.Select
		req.Trackers = t.Options.Trackers
		req.SeedRatio, req.SeedTime = s.Torrent.SeedRatio, s.Torrent.SeedTime
		req.MaxPeers = s.Torrent.TorrentMaxConn
		if s.Torrent.TorrentMaxUp > 0 && (req.MaxUp == 0 || req.MaxUp > int64(s.Torrent.TorrentMaxUp)*1024) {
			req.MaxUp = int64(s.Torrent.TorrentMaxUp) * 1024
		}
		req.Sequential = t.Options.Sequential
		// A forced check only when asked for: without resume data
		// libtorrent checks the files on disk by itself, and with it a check
		// would throw the resume data away
		req.Check = t.Options.Check
		return req, nil
	}
	if t.WorkDir == "" {
		t.WorkDir = m.workDirFor(t.TempDir, t.Hash)
	}
	if err := os.MkdirAll(t.WorkDir, 0775); err != nil {
		return req, fmt.Errorf("Cannot create temporary folder: %v", err)
	}
	req.Dir = t.WorkDir
	req.Out = t.Options.OutName
	req.Headers = t.Options.Headers
	req.URIs = []string{t.Source}
	if t.Options.Hoster != "" && m.Hosters != nil {
		if t.Options.Direct == "" || (t.Options.ExpiresAt > 0 && t.Options.ExpiresAt < time.Now().Unix()+120) {
			m.resolveAsync(t)
			return req, errResolving
		}
		req.URIs = []string{t.Options.Direct}
		req.Headers = append(append([]string{}, t.Options.Headers...), t.Options.DirectHeaders...)
	} else if user, pass, ok := m.siteCredentials(t); ok {
		req.User, req.Pass = user, pass
	}
	var err error
	if req.Proxy, err = m.proxyFor(t); err != nil {
		return req, err
	}
	t.ProxyUsed = req.Proxy
	// Regular users cannot reach the NAS itself through a download
	req.Guard = !m.isAdminOwner(t.Owner)
	return req, nil
}

// Proxy choice errors: the task must not run rather than go direct.
var (
	ErrProxyGone       = errors.New("The proxy this task uses was deleted; choose another proxy in the task details")
	ErrProxyNotAllowed = errors.New("This proxy cannot be used")
	ErrProxyRequired   = errors.New("The administrator requires regular users to use a proxy, and none is available for this task")
)

// ProxyFor resolves the proxy of a URL. choice is "" or "auto" (a profile
// whose sites match the host, else the URL default), "none" or a profile
// id. A nil profile means a direct connection. The no-proxy list of the
// chosen profile sends listed hosts direct.
func (m *Manager) ProxyFor(choice, rawURL string, admin bool) (*ProxyProfile, error) {
	px := m.Settings().Proxy
	var p *ProxyProfile
	switch choice {
	case "", "auto":
		for i := range px.Profiles {
			if px.Profiles[i].Sites != "" && hostInList(rawURL, px.Profiles[i].Sites) {
				p = &px.Profiles[i]
				break
			}
		}
		if p == nil {
			p = px.Profile(px.URLDefault)
		}
	case "none":
	default:
		if p = px.Profile(choice); p == nil {
			return nil, ErrProxyGone
		}
		if !admin && !p.ForUsers {
			return nil, ErrProxyNotAllowed
		}
	}
	if p != nil && hostInList(rawURL, p.NoProxy) {
		return nil, nil
	}
	if p == nil && !admin && px.RequireForUsers {
		return nil, ErrProxyRequired
	}
	return p, nil
}

// TaskProxyName is the name of the profile a URL task connects through
// ("" = direct), or why it cannot run.
func (m *Manager) TaskProxyName(t *Task) (name, problem string) {
	link := t.Source
	if t.Options.OrigURL != "" {
		link = t.Options.OrigURL
	}
	p, err := m.ProxyFor(t.Options.Proxy, link, m.isAdminOwner(t.Owner))
	if err != nil {
		return "", err.Error()
	}
	if p != nil {
		return p.Name, ""
	}
	return "", ""
}

// SetProxy changes the proxy choice of a URL task ("auto", "none" or a
// profile id). The choice is checked against the owner's role. A running
// download reconnects through the new proxy and continues from its control
// file; a file-hosting link is resolved again through it.
func (m *Manager) SetProxy(hash, choice string) error {
	if choice == "auto" {
		choice = ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.live[hash]
	if !ok {
		return ErrNotFound
	}
	if t.Kind == KindBT {
		return ErrUnsupported
	}
	link := t.Source
	if t.Options.OrigURL != "" {
		link = t.Options.OrigURL
	}
	if _, err := m.ProxyFor(choice, link, m.isAdminOwner(t.Owner)); err != nil {
		return err
	}
	// Choosing again restarts a task that stopped over its proxy
	if t.Options.Proxy == choice && t.State != StError {
		return nil
	}
	t.Options.Proxy = choice
	if t.Options.Hoster != "" {
		t.Options.Direct = ""
	}
	if t.State != StDone && !m.moving[hash] {
		if e := m.engineOf(t); e != nil && t.EngineRef != "" {
			e.Remove(t.EngineRef)
		}
		t.EngineRef = ""
		delete(m.running, hash)
		delete(m.applied, hash)
		if t.State == StError {
			t.State, t.ErrorCode, t.ErrorMsg = StQueued, "", ""
			t.Options.AutoRetries, t.Options.RetryAt = 0, 0
		}
	}
	m.markDirty(t)
	m.Log(hash, "Proxy setting changed")
	go m.Kick()
	return nil
}

// refreshProxies takes URL downloads whose proxy changed with the settings
// out of the engine: the next tick adds them again through the new proxy,
// or stops them when they must not run (their profile was deleted, direct
// connections are not allowed). Nothing keeps going through a stale proxy.
func (m *Manager) refreshProxies() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for h, t := range m.live {
		if t.Kind == KindBT || t.EngineRef == "" || t.State == StDone || m.moving[h] {
			continue
		}
		if u, err := m.proxyFor(t); err == nil && u == t.ProxyUsed {
			continue
		}
		if e := m.engineOf(t); e != nil {
			e.Remove(t.EngineRef)
		}
		t.EngineRef = ""
		t.DownRate, t.UpRate = 0, 0
		delete(m.running, h)
		delete(m.applied, h)
	}
}

// proxyFor returns the proxy URL a URL task uses ("" = direct). Torrents
// get theirs through the engine's global options.
func (m *Manager) proxyFor(t *Task) (string, error) {
	link := t.Source
	if t.Options.OrigURL != "" {
		link = t.Options.OrigURL
	}
	p, err := m.ProxyFor(t.Options.Proxy, link, m.isAdminOwner(t.Owner))
	if err != nil {
		return "", err
	}
	return m.profileURL(p), nil
}

// hostInList reports whether the host of rawURL is in a list of host names
// ("example.com" also covers its subdomains, ".example.com" only them) and,
// for literal addresses, CIDR blocks.
func hostInList(rawURL, list string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	ip := net.ParseIP(host)
	for _, item := range strings.Split(list, ",") {
		item = strings.ToLower(strings.TrimSpace(item))
		switch {
		case item == "":
		case strings.Contains(item, "/"):
			if _, n, err := net.ParseCIDR(item); err == nil && ip != nil && n.Contains(ip) {
				return true
			}
		case ip != nil:
			if ip.Equal(net.ParseIP(item)) {
				return true
			}
		case strings.HasPrefix(item, "."):
			if strings.HasSuffix(host, item) {
				return true
			}
		case host == item || strings.HasSuffix(host, "."+item):
			return true
		}
	}
	return false
}

// applyStatus folds an engine snapshot into t. Caller holds m.mu.
func (m *Manager) applyStatus(t *Task, st *engine.Status, e engine.Engine, elapsed int64) {
	// Magnet: metadata arrived, the engine continues under a new reference
	if st.FollowedBy != "" && st.FollowedBy != t.EngineRef {
		m.metadataDone(t, st, e)
		return
	}
	m.running[t.Hash] = st.State == engine.Active
	t.DownRate, t.UpRate, t.Peers, t.Seeds = st.DownRate, st.UpRate, st.Connections, st.Seeders
	if st.State != engine.Active {
		// Engines may report a decaying speed for a while after a pause
		t.DownRate, t.UpRate = 0, 0
	}
	t.Bitfield, t.NumPieces, t.PieceLen = st.Bitfield, st.NumPieces, st.PieceLength
	t.Verifying = st.Verifying
	if !st.IsMetadata {
		if st.Total > 0 {
			t.Size = st.Total
		}
		t.DoneBytes = st.Completed
		if t.DownTotal < t.DoneBytes {
			t.DownTotal = t.DoneBytes
		}
		// A magnet task is called after the link's dn until the metadata
		// arrives; from then on it carries the torrent's own name, which
		// is also what its data is called
		if n := torrent.SafeName(st.Name); n != "" && n != t.Name && (t.Name == "" || t.Kind == KindBT && t.Name == linkName(t)) {
			t.Name = n
			m.markDirty(t)
		}
		if t.Comment == "" && st.Comment != "" {
			t.Comment = st.Comment
		}
		if len(st.Files) > 0 && (m.dirty[t.Hash] || time.Now().Unix()%5 == 0 || t.FilesTotal == 0) {
			m.syncFiles(t, st.Files)
		}
	}
	if up := t.UpBase + st.Uploaded; up > t.UpTotal {
		t.UpTotal = up
	}
	if st.DownRate > 0 || st.UpRate > 0 {
		t.Options.LastActive = time.Now().Unix()
	}
	if st.State == engine.Active && !st.Seeding && st.DownRate > 0 {
		t.ActiveSecs += elapsed
	}
	if t.State == StMoving && t.staged() {
		m.checkMove(t, st, e)
		return
	}
	switch st.State {
	case engine.Error:
		m.engineError(t, st, e)
	case engine.Complete:
		m.completed(t, st, e)
	case engine.Active:
		switch {
		case st.Seeding && st.Total > 0 && st.Completed >= st.Total:
			if t.State != StSeeding || t.InTemp() {
				m.downloadDone(t, st, e)
			}
		case st.Verifying:
			m.setState(t, StChecking)
		case st.IsMetadata:
			m.setState(t, StMetadata)
		default:
			if t.StartedAt == 0 {
				t.StartedAt = time.Now().Unix()
				m.TaskEvent("task.started", t, nil)
			}
			m.setState(t, StDownloading)
		}
	}
}

func (m *Manager) setState(t *Task, s string) {
	if t.State != s {
		t.State = s
		m.markDirty(t)
	}
}

func (m *Manager) syncFiles(t *Task, files []engine.File) {
	rows := make([]FileRow, 0, len(files))
	chosen := 0
	for _, f := range files {
		prio := 0
		if f.Selected {
			prio = 1
			chosen++
			if p, ok := t.Options.Priorities[fmt.Sprint(f.Index)]; ok && p > 0 {
				prio = p
			}
		}
		rows = append(rows, FileRow{Index: f.Index, Path: f.Path, Size: f.Size, Done: f.Completed, Priority: prio})
	}
	t.FilesTotal, t.FilesChosen = len(rows), chosen
	t.IsFolder = len(rows) > 1 || (len(rows) == 1 && strings.Contains(rows[0].Path, "/"))
	m.db.Tx(func(tx *sqlTx) error { return m.storeFiles(tx, t.Hash, rows) })
}

// metadataDone handles a magnet whose metadata arrived.
func (m *Manager) metadataDone(t *Task, st *engine.Status, e engine.Engine) {
	old := t.EngineRef
	t.EngineRef = st.FollowedBy
	// The engine may save the metadata as <infohash>.torrent in the download folder
	saved := filepath.Join(t.TempDir, t.Hash+".torrent")
	if b, err := os.ReadFile(saved); err == nil {
		if meta, perr := torrent.Parse(b); perr == nil && meta.InfoHash == t.Hash {
			os.WriteFile(m.torrentPath(t.Hash), b, 0600)
			os.Remove(saved)
			t.Name = meta.Name
			t.IsFolder = meta.IsFolder
			if t.Comment == "" {
				t.Comment = meta.Comment
			}
		}
	}
	if fe, ok := e.(interface{ Forget(string) }); ok {
		fe.Forget(old)
	}
	if len(t.Options.Select) > 0 {
		e.SetFiles(t.EngineRef, t.Options.Select, nil)
	}
	m.Log(t.Hash, "Got the torrent's file list")
	m.setState(t, StQueued)
	m.markDirty(t)
}

func (m *Manager) engineError(t *Task, st *engine.Status, e engine.Engine) {
	if t.State == StError {
		return
	}
	// Expired file-hosting link: resolve again (in the background) and continue
	if t.Options.Hoster != "" && m.Hosters != nil && (st.ErrorCode == "22" || st.ErrorCode == "24" || st.ErrorCode == "3") && t.Options.Retries < 3 {
		if fe, ok := e.(interface{ Forget(string) }); ok {
			fe.Forget(t.EngineRef)
		}
		t.Options.Retries++
		t.Options.Direct = ""
		t.EngineRef = ""
		m.Log(t.Hash, "The download URL may have expired; getting a new one")
		m.markDirty(t)
		return
	}
	// Transient failures (timeouts, network, 429/503) are retried with
	// back-off before the task is marked failed
	pu, _ := m.proxyFor(t)
	viaProxy := pu != ""
	if viaProxy && (strings.Contains(st.ErrorMsg, "Failed to establish connection") || strings.Contains(st.ErrorMsg, "Proxy")) {
		m.fail(t, "proxy_unreachable", "Cannot connect to the proxy server; the task will not fall back to a direct connection. Check Settings › Downloads › Proxy server.")
		return
	}
	if transient(st) && t.Options.AutoRetries < 6 && !(viaProxy && st.ErrorCode == "1") {
		if fe, ok := e.(interface{ Forget(string) }); ok {
			fe.Forget(t.EngineRef)
		}
		delay := int64(30) << uint(t.Options.AutoRetries)
		t.Options.AutoRetries++
		t.Options.RetryAt = time.Now().Unix() + delay
		t.EngineRef = ""
		t.State = StQueued
		t.DownRate, t.UpRate = 0, 0
		m.Log(t.Hash, fmt.Sprintf("Temporarily unable to download (%s); retrying in %d s", truncate(st.ErrorMsg, 80), delay))
		m.markDirty(t)
		return
	}
	code, msg := engineError(st.ErrorCode, st.ErrorMsg)
	m.fail(t, code, msg)
}

func (m *Manager) fail(t *Task, code, msg string) {
	t.State = StError
	t.ErrorCode, t.ErrorMsg = code, msg
	t.DownRate, t.UpRate = 0, 0
	m.markDirty(t)
	m.Log(t.Hash, "Error: "+msg)
	m.TaskEvent("task.failed", t, map[string]any{"error": map[string]any{"code": code, "message": msg, "retryable": true}})
}

// downloadDone: all selected data is present (torrents keep seeding). A
// staged torrent first moves out of its temporary folder and is reported
// complete once its data has arrived (torrentMoved).
func (m *Manager) downloadDone(t *Task, st *engine.Status, e engine.Engine) {
	again := t.FinishedAt != 0 && t.State != StDownloading
	t.State = StSeeding
	t.DoneBytes = t.Size
	m.markDirty(t)
	if t.InTemp() {
		m.moveTorrent(t, st, e)
		if t.InTemp() {
			return
		}
		// The engine cannot move it: it seeds in place like a 0.9.x torrent
	}
	if again {
		// Resumed after a pause or an engine restart: already reported
		return
	}
	if t.FinishedAt == 0 {
		t.FinishedAt = time.Now().Unix()
	}
	m.Log(t.Hash, "Download finished; seeding started")
	if p := t.dataPath(); p != "" {
		if u := ownerFor(t.Owner, m.isAdminOwner(t.Owner), p); u != "" {
			go chownPath(p, u, true)
		}
	}
	m.TaskEvent("task.completed", t, nil)
	if t.AutoRemove == "completed" {
		go m.autoRemove(t.Hash)
	}
}

// moveTorrent moves the complete data of a staged torrent from its
// temporary folder to its destination; the engine keeps seeding from there.
// Between volumes the data is copied into the destination volume's
// @DownloadCenterTemp first, so a half-copied file never shows up either.
// Caller holds m.mu.
func (m *Manager) moveTorrent(t *Task, st *engine.Status, e engine.Engine) {
	mv, ok := e.(engine.StorageMover)
	if !ok || !e.Caps().MoveWhileSeeding {
		m.unstage(t)
		return
	}
	dst := t.finalDir()
	dir, root := dst, t.Options.Root
	if !sameVolume(t.WorkDir, dst) {
		dir = m.workDirFor(dst, t.Hash)
		if ownTemp(dir) {
			os.RemoveAll(dir) // what an interrupted copy left behind
		}
	} else if name := torrentRoot(t, st); name != "" {
		p := filepath.Join(dst, name)
		q := uniquePath(p)
		if t.IsFolder {
			q = uniqueDir(p)
		}
		if q != p {
			root = filepath.Base(q)
		}
	}
	if err := os.MkdirAll(dir, 0775); err != nil {
		m.moveFailed(t, e, err.Error())
		return
	}
	if err := mv.MoveStorage(t.EngineRef, dir, root); err != nil {
		m.moveFailed(t, e, err.Error())
		return
	}
	if t.State != StMoving {
		m.Log(t.Hash, "Download finished; moving files")
	}
	t.Options.Root = root
	t.Options.MoveDst, t.Options.MoveAt = dst, time.Now().Unix()
	t.State = StMoving
	t.DownRate, t.UpRate = 0, 0
	m.markDirty(t)
	m.saveTask(t)
}

// checkMove follows the engine moving a staged torrent. Caller holds m.mu.
func (m *Manager) checkMove(t *Task, st *engine.Status, e engine.Engine) {
	if st.Moving {
		return
	}
	if st.MoveError != "" {
		m.moveFailed(t, e, st.MoveError)
		return
	}
	dir, dst := filepath.Clean(st.Dir), filepath.Clean(t.Options.MoveDst)
	switch {
	case dir == dst:
		m.torrentMoved(t, st, e)
	case dir != filepath.Clean(t.WorkDir) && dir == m.workDirFor(dst, t.Hash):
		// Copied to the destination's volume: now rename it into place
		old := t.WorkDir
		t.WorkDir = dir
		leaveTemp(old)
		m.moveTorrent(t, st, e)
	case time.Now().Unix()-t.Options.MoveAt > 30:
		// The engine lost the move (it restarted meanwhile): ask again
		m.moveTorrent(t, st, e)
	}
}

// torrentMoved: the data of a staged torrent has reached its destination.
func (m *Manager) torrentMoved(t *Task, st *engine.Status, e engine.Engine) {
	name := t.Options.Root
	if name == "" {
		name = torrentRoot(t, st)
	}
	t.DataPath = filepath.Join(t.Options.MoveDst, name)
	t.Options.MoveDst, t.Options.MoveAt = "", 0
	t.State = StSeeding
	// Torrents imported while complete were reported before
	report := t.FinishedAt == 0
	if report {
		t.FinishedAt = time.Now().Unix()
	}
	m.markDirty(t)
	m.saveTask(t)
	leaveTemp(t.WorkDir)
	if u := ownerFor(t.Owner, m.isAdminOwner(t.Owner), t.DataPath); u != "" {
		go chownPath(t.DataPath, u, true)
	}
	m.Log(t.Hash, "Files moved")
	if report {
		m.TaskEvent("task.completed", t, nil)
		if t.MoveDir != "" {
			m.TaskEvent("task.moved", t, map[string]any{"path": m.DisplayPath(t.Owner, t.DataPath)})
		}
	}
	if st.State == engine.Complete {
		// Seeding is already over (or not wanted)
		m.completed(t, st, e)
		return
	}
	if report && t.AutoRemove == "completed" {
		go m.autoRemove(t.Hash)
	}
}

// moveFailed stops a staged torrent whose data could not be moved. The data
// stays in its temporary folder; Retry checks it and moves it again.
func (m *Manager) moveFailed(t *Task, e engine.Engine, msg string) {
	if fe, ok := e.(interface{ Forget(string) }); ok && t.EngineRef != "" {
		fe.Forget(t.EngineRef)
	}
	if r := t.Options.Root; r != "" && !pathExists(filepath.Join(t.WorkDir, r)) {
		t.Options.Root = "" // the rename did not happen
	}
	t.Options.MoveDst, t.Options.MoveAt = "", 0
	m.fail(t, "move", "Failed to move files: "+msg)
}

// unstage falls back to how 0.9.x handled torrents when the engine cannot
// move data while seeding: seed where the data is, move it when seeding ends.
func (m *Manager) unstage(t *Task) {
	t.TempDir, t.MoveDir, t.WorkDir = t.WorkDir, t.finalDir(), ""
	m.markDirty(t)
}

// linkName is the name a magnet task's link suggests (its dn), "" for other
// tasks.
func linkName(t *Task) string {
	link := t.Options.Magnet
	if link == "" && t.IsMagnet() {
		link = t.Source
	}
	if link == "" {
		return ""
	}
	mg, err := torrent.ParseMagnet(link)
	if err != nil {
		return ""
	}
	return mg.Name
}

// torrentRoot is the name of a torrent's top folder (or single file) on disk.
func torrentRoot(t *Task, st *engine.Status) string {
	if st != nil && len(st.Files) > 0 {
		p := st.Files[0].Path
		if i := strings.IndexByte(p, '/'); i > 0 {
			return p[:i]
		}
		return p
	}
	if t.Options.Root != "" {
		return t.Options.Root
	}
	return t.Name
}

// completed: the engine finished the task (URL done, or torrent seeding over).
func (m *Manager) completed(t *Task, st *engine.Status, e engine.Engine) {
	if t.Kind == KindBT {
		if t.State != StSeeding || t.InTemp() {
			m.downloadDone(t, st, e)
			if t.RemovedAt != 0 || t.State != StSeeding {
				return // removed, or moving out of its temporary folder first
			}
		}
		t.SeededAt = time.Now().Unix()
		m.TaskEvent("task.seeding_finished", t, map[string]any{"ratio": t.Ratio()})
		if fe, ok := e.(interface{ Forget(string) }); ok {
			fe.Forget(t.EngineRef)
		}
		m.removePartFiles(t.Hash, st.Dir, t.SaveDir())
		if t.DataPath == "" && t.MoveDir != "" && t.MoveDir != t.TempDir && t.Name != "" {
			// Torrents of 0.9.x seed in place and move when seeding ends
			m.startMove(t, filepath.Join(t.TempDir, t.Name), t.MoveDir, false)
			return
		}
		if t.DataPath == "" && t.Name != "" {
			t.DataPath = filepath.Join(t.TempDir, t.Name)
		}
		m.finish(t)
		return
	}
	if fe, ok := e.(interface{ Forget(string) }); ok {
		fe.Forget(t.EngineRef)
	}
	t.DoneBytes = t.Size
	m.startMove(t, t.WorkDir, t.finalDir(), true)
}

// startMove moves finished data out of the engine's folder in the background.
func (m *Manager) startMove(t *Task, src, dst string, urlTask bool) {
	t.State = StMoving
	t.Options.MoveSrc, t.Options.MoveDst = src, dst
	t.DownRate, t.UpRate = 0, 0
	m.moving[t.Hash] = true
	m.markDirty(t)
	m.saveTask(t)
	owner, admin := t.Owner, m.isAdminOwner(t.Owner)
	go func() {
		var final string
		var err error
		if urlTask {
			final, err = moveURLResult(src, dst)
			if err == nil {
				removeTempDir(src)
			}
		} else {
			final, err = moveInto(src, dst)
		}
		if err == nil {
			if u := ownerFor(owner, admin, final); u != "" {
				chownPath(final, u, true)
			}
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.moving, t.Hash)
		if err != nil {
			m.fail(t, "move", "Failed to move files: "+err.Error())
			return
		}
		t.DataPath = final
		if urlTask {
			if t.FinishedAt == 0 {
				t.FinishedAt = time.Now().Unix()
			}
			if t.Name == "" {
				t.Name = filepath.Base(final)
			}
			m.TaskEvent("task.completed", t, nil)
		}
		if t.MoveDir != "" {
			m.TaskEvent("task.moved", t, map[string]any{"path": m.DisplayPath(t.Owner, final)})
		}
		m.finish(t)
	}()
}

// moveURLResult moves the downloaded file(s) of a URL task from its temp
// folder into dst and returns the path of the (first) result.
func moveURLResult(work, dst string) (string, error) {
	ents, err := os.ReadDir(work)
	if err != nil {
		return "", err
	}
	first := ""
	for _, e := range ents {
		if isControlFile(e.Name()) {
			continue
		}
		p, err := moveInto(filepath.Join(work, e.Name()), dst)
		if err != nil {
			return "", err
		}
		if first == "" {
			first = p
		}
	}
	if first == "" {
		return "", errors.New("The downloaded files are missing")
	}
	return first, nil
}

// finish marks t done. Caller holds m.mu.
func (m *Manager) finish(t *Task) {
	t.State = StDone
	t.DownRate, t.UpRate, t.Peers, t.Seeds = 0, 0, 0, 0
	t.EngineRef = ""
	m.markDirty(t)
	m.saveTask(t)
	m.Log(t.Hash, "Done")
	if t.AutoRemove == "completed" || t.AutoRemove == "seeded" {
		go m.autoRemove(t.Hash)
	}
}

func (m *Manager) autoRemove(hash string) {
	time.Sleep(time.Second)
	m.Remove(hash, false, true)
}

// scheduleMode is the current schedule mode: full, limited or off.
func (m *Manager) scheduleMode() string {
	s := m.Settings()
	return modeAt(s.Schedule, time.Now())
}

func modeAt(s Schedule, now time.Time) string {
	if !s.Enabled {
		return "full"
	}
	day := (int(now.Weekday()) + 6) % 7 // Monday = 0
	switch s.Days[day][now.Hour()] {
	case '0':
		return "off"
	case '2':
		return "limited"
	}
	return "full"
}

// ScheduleState returns the current mode and the next change.
func (m *Manager) ScheduleState() (mode string, next time.Time, nextMode string) {
	s := m.Settings()
	now := time.Now()
	mode = modeAt(s.Schedule, now)
	if !s.Schedule.Enabled {
		return mode, time.Time{}, ""
	}
	t := now.Truncate(time.Hour)
	for i := 1; i <= 24*7; i++ {
		c := t.Add(time.Duration(i) * time.Hour)
		if md := modeAt(s.Schedule, c); md != mode {
			return mode, c, md
		}
	}
	return mode, time.Time{}, ""
}

func (m *Manager) kindLimits(kind string) TypeLimits {
	s := m.Settings()
	switch kind {
	case KindHTTP:
		return s.HTTP
	case KindFTP:
		return s.FTP
	}
	return s.BT
}

// slotHolders lists the tasks that get a download slot with the tasks in this
// order: per kind, the first max_num that are in the engine and not paused,
// finished, failed, moving or seeding. The scheduler and the queue moves use
// the same rule, so what a move reports is what the next tick does.
func (m *Manager) slotHolders(list []*Task, mode string) map[string]bool {
	out := map[string]bool{}
	if mode == "off" {
		return out
	}
	count := map[string]int{}
	for _, t := range list {
		switch t.State {
		case StDone, StError, StMoving, StSeeding:
			continue
		}
		if t.EngineRef == "" || t.UserPaused {
			continue
		}
		// A paused torrent with all its data seeds when it resumes
		if t.State == StPaused && t.Kind == KindBT && t.FinishedAt > 0 && t.Size > 0 && t.DoneBytes >= t.Size {
			continue
		}
		if count[t.Kind] < m.kindLimits(t.Kind).MaxNum {
			count[t.Kind]++
			out[t.Hash] = true
		}
	}
	return out
}

// queueOrder is every live task in queue order. Caller holds m.mu.
func (m *Manager) queueOrder() []*Task {
	list := make([]*Task, 0, len(m.live))
	for _, t := range m.live {
		list = append(list, t)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Position != list[j].Position {
			return list[i].Position < list[j].Position
		}
		return list[i].CreatedAt < list[j].CreatedAt
	})
	return list
}

// QueueRanks gives each task waiting for a slot its place among the waiting
// tasks of its kind (from 1), counting every user's tasks.
func (m *Manager) QueueRanks() map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	ranks, count := map[string]int{}, map[string]int{}
	for _, t := range m.queueOrder() {
		if t.State == StQueued && !t.UserPaused {
			count[t.Kind]++
			ranks[t.Hash] = count[t.Kind]
		}
	}
	return ranks
}

// schedule decides which tasks run and applies speed limits. Caller holds m.mu.
func (m *Manager) schedule(now time.Time) {
	mode := m.scheduleMode()
	if m.schedMode != "" && mode != m.schedMode {
		m.Emit(Event{Type: "schedule.changed", Data: map[string]any{"mode": mode, "by": "schedule"}})
	}
	m.schedMode = mode
	nowU := now.Unix()
	list := m.queueOrder()
	count := map[string]int{}
	want := map[string]bool{}
	anyDown := false
	for _, t := range list {
		if t.WakeTime > 0 && nowU >= t.WakeTime {
			t.WakeTime = 0
			t.UserPaused = false
			m.markDirty(t)
			m.TaskEvent("task.resumed", t, map[string]any{"by": "timer"})
		}
	}
	holders := m.slotHolders(list, mode)
	for _, t := range list {
		switch t.State {
		case StDone, StError, StMoving:
			continue
		}
		if t.EngineRef == "" {
			continue
		}
		t.SchedPaused = false
		if t.UserPaused {
			want[t.Hash] = false
			if t.State != StPaused {
				t.State = StPaused
				m.markDirty(t)
			}
			continue
		}
		if t.State == StPaused {
			t.State = StQueued
			if t.Kind == KindBT && t.FinishedAt > 0 && t.Size > 0 && t.DoneBytes >= t.Size {
				t.State = StSeeding
			}
			m.markDirty(t)
		}
		if mode == "off" {
			want[t.Hash] = false
			t.SchedPaused = true
			if t.State == StDownloading || t.State == StMetadata {
				t.State = StQueued
				m.markDirty(t)
			}
			continue
		}
		if t.State == StSeeding {
			want[t.Hash] = true
			continue
		}
		if holders[t.Hash] {
			count[t.Kind]++
			want[t.Hash] = true
			anyDown = true
		} else {
			want[t.Hash] = false
			if t.State == StDownloading || t.State == StMetadata {
				t.State = StQueued
				m.markDirty(t)
			}
		}
	}
	// Apply run/pause and limits
	for _, t := range list {
		w, ok := want[t.Hash]
		if !ok {
			continue
		}
		e := m.engineOf(t)
		if e == nil || m.down[e.Name()] {
			continue
		}
		if w && !m.running[t.Hash] {
			if err := e.Resume(t.EngineRef); err == nil {
				m.running[t.Hash] = true
			}
		} else if !w && m.running[t.Hash] {
			if err := e.Pause(t.EngineRef); err == nil {
				m.running[t.Hash] = false
				t.DownRate, t.UpRate = 0, 0
			}
		}
		if w {
			down, up := m.taskLimits(t, mode, count)
			if a, ok := m.applied[t.Hash]; !ok || a[0] != down || a[1] != up {
				if e.SetLimits(t.EngineRef, down, up) == nil {
					m.applied[t.Hash] = [2]int64{down, up}
				}
			}
		}
	}
	if m.busy && !anyDown {
		m.Emit(Event{Type: "queue.idle"})
	}
	m.busy = anyDown
}

// taskLimits splits the per-type limit among that type's running tasks
// (the engines enforce per-task limits).
func (m *Manager) taskLimits(t *Task, mode string, count map[string]int) (down, up int64) {
	lim := m.kindLimits(t.Kind)
	d, u := lim.MaxDown, lim.MaxUp
	if mode == "limited" {
		if lim.LimitedDown > 0 {
			d = lim.LimitedDown
		}
		if lim.LimitedUp > 0 {
			u = lim.LimitedUp
		}
	}
	n := int64(count[t.Kind])
	if n < 1 {
		n = 1
	}
	if d > 0 {
		down = int64(d) * 1024 / n
	}
	if u > 0 {
		seeding := int64(0)
		for _, o := range m.live {
			if o.Kind == t.Kind && (o.State == StSeeding || o.State == StDownloading) {
				seeding++
			}
		}
		if seeding < 1 {
			seeding = 1
		}
		up = int64(u) * 1024 / seeding
	}
	if t.Options.MaxDown > 0 && (down == 0 || t.Options.MaxDown < down) {
		down = t.Options.MaxDown
	}
	if t.Options.MaxUp > 0 && (up == 0 || t.Options.MaxUp < up) {
		up = t.Options.MaxUp
	}
	if t.Kind == KindBT {
		if tm := int64(m.Settings().Torrent.TorrentMaxUp) * 1024; tm > 0 && (up == 0 || tm < up) {
			up = tm
		}
	}
	return down, up
}

// minute runs housekeeping: disk space, history purge, stale temp folders,
// expired pair codes, content-merge source switching.
func (m *Manager) minute() {
	s := m.Settings()
	now := time.Now()
	if s.DiskLowMB > 0 {
		m.checkDisk(int64(s.DiskLowMB) << 20)
	}
	m.switchStalledSources()
	if now.Sub(m.lastDay) >= 24*time.Hour {
		m.lastDay = now
		cut := now.Unix() - int64(s.HistoryDays)*86400
		m.db.X(`DELETE FROM task_files WHERE hash IN (SELECT hash FROM tasks WHERE removed_at > 0 AND removed_at < ?)`, cut)
		m.db.X(`DELETE FROM task_log WHERE hash IN (SELECT hash FROM tasks WHERE removed_at > 0 AND removed_at < ?)`, cut)
		m.db.X(`DELETE FROM tasks WHERE removed_at > 0 AND removed_at < ?`, cut)
		m.db.X(`DELETE FROM events WHERE time < ?`, now.Unix()-30*86400)
		m.db.X(`DELETE FROM audit WHERE time < ?`, now.Unix()-90*86400)
		m.db.X(`DELETE FROM deliveries WHERE time < ? AND status != 'pending'`, now.Unix()-30*86400)
		m.cleanTempFolders()
	}
	m.db.X(`DELETE FROM pair_codes WHERE expires_at < ?`, now.Unix())
}

var diskWarned = map[string]bool{}

func (m *Manager) checkDisk(min int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	low := map[string]bool{}
	for _, t := range m.live {
		if t.State != StDownloading && t.State != StQueued && t.State != StMetadata {
			continue
		}
		dir := t.TempDir
		free := FreeSpace(dir)
		if free >= 0 && free < min {
			low[dir] = true
			if !t.UserPaused {
				t.UserPaused = true
				m.markDirty(t)
				m.Log(t.Hash, "Not enough free space; paused")
				m.TaskEvent("task.paused", t, map[string]any{"by": "disk"})
			}
		}
	}
	for dir := range low {
		if !diskWarned[dir] {
			diskWarned[dir] = true
			m.Emit(Event{Type: "disk.low", Data: map[string]any{"folder": m.DisplayPath("", dir), "free": FreeSpace(dir)}})
		}
	}
	// Warned again only once the space has come back: tasks paused for lack of
	// space no longer count here, and a new one would repeat the warning
	for dir := range diskWarned {
		if !low[dir] {
			if free := FreeSpace(dir); free < 0 || free >= min {
				delete(diskWarned, dir)
			}
		}
	}
}

// cleanTempFolders removes @DownloadCenterTemp/<hash> folders older than 7
// days that no live task owns.
func (m *Manager) cleanTempFolders() {
	m.mu.Lock()
	owned := map[string]bool{}
	for _, t := range m.live {
		if t.WorkDir != "" {
			owned[t.WorkDir] = true
		}
	}
	m.mu.Unlock()
	roots := map[string]bool{}
	for _, d := range m.tempRoots() {
		roots[d] = true
	}
	cut := time.Now().Add(-7 * 24 * time.Hour)
	for root := range roots {
		ents, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range ents {
			p := filepath.Join(root, e.Name())
			if owned[p] {
				continue
			}
			if info, err := e.Info(); err == nil && info.ModTime().Before(cut) {
				os.RemoveAll(p)
			}
		}
		os.Remove(root)
	}
}

// isAdminOwner reports a task owner who is an administrator (a QTS
// administrator).
func (m *Manager) isAdminOwner(user string) bool {
	return qts.IsQTSAdmin(user)
}

// Live returns a snapshot of a task (nil if unknown or removed).
func (m *Manager) Live(hash string) *Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.live[hash]; ok {
		c := *t
		return &c
	}
	return nil
}

// List returns snapshots of all live tasks.
func (m *Manager) List() []*Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Task, 0, len(m.live))
	for _, t := range m.live {
		c := *t
		out = append(out, &c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Position != out[j].Position {
			return out[i].Position < out[j].Position
		}
		return out[i].CreatedAt < out[j].CreatedAt
	})
	return out
}

// History returns removed tasks of an owner ("" = all), newest first.
func (m *Manager) History(owner string, limit int) []*Task {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var ts []*Task
	if owner == "" {
		ts, _ = m.loadTasks(`removed_at > 0 ORDER BY removed_at DESC LIMIT ?`, limit)
	} else {
		ts, _ = m.loadTasks(`removed_at > 0 AND owner = ? ORDER BY removed_at DESC LIMIT ?`, owner, limit)
	}
	return ts
}

// errResolving: a file-hosting link is being resolved; the task is added to
// the engine on a later tick.
var errResolving = errors.New("resolving")

// resolveAsync resolves a file-hosting link outside the manager lock.
// Caller holds m.mu.
func (m *Manager) resolveAsync(t *Task) {
	if m.resolving[t.Hash] {
		return
	}
	m.resolving[t.Hash] = true
	owner, acct, link, hash := t.Owner, t.Options.HosterAcct, t.Options.OrigURL, t.Hash
	// The share link is resolved through the proxy the download uses: many
	// services tie the direct link to the address that asked for it
	proxy, perr := m.proxyFor(t)
	go func() {
		var res *Resolved
		err := perr
		if err == nil {
			res, err = m.Hosters.Resolve(owner, acct, link, proxy)
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.resolving, hash)
		t, ok := m.live[hash]
		if !ok {
			return
		}
		if err != nil {
			m.fail(t, "hoster", err.Error())
			return
		}
		t.Options.Direct, t.Options.DirectHeaders, t.Options.ExpiresAt = res.URL, res.Headers, res.ExpiresAt
		if res.Account != "" {
			t.Options.HosterAcct = res.Account
		}
		m.markDirty(t)
		go m.Kick()
	}()
}

// transient reports engine errors worth retrying automatically.
func transient(st *engine.Status) bool {
	switch st.ErrorCode {
	case "2", "5", "6", "19", "29":
		return true
	case "1", "22":
		msg := st.ErrorMsg
		return strings.Contains(msg, "Failed to establish connection") || strings.Contains(msg, "status=429") || strings.Contains(msg, "status=503") || strings.Contains(msg, "status=502") ||
			strings.Contains(msg, "status=504") || strings.Contains(msg, "No URI available") || strings.Contains(msg, "timeout")
	}
	return false
}
