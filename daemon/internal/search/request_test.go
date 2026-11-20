package search

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func mustParse(t *testing.T, b []byte) *Plugin {
	t.Helper()
	p, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBuildRequest(t *testing.T) {
	p := mustParse(t, []byte(examplePlugin))
	c, err := p.BuildRequest(Query{Text: `a&b=c #d/../e`, Category: "video"}, map[string]string{"apikey": "k/y&z"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(c.URL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "api.example.org" || u.Path != "/search" {
		t.Errorf("address %s", c.URL)
	}
	q := u.Query()
	if q.Get("q") != "a&b=c #d/../e" || q.Get("rows") != "100" || q.Get("cat") != "movies" || q.Get("key") != "k/y&z" || len(q) != 4 {
		t.Errorf("query %v", q)
	}
	if c.Headers["Accept"] != "application/json" {
		t.Errorf("headers %v", c.Headers)
	}

	// A category without a value, and all categories, give an empty {category}
	for _, cat := range []string{"", CategoryAll, "books"} {
		c, _ := p.BuildRequest(Query{Text: "x", Category: cat}, nil)
		if u, _ := url.Parse(c.URL); u.Query().Get("cat") != "" {
			t.Errorf("%q: %s", cat, c.URL)
		}
	}
}

// No keyword can change the host, whatever it holds.
func TestBuildRequestHostFixed(t *testing.T) {
	p := mustParse(t, variant(t, setURL("https://api.example.org/s/{query}?p={page}&k={setting:apikey}")))
	for _, kw := range []string{"@evil.org", "//evil.org/", "x?y#z", "../../..", "evil.org:80", "%2F%2Fevil", "‮"} {
		c, err := p.BuildRequest(Query{Text: kw}, map[string]string{"apikey": "@evil.org/"})
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(c.URL)
		if err != nil || u.Host != "api.example.org" || u.User != nil || u.Fragment != "" {
			t.Errorf("%q: %s", kw, c.URL)
		}
		if u.Query().Get("p") != "1" {
			t.Errorf("page: %s", c.URL)
		}
	}
}

func TestBuildRequestStrip(t *testing.T) {
	ia := Builtin("internet-archive")
	c, err := ia.BuildRequest(Query{Text: `night (of) "the" living:dead*`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(c.URL)
	q := u.Query().Get("q")
	want := `title:(night of the living dead) AND format:"Archive BitTorrent" AND NOT access-restricted-item:true AND NOT mediatype:(collection OR web OR account) AND licenseurl:*`
	if q != want {
		t.Errorf("q = %s", q)
	}
	if u.Query().Get("rows") != "100" || u.Query().Get("output") != "json" || u.Query()["fl[]"] == nil || u.Query().Get("sort[]") != "downloads desc" {
		t.Errorf("address %s", c.URL)
	}
	if _, err := ia.BuildRequest(Query{Text: `"*?"`}, nil); !errors.Is(err, ErrEmptyQuery) {
		t.Errorf("only stripped characters: %v", err)
	}
}

// A source matched on the NAS asks the same address for every keyword.
func TestBuildRequestLocal(t *testing.T) {
	p := Builtin("fedora")
	a, err := p.BuildRequest(Query{Text: "fedora kde"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := p.BuildRequest(Query{Text: "workstation", Category: "software"}, nil)
	if a.URL != "https://torrent.fedoraproject.org/rss20.xml" || a.URL != b.URL {
		t.Errorf("%s %s", a.URL, b.URL)
	}
}

func TestBuildRequestHeaderSetting(t *testing.T) {
	p := mustParse(t, variant(t, func(m map[string]any) {
		req(m)["headers"] = map[string]any{"Authorization": "Bearer {setting:apikey}"}
	}))
	c, err := p.BuildRequest(Query{Text: "x"}, map[string]string{"apikey": "a b+c"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Headers["Authorization"] != "Bearer a b+c" {
		t.Errorf("headers %v", c.Headers)
	}
}

func TestTorznabRequest(t *testing.T) {
	p := mustParse(t, []byte(`{"schema":1,"id":"jackett","name":"Jackett","type":"torznab"}`))
	cases := []struct {
		base, cat, apikey string
		want              url.Values
	}{
		{"http://192.168.1.20:9117/api/v2.0/indexers/all/results/torznab/", "", "KEY",
			url.Values{"t": {"search"}, "q": {"big buck bunny"}, "limit": {"100"}, "apikey": {"KEY"}}},
		{"http://192.168.1.21:9696/3/api", "video", "KEY",
			url.Values{"t": {"search"}, "q": {"big buck bunny"}, "limit": {"100"}, "apikey": {"KEY"}, "cat": {"2000,5000"}}},
		{"https://torznab.example.org/api?cat=1&t=caps&extra=1", "books", "",
			url.Values{"t": {"search"}, "q": {"big buck bunny"}, "limit": {"100"}, "cat": {"7000"}, "extra": {"1"}}},
		{"https://torznab.example.org/api", CategoryAll, "",
			url.Values{"t": {"search"}, "q": {"big buck bunny"}, "limit": {"100"}}},
	}
	for _, c := range cases {
		call, err := p.BuildRequest(Query{Text: " big  buck bunny ", Category: c.cat}, map[string]string{"url": c.base, "apikey": c.apikey})
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(call.URL)
		b, _ := url.Parse(c.base)
		if u.Host != b.Host || u.Path != b.Path {
			t.Errorf("%s: %s", c.base, call.URL)
		}
		if got := u.Query(); got.Encode() != c.want.Encode() {
			t.Errorf("%s: %v, want %v", c.base, got, c.want)
		}
	}
	if _, err := p.BuildRequest(Query{Text: "x"}, map[string]string{"url": "ftp://h/"}); err == nil {
		t.Error("ftp server accepted")
	}
	if _, err := p.BuildRequest(Query{Text: "x"}, nil); err == nil {
		t.Error("no server accepted")
	}
	// An API key in the address would not be kept as a secret
	for _, u := range []string{"https://torznab.example.org/api?apikey=INURL", "https://torznab.example.org/api?x=1&ApiKey=INURL"} {
		if _, err := p.BuildRequest(Query{Text: "x"}, map[string]string{"url": u}); err == nil || !strings.Contains(err.Error(), "API key") {
			t.Errorf("%s: %v", u, err)
		}
		if err := p.CheckSettings(map[string]string{"url": u}); err == nil {
			t.Errorf("%s accepted as a setting", u)
		}
	}
}
