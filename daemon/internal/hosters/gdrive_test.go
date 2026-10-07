package hosters

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestDriveLinks(t *testing.T) {
	const id = "1AbC_d-EfGhIjKlMnOp"
	s, _, _ := setup(t)
	cases := []struct {
		url, id, key string
		folder, ok   bool
	}{
		{"https://drive.google.com/file/d/" + id + "/view?usp=sharing", id, "", false, true},
		{"https://drive.google.com/file/d/" + id + "/edit", id, "", false, true},
		{"https://drive.google.com/file/d/" + id, id, "", false, true},
		{"https://drive.google.com/file/u/1/d/" + id + "/view", id, "", false, true},
		{"http://drive.google.com/open?id=" + id, id, "", false, true},
		{"https://drive.google.com/uc?id=" + id + "&export=download", id, "", false, true},
		{"https://docs.google.com/uc?export=download&id=" + id, id, "", false, true},
		{"https://drive.usercontent.google.com/download?id=" + id + "&export=download", id, "", false, true},
		{"https://drive.usercontent.google.com/u/0/uc?id=" + id + "&export=download", id, "", false, true},
		{"https://drive.google.com/file/d/" + id + "/view?usp=sharing&resourcekey=0-abcDEF", id, "0-abcDEF", false, true},
		{"https://drive.google.com/drive/folders/" + id + "?usp=sharing", id, "", true, true},
		{"https://drive.google.com/drive/u/2/folders/" + id, id, "", true, true},
		{"https://drive.google.com/folderview?id=" + id, id, "", true, true},
		{"https://docs.google.com/document/d/" + id + "/edit", "", "", false, false},
		{"https://drive.google.com/drive/my-drive", "", "", false, false},
		{"https://drive.google.com/file/d/short/view", "", "", false, false},
		{"https://drive.google.com/open?id=bad!" + id, "", "", false, false},
		{"ftp://drive.google.com/file/d/" + id, "", "", false, false},
		{"https://example.com/file/d/" + id + "/view", "", "", false, false},
		{"https://drive.google.com.example.com/file/d/" + id + "/view", "", "", false, false},
	}
	for _, c := range cases {
		l, ok := driveLinkOf(c.url)
		if ok != c.ok || (ok && (l.id != c.id || l.key != c.key || l.folder != c.folder)) {
			t.Errorf("driveLinkOf(%s) = %+v,%v", c.url, l, ok)
		}
		svc, mok := s.Match(c.url)
		if mok != c.ok || (mok && svc != GDrive) {
			t.Errorf("Match(%s) = %q,%v", c.url, svc, mok)
		}
	}
	// Without an account only Drive links are resolved
	if r, err := s.ResolvePublic("https://1fichier.com/?abcdef", ""); r != nil || err != nil {
		t.Errorf("ResolvePublic(1fichier) = %+v, %v", r, err)
	}
}

// driveMock answers like drive.usercontent.google.com, by file id.
func driveMock(t *testing.T) (*Service, *httptest.Server, func() int) {
	var mu sync.Mutex
	hits := 0
	page := func(w http.ResponseWriter, status int, body string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		io.WriteString(w, "<!DOCTYPE html><html><head><title>Google Drive</title></head><body>"+body+"</body></html>")
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		q := r.URL.Query()
		if r.URL.Path != "/download" || q.Get("export") != "download" || q.Get("confirm") != "t" || r.Header.Get("Range") != "bytes=0-0" {
			http.Error(w, "bad request "+r.URL.String(), 400)
			return
		}
		file := func(name string) {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", name)
			w.Header().Set("Content-Range", "bytes 0-0/12345")
			w.WriteHeader(206)
			io.WriteString(w, "x")
		}
		scan := `<form id="download-form" action="` + srv.URL + `/download" method="get">` +
			`<input type="submit" id="uc-download-link" value="Download anyway"/>` +
			`<input type="hidden" name="id" value="` + q.Get("id") + `">` +
			`<input type="hidden" name="export" value="download">` +
			`<input type="hidden" name="confirm" value="t">` +
			`<input type="hidden" name="uuid" value="u&amp;1"></form>`
		switch q.Get("id") {
		case "fileRanged0001":
			file(`attachment; filename="a b.bin"`)
		case "fileUtf8Name01":
			file(`attachment; filename*=UTF-8''%E6%B8%AC%E8%A9%A6.zip`)
		case "fileWhole00001":
			w.Header().Set("Content-Type", "application/zip")
			w.Header().Set("Content-Length", "777")
			w.WriteHeader(200)
			w.Write(make([]byte, 777))
		case "keyedFile0001":
			if q.Get("resourcekey") != "0-RK" {
				page(w, 404, "Not Found")
				return
			}
			file(`attachment; filename="k.bin"`)
		case "scanAgain0001":
			if q.Get("uuid") == "u&1" {
				file(`attachment; filename="big.iso"`)
				return
			}
			page(w, 200, "<p class=\"uc-warning-caption\">Google Drive can't scan this file for viruses.</p>"+scan)
		case "scanLoop00001":
			page(w, 200, scan)
		case "private000001":
			http.Redirect(w, r, "https://accounts.google.com/ServiceLogin?continue=x", http.StatusFound)
		case "sharedWithMe1":
			// Shared with one Google account: only its session gets the file
			if r.Header.Get("Cookie") != "SID=secret" {
				http.Redirect(w, r, "https://accounts.google.com/ServiceLogin?continue=x", http.StatusFound)
				return
			}
			file(`attachment; filename="mine.bin"`)
		case "forbidden0001":
			page(w, 403, "You need access")
		case "missing000001":
			page(w, 404, "The requested URL was not found on this server.")
		case "limited000001":
			w.WriteHeader(429)
		case "quotaPage0001":
			page(w, 200, "<p>Sorry, you can't view or download this file at this time.</p><p>Too many users have viewed or downloaded this file recently. Please try accessing the file again later.</p>")
		case "unknownPage01":
			page(w, 200, "<p>Something else</p>")
		case "broken0000001":
			w.WriteHeader(500)
		case "foreign000001":
			http.Redirect(w, r, "http://example.invalid/x", http.StatusFound)
		default:
			page(w, 404, "Not Found")
		}
	}))
	t.Cleanup(srv.Close)
	s, _, _ := setup(t)
	s.base[GDrive] = srv.URL + "/download"
	s.client = func() *http.Client { return srv.Client() }
	s.proxyClient = func(string) *http.Client { return srv.Client() }
	return s, srv, func() int { mu.Lock(); defer mu.Unlock(); return hits }
}

func TestDriveResolve(t *testing.T) {
	s, srv, hits := driveMock(t)
	link := func(id string) string { return "https://drive.google.com/file/d/" + id + "/view?usp=sharing" }
	for _, c := range []struct {
		id, name string
		size     int64
	}{
		{"fileRanged0001", "a b.bin", 12345},
		{"fileUtf8Name01", "測試.zip", 12345},
		{"fileWhole00001", "", 777},
		{"scanAgain0001", "big.iso", 12345},
	} {
		res, err := s.Resolve("alice", "", link(c.id), "")
		if err != nil {
			t.Errorf("%s: %v", c.id, err)
			continue
		}
		if res.Name != c.name || res.Size != c.size || !res.NoPages || len(res.Headers) > 0 || res.Account != "" || res.ExpiresAt != 0 ||
			!strings.HasPrefix(res.URL, srv.URL+"/download?") || !strings.Contains(res.URL, "id="+c.id) {
			t.Errorf("%s: %+v", c.id, res)
		}
	}
	res, err := s.Resolve("alice", "", link("keyedFile0001")+"&resourcekey=0-RK", "")
	if err != nil || res.Name != "k.bin" {
		t.Errorf("resource key: %+v %v", res, err)
	}
	for _, c := range []struct {
		id   string
		want error
	}{
		{"scanLoop00001", ErrDrivePage},
		{"private000001", ErrDrivePrivate},
		{"forbidden0001", ErrDrivePrivate},
		{"missing000001", ErrFileMissing},
		{"limited000001", ErrDriveQuota},
		{"quotaPage0001", ErrDriveQuota},
		{"unknownPage01", ErrDrivePage},
		{"broken0000001", ErrTryLater},
		{"foreign000001", ErrDrivePage},
	} {
		if _, err := s.Resolve("alice", "", link(c.id), ""); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.id, err, c.want)
		}
	}
	// An account id that is not a fitting cookies account changes nothing
	if res, err := s.Resolve("alice", "someone-elses", link("fileRanged0001"), ""); err != nil || len(res.Headers) > 0 {
		t.Errorf("with an account id: %+v %v", res, err)
	}

	// A cookies account for Google is used like for any other site; the
	// cookies are the ones for Drive's download address
	shared := link("sharedWithMe1")
	if _, err := s.Resolve("alice", "", shared, ""); !errors.Is(err, ErrDrivePrivate) {
		t.Errorf("shared file without cookies: %v", err)
	}
	f := s.b.(*fakeBackend)
	jar := "# Netscape HTTP Cookie File\n" +
		".google.com\tTRUE\t/\tTRUE\t4102444800\tSID\tsecret\n" +
		"drive.google.com\tFALSE\t/\tTRUE\t4102444800\tDRIVE\tsecret\n"
	c := f.add("c1", "alice", Cookies, "", jar)
	c.Host = "drive.google.com"
	if svc, ok := s.Match(shared); !ok || svc != GDrive {
		t.Errorf("with a Google cookies account: Match = %q,%v", svc, ok)
	}
	for _, acct := range []string{"", "c1", "someone-elses"} {
		res, err := s.Resolve("alice", acct, shared, "")
		if err != nil || res.Name != "mine.bin" || res.Account != "c1" || len(res.Headers) != 1 || res.Headers[0] != "Cookie: SID=secret" || !res.NoPages {
			t.Errorf("with cookies (account %q): %+v %v", acct, res, err)
		}
	}
	// Never someone else's cookies, never without an account, never a
	// disabled account or one for another site
	if _, err := s.Resolve("bob", "c1", shared, ""); !errors.Is(err, ErrDrivePrivate) {
		t.Errorf("another user's cookies: %v", err)
	}
	if res, err := s.ResolvePublic(shared, ""); !errors.Is(err, ErrDrivePrivate) || res != nil {
		t.Errorf("no account: %+v %v", res, err)
	}
	if res, err := s.ResolvePublic(link("fileRanged0001"), ""); err != nil || len(res.Headers) > 0 || res.Account != "" {
		t.Errorf("no account, public file: %+v %v", res, err)
	}
	c.Host = "example.com"
	if _, err := s.Resolve("alice", "c1", shared, ""); !errors.Is(err, ErrDrivePrivate) {
		t.Errorf("cookies for another site: %v", err)
	}
	c.Host, c.Enabled = "", false
	if _, err := s.Resolve("alice", "", shared, ""); !errors.Is(err, ErrDrivePrivate) {
		t.Errorf("disabled cookies account: %v", err)
	}
	// Folders are refused without asking Google
	n := hits()
	if _, err := s.Resolve("alice", "", "https://drive.google.com/drive/folders/1XyZaBcDeFgHiJ", ""); !errors.Is(err, ErrDriveFolder) {
		t.Errorf("folder: %v", err)
	}
	if hits() != n {
		t.Error("folder link sent a request")
	}
}

func TestDriveServices(t *testing.T) {
	s, _, _ := setup(t)
	for _, svc := range s.Services() {
		if svc["id"] == GDrive {
			if svc["no_account"] != true || fmt.Sprint(svc["hosts"]) != "[drive.google.com drive.usercontent.google.com]" {
				t.Errorf("gdrive service: %v", svc)
			}
			return
		}
	}
	t.Error("no gdrive service")
}
