package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
)

func tokenAdmin() *auth.Principal {
	return &auth.Principal{User: "admin", Admin: true, AllTasks: true, Via: "token", Token: &auth.Token{Name: "t"}}
}

// webmcpAdmin is an administrator's browser session driven by the page's AI
// agent tools (marked X-DC-Via: webmcp).
func webmcpAdmin() *auth.Principal {
	return &auth.Principal{User: "admin", Admin: true, AllTasks: true, Via: "session", Agent: true}
}

// Only an administrator's own session may read a page on the NAS itself:
// not a token of theirs, not the AI agent in their browser.
func TestExtractGuardedForPrograms(t *testing.T) {
	s := fixServer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<a href="/secret.iso">x</a>`))
	}))
	defer srv.Close()
	for _, c := range []struct {
		p    *auth.Principal
		want int
	}{{tokenAdmin(), 502}, {webmcpAdmin(), 502}, {trkAdmin(), 200}} {
		body, _ := json.Marshal(map[string]string{"url": srv.URL + "/page"})
		rec := httptest.NewRecorder()
		s.extract(rec, httptest.NewRequest("POST", "/x", bytes.NewReader(body)), c.p)
		if rec.Code != c.want {
			t.Errorf("%s agent=%v: %d %s", c.p.Via, c.p.Agent, rec.Code, rec.Body.String())
		}
	}
}

// Trackers on loopback or the NAS itself are refused when a program adds
// them, even to an administrator's own torrent that has no guard.
func TestProgramTrackersFiltered(t *testing.T) {
	s := trackerServer(t)
	id := magnetOf(t, s, "admin", "0123456789abcdef0123456789abcdef01234567")
	for _, p := range []*auth.Principal{tokenAdmin(), webmcpAdmin()} {
		code, out := postTrackers(s, p, id, map[string]any{"trackers": []string{"http://127.0.0.1:9/announce"}})
		if code == 200 {
			t.Errorf("%s agent=%v: loopback tracker accepted %v", p.Via, p.Agent, out)
		}
	}
	if code, out := postTrackers(s, trkAdmin(), id, map[string]any{"trackers": []string{"http://127.0.0.1:9/announce"}}); code != 200 {
		t.Errorf("administrator's session: %d %v", code, out)
	}
	// A duplicate magnet added by a program merges no such tracker
	_, err := s.M.AddMagnet("magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&tr=http://127.0.0.1:8/announce&tr=udp://t.example.org:1/announce",
		core.AddOptions{Owner: "admin", Admin: true, AllTasks: true, Guard: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range s.M.Live(id).Options.Trackers {
		if tr == "http://127.0.0.1:8/announce" {
			t.Error("a program merged a loopback tracker")
		}
	}
}

// The page's AI agent tools add like a token does: guarded, and the task
// says the AI agent added it.
func TestWebMCPAddsLikeAProgram(t *testing.T) {
	r := httptest.NewRequest("POST", "/x", nil)
	r.Header.Set("X-DC-Via", "webmcp")
	p := &auth.Principal{User: "admin", Admin: true, AllTasks: true, Via: "session"}
	markAgent(p, r)
	o := (&addBody{}).options(p, r)
	if !o.Guard || o.Caller != "AI agent" {
		t.Errorf("guard %v caller %q", o.Guard, o.Caller)
	}
	// A token cannot claim to be the page
	tp := tokenAdmin()
	markAgent(tp, r)
	if o := (&addBody{}).options(tp, r); o.Caller != "API: t" {
		t.Errorf("token caller %q", o.Caller)
	}
}

// Uploaded .torrent files need a token that may add torrents, for a look
// at their files and for the duplicate check too.
func TestTorrentUploadsNeedTorrentSource(t *testing.T) {
	s := fixServer(t)
	p := &auth.Principal{User: "admin", Admin: true, AllTasks: true, Via: "token", Token: &auth.Token{Name: "t"}, Sources: []string{"url"}}
	tor := "d4:infod6:lengthi1e4:name1:a12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaaee"
	for name, h := range map[string]Handler{"probe": s.probe, "check": s.checkTasks} {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", "a.torrent")
		fw.Write([]byte(tor))
		mw.Close()
		req := httptest.NewRequest("POST", "/x", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := httptest.NewRecorder()
		h(rec, req, p)
		if rec.Code != 403 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

// Whether a probe may look at another owner's task follows what the
// caller sees (all tasks or its own), not whether it is guarded.
func TestProbeSeesLikeTheCaller(t *testing.T) {
	s := trackerServer(t)
	id := magnetOf(t, s, "alice", "0123456789abcdef0123456789abcdef01234567")
	magnet := "magnet:?xt=urn:btih:" + id
	// Its file list is known: a caller that may see it gets it right away
	os.WriteFile(filepath.Join(s.M.TorrentDir(), id+".torrent"), []byte("d4:infod6:lengthi1e4:name1:a12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaaee"), 0600)
	probe := func(p *auth.Principal) int {
		b, _ := json.Marshal(map[string]string{"magnet": magnet})
		rec := httptest.NewRecorder()
		s.probe(rec, httptest.NewRequest("POST", "/x", bytes.NewReader(b)), p)
		return rec.Code
	}
	all := &auth.Principal{User: "admin", Admin: true, AllTasks: true, Via: "token", Token: &auth.Token{Name: "t"}}
	own := &auth.Principal{User: "admin", Admin: true, Via: "token", Token: &auth.Token{Name: "t"}}
	if c := probe(all); c != 200 {
		t.Errorf("a token that sees every task: %d", c)
	}
	if c := probe(own); c != 409 {
		t.Errorf("a token limited to its own tasks: %d", c)
	}
}

// A task's merged sources are shown without the password of an address,
// like its own source.
func TestSourcesHidePasswords(t *testing.T) {
	s := fixServer(t)
	s.DevUser = "admin"
	hash := "2222222222222222222222222222222222222222"
	if err := s.M.ImportTask(core.ImportSpec{Hash: hash, Owner: "admin", Kind: "http", Source: "https://example.org/a", Name: "a", TempDir: t.TempDir(),
		Size: 1, Done: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.M.Undo(hash); err != nil {
		t.Fatal(err)
	}
	s.M.DB().X(`INSERT INTO task_sources (task_hash, source_hash, kind, source, name, active, added_at) VALUES (?,?,?,?,?,1,1)`,
		hash, hash, "url", "ftp://joe:s3cret@ftp.example.org/a.iso", "a")
	req := httptest.NewRequest("GET", APIBase+"/tasks/"+hash, nil)
	req.RemoteAddr = "127.0.0.1:5000"
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte("ftp.example.org")) || bytes.Contains(rec.Body.Bytes(), []byte("s3cret")) {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
}
