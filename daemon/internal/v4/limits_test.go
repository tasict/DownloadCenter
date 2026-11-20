package v4

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"downloadcenter/internal/api"
	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/store"
)

// endless is a request body of n bytes that counts what was read of it.
type endless struct{ n, read int64 }

func (e *endless) Read(p []byte) (int, error) {
	if e.n <= 0 {
		return 0, io.EOF
	}
	k := int64(len(p))
	if k > e.n {
		k = e.n
	}
	for i := range p[:k] {
		p[i] = 'a'
	}
	e.n -= k
	e.read += k
	return int(k), nil
}

func limitServer(t *testing.T) *api.Server {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "dc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	m := core.New(db, dir)
	au := auth.New(db)
	srv := api.New(m, au, dir, "test")
	Register(srv, m, au, dir, dir)
	return srv
}

// Without a session, a large body is not read: at most the start of it
// (to find the sid), the rest never.
func TestPreAuthBodyLimit(t *testing.T) {
	srv := limitServer(t)
	for _, c := range []struct {
		path, ct string
		max      int64
		want     int
	}{
		{"Nope/Nope", "application/x-www-form-urlencoded", 0, errAPINotExists},
		{"Rss/Query", "application/x-www-form-urlencoded", 0, errAPINotExists},
		{"Misc/Login", "application/x-www-form-urlencoded", preAuthBody + 1, errParameter},
		{"Task/Query", "application/x-www-form-urlencoded", preAuthBody + 1, errSessionTimeout},
		{"Task/AddUrl", "text/plain", preAuthBody + 1, errSessionTimeout},
		{"Task/AddTorrent", "multipart/form-data; boundary=x", maxTorrentBody + 1, errSessionTimeout},
	} {
		body := &endless{n: 64 << 20}
		r := httptest.NewRequest("POST", "/downloadstation/V4/"+c.path, body)
		r.Header.Set("Content-Type", c.ct)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		if body.read > c.max {
			t.Errorf("%s: read %d bytes before knowing the caller (at most %d)", c.path, body.read, c.max)
		}
		if code, _ := out["error"].(float64); int(code) != c.want {
			t.Errorf("%s: answer %v, want error %d", c.path, out, c.want)
		}
	}
}

func v4call(srv *api.Server, method, path, body string, set func(r *http.Request)) map[string]any {
	r := httptest.NewRequest(method, "/downloadstation/V4/"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if set != nil {
		set(r)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return out
}

// Ending a cookie session needs a POST from this NAS's own pages.
func TestLogoutNeedsSameOrigin(t *testing.T) {
	srv := limitServer(t)
	cookie := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "NAS_SID", Value: "dev"}) }
	if out := v4call(srv, "GET", "Misc/Logout", "", cookie); out["error"] != float64(errPermission) {
		t.Errorf("cross-site GET: %v", out)
	}
	if out := v4call(srv, "POST", "Misc/Logout", "", func(r *http.Request) { cookie(r); r.Header.Set("Sec-Fetch-Site", "cross-site") }); out["error"] != float64(errPermission) {
		t.Errorf("cross-site POST: %v", out)
	}
	if out := v4call(srv, "POST", "Misc/Logout", "", func(r *http.Request) { cookie(r); r.Header.Set("Sec-Fetch-Site", "same-origin") }); out["error"] != float64(errOK) {
		t.Errorf("same origin: %v", out)
	}
	// The sid as a parameter (official clients) is not a cookie
	if out := v4call(srv, "GET", "Misc/Logout?sid=dev", "", nil); out["error"] != float64(errOK) {
		t.Errorf("sid parameter: %v", out)
	}
}

// A client with ten failed password sign-ins is answered without asking QTS.
func TestLoginFailuresStop(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "dc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := core.New(db, dir)
	au := auth.New(db)
	srv := api.New(m, au, dir, "test")
	Register(srv, m, au, dir, dir)
	for i := 0; i < 10; i++ {
		au.LoginFailed("192.0.2.44")
	}
	out := v4call(srv, "POST", "Misc/Login", "user=nobody&pass=eA%3D%3D", func(r *http.Request) { r.RemoteAddr = "192.0.2.44:4000" })
	if out["error"] != float64(errAuthFail) {
		t.Errorf("answer %v", out)
	}
}

// The program name a V4 client gives is a short printable text that never
// passes for one of Download Center's own callers.
func TestCleanCaller(t *testing.T) {
	for in, want := range map[string]string{
		"":                      "Download Station",
		"Qget":                  "Qget",
		"Download Center":       "Download Station",
		"api: mytoken":          "Download Station",
		"Deskpane":              "Download Station",
		"ai Agent":              "Download Station",
		"a\x00b\nc‮":            "abc",
		strings.Repeat("長", 40): strings.Repeat("長", 32),
	} {
		if got := cleanCaller(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// A client that sends its body slowly holds its place among the requests
// not yet known only for a while.
func TestPreAuthSlowBody(t *testing.T) {
	old := preAuthTime
	preAuthTime = 300 * time.Millisecond
	defer func() { preAuthTime = old }()
	ts := httptest.NewServer(limitServer(t))
	defer ts.Close()
	pr, pw := io.Pipe()
	defer pw.Close()
	go pw.Write([]byte("user=a&"))
	done := make(chan error, 1)
	go func() {
		resp, err := http.Post(ts.URL+"/downloadstation/V4/Misc/Login", "application/x-www-form-urlencoded", pr)
		if err == nil {
			resp.Body.Close()
		}
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("a slow body kept the request open")
	}
	if len(preAuthSlots.all) != 0 {
		t.Errorf("%d places still taken", len(preAuthSlots.all))
	}
}

// One client address holds at most a few places among the requests not
// yet known, and a small body of known length needs none.
func TestPreAuthPerAddress(t *testing.T) {
	api.SetProxyAuth("0123456789abcdef0123456789abcdef")
	defer api.SetProxyAuth("")
	ts := httptest.NewServer(limitServer(t))
	defer ts.Close()
	send := func(from string, body io.Reader, size int64) chan float64 {
		out := make(chan float64, 1)
		go func() {
			req, _ := http.NewRequest("POST", ts.URL+"/downloadstation/V4/Task/Query", body)
			req.ContentLength = size
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("X-DC-Auth", "0123456789abcdef0123456789abcdef")
			req.Header.Set("X-DC-Remote", from)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				out <- -1
				return
			}
			defer resp.Body.Close()
			var m map[string]any
			json.NewDecoder(resp.Body).Decode(&m)
			code, _ := m["error"].(float64)
			out <- code
		}()
		return out
	}
	// Four slow requests of one address hold its places
	var writers []*io.PipeWriter
	var slow []chan float64
	for i := 0; i < 4; i++ {
		pr, pw := io.Pipe()
		writers = append(writers, pw)
		slow = append(slow, send("192.0.2.5", pr, -1))
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		preAuthSlots.mu.Lock()
		n := preAuthSlots.taken["192.0.2.5"]
		preAuthSlots.mu.Unlock()
		if n == 4 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	pr, pw := io.Pipe()
	defer pw.Close()
	if code := <-send("192.0.2.5", pr, -1); code != errException {
		t.Errorf("fifth slow request of one address: %v", code)
	}
	pr2, pw2 := io.Pipe()
	go func() { pw2.Write([]byte("a=b")); pw2.Close() }()
	if code := <-send("192.0.2.6", pr2, -1); code != errSessionTimeout {
		t.Errorf("another address: %v", code)
	}
	// A small body of known length needs no place
	if code := <-send("192.0.2.5", strings.NewReader("a=b"), 3); code != errSessionTimeout {
		t.Errorf("small body: %v", code)
	}
	for _, w := range writers {
		w.Close()
	}
	for _, c := range slow {
		<-c
	}
	if len(preAuthSlots.all) != 0 || len(preAuthSlots.taken) != 0 {
		t.Errorf("places left taken: %d %v", len(preAuthSlots.all), preAuthSlots.taken)
	}
}
