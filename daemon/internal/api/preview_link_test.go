package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
)

// A finished file swapped for a link to a file outside the task is not
// served by the preview or listed as an archive.
func TestPreviewRefusesLinks(t *testing.T) {
	s := fixServer(t)
	root := t.TempDir()
	outside := filepath.Join(root, "secret.txt")
	os.WriteFile(outside, []byte("OUTSIDE-CONTENT"), 0600)
	dl := filepath.Join(root, "Download")
	os.MkdirAll(dl, 0755)
	data := filepath.Join(dl, "notes.txt")
	os.WriteFile(data, []byte("mine"), 0644)
	hash := "1111111111111111111111111111111111111111"
	if err := s.M.ImportTask(core.ImportSpec{Hash: hash, Owner: "bob", Kind: "http", Source: "http://example.invalid/notes.txt",
		Name: "notes.txt", TempDir: dl, DataPath: data, Size: 4, Done: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.M.Undo(hash); err != nil {
		t.Fatal(err)
	}
	serve := func() (int, string) {
		req := httptest.NewRequest("GET", "/x?file=0", nil)
		req.SetPathValue("id", hash)
		rec := httptest.NewRecorder()
		s.preview(rec, req, &auth.Principal{User: "bob", Via: "session"})
		return rec.Code, rec.Body.String()
	}
	if code, body := serve(); code != 200 || body != "mine" {
		t.Fatalf("own file: %d %q", code, body)
	}
	os.Remove(data)
	os.Symlink(outside, data)
	if code, body := serve(); code != 403 {
		t.Errorf("through a link: %d %q", code, body)
	}
	req := httptest.NewRequest("GET", "/x?file=0", nil)
	req.SetPathValue("id", hash)
	rec := httptest.NewRecorder()
	s.archiveList(rec, req, &auth.Principal{User: "bob", Via: "session"})
	if rec.Code != 403 {
		t.Errorf("archive through a link: %d", rec.Code)
	}
}
