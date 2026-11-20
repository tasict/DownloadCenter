package search

import (
	"bytes"
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
	"downloadcenter/internal/qts"
	"downloadcenter/internal/store"
)

type apiEnv struct {
	srv *api.Server
	s   *Service
	n   *fakeNet
}

func newAPIEnv(t *testing.T) *apiEnv {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "dc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// QTS lets admin and alice use Download Center
	t.Cleanup(qts.StubAppriv(func(args ...string) (string, int, error) {
		if args[0] == "-C" && args[2] != "admin" && args[2] != "alice" {
			return "Permission Deny", 254, nil
		}
		return "", 0, nil
	}))
	srv := api.New(core.New(db, dir), auth.New(db), t.TempDir(), "test")
	n := newFakeNet()
	s := newTestService(t, n)
	s.routes(srv)
	return &apiEnv{srv: srv, s: s, n: n}
}

// call makes a request as the session user (DevUser) or, with a bearer
// value, with that token.
func (e *apiEnv) call(t *testing.T, user, bearer, method, path string, body any) (int, map[string]any, *httptest.ResponseRecorder) {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
	}
	e.srv.DevUser = user
	req := httptest.NewRequest(method, api.APIBase+path, rd)
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec
}

func (e *apiEnv) token(t *testing.T, owner string, scopes ...string) string {
	t.Helper()
	v, err := e.srv.Auth.CreateToken(&auth.Token{Owner: owner, Name: "t", Scopes: scopes}, owner == "admin")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func errCode(out map[string]any) string {
	if e, ok := out["error"].(map[string]any); ok {
		s, _ := e["code"].(string)
		return s
	}
	return ""
}

func errMsg(out map[string]any) string {
	if e, ok := out["error"].(map[string]any); ok {
		s, _ := e["message"].(string)
		return s
	}
	return ""
}

func TestAPISearchWithToken(t *testing.T) {
	e := newAPIEnv(t)
	e.n.handle("src.example.org", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/s":
			w.Write([]byte(items(jsonItem("Ubuntu 26.04", hashA, ""), jsonItem("Ubuntu ISO", "", "https://src.example.org/u.torrent"))))
		case "/u.torrent":
			w.Write([]byte(sampleTorrent))
		}
	})
	addPlugin(t, e.s, jsonSource("src", "src.example.org"), nil)

	// spec: a token without search:use
	read := e.token(t, "alice", "tasks:read")
	code, out, _ := e.call(t, "", read, "GET", "/search/sources", nil)
	if code != 403 || errCode(out) != "insufficient_scope" || !strings.Contains(errMsg(out), "search:use") {
		t.Fatalf("missing scope: %d %v", code, out)
	}
	// spec: a token made before search existed ("full control") cannot search
	full := e.token(t, "alice", "tasks:read", "tasks:add", "tasks:control", "tasks:remove", "stats:read", "events:read")
	if code, out, _ := e.call(t, "", full, "POST", "/search", map[string]any{"q": "ubuntu"}); code != 403 || errCode(out) != "insufficient_scope" {
		t.Fatalf("old token: %d %v", code, out)
	}

	// spec: search, poll, then add with the magnet or the .torrent
	tok := e.token(t, "alice", "search:use", "tasks:add")
	// The source has a key setting: for administrators until opened to all
	if _, out, _ := e.call(t, "", tok, "GET", "/search/sources", nil); len(out["sources"].([]any)) != 0 {
		t.Fatalf("a source with a key setting for everyone: %v", out)
	}
	if err := e.s.store.SetAudience("src", AudienceAll); err != nil {
		t.Fatal(err)
	}
	code, out, _ = e.call(t, "", tok, "GET", "/search/sources", nil)
	srcs := out["sources"].([]any)
	if code != 200 || len(srcs) != 1 {
		t.Fatalf("sources: %d %v", code, out)
	}
	if src := srcs[0].(map[string]any); len(src) != 4 || src["id"] != "src" || src["builtin"] != false || src["categories"] == nil {
		t.Errorf("source as a regular caller sees it: %v", src)
	}
	code, out, _ = e.call(t, "", tok, "POST", "/search", map[string]any{"q": "ubuntu", "category": "all"})
	if code != 202 || out["id"] == nil {
		t.Fatalf("start: %d %v", code, out)
	}
	id := out["id"].(string)
	for i := 0; i < 500; i++ {
		_, out, _ = e.call(t, "", tok, "GET", "/search/"+id, nil)
		if out["done"] == true {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if out["done"] != true || out["total"].(float64) != 2 {
		t.Fatalf("results: %v", out)
	}
	var ref string
	for _, x := range out["results"].([]any) {
		r := x.(map[string]any)
		if r["magnet"] == nil && r["ref"] != nil {
			ref = r["ref"].(string)
		}
	}
	code, _, rec := e.call(t, "", tok, "GET", "/search/"+id+"/torrent/"+ref, nil)
	if code != 200 || rec.Header().Get("Content-Type") != "application/x-bittorrent" || rec.Body.String() != sampleTorrent ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), ".torrent") {
		t.Fatalf("torrent: %d %v %q", code, rec.Header(), rec.Body.String())
	}

	// spec: another account gets not_found, the administrator included
	if code, out, _ := e.call(t, "admin", "", "GET", "/search/"+id, nil); code != 404 || errCode(out) != "not_found" {
		t.Errorf("admin reads alice's search: %d %v", code, out)
	}
	if code, _, _ := e.call(t, "admin", "", "GET", "/search/"+id+"/torrent/"+ref, nil); code != 404 {
		t.Errorf("admin fetches alice's result: %d", code)
	}
	// The same account in the web UI reads it
	if code, _, _ := e.call(t, "alice", "", "GET", "/search/"+id, nil); code != 200 {
		t.Errorf("alice's session: %d", code)
	}
	if code, out, _ := e.call(t, "", tok, "DELETE", "/search/"+id, nil); code != 200 || out["ok"] != true {
		t.Errorf("cancel: %d %v", code, out)
	}

	// spec: a keyword too short, an unknown category
	if code, out, _ := e.call(t, "", tok, "POST", "/search", map[string]any{"q": "u"}); code != 400 || errCode(out) != "invalid_request" {
		t.Errorf("short keyword: %d %v", code, out)
	}
	if code, out, _ := e.call(t, "", tok, "POST", "/search", map[string]any{"q": "ubuntu", "category": "games"}); code != 400 || errCode(out) != "invalid_request" {
		t.Errorf("category: %d %v", code, out)
	}
	// spec: no source
	off := false
	e.s.store.Update("src", &off, nil, nil)
	code, out, _ = e.call(t, "", tok, "POST", "/search", map[string]any{"q": "ubuntu"})
	if code != 409 || errCode(out) != "search_no_sources" || errMsg(out) != msgNoSources {
		t.Errorf("no sources: %d %v", code, out)
	}
	// The message follows the UI language
	e.srv.DevUser = "alice"
	req := httptest.NewRequest("POST", api.APIBase+"/search", strings.NewReader(`{"q":"ubuntu"}`))
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-DC-Lang", "TCH")
	rec = httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "search_no_sources") || strings.Contains(rec.Body.String(), msgNoSources) {
		t.Errorf("TCH: %s", rec.Body.String())
	}
}

func TestAPIManageSources(t *testing.T) {
	e := newAPIEnv(t)
	plugin := `{"schema":1,"id":"example","name":"Example","version":"1.0","type":"json",
		"request":{"url":"https://api.example.org/search?q={query}&key={setting:apikey}","headers":{"Accept":"application/json"}},
		"settings":[{"id":"apikey","label":"API key","secret":true,"required":true}],
		"response":{"items":"docs","fields":{"title":"title","infohash":"btih"}}}`

	// spec: a token cannot manage sources, a regular user neither
	admTok := e.token(t, "admin", "settings:read", "settings:write", "search:use")
	if code, out, _ := e.call(t, "", admTok, "POST", "/search/plugins", map[string]any{"json": plugin}); code != 403 || errCode(out) != "session_required" {
		t.Fatalf("token installs: %d %v", code, out)
	}
	for _, path := range []string{"/search/plugins", "/search/plugins/kiwix"} {
		if code, out, _ := e.call(t, "alice", "", "GET", path, nil); code != 403 || errCode(out) != "admin_only" {
			t.Errorf("regular user %s: %d %v", path, code, out)
		}
	}
	if _, ok := e.s.store.Get("example"); ok {
		t.Fatal("installed")
	}

	// spec: inspect shows the hosts and saves nothing
	code, out, _ := e.call(t, "admin", "", "POST", "/search/plugins/inspect", map[string]any{"json": plugin})
	if code != 200 || out["name"] != "Example" || out["version"] != "1.0" || out["exists"] != false {
		t.Fatalf("inspect: %d %v", code, out)
	}
	if hosts := out["hosts"].([]any); len(hosts) != 1 || hosts[0] != "api.example.org" {
		t.Errorf("hosts %v", hosts)
	}
	if h := out["headers"].([]any); len(h) != 1 || h[0] != "Accept" {
		t.Errorf("headers %v", h)
	}
	if st := out["settings"].([]any); len(st) != 1 || st[0].(map[string]any)["id"] != "apikey" {
		t.Errorf("settings %v", st)
	}
	if _, ok := e.s.store.Get("example"); ok {
		t.Fatal("inspect installed the plugin")
	}

	// spec: invalid plugins
	for name, c := range map[string]struct {
		body any
		code int
		err  string
	}{
		"unknown field":   {map[string]any{"json": strings.Replace(plugin, `"version"`, `"script":"x","version"`, 1)}, 400, "search_plugin_invalid"},
		"too large":       {map[string]any{"json": strings.Replace(plugin, `"Example"`, `"`+strings.Repeat("x", 100<<10)+`"`, 1)}, 400, "search_plugin_invalid"},
		"host variable":   {map[string]any{"json": strings.Replace(plugin, "https://api.example.org/", "https://{setting:apikey}/", 1)}, 400, "search_plugin_invalid"},
		"built-in id":     {map[string]any{"json": strings.Replace(plugin, `"id":"example"`, `"id":"kiwix"`, 1)}, 409, "search_plugin_exists"},
		"nothing":         {map[string]any{}, 400, "search_plugin_invalid"},
		"http address":    {map[string]any{"url": "http://plugins.example.org/p.json", "sha256": "x"}, 400, "search_plugin_invalid"},
		"loopback":        {map[string]any{"url": "https://127.0.0.1/plugin.json", "sha256": "x"}, 502, "fetch_failed"},
		"torznab no name": {map[string]any{"torznab": map[string]any{"url": "http://192.168.1.20:9117/"}}, 400, "search_plugin_invalid"},
		"torznab ftp":     {map[string]any{"torznab": map[string]any{"name": "J", "url": "ftp://192.168.1.20/"}}, 400, "search_plugin_invalid"},
		"bad json":        {"{", 400, "bad_request"},
	} {
		code, out, _ := e.call(t, "admin", "", "POST", "/search/plugins", c.body)
		if code != c.code || errCode(out) != c.err {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	// spec: installing from a loopback address says why and connects nowhere
	_, out, _ = e.call(t, "admin", "", "POST", "/search/plugins/inspect", map[string]any{"url": "https://127.0.0.1/plugin.json"})
	if !strings.Contains(errMsg(out), ReasonNotAllowed) {
		t.Errorf("loopback: %v", out)
	}
	if len(e.n.requests()) != 0 {
		t.Errorf("requests made: %d", len(e.n.requests()))
	}

	// From an address
	e.n.handle("plugins.example.org", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(plugin)) })
	_, out, _ = e.call(t, "admin", "", "POST", "/search/plugins/inspect", map[string]any{"url": "https://plugins.example.org/p.json"})
	code, out, _ = e.call(t, "admin", "", "POST", "/search/plugins", map[string]any{"url": "https://plugins.example.org/p.json", "sha256": out["sha256"]})
	if code != 200 || out["id"] != "example" || out["enabled"] != false || out["needs_setup"] != true || out["kind"] != "custom" {
		t.Fatalf("install: %d %v", code, out)
	}
	if code, out, _ := e.call(t, "admin", "", "POST", "/search/plugins", map[string]any{"json": plugin}); code != 409 || errCode(out) != "search_plugin_exists" {
		t.Errorf("twice: %d %v", code, out)
	}
	if code, _, _ := e.call(t, "admin", "", "POST", "/search/plugins", map[string]any{"json": plugin, "replace": true}); code != 200 {
		t.Errorf("replace: %d", code)
	}
	code, out, _ = e.call(t, "admin", "", "PUT", "/search/plugins/example", map[string]any{"enabled": true, "settings": map[string]string{"apikey": "PLUGINKEY"}})
	if code != 200 || out["enabled"] != true || out["needs_setup"] != false {
		t.Fatalf("update: %d %v", code, out)
	}

	// spec: a Torznab server; its key is never shown again
	code, out, _ = e.call(t, "admin", "", "POST", "/search/plugins", map[string]any{"torznab": map[string]any{
		"name": "Jackett", "url": "http://192.168.1.20:9117/api/v2.0/indexers/all/results/torznab", "apikey": "JACKETTKEY"}})
	if code != 200 || out["id"] != "jackett" || out["enabled"] != true || out["kind"] != "torznab" || out["host"] != "192.168.1.20" {
		t.Fatalf("torznab: %d %v", code, out)
	}
	code, out, rec := e.call(t, "admin", "", "GET", "/search/plugins", nil)
	if code != 200 || strings.Contains(rec.Body.String(), "JACKETTKEY") || strings.Contains(rec.Body.String(), "PLUGINKEY") {
		t.Fatalf("list: %d %s", code, rec.Body.String())
	}
	for _, x := range out["sources"].([]any) {
		src := x.(map[string]any)
		if src["id"] != "jackett" {
			continue
		}
		for _, sx := range src["settings"].([]any) {
			st := sx.(map[string]any)
			if st["id"] == "apikey" && (st["set"] != true || st["value"] != nil) {
				t.Errorf("apikey %v", st)
			}
			if st["id"] == "url" && st["value"] != "http://192.168.1.20:9117/api/v2.0/indexers/all/results/torznab" {
				t.Errorf("url %v", st)
			}
		}
	}
	// An empty key keeps the saved one; the name can change
	code, out, _ = e.call(t, "admin", "", "PUT", "/search/plugins/jackett", map[string]any{"name": "Jackett (LAN)", "settings": map[string]string{"apikey": ""}})
	if src, _ := e.s.store.Get("jackett"); code != 200 || src.Settings["apikey"] != "JACKETTKEY" || src.Plugin.Name != "Jackett (LAN)" {
		t.Errorf("update torznab: %d %v", code, out)
	}
	// Regular callers see neither addresses nor settings
	tok := e.token(t, "alice", "search:use")
	_, _, rec = e.call(t, "", tok, "GET", "/search/sources", nil)
	if strings.Contains(rec.Body.String(), "192.168") || strings.Contains(rec.Body.String(), "settings") {
		t.Errorf("sources: %s", rec.Body.String())
	}

	// Export: the definition without values
	code, _, rec = e.call(t, "admin", "", "GET", "/search/plugins/jackett", nil)
	if code != 200 || strings.Contains(rec.Body.String(), "JACKETTKEY") || strings.Contains(rec.Body.String(), "192.168") ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), "jackett.json") {
		t.Errorf("export: %d %s", code, rec.Body.String())
	}
	if _, err := Parse(rec.Body.Bytes()); err != nil {
		t.Errorf("export does not parse: %v", err)
	}

	// spec: built-in sources stay
	if code, out, _ := e.call(t, "admin", "", "DELETE", "/search/plugins/kiwix", nil); code != 409 || errCode(out) != "search_plugin_builtin" {
		t.Errorf("delete built-in: %d %v", code, out)
	}
	if code, out, _ := e.call(t, "admin", "", "DELETE", "/search/plugins/nothere", nil); code != 404 || errCode(out) != "not_found" {
		t.Errorf("delete unknown: %d %v", code, out)
	}
	if code, _, _ := e.call(t, "admin", "", "DELETE", "/search/plugins/example", nil); code != 200 {
		t.Errorf("delete: %d", code)
	}

	// spec: a Torznab server on the NAS fails its test, saying why
	code, out, _ = e.call(t, "admin", "", "POST", "/search/plugins/test", map[string]any{"torznab": map[string]any{
		"name": "Local", "url": "http://127.0.0.1:9117/api/v2.0/indexers/all/results/torznab", "apikey": "K"}})
	if code != 200 || out["ok"] != false || out["error"] != ReasonNotAllowed {
		t.Errorf("test on loopback: %d %v", code, out)
	}
	if _, ok := e.s.store.Get("local"); ok {
		t.Error("the test saved the source")
	}
	// A saved source's test: the Torznab answer counted
	e.n.handle("192.168.1.20:9117", func(w http.ResponseWriter, r *http.Request) { w.Write(readData(t, "jackett.xml")) })
	code, out, _ = e.call(t, "admin", "", "POST", "/search/plugins/jackett/test", nil)
	if code != 200 || out["ok"] != true || out["count"].(float64) != 3 {
		t.Errorf("test: %d %v", code, out)
	}
	if code, _, _ := e.call(t, "admin", "", "POST", "/search/plugins/nothere/test", nil); code != 404 {
		t.Errorf("test unknown: %d", code)
	}
}

// /me tells every account whether there is anything to search.
func TestAPIMe(t *testing.T) {
	e := newAPIEnv(t)
	_, out, _ := e.call(t, "alice", "", "GET", "/me", nil)
	if s, ok := out["search"].(map[string]any); !ok || s["available"] != false {
		t.Errorf("no source: %v", out["search"])
	}
	on := true
	e.s.store.Update("kiwix", &on, nil, nil)
	_, out, _ = e.call(t, "alice", "", "GET", "/me", nil)
	if s, ok := out["search"].(map[string]any); !ok || s["available"] != true {
		t.Errorf("kiwix on: %v", out["search"])
	}
}

// Only a Torznab server added with its form starts enabled; a plugin file
// of any type waits. The test endpoint takes every body form and does not
// mind an id that is taken.
func TestAPIInstallAndTestRules(t *testing.T) {
	e := newAPIEnv(t)
	e.n.handle("src.example.org", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(items(jsonItem("Debian", hashA, ""), jsonItem("Fedora", "", "https://src.example.org/f.torrent"))))
	})
	plugin := jsonSource("mine", "src.example.org")
	e.n.handle("plugins.example.org", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(plugin)) })

	tz := `{"schema":1,"id":"filetz","name":"Torznab from a file","type":"torznab"}`
	code, out, _ := e.call(t, "admin", "", "POST", "/search/plugins", map[string]any{"json": tz})
	if code != 200 || out["enabled"] != false || out["kind"] != "torznab" {
		t.Errorf("torznab from a file: %d %v", code, out)
	}
	for name, body := range map[string]any{
		"url":  map[string]any{"url": "https://plugins.example.org/p.json"},
		"json": map[string]any{"json": plugin},
	} {
		code, out, _ := e.call(t, "admin", "", "POST", "/search/plugins/test", body)
		if code != 200 || out["ok"] != true || out["count"].(float64) != 2 {
			t.Errorf("test from %s: %d %v", name, code, out)
		}
	}
	if _, ok := e.s.store.Get("mine"); ok {
		t.Fatal("the test installed the plugin")
	}
	// Installed, the same plugin can still be tested, with its saved key
	if code, _, _ := e.call(t, "admin", "", "POST", "/search/plugins", map[string]any{"json": plugin, "settings": map[string]string{"key": "SAVED"}}); code != 200 {
		t.Fatalf("install: %d", code)
	}
	n := len(e.n.requests())
	code, out, _ = e.call(t, "admin", "", "POST", "/search/plugins/test", map[string]any{"json": plugin})
	if code != 200 || out["ok"] != true {
		t.Errorf("test of an installed id: %d %v", code, out)
	}
	if reqs := e.n.requests(); len(reqs) != n+1 || reqs[n].Header.Get("X-Api-Key") != "SAVED" {
		t.Errorf("saved settings not used")
	}
	// A built-in source's id too
	code, out, _ = e.call(t, "admin", "", "POST", "/search/plugins/test", map[string]any{"json": strings.Replace(plugin, `"id":"mine"`, `"id":"kiwix"`, 1)})
	if code != 200 || out["ok"] != true {
		t.Errorf("test with a built-in id: %d %v", code, out)
	}
}
