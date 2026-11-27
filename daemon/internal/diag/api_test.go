package diag

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/store"
)

func testHandlers(t *testing.T) *handlers {
	t.Helper()
	qtsEventLog, kernelLog, dmesgCmd = "/nonexistent/event.log", "/nonexistent/kmsg", "/nonexistent/dmesg"
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	os.MkdirAll(filepath.Join(data, "logs"), 0700)
	db, err := store.Open(filepath.Join(data, "dc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &handlers{src: Source{M: core.New(db, data), Root: dir, Data: data, Version: "1.4.3", Started: time.Now()}}
}

func TestHandlersRefuse(t *testing.T) {
	h := testHandlers(t)
	for name, p := range map[string]*auth.Principal{
		"AI agent":     {User: "admin", Admin: true, Via: "session", Agent: true},
		"token":        {User: "admin", Admin: true, Via: "token"},
		"V4":           {User: "admin", Admin: true, Via: "v4"},
		"regular user": {User: "bob", Via: "session"},
	} {
		for _, f := range []func(*httptest.ResponseRecorder){
			func(w *httptest.ResponseRecorder) { h.logs(w, httptest.NewRequest("GET", "/diagnostics/logs", nil), p) },
			func(w *httptest.ResponseRecorder) {
				h.code(w, httptest.NewRequest("GET", "/diagnostics/code/01234567", nil), p)
			},
		} {
			w := httptest.NewRecorder()
			f(w)
			if w.Code != 403 || w.Header().Get("Content-Type") == "application/zip" {
				t.Errorf("%s: HTTP %d %s", name, w.Code, w.Header().Get("Content-Type"))
			}
		}
	}
}

func TestHandlersAdmin(t *testing.T) {
	h := testHandlers(t)
	p := &auth.Principal{User: "admin", Admin: true, Via: "session"}
	w := httptest.NewRecorder()
	h.logs(w, httptest.NewRequest("GET", "/diagnostics/logs", nil), p)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/zip" || w.Header().Get("Cache-Control") != "no-store" ||
		!strings.HasPrefix(w.Header().Get("Content-Disposition"), `attachment; filename="DownloadCenter-logs-1.4.3-`) || !strings.HasPrefix(w.Body.String(), "PK") {
		t.Fatalf("HTTP %d %v", w.Code, w.Header())
	}
	// One export at a time
	h.busy.Lock()
	w = httptest.NewRecorder()
	h.logs(w, httptest.NewRequest("GET", "/diagnostics/logs", nil), p)
	h.busy.Unlock()
	if w.Code != 429 {
		t.Fatalf("second export at once: HTTP %d", w.Code)
	}
	w = httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/diagnostics/code/01234567", nil)
	r.SetPathValue("code", "01234567")
	h.code(w, r, p)
	if w.Code != 404 {
		t.Fatalf("unknown code: HTTP %d", w.Code)
	}
}

func TestExportReadsOnlyItsList(t *testing.T) {
	h := testHandlers(t)
	logs := filepath.Join(h.src.Data, "logs")
	os.WriteFile(filepath.Join(h.src.Data, "dc.db.secret"), []byte("2026/11/27 10:00:00 not a log\n"), 0600)
	os.WriteFile(filepath.Join(logs, "other.log"), []byte(time.Now().Format("2006/01/02 15:04:05")+" not on the list\n"), 0600)
	// A link in place of a log is not followed
	os.Symlink(filepath.Join(h.src.Data, "dc.db.secret"), filepath.Join(logs, "dc-bt.log"))
	big := strings.Repeat(time.Now().Format("2006/01/02 15:04:05")+" filler line\n", 300000)
	os.WriteFile(filepath.Join(logs, "dcd.log"), []byte(big), 0600)
	w := httptest.NewRecorder()
	h.logs(w, httptest.NewRequest("GET", "/diagnostics/logs", nil), &auth.Principal{User: "admin", Admin: true, Via: "session"})
	files := unzip(t, w.Body.Bytes())
	if _, ok := files["other.log.txt"]; ok {
		t.Error("a file outside the list was exported")
	}
	if _, ok := files["dc-bt.log.txt"]; ok || strings.Contains(strings.Join(values(files), ""), "not a log") {
		t.Error("a link was followed")
	}
	if n := len(files["dcd.log.txt"]); n == 0 || n > fileTail {
		t.Errorf("dcd.log: %d bytes, want 1..%d", n, fileTail)
	}
}
