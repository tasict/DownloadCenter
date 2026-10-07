package dl

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"downloadcenter/internal/engine"
)

// fileServer serves data with optional byte ranges, counting connections.
type fileServer struct {
	data      []byte
	etag      string
	ranges    bool
	noLength  bool
	status    int           // fixed status to answer with (0 = serve)
	page      bool          // answer every request with a web page
	delay     time.Duration // per 64 KiB written
	mu        sync.Mutex
	active    int
	maxActive int
	ranged    []string
}

func (s *fileServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.active++
	s.maxActive = max(s.maxActive, s.active)
	if rg := r.Header.Get("Range"); rg != "" && rg != "bytes=0-" {
		s.ranged = append(s.ranged, rg)
	}
	data, etag, status, page := s.data, s.etag, s.status, s.page
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	if page {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, "<html><title>Quota exceeded</title></html>")
		return
	}
	if r.Header.Get("Accept-Encoding") != "identity" {
		http.Error(w, "expected identity encoding", 400)
		return
	}
	start, end := int64(0), int64(len(data))
	partial := false
	if rg := r.Header.Get("Range"); s.ranges && rg != "" {
		if ir := r.Header.Get("If-Range"); ir == "" || ir == etag {
			spec := strings.TrimPrefix(rg, "bytes=")
			a, b, _ := strings.Cut(spec, "-")
			start, _ = strconv.ParseInt(a, 10, 64)
			if b != "" {
				e, _ := strconv.ParseInt(b, 10, 64)
				end = e + 1
			}
			partial = true
		}
	}
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	w.Header().Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")
	if s.ranges {
		w.Header().Set("Accept-Ranges", "bytes")
	}
	if !s.noLength {
		w.Header().Set("Content-Length", strconv.FormatInt(end-start, 10))
	}
	if partial {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end-1, len(data)))
		w.WriteHeader(206)
	}
	chunk := data[start:end]
	for len(chunk) > 0 {
		n := min(len(chunk), 64<<10)
		if _, err := w.Write(chunk[:n]); err != nil {
			return
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		chunk = chunk[n:]
		if s.delay > 0 {
			time.Sleep(s.delay)
		}
	}
}

func randomData(t *testing.T, n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func waitFor(t *testing.T, e *Engine, ref string, want engine.State, d time.Duration) *engine.Status {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		st, err := e.Status(ref)
		if err != nil {
			t.Fatal(err)
		}
		if st.State == want {
			return st
		}
		if st.State == engine.Error && want != engine.Error {
			t.Fatalf("task failed: %s %s", st.ErrorCode, st.ErrorMsg)
		}
		time.Sleep(20 * time.Millisecond)
	}
	st, _ := e.Status(ref)
	t.Fatalf("timeout waiting for %s, state %s (%d/%d)", want, st.State, st.Completed, st.Total)
	return nil
}

// dcdlBin is the dc-dl the tests run against: $DC_DL, or the one
// tools/build-dcdl.sh built for this machine.
func dcdlBin(t *testing.T) string {
	b := os.Getenv("DC_DL")
	if b == "" {
		arch := map[string]string{"amd64": "x86_64", "arm64": "arm_64", "arm": "arm-x41"}[runtime.GOARCH]
		b, _ = filepath.Abs("../../../../tools/cache/dc-dl-" + arch)
	}
	if _, err := os.Stat(b); err != nil {
		t.Skip("dc-dl is not built (sh tools/build-dcdl.sh)")
	}
	return b
}

// rawEngine starts an engine with its own dc-dl; the caller stops it. The
// data folder is short: a unix socket path has a length limit.
func rawEngine(t *testing.T) *Engine {
	dir, err := os.MkdirTemp("", "dl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	e := New(dcdlBin(t), dir)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	return e
}

func newEngine(t *testing.T) *Engine {
	e := rawEngine(t)
	t.Cleanup(func() { e.Stop() })
	return e
}

func checkFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s: content differs (%d vs %d bytes)", path, len(got), len(want))
	}
}

func TestSegmented(t *testing.T) {
	fs := &fileServer{data: randomData(t, 20<<20), etag: `"a"`, ranges: true, delay: time.Millisecond}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	e := newEngine(t)
	dir := t.TempDir()
	ref, err := e.Add("h1", engine.AddRequest{URIs: []string{srv.URL + "/files/big.bin"}, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	st := waitFor(t, e, ref, engine.Complete, 30*time.Second)
	if st.Name != "big.bin" || st.Total != int64(len(fs.data)) || st.Completed != st.Total {
		t.Fatalf("status %+v", st)
	}
	checkFile(t, filepath.Join(dir, "big.bin"), fs.data)
	if fs.maxActive < 2 {
		t.Fatalf("expected several connections, saw %d", fs.maxActive)
	}
	if _, err := os.Stat(controlPath(dir, "big.bin")); !os.IsNotExist(err) {
		t.Fatal("control file left behind")
	}
	if fi, _ := os.Stat(filepath.Join(dir, "big.bin")); fi.ModTime().Year() != 2006 {
		t.Fatal("modification time not taken from the server")
	}
}

func TestNoRanges(t *testing.T) {
	fs := &fileServer{data: randomData(t, 10<<20)}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	e := newEngine(t)
	dir := t.TempDir()
	e.Add("h2", engine.AddRequest{URIs: []string{srv.URL + "/a.iso"}, Dir: dir, Out: "renamed.iso"})
	waitFor(t, e, "h2", engine.Complete, 30*time.Second)
	checkFile(t, filepath.Join(dir, "renamed.iso"), fs.data)
	if fs.maxActive != 1 {
		t.Fatalf("expected one connection, saw %d", fs.maxActive)
	}
}

func TestUnknownLength(t *testing.T) {
	fs := &fileServer{data: randomData(t, 3<<20), noLength: true}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	e := newEngine(t)
	dir := t.TempDir()
	e.Add("h3", engine.AddRequest{URIs: []string{srv.URL + "/stream"}, Dir: dir})
	st := waitFor(t, e, "h3", engine.Complete, 30*time.Second)
	checkFile(t, filepath.Join(dir, "stream"), fs.data)
	if st.Total != int64(len(fs.data)) {
		t.Fatalf("total %d", st.Total)
	}
}

func TestContentDisposition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename*=UTF-8''%E4%B8%AD%E6%96%87.txt; filename="x.txt"`)
		io.WriteString(w, "hello")
	}))
	defer srv.Close()
	e := newEngine(t)
	dir := t.TempDir()
	e.Add("h4", engine.AddRequest{URIs: []string{srv.URL + "/download?id=1"}, Dir: dir})
	st := waitFor(t, e, "h4", engine.Complete, 10*time.Second)
	if st.Name != "中文.txt" {
		t.Fatalf("name %q", st.Name)
	}
	checkFile(t, filepath.Join(dir, "中文.txt"), []byte("hello"))
}

func TestPauseResumeAcrossEngines(t *testing.T) {
	fs := &fileServer{data: randomData(t, 12<<20), etag: `"v1"`, ranges: true, delay: 15 * time.Millisecond}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	dir := t.TempDir()
	e := rawEngine(t)
	e.Add("h5", engine.AddRequest{URIs: []string{srv.URL + "/r.bin"}, Dir: dir})
	for {
		st, _ := e.Status("h5")
		if st.Completed > 3<<20 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.Pause("h5")
	before, _ := e.Status("h5")
	e.Stop()
	if _, err := os.Stat(controlPath(dir, "r.bin")); err != nil {
		t.Fatal("no control file after pause")
	}
	fs.mu.Lock()
	fs.ranged, fs.delay = nil, 0
	fs.mu.Unlock()
	// A new engine (dcd restart) picks the download up from the control file
	e2 := newEngine(t)
	e2.Add("h5", engine.AddRequest{URIs: []string{srv.URL + "/r.bin"}, Dir: dir, Paused: true})
	st, _ := e2.Status("h5")
	if st.Completed == 0 || st.Completed > before.Completed {
		t.Fatalf("resumed progress %d, before %d", st.Completed, before.Completed)
	}
	e2.Resume("h5")
	waitFor(t, e2, "h5", engine.Complete, 30*time.Second)
	checkFile(t, filepath.Join(dir, "r.bin"), fs.data)
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for _, rg := range fs.ranged {
		if strings.HasPrefix(rg, "bytes=0-") {
			t.Fatalf("downloaded from zero again: %v", fs.ranged)
		}
	}
}

func TestChangedOnServer(t *testing.T) {
	fs := &fileServer{data: randomData(t, 6<<20), etag: `"old"`, ranges: true, delay: 15 * time.Millisecond}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	dir := t.TempDir()
	e := rawEngine(t)
	e.Add("h6", engine.AddRequest{URIs: []string{srv.URL + "/c.bin"}, Dir: dir})
	for {
		st, _ := e.Status("h6")
		if st.Completed > 1<<20 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.Stop()
	var logged []string
	fs.mu.Lock()
	fs.data, fs.etag, fs.delay = randomData(t, 5<<20), `"new"`, 0
	fs.mu.Unlock()
	e2 := newEngine(t)
	e2.Logf = func(ref, msg string) { logged = append(logged, msg) }
	e2.Add("h6", engine.AddRequest{URIs: []string{srv.URL + "/c.bin"}, Dir: dir})
	waitFor(t, e2, "h6", engine.Complete, 30*time.Second)
	checkFile(t, filepath.Join(dir, "c.bin"), fs.data)
	if len(logged) == 0 {
		t.Fatal("restart not logged")
	}
}

func TestNoPages(t *testing.T) {
	// A page at the start: an error, nothing written
	srv := httptest.NewServer(&fileServer{page: true})
	defer srv.Close()
	e := newEngine(t)
	dir := t.TempDir()
	e.Add("p1", engine.AddRequest{URIs: []string{srv.URL + "/p.bin"}, Dir: dir, NoPages: true})
	st := waitFor(t, e, "p1", engine.Error, 10*time.Second)
	if st.ErrorCode != engine.ErrBadResponse {
		t.Fatalf("code %s %s", st.ErrorCode, st.ErrorMsg)
	}
	if _, err := os.Stat(filepath.Join(dir, "p.bin")); err == nil {
		t.Fatal("page written")
	}
	// Without NoPages a page is downloaded like any file
	e.Add("p2", engine.AddRequest{URIs: []string{srv.URL + "/p2.html"}, Dir: dir})
	waitFor(t, e, "p2", engine.Complete, 10*time.Second)

	// A page while resuming: an error, the data so far and its control file stay
	fs := &fileServer{data: randomData(t, 6<<20), etag: `"a"`, ranges: true, delay: 15 * time.Millisecond}
	srv2 := httptest.NewServer(fs)
	defer srv2.Close()
	dir2 := t.TempDir()
	req := engine.AddRequest{URIs: []string{srv2.URL + "/q.bin"}, Dir: dir2, NoPages: true}
	e1 := rawEngine(t)
	e1.Add("p3", req)
	for {
		st, _ := e1.Status("p3")
		if st.Completed > 1<<20 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	e1.Stop()
	fs.mu.Lock()
	fs.page = true
	fs.mu.Unlock()
	var logged []string
	e2 := newEngine(t)
	e2.Logf = func(ref, msg string) { logged = append(logged, msg) }
	e2.Add("p3", req)
	st = waitFor(t, e2, "p3", engine.Error, 10*time.Second)
	if st.ErrorCode != engine.ErrBadResponse || len(logged) > 0 {
		t.Fatalf("code %s %s, log %q", st.ErrorCode, st.ErrorMsg, logged)
	}
	got, err := os.ReadFile(filepath.Join(dir2, "q.bin"))
	if err != nil || len(got) < 64<<10 || !bytes.Equal(got[:64<<10], fs.data[:64<<10]) {
		t.Fatalf("data lost: %d bytes, %v", len(got), err)
	}
	if _, err := os.Stat(controlPath(dir2, "q.bin")); err != nil {
		t.Fatal("control file gone")
	}
}

func TestErrors(t *testing.T) {
	for _, c := range []struct {
		status int
		code   string
	}{{404, engine.ErrNotFoundURL}, {401, engine.ErrAuth}, {403, engine.ErrAuth}, {418, engine.ErrBadResponse}} {
		srv := httptest.NewServer(&fileServer{status: c.status})
		e := newEngine(t)
		ref := "e" + strconv.Itoa(c.status)
		e.Add(ref, engine.AddRequest{URIs: []string{srv.URL + "/x"}, Dir: t.TempDir()})
		st := waitFor(t, e, ref, engine.Error, 10*time.Second)
		if st.ErrorCode != c.code {
			t.Fatalf("%d: code %s %s", c.status, st.ErrorCode, st.ErrorMsg)
		}
		srv.Close()
	}
}

func TestGuard(t *testing.T) {
	srv := httptest.NewServer(&fileServer{data: []byte("x")})
	defer srv.Close()
	e := newEngine(t)
	e.Add("g", engine.AddRequest{URIs: []string{srv.URL + "/x"}, Dir: t.TempDir(), Guard: true})
	st := waitFor(t, e, "g", engine.Error, 10*time.Second)
	if !strings.Contains(st.ErrorMsg, "not allowed") {
		t.Fatalf("guard: %s %s", st.ErrorCode, st.ErrorMsg)
	}
}

func TestRateLimit(t *testing.T) {
	fs := &fileServer{data: randomData(t, 600<<10), ranges: true}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	e := newEngine(t)
	start := time.Now()
	e.Add("rl", engine.AddRequest{URIs: []string{srv.URL + "/s"}, Dir: t.TempDir(), MaxDown: 300 << 10})
	waitFor(t, e, "rl", engine.Complete, 20*time.Second)
	if d := time.Since(start); d < 1500*time.Millisecond {
		t.Fatalf("600 KiB at 300 KiB/s took %v", d)
	}
}

// aria2Control builds a version 1 aria2 control file.
func aria2Control(piece, total int64, bits []byte) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, uint16(1))
	binary.Write(&b, binary.BigEndian, uint32(0))
	binary.Write(&b, binary.BigEndian, uint32(0)) // no info hash
	binary.Write(&b, binary.BigEndian, uint32(piece))
	binary.Write(&b, binary.BigEndian, uint64(total))
	binary.Write(&b, binary.BigEndian, uint64(0))
	binary.Write(&b, binary.BigEndian, uint32(len(bits)))
	b.Write(bits)
	binary.Write(&b, binary.BigEndian, uint32(0)) // in-flight pieces
	return b.Bytes()
}

func TestAdoptAria2(t *testing.T) {
	data := randomData(t, 8<<20)
	fs := &fileServer{data: data, ranges: true}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	dir := t.TempDir()
	// The first 4 of 8 pieces were done by aria2; the rest of the file is zeros
	part := append(append([]byte{}, data[:4<<20]...), make([]byte, 4<<20)...)
	os.WriteFile(filepath.Join(dir, "old.bin"), part, 0644)
	os.WriteFile(filepath.Join(dir, "old.bin.aria2"), aria2Control(1<<20, int64(len(data)), []byte{0xF0}), 0644)
	var logged []string
	e := newEngine(t)
	e.Logf = func(ref, msg string) { logged = append(logged, msg) }
	e.Add("ad", engine.AddRequest{URIs: []string{srv.URL + "/other-name"}, Dir: dir, Paused: true})
	st, _ := e.Status("ad")
	if st.Name != "old.bin" || st.Completed != 4<<20 {
		t.Fatalf("adopted %+v", st)
	}
	e.Resume("ad")
	waitFor(t, e, "ad", engine.Complete, 30*time.Second)
	checkFile(t, filepath.Join(dir, "old.bin"), data)
	if _, err := os.Stat(filepath.Join(dir, "old.bin.aria2")); !os.IsNotExist(err) {
		t.Fatal("aria2 control file not removed")
	}
	if len(logged) != 1 {
		t.Fatalf("log %v", logged)
	}
	for _, rg := range fs.ranged {
		if strings.HasPrefix(rg, "bytes=0-") {
			t.Fatalf("fetched pieces aria2 already had: %v", fs.ranged)
		}
	}
}

func TestContinueExistingFile(t *testing.T) {
	data := randomData(t, 5<<20)
	fs := &fileServer{data: data, ranges: true}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	dir := t.TempDir()
	// An imported task: 2.5 MiB of the file, no control file
	os.WriteFile(filepath.Join(dir, "imp.bin"), data[:5<<19], 0644)
	e := newEngine(t)
	e.Add("ct", engine.AddRequest{URIs: []string{srv.URL + "/imp.bin"}, Dir: dir, Out: "imp.bin"})
	waitFor(t, e, "ct", engine.Complete, 30*time.Second)
	checkFile(t, filepath.Join(dir, "imp.bin"), data)
	for _, rg := range fs.ranged {
		if strings.HasPrefix(rg, "bytes=0-") || strings.HasPrefix(rg, "bytes=1048576-") {
			t.Fatalf("fetched what was already there: %v", fs.ranged)
		}
	}
}

// --- FTP through the system curl ---

type ftpServer struct {
	ln    net.Listener
	data  []byte
	user  string
	pass  string
	rests []int64
	mu    sync.Mutex
}

func startFTP(t *testing.T, data []byte, user, pass string) *ftpServer {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &ftpServer{ln: ln, data: data, user: user, pass: pass}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *ftpServer) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	reply := func(f string, a ...any) { fmt.Fprintf(c, f+"\r\n", a...) }
	reply("220 test")
	var rest int64
	var pasv net.Listener
	authed, gotUser := false, ""
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd, arg, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch strings.ToUpper(cmd) {
		case "USER":
			gotUser = arg
			reply("331 password")
		case "PASS":
			if gotUser == s.user && arg == s.pass {
				authed = true
				reply("230 ok")
			} else {
				reply("530 login incorrect")
			}
		case "PWD":
			reply(`257 "/"`)
		case "CWD":
			reply("250 ok")
		case "TYPE":
			reply("200 ok")
		case "SIZE":
			if !authed || arg != "file.bin" {
				reply("550 no")
			} else {
				reply("213 %d", len(s.data))
			}
		case "MDTM":
			reply("213 20200102030405")
		case "REST":
			rest, _ = strconv.ParseInt(arg, 10, 64)
			s.mu.Lock()
			s.rests = append(s.rests, rest)
			s.mu.Unlock()
			reply("350 ok")
		case "EPSV", "PASV":
			pasv, _ = net.Listen("tcp", "127.0.0.1:0")
			port := pasv.Addr().(*net.TCPAddr).Port
			if strings.ToUpper(cmd) == "EPSV" {
				reply("229 Entering Extended Passive Mode (|||%d|)", port)
			} else {
				reply("227 Entering Passive Mode (127,0,0,1,%d,%d)", port/256, port%256)
			}
		case "RETR":
			if !authed || arg != "file.bin" || pasv == nil {
				reply("550 no")
				continue
			}
			reply("150 sending")
			dc, err := pasv.Accept()
			pasv.Close()
			pasv = nil
			if err != nil {
				return
			}
			dc.Write(s.data[rest:])
			dc.Close()
			rest = 0
			reply("226 done")
		case "QUIT":
			reply("221 bye")
			return
		case "ABOR":
			reply("226 aborted")
		default:
			reply("502 not implemented")
		}
	}
}

func TestFTP(t *testing.T) {
	e := newEngine(t)
	if !e.Caps().FTP {
		t.Fatal("dc-dl without FTP")
	}
	data := randomData(t, 3<<20)
	s := startFTP(t, data, "bob", `p"a\ss`)
	url := fmt.Sprintf("ftp://%s/file.bin", s.ln.Addr())
	// The probe learns size and date (libcurl reports them as body lines)
	e.Add("f0", engine.AddRequest{URIs: []string{url}, Dir: t.TempDir(), User: "bob", Pass: `p"a\ss`, Paused: true})
	if _, inf, err := e.get("f0").probe(context.Background()); err != nil || inf.size != int64(len(data)) || !inf.ranges || inf.modified == "" {
		t.Fatalf("probe: %+v %v", inf, err)
	}
	dir := t.TempDir()
	e.Add("f1", engine.AddRequest{URIs: []string{url}, Dir: dir, User: "bob", Pass: `p"a\ss`})
	st := waitFor(t, e, "f1", engine.Complete, 30*time.Second)
	if st.Total != int64(len(data)) {
		t.Fatalf("total %d", st.Total)
	}
	checkFile(t, filepath.Join(dir, "file.bin"), data)

	// Resume from a control file with the first half done
	dir2 := t.TempDir()
	os.WriteFile(filepath.Join(dir2, "file.bin"), append(append([]byte{}, data[:2<<20]...), make([]byte, 1<<20)...), 0644)
	os.WriteFile(filepath.Join(dir2, "file.bin.aria2"), aria2Control(1<<20, int64(len(data)), []byte{0xC0}), 0644)
	e.Add("f2", engine.AddRequest{URIs: []string{url}, Dir: dir2, User: "bob", Pass: `p"a\ss`})
	waitFor(t, e, "f2", engine.Complete, 30*time.Second)
	checkFile(t, filepath.Join(dir2, "file.bin"), data)
	s.mu.Lock()
	rests := append([]int64{}, s.rests...)
	s.mu.Unlock()
	if len(rests) == 0 || rests[len(rests)-1] != 2<<20 {
		t.Fatalf("REST offsets %v", rests)
	}

	// Wrong password
	e.Add("f3", engine.AddRequest{URIs: []string{url}, Dir: t.TempDir(), User: "bob", Pass: "nope"})
	st = waitFor(t, e, "f3", engine.Error, 30*time.Second)
	if st.ErrorCode != engine.ErrAuth {
		t.Fatalf("wrong password: %s %s", st.ErrorCode, st.ErrorMsg)
	}
}

func TestSidecarCrash(t *testing.T) {
	fs := &fileServer{data: randomData(t, 8<<20), etag: `"c"`, ranges: true, delay: 10 * time.Millisecond}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	e := newEngine(t)
	dir := t.TempDir()
	e.Add("sc", engine.AddRequest{URIs: []string{srv.URL + "/k.bin"}, Dir: dir})
	for {
		st, _ := e.Status("sc")
		if st.Completed > 1<<20 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.sc.mu.Lock()
	e.sc.cmd.Process.Kill()
	exited := e.sc.exited
	e.sc.mu.Unlock()
	<-exited
	if _, err := e.StatusAll(); err == nil {
		t.Log("dc-dl restarted at once")
	}
	fs.mu.Lock()
	fs.delay = 0
	fs.mu.Unlock()
	waitFor(t, e, "sc", engine.Complete, 60*time.Second)
	checkFile(t, filepath.Join(dir, "k.bin"), fs.data)
}
