package dl

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
)

// sidecar runs dc-dl, the libcurl transfer engine (see dcdl/PROTOCOL.md):
// one unix socket connection per transfer.
type sidecar struct {
	bin     string
	dataDir string

	mu      sync.Mutex
	cmd     *exec.Cmd
	exited  chan struct{}
	info    *versionInfo
	lastTry time.Time
}

type versionInfo struct {
	DCDL      string   `json:"dcdl"`
	Curl      string   `json:"curl"`
	SSL       string   `json:"ssl"`
	LibSSH    string   `json:"libssh"`
	HTTP2     bool     `json:"http2"`
	Protocols []string `json:"protocols"`
}

func (s *sidecar) sockPath() string { return filepath.Join(s.dataDir, "run", "dc-dl.sock") }
func (s *sidecar) pidPath() string  { return filepath.Join(s.dataDir, "run", "dc-dl.pid") }

// available reports whether the dc-dl binary is installed.
func (s *sidecar) available() bool {
	st, err := os.Stat(s.bin)
	return err == nil && st.Mode().IsRegular() && st.Mode()&0111 != 0
}

func (s *sidecar) running() bool {
	return s.cmd != nil && s.exited != nil && !isClosed(s.exited)
}

func isClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// versions returns what dc-dl reported at start (nil when not running).
func (s *sidecar) versions() *versionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running() {
		return nil
	}
	return s.info
}

// ensure starts dc-dl unless it runs. Failed starts are retried at most
// every two seconds.
func (s *sidecar) ensure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running() {
		return nil
	}
	if !s.available() {
		return errors.New("dc-dl is not installed")
	}
	if time.Since(s.lastTry) < 2*time.Second {
		return errors.New("dc-dl is not running")
	}
	s.lastTry = time.Now()
	return s.spawn()
}

func (s *sidecar) spawn() error {
	os.MkdirAll(filepath.Join(s.dataDir, "run"), 0700)
	os.MkdirAll(filepath.Join(s.dataDir, "logs"), 0700)
	s.killStale()
	os.Remove(s.sockPath())
	cmd := exec.Command(s.bin, "--socket", s.sockPath(), "--known-hosts", filepath.Join(s.dataDir, "ssh_known_hosts"))
	cmd.Dir = s.dataDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	lf, _ := os.OpenFile(filepath.Join(s.dataDir, "logs", "dc-dl.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if lf != nil {
		if st, err := lf.Stat(); err == nil && st.Size() > 4<<20 {
			lf.Truncate(0)
		}
		cmd.Stdout, cmd.Stderr = lf, lf
	}
	if err := cmd.Start(); err != nil {
		if lf != nil {
			lf.Close()
		}
		return fmt.Errorf("dc-dl: start: %w", err)
	}
	exited := make(chan struct{})
	s.cmd, s.exited, s.info = cmd, exited, nil
	os.WriteFile(s.pidPath(), []byte(strconv.Itoa(cmd.Process.Pid)), 0600)
	go func() {
		cmd.Wait()
		if lf != nil {
			lf.Close()
		}
		close(exited)
	}()
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		if isClosed(exited) {
			return errors.New("dc-dl exited at start, see logs/dc-dl.log")
		}
		if v, err := s.queryVersion(); err == nil {
			s.info = v
			log.Printf("url engine: started dc-dl %s (curl %s, pid %d)", v.DCDL, v.Curl, cmd.Process.Pid)
			return nil
		}
	}
	return errors.New("dc-dl did not answer")
}

// killStale stops a dc-dl left by an earlier dcd.
func (s *sidecar) killStale() {
	b, err := os.ReadFile(s.pidPath())
	if err != nil {
		return
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid <= 1 {
		return
	}
	if cmdline, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); strings.Contains(string(cmdline), "dc-dl") {
		syscall.Kill(pid, syscall.SIGTERM)
		for i := 0; i < 30; i++ {
			if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err != nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		syscall.Kill(pid, syscall.SIGKILL)
	}
	os.Remove(s.pidPath())
}

func (s *sidecar) queryVersion() (*versionInfo, error) {
	c, err := net.DialTimeout("unix", s.sockPath(), time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.WriteString(c, `{"cmd":"version"}`+"\n"); err != nil {
		return nil, err
	}
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var v versionInfo
	if err := json.Unmarshal(line, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// stop ends dc-dl.
func (s *sidecar) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running() {
		s.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-s.exited:
		case <-time.After(3 * time.Second):
			s.cmd.Process.Kill()
		}
	}
	os.Remove(s.pidPath())
}

// xreq is one transfer request.
type xreq struct {
	URL     string   `json:"url"`
	Range   string   `json:"range,omitempty"`
	Head    bool     `json:"head,omitempty"`
	Headers []string `json:"headers,omitempty"`
	User    string   `json:"user,omitempty"`
	Pass    string   `json:"pass,omitempty"`
	Proxy   string   `json:"proxy,omitempty"`
	Guard   bool     `json:"guard,omitempty"`
}

// hframe is what dc-dl knows about the response before the data.
type hframe struct {
	Status   int      `json:"status"`
	Headers  []string `json:"headers"`
	URL      string   `json:"url"`
	Length   int64    `json:"length"`
	Filetime int64    `json:"filetime"`
}

// header returns the value of a response header (case-insensitive).
func (h *hframe) header(name string) string {
	for _, l := range h.Headers {
		if k, v, ok := strings.Cut(l, ":"); ok && strings.EqualFold(strings.TrimSpace(k), name) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

type eframe struct {
	Code           int    `json:"code"`
	Error          string `json:"error"`
	Blocked        bool   `json:"blocked"`
	HostKeyChanged bool   `json:"hostkey_changed"`
}

// xfer reads the frames of one transfer; its Read returns the data, then
// io.EOF or the transfer's error.
type xfer struct {
	conn net.Conn
	r    *bufio.Reader
	h    hframe
	left int
	end  *eframe
	peek byte // frame type read ahead by open (0 = none)
	plen int
}

// transfer opens a connection to dc-dl, sends r and reads the H frame.
// When the next frame is already the end (no data) it is read too.
func (s *sidecar) transfer(ctx context.Context, r xreq) (*xfer, context.CancelFunc, error) {
	if err := s.ensure(); err != nil {
		return nil, nil, &dlError{code: engineDownCode, msg: err.Error(), transient: true}
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", s.sockPath())
	if err != nil {
		return nil, nil, &dlError{code: engineDownCode, msg: "dc-dl: " + err.Error(), transient: true}
	}
	rctx, cancel := context.WithCancel(ctx)
	go func() {
		<-rctx.Done()
		c.Close()
	}()
	b, _ := json.Marshal(r)
	if _, err := c.Write(append(b, '\n')); err != nil {
		cancel()
		return nil, nil, &dlError{code: engineDownCode, msg: "dc-dl: " + err.Error(), transient: true}
	}
	x := &xfer{conn: c, r: bufio.NewReaderSize(c, 128<<10)}
	typ, n, err := x.frame()
	if err == nil && typ != 'H' {
		err = errors.New("unexpected frame")
	}
	if err == nil {
		err = x.readJSON(n, &x.h)
	}
	if err == nil {
		// Peek: an E right away means no data (or an error)
		x.peek, x.plen, err = x.frame()
		if err == nil && x.peek == 'E' {
			var e eframe
			err = x.readJSON(x.plen, &e)
			x.end, x.peek = &e, 0
		}
	}
	if err != nil {
		cancel()
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, &dlError{code: engineDownCode, msg: "dc-dl: " + err.Error(), transient: true}
	}
	if x.end != nil && x.end.Code != 0 {
		cancel()
		return nil, nil, x.end.err()
	}
	return x, cancel, nil
}

func (x *xfer) frame() (byte, int, error) {
	var h [5]byte
	if _, err := io.ReadFull(x.r, h[:]); err != nil {
		return 0, 0, err
	}
	return h[0], int(binary.BigEndian.Uint32(h[1:])), nil
}

func (x *xfer) readJSON(n int, v any) error {
	if n > 1<<20 {
		return errors.New("frame too large")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(x.r, b); err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func (x *xfer) Read(p []byte) (int, error) {
	for x.left == 0 {
		if x.end != nil {
			if x.end.Code != 0 {
				return 0, x.end.err()
			}
			return 0, io.EOF
		}
		typ, n := x.peek, x.plen
		if typ == 0 {
			var err error
			if typ, n, err = x.frame(); err != nil {
				return 0, &dlError{code: engineDownCode, msg: "dc-dl closed the connection", transient: true}
			}
		}
		x.peek = 0
		switch typ {
		case 'D':
			x.left = n
		case 'E':
			var e eframe
			if err := x.readJSON(n, &e); err != nil {
				return 0, &dlError{code: engineDownCode, msg: "dc-dl: " + err.Error(), transient: true}
			}
			x.end = &e
		default:
			if _, err := io.CopyN(io.Discard, x.r, int64(n)); err != nil {
				return 0, &dlError{code: engineDownCode, msg: "dc-dl closed the connection", transient: true}
			}
		}
	}
	if len(p) > x.left {
		p = p[:x.left]
	}
	n, err := x.r.Read(p)
	x.left -= n
	if err != nil {
		return n, &dlError{code: engineDownCode, msg: "dc-dl closed the connection", transient: true}
	}
	return n, nil
}

func (x *xfer) Close() error { return x.conn.Close() }

// err maps the end of a failed transfer to the engine's error codes.
func (e *eframe) err() *dlError {
	switch {
	case e.Blocked:
		return &dlError{code: engineBlockedCode, msg: "address is not allowed"}
	case e.HostKeyChanged:
		return &dlError{code: engineHostKeyCode, msg: e.Error}
	}
	return curlError(e.Code, e.Error)
}
