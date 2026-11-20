package search

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func jsonOf(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

const hashA = "6162636465666768696a6b6c6d6e6f7071727374"

func plainPlugin(t *testing.T, prefer string) *Plugin {
	p := mustParse(t, []byte(`{"schema":1,"id":"src","name":"Src","type":"json","prefer":"`+prefer+`",
		"request":{"url":"https://api.example.org/?q={query}"},
		"response":{"fields":{"title":"t","infohash":"h","magnet":"m","torrent":"u","page":"p"}}}`))
	return p
}

func TestNormalise(t *testing.T) {
	p := plainPlugin(t, "")
	var trs []string
	for i := 0; i < 30; i++ {
		trs = append(trs, "tr="+url.QueryEscape(fmt.Sprintf("udp://t%d.example.org:6969/announce", i)))
	}
	bad := "tr=" + url.QueryEscape("wss://w.example.org") + "&tr=" + url.QueryEscape("javascript:alert(1)") + "&tr=" + url.QueryEscape("udp://noport.example.org")
	stuffed := "magnet:?xt=urn:btih:" + strings.ToUpper(hashA) + "&dn=Other+name&xs=" + url.QueryEscape("http://evil.example.org/x.torrent") +
		"&as=" + url.QueryEscape("http://evil.example.org/x") + "&ws=" + url.QueryEscape("http://evil.example.org/") + "&" + bad + "&" + strings.Join(trs, "&")

	cases := []struct {
		name string
		in   raw
		ok   bool
		test func(r Result) string
	}{
		// spec: a javascript: page is dropped, the result kept
		{"javascript page", raw{title: "A", infohash: hashA, page: "javascript:alert(1)"}, true, func(r Result) string {
			if r.Page != "" || r.Magnet == "" {
				return "page kept"
			}
			return ""
		}},
		// spec: an invalid infohash without a .torrent drops the result
		{"short infohash", raw{title: "A", infohash: "0123456789ab"}, false, nil},
		{"infohash not hex", raw{title: "A", infohash: strings.Repeat("g", 40)}, false, nil},
		{"base32 infohash", raw{title: "A", infohash: "MFRGGZDFMZTWQ2LKNNWG23TPOBYXE43U"}, true, func(r Result) string {
			if r.InfoHash != hashA {
				return r.InfoHash
			}
			return ""
		}},
		{"no title", raw{title: " \t", infohash: hashA}, false, nil},
		{"no link", raw{title: "A", page: "https://x.org/"}, false, nil},
		{"magnet without infohash", raw{title: "A", magnet: "magnet:?dn=x&tr=udp%3A%2F%2Ft.org%3A1"}, false, nil},
		{"ftp torrent", raw{title: "A", torrent: "ftp://x.org/a.torrent"}, false, nil},
		{"file torrent", raw{title: "A", torrent: "file:///etc/passwd"}, false, nil},
		{"data torrent", raw{title: "A", torrent: "data:text/html,x"}, false, nil},
		{"torrent with user name", raw{title: "A", torrent: "http://u:p@x.org/a.torrent"}, false, nil},
		{"relative torrent without base", raw{title: "A", torrent: "/a.torrent"}, false, nil},
		{"https torrent", raw{title: "A", torrent: " https://x.org/a.torrent "}, true, func(r Result) string {
			if r.Torrent != "https://x.org/a.torrent" || r.Magnet != "" || r.InfoHash != "" {
				return fmt.Sprint(r)
			}
			return ""
		}},
		// spec: a magnet with other parameters and 30 trackers is rebuilt
		{"stuffed magnet", raw{title: "Name", magnet: stuffed}, true, func(r Result) string {
			u, err := url.Parse(r.Magnet)
			if err != nil {
				return err.Error()
			}
			q := u.Query()
			if len(q) != 3 || q.Get("xt") != "urn:btih:"+hashA || q.Get("dn") != "Name" || len(q["tr"]) != MaxTrackers ||
				q["tr"][0] != "udp://t0.example.org:6969/announce" || q["tr"][19] != "udp://t19.example.org:6969/announce" {
				return r.Magnet
			}
			if strings.Contains(r.Magnet, "evil") || strings.Contains(r.Magnet, "wss") || strings.Contains(r.Magnet, "javascript") {
				return r.Magnet
			}
			return ""
		}},
		{"magnet in the torrent field", raw{title: "A", torrent: "magnet:?xt=urn:btih:" + hashA}, true, func(r Result) string {
			if r.Torrent != "" || r.InfoHash != hashA || r.Magnet == "" {
				return fmt.Sprint(r)
			}
			return ""
		}},
		{"title cleaned", raw{title: "‮A\x00b\r\n\tc\u0085  d‏", infohash: hashA}, true, func(r Result) string {
			if r.Title != "Ab c d" {
				return fmt.Sprintf("%q", r.Title)
			}
			return ""
		}},
		{"long title", raw{title: strings.Repeat("é", 400), infohash: hashA}, true, func(r Result) string {
			if len([]rune(r.Title)) != MaxTitle {
				return fmt.Sprint(len([]rune(r.Title)))
			}
			return ""
		}},
		{"html title stays text", raw{title: `<img src=x onerror=alert(1)>`, infohash: hashA}, true, func(r Result) string {
			if r.Title != `<img src=x onerror=alert(1)>` {
				return r.Title
			}
			return ""
		}},
	}
	for _, c := range cases {
		r, ok := p.normalise(c.in, nil)
		if ok != c.ok {
			t.Errorf("%s: ok = %v (%+v)", c.name, ok, r)
			continue
		}
		if ok && c.test != nil {
			if msg := c.test(r); msg != "" {
				t.Errorf("%s: %s", c.name, msg)
			}
		}
	}
}

// spec: a source that prefers .torrent files gives no magnet when it has one
func TestNormalisePreferTorrent(t *testing.T) {
	p := plainPlugin(t, "torrent")
	r, ok := p.normalise(raw{title: "A", infohash: hashA, torrent: "https://x.org/a.torrent"}, nil)
	if !ok || r.Magnet != "" || r.InfoHash != hashA || r.Torrent == "" {
		t.Errorf("%+v", r)
	}
	// Without a usable .torrent address the magnet is all there is
	r, ok = p.normalise(raw{title: "A", infohash: hashA, torrent: "ftp://x.org/a.torrent"}, nil)
	if !ok || r.Magnet == "" {
		t.Errorf("%+v", r)
	}
}

func TestNumbersAndDates(t *testing.T) {
	for s, want := range map[string]int64{"12": 12, " 7 ": 7, "1.5e3": 1500, "0": 0, "-1": -1, "x": -1, "": -1, "NaN": -1, "1e30": -1} {
		n, ok := number(s)
		if !ok {
			n = -1
		}
		if n != want {
			t.Errorf("number(%q) = %d", s, n)
		}
	}
	for s, want := range map[string]string{
		"2024-03-19T23:29:51Z":            "2024-03-19T23:29:51Z",
		"2026-11-01T08:54:41.636Z":        "2026-11-01T08:54:41Z",
		"2026-11-01":                      "2026-11-01T00:00:00Z",
		"Tue, 28 Oct 2025 00:00:00":       "2025-10-28T00:00:00Z",
		"Wed, 6 May 2026 23:04:10 -0300":  "2026-05-07T02:04:10Z",
		"Sat, 14 Mar 2015 17:10:42 -0400": "2015-03-14T21:10:42Z",
		"Mon, 02 Jan 2006 15:04:05 GMT":   "2006-01-02T15:04:05Z",
		"1700000000":                      "2023-11-14T22:13:20Z",
		"1700000000000":                   "2023-11-14T22:13:20Z",
	} {
		if got := parseDate(s); got != unix(want) {
			t.Errorf("parseDate(%q) = %d, want %s", s, got, want)
		}
	}
	for _, s := range []string{"", "yesterday", "0001-01-01T00:00:00Z"} {
		if got := parseDate(s); got != 0 {
			t.Errorf("parseDate(%q) = %d", s, got)
		}
	}
}

func TestMatchTitle(t *testing.T) {
	cases := []struct {
		title, kw string
		want      bool
	}{
		{"Fedora KDE Desktop Live x86_64 43", "fedora kde", true},
		{"Fedora KDE Desktop Live x86_64 43", "KDE   fedora", true},
		{"Fedora Workstation Live x86_64 43", "fedora kde", false},
		{"Ubuntu 26.04 - Desktop (amd64)", "ubuntu 26.04", true},
		{"Anything", "  ", false},
	}
	for _, c := range cases {
		if got := MatchTitle(c.title, c.kw); got != c.want {
			t.Errorf("%q %q: %v", c.title, c.kw, got)
		}
	}
}

func count(n int64) *int64 { return &n }

// spec: two sources with the same infohash give one result
func TestMerge(t *testing.T) {
	trA := []string{"udp://a.example.org:1"}
	a := []Result{
		{Title: "Ubuntu", InfoHash: hashA, Sources: []string{"a"}, Seeders: count(12), Leechers: nil, Trackers: trA,
			Magnet: "magnet:?xt=urn:btih:" + hashA, Category: "other"},
		{Title: "No hash", Torrent: "https://x.org/1.torrent", TorrentSource: "a", Sources: []string{"a"}},
	}
	b := []Result{
		{Title: "Ubuntu (B)", InfoHash: hashA, Sources: []string{"b"}, Seeders: count(30), Leechers: count(4), Size: count(99),
			Torrent: "https://b.example.org/u.torrent", TorrentSource: "b", Trackers: []string{"udp://a.example.org:1", "udp://b.example.org:2"},
			Category: "software", Date: 5, Page: "https://b.example.org/u"},
		{Title: "No hash", Torrent: "https://x.org/1.torrent", TorrentSource: "b", Sources: []string{"b"}},
	}
	out := Merge([][]Result{a, b})
	if len(out) != 3 {
		t.Fatalf("%d results: %+v", len(out), out)
	}
	m := out[0]
	if m.Title != "Ubuntu" || strings.Join(m.Sources, ",") != "a,b" || n64(m.Seeders) != 30 || n64(m.Leechers) != 4 ||
		n64(m.Size) != 99 || m.Date != 5 || m.Page != "https://b.example.org/u" || m.Category != "software" ||
		m.Torrent != "https://b.example.org/u.torrent" || m.TorrentSource != "b" || len(m.Trackers) != 2 {
		t.Errorf("%+v", m)
	}
	if m.Magnet != "magnet:?xt=urn:btih:"+hashA+"&dn=Ubuntu&tr=udp%3A%2F%2Fa.example.org%3A1&tr=udp%3A%2F%2Fb.example.org%3A2" {
		t.Errorf("magnet %s", m.Magnet)
	}
	if out[1].Title != "No hash" || out[2].Title != "No hash" {
		t.Errorf("results without infohash merged: %+v", out[1:])
	}
	// The inputs are left as they were
	if len(a[0].Sources) != 1 || n64(a[0].Seeders) != 12 || len(a[0].Trackers) != 1 {
		t.Errorf("input changed: %+v", a[0])
	}

	// Results from a source that prefers .torrent files and one that gives
	// magnet links: the merged result has both
	torrentOnly := []Result{{Title: "X", InfoHash: hashA, Sources: []string{"ia"}, Torrent: "https://ia.example.org/x.torrent", TorrentSource: "ia"}}
	magnetOnly := []Result{{Title: "X", InfoHash: hashA, Sources: []string{"tz"}, Magnet: "magnet:?xt=urn:btih:" + hashA}}
	m = Merge([][]Result{torrentOnly, magnetOnly})[0]
	if m.Magnet == "" || m.Torrent == "" || m.TorrentSource != "ia" {
		t.Errorf("%+v", m)
	}
	m = Merge([][]Result{torrentOnly, torrentOnly})[0]
	if m.Magnet != "" || len(m.Sources) != 1 {
		t.Errorf("%+v", m)
	}
}

// Merging the Torznab samples: the Ubuntu torrent both servers found.
func TestMergeTorznab(t *testing.T) {
	j := results(t, torznabPlugin(t, "jackett"), "jackett.xml", "")
	p := results(t, torznabPlugin(t, "prowlarr"), "prowlarr.xml", "")
	out := Merge([][]Result{j, p})
	if len(out) != 4 {
		t.Fatalf("%d results", len(out))
	}
	if u := out[0]; strings.Join(u.Sources, ",") != "jackett,prowlarr" || n64(u.Seeders) != 240 || n64(u.Leechers) != 11 ||
		u.TorrentSource != "jackett" {
		t.Errorf("%+v", u)
	}
}

func TestResultJSON(t *testing.T) {
	r := Result{Title: "A", Sources: []string{"s"}, Seeders: count(0), Torrent: "http://h/?apikey=SECRET", Trackers: []string{"udp://t:1"}}
	s, _ := jsonOf(r)
	if s != `{"title":"A","seeders":0,"sources":["s"]}` {
		t.Errorf("%s", s)
	}
}
