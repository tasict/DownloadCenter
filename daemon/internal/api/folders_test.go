package api

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/qts"
	"downloadcenter/internal/store"
)

// A token limited to some folders: the pickers show only those, and a task
// cannot reach another folder through the default "move completed to".
// Runs on a NAS with two writable shares.
func TestTokenFolderLimit(t *testing.T) {
	var shares []qts.Share
	for _, sh := range qts.Shares() {
		if core.Choosable(sh.Path) {
			shares = append(shares, sh)
		}
	}
	if len(shares) < 2 {
		t.Skip("needs two writable shared folders")
	}
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "dc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := core.New(db, dir)
	st := m.Settings()
	st.TempDir, st.MoveDir = shares[0].Path, shares[1].Path
	if err := m.SaveSettings(st); err != nil {
		t.Fatal(err)
	}
	s := New(m, auth.New(db), dir, "test")
	p := &auth.Principal{User: "bot", Admin: true, Via: "token", Folders: []string{shares[0].Name}}

	if s.checkFolders(p, &core.AddOptions{}) {
		t.Error("the default move folder outside the allowlist was accepted")
	}
	if !s.checkFolders(p, &core.AddOptions{MoveSet: true}) {
		t.Error("staying in the allowed folder was refused")
	}
	if s.checkFolders(p, &core.AddOptions{TempDir: shares[1].Name, MoveSet: true}) {
		t.Error("a temporary folder outside the allowlist was accepted")
	}

	list := func(path string) (int, map[string]any) {
		w := httptest.NewRecorder()
		s.folders(w, httptest.NewRequest("GET", "/x?path="+path, nil), p)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	code, out := list("")
	fs, _ := out["folders"].([]any)
	if code != 200 || len(fs) != 1 || fs[0].(map[string]any)["path"] != shares[0].Name {
		t.Errorf("top level for a limited token: %d %v", code, out)
	}
	if code, _ := list(shares[0].Name); code != 200 {
		t.Errorf("allowed folder: %d", code)
	}
	if code, _ := list(shares[1].Name); code != 403 {
		t.Errorf("folder outside the allowlist: %d", code)
	}

	p.Folders = append(p.Folders, shares[1].Name)
	if !s.checkFolders(p, &core.AddOptions{}) {
		t.Error("both folders allowed, still refused")
	}
}
