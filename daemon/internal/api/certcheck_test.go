package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/engine"
)

// anyURL stands in for the URL engine (only its protocol check is used).
type anyURL struct{ engine.Engine }

func (anyURL) Name() string                    { return "builtin" }
func (anyURL) Supports(string) bool            { return true }
func (anyURL) ApplyGlobal(engine.Global) error { return nil }

// skip_cert_check: absent means checked, anyone adding may ask to skip, a
// URL task reports it, PATCH changes it for those who see the task.
func TestSkipCertCheckAPI(t *testing.T) {
	s := fixServer(t)
	s.M.URL = anyURL{}
	st := s.M.Settings()
	st.TempDir = t.TempDir()
	s.M.SaveSettings(st)
	admin := &auth.Principal{User: "admin", Admin: true, Via: "session", AllTasks: true}
	add := func(p *auth.Principal, body map[string]any) map[string]any {
		t.Helper()
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		s.addTasks(&statusRecorder{ResponseWriter: rec, status: 200}, httptest.NewRequest("POST", "/x", bytes.NewReader(b)), p)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != 200 {
			t.Fatalf("add: %d %s", rec.Code, rec.Body)
		}
		return out
	}
	idOf := func(out map[string]any) string {
		if id, ok := out["id"].(string); ok {
			return id
		}
		return out["results"].([]any)[0].(map[string]any)["id"].(string)
	}
	plain := idOf(add(admin, map[string]any{"source": "https://dl.example.com/a.bin", "start": false}))
	skipped := idOf(add(admin, map[string]any{"source": "https://dl.example.com/b.bin", "start": false, "skip_cert_check": true}))
	if s.M.Live(plain).Options.SkipCertCheck || !s.M.Live(skipped).Options.SkipCertCheck {
		t.Fatal("stored values wrong")
	}
	j := s.taskJSON(admin, s.M.Live(plain), false)
	if j.SkipCert == nil || *j.SkipCert {
		t.Fatalf("task JSON: %v", j.SkipCert)
	}
	// A task stopped by a refused certificate says so in a field (its
	// message reaches clients translated)
	failed := *s.M.Live(plain)
	failed.State, failed.ErrorCode, failed.ErrorMsg = core.StError, "dl_1", "Download failed (unknown error) (The certificate of dl.example.com has expired or is not valid yet; check the NAS's date and time)"
	if j := s.taskJSON(admin, &failed, false); j.Error["certificate"] != true {
		t.Fatalf("error: %v", j.Error)
	}
	failed.ErrorMsg = "Download failed (unknown error) (The server's certificate cannot be verified; the list of trusted certificate authorities on the NAS may be out of date)"
	if j := s.taskJSON(admin, &failed, false); j.Error["certificate"] != true {
		t.Fatalf("1.4.3 wording: %v", j.Error)
	}
	failed.ErrorMsg = "File not found (404) (status=404)"
	if j := s.taskJSON(admin, &failed, false); j.Error["certificate"] != nil {
		t.Fatalf("other error: %v", j.Error)
	}
	// A regular user's request is read the same way
	var b addBody
	json.Unmarshal([]byte(`{"source":"https://x/y","skip_cert_check":true}`), &b)
	if !b.options(&auth.Principal{User: "bob", Via: "session"}, httptest.NewRequest("POST", "/x", nil)).SkipCertCheck {
		t.Fatal("a regular user cannot skip")
	}

	patch := func(p *auth.Principal, id, body string) int {
		req := httptest.NewRequest("PATCH", "/x", bytes.NewReader([]byte(body)))
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		s.patchTask(&statusRecorder{ResponseWriter: rec, status: 200}, req, p)
		return rec.Code
	}
	if code := patch(admin, plain, `{"skip_cert_check":true}`); code != 200 || !s.M.Live(plain).Options.SkipCertCheck {
		t.Fatalf("patch: %d", code)
	}
	// Someone who does not see the task gets "not found" and changes nothing
	if code := patch(&auth.Principal{User: "bob", Via: "session"}, skipped, `{"skip_cert_check":false}`); code != 404 || !s.M.Live(skipped).Options.SkipCertCheck {
		t.Fatalf("other owner: %d", code)
	}
}
