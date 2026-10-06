package v4

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"downloadcenter/internal/api"
	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/engine"
	"downloadcenter/internal/qts"
	"downloadcenter/internal/store"
)

// stubURL stands in for the URL engine: the manager loop does not run in
// these tests, only the protocol check is used.
type stubURL struct{ engine.Engine }

func (stubURL) Name() string         { return "builtin" }
func (stubURL) Supports(string) bool { return true }

func TestEzEncode(t *testing.T) {
	for _, s := range []string{"", "a", "ab", "abc", "pässwörd", "密碼123"} {
		if got := ezDecode(ezEncode(s)); got != s {
			t.Errorf("roundtrip %q -> %q", s, got)
		}
	}
	if got := ezDecode("YW Jj\n"); got != "abc" {
		t.Errorf("tolerant decode: %q", got)
	}
	if got := ezDecode("YWI=garbage"); got != "ab" {
		t.Errorf("stop at '=': %q", got)
	}
}

func TestFixDoubleUTF8(t *testing.T) {
	orig := "使用者"
	// utf16to8 then treated as code points: each UTF-8 byte becomes a rune
	var b strings.Builder
	for _, c := range []byte(orig) {
		b.WriteRune(rune(c))
	}
	if got := fixDoubleUTF8(b.String()); got != orig {
		t.Errorf("double: %q", got)
	}
	for _, s := range []string{"admin", orig, "café"} {
		if got := fixDoubleUTF8(s); got != s {
			t.Errorf("plain %q changed to %q", s, got)
		}
	}
}

func TestParseRequest(t *testing.T) {
	body := "hash=a&hash%5B%5D=b&priority=1&priority=&priority=0&sid=x"
	r := httptest.NewRequest("POST", "/downloadstation/V4/Task/SetFile?from=0", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	p, _, err := parseRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.all("hash"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("hash: %v", got)
	}
	if got := p.all("priority"); len(got) != 3 || got[1] != "" {
		t.Errorf("priority: %v", got)
	}
	if p.get("from") != "0" || p.get("sid") != "x" {
		t.Errorf("query/sid: %v", p)
	}
	// Multipart without a boundary parameter
	mp := "--XyZ\r\nContent-Disposition: form-data; name=\"sid\"\r\n\r\nabc\r\n" +
		"--XyZ\r\nContent-Disposition: form-data; name=\"file[]\"; filename=\"t.torrent\"\r\nContent-Type: application/x-bittorrent\r\n\r\nDATA\r\n--XyZ--\r\n"
	r = httptest.NewRequest("POST", "/downloadstation/V4/Task/AddTorrent", strings.NewReader(mp))
	r.Header.Set("Content-Type", "multipart/form-data")
	p, files, err := parseRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	if p.get("sid") != "abc" || len(files) != 1 || string(files[0].data) != "DATA" || files[0].name != "t.torrent" {
		t.Errorf("multipart: %v %v", p, files)
	}
}

func TestStateMapping(t *testing.T) {
	cases := map[string]int{core.StQueued: 0, core.StPaused: 1, core.StMoving: 3, core.StError: 4, core.StDone: 5,
		core.StSeeding: 100, core.StChecking: 102, core.StMetadata: 103, core.StDownloading: 104}
	for st, want := range cases {
		if got := stateCode(&core.Task{State: st}); got != want {
			t.Errorf("%s -> %d, want %d", st, got, want)
		}
	}
	if typeString(core.KindBT) != "BT" || typeString(core.KindHTTP) != "HTTP" || typeString(core.KindFTP) != "FTP" {
		t.Error("type strings")
	}
	if !statusMatch(&core.Task{State: core.StQueued}, "downloading") || statusMatch(&core.Task{State: core.StDone}, "downloading") {
		t.Error("status filter")
	}
}

func TestScheduleRotation(t *testing.T) {
	var ours [7]string
	for i := range ours {
		ours[i] = strings.Repeat(string(rune('0'+i%3)), 24)
	}
	off := OfficialSchedule(ours)
	if off[0] != ours[6] || off[1] != ours[0] {
		t.Errorf("Sunday/Monday mapping: %v", off)
	}
	if FromOfficialSchedule(off) != ours {
		t.Error("roundtrip")
	}
}

func TestSortAndPage(t *testing.T) {
	ts := []*core.Task{
		{Hash: "a", Position: 3, Size: 10, Name: "b"},
		{Hash: "b", Position: 1, Size: 30, Name: "c"},
		{Hash: "c", Position: 2, Size: 20, Name: "a"},
	}
	rec := func(t *core.Task) map[string]any {
		return map[string]any{"size": t.Size, "source_name": t.Name}
	}
	sortV4(ts, "", "", rec)
	if ts[0].Hash != "b" || ts[2].Hash != "a" {
		t.Errorf("priority sort: %v %v %v", ts[0].Hash, ts[1].Hash, ts[2].Hash)
	}
	sortV4(ts, "size", "DESC", rec)
	if ts[0].Hash != "b" || ts[2].Hash != "a" {
		t.Errorf("size desc")
	}
	sortV4(ts, "source_name", "ASC", rec)
	if ts[0].Hash != "c" {
		t.Errorf("name asc")
	}
}

func TestDecide(t *testing.T) {
	none := linkState{}
	official := linkState{exists: true, symlink: true, target: "/mnt/ext/opt/DownloadStation/opt/www"}
	ours := linkState{exists: true, symlink: true, ours: true}
	folder := linkState{exists: true}
	cases := []struct {
		enable, inst, en bool
		st               linkState
		want             decision
	}{
		{true, true, true, official, refuse},
		{true, true, true, none, refuse},
		{true, true, false, official, link},
		{true, false, false, none, link},
		{true, false, false, ours, keep},
		{true, false, false, folder, refuse},
		{false, true, true, official, keep},
		{false, false, false, ours, unlink},
		{false, true, true, ours, unlink},
	}
	for i, c := range cases {
		if got := decide(c.enable, c.inst, c.en, c.st); got != c.want {
			t.Errorf("case %d: got %d want %d", i, got, c.want)
		}
	}
}

// TestEndToEnd adds, lists and removes a URL task through the V4 endpoints
// against a temporary database (no engine).
func TestEndToEnd(t *testing.T) {
	user := ""
	for _, a := range qts.Accounts() {
		if a.Admin && a.Name != "admin" {
			user = a.Name
			break
		}
	}
	if user == "" || len(qts.Shares()) == 0 {
		t.Skip("needs a QTS administrator account and shares")
	}
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "dc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := core.New(db, dir)
	m.URL = stubURL{}
	au := auth.New(db)
	srv := api.New(m, au, dir, "test")
	srv.DevUser = user
	Register(srv, m, au, dir, dir)
	call := func(path string, form url.Values) map[string]any {
		form.Set("sid", "dev")
		r := httptest.NewRequest("POST", "/DownloadCenter/downloadstation/V4/"+path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Accept", "*/*")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: HTTP %d", path, w.Code)
		}
		if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Errorf("content type %q", w.Header().Get("Content-Type"))
		}
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	if res := call("Misc/Env", url.Values{}); res["error"].(float64) != 0 || res["user"] != user {
		t.Fatalf("env: %v", res)
	}
	res := call("Task/AddUrl", url.Values{"url": {"https://example.com/v4test.bin", "https://example.com/v4test2.bin"}, "config": {"-1"}})
	if res["error"].(float64) != 0 {
		t.Fatalf("add: %v", res)
	}
	res = call("Task/AddUrl", url.Values{"url": {"https://example.com/v4test.bin"}})
	if res["error"].(float64) != errDuplicate {
		t.Errorf("duplicate: %v", res)
	}
	res = call("Task/Query", url.Values{"from": {"0"}, "limit": {"1"}, "type": {"http"}, "status": {"all"}})
	if res["total"].(float64) != 2 || len(res["data"].([]any)) != 1 {
		t.Fatalf("query: %v", res)
	}
	rec := res["data"].([]any)[0].(map[string]any)
	if rec["type"] != "HTTP" || rec["state"].(float64) != 0 || rec["username"] != user {
		t.Errorf("record: %v", rec)
	}
	h := rec["hash"].(string)
	if res := call("Task/Pause", url.Values{"hash": {h}, "time": {"10"}}); res["error"].(float64) != 0 {
		t.Errorf("pause: %v", res)
	}
	res = call("Task/Detail", url.Values{"hash": {h}})
	if d := res["data"].([]any)[0].(map[string]any); d["state"].(float64) != 1 || d["wakeup_time"] == "" {
		t.Errorf("paused record: %v", d)
	}
	if res := call("Task/Remove", url.Values{"hash": {"all"}, "clean": {"0"}}); res["error"].(float64) != 0 {
		t.Errorf("remove: %v", res)
	}
	res = call("Task/Query", url.Values{})
	if res["total"].(float64) != 0 {
		t.Errorf("after remove: %v", res)
	}
	if res := call("Task/Start", url.Values{"hash": {"nope"}}); res["error"].(float64) != errTaskNotFound {
		t.Errorf("not found: %v", res)
	}
	if res := call("Rss/Query", url.Values{}); res["error"].(float64) != errAPINotExists {
		t.Errorf("rss: %v", res)
	}
	if res := call("Config/Get", url.Values{}); res["bt"] == nil {
		t.Errorf("config: %v", res)
	}
	// No session
	r := httptest.NewRequest("GET", "/downloadstation/V4/Task/Query", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	var out map[string]any
	json.NewDecoder(bytes.NewReader(w.Body.Bytes())).Decode(&out)
	if out["error"].(float64) != errSessionTimeout {
		t.Errorf("no session: %v", out)
	}
	_ = http.StatusOK
}

// Requests are counted by the endpoint table's name and the answer's error
// code; the answers stay as they were.
func TestCounts(t *testing.T) {
	got := map[string]int{}
	api.Counter = func(k string) { got[k]++ }
	defer func() { api.Counter = nil }()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "dc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := core.New(db, dir)
	au := auth.New(db)
	srv := api.New(m, au, dir, "test")
	Register(srv, m, au, dir, dir)
	for _, c := range []struct{ path, body, counts string }{
		{"/downloadstation/V4/Task/Query", `{"error":5}`, "v4_err_5=1 v4_task_query=1"},
		{"/downloadstation/V4/Task/Secretname", `{"error":2}`, "v4_err_2=1 v4_unknown=1"},
		{"/DownloadCenter/downloadstation/V4/Rss/Feed", `{"error":2}`, "v4_err_2=1 v4_rss=1"},
		{"/downloadstation/V4/Misc/Logout", `{"error":0}`, "v4_misc_logout=1"},
	} {
		clear(got)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest("GET", c.path, nil))
		if body := strings.TrimSpace(w.Body.String()); w.Code != 200 || body != c.body {
			t.Errorf("%s: %d %s, want %s", c.path, w.Code, body, c.body)
		}
		var l []string
		for k, n := range got {
			l = append(l, k+"="+strconv.Itoa(n))
		}
		sort.Strings(l)
		if s := strings.Join(l, " "); s != c.counts {
			t.Errorf("%s: counted %q, want %q", c.path, s, c.counts)
		}
	}
}
