package search

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"downloadcenter/internal/api"
)

// apiSearch runs a search through the API as user and returns its id and
// its results by title.
func apiSearch(t *testing.T, e *apiEnv, user string) (string, map[string]map[string]any) {
	t.Helper()
	code, out, _ := e.call(t, user, "", "POST", "/search", map[string]any{"q": "ubuntu"})
	if code != 202 {
		t.Fatalf("start: %d %v", code, out)
	}
	id := out["id"].(string)
	for i := 0; i < 500 && out["done"] != true; i++ {
		time.Sleep(10 * time.Millisecond)
		_, out, _ = e.call(t, user, "", "GET", "/search/"+id, nil)
	}
	byTitle := map[string]map[string]any{}
	for _, x := range out["results"].([]any) {
		r := x.(map[string]any)
		byTitle[r["title"].(string)] = r
	}
	return id, byTitle
}

// spec: a result's .torrent is fetched only from public addresses on the
// web ports, except on the administrator's Torznab server; the caller is
// not shown other addresses, and every failure reads the same
func TestResultTorrentStrict(t *testing.T) {
	e := newAPIEnv(t)
	var reached atomic.Int32
	record := func(w http.ResponseWriter, r *http.Request) { reached.Add(1); w.Write([]byte(sampleTorrent)) }
	for _, h := range []string{"10.255.255.1:8080", "192.0.2.1:22", "8.8.8.8:8080", "192.168.1.20:9117", "[fd00::1]", "100.64.0.1"} {
		e.n.handle(h, record)
	}
	e.n.handle("public.example.org", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing.torrent" {
			w.WriteHeader(404)
			return
		}
		if r.URL.Path == "/denied.torrent" {
			w.WriteHeader(403)
			return
		}
		w.Write([]byte(sampleTorrent))
	})
	e.n.handle("src.example.org", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(items(jsonItem("Lan", "", "http://10.255.255.1:8080/a.torrent"), jsonItem("Doc", "", "http://192.0.2.1:22/b.torrent"),
			jsonItem("Port", "", "http://8.8.8.8:8080/c.torrent"), jsonItem("Ula", "", "http://[fd00::1]/d.torrent"),
			jsonItem("Cgnat", "", "http://100.64.0.1/e.torrent"), jsonItem("Server", "", "http://192.168.1.20:9117/f.torrent"),
			jsonItem("Public", "", "https://public.example.org/ok.torrent"), jsonItem("Missing", "", "https://public.example.org/missing.torrent"),
			jsonItem("Denied", "", "https://public.example.org/denied.torrent"))))
	})
	addPlugin(t, e.s, jsonSource("src", "src.example.org"), nil)
	id, rs := apiSearch(t, e, "admin")
	for title, want := range map[string]int{"Lan": 502, "Doc": 502, "Port": 502, "Ula": 502, "Cgnat": 502, "Server": 502, "Public": 200, "Missing": 502, "Denied": 502} {
		r := rs[title]
		if r == nil || r["ref"] == nil {
			t.Fatalf("%s: %v", title, r)
		}
		start := time.Now()
		code, out, _ := e.call(t, "admin", "", "GET", "/search/"+id+"/torrent/"+r["ref"].(string), nil)
		if code != want || (want == 502 && (errCode(out) != "search_torrent_failed" || errMsg(out) != ReasonNotTorrent)) || time.Since(start) > 2*time.Second {
			t.Errorf("%s: %d %v", title, code, out)
		}
		if shown := r["torrent_url"] != nil; shown != (title == "Public" || title == "Missing" || title == "Denied") {
			t.Errorf("%s: torrent_url %v", title, r["torrent_url"])
		}
	}
	if n := reached.Load(); n != 0 {
		t.Errorf("%d requests to addresses that are not public or not on the web ports", n)
	}

	// The administrator's Torznab server keeps the usual rule, any port
	tz := &Plugin{Schema: 1, ID: "tz", Name: "TZ", Type: TypeTorznab}
	if err := e.s.store.Install(tz, true, false, map[string]string{"url": "http://192.168.1.20:9117/api", "apikey": "K"}); err != nil {
		t.Fatal(err)
	}
	off := false
	e.s.store.Update("src", &off, nil, nil)
	e.n.handle("192.168.1.20:9117", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" {
			w.Write([]byte(`<rss><channel><item><title>Mine</title><enclosure url="http://192.168.1.20:9117/dl/1.torrent"/></item>` +
				`<item><title>Elsewhere</title><enclosure url="http://10.255.255.1:8080/a.torrent"/></item></channel></rss>`))
			return
		}
		reached.Add(1)
		w.Write([]byte(sampleTorrent))
	})
	id, rs = apiSearch(t, e, "admin")
	if code, out, _ := e.call(t, "admin", "", "GET", "/search/"+id+"/torrent/"+rs["Mine"]["ref"].(string), nil); code != 200 || reached.Load() != 1 {
		t.Errorf("from the Torznab server: %d %v", code, out)
	}
	if code, _, _ := e.call(t, "admin", "", "GET", "/search/"+id+"/torrent/"+rs["Elsewhere"]["ref"].(string), nil); code != 502 || reached.Load() != 1 {
		t.Errorf("elsewhere through the Torznab server: %d", code)
	}
}

// spec: a result's .torrent is fetched only while its source is still on
// and may be used by the caller
func TestResultTorrentSourceRechecked(t *testing.T) {
	e := newAPIEnv(t)
	e.n.handle("src.example.org", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(items(jsonItem("Public", "", "https://public.example.org/ok.torrent"))))
	})
	e.n.handle("public.example.org", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(sampleTorrent)) })
	addPlugin(t, e.s, jsonSource("src", "src.example.org"), nil)
	e.s.store.SetAudience("src", AudienceAll)
	id, rs := apiSearch(t, e, "alice")
	path := "/search/" + id + "/torrent/" + rs["Public"]["ref"].(string)
	if code, _, _ := e.call(t, "alice", "", "GET", path, nil); code != 200 {
		t.Fatalf("first: %d", code)
	}
	e.s.store.SetAudience("src", AudienceAdmins)
	if code, out, _ := e.call(t, "alice", "", "GET", path, nil); code != 404 {
		t.Errorf("for administrators now: %d %v", code, out)
	}
	e.s.store.SetAudience("src", AudienceAll)
	off := false
	e.s.store.Update("src", &off, nil, nil)
	if code, out, _ := e.call(t, "alice", "", "GET", path, nil); code != 404 {
		t.Errorf("switched off: %d %v", code, out)
	}
}

// spec: an account fetches at most two .torrent files at a time, and the
// same result twice costs one request
func TestResultTorrentLimits(t *testing.T) {
	e := newAPIEnv(t)
	release := make(chan struct{})
	var mu sync.Mutex
	asked := map[string]int{}
	e.n.handle("public.example.org", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked[r.URL.Path]++
		mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/slow") {
			<-release
		}
		w.Write([]byte(sampleTorrent))
	})
	e.n.handle("src.example.org", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(items(jsonItem("Slow1", "", "https://public.example.org/slow1.torrent"), jsonItem("Slow2", "", "https://public.example.org/slow2.torrent"),
			jsonItem("Third", "", "https://public.example.org/third.torrent"))))
	})
	addPlugin(t, e.s, jsonSource("src", "src.example.org"), nil)
	id, rs := apiSearch(t, e, "admin")
	ref := func(title string) string { return "/search/" + id + "/torrent/" + rs[title]["ref"].(string) }
	var wg sync.WaitGroup
	for _, title := range []string{"Slow1", "Slow2"} {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			req := httptest.NewRequest("GET", api.APIBase+p, nil)
			req.RemoteAddr = "127.0.0.1:40000"
			req.Header.Set("X-Requested-With", "XMLHttpRequest")
			e.srv.ServeHTTP(httptest.NewRecorder(), req)
		}(ref(title))
	}
	for i := 0; i < 200; i++ {
		mu.Lock()
		n := asked["/slow1.torrent"] + asked["/slow2.torrent"]
		mu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code, out, _ := e.call(t, "admin", "", "GET", ref("Third"), nil); code != 429 || errCode(out) != "rate_limited" {
		t.Errorf("third at once: %d %v", code, out)
	}
	close(release)
	wg.Wait()
	for i := 0; i < 3; i++ {
		if code, _, rec := e.call(t, "admin", "", "GET", ref("Third"), nil); code != 200 || rec.Body.String() != sampleTorrent {
			t.Errorf("third, then: %d", code)
		}
	}
	if asked["/third.torrent"] != 1 {
		t.Errorf("the same result was fetched %d times", asked["/third.torrent"])
	}
}
