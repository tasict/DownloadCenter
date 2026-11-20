package search

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"downloadcenter/internal/phpbox"
	"downloadcenter/internal/phpbox/phpboxtest"
)

// fakeBox stands for phpbox in tests that do not run PHP.
type fakeBox struct {
	mu       sync.Mutex
	ok       bool
	code     string
	reason   string
	checks   int
	results  []phpbox.Result
	err      error
	errText  string
	verified bool
	plugins  []phpbox.Plugin
	queries  []phpbox.Query
	accounts []phpbox.Account
}

func (f *fakeBox) Check(ctx context.Context) (bool, string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	return f.ok, f.code, f.reason
}

func (f *fakeBox) Available() (bool, string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ok, f.code, f.reason
}

func (f *fakeBox) Search(ctx context.Context, p phpbox.Plugin, q phpbox.Query) (*phpbox.Output, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plugins, f.queries = append(f.plugins, p), append(f.queries, q)
	return &phpbox.Output{Results: f.results, Error: f.errText}, f.err
}

func (f *fakeBox) Verify(ctx context.Context, p phpbox.Plugin, a phpbox.Account) (bool, *phpbox.Output, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plugins, f.accounts = append(f.plugins, p), append(f.accounts, a)
	return f.verified, &phpbox.Output{Error: f.errText}, f.err
}

func okBox() *fakeBox { return &fakeBox{ok: true} }

const codeInfo = `{"name":"testsearch","displayname":"Test search","description":"A module for tests","version":"1.0",
	"site":"https://search.example.org/","module":"search.php","type":"search","class":"DcTestSearch","accountsupport":true}`

const codeAddonJSON = `{"name":"Test add-on","author":"Tests","website":"https://addon.example.org/","class":"DcTestAddon",
	"addon":"main.php","hosts":["addon.example.org","cdn.example.org"],"version":105,"interface":["ISearch","IDownload"],
	"description":{"ENG":"An add-on for tests"}}`

func codeDLM(version string) []byte {
	return phpboxtest.DLM([]phpboxtest.Entry{{Name: "INFO", Body: strings.Replace(codeInfo, `"1.0"`, `"`+version+`"`, 1)},
		{Name: "search.php", Body: "<?php class DcTestSearch {}"}})
}

func upload(data []byte, name string) map[string]any {
	return map[string]any{"file": base64.StdEncoding.EncodeToString(data), "filename": name}
}

func sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

func with(m map[string]any, kv ...any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

// spec: a search module is checked, agreed to and installed, disabled, for
// administrators; without consent, with another file or with a token
// nothing is installed
func TestCodeInstall(t *testing.T) {
	e := newAPIEnv(t)
	box := okBox()
	e.s.code = box
	data := codeDLM("1.0")
	code, out, _ := e.call(t, "admin", "", "POST", "/search/plugins/inspect", upload(data, "test.dlm"))
	if code != 200 || out["kind"] != "dlm" || out["runs_code"] != true || out["sha256"] != sum(data) || out["name"] != "Test search" ||
		out["version"] != "1.0" || out["account"] != true || out["exists"] != false || out["key_changed"] != false || out["id"] != "dlm-testsearch" {
		t.Fatalf("inspect: %d %v", code, out)
	}
	if rt := out["runtime"].(map[string]any); rt["ok"] != true {
		t.Errorf("runtime %v", rt)
	}
	if site := out["site"].([]any); len(site) != 1 || site[0] != "https://search.example.org/" {
		t.Errorf("site %v", out["site"])
	}
	if box.checks != 1 {
		t.Errorf("%d checks for an inspect", box.checks)
	}
	codeDir := filepath.Join(e.s.store.dir, "code", "dlm-testsearch")
	installed := func() bool { _, err := os.Stat(codeDir); return err == nil }
	if installed() {
		t.Fatal("inspect stored the plugin")
	}
	// spec: no consent, another file's hash, a token
	other := codeDLM("2.0")
	for name, c := range map[string]struct {
		body   map[string]any
		bearer bool
		code   int
		err    string
	}{
		"no consent":     {with(upload(data, "test.dlm"), "sha256", sum(data)), false, 400, "search_consent_required"},
		"consent false":  {with(upload(data, "test.dlm"), "sha256", sum(data), "accept_risk", false), false, 400, "search_consent_required"},
		"other file":     {with(upload(other, "test.dlm"), "sha256", sum(data), "accept_risk", true), false, 400, "search_consent_required"},
		"no hash":        {with(upload(data, "test.dlm"), "accept_risk", true), false, 400, "search_consent_required"},
		"token":          {with(upload(data, "test.dlm"), "sha256", sum(data), "accept_risk", true), true, 403, "session_required"},
		"not base64":     {map[string]any{"file": "!!!", "filename": "x.dlm", "accept_risk": true, "sha256": "x"}, false, 400, "search_plugin_invalid"},
		"from a url":     {map[string]any{"url": "https://plugins.example.org/test.dlm", "accept_risk": true, "sha256": "x"}, false, 502, "fetch_failed"},
		"host module":    {with(upload(data, "test.host"), "sha256", sum(data), "accept_risk", true), false, 400, "search_plugin_invalid"},
		"too large file": {map[string]any{"file": strings.Repeat("A", 3<<20), "filename": "x.dlm", "accept_risk": true}, false, 400, "search_plugin_invalid"},
	} {
		bearer := ""
		user := "admin"
		if c.bearer {
			bearer, user = e.token(t, "admin", "settings:write", "search:use"), ""
		}
		code, out, _ := e.call(t, user, bearer, "POST", "/search/plugins", c.body)
		if code != c.code || errCode(out) != c.err {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	if installed() {
		t.Fatal("installed without consent")
	}
	code, out, _ = e.call(t, "admin", "", "POST", "/search/plugins", with(upload(data, "test.dlm"), "sha256", strings.ToUpper(sum(data)), "accept_risk", true))
	if code != 200 || out["kind"] != "dlm" || out["runs_code"] != true || out["enabled"] != false || out["audience"] != "admins" ||
		out["secret_bearing"] != false || out["host"] != "search.example.org" || out["version"] != "1.0" {
		t.Fatalf("install: %d %v", code, out)
	}
	consent := out["consent"].(map[string]any)
	if consent["user"] != "admin" || consent["time"].(float64) < 1e9 {
		t.Errorf("consent %v", consent)
	}
	var ids []string
	for _, s := range out["settings"].([]any) {
		st := s.(map[string]any)
		ids = append(ids, st["id"].(string)+"="+st["label"].(string))
		if st["id"] == "password" && st["secret"] != true {
			t.Errorf("password not secret: %v", st)
		}
	}
	if strings.Join(ids, " ") != "username=User name password=Password" {
		t.Errorf("settings %v", ids)
	}
	// Files kept as dcd's own; the consent bound to the file in state.json
	if fi, err := os.Stat(codeDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("code directory %v %v", fi, err)
	}
	if fi, err := os.Stat(filepath.Join(codeDir, "search.php")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("module file %v %v", fi, err)
	}
	st, _ := os.ReadFile(filepath.Join(e.s.store.dir, "state.json"))
	if !bytes.Contains(st, []byte(sum(data))) || !bytes.Contains(st, []byte(`"user": "admin"`)) {
		t.Errorf("state.json %s", st)
	}
	// The same id again: only as a replacement, which is a new consent
	if code, out, _ := e.call(t, "admin", "", "POST", "/search/plugins", with(upload(other, "test.dlm"), "sha256", sum(other), "accept_risk", true)); code != 409 || errCode(out) != "search_plugin_exists" {
		t.Errorf("without replace: %d %v", code, out)
	}
	if _, out, _ := e.call(t, "admin", "", "POST", "/search/plugins/inspect", upload(other, "test.dlm")); out["exists"] != true {
		t.Errorf("inspect of a new version: %v", out)
	}
	// spec: a replacement starts anew: off, for administrators, without the
	// account, which the new code is never given
	on := true
	e.s.store.Update("dlm-testsearch", &on, map[string]string{"username": "alice", "password": "pw"}, nil)
	e.s.store.SetAudience("dlm-testsearch", AudienceAll)
	if src, _ := e.s.store.Get("dlm-testsearch"); !src.SecretBearing() || !src.Enabled || src.Audience != AudienceAll {
		t.Fatalf("before: %+v", src)
	}
	e.s.now = func() time.Time { return time.Unix(2000000000, 0) }
	code, out, _ = e.call(t, "admin", "", "POST", "/search/plugins", with(upload(other, "test.dlm"), "sha256", sum(other), "accept_risk", true, "replace", true))
	if code != 200 || out["version"] != "2.0" || out["enabled"] != false || out["audience"] != "admins" ||
		out["consent"].(map[string]any)["time"].(float64) != 2000000000 || out["secret_bearing"] != false || out["needs_setup"] != false {
		t.Errorf("replace: %d %v", code, out)
	}
	for _, x := range out["settings"].([]any) {
		if st := x.(map[string]any); st["set"] != false || (st["value"] != nil && st["value"] != "") {
			t.Errorf("setting kept: %v", st)
		}
	}
	if src, _ := e.s.store.Get("dlm-testsearch"); len(src.Settings) != 0 || account(src) != nil {
		t.Errorf("account kept: %+v", src.Settings)
	}
	st, _ = os.ReadFile(filepath.Join(e.s.store.dir, "state.json"))
	if bytes.Contains(st, []byte(`"pw"`)) || bytes.Contains(st, []byte("alice")) {
		t.Errorf("state.json still has the account: %s", st)
	}
	// Switched on again, it runs without any account
	e.s.store.Update("dlm-testsearch", &on, nil, nil)
	id, _ := e.s.Start("admin", true, Query{Text: "ubuntu"}, []string{"dlm-testsearch"})
	waitDone(t, e.s, "admin", id)
	if q := box.queries[len(box.queries)-1]; q.Account != nil {
		t.Errorf("the new code got an account: %+v", q.Account)
	}
	// Export: there is no plugin file; delete removes the files
	if code, out, _ := e.call(t, "admin", "", "GET", "/search/plugins/dlm-testsearch", nil); code != 400 || errCode(out) != "invalid_request" {
		t.Errorf("export: %d %v", code, out)
	}
	if code, _, _ := e.call(t, "admin", "", "DELETE", "/search/plugins/dlm-testsearch", nil); code != 200 || installed() {
		t.Errorf("delete: %d, files there: %v", code, installed())
	}
}

// spec: package checks refuse links and changed add-ons, nothing is
// unpacked
func TestCodePackageRefused(t *testing.T) {
	e := newAPIEnv(t)
	e.s.code = okBox()
	link := phpboxtest.DLM([]phpboxtest.Entry{{Name: "INFO", Body: codeInfo}, {Name: "search.php", Body: "<?php"},
		{Name: "cfg", Type: tar.TypeSymlink, Link: "/etc/config"}})
	hostType := phpboxtest.DLM([]phpboxtest.Entry{{Name: "INFO", Body: strings.Replace(codeInfo, `"search"`, `"host"`, 1)}, {Name: "search.php", Body: "<?php"}})
	addon := phpboxtest.Addon([]phpboxtest.Entry{{Name: "addon.json", Body: codeAddonJSON}, {Name: "main.php", Body: "<?php class DcTestAddon {}"}}, phpboxtest.Key())
	tampered := bytes.Replace(addon, []byte("DcTestAddon {}"), []byte("DcTestAddon {;"), 1)
	for name, c := range map[string]struct {
		data []byte
		file string
		msg  string
	}{
		"link":      {link, "x.dlm", phpbox.MsgUnsafe},
		"host type": {hostType, "x.dlm", phpbox.MsgOnlySearch},
		"host file": {link, "x.host", phpbox.MsgOnlySearch},
		"tampered":  {tampered, "x.addon", phpbox.MsgDamaged},
	} {
		for _, path := range []string{"/search/plugins/inspect", "/search/plugins"} {
			code, out, _ := e.call(t, "admin", "", "POST", path, with(upload(c.data, c.file), "sha256", sum(c.data), "accept_risk", true))
			if code != 400 || errCode(out) != "search_plugin_invalid" || errMsg(out) != c.msg {
				t.Errorf("%s %s: %d %v", name, path, code, out)
			}
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(e.s.store.dir, "code")); len(entries) != 0 {
		t.Errorf("unpacked: %v", entries)
	}
	// spec: an add-on with a download interface installs; a new key shows
	code, out, _ := e.call(t, "admin", "", "POST", "/search/plugins", with(upload(addon, "t.addon"), "sha256", sum(addon), "accept_risk", true))
	if code != 200 || out["kind"] != "qnap-addon" || out["version"] != "1.05" || out["host"] != "addon.example.org, cdn.example.org" ||
		out["key_fingerprint"] == nil || len(out["settings"].([]any)) != 0 {
		t.Fatalf("add-on: %d %v", code, out)
	}
	other := phpboxtest.Addon([]phpboxtest.Entry{{Name: "addon.json", Body: codeAddonJSON}, {Name: "main.php", Body: "<?php class DcTestAddon {}"}}, phpboxtest.NewKey())
	if _, out, _ := e.call(t, "admin", "", "POST", "/search/plugins/inspect", upload(other, "t.addon")); out["exists"] != true || out["key_changed"] != true || out["kind"] != "qnap-addon" {
		t.Errorf("new key: %v", out)
	}
	if _, out, _ := e.call(t, "admin", "", "POST", "/search/plugins/inspect", upload(addon, "t.addon")); out["key_changed"] != false {
		t.Errorf("same key: %v", out)
	}
}

// spec: without QTS's PHP (or a sandbox) nothing of this kind installs or
// runs, and the reason is given
func TestCodeRuntimeUnavailable(t *testing.T) {
	e := newAPIEnv(t)
	box := okBox()
	e.s.code = box
	data := codeDLM("1.0")
	if code, _, _ := e.call(t, "admin", "", "POST", "/search/plugins", with(upload(data, "t.dlm"), "sha256", sum(data), "accept_risk", true)); code != 200 {
		t.Fatal(code)
	}
	on := true
	e.s.store.Update("dlm-testsearch", &on, nil, nil)
	e.s.store.SetAudience("dlm-testsearch", AudienceAll)
	box.ok, box.code, box.reason = false, phpbox.CodePHPMissing, phpbox.ReasonPHPMissing
	code, out, _ := e.call(t, "admin", "", "POST", "/search/plugins/inspect", upload(data, "t.dlm"))
	rt, _ := out["runtime"].(map[string]any)
	if code != 200 || rt["ok"] != false || rt["code"] != phpbox.CodePHPMissing || rt["reason"] != phpbox.ReasonPHPMissing {
		t.Errorf("inspect: %d %v", code, out)
	}
	code, out, _ = e.call(t, "admin", "", "POST", "/search/plugins", with(upload(data, "t.dlm"), "sha256", sum(data), "accept_risk", true, "replace", true))
	if code != 409 || errCode(out) != "search_runtime_unavailable" || errMsg(out) != phpbox.ReasonPHPMissing {
		t.Errorf("install: %d %v", code, out)
	}
	_, out, _ = e.call(t, "admin", "", "GET", "/search/plugins", nil)
	if rt := out["runtime"].(map[string]any); rt["ok"] != false || rt["code"] != phpbox.CodePHPMissing {
		t.Errorf("list runtime %v", rt)
	}
	for _, x := range out["sources"].([]any) {
		s := x.(map[string]any)
		if s["id"] == "dlm-testsearch" {
			if rt, _ := s["runtime"].(map[string]any); rt == nil || rt["ok"] != false || rt["reason"] != phpbox.ReasonPHPMissing {
				t.Errorf("item %v", s)
			}
		} else if s["runtime"] != nil {
			t.Errorf("runtime on a declarative source: %v", s)
		}
	}
	// Not offered, not searched
	if _, out, _ := e.call(t, "admin", "", "GET", "/search/sources", nil); len(out["sources"].([]any)) != 0 {
		t.Errorf("sources %v", out)
	}
	if code, out, _ := e.call(t, "admin", "", "POST", "/search", map[string]any{"q": "ubuntu", "sources": []string{"dlm-testsearch"}}); code != 409 || errCode(out) != "search_no_sources" {
		t.Errorf("search: %d %v", code, out)
	}
	if len(box.queries) != 0 {
		t.Error("the plugin ran")
	}
}

// spec: a source for administrators is neither listed for nor searched by
// a regular user
func TestAudience(t *testing.T) {
	e := newAPIEnv(t)
	e.s.code = okBox()
	data := codeDLM("1.0")
	e.call(t, "admin", "", "POST", "/search/plugins", with(upload(data, "t.dlm"), "sha256", sum(data), "accept_risk", true))
	on := true
	e.s.store.Update("dlm-testsearch", &on, nil, nil)
	// Defaults: built-ins and declarative plugins without secrets for
	// everyone, the rest for administrators
	addPlugin(t, e.s, jsonSource("keyed", "src.example.org"), nil)
	addPlugin(t, e.s, `{"schema":1,"id":"open","name":"Open","type":"json","request":{"url":"https://src.example.org/s?q={query}"},
		"response":{"items":"items","fields":{"title":"t","infohash":"h"}}}`, nil)
	for id, want := range map[string]string{"kiwix": "all", "open": "all", "keyed": "admins", "dlm-testsearch": "admins"} {
		if src, _ := e.s.store.Get(id); src.Audience != want {
			t.Errorf("%s: %s", id, src.Audience)
		}
	}
	tz := &Plugin{Schema: 1, ID: "tz", Name: "TZ", Type: TypeTorznab}
	e.s.store.Install(tz, true, false, map[string]string{"url": "http://192.168.1.20:9117/"})
	if src, _ := e.s.store.Get("tz"); src.Audience != "admins" {
		t.Errorf("torznab: %s", src.Audience)
	}
	off := false
	e.s.store.Update("open", &off, nil, nil)
	e.s.store.Update("tz", &off, nil, nil)
	ids := func(user string) string {
		_, out, _ := e.call(t, user, "", "GET", "/search/sources", nil)
		var l []string
		for _, x := range out["sources"].([]any) {
			l = append(l, x.(map[string]any)["id"].(string))
		}
		return strings.Join(l, " ")
	}
	if got := ids("alice"); got != "" {
		t.Errorf("alice sees %q", got)
	}
	if got := ids("admin"); got != "keyed dlm-testsearch" && got != "dlm-testsearch keyed" {
		t.Errorf("admin sees %q", got)
	}
	if code, out, _ := e.call(t, "alice", "", "POST", "/search", map[string]any{"q": "ubuntu", "sources": []string{"dlm-testsearch"}}); code != 409 || errCode(out) != "search_no_sources" {
		t.Errorf("alice names the source: %d %v", code, out)
	}
	if _, out, _ := e.call(t, "alice", "", "GET", "/me", nil); out["search"].(map[string]any)["available"] != false {
		t.Errorf("alice /me %v", out["search"])
	}
	tok := e.token(t, "admin", "search:use")
	if _, out, _ := e.call(t, "", tok, "GET", "/me", nil); out["search"].(map[string]any)["available"] != true {
		t.Errorf("an administrator's token: %v", out["search"])
	}
	// Opened to everyone
	code, out, _ := e.call(t, "admin", "", "PUT", "/search/plugins/dlm-testsearch", map[string]any{"audience": "all"})
	if code != 200 || out["audience"] != "all" {
		t.Errorf("put: %d %v", code, out)
	}
	if got := ids("alice"); got != "dlm-testsearch" {
		t.Errorf("alice sees %q", got)
	}
	if code, out, _ := e.call(t, "admin", "", "PUT", "/search/plugins/dlm-testsearch", map[string]any{"audience": "friends"}); code != 400 || errCode(out) != "invalid_request" {
		t.Errorf("bad audience: %d %v", code, out)
	}
	// Kept across a restart
	st2, _ := OpenStore(e.s.store.dir)
	if src, _ := st2.Get("dlm-testsearch"); src.Audience != "all" || !src.Plugin.RunsCode() {
		t.Errorf("after reload: %+v", src)
	}
	_, _, _, code4 := st2.Counts()
	if code4 != 1 {
		t.Errorf("counts: %d", code4)
	}
}

// A plugin from another NAS goes through the same normalisation as any
// source; only its own account reaches it; failures are fixed reasons and
// its own text reaches the log only cleaned.
func TestCodeSearch(t *testing.T) {
	e := newAPIEnv(t)
	box := okBox()
	e.s.code = box
	data := codeDLM("1.0")
	e.call(t, "admin", "", "POST", "/search/plugins", with(upload(data, "t.dlm"), "sha256", sum(data), "accept_risk", true))
	on := true
	e.s.store.Update("dlm-testsearch", &on, map[string]string{"username": "alice", "password": "SECRETPW"}, nil)
	addPlugin(t, e.s, jsonSource("other", "src.example.org"), map[string]string{"key": "OTHERKEY"})
	e.n.handle("src.example.org", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(items())) })
	stuffed := "magnet:?xt=urn:btih:" + hashA + "&dn=x&xs=http://evil.example.org/&tr=" + url.QueryEscape("udp://t.example.org:1")
	box.results = []phpbox.Result{
		{Title: "Ubuntu\x00 26.04", Download: stuffed, Page: "javascript:alert(1)", Size: 123, Seeds: 5, Leechs: -1, Category: "Linux ISO", Date: "2026-01-02 03:04:05"},
		{Title: "Film", Download: "https://search.example.org/f.torrent", Page: "https://search.example.org/f", Size: -1, Seeds: -1, Leechs: -1, Category: "Movies"},
		{Title: "", Download: "magnet:?xt=urn:btih:" + hashA},
		{Title: "No link", Download: "ftp://x.example.org/a.torrent"},
	}
	id, err := e.s.Start("admin", true, Query{Text: "ubuntu linux"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	v := waitDone(t, e.s, "admin", id)
	var code []Result
	for _, r := range v.Results {
		if r.Sources[0] == "dlm-testsearch" {
			code = append(code, r)
		}
	}
	if len(code) != 2 {
		t.Fatalf("%+v", v.Results)
	}
	u := code[0]
	if u.Title != "Ubuntu 26.04" || u.Magnet != "magnet:?xt=urn:btih:"+hashA+"&dn=Ubuntu+26.04&tr=udp%3A%2F%2Ft.example.org%3A1" || u.Page != "" ||
		*u.Size != 123 || *u.Seeders != 5 || u.Leechers != nil || u.Category != "software" || u.Date != unix("2026-01-02T03:04:05Z") {
		t.Errorf("%+v", u)
	}
	f := code[1]
	if f.Ref == "" || f.TorrentURL != "https://search.example.org/f.torrent" || f.Category != "video" || f.Magnet != "" {
		t.Errorf("%+v", f)
	}
	if len(box.queries) != 1 || box.queries[0].Keyword != "ubuntu linux" || box.queries[0].Limit != MaxResults ||
		box.queries[0].Account == nil || box.queries[0].Account.Password != "SECRETPW" || box.plugins[0].Entry != "search.php" ||
		box.plugins[0].Class != "DcTestSearch" || !strings.HasSuffix(box.plugins[0].Dir, filepath.Join("code", "dlm-testsearch")) {
		t.Errorf("run %+v %+v", box.queries, box.plugins)
	}

	// Failures
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	for _, c := range []struct {
		err  error
		want string
	}{
		{phpbox.ErrTimeout, ReasonPluginTimeout},
		{&phpbox.Unavailable{Code: phpbox.CodeCheckFailed, Reason: phpbox.ReasonCheckFailed}, "This NAS cannot run this plugin: " + phpbox.ReasonCheckFailed},
		{phpbox.ErrFailed, ReasonPluginFailed},
		{phpbox.ErrOutput, ReasonPluginFailed},
	} {
		box.err, box.errText = c.err, "the plugin failed: no results for ubuntu linux, password SECRETPW at https://search.example.org/?q=ubuntu"
		id, _ := e.s.Start("admin", true, Query{Text: "ubuntu linux"}, []string{"dlm-testsearch"})
		v := waitDone(t, e.s, "admin", id)
		if v.Sources[0].State != "failed" || v.Sources[0].Error != c.want {
			t.Errorf("%v: %+v", c.err, v.Sources)
		}
	}
	for _, bad := range []string{"ubuntu", "linux", "SECRETPW", "search.example.org", "q="} {
		if strings.Contains(buf.String(), bad) {
			t.Errorf("%q in the log:\n%s", bad, buf.String())
		}
	}
	if !strings.Contains(buf.String(), "plugin dlm-testsearch ran") {
		t.Errorf("no run line:\n%s", buf.String())
	}
}

// spec: an administrator checks the account of a plugin that takes one
func TestCodeVerify(t *testing.T) {
	e := newAPIEnv(t)
	box := okBox()
	e.s.code = box
	data := codeDLM("1.0")
	e.call(t, "admin", "", "POST", "/search/plugins", with(upload(data, "t.dlm"), "sha256", sum(data), "accept_risk", true))
	if code, _, _ := e.call(t, "admin", "", "PUT", "/search/plugins/dlm-testsearch", map[string]any{"settings": map[string]string{"username": "alice", "password": "pw"}}); code != 200 {
		t.Fatal(code)
	}
	// An empty password keeps the saved one
	e.call(t, "admin", "", "PUT", "/search/plugins/dlm-testsearch", map[string]any{"settings": map[string]string{"username": "alice", "password": ""}})
	box.verified = true
	code, out, _ := e.call(t, "admin", "", "POST", "/search/plugins/dlm-testsearch/verify", map[string]any{})
	if code != 200 || out["ok"] != true || len(box.accounts) != 1 || box.accounts[0] != (phpbox.Account{Username: "alice", Password: "pw"}) {
		t.Errorf("verify: %d %v %+v", code, out, box.accounts)
	}
	box.verified = false
	if _, out, _ := e.call(t, "admin", "", "POST", "/search/plugins/dlm-testsearch/verify", map[string]any{}); out["ok"] != false || out["error"] != nil {
		t.Errorf("refused: %v", out)
	}
	box.err = phpbox.ErrTimeout
	if _, out, _ := e.call(t, "admin", "", "POST", "/search/plugins/dlm-testsearch/verify", map[string]any{}); out["ok"] != false || out["error"] != ReasonPluginTimeout {
		t.Errorf("timeout: %v", out)
	}
	// Sources without an account
	for _, id := range []string{"kiwix", "nothere"} {
		code, out, _ := e.call(t, "admin", "", "POST", "/search/plugins/"+id+"/verify", map[string]any{})
		if (id == "kiwix" && (code != 400 || errCode(out) != "invalid_request")) || (id == "nothere" && code != 404) {
			t.Errorf("%s: %d %v", id, code, out)
		}
	}
	if code, _, _ := e.call(t, "alice", "", "POST", "/search/plugins/dlm-testsearch/verify", map[string]any{}); code != 403 {
		t.Errorf("regular user: %d", code)
	}
}

// The state of a plugin from another NAS reads back as it was written.
func TestCodeStateRoundTrip(t *testing.T) {
	st, _ := OpenStore(t.TempDir())
	pkg, err := phpbox.Inspect("t.dlm", codeDLM("1.0"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.InstallCode(pkg, Consent{User: "admin", Time: 1}, false)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(st.dir, "state.json"))
	var f stateFile
	if err := json.Unmarshal(b, &f); err != nil || f.Sources[id].Code == nil || f.Sources[id].Code.SHA256 != pkg.SHA256 {
		t.Fatalf("%s %v", b, err)
	}
	if _, err := st.InstallCode(pkg, Consent{}, false); !errors.Is(err, ErrExists) {
		t.Errorf("twice: %v", err)
	}
	// The files gone, the plugin is skipped at the next start
	os.RemoveAll(st.codeDir(id))
	st2, _ := OpenStore(st.dir)
	if _, ok := st2.Get(id); ok {
		t.Error("loaded without files")
	}
}

// A plugin's results are cut to the search's limit, its addresses that
// carry its password stay on the NAS, a legal package of nearly the
// largest size is taken, and the Test button runs it like a search.
func TestCodeLimits(t *testing.T) {
	e := newAPIEnv(t)
	box := okBox()
	e.s.code = box
	data := codeDLM("1.0")
	e.call(t, "admin", "", "POST", "/search/plugins", with(upload(data, "t.dlm"), "sha256", sum(data), "accept_risk", true))
	on := true
	e.s.store.Update("dlm-testsearch", &on, map[string]string{"username": "alice", "password": "SECRETPW"}, nil)
	box.results = []phpbox.Result{{Title: "Private", Download: "https://search.example.org/t.torrent?pass=SECRETPW",
		Page: "https://search.example.org/item?pw=SECRETPW", Size: -1, Seeds: -1, Leechs: -1}}
	for i := 0; i < 150; i++ {
		box.results = append(box.results, phpbox.Result{Title: fmt.Sprintf("Item %d", i), Download: fmt.Sprintf("https://search.example.org/%d.torrent", i), Size: -1, Seeds: -1, Leechs: -1})
	}
	id, _ := e.s.Start("admin", true, Query{Text: "item"}, []string{"dlm-testsearch"})
	v := waitDone(t, e.s, "admin", id)
	// At most MaxResults; the one whose .torrent address holds the password
	// has nothing left (a program has no host of its own to send it to)
	if len(v.Results) != MaxResults-1 || v.Sources[0].Count != MaxResults-1 {
		t.Errorf("%d results, count %d", len(v.Results), v.Sources[0].Count)
	}
	p := v.Results[0]
	if p.Title != "Item 0" || p.Ref == "" {
		t.Errorf("first: %+v", p)
	}
	// The .torrent is fetched by the NAS, without anything of the plugin
	e.n.handle("search.example.org", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.URL.Path != "/0.torrent" {
			t.Errorf("torrent request %s %v", r.URL, r.Header)
		}
		w.Write([]byte(sampleTorrent))
	})
	if tf, err := e.s.Torrent(context.Background(), "admin", true, id, p.Ref); err != nil || len(tf.Data) == 0 {
		t.Errorf("torrent: %v", err)
	}

	// Test: a search with the test keyword
	box.results = box.results[:3]
	code, out, _ := e.call(t, "admin", "", "POST", "/search/plugins/dlm-testsearch/test", map[string]any{})
	if code != 200 || out["ok"] != true || out["count"].(float64) != 3 || box.queries[len(box.queries)-1].Keyword != defaultTest {
		t.Errorf("test: %d %v", code, out)
	}
	box.ok, box.code, box.reason = false, phpbox.CodeNoIsolation, phpbox.ReasonNoIsolation
	box.err = &phpbox.Unavailable{Code: phpbox.CodeNoIsolation, Reason: phpbox.ReasonNoIsolation}
	code, out, _ = e.call(t, "admin", "", "POST", "/search/plugins/dlm-testsearch/test", map[string]any{})
	if code != 200 || out["ok"] != false || out["error"] != "This NAS cannot run this plugin: "+phpbox.ReasonNoIsolation {
		t.Errorf("test without the sandbox: %d %v", code, out)
	}

	// Nearly 2 MiB that does not compress
	box.ok, box.err = true, nil
	noise := make([]byte, 1900<<10)
	rand.Read(noise)
	big := phpboxtest.DLM([]phpboxtest.Entry{{Name: "INFO", Body: codeInfo}, {Name: "search.php", Body: "<?php class DcTestSearch {}"}, {Name: "data.bin", Body: string(noise)}})
	if len(big) < 1900<<10 || len(big) > phpbox.MaxPackage {
		t.Fatalf("package of %d bytes", len(big))
	}
	if code, out, _ := e.call(t, "admin", "", "POST", "/search/plugins/inspect", upload(big, "big.dlm")); code != 200 || out["sha256"] != sum(big) {
		t.Errorf("large package: %d %v", code, out)
	}
}
