package dl

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"downloadcenter/internal/engine"
)

// The engine runs as root and the task's folder may be one a regular user
// could change (tasks from before the folder moved out of the user's home):
// nothing in it is followed when it is a link, and nothing a server or a
// file there says can end dcd. The tests run as root: they check targets
// and contents themselves.

// victimFile is a file outside the task's folder that must not change.
func victimFile(t *testing.T) (string, []byte) {
	t.Helper()
	want := []byte("ORIGINAL CONTENT\n")
	p := filepath.Join(t.TempDir(), "target.conf")
	if err := os.WriteFile(p, want, 0600); err != nil {
		t.Fatal(err)
	}
	return p, want
}

func unchanged(t *testing.T, p string, want []byte) {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Lstat(p, &st); err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	got, _ := os.ReadFile(p)
	if !bytes.Equal(got, want) || st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		t.Fatalf("%s changed: %q", p, got)
	}
}

func TestWorkDirLink(t *testing.T) {
	srv := httptest.NewServer(&fileServer{data: []byte("ATTACKER CONTENT\n")})
	defer srv.Close()
	e := newEngine(t)
	victim, want := victimFile(t)
	work := filepath.Join(t.TempDir(), "0123hash")
	os.Symlink(filepath.Dir(victim), work)
	os.Lchown(work, 54321, 54321) // made by a regular user
	e.Add("wl", engine.AddRequest{URIs: []string{srv.URL + "/x"}, Dir: work, Out: "target.conf"})
	st := waitFor(t, e, "wl", engine.Error, 10*time.Second)
	unchanged(t, victim, want)
	if st.ErrorCode != engine.ErrCreateDir {
		t.Fatalf("error %s %s", st.ErrorCode, st.ErrorMsg)
	}
}

func TestDataFileLink(t *testing.T) {
	srv := httptest.NewServer(&fileServer{data: []byte("ATTACKER CONTENT\n")})
	defer srv.Close()
	e := newEngine(t)
	for i, link := range []func(target, at string) error{os.Symlink, os.Link} {
		victim, want := victimFile(t)
		work := t.TempDir()
		if err := link(victim, filepath.Join(work, "movie.mp4")); err != nil {
			t.Fatal(err)
		}
		ref := fmt.Sprint("dl", i)
		e.Add(ref, engine.AddRequest{URIs: []string{srv.URL + "/movie.mp4"}, Dir: work})
		waitFor(t, e, ref, engine.Error, 10*time.Second)
		unchanged(t, victim, want)
	}
}

// The control file is written under a temporary name and renamed: a link
// in either place is replaced, never written through.
func TestControlFileLink(t *testing.T) {
	fs := &fileServer{data: randomData(t, 6<<20), etag: `"c"`, ranges: true, delay: 15 * time.Millisecond}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	work := t.TempDir()
	v1, w1 := victimFile(t)
	v2, w2 := victimFile(t)
	os.Symlink(v1, filepath.Join(work, ".c.bin.dcdl.tmp"))
	os.Symlink(v2, filepath.Join(work, ".c.bin.dcdl"))
	e := rawEngine(t)
	e.Add("cl", engine.AddRequest{URIs: []string{srv.URL + "/c.bin"}, Dir: work})
	for {
		st, _ := e.Status("cl")
		if st.Completed > 1<<20 || st.State == engine.Error {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.Stop()
	unchanged(t, v1, w1)
	unchanged(t, v2, w2)
	var st syscall.Stat_t
	if err := syscall.Lstat(controlPath(work, "c.bin"), &st); err != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		t.Fatalf("control file: %v %o", err, st.Mode)
	}
}

// A server that reports an absurd size: an error of the task, not a crash.
func TestImpossibleSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-0/9223372036854775807")
		w.Header().Set("Content-Length", "1")
		w.WriteHeader(206)
		w.Write([]byte("x"))
	}))
	defer srv.Close()
	e := newEngine(t)
	e.Add("big", engine.AddRequest{URIs: []string{srv.URL + "/big"}, Dir: t.TempDir()})
	st := waitFor(t, e, "big", engine.Error, 10*time.Second)
	if st.ErrorCode != engine.ErrBadResponse {
		t.Fatalf("error %s %s", st.ErrorCode, st.ErrorMsg)
	}
}

// Control files with numbers that do not add up are not taken over, and
// do not end dcd.
func TestBadControlFile(t *testing.T) {
	data := randomData(t, 1<<20)
	srv := httptest.NewServer(&fileServer{data: data, ranges: true})
	defer srv.Close()
	work := t.TempDir()
	os.WriteFile(filepath.Join(work, "f.bin"), make([]byte, 10), 0644)
	for _, c := range []control{
		{V: 1, Ref: "bc", Name: "f.bin", Size: 1<<63 - 1, Piece: 1 << 62, Bits: "ff"},
		{V: 1, Ref: "bc", Name: "f.bin", Size: 1<<63 - 1, Piece: 1 << 62, Bits: ""},
		{V: 1, Ref: "bc", Name: "f.bin", Size: 1 << 40, Piece: 1, Bits: "ff"},
	} {
		b, _ := json.Marshal(c)
		os.WriteFile(controlPath(work, "f.bin"), b, 0600)
		e := rawEngine(t)
		e.Add("bc", engine.AddRequest{URIs: []string{srv.URL + "/f.bin"}, Dir: work, Paused: true})
		if st, _ := e.Status("bc"); st.Completed != 0 || st.Total != 0 {
			t.Fatalf("taken over: %+v", st)
		}
		e.Stop()
	}
	// An aria2 control file whose bitfield length runs past its end
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, uint16(1))
	binary.Write(&b, binary.BigEndian, uint32(0))
	binary.Write(&b, binary.BigEndian, uint32(0))
	binary.Write(&b, binary.BigEndian, uint32(1<<20))
	binary.Write(&b, binary.BigEndian, uint64(1<<20))
	binary.Write(&b, binary.BigEndian, uint64(0))
	binary.Write(&b, binary.BigEndian, uint32(0xFFFFFFF0))
	if _, _, _, err := parseAria2Control(b.Bytes()); err == nil {
		t.Fatal("bitfield past the end accepted")
	}
}

// startDCDL runs a dc-dl of its own for raw requests.
func startDCDL(t *testing.T) *sidecar {
	dir, err := os.MkdirTemp("", "dl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	s := &sidecar{bin: dcdlBin(t), dataDir: dir}
	if err := s.ensure(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.stop)
	return s
}

// rawRequest sends one request line to dc-dl and returns the frames' types
// and the last frame's payload.
func rawRequest(t *testing.T, s *sidecar, line string) (string, []byte) {
	c, err := net.Dial("unix", s.sockPath())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(20 * time.Second))
	io.WriteString(c, line+"\n")
	r := bufio.NewReader(c)
	var types string
	var last []byte
	for {
		var h [5]byte
		if _, err := io.ReadFull(r, h[:]); err != nil {
			return types, last
		}
		last = make([]byte, binary.BigEndian.Uint32(h[1:]))
		io.ReadFull(r, last)
		types += string(h[0])
	}
}

// Fields of the wrong type, and a range that would add a header line, are
// refused; dc-dl keeps running.
func TestSidecarBadRequests(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("X-Injected"))
		mu.Unlock()
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	s := startDCDL(t)
	for _, line := range []string{
		`{"url":"` + srv.URL + `/a","guard":"yes"}`,
		`{"url":"` + srv.URL + `/a","head":1}`,
		`{"url":"` + srv.URL + `/a","range":"0-1\r\nX-Injected: 1"}`,
		`["not", "an", "object"]`,
	} {
		types, last := rawRequest(t, s, line)
		if !strings.HasSuffix(types, "E") {
			t.Fatalf("%s: frames %q", line, types)
		}
		var e eframe
		json.Unmarshal(last, &e)
		t.Logf("%s: %q %d %s", line, types, e.Code, e.Error)
		if _, err := s.queryVersion(); err != nil {
			t.Fatalf("dc-dl stopped after %s: %v", line, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, h := range seen {
		if h != "" {
			t.Fatal("a header line went out through the range")
		}
	}
}

// A connection libcurl kept from a transfer without the guard must not
// carry a guarded one.
func TestGuardPooledConnection(t *testing.T) {
	var mu sync.Mutex
	var from []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		from = append(from, r.URL.Path+" "+r.RemoteAddr)
		mu.Unlock()
		w.Write([]byte("secret of " + r.URL.Path))
	}))
	defer srv.Close()
	s := startDCDL(t)
	get := func(path string, guard bool) eframe {
		b, _ := json.Marshal(xreq{URL: srv.URL + path, Guard: guard})
		_, last := rawRequest(t, s, string(b))
		var e eframe
		json.Unmarshal(last, &e)
		return e
	}
	if e := get("/t1", true); !e.Blocked {
		t.Fatalf("guarded request on a new connection: %+v", e)
	}
	if e := get("/t2a", false); e.Code != 0 {
		t.Fatalf("unguarded request: %+v", e)
	}
	if e := get("/t2b", true); !e.Blocked {
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("guarded request went out on the kept connection: %+v %q", e, from)
	}
}

// fakeProxy is an HTTP proxy for plain http that relays to whatever it is
// asked for, and records the targets.
func fakeProxy(t *testing.T) (*httptest.Server, func() []string) {
	var mu sync.Mutex
	var seen []string
	px := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.String())
		mu.Unlock()
		if r.URL.Host == "203.0.113.10" {
			http.Redirect(w, r, r.Header.Get("X-Next"), http.StatusFound)
			return
		}
		out, _ := http.NewRequest(r.Method, r.URL.String(), nil)
		for k, v := range r.Header {
			out.Header[k] = v
		}
		resp, err := http.DefaultTransport.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	t.Cleanup(px.Close)
	return px, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// Through a proxy, a guarded download may not be sent on to the NAS itself
// by a redirect: every address it is sent to is checked.
func TestGuardProxyRedirect(t *testing.T) {
	secret := httptest.NewServer(&fileServer{data: []byte("SECRET-ONLY-ON-LOOPBACK")})
	defer secret.Close()
	px, seen := fakeProxy(t)
	e := newEngine(t)
	dir := t.TempDir()
	e.Add("pr", engine.AddRequest{URIs: []string{"http://203.0.113.10/file.bin"}, Dir: dir, Guard: true,
		Proxy: px.URL, Headers: []string{"X-Next: " + secret.URL + "/secret.bin"}})
	st := waitFor(t, e, "pr", engine.Error, 20*time.Second)
	for _, u := range seen() {
		if strings.Contains(u, "127.0.0.1") {
			t.Fatalf("the proxy was sent to %s", u)
		}
	}
	if !strings.Contains(st.ErrorMsg, "not allowed") {
		t.Fatalf("error %s %s", st.ErrorCode, st.ErrorMsg)
	}
	if ents, _ := os.ReadDir(dir); len(ents) > 0 {
		t.Fatalf("written: %v", ents)
	}
}

// Headers the task added go to the host it names, not to another one a
// redirect points at; a redirect on the same host keeps them.
func TestRedirectHeaders(t *testing.T) {
	var mu sync.Mutex
	got := map[string]string{}
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got[r.URL.Path] = r.Header.Get("X-Token") + "|" + r.Header.Get("Cookie") + "|" + r.Header.Get("Authorization") + "|" + r.Header.Get("Accept-Encoding")
		mu.Unlock()
		w.Write([]byte("data"))
	}))
	defer b.Close()
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got[r.URL.Path] = r.Header.Get("X-Token")
		mu.Unlock()
		switch r.URL.Path {
		case "/same":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			w.Write([]byte("data"))
		default:
			http.Redirect(w, r, b.URL+"/other", http.StatusFound)
		}
	}))
	defer a.Close()
	e := newEngine(t)
	hs := []string{"X-Token: SECRET", "Cookie: s=1"}
	e.Add("r1", engine.AddRequest{URIs: []string{a.URL + "/away"}, Dir: t.TempDir(), Headers: hs, User: "u", Pass: "p"})
	waitFor(t, e, "r1", engine.Complete, 10*time.Second)
	e.Add("r2", engine.AddRequest{URIs: []string{a.URL + "/same"}, Dir: t.TempDir(), Headers: hs})
	waitFor(t, e, "r2", engine.Complete, 10*time.Second)
	mu.Lock()
	defer mu.Unlock()
	if got["/away"] != "SECRET" || got["/other"] != "|||identity" || got["/final"] != "SECRET" {
		t.Fatalf("headers seen %q", got)
	}
}

// A server that takes the connection and never answers: the task fails
// (and is retried by the core) instead of waiting forever.
func TestNoAnswer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	old := headerTimeout
	headerTimeout = 2 * time.Second
	defer func() { headerTimeout = old }()
	e := newEngine(t)
	e.Add("na", engine.AddRequest{URIs: []string{"http://" + ln.Addr().String() + "/x"}, Dir: t.TempDir()})
	st := waitFor(t, e, "na", engine.Error, 20*time.Second)
	if st.ErrorCode != engine.ErrTimeout {
		t.Fatalf("error %s %s", st.ErrorCode, st.ErrorMsg)
	}
}

// Response headers that would make dc-dl's first frame larger than dcd
// reads (control characters are escaped in it) are cut by dc-dl.
func TestHugeHeaders(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				bufio.NewReader(c).ReadString('\n')
				var w bytes.Buffer
				w.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 4\r\n")
				for i := 0; i < 40; i++ {
					fmt.Fprintf(&w, "X-%d: %s\r\n", i, strings.Repeat("\x01", 6000))
				}
				w.WriteString("\r\ndata")
				c.Write(w.Bytes())
			}(c)
		}
	}()
	e := newEngine(t)
	dir := t.TempDir()
	e.Add("hh", engine.AddRequest{URIs: []string{"http://" + ln.Addr().String() + "/h.bin"}, Dir: dir})
	waitFor(t, e, "hh", engine.Complete, 20*time.Second)
	checkFile(t, filepath.Join(dir, "h.bin"), []byte("data"))
}

// The task's folder may be named through a link only root can have made
// (as /share/homes is), not through one in a folder others can write to.
func TestWorkDirTrustedLink(t *testing.T) {
	srv := httptest.NewServer(&fileServer{data: []byte("data")})
	defer srv.Close()
	e := newEngine(t)
	for i, c := range []struct {
		mode os.FileMode
		ok   bool
	}{{0755, true}, {0755 | os.ModeSticky, true}, {0777, false}} {
		base := t.TempDir()
		os.Mkdir(filepath.Join(base, "real"), 0700)
		links := filepath.Join(base, "links")
		os.Mkdir(links, 0755)
		os.Chmod(links, c.mode)
		os.Symlink("../real", filepath.Join(links, "work"))
		ref := fmt.Sprint("tl", i)
		e.Add(ref, engine.AddRequest{URIs: []string{srv.URL + "/f.bin"}, Dir: filepath.Join(links, "work")})
		if c.ok {
			waitFor(t, e, ref, engine.Complete, 10*time.Second)
			checkFile(t, filepath.Join(base, "real", "f.bin"), []byte("data"))
		} else if st := waitFor(t, e, ref, engine.Error, 10*time.Second); st.ErrorCode != engine.ErrCreateDir {
			t.Fatalf("%v: %s %s", c.mode, st.ErrorCode, st.ErrorMsg)
		}
	}
}
