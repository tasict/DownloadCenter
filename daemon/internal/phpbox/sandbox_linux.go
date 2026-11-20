//go:build linux

package phpbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// layout is what of the system the box shows: QTS's PHP and what it needs,
// read-only, at the same paths. It is found again for every check and run,
// so a PHP that moved is never run from a stale path.
type layout struct {
	php    string // the PHP binary (symbolic links resolved)
	extDir string
	exts   []string
	binds  []string    // directories bound read-only at the same path
	links  [][2]string // symbolic links made again: path, target
	capath string
}

// Extensions loaded besides curl when PHP has them.
var optionalExts = []string{"mbstring", "iconv", "openssl", "zlib"}

// discover finds QTS's PHP at phpPath, without running anything.
func discover() (*layout, string, string) {
	fi, err := os.Stat(phpPath)
	if err != nil {
		return nil, CodePHPMissing, ReasonPHPMissing
	}
	real, err := filepath.EvalSymlinks(phpPath)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode()&0o111 == 0 {
		return nil, CodePHPBroken, ReasonPHPNoStart
	}
	// The tree PHP comes in: <tree>/bin/php, <tree>/modules, <tree>/lib
	tree := filepath.Dir(filepath.Dir(phpPath))
	ext, err := filepath.EvalSymlinks(filepath.Join(tree, "modules"))
	if err != nil || !exists(filepath.Join(ext, "curl.so")) {
		return nil, CodePHPBroken, ReasonPHPNoCurl
	}
	l := &layout{php: real, extDir: ext, exts: []string{"curl"}}
	for _, e := range optionalExts {
		if exists(filepath.Join(ext, e+".so")) {
			l.exts = append(l.exts, e)
		}
	}
	// Only bin, modules and lib of the tree: it also holds the web
	// server's logs and a link to its settings
	paths := []string{filepath.Dir(phpPath), filepath.Join(tree, "modules"), filepath.Join(tree, "lib"),
		"/lib", "/lib64", "/usr/lib", "/usr/lib64", "/etc/ssl/certs"}
	for _, p := range paths {
		if _, err := os.Lstat(p); err != nil {
			continue
		}
		if err := l.add(p, 0); err != nil {
			return nil, CodePHPBroken, ReasonPHPNoStart
		}
	}
	if exists("/etc/ssl/certs") {
		l.capath = "/etc/ssl/certs"
	}
	sort.Strings(l.binds)
	return l, "", ""
}

// add plans p: every symbolic link on the way is made again in the box,
// the directory it ends in is bound.
func (l *layout) add(p string, depth int) error {
	if depth > 8 {
		return errors.New("too many links")
	}
	parts := strings.Split(strings.Trim(filepath.Clean(p), "/"), "/")
	cur := "/"
	for i, part := range parts {
		next := filepath.Join(cur, part)
		fi, err := os.Lstat(next)
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(next)
			if err != nil {
				return err
			}
			l.link(next, target)
			resolved, err := filepath.EvalSymlinks(next)
			if err != nil {
				return err
			}
			return l.add(filepath.Join(append([]string{resolved}, parts[i+1:]...)...), depth+1)
		}
		cur = next
	}
	fi, err := os.Stat(cur)
	if err != nil || !fi.IsDir() {
		return errors.New("not a directory")
	}
	for _, b := range l.binds {
		if b == cur {
			return nil
		}
	}
	l.binds = append(l.binds, cur)
	return nil
}

func (l *layout) link(p, target string) {
	for _, k := range l.links {
		if k[0] == p {
			return
		}
	}
	l.links = append(l.links, [2]string{p, target})
}

// helperConfig is what the helper gets on stdin.
type helperConfig struct {
	Root      string      `json:"root"`
	PHP       string      `json:"php"`
	ExtDir    string      `json:"ext_dir"`
	Exts      []string    `json:"exts"`
	Binds     []string    `json:"binds"`
	Links     [][2]string `json:"links"`
	PluginDir string      `json:"plugin_dir"`
	Shim      string      `json:"shim"`
	Basedir   bool        `json:"basedir"`
	Wall      int         `json:"wall"`
	Request   []byte      `json:"request"`
	// CAFile is the merged CA file on the host ("": none), copied to
	// boxCAFile
	CAFile string `json:"ca_file,omitempty"`
	// The uid and gid PHP runs as (its slot's), and its memory limit
	UID    uint32 `json:"uid"`
	Memory uint64 `json:"memory"`
}

// status is what the helper says on the control socket.
type status struct {
	Stage string `json:"stage"` // setup, filter, exec, done
	Err   string `json:"err,omitempty"`
}

// root is the empty directory the box's root is mounted on.
func (r *Runtime) root() (string, error) {
	dir := filepath.Join(r.cfg.Dir, "root")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	os.Chmod(r.cfg.Dir, 0o700)
	return dir, nil
}

// capped keeps the first max bytes written and drops the rest.
type capped struct {
	max      int
	buf      bytes.Buffer
	overflow bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
			c.overflow = true
		} else {
			c.buf.Write(p)
		}
	} else if len(p) > 0 {
		c.overflow = true
	}
	return len(p), nil
}

// run runs a shim in a new sandbox and waits for it, at most r.wall.
func (r *Runtime) run(ctx context.Context, lay *layout, pluginDir, shim string, probe bool, req request) runOut {
	var slot int
	select {
	case slot = <-r.slots:
	case <-ctx.Done():
		return runOut{cancelled: true}
	}
	defer func() { r.slots <- slot }()
	if ctx.Err() != nil {
		return runOut{cancelled: true}
	}
	outer := ctx
	ctx, cancel := context.WithTimeout(ctx, r.wall)
	defer cancel()

	root, err := r.root()
	if err != nil {
		return runOut{err: err, stage: "start"}
	}
	req.UserAgent, req.CAPath = r.userAgent(), lay.capath
	caFile := r.caFile()
	if caFile != "" {
		req.CAInfo = boxCAFile
	}
	reqJSON, _ := json.Marshal(req)
	cfg, _ := json.Marshal(helperConfig{Root: root, PHP: lay.php, ExtDir: lay.extDir, Exts: lay.exts, Binds: lay.binds,
		Links: lay.links, PluginDir: pluginDir, Shim: shim, Basedir: !probe, Wall: int(r.wall / time.Second), Request: reqJSON,
		UID: r.uidOf(slot), Memory: memoryLimit, CAFile: caFile})

	// The control socket (connections out, status) and the answer pipe
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_SEQPACKET|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return runOut{err: err, stage: "start"}
	}
	ctlParent, ctlChild := os.NewFile(uintptr(fds[0]), "control"), os.NewFile(uintptr(fds[1]), "control")
	ctlConn, err := net.FileConn(ctlParent)
	ctlParent.Close()
	if err != nil {
		ctlChild.Close()
		return runOut{err: err, stage: "start"}
	}
	ctl := ctlConn.(*net.UnixConn)
	defer ctl.Close()
	ansR, ansW, err := os.Pipe()
	if err != nil {
		ctlChild.Close()
		return runOut{err: err, stage: "start"}
	}
	defer ansR.Close()

	out := &capped{max: maxStdout}
	logs := &capped{max: maxStdout}
	cmd := exec.Command("/proc/self/exe", HelperArg)
	cmd.Env = []string{}
	cmd.Dir = "/"
	cmd.Stdin = bytes.NewReader(cfg)
	cmd.Stdout, cmd.Stderr = out, logs
	cmd.ExtraFiles = []*os.File{ctlChild, ansW}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWNET | syscall.CLONE_NEWNS | syscall.CLONE_NEWPID | syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS,
		Pdeathsig:  syscall.SIGKILL,
	}
	// Pdeathsig follows the thread that started the helper: keep it
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	r.starts.Add(1)
	err = cmd.Start()
	ctlChild.Close()
	ansW.Close()
	if err != nil {
		return runOut{err: err, stage: "start"}
	}

	sess := r.relay.session(ctx, probe)
	var stages []status
	ctlDone := make(chan struct{})
	go func() {
		stages = r.control(ctl, sess)
		close(ctlDone)
	}()
	ans := &capped{max: maxAnswer}
	ansDone := make(chan struct{})
	go func() {
		io.Copy(ans, ansR)
		close(ansDone)
	}()
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	ro := runOut{stage: "start"}
	select {
	case <-waited:
	case <-ctx.Done():
		// Killing the namespace's first process ends every process in it
		cmd.Process.Kill()
		<-waited
		// The time limit, or the caller gave up
		ro.timedOut, ro.cancelled = outer.Err() == nil, outer.Err() != nil
	}
	<-ansDone
	ctl.Close()
	<-ctlDone
	sess.close()
	for _, st := range stages {
		ro.stage = st.Stage
		if st.Err != "" {
			ro.err = errors.New(st.Err)
		}
	}
	if ro.timedOut || ro.cancelled {
		ro.err = nil
	}
	ro.answer, ro.overflow, ro.uid = ans.buf.Bytes(), ans.overflow, r.uidOf(slot)
	ro.log = logs.buf.String()
	ro.relay = sess.stats()
	return ro
}

// control reads the helper's messages until it is gone: connections the
// box opened (file descriptors) go to the relay, status is returned.
func (r *Runtime) control(c *net.UnixConn, sess *session) []status {
	var stages []status
	buf := make([]byte, 1024)
	oob := make([]byte, syscall.CmsgSpace(4*4))
	for {
		n, oobn, _, _, err := c.ReadMsgUnix(buf, oob)
		if err != nil || n == 0 {
			return stages
		}
		var conns []net.Conn
		if oobn > 0 {
			msgs, _ := syscall.ParseSocketControlMessage(oob[:oobn])
			for _, m := range msgs {
				fds, _ := syscall.ParseUnixRights(&m)
				for _, fd := range fds {
					f := os.NewFile(uintptr(fd), "relay")
					if conn, err := net.FileConn(f); err == nil {
						conns = append(conns, conn)
					}
					f.Close()
				}
			}
		}
		if buf[0] == 'C' {
			for _, conn := range conns {
				sess.serve(conn)
			}
			continue
		}
		for _, conn := range conns {
			conn.Close()
		}
		var st status
		if buf[0] == 'S' && len(stages) < 8 && json.Unmarshal(buf[1:n], &st) == nil {
			stages = append(stages, st)
		}
	}
}

// probeView is what the self-check reports from inside the box.
type probeView struct {
	PHP           string          `json:"php"`
	UID           int             `json:"uid"`
	EUID          int             `json:"euid"`
	GID           int             `json:"gid"`
	Groups        []int           `json:"groups"`
	Root          []string        `json:"root"`
	Exists        map[string]bool `json:"exists"`
	Writable      map[string]bool `json:"writable"`
	TmpWrite      bool            `json:"tmp_write"`
	Procs         []int           `json:"procs"`
	Functions     map[string]bool `json:"functions"`
	Extensions    map[string]bool `json:"extensions"`
	Direct        bool            `json:"direct"`
	RelayHTTP     string          `json:"relay_http"`
	BasedirRoot   bool            `json:"basedir_root"`
	BasedirLoosen bool            `json:"basedir_loosen"`
	Seccomp       int             `json:"seccomp"`
	FifoMade      bool            `json:"fifo_made"`
	FifoErrno     int             `json:"fifo_errno"`
	AddressSpace  string          `json:"address_space"`
	MaxProcesses  string          `json:"max_processes"`
}

// probe runs the self-check in the sandbox and judges what it saw.
func (r *Runtime) probe(ctx context.Context, lay *layout) (string, string) {
	direct := "192.0.2.1:80"
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
				direct = net.JoinHostPort(ipn.IP.String(), "8080")
				break
			}
		}
	}
	hidden := []string{"/share", "/etc/passwd", "/etc/shadow", "/etc/config", "/etc/config/shadow", "/root", "/home",
		"/var", "/sys", "/bin/sh", "/mnt/ext/opt/apache/conf", "/mnt/ext/opt/apache/logs"}
	if r.cfg.Dir != "" {
		hidden = append(hidden, r.cfg.Dir, filepath.Dir(r.cfg.Dir))
	}
	ro := r.run(ctx, lay, "", "probe.php", true, request{Mode: "probe", Paths: hidden, Direct: direct})
	switch {
	case ro.cancelled:
		return CodeCheckFailed, ReasonCheckFailed
	case ro.err != nil && (ro.stage == "start" || ro.stage == "setup" || ro.stage == "filter"):
		return CodeNoIsolation, ReasonNoIsolation
	case ro.err != nil:
		return CodePHPBroken, ReasonPHPNoStart
	case len(ro.answer) == 0 && strings.Contains(ro.log, "error while loading shared libraries"):
		return CodePHPBroken, ReasonPHPNoStart
	case ro.timedOut || ro.overflow || len(ro.answer) == 0:
		return CodeCheckFailed, ReasonCheckFailed
	}
	a, _, err := parseAnswer(ro.answer)
	if err != nil || len(a.Probe) == 0 {
		return CodeCheckFailed, ReasonCheckFailed
	}
	var p probeView
	if json.Unmarshal(a.Probe, &p) != nil {
		return CodeCheckFailed, ReasonCheckFailed
	}
	if !p.Extensions["curl"] {
		return CodePHPBroken, ReasonPHPNoCurl
	}
	if !r.judge(lay, &p, ro.relay, int(ro.uid)) {
		return CodeCheckFailed, ReasonCheckFailed
	}
	return "", ""
}

// judge decides whether the box was what it must be: the slot's uid and
// gid (uid) without groups; a root of nothing but what the layout put
// there; none of the hidden paths; nothing writable but /tmp; no other
// process; none of the disabled functions; no way out but the relay, whose
// plain and tunnelled requests both arrived; open_basedir that holds; the
// syscall filter on (a FIFO refused with EPERM), the memory limit set and
// no second process or thread allowed.
func (r *Runtime) judge(lay *layout, p *probeView, rs RelayStats, uid int) bool {
	want := uid + r.skewUID
	allowed := map[string]bool{"dev": true, "etc": true, "host": true, "plugin": true, "proc": true, "tmp": true}
	for _, b := range lay.binds {
		allowed[strings.Split(strings.Trim(b, "/"), "/")[0]] = true
	}
	for _, k := range lay.links {
		allowed[strings.Split(strings.Trim(k[0], "/"), "/")[0]] = true
	}
	ok := uid != 0 && p.UID == want && p.EUID == want && p.GID == want && len(p.Groups) == 0 && p.Root != nil
	ok = ok && p.Seccomp == 2 && !p.FifoMade && p.FifoErrno == int(syscall.EPERM) && p.AddressSpace == strconv.Itoa(memoryLimit) &&
		p.MaxProcesses == "1"
	for _, e := range p.Root {
		ok = ok && allowed[e]
	}
	for _, seen := range p.Exists {
		ok = ok && !seen
	}
	ok = ok && len(p.Exists) > 0 && !p.Writable["/"] && !p.Writable["/lib"] && !p.Writable["/plugin"] && !p.Writable["/host"] &&
		p.Writable["/tmp"] && p.TmpWrite
	// Only its own process (and the helper, where /proc shows it)
	ok = ok && len(p.Procs) >= 1 && len(p.Procs) <= 2
	for _, there := range p.Functions {
		ok = ok && !there
	}
	return ok && len(p.Functions) > 0 && !p.Direct && p.RelayHTTP == probeBody && rs.Probe && rs.Connect &&
		!p.BasedirRoot && !p.BasedirLoosen
}

// HelperArg is the hidden subcommand dcd runs itself with as the sandbox's
// first process (see Helper).
const HelperArg = "__phpbox"

// IsHelper reports whether this process was started as the helper.
func IsHelper() bool { return len(os.Args) == 2 && os.Args[1] == HelperArg }
