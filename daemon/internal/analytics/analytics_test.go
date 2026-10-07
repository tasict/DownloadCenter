package analytics

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/store"
)

func testService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "dc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(core.New(db, dir), db, dir, "1.2.3", "x86_64")
}

// Every request stays within the Measurement Protocol limits, whatever was counted.
func TestBuildLimits(t *testing.T) {
	s := testService(t)
	counts := map[string]int64{}
	for i := 0; i < 300; i++ {
		counts[fmt.Sprintf("ui_k%03d", i)] = int64(i + 1)
		counts[fmt.Sprintf("add_k%03d", i)] = 1
	}
	reqs := s.build(counts, s.snapshot(), time.Now())
	if len(reqs) < 2 {
		t.Fatalf("expected several requests, got %d", len(reqs))
	}
	seen := 0
	for _, p := range reqs {
		if p.ClientID == "" || len(p.Events) > maxEvents {
			t.Fatalf("bad request: client %q, %d events", p.ClientID, len(p.Events))
		}
		for k, v := range p.UserProperties {
			if len(k) > maxPropName || len(fmt.Sprint(v["value"])) > maxPropValue {
				t.Fatalf("user property %s too long", k)
			}
		}
		for _, e := range p.Events {
			if len(e.Name) > maxNameLen || !nameRe.MatchString(e.Name) || len(e.Params) > 25 {
				t.Fatalf("event %s with %d params", e.Name, len(e.Params))
			}
			if e.Params["engagement_time_msec"] == nil || e.Params["session_id"] == nil {
				t.Fatalf("event %s lacks engagement_time_msec/session_id", e.Name)
			}
			for k, v := range e.Params {
				if len(k) > maxNameLen || !nameRe.MatchString(k) {
					t.Fatalf("bad parameter name %q", k)
				}
				if s, ok := v.(string); ok && len(s) > 100 {
					t.Fatalf("parameter %s too long", k)
				}
			}
			if k, _ := e.Params["key"].(string); e.Name == "usage" && (strings.HasPrefix(k, "ui_k") || strings.HasPrefix(k, "add_k")) {
				seen++
			}
		}
	}
	if seen != 600 {
		t.Fatalf("%d counters reported, want 600", seen)
	}
	if reqs[0].Events[1].Name != "install" {
		t.Fatalf("first report should carry install, got %s", reqs[0].Events[1].Name)
	}
}

// The UI can only add known counters, in bounded steps.
func TestUIWhitelist(t *testing.T) {
	s := testService(t)
	s.UI("alice", map[string]int64{"tab_peers": 3, "lang_ENG": 1, "lang_XX": 1, "/share/Download/secret.mkv": 1, "ui_evil": 1, "sort_eta": 1000, "preview_subs": 2, "subs_enc": 1,
		"token_agent": 1, "agent_copy_claude": 2, "agent_copy_other": 1, "skill_download": 1})
	if s.counts["ui_tab_peers"] != 3 || s.counts["ui_lang_eng"] != 1 || s.counts["ui_sort_eta"] != maxUIPerPost ||
		s.counts["ui_preview_subs"] != 2 || s.counts["ui_subs_enc"] != 1 || s.counts["ui_token_agent"] != 1 ||
		s.counts["ui_agent_copy_claude"] != 2 || s.counts["ui_agent_copy_other"] != 1 || s.counts["ui_skill_download"] != 1 {
		t.Fatalf("counts %v", s.counts)
	}
	if len(s.counts) != 9 {
		t.Fatalf("unexpected keys in %v", s.counts)
	}
	s.UI("alice", map[string]int64{"tab_log": 1}) // too soon after the last report
	if s.counts["ui_tab_log"] != 0 {
		t.Fatal("rate limit not applied")
	}
}

// Free text in events (token names, error messages, paths) never becomes a key.
func TestEventsCountWithoutContent(t *testing.T) {
	s := testService(t)
	s.event(core.Event{Type: "task.failed", Task: &core.EventTask{ID: "x", Name: "secret.mkv", Kind: "url"},
		Data: map[string]any{"error": map[string]any{"code": "/share/x y", "message": "secret"}}})
	s.event(core.Event{Type: "task.failed", Task: &core.EventTask{ID: "x", Kind: "url"}, Data: map[string]any{"error": map[string]any{"code": "dl_6"}}})
	s.event(core.Event{Type: "task.completed", Task: &core.EventTask{ID: "x", Kind: "torrent", Size: 3 << 20}})
	b, _ := json.Marshal(s.build(s.counts, nil, time.Now()))
	for _, bad := range []string{"secret", "/share"} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("payload contains %q: %s", bad, b)
		}
	}
	if s.counts["res_failed"] != 2 || s.counts["res_err_dl_6"] != 1 || s.counts["res_done_bt"] != 1 || s.counts["res_mb"] != 3 {
		t.Fatalf("counts %v", s.counts)
	}
	if via("API: my secret token") != "api" || via("Qget 2.0") != "v4" || via("Download Center") != "ui" {
		t.Fatal("via")
	}
}

// File-hosting adds are counted by service name only for known services.
func TestHosterKey(t *testing.T) {
	for h, want := range map[string]string{
		"gdrive": "add_hoster_gdrive", "1fichier": "add_hoster_1fichier", "cookies": "add_hoster_cookies",
		"unknown": "add_hoster_other", "/share/x": "add_hoster_other",
	} {
		if got := hosterKey(h); got != want {
			t.Errorf("hosterKey(%q) = %q, want %q", h, got, want)
		}
	}
}

// Turning it off stops counting and drops what was collected.
func TestDisable(t *testing.T) {
	s := testService(t)
	s.add("ui_session", 1)
	s.SetEnabled(false)
	s.add("ui_session", 1)
	if len(s.counts) != 0 || s.Enabled() || s.db.Meta(kAck) != "1" {
		t.Fatalf("counts %v enabled %v", s.counts, s.Enabled())
	}
	// The switch is cached; a restart reads it back from the database
	if New(s.m, s.db, s.data, s.version, s.arch).Enabled() {
		t.Fatal("reloaded service is on")
	}
	s.SetEnabled(true)
	if !s.Enabled() {
		t.Fatal("not re-enabled")
	}
}

// A report clears what it sent and records the version (upgrade next time).
func TestSendDryRun(t *testing.T) {
	s := testService(t)
	var sent [][]byte
	s.Post = func(u string, body []byte) error { sent = append(sent, body); return nil }
	MeasurementID, APISecret = "G-TEST", "secret"
	defer func() { MeasurementID, APISecret = "", "" }()
	s.add("ui_session", 2)
	if err := s.Send(); err != nil {
		t.Fatal(err)
	}
	if len(sent) == 0 || len(s.counts) != 0 || s.db.Meta(kVersion) != "1.2.3" {
		t.Fatalf("sent %d, counts %v, version %q", len(sent), s.counts, s.db.Meta(kVersion))
	}
	s.version = "1.3.0"
	reqs := s.build(nil, nil, time.Now())
	if reqs[0].Events[1].Name != "upgrade" || reqs[0].Events[1].Params["from_version"] != "1.2.3" {
		t.Fatalf("events %+v", reqs[0].Events)
	}
}

// Counters of every group reach the report under their group; others do not.
func TestBuildGroups(t *testing.T) {
	s := testService(t)
	counts := map[string]int64{"tok_created": 1, "api_get_tasks": 3, "v4_task_query": 2, "chat_add": 1, "zz_x": 1}
	got := map[string]string{}
	for _, p := range s.build(counts, nil, time.Now()) {
		for _, e := range p.Events {
			if e.Name == "usage" {
				got[e.Params["key"].(string)] = e.Params["group"].(string)
			}
		}
	}
	want := map[string]string{"tok_created": "tok", "api_get_tasks": "api", "v4_task_query": "v4", "chat_add": "chat"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("usage events %v, want %v", got, want)
	}
}

// Other packages can only count their own groups, and nothing while off.
func TestCountHook(t *testing.T) {
	s := testService(t)
	for _, k := range []string{"api_get_tasks", "ui_session", "/share/x", "add_https", "tok_created"} {
		s.count(k)
	}
	if len(s.counts) != 2 || s.counts["api_get_tasks"] != 1 || s.counts["tok_created"] != 1 {
		t.Fatalf("counts %v", s.counts)
	}
	s.SetEnabled(false)
	s.count("api_get_tasks")
	if len(s.counts) != 0 {
		t.Fatalf("counted while off: %v", s.counts)
	}
}

// Tokens are described by how many there are, are in use and hold each scope.
func TestTokenState(t *testing.T) {
	s := testService(t)
	au := auth.New(s.db)
	now := time.Now().Unix()
	for i, tk := range []struct {
		owner  string
		scopes []string
		used   int64
	}{
		{"alice", []string{"tasks:read", "tasks:add"}, now - 3*3600},
		{"alice", []string{"tasks:read", "files:delete"}, now - 60*86400},
		{"bob", []string{"stats:read"}, 0},
	} {
		tok := &auth.Token{Owner: tk.owner, Name: fmt.Sprintf("t%d", i), Scopes: tk.scopes}
		if _, err := au.CreateToken(tok, false); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.X(`UPDATE tokens SET last_used_at = ? WHERE id = ?`, tk.used, tok.ID); err != nil {
			t.Fatal(err)
		}
	}
	st := s.snapshot()
	want := map[string]int64{"st_tokens": 3, "st_tokens_1d": 1, "st_tokens_30d": 1, "st_token_owners": 2,
		"st_tok_tasks_read": 2, "st_tok_tasks_add": 1, "st_tok_files_delete": 1, "st_tok_stats_read": 1, "st_tok_settings_write": 0}
	for k, v := range want {
		if n, ok := st[k]; !ok || n != v {
			t.Errorf("%s = %d (present %v), want %d", k, n, ok, v)
		}
	}
}
