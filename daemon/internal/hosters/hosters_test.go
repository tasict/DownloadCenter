package hosters

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"downloadcenter/internal/core"
)

type fakeBackend struct {
	mu       sync.Mutex
	accounts []*core.Account
	secrets  map[string]string
	infos    map[string]map[string]any
	events   []core.Event
}

func (f *fakeBackend) Accounts(owner string) []*core.Account {
	var out []*core.Account
	for _, a := range f.accounts {
		if owner == "" || a.Owner == owner {
			out = append(out, a)
		}
	}
	return out
}

func (f *fakeBackend) Account(id string) (*core.Account, error) {
	for _, a := range f.accounts {
		if a.ID == id {
			return a, nil
		}
	}
	return nil, core.ErrNotFound
}

func (f *fakeBackend) AccountSecret(id string) string { return f.secrets[id] }

func (f *fakeBackend) SetAccountInfo(id string, info map[string]any) {
	f.mu.Lock()
	f.infos[id] = info
	f.mu.Unlock()
}

func (f *fakeBackend) Emit(e core.Event) core.Event {
	f.mu.Lock()
	f.events = append(f.events, e)
	f.mu.Unlock()
	return e
}

func (f *fakeBackend) Settings() core.Settings { return core.Settings{} }
func (f *fakeBackend) DefaultURLProxy() string { return "" }

func (f *fakeBackend) add(id, owner, kind, user, secret string) *core.Account {
	a := &core.Account{ID: id, Owner: owner, Kind: kind, Username: user, Enabled: true, Info: map[string]any{}}
	f.accounts = append(f.accounts, a)
	f.secrets[id] = secret
	return a
}

// mock serves fake versions of the four APIs under /1f, /rg, /rd, /ad.
func mock(t *testing.T) (*httptest.Server, *int) {
	logins := 0
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
	// 1fichier
	mux.HandleFunc("/1f/download/get_token.cgi", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		switch r.Header.Get("Authorization") {
		case "Bearer good":
		case "Bearer expired":
			writeJSON(w, 403, map[string]any{"status": "KO", "message": "Must be a premium or access user"})
			return
		default:
			writeJSON(w, 401, map[string]any{"status": "KO", "message": "Not authenticated #247"})
			return
		}
		if strings.Contains(b["url"], "gone") {
			writeJSON(w, 404, map[string]any{"status": "KO", "message": "Resource not found #606"})
			return
		}
		writeJSON(w, 200, map[string]any{"status": "OK", "url": "https://a-1.1fichier.com/c123456"})
	})
	mux.HandleFunc("/1f/file/info.cgi", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"filename": "movie.mkv", "size": 1234567})
	})
	mux.HandleFunc("/1f/user/info.cgi", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"email": "x@example.com", "offer": "premium", "premium_expire": time.Now().Add(72 * time.Hour).Format("2006-01-02 15:04:05")})
	})
	// Rapidgator
	mux.HandleFunc("/rg/user/login", func(w http.ResponseWriter, r *http.Request) {
		logins++
		q := r.URL.Query()
		user := map[string]any{"email": q.Get("login"), "is_premium": true, "premium_end_time": time.Now().Add(60 * 24 * time.Hour).Unix(),
			"traffic": map[string]any{"total": 1000, "left": 500}}
		switch q.Get("login") {
		case "free":
			user["is_premium"] = false
		case "empty":
			user["traffic"] = map[string]any{"total": 1000, "left": 0}
		}
		if q.Get("password") != "pw" {
			writeJSON(w, 200, map[string]any{"response": nil, "status": 401, "details": "Error: Invalid login or password"})
			return
		}
		writeJSON(w, 200, map[string]any{"response": map[string]any{"token": fmt.Sprintf("tok%d", logins), "user": user}, "status": 200, "details": nil})
	})
	mux.HandleFunc("/rg/file/download", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") == "tok1" && r.URL.Query().Get("file_id") == "stale" {
			writeJSON(w, 200, map[string]any{"response": nil, "status": 401, "details": "Session expired"})
			return
		}
		if r.URL.Query().Get("file_id") == "missing" {
			writeJSON(w, 200, map[string]any{"response": nil, "status": 404, "details": "File not found"})
			return
		}
		writeJSON(w, 200, map[string]any{"response": map[string]any{"download_url": "https://pr.rapidgator.net/d/" + r.URL.Query().Get("file_id")}, "status": 200})
	})
	mux.HandleFunc("/rg/file/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"response": map[string]any{"file": map[string]any{"name": "archive.rar", "size": "2048"}}, "status": 200})
	})
	// Real-Debrid
	mux.HandleFunc("/rd/unrestrict/link", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer rdtok" {
			writeJSON(w, 401, map[string]any{"error": "bad_token", "error_code": 8})
			return
		}
		r.ParseForm()
		if strings.Contains(r.Form.Get("link"), "gone") {
			writeJSON(w, 503, map[string]any{"error": "unavailable_file", "error_code": 24})
			return
		}
		writeJSON(w, 200, map[string]any{"filename": "rd.zip", "filesize": 999, "download": "https://xyz.download.real-debrid.com/d/rd.zip"})
	})
	mux.HandleFunc("/rd/user", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"username": "u", "type": "premium", "expiration": time.Now().Add(100 * 24 * time.Hour).UTC().Format(time.RFC3339)})
	})
	mux.HandleFunc("/rd/hosts/domains", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, []string{"uptobox.com", "katfile.com", "1fichier.com"})
	})
	// AllDebrid
	mux.HandleFunc("/ad/link/unlock", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("agent") != agent {
			t.Errorf("alldebrid agent missing")
		}
		if q.Get("apikey") != "adkey" {
			writeJSON(w, 200, map[string]any{"status": "error", "error": map[string]any{"code": "AUTH_BAD_APIKEY", "message": "The auth apikey is invalid"}})
			return
		}
		if strings.Contains(q.Get("link"), "nosupport") {
			writeJSON(w, 200, map[string]any{"status": "error", "error": map[string]any{"code": "LINK_HOST_NOT_SUPPORTED", "message": "This host is not supported"}})
			return
		}
		writeJSON(w, 200, map[string]any{"status": "success", "data": map[string]any{"link": "https://ad.example/dl/file.bin", "filename": "file.bin", "filesize": 77}})
	})
	mux.HandleFunc("/ad/user", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"status": "success", "data": map[string]any{"user": map[string]any{"username": "u", "isPremium": true, "premiumUntil": time.Now().Add(3 * 24 * time.Hour).Unix()}}})
	})
	mux.HandleFunc("/ad/hosts/domains", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"status": "success", "data": map[string]any{"hosts": []string{"turbobit.net", "nitroflare.com"}, "redirectors": []string{"ouo.io"}}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &logins
}

func setup(t *testing.T) (*Service, *fakeBackend, *int) {
	srv, logins := mock(t)
	f := &fakeBackend{secrets: map[string]string{}, infos: map[string]map[string]any{}}
	s := newWith(f)
	s.base[OneFichier] = srv.URL + "/1f"
	s.base[Rapidgator] = srv.URL + "/rg"
	s.base[RealDebrid] = srv.URL + "/rd"
	s.base[AllDebrid] = srv.URL + "/ad"
	s.client = func() *http.Client { return srv.Client() }
	s.proxyClient = func(string) *http.Client { return srv.Client() }
	return s, f, logins
}

const jarText = "# Netscape HTTP Cookie File\n" +
	".example.com\tTRUE\t/\tFALSE\t4102444800\tsession\tabc123\n" +
	"#HttpOnly_.example.com\tTRUE\t/\tTRUE\t4102444800\tauth\tsecret\n" +
	"example.com\tFALSE\t/private\tFALSE\t4102444800\tdeep\tyes\n" +
	".example.com\tTRUE\t/\tFALSE\t1000\told\tgone\n" +
	"other.org\tFALSE\t/\tFALSE\t0\tsid\tx\n"

func TestMatch(t *testing.T) {
	s, f, _ := setup(t)
	cases := []struct {
		url, svc string
		ok       bool
	}{
		{"https://1fichier.com/?abcdef", OneFichier, true},
		{"https://www.alterupload.com/?x", OneFichier, true},
		{"https://rapidgator.net/file/abc/name.rar.html", Rapidgator, true},
		{"https://rg.to/file/abc", Rapidgator, true},
		{"https://mega.nz/file/abc#key", Mega, true},
		{"https://example.com/file.zip", "", false},
		{"ftp://1fichier.com/x", "", false},
		{"https://uptobox.com/abc", "", false},
	}
	for _, c := range cases {
		svc, ok := s.Match(c.url)
		if svc != c.svc || ok != c.ok {
			t.Errorf("Match(%s) = %q,%v want %q,%v", c.url, svc, ok, c.svc, c.ok)
		}
	}
	f.add("c1", "alice", Cookies, "", jarText)
	f.add("rd1", "bob", RealDebrid, "", "rdtok")
	f.add("ad1", "bob", AllDebrid, "", "adkey")
	for _, c := range []struct{ url, svc string }{
		{"https://files.example.com/a.zip", Cookies},
		{"https://uptobox.com/abc", RealDebrid},
		{"https://nitroflare.com/view/1", AllDebrid},
		{"https://ouo.io/x", AllDebrid},
	} {
		if svc, ok := s.Match(c.url); !ok || svc != c.svc {
			t.Errorf("Match(%s) = %q,%v want %q", c.url, svc, ok, c.svc)
		}
	}
	if _, ok := s.Match("https://unknown.net/x"); ok {
		t.Error("unknown domain matched")
	}
}

func TestOneFichier(t *testing.T) {
	s, f, _ := setup(t)
	f.add("f1", "alice", OneFichier, "", "good")
	res, err := s.Resolve("alice", "", "https://1fichier.com/?abc", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "https://a-1.1fichier.com/c123456" || res.Name != "movie.mkv" || res.Size != 1234567 || res.Account != "f1" || res.ExpiresAt == 0 {
		t.Errorf("bad resolve %+v", res)
	}
	if _, err := s.Resolve("alice", "", "https://1fichier.com/?gone", ""); !errors.Is(err, ErrFileMissing) {
		t.Errorf("want missing, got %v", err)
	}
	// Another user's account is never used
	if _, err := s.Resolve("bob", "f1", "https://1fichier.com/?abc", ""); !errors.Is(err, ErrNoAccount) {
		t.Errorf("bob used alice's account: %v", err)
	}
	// Expired account: recorded and announced once a day
	f.add("f2", "carol", OneFichier, "", "expired")
	for i := 0; i < 2; i++ {
		if _, err := s.Resolve("carol", "", "https://1fichier.com/?abc", ""); !errors.Is(err, ErrExpired) {
			t.Errorf("want expired, got %v", err)
		}
	}
	if f.infos["f2"]["error"] == nil {
		t.Error("account info not updated")
	}
	n := 0
	for _, e := range f.events {
		if e.Type == "account.expiring" && e.Owner == "carol" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("want 1 account.expiring event, got %d", n)
	}
	info, err := s.Verify(f.accounts[0], "good")
	if err != nil || info["premium"] != true || info["expires_at"].(int64) == 0 {
		t.Errorf("verify: %v %v", info, err)
	}
	// Expires in 3 days -> warning
	found := false
	for _, e := range f.events {
		if e.Type == "account.expiring" && e.Owner == "alice" {
			found = true
		}
	}
	if !found {
		t.Error("no expiring warning for an account ending within 7 days")
	}
}

func TestRapidgator(t *testing.T) {
	s, f, logins := setup(t)
	f.add("r1", "alice", Rapidgator, "alice@example.com", "pw")
	res, err := s.Resolve("alice", "", "https://rapidgator.net/file/abc123/archive.rar.html", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "https://pr.rapidgator.net/d/abc123" || res.Name != "archive.rar" || res.Size != 2048 {
		t.Errorf("bad resolve %+v", res)
	}
	// Cached token
	if _, err := s.Resolve("alice", "", "https://rapidgator.net/file/def/x.html", ""); err != nil {
		t.Fatal(err)
	}
	if *logins != 1 {
		t.Errorf("token not cached: %d logins", *logins)
	}
	if _, err := s.Resolve("alice", "", "https://rg.to/file/missing", ""); !errors.Is(err, ErrFileMissing) {
		t.Errorf("want missing, got %v", err)
	}
	f.add("r2", "bob", Rapidgator, "free", "pw")
	if _, err := s.Resolve("bob", "", "https://rg.to/file/abc", ""); !errors.Is(err, ErrExpired) {
		t.Errorf("free account: %v", err)
	}
	f.add("r3", "carol", Rapidgator, "empty", "pw")
	if _, err := s.Resolve("carol", "", "https://rg.to/file/abc", ""); !errors.Is(err, ErrTraffic) {
		t.Errorf("no traffic: %v", err)
	}
	f.add("r4", "dave", Rapidgator, "dave", "wrong")
	if _, err := s.Resolve("dave", "", "https://rg.to/file/abc", ""); !errors.Is(err, ErrBadLogin) {
		t.Errorf("bad login: %v", err)
	}
	info, err := s.Verify(f.accounts[0], "pw")
	if err != nil || info["premium"] != true || info["traffic_left"].(int64) != 500 {
		t.Errorf("verify: %v %v", info, err)
	}
}

func TestRapidgatorRelogin(t *testing.T) {
	s, f, logins := setup(t)
	f.add("r1", "alice", Rapidgator, "alice", "pw")
	res, err := s.Resolve("alice", "", "https://rapidgator.net/file/stale/x.html", "")
	if err != nil {
		t.Fatal(err)
	}
	if *logins != 2 || !strings.HasSuffix(res.URL, "/stale") {
		t.Errorf("relogin: logins=%d res=%+v", *logins, res)
	}
}

func TestRealDebrid(t *testing.T) {
	s, f, _ := setup(t)
	f.add("rd1", "alice", RealDebrid, "", "rdtok")
	res, err := s.Resolve("alice", "", "https://uptobox.com/abc", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "https://xyz.download.real-debrid.com/d/rd.zip" || res.Name != "rd.zip" || res.Size != 999 || res.Account != "rd1" {
		t.Errorf("bad resolve %+v", res)
	}
	// 1fichier link without a 1fichier account: Real-Debrid covers it
	if res, err := s.Resolve("alice", "", "https://1fichier.com/?abc", ""); err != nil || res.Account != "rd1" {
		t.Errorf("debrid fallback: %+v %v", res, err)
	}
	if _, err := s.Resolve("alice", "", "https://uptobox.com/gone", ""); !errors.Is(err, ErrFileMissing) {
		t.Errorf("want missing, got %v", err)
	}
	f.secrets["rd1"] = "bad"
	if _, err := s.Resolve("alice", "", "https://uptobox.com/abc", ""); !errors.Is(err, ErrBadLogin) {
		t.Errorf("bad token: %v", err)
	}
	info, err := s.Verify(f.accounts[0], "rdtok")
	if err != nil || info["premium"] != true || info["expires_at"].(int64) == 0 {
		t.Errorf("verify: %v %v", info, err)
	}
}

func TestAllDebrid(t *testing.T) {
	s, f, _ := setup(t)
	f.add("ad1", "alice", AllDebrid, "", "adkey")
	res, err := s.Resolve("alice", "", "https://turbobit.net/abc.html", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "https://ad.example/dl/file.bin" || res.Size != 77 {
		t.Errorf("bad resolve %+v", res)
	}
	if _, err := s.adResolve("adkey", "https://turbobit.net/nosupport"); !errors.Is(err, ErrHostNotSup) {
		t.Errorf("unsupported host: %v", err)
	}
	if _, err := s.adResolve("nope", "https://turbobit.net/x"); !errors.Is(err, ErrBadLogin) {
		t.Errorf("bad key: %v", err)
	}
	info, err := s.Verify(f.accounts[0], "adkey")
	if err != nil || info["premium"] != true {
		t.Errorf("verify: %v %v", info, err)
	}
}

func TestMegaAndPassthrough(t *testing.T) {
	s, f, _ := setup(t)
	if _, err := s.Resolve("alice", "", "https://mega.nz/file/x#y", ""); !errors.Is(err, ErrMega) {
		t.Errorf("mega: %v", err)
	}
	// example.com matches because of alice's cookies; bob downloads it plainly
	f.add("c1", "alice", Cookies, "", jarText)
	res, err := s.Resolve("bob", "", "https://files.example.com/a.zip", "")
	if err != nil || res.URL != "https://files.example.com/a.zip" || len(res.Headers) != 0 || res.Account != "" {
		t.Errorf("passthrough: %+v %v", res, err)
	}
	res, err = s.Resolve("alice", "", "https://files.example.com/a.zip", "")
	if err != nil || len(res.Headers) != 1 || res.Account != "c1" {
		t.Fatalf("cookies: %+v %v", res, err)
	}
	h := res.Headers[0]
	if !strings.HasPrefix(h, "Cookie: ") || !strings.Contains(h, "session=abc123") || !strings.Contains(h, "auth=secret") || strings.Contains(h, "old=") || strings.Contains(h, "deep=") {
		t.Errorf("cookie header %q", h)
	}
}

func TestCookies(t *testing.T) {
	cs := parseCookies(jarText)
	if len(cs) != 5 {
		t.Fatalf("parsed %d cookies", len(cs))
	}
	if h := cookieHeader(cs, "http://example.com/private/file"); h != "deep=yes; session=abc123" {
		t.Errorf("http header %q (secure cookie must be left out, deeper path first)", h)
	}
	if h := cookieHeader(cs, "https://sub.example.com/"); !strings.Contains(h, "auth=secret") || strings.Contains(h, "deep") {
		t.Errorf("subdomain header %q", h)
	}
	if h := cookieHeader(cs, "https://sub.other.org/"); h != "" {
		t.Errorf("host-only cookie leaked to subdomain: %q", h)
	}
	if h := cookieHeader(cs, "https://other.org/"); h != "sid=x" {
		t.Errorf("session cookie %q", h)
	}
	s, f, _ := setup(t)
	a := f.add("c1", "alice", Cookies, "", jarText)
	info, err := s.Verify(a, jarText)
	if err != nil || info["cookies"] != 4 {
		t.Errorf("verify: %v %v", info, err)
	}
	if _, err := s.Verify(a, "garbage"); err == nil {
		t.Error("garbage accepted")
	}
	// An account restricted to one host covers only that host
	a.Host = "other.org"
	if s.jarCovers(a, "example.com") || !s.jarCovers(a, "other.org") {
		t.Error("host restriction ignored")
	}
}

func TestServices(t *testing.T) {
	s, _, _ := setup(t)
	ids := map[string]bool{}
	for _, svc := range s.Services() {
		ids[svc["id"].(string)] = true
		if svc["secret_label"] == "" || svc["title"] == "" {
			t.Errorf("incomplete service %v", svc)
		}
	}
	for _, id := range []string{OneFichier, Rapidgator, RealDebrid, AllDebrid, Cookies} {
		if !ids[id] {
			t.Errorf("missing service %s", id)
		}
	}
}
