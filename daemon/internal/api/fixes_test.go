package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/engine"
	"downloadcenter/internal/store"
)

func fixServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "dc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(core.New(db, dir), auth.New(db), dir, "test")
}

// /me says "all" only when the caller really sees everyone's tasks.
func TestMeTasksScope(t *testing.T) {
	s := fixServer(t)
	for _, c := range []struct {
		admin bool
		want  string
	}{{false, "own"}, {true, "all"}} {
		rec := httptest.NewRecorder()
		s.me(rec, httptest.NewRequest("GET", "/x", nil), &auth.Principal{User: "bob", Admin: c.admin, Via: "session", AllTasks: true})
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		if out["tasks"] != c.want {
			t.Errorf("admin %v: tasks %v, want %s", c.admin, out["tasks"], c.want)
		}
	}
}

// An unknown bulk action is refused even when no task matches.
func TestBulkUnknownAction(t *testing.T) {
	s := fixServer(t)
	rec := httptest.NewRecorder()
	b, _ := json.Marshal(map[string]any{"ids": []string{}, "action": "explode"})
	s.bulk(rec, httptest.NewRequest("POST", "/x", bytes.NewReader(b)), &auth.Principal{User: "admin", Admin: true, Via: "session", AllTasks: true})
	if rec.Code != 400 {
		t.Errorf("status %d", rec.Code)
	}
}

// Removing a task whose files are being moved has its own code and message.
func TestMovingTaskError(t *testing.T) {
	status, code, msg := coreError(core.ErrMoving)
	if status != 409 || code != "task_moving" || msg == "task_moving" {
		t.Errorf("%d %s %q", status, code, msg)
	}
}

func TestAnchorOf(t *testing.T) {
	cases := []struct {
		in     map[string]any
		anchor string
		after  bool
		ok     bool
	}{
		{map[string]any{"before": "x"}, "x", false, true},
		{map[string]any{"after": "y"}, "y", true, true},
		{map[string]any{"before": "x", "after": "y"}, "", false, false},
		{map[string]any{"before": ""}, "", false, false},
		{map[string]any{"before": 3.0}, "", false, false},
		{map[string]any{}, "", false, false},
	}
	for _, c := range cases {
		a, after, ok := anchorOf(c.in)
		if a != c.anchor || after != c.after || ok != c.ok {
			t.Errorf("%v -> %q %v %v", c.in, a, after, ok)
		}
	}
}

// A bulk move needs exactly one anchor.
func TestBulkMoveNeedsAnchor(t *testing.T) {
	s := fixServer(t)
	rec := httptest.NewRecorder()
	b, _ := json.Marshal(map[string]any{"ids": []string{}, "action": "move"})
	s.bulk(rec, httptest.NewRequest("POST", "/x", bytes.NewReader(b)), &auth.Principal{User: "admin", Admin: true, Via: "session", AllTasks: true})
	if rec.Code != 400 {
		t.Errorf("status %d %s", rec.Code, rec.Body.String())
	}
}

// identEngine is a torrent engine that reports its own client identity.
type identEngine struct{ engine.Engine }

func (identEngine) Name() string                  { return "libtorrent" }
func (identEngine) Version() string               { return "2.0.15.0" }
func (identEngine) Caps() engine.Caps             { return engine.Caps{Torrents: true} }
func (identEngine) Health() error                 { return nil }
func (identEngine) OwnIdentity() (string, string) { return "-LT20F0-", "libtorrent/2.0.15.0" }

// The settings say what the default client identity sends.
func TestSettingsBTIdentity(t *testing.T) {
	s := fixServer(t)
	admin := &auth.Principal{User: "admin", Admin: true, Via: "session", AllTasks: true}
	get := func() map[string]any {
		rec := httptest.NewRecorder()
		s.getSettings(rec, httptest.NewRequest("GET", "/x", nil), admin)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	if _, ok := get()["bt_identity"]; ok {
		t.Error("bt_identity without a torrent engine")
	}
	s.M.Engines["libtorrent"] = identEngine{}
	id, _ := get()["bt_identity"].(map[string]any)
	if id["peer_id"] != "-LT20F0-" || id["user_agent"] != "libtorrent/2.0.15.0" {
		t.Errorf("bt_identity %v", id)
	}
}
