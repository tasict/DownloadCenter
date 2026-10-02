// Package lt is the engine adapter for dc-bt, the libtorrent 2.0 sidecar:
// line-delimited JSON over a unix socket in data/run (see dcbt/PROTOCOL.md).
// dcd starts dc-bt, reuses a running one after its own restart, and starts it
// again when it dies (the manager calls Start when StatusAll fails).
package lt

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"downloadcenter/internal/engine"
)

// Available reports whether the dc-bt binary exists and is executable.
func Available(bin string) bool {
	st, err := os.Stat(bin)
	return err == nil && st.Mode().IsRegular() && st.Mode()&0111 != 0
}

// New returns the adapter (nil when the binary is missing).
func New(bin, dataDir string) engine.Engine {
	if !Available(bin) {
		return nil
	}
	return newEngine(bin, dataDir)
}

// Engine talks to one dc-bt process.
type Engine struct {
	bin     string
	dataDir string

	smu     sync.Mutex // start/stop
	mu      sync.Mutex // connection state
	conn    net.Conn
	wmu     sync.Mutex
	nextID  int64
	pending map[int64]chan reply
	global  engine.Global
	haveG   bool
	version string
	cmd     *exec.Cmd

	omu       sync.Mutex
	offsets   map[string]int64 // upload already counted before a resumed re-add
	needOff   map[string]bool
	resumeFor map[string]string // official .fastresume to use on the next add
}

type reply struct {
	OK     bool            `json:"ok"`
	Error  string          `json:"error"`
	Result json.RawMessage `json:"result"`
}

func newEngine(bin, dataDir string) *Engine {
	return &Engine{bin: bin, dataDir: dataDir, pending: map[int64]chan reply{},
		offsets: map[string]int64{}, needOff: map[string]bool{}, resumeFor: map[string]string{}}
}

func (e *Engine) Name() string    { return "libtorrent" }
func (e *Engine) Version() string { return e.version }

func (e *Engine) Caps() engine.Caps {
	return engine.Caps{
		Socks5Peers:          true,
		Socks5Trackers:       true,
		HTTPProxy:            true,
		UPnP:                 true,
		FilePriorityLevels:   true,
		GlobalConnLimit:      true,
		Sequential:           true,
		MoveWhileSeeding:     true,
		Webseeds:             true,
		ResumeImportOfficial: true,
		Torrents:             true,
	}
}

// sockPath is data/run/dc-bt.sock, or a private directory under /tmp when
// that path is too long for a unix socket address (108 bytes).
func (e *Engine) sockPath() string {
	p := filepath.Join(e.dataDir, "run", "dc-bt.sock")
	if len(p) < 100 {
		return p
	}
	h := sha1.Sum([]byte(e.dataDir))
	dir := filepath.Join(os.TempDir(), "dc-bt-"+hex.EncodeToString(h[:6]))
	os.MkdirAll(dir, 0700)
	os.Chmod(dir, 0700)
	return filepath.Join(dir, "sock")
}
func (e *Engine) pidPath() string      { return filepath.Join(e.dataDir, "run", "dc-bt.pid") }
func (e *Engine) settingsPath() string { return filepath.Join(e.dataDir, "dc-bt.json") }

// settingsJSON maps the global options to the dc-bt settings object.
func settingsJSON(g engine.Global) map[string]any {
	from, to := g.PortFrom, g.PortTo
	if from <= 0 {
		from, to = 16891, 16899
	}
	if to < from {
		to = from
	}
	px := map[string]any{"type": "none"}
	if g.Proxy.Type != "" && g.Proxy.Type != "none" && g.Proxy.Host != "" {
		px = map[string]any{
			"type": g.Proxy.Type, "host": g.Proxy.Host, "port": g.Proxy.Port,
			"user": g.Proxy.User, "pass": g.Proxy.Pass,
			"hostnames": g.Proxy.RemoteDNS, "peers": g.Proxy.ApplyPeers,
			"trackers": g.Proxy.ApplyTrackers, "force": g.Proxy.Force,
		}
		if g.Proxy.Type == "http" {
			// An HTTP proxy is only offered for trackers
			px["peers"] = false
		}
	}
	return map[string]any{
		"listen_from": from, "listen_to": to,
		"dht": g.DHT, "lsd": g.LSD, "pex": g.PEX, "upnp": g.UPnP, "encrypt": g.Encrypt,
		"connections_limit": g.MaxConn, "max_down": g.MaxDown, "max_up": g.MaxUp,
		"peer_id_prefix": g.PeerID, "user_agent": g.PeerAgent,
		"proxy": px,
	}
}

func (e *Engine) writeSettings() error {
	b, _ := json.Marshal(settingsJSON(e.global))
	os.MkdirAll(e.dataDir, 0700)
	tmp := e.settingsPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, e.settingsPath())
}

// Start connects to a running dc-bt or starts one.
func (e *Engine) Start() error {
	e.smu.Lock()
	defer e.smu.Unlock()
	if e.connected() {
		if _, err := e.getVersion(); err == nil {
			return nil
		}
		e.disconnect()
	}
	if err := e.connect(); err == nil {
		if v, err := e.getVersion(); err == nil {
			e.version = v
			if e.haveG {
				e.call("apply_settings", map[string]any{"settings": settingsJSON(e.global)}, nil)
			}
			log.Printf("libtorrent: reusing running dc-bt (%s)", v)
			return nil
		}
		e.disconnect()
	}
	return e.spawn()
}

func (e *Engine) spawn() error {
	os.MkdirAll(filepath.Join(e.dataDir, "run"), 0700)
	os.MkdirAll(filepath.Join(e.dataDir, "logs"), 0700)
	os.MkdirAll(filepath.Join(e.dataDir, "bt"), 0700)
	os.MkdirAll(filepath.Join(e.dataDir, "torrents"), 0700)
	if err := e.writeSettings(); err != nil {
		return err
	}
	e.killStale()
	cmd := exec.Command(e.bin, "--socket", e.sockPath(), "--state", filepath.Join(e.dataDir, "bt"),
		"--torrents", filepath.Join(e.dataDir, "torrents"), "--settings", e.settingsPath())
	cmd.Dir = e.dataDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	lf, _ := os.OpenFile(filepath.Join(e.dataDir, "logs", "dc-bt.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if lf != nil {
		if st, err := lf.Stat(); err == nil && st.Size() > 4<<20 {
			lf.Truncate(0)
		}
	}
	cmd.Stdout, cmd.Stderr = lf, lf
	if err := cmd.Start(); err != nil {
		if lf != nil {
			lf.Close()
		}
		return fmt.Errorf("dc-bt: start: %w", err)
	}
	e.cmd = cmd
	os.WriteFile(e.pidPath(), []byte(strconv.Itoa(cmd.Process.Pid)), 0600)
	go func() {
		cmd.Wait()
		if lf != nil {
			lf.Close()
		}
	}()
	for i := 0; i < 75; i++ {
		time.Sleep(200 * time.Millisecond)
		if e.connect() == nil {
			if v, err := e.getVersion(); err == nil {
				e.version = v
				log.Printf("libtorrent: started dc-bt %s (pid %d)", v, cmd.Process.Pid)
				return nil
			}
			e.disconnect()
		}
	}
	return errors.New("dc-bt did not come up")
}

// killStale stops a dc-bt from an earlier dcd that no longer answers.
func (e *Engine) killStale() {
	b, err := os.ReadFile(e.pidPath())
	if err != nil {
		return
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid <= 1 {
		return
	}
	cmdline, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if !strings.Contains(string(cmdline), "dc-bt") {
		return
	}
	syscall.Kill(pid, syscall.SIGTERM)
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err != nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	syscall.Kill(pid, syscall.SIGKILL)
}

func (e *Engine) connected() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.conn != nil
}

func (e *Engine) connect() error {
	c, err := net.DialTimeout("unix", e.sockPath(), 2*time.Second)
	if err != nil {
		return err
	}
	e.mu.Lock()
	if e.conn != nil {
		e.conn.Close()
	}
	e.conn = c
	e.mu.Unlock()
	go e.readLoop(c)
	return nil
}

func (e *Engine) disconnect() {
	e.mu.Lock()
	if e.conn != nil {
		e.conn.Close()
		e.conn = nil
	}
	for id, ch := range e.pending {
		ch <- reply{Error: "dc-bt: connection closed"}
		delete(e.pending, id)
	}
	e.mu.Unlock()
}

func (e *Engine) readLoop(c net.Conn) {
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64*1024), 256<<20)
	for sc.Scan() {
		var msg struct {
			ID    *int64 `json:"id"`
			Event string `json:"event"`
			reply
		}
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			continue
		}
		if msg.ID == nil {
			if msg.Event == "listen_failed" || msg.Event == "torrent_error" {
				log.Printf("libtorrent: %s", sc.Text())
			}
			continue
		}
		e.mu.Lock()
		ch := e.pending[*msg.ID]
		delete(e.pending, *msg.ID)
		e.mu.Unlock()
		if ch != nil {
			ch <- msg.reply
		}
	}
	e.mu.Lock()
	if e.conn == c {
		e.conn = nil
		for id, ch := range e.pending {
			ch <- reply{Error: "dc-bt: connection closed"}
			delete(e.pending, id)
		}
	}
	e.mu.Unlock()
	c.Close()
}

// call sends one command and waits for its answer.
func (e *Engine) call(cmd string, args map[string]any, out any) error {
	return e.callTimeout(cmd, args, out, 30*time.Second)
}

func (e *Engine) callTimeout(cmd string, args map[string]any, out any, timeout time.Duration) error {
	e.mu.Lock()
	c := e.conn
	if c == nil {
		e.mu.Unlock()
		return errors.New("dc-bt: not connected")
	}
	e.nextID++
	id := e.nextID
	ch := make(chan reply, 1)
	e.pending[id] = ch
	e.mu.Unlock()
	msg := map[string]any{}
	for k, v := range args {
		msg[k] = v
	}
	msg["id"] = id
	msg["cmd"] = cmd
	b, _ := json.Marshal(msg)
	b = append(b, '\n')
	e.wmu.Lock()
	c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := c.Write(b)
	e.wmu.Unlock()
	if err != nil {
		e.mu.Lock()
		delete(e.pending, id)
		e.mu.Unlock()
		e.disconnect()
		return fmt.Errorf("dc-bt: %w", err)
	}
	select {
	case r := <-ch:
		if !r.OK {
			if r.Error == "" {
				r.Error = "failed"
			}
			if r.Error == "torrent not found" {
				return engine.ErrNotFound
			}
			return errors.New("dc-bt: " + r.Error)
		}
		if out != nil && len(r.Result) > 0 {
			return json.Unmarshal(r.Result, out)
		}
		return nil
	case <-time.After(timeout):
		e.mu.Lock()
		delete(e.pending, id)
		e.mu.Unlock()
		return errors.New("dc-bt: timeout")
	}
}

func (e *Engine) getVersion() (string, error) {
	var v struct {
		Dcbt       string `json:"dcbt"`
		Libtorrent string `json:"libtorrent"`
	}
	if err := e.callTimeout("version", nil, &v, 5*time.Second); err != nil {
		return "", err
	}
	return v.Libtorrent, nil
}

func (e *Engine) Health() error {
	_, err := e.getVersion()
	return err
}

// Stop saves the state and ends dc-bt.
func (e *Engine) Stop() error {
	e.smu.Lock()
	defer e.smu.Unlock()
	e.shutdown()
	return nil
}

func (e *Engine) shutdown() {
	pid := 0
	if e.cmd != nil && e.cmd.Process != nil {
		pid = e.cmd.Process.Pid
	} else if b, err := os.ReadFile(e.pidPath()); err == nil {
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	e.callTimeout("shutdown", nil, nil, 5*time.Second)
	e.disconnect()
	if pid > 1 {
		for i := 0; i < 150; i++ {
			if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err != nil {
				break
			}
			if i == 100 {
				syscall.Kill(pid, syscall.SIGTERM)
			}
			time.Sleep(200 * time.Millisecond)
		}
		if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err == nil {
			syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	e.cmd = nil
	os.Remove(e.pidPath())
}

// ApplyGlobal applies settings live (dc-bt needs no restart).
func (e *Engine) ApplyGlobal(g engine.Global) error {
	e.global, e.haveG = g, true
	e.writeSettings()
	if !e.connected() {
		return nil
	}
	return e.call("apply_settings", map[string]any{"settings": settingsJSON(g)}, nil)
}

// Restart applies new settings; dc-bt applies them live.
func (e *Engine) Restart(g engine.Global) error {
	if err := e.ApplyGlobal(g); err != nil {
		e.smu.Lock()
		e.shutdown()
		err = e.spawn()
		e.smu.Unlock()
		return err
	}
	return nil
}

// SetFastResume registers an official libtorrent 1.2 .fastresume file to try
// on the next Add of that info hash (used by the importer).
func (e *Engine) SetFastResume(infohash, path string) {
	e.omu.Lock()
	e.resumeFor[strings.ToLower(infohash)] = path
	e.omu.Unlock()
}

// Add adds a torrent; the reference is its info hash.
func (e *Engine) Add(hash string, r engine.AddRequest) (string, error) {
	args := map[string]any{
		"save_path":  r.Dir,
		"paused":     r.Paused,
		"seed_ratio": r.SeedRatio,
		"seed_time":  r.SeedTime,
		"sequential": r.Sequential,
		"check":      r.Check,
		"max_down":   r.MaxDown,
		"max_up":     r.MaxUp,
		"max_peers":  r.MaxPeers,
	}
	switch {
	case len(r.Torrent) > 0:
		args["torrent_b64"] = base64.StdEncoding.EncodeToString(r.Torrent)
	case r.Magnet != "":
		args["magnet"] = r.Magnet
	default:
		return "", errors.New("dc-bt: only torrents and magnet links")
	}
	if r.Select != nil {
		args["select"] = r.Select
	}
	if len(r.Trackers) > 0 {
		args["trackers"] = r.Trackers
	}
	if r.MetadataOnly {
		args["metadata_only"] = true
	}
	if hash != "" {
		e.omu.Lock()
		if p := e.resumeFor[strings.ToLower(hash)]; p != "" {
			args["fastresume"] = p
			delete(e.resumeFor, strings.ToLower(hash))
		}
		e.omu.Unlock()
	}
	var res struct {
		Infohash string `json:"infohash"`
		Existed  bool   `json:"existed"`
		Resumed  bool   `json:"resumed"`
	}
	if err := e.call("add", args, &res); err != nil {
		return "", err
	}
	if res.Resumed && !r.MetadataOnly {
		// The resume data carries the all-time upload the manager already
		// counted before this re-add: report only what comes on top of it
		e.omu.Lock()
		e.needOff[res.Infohash] = true
		e.omu.Unlock()
	}
	return res.Infohash, nil
}

func (e *Engine) simple(cmd, ref string, extra map[string]any) error {
	args := map[string]any{"infohash": ref}
	for k, v := range extra {
		args[k] = v
	}
	return e.call(cmd, args, nil)
}

func (e *Engine) Pause(ref string) error {
	err := e.simple("pause", ref, nil)
	if err == engine.ErrNotFound {
		return nil
	}
	return err
}

func (e *Engine) Resume(ref string) error { return e.simple("resume", ref, nil) }

func (e *Engine) Remove(ref string) error {
	e.omu.Lock()
	delete(e.offsets, ref)
	delete(e.needOff, ref)
	e.omu.Unlock()
	return e.simple("remove", ref, nil)
}

// Forget drops a finished torrent from the session, keeping its data.
func (e *Engine) Forget(ref string) { e.Remove(ref) }

func (e *Engine) SetFiles(ref string, sel []int, prio map[int]int) error {
	if len(sel) == 0 {
		return errors.New("at least one file must be selected")
	}
	args := map[string]any{"select": sel}
	// Levels only when the caller used them (high/normal/low = 7/4/1);
	// plain "download" (1 everywhere) keeps libtorrent's default priority
	levels := false
	for _, p := range prio {
		if p > 1 {
			levels = true
		}
	}
	if levels {
		pm := map[string]int{}
		for i, p := range prio {
			if p > 7 {
				p = 7
			}
			if p > 0 {
				pm[strconv.Itoa(i)] = p
			}
		}
		args["priorities"] = pm
	}
	return e.simple("set_file_priorities", ref, args)
}

func (e *Engine) SetLimits(ref string, down, up int64) error {
	err := e.simple("set_limits", ref, map[string]any{"down": down, "up": up})
	if err == engine.ErrNotFound {
		return nil
	}
	return err
}

func (e *Engine) SetSequential(ref string, on bool) error {
	return e.simple("set_sequential", ref, map[string]any{"on": on})
}

func (e *Engine) SetSeeding(ref string, ratio float64, minutes int) error {
	return e.simple("set_seeding", ref, map[string]any{"seed_ratio": ratio, "seed_time": minutes})
}

func (e *Engine) AddTrackers(ref string, trackers []string) error {
	return e.simple("add_trackers", ref, map[string]any{"trackers": trackers})
}

func (e *Engine) ReplaceURIs(ref string, uris []string) error { return engine.ErrUnsupported }

// rawStatus is one torrent of status_all.
type rawStatus struct {
	Infohash        string `json:"infohash"`
	Name            string `json:"name"`
	SavePath        string `json:"save_path"`
	State           string `json:"state"`
	Paused          bool   `json:"paused"`
	Complete        bool   `json:"complete"`
	HasMetadata     bool   `json:"has_metadata"`
	Error           string `json:"error"`
	TotalWanted     int64  `json:"total_wanted"`
	TotalWantedDone int64  `json:"total_wanted_done"`
	AllTimeUpload   int64  `json:"all_time_upload"`
	AllTimeDownload int64  `json:"all_time_download"`
	DownRate        int64  `json:"down_rate"`
	UpRate          int64  `json:"up_rate"`
	Peers           int    `json:"peers"`
	Seeds           int    `json:"seeds"`
	Pieces          string `json:"pieces"`
	NumPieces       int    `json:"num_pieces"`
	PieceLength     int64  `json:"piece_length"`
	Comment         string `json:"comment"`
	Files           []struct {
		Index    int    `json:"index"`
		Path     string `json:"path"`
		Size     int64  `json:"size"`
		Done     int64  `json:"done"`
		Priority int    `json:"priority"`
		Pad      bool   `json:"pad"`
	} `json:"files"`
}

func (e *Engine) convert(r *rawStatus) *engine.Status {
	st := &engine.Status{
		Ref: r.Infohash, Name: r.Name, Dir: r.SavePath, InfoHash: r.Infohash,
		Total: r.TotalWanted, Completed: r.TotalWantedDone,
		DownRate: r.DownRate, UpRate: r.UpRate, Connections: r.Peers, Seeders: r.Seeds,
		NumPieces: r.NumPieces, PieceLength: r.PieceLength, Bitfield: r.Pieces, Comment: r.Comment,
		IsMetadata: r.State == "downloading_metadata" || !r.HasMetadata,
		Verifying:  r.State == "checking_files" || r.State == "checking_resume_data",
	}
	e.omu.Lock()
	if e.needOff[r.Infohash] {
		e.offsets[r.Infohash] = r.AllTimeUpload
		delete(e.needOff, r.Infohash)
	}
	st.Uploaded = r.AllTimeUpload - e.offsets[r.Infohash]
	e.omu.Unlock()
	if st.Uploaded < 0 {
		st.Uploaded = 0
	}
	switch {
	case r.Complete:
		st.State = engine.Complete
		st.Seeding = true
	case r.Error != "":
		st.State = engine.Error
		st.ErrorCode = "libtorrent"
		st.ErrorMsg = r.Error
	case r.Paused:
		st.State = engine.Paused
	default:
		st.State = engine.Active
		st.Seeding = r.HasMetadata && (r.State == "finished" || r.State == "seeding")
	}
	for _, f := range r.Files {
		st.Files = append(st.Files, engine.File{
			Index: f.Index, Path: f.Path, Size: f.Size, Completed: f.Done,
			Selected: f.Priority > 0, Priority: f.Priority,
		})
	}
	return st
}

func (e *Engine) StatusAll() (map[string]*engine.Status, error) {
	var res struct {
		Torrents []rawStatus `json:"torrents"`
	}
	if err := e.call("status_all", map[string]any{"files": true}, &res); err != nil {
		return nil, err
	}
	out := make(map[string]*engine.Status, len(res.Torrents))
	for i := range res.Torrents {
		s := e.convert(&res.Torrents[i])
		out[s.Ref] = s
	}
	return out, nil
}

func (e *Engine) Status(ref string) (*engine.Status, error) {
	var r rawStatus
	if err := e.call("status", map[string]any{"infohash": ref}, &r); err != nil {
		return nil, err
	}
	return e.convert(&r), nil
}

func (e *Engine) Peers(ref string) ([]engine.Peer, error) {
	var res struct {
		Peers []struct {
			IP       string  `json:"ip"`
			Port     int     `json:"port"`
			Client   string  `json:"client"`
			DownRate int64   `json:"down_rate"`
			UpRate   int64   `json:"up_rate"`
			Seed     bool    `json:"seed"`
			Progress float64 `json:"progress"`
		} `json:"peers"`
	}
	if err := e.call("peers", map[string]any{"infohash": ref}, &res); err != nil {
		if err == engine.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	out := make([]engine.Peer, 0, len(res.Peers))
	for _, p := range res.Peers {
		out = append(out, engine.Peer{IP: p.IP, Port: p.Port, Client: p.Client, DownRate: p.DownRate, UpRate: p.UpRate, Seeder: p.Seed, Progress: p.Progress})
	}
	return out, nil
}

func (e *Engine) Trackers(ref string) ([]string, error) {
	var res struct {
		Trackers []string `json:"trackers"`
	}
	if err := e.call("trackers", map[string]any{"infohash": ref}, &res); err != nil {
		return nil, err
	}
	return res.Trackers, nil
}

// SaveState writes resume data for every torrent.
func (e *Engine) SaveState() error {
	return e.callTimeout("save_state", nil, nil, 30*time.Second)
}
