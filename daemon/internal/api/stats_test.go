package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/qts"
)

func sorted(l []string) string {
	l = append([]string(nil), l...)
	sort.Strings(l)
	return strings.Join(l, " ")
}

// A new token is described by its preset, scopes, expiry and limits only.
func TestTokenKeys(t *testing.T) {
	const day = 86400
	full := tokenPresets["full"]
	cases := []struct {
		name  string
		tok   auth.Token
		admin bool
		want  string
	}{
		{"agent", auth.Token{Name: "Claude", Scopes: full, Tasks: "own", RateLimit: 120, Created: 1000, ExpiresAt: 1000 + 90*day - 1}, true,
			"tok_by_admin tok_created tok_exp_90 tok_preset_full tok_scope_events_read tok_scope_stats_read tok_scope_tasks_add tok_scope_tasks_control tok_scope_tasks_read tok_scope_tasks_remove"},
		{"custom with limits", auth.Token{Scopes: []string{"tasks:read", "files:delete"}, Tasks: "all", Folders: []string{"/x"}, Sources: []string{"url"},
			IPAllow: []string{"10.0.0.0/8"}, RateLimit: 60, Created: 1000, ExpiresAt: 1000 + 30*day}, true,
			"tok_all_tasks tok_by_admin tok_created tok_exp_30 tok_folders tok_ip_allow tok_preset_custom tok_rate_custom tok_scope_files_delete tok_scope_tasks_read tok_sources"},
		{"read, a year", auth.Token{Scopes: []string{"events:read", "tasks:read", "stats:read"}, RateLimit: 120, Created: 5, ExpiresAt: 5 + 365*day}, false,
			"tok_by_user tok_created tok_exp_365 tok_preset_read tok_scope_events_read tok_scope_stats_read tok_scope_tasks_read"},
		{"never", auth.Token{Scopes: []string{"tasks:add"}, RateLimit: 120, Created: 5}, false,
			"tok_by_user tok_created tok_exp_never tok_preset_custom tok_scope_tasks_add"},
		{"45 days", auth.Token{Scopes: tokenPresets["add"], RateLimit: 120, Created: 5, ExpiresAt: 5 + 45*day}, false,
			"tok_by_user tok_created tok_exp_other tok_preset_add tok_scope_events_read tok_scope_stats_read tok_scope_tasks_add tok_scope_tasks_read"},
	}
	for _, c := range cases {
		got := tokenKeys(&c.tok, c.admin)
		if sorted(got) != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, sorted(got), c.want)
		}
		for _, k := range got {
			if strings.Contains(k, "Claude") || len(k) > 40 {
				t.Errorf("%s: bad key %q", c.name, k)
			}
		}
	}
}

// The presets here are the token window's: changing one side alone fails.
func TestTokenPresetsMatchUI(t *testing.T) {
	js, err := os.ReadFile("../../../shared/web/js/settings-more.js")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`var PRESETS = \{([^}]*)\}`).FindSubmatch(js)
	if m == nil {
		t.Fatal("PRESETS not found in settings-more.js")
	}
	ui := map[string]string{}
	for _, p := range regexp.MustCompile(`(\w+):\[([^\]]*)\]`).FindAllSubmatch(m[1], -1) {
		var sc []string
		for _, q := range regexp.MustCompile(`'([^']+)'`).FindAllSubmatch(p[2], -1) {
			sc = append(sc, string(q[1]))
		}
		ui[string(p[1])] = sorted(sc)
	}
	if len(ui) != len(tokenPresets) {
		t.Fatalf("UI presets %v, Go presets %v", ui, tokenPresets)
	}
	for name, sc := range tokenPresets {
		if ui[name] != sorted(sc) {
			t.Errorf("preset %s: UI %q, Go %q", name, ui[name], sorted(sc))
		}
	}
}

// counted replaces the statistics hook for one test.
func counted(t *testing.T) map[string]int {
	t.Helper()
	got := map[string]int{}
	Counter = func(k string) { got[k]++ }
	t.Cleanup(func() { Counter = nil })
	return got
}

// call sends a request through the server's routes, as the dev user on
// loopback (a session) unless a header such as Authorization is given.
func call(s *Server, method, path, body string, hdr ...string) (int, map[string]any) {
	req := httptest.NewRequest(method, APIBase+path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func tokCounts(got map[string]int) string {
	var l []string
	for k, n := range got {
		if strings.HasPrefix(k, "tok_") {
			l = append(l, fmt.Sprintf("%s=%d", k, n))
		}
	}
	return sorted(l)
}

// Creating, editing, regenerating and revoking tokens are counted once they succeed.
func TestTokenCounts(t *testing.T) {
	s := fixServer(t)
	s.DevUser = "admin"
	got := counted(t)
	code, out := call(s, "POST", "/tokens", `{"name":"Claude","scopes":["tasks:read","tasks:add","tasks:control","tasks:remove","stats:read","events:read"],"expires_days":90}`)
	if code != 200 {
		t.Fatalf("create: %d %v", code, out)
	}
	want := "tok_by_admin=1 tok_created=1 tok_exp_90=1 tok_preset_full=1 tok_scope_events_read=1 tok_scope_stats_read=1 tok_scope_tasks_add=1 tok_scope_tasks_control=1 tok_scope_tasks_read=1 tok_scope_tasks_remove=1"
	if tokCounts(got) != want {
		t.Fatalf("create counted\n %s\nwant %s", tokCounts(got), want)
	}
	for k := range got {
		if strings.Contains(k, "claude") {
			t.Fatalf("token name in %q", k)
		}
	}
	id := out["token"].(map[string]any)["id"].(string)
	for _, step := range []struct{ method, path, body, key string }{
		{"PATCH", "/tokens/" + id, `{"expires_days":0}`, "tok_edited"},
		{"POST", "/tokens/" + id + "/regenerate", `{}`, "tok_regenerated"},
		{"DELETE", "/tokens/tok_missing", ``, ""},
		{"DELETE", "/tokens/" + id, ``, "tok_revoked"},
		{"PATCH", "/tokens/" + id, `{"name":"x"}`, ""}, // gone: 404
	} {
		clear(got)
		call(s, step.method, step.path, step.body)
		w := ""
		if step.key != "" {
			w = step.key + "=1"
		}
		if tokCounts(got) != w {
			t.Errorf("%s %s counted %q, want %q", step.method, step.path, tokCounts(got), w)
		}
	}
}

// A token a regular user may not create is not counted.
func TestTokenRejectedNotCounted(t *testing.T) {
	t.Cleanup(qts.StubAppriv(func(args ...string) (string, int, error) {
		if args[0] == "-C" {
			return "Permission Allow", 0, nil
		}
		return "", 0, nil
	}))
	s := fixServer(t)
	s.DevUser = "dcstatsbob"
	got := counted(t)
	code, out := call(s, "POST", "/tokens", `{"name":"x","scopes":["settings:write"]}`)
	if code != 400 || len(got) != 0 {
		t.Fatalf("%d %v, counted %v", code, out, got)
	}
}

func TestRouteKey(t *testing.T) {
	for pattern, want := range map[string]string{
		"GET /tasks/{id}/files":                   "api_get_tasks_id_files",
		"POST /settings/proxy-test":               "api_post_settings_proxy_test",
		"GET /tasks/{id}/preview-info":            "api_get_tasks_id_preview_info",
		"DELETE /channels/{id}/links/{chat_user}": "api_delete_channels_id_links_chat_user",
		"GET /me": "api_get_me",
		"POST /a-very-long/route/{that_goes}/on/and/on": "api_other",
	} {
		if got := routeKey(pattern); got != want {
			t.Errorf("%s: %s, want %s", pattern, got, want)
		}
	}
}

func TestClientOf(t *testing.T) {
	for ua, want := range map[string]string{
		"":                       "none",
		"curl/8.7.1":             "curl",
		"Wget/1.21.4":            "wget",
		"python-requests/2.32.3": "python",
		"Python-urllib/3.12":     "python",
		"python-httpx/0.27.0":    "python",
		"aiohttp/3.10.5":         "python",
		"HomeAssistant/2026.9.0 aiohttp/3.10 Python/3.13":                                     "homeassistant",
		"Mozilla/5.0 (Windows NT 10.0; Microsoft Windows 10.0.22631; en-US) PowerShell/7.4.5": "powershell",
		"Mozilla/5.0 (Windows NT; Windows NT 10.0; de-DE) WindowsPowerShell/5.1.22621.4111":   "powershell",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15":                "browser",
		"node":        "node",
		"axios/1.7.7": "node",
		"node-fetch/1.0 (+https://github.com/bitinn/node-fetch)": "node",
		"undici":             "node",
		"Go-http-client/2.0": "go",
		"MyBot/1.0 (secret)": "other",
	} {
		if got := clientOf(ua); got != want {
			t.Errorf("%q: %s, want %s", ua, got, want)
		}
	}
}

// Every route has a valid name of its own in the statistics.
func TestRouteKeysValid(t *testing.T) {
	s := fixServer(t)
	nameRe := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	seen := map[string]string{}
	for pattern, k := range s.keys {
		if !nameRe.MatchString(k) || len(k) > 40 || k == "api_other" {
			t.Errorf("%s: bad name %q", pattern, k)
		}
		if p, dup := seen[k]; dup {
			t.Errorf("%s and %s are both %s", p, pattern, k)
		}
		seen[k] = pattern
	}
	if len(seen) < 40 {
		t.Fatalf("only %d routes", len(seen))
	}
}

// Calls with a token are counted by route, client and error status; the UI's
// session calls are not, and nothing from the request becomes a name.
func TestTokenCallCounts(t *testing.T) {
	s := fixServer(t)
	s.DevUser = "admin"
	got := counted(t)
	tok := &auth.Token{Owner: "admin", Name: "my secret bot", Scopes: []string{"tasks:read"}}
	value, err := s.Auth.CreateToken(tok, true)
	if err != nil {
		t.Fatal(err)
	}
	bearer := "Bearer " + value
	check := func(what, want string) {
		t.Helper()
		var l []string
		for k, n := range got {
			l = append(l, fmt.Sprintf("%s=%d", k, n))
		}
		if sorted(l) != want {
			t.Errorf("%s: counted %q, want %q", what, sorted(l), want)
		}
		clear(got)
	}

	call(s, "POST", "/tasks", `{"source":"https://example.com/a.iso"}`, "Authorization", bearer, "User-Agent", "curl/8.7.1")
	check("missing scope", "api_client_curl=1 api_post_tasks=1 api_status_403=1")

	call(s, "GET", "/tasks", "", "Authorization", "Bearer dct_nothere_abc", "User-Agent", "curl/8.7.1")
	check("invalid token", "api_status_401=1")

	for i := 0; i < 3; i++ {
		call(s, "GET", "/tasks", "", "Authorization", bearer, "User-Agent", "curl/8.7.1")
	}
	check("polling", "api_client_curl=3 api_get_tasks=3")

	call(s, "GET", "/tasks", "")
	call(s, "GET", "/me", "")
	check("web UI", "")

	// (no such task here, hence the 404; with the task it is the first two only)
	id := "9f86d081884c7d65"
	call(s, "GET", "/tasks/"+id+"/files", "", "Authorization", bearer, "User-Agent", "MyBot/1.0 (secret)")
	for k := range got {
		if strings.Contains(k, id) || strings.Contains(k, "mybot") || strings.Contains(k, "secret") {
			t.Errorf("request data in %q", k)
		}
	}
	check("task id and User-Agent", "api_client_other=1 api_get_tasks_id_files=1 api_status_404=1")
}
