package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
)

// A caller limited to URLs cannot add a torrent through a link to a
// .torrent file, nor look up a magnet link's files.
func TestSourceLimitsHold(t *testing.T) {
	s := fixServer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("d4:infod6:lengthi1e4:name1:a12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaaee"))
	}))
	defer srv.Close()
	// Unguarded (an administrator's own session) so that the test server on
	// loopback can be reached; the source list is what is tested
	p := &auth.Principal{User: "admin", Admin: true, AllTasks: true, Via: "session", Sources: []string{"url"}}
	out := s.addOne(p, srv.URL+"/x.torrent", core.AddOptions{Owner: "admin", Admin: true, AllTasks: true})
	if out.Error == nil || out.Error["code"] != "source_not_allowed" {
		t.Errorf("torrent through a link: %+v", out)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"magnet":"magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"}`))
	s.probe(rec, req, p)
	if rec.Code != 403 {
		t.Errorf("magnet probe: %d %s", rec.Code, rec.Body.String())
	}
}
