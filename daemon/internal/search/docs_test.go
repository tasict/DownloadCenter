package search

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
)

// docBlocks are the ```json blocks of the plugin format document.
func docBlocks(t *testing.T) [][]byte {
	t.Helper()
	b, err := os.ReadFile("../../../docs/SEARCH-PLUGINS.md")
	if err != nil {
		t.Fatal(err)
	}
	var blocks [][]byte
	var cur *bytes.Buffer
	for _, line := range strings.Split(string(b), "\n") {
		switch {
		case cur == nil && strings.TrimSpace(line) == "```json":
			cur = &bytes.Buffer{}
		case cur != nil && strings.TrimSpace(line) == "```":
			blocks = append(blocks, cur.Bytes())
			cur = nil
		case cur != nil:
			cur.WriteString(line + "\n")
		}
	}
	if cur != nil {
		t.Fatal("unterminated ```json block")
	}
	return blocks
}

// Every JSON block of SEARCH-PLUGINS.md is valid JSON, and every complete
// plugin in it (an object with "schema") installs. The main example builds
// the request the document shows and reads the sample answer as it says.
func TestDocsExamples(t *testing.T) {
	plugins := map[string]*Plugin{}
	var answer []byte
	for i, b := range docBlocks(t) {
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("block %d is not a JSON object: %v\n%s", i, err, b)
		}
		if _, ok := m["schema"]; !ok {
			if _, ok := m["data"]; ok {
				answer = b
			}
			continue
		}
		p, err := ParseCustom(b)
		if err != nil {
			t.Fatalf("block %d does not install: %v\n%s", i, err, b)
		}
		plugins[p.ID] = p
	}
	if len(plugins) < 3 {
		t.Fatalf("%d plugins in the document", len(plugins))
	}
	p := plugins["example-library"]
	if p == nil {
		t.Fatal("the main example is missing")
	}
	if tz := plugins["jackett-lan"]; tz == nil || tz.Type != TypeTorznab {
		t.Error("the Torznab example is missing")
	}

	// The request of section 4.1
	settings := map[string]string{"apikey": "k/y&z", "language": "en"}
	if err := p.CheckSettings(settings); err != nil {
		t.Fatal(err)
	}
	c, err := p.BuildRequest(Query{Text: `big "buck" (bunny)`, Category: "video"}, settings)
	if err != nil {
		t.Fatal(err)
	}
	if c.URL != "https://api.example.org/v2/search?q=big+buck+bunny&kind=film&rows=100&page=1&lang=en" {
		t.Errorf("address %s", c.URL)
	}
	if c.Headers["Authorization"] != "Bearer k/y&z" || c.Headers["Accept"] != "application/json" {
		t.Errorf("headers %v", c.Headers)
	}
	// A setting is query-escaped in the address, put in as it is in a header
	c, err = p.BuildRequest(Query{Text: "x y", Category: CategoryAll}, map[string]string{"apikey": "a b", "language": "pt BR&x=1"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Host != "api.example.org" {
		t.Fatalf("address %s", c.URL)
	}
	q := u.Query()
	if q.Get("lang") != "pt BR&x=1" || q.Get("x") != "" || q.Get("kind") != "" || q.Get("q") != "x y" || c.Headers["Authorization"] != "Bearer a b" {
		t.Errorf("address %s, headers %v", c.URL, c.Headers)
	}
	if p.Asks("software") || !p.Asks("other") || !p.Complete(map[string]string{"apikey": "k"}) || p.Complete(nil) {
		t.Error("categories or settings")
	}

	// The sample answer of section 4.1
	if answer == nil {
		t.Fatal("the sample answer is missing")
	}
	rs, err := p.Results(answer, "https://api.example.org/v2/search?q=big+buck+bunny")
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 {
		t.Fatalf("%d results: %+v", len(rs), rs)
	}
	a := rs[0]
	if a.Title != "Big Buck Bunny (1080p)" || a.InfoHash != "3f9a2c0b7d5e41a8b6c2e9f0d1a47b3c5e8f2a61" ||
		a.Torrent != "https://dl.example.org/torrents/big-buck-bunny-1080p.torrent" || a.Magnet != "" ||
		a.Page != "https://www.example.org/films/big-buck-bunny-1080p" || n64(a.Size) != 276445467 ||
		n64(a.Seeders) != 31 || n64(a.Leechers) != 2 || a.Category != "video" || !a.WebSeed ||
		a.Date != unix("2008-05-20T00:00:00Z") {
		t.Errorf("first result %+v", a)
	}
	b := rs[1]
	if b.Title != "Field Recordings, Vol. 2" || b.Torrent != "" || b.InfoHash != "9b2e4d6f8a0c1e3b5d7f9a1c3e5b7d9f0a2c4e6b" ||
		b.Magnet != "magnet:?xt=urn:btih:9b2e4d6f8a0c1e3b5d7f9a1c3e5b7d9f0a2c4e6b&dn=Field+Recordings%2C+Vol.+2&tr=udp%3A%2F%2Ftracker.example.org%3A6969%2Fannounce" ||
		n64(b.Seeders) != 0 || b.Leechers != nil || b.Category != "audio" || !b.WebSeed || b.Date != 1717401600 {
		t.Errorf("second result %+v", b)
	}
	if sel := p.Select(rs, Query{Text: "bunny", Category: "video"}); len(sel) != 2 || sel[0].Category != "video" {
		t.Errorf("selected %+v", sel)
	}

	// The rss example of section 4.7 asks the same address for any keyword
	if r := plugins["example-os-releases"]; r == nil || r.Match != "local" {
		t.Error("the rss example is missing")
	} else if c, err := r.BuildRequest(Query{Text: "example os"}, nil); err != nil || c.URL != "https://releases.example.net/torrents.rss" {
		t.Errorf("rss example: %v %+v", err, c)
	}
}
