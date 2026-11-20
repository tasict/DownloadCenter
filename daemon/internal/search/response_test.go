package search

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func readData(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func results(t *testing.T, p *Plugin, file, base string) []Result {
	t.Helper()
	rs, err := p.Results(readData(t, file), base)
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	return rs
}

func unix(s string) int64 {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.Unix()
}

func n64(r *int64) int64 {
	if r == nil {
		return -1
	}
	return *r
}

func TestInternetArchive(t *testing.T) {
	ia := Builtin("internet-archive")
	rs := results(t, ia, "ia.json", "https://archive.org/advancedsearch.php?q=x")
	if len(rs) != 7 {
		t.Fatalf("%d results", len(rs))
	}
	r := rs[0]
	if r.Title != "ruffle Build 2024-03-15" || r.InfoHash != "f255aa62476b76f1219225dd053d05344af77452" ||
		r.Torrent != "https://archive.org/download/ruffle-rs-ruffle-2024-03-15/ruffle-rs-ruffle-2024-03-15_archive.torrent" ||
		r.Page != "https://archive.org/details/ruffle-rs-ruffle-2024-03-15" || n64(r.Size) != 98130639 ||
		r.Date != unix("2024-03-19T23:29:51Z") || r.Category != "software" || !r.WebSeed ||
		r.Seeders != nil || r.Leechers != nil || r.TorrentSource != "internet-archive" {
		t.Errorf("%+v", r)
	}
	// Torrents only: their web seeds are all there is, a magnet would stall
	for _, r := range rs {
		if r.Magnet != "" || r.Torrent == "" || r.InfoHash == "" {
			t.Errorf("%s: magnet %q torrent %q", r.Title, r.Magnet, r.Torrent)
		}
	}
	cats := []string{}
	for _, r := range rs {
		cats = append(cats, r.Category)
	}
	if strings.Join(cats, " ") != "software audio software other books video video" {
		t.Errorf("categories %v", cats)
	}
}

func TestArchLinux(t *testing.T) {
	arch := Builtin("archlinux")
	rs := results(t, arch, "arch.json", "https://archlinux.org/releng/releases/json/")
	// Releases no longer offered are left out even when they have an infohash
	if len(rs) != 2 {
		t.Fatalf("%d results: %+v", len(rs), rs)
	}
	r := rs[0]
	if r.Title != "Arch Linux 2026.11.01" || r.InfoHash != "ed0f013475535f19b5410d68fd20f627e4950b1f" ||
		r.Magnet != "magnet:?xt=urn:btih:ed0f013475535f19b5410d68fd20f627e4950b1f&dn=Arch+Linux+2026.11.01" ||
		r.Torrent != "" || n64(r.Size) != 1317568512 || r.Date != unix("2026-11-01T00:00:00Z") ||
		r.Page != "https://archlinux.org/releng/releases/2026.11.01/" {
		t.Errorf("%+v", r)
	}
	sel := arch.Select(rs, Query{Text: "Arch LINUX"})
	if len(sel) != 2 || sel[0].Category != "software" {
		t.Errorf("selected %+v", sel)
	}
	if sel := arch.Select(rs, Query{Text: "debian"}); len(sel) != 0 {
		t.Errorf("debian: %+v", sel)
	}

	// Without require: the release no longer offered comes back, the one
	// without an infohash still falls away
	loose := *arch
	resp := *arch.Response
	resp.Require = nil
	loose.Response = &resp
	if rs := results(t, &loose, "arch.json", ""); len(rs) != 3 {
		t.Errorf("without require: %d results", len(rs))
	}
}

func TestKiwix(t *testing.T) {
	k := Builtin("kiwix")
	rs := results(t, k, "kiwix.xml", "https://opds.library.kiwix.org/catalog/v2/entries?q=wikipedia&count=100")
	if len(rs) != 6 {
		t.Fatalf("%d results", len(rs))
	}
	r := rs[0]
	if r.Title != "Wikipedia 100 – Top one hundred Wikipedia articles" ||
		r.Torrent != "https://lb.download.kiwix.org/zim/wikipedia/wikipedia_en_100_2026-08.zim.torrent" ||
		r.Page != "https://browse.library.kiwix.org/content/wikipedia_en_100_2026-08" ||
		r.Date != unix("2026-08-20T00:00:00Z") || r.InfoHash != "" || r.Magnet != "" || !r.WebSeed || r.Size == nil {
		t.Errorf("%+v", r)
	}
	seen := map[string]bool{}
	for _, r := range rs {
		if seen[r.Title] {
			t.Errorf("title %q twice", r.Title)
		}
		seen[r.Title] = true
		if strings.Contains(r.Page, "illustration") {
			t.Errorf("thumbnail as page: %s", r.Page)
		}
	}
	if !seen["Wikipedia maxi – The free encyclopedia"] {
		t.Errorf("titles %v", seen)
	}
	// Kiwix is asked for books too: its results count as books then, as
	// other in a search of every category
	if sel := k.Select(rs, Query{Text: "wikipedia", Category: "books"}); sel[0].Category != "books" {
		t.Errorf("books: %s", sel[0].Category)
	}
	if sel := k.Select(rs, Query{Text: "wikipedia"}); sel[0].Category != "other" || len(sel) != 6 {
		t.Errorf("all: %s", sel[0].Category)
	}
}

// spec: a source matched on the NAS keeps the titles with every word
func TestFedoraLocalMatch(t *testing.T) {
	f := Builtin("fedora")
	rs := results(t, f, "fedora.xml", "https://torrent.fedoraproject.org/rss20.xml")
	if len(rs) != 8 {
		t.Fatalf("%d results", len(rs))
	}
	sel := f.Select(rs, Query{Text: "fedora KDE"})
	if len(sel) != 2 || sel[0].Title != "Fedora KDE Desktop Live aarch64 43" || sel[1].Title != "Fedora KDE Desktop Live x86_64 43" {
		t.Fatalf("selected %+v", sel)
	}
	r := sel[1]
	if r.Torrent != "https://torrent.fedoraproject.org/torrents/Fedora-KDE-Desktop-Live-x86_64-43.torrent" ||
		r.Date != unix("2025-10-28T00:00:00Z") || r.Category != "software" || r.Magnet != "" || r.Size != nil {
		t.Errorf("%+v", r)
	}
	// Select leaves the kept results alone
	if rs[0].Category != "" {
		t.Errorf("results changed: %+v", rs[0])
	}
}

func TestFOSSTorrents(t *testing.T) {
	f := Builtin("fosstorrents")
	rs := results(t, f, "foss.xml", "https://fosstorrents.com/feed/torrents.xml")
	if len(rs) != 4 {
		t.Fatalf("%d results", len(rs))
	}
	sel := f.Select(rs, Query{Text: "ubuntu desktop"})
	if len(sel) != 2 || sel[0].Torrent != "https://fosstorrents.com/direct-files/ubuntu-26.10-desktop-amd64.iso.torrent" ||
		sel[0].Date != unix("2026-10-20T02:04:10Z") || sel[0].InfoHash != "" {
		t.Errorf("selected %+v", sel)
	}
	// The guid is a SHA-256, not an infohash, and is not read
	for _, r := range rs {
		if r.InfoHash != "" || r.Page != "" {
			t.Errorf("%+v", r)
		}
	}
}

func torznabPlugin(t *testing.T, id string) *Plugin {
	return mustParse(t, []byte(`{"schema":1,"id":"`+id+`","name":"`+id+`","type":"torznab"}`))
}

func TestJackett(t *testing.T) {
	rs := results(t, torznabPlugin(t, "jackett"), "jackett.xml", "http://192.168.1.20:9117/api/v2.0/indexers/all/results/torznab/api?t=search")
	if len(rs) != 3 {
		t.Fatalf("%d results", len(rs))
	}
	u := rs[0]
	if u.Title != "Ubuntu 24.04 Desktop amd64" || u.InfoHash != "3e4b2c0a8f64c0f5d0e12a9c7b1d5e6f7a8b9c0d" ||
		!strings.Contains(u.Torrent, "jackett_apikey=SECRETKEY") || u.TorrentURL != "" ||
		n64(u.Seeders) != 182 || n64(u.Leechers) != 8 || n64(u.Size) != 6114656256 || u.Category != "software" ||
		u.Page != "https://tracker.example.org/details/1234" || u.Date != unix("2024-04-25T16:06:43Z") ||
		u.Magnet != "magnet:?xt=urn:btih:3e4b2c0a8f64c0f5d0e12a9c7b1d5e6f7a8b9c0d&dn=Ubuntu+24.04+Desktop+amd64" {
		t.Errorf("%+v", u)
	}
	// A magnet link where the .torrent address goes
	b := rs[1]
	if b.Torrent != "" || b.InfoHash != "dd8255ecdc7ca55fb0bbf81323d87062db1f6d1c" || n64(b.Seeders) != 0 || n64(b.Leechers) != 3 ||
		b.Category != "video" || b.Page != "" ||
		b.Magnet != "magnet:?xt=urn:btih:dd8255ecdc7ca55fb0bbf81323d87062db1f6d1c&dn=Big+Buck+Bunny+1080p&tr=udp%3A%2F%2Fexplodie.org%3A6969" {
		t.Errorf("%+v", b)
	}
	// More seeders than peers; a javascript: details page
	s := rs[2]
	if n64(s.Leechers) != 0 || s.Page != "" || s.InfoHash != "" || s.Magnet != "" || s.Category != "video" ||
		s.Date != unix("2024-06-04T08:30:00Z") {
		t.Errorf("%+v", s)
	}
	// Results never encode the address the source gave
	for _, r := range rs {
		if b, _ := jsonOf(r); strings.Contains(b, "SECRETKEY") {
			t.Errorf("address encoded: %s", b)
		}
	}
}

func TestProwlarr(t *testing.T) {
	rs := results(t, torznabPlugin(t, "prowlarr"), "prowlarr.xml", "http://192.168.1.21:9696/3/api")
	if len(rs) != 2 {
		t.Fatalf("%d results", len(rs))
	}
	u := rs[0]
	if n64(u.Seeders) != 240 || n64(u.Leechers) != 11 || u.Category != "software" || n64(u.Size) != 6114656256 ||
		!strings.HasPrefix(u.Torrent, "http://192.168.1.21:9696/3/download?apikey=PROWLARRKEY") || u.Magnet == "" {
		t.Errorf("%+v", u)
	}
	// No details page: the guid (a tracker download link) is not used
	tom := rs[1]
	if tom.Size != nil || tom.Category != "audio" || tom.InfoHash != "" || tom.Magnet != "" || n64(tom.Leechers) != 2 || tom.Page != "" {
		t.Errorf("%+v", tom)
	}
	for _, r := range rs {
		if b, _ := jsonOf(r); strings.Contains(b, "PROWLARRKEY") || strings.Contains(b, "TRACKERPASSKEY") {
			t.Errorf("secret encoded: %s", b)
		}
	}
}

// spec: a Torznab error is the source's failure, whatever the HTTP status
func TestTorznabError(t *testing.T) {
	_, err := torznabPlugin(t, "jackett").Results(readData(t, "torznab-error.xml"), "")
	var te *TorznabError
	if !errors.As(err, &te) || te.Code != 100 || te.Description != "Invalid API Key" || te.Kind() != "auth" {
		t.Fatalf("got %v", err)
	}
	for code, kind := range map[int]string{101: "auth", 500: "busy", 501: "busy", 900: "", 201: ""} {
		if k := (&TorznabError{Code: code}).Kind(); k != kind {
			t.Errorf("%d: %q", code, k)
		}
	}
}

func TestUnreadable(t *testing.T) {
	tz := torznabPlugin(t, "jackett")
	ia := Builtin("internet-archive")
	cases := []struct {
		p    *Plugin
		body string
	}{
		{tz, "<!DOCTYPE html><html><body>Login</body></html>"},
		{tz, "<html><body>x</body></html>"},
		{tz, ""},
		{tz, `<?xml version="1.0"?><!DOCTYPE r [<!ENTITY x SYSTEM "file:///etc/passwd">]><rss><channel><item><title>&x;</title></item></channel></rss>`},
		{tz, strings.Repeat("<rss>", 100)},
		{ia, "not json"},
		{ia, `{"response":{"docs":{}}}`},
		{ia, `{"response":[]}`},
	}
	for _, c := range cases {
		if _, err := c.p.Results([]byte(c.body), ""); !errors.Is(err, ErrUnreadable) {
			t.Errorf("%.40q: %v", c.body, err)
		}
	}
}

func TestFeedForms(t *testing.T) {
	p := mustParse(t, []byte(`{"schema":1,"id":"feed","name":"Feed","type":"rss","request":{"url":"https://feed.example.org/?q={query}"},
		"response":{"fields":{"title":"title","torrent":"enclosure.url","size":"enclosure.length","infohash":"ns:infoHash",
		"seeders":"attr:seeders","page":"link","date":"pubDate"}}}`))
	rss1 := `<?xml version="1.0" encoding="ISO-8859-1"?>
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns="http://purl.org/rss/1.0/" xmlns:t="urn:x">
<item><title>Caf` + "\xe9" + ` &amp; cr&egrave;me</title><link>/details/1</link><t:infoHash>MFRGGZDFMZTWQ2LKNNWG23TPOBYXE43U</t:infoHash>
<enclosure url="../files/1.torrent" length="1234"/><pubDate>Wed, 6 May 2026 23:04:10 -0300</pubDate></item>
</rdf:RDF>`
	rs, err := p.Results([]byte(rss1), "https://feed.example.org/feeds/rss?q=x")
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 {
		t.Fatalf("%+v", rs)
	}
	r := rs[0]
	if r.Title != "Café & crème" || r.Page != "https://feed.example.org/details/1" || r.Torrent != "https://feed.example.org/files/1.torrent" ||
		n64(r.Size) != 1234 || r.InfoHash != "6162636465666768696a6b6c6d6e6f7071727374" || r.Date != unix("2026-05-07T02:04:10Z") {
		t.Errorf("%+v", r)
	}

	atom := `<feed xmlns="http://www.w3.org/2005/Atom"><entry><title>A</title><id>urn:x</id>
<link rel="alternate" type="application/json" href="https://x.org/a.json"/><link href="https://x.org/a"/>
<link rel="enclosure" href="https://x.org/a.torrent" length="10"/><updated>2026-01-02T03:04:05Z</updated></entry>
<entry><title>B</title><link rel="http://opds-spec.org/acquisition" href="https://x.org/b.torrent"/></entry></feed>`
	rs, err = p.Results([]byte(atom), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 || rs[0].Page != "https://x.org/a.json" || rs[0].Torrent != "https://x.org/a.torrent" || n64(rs[0].Size) != 10 ||
		rs[0].Date != unix("2026-01-02T03:04:05Z") || rs[1].Torrent != "https://x.org/b.torrent" {
		t.Errorf("%+v", rs)
	}
}

func TestJSONForms(t *testing.T) {
	p := mustParse(t, []byte(`{"schema":1,"id":"api","name":"API","type":"json","request":{"url":"https://api.example.org/?q={query}"},
		"response":{"require":["ok"],"fields":{"title":"name.0","infohash":"hash","seeders":"stats.seeders",
		"torrent":"https://dl.example.org/{id}.torrent","page":{"from":"url","replace":["http:","https:"]},"date":"added","size":"bytes"}}}`))
	body := `[
		{"ok":1,"name":["First","x"],"hash":"6162636465666768696A6B6C6D6E6F7071727374","stats":{"seeders":"12"},"id":"a/b","url":"http://example.org/1","added":1700000000,"bytes":1.5e3},
		{"ok":true,"name":["No id"],"hash":"6162636465666768696a6b6c6d6e6f7071727375","added":"1700000000000"},
		{"ok":0,"name":["Skipped"],"hash":"6162636465666768696a6b6c6d6e6f7071727374"},
		{"ok":"yes","name":[],"hash":"6162636465666768696a6b6c6d6e6f7071727374"},
		{"ok":"yes","name":["Nothing to add"],"hash":"abc"}
	]`
	rs, err := p.Results([]byte(body), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 {
		t.Fatalf("%+v", rs)
	}
	a := rs[0]
	if a.Title != "First" || a.InfoHash != "6162636465666768696a6b6c6d6e6f7071727374" || n64(a.Seeders) != 12 ||
		a.Torrent != "https://dl.example.org/a%2Fb.torrent" || a.Page != "https://example.org/1" || a.Date != 1700000000 || n64(a.Size) != 1500 {
		t.Errorf("%+v", a)
	}
	// An address template with a missing value gives no address
	if b := rs[1]; b.Torrent != "" || b.Date != 1700000000 || b.Magnet == "" {
		t.Errorf("%+v", b)
	}
}

func TestResultsCap(t *testing.T) {
	var b strings.Builder
	b.WriteString("<rss><channel>")
	for i := 0; i < 300; i++ {
		b.WriteString("<item><title>Fedora item</title><link>https://x.org/f.torrent</link></item>")
	}
	b.WriteString("</channel></rss>")
	f := Builtin("fedora")
	rs, err := f.Results([]byte(b.String()), "")
	if err != nil || len(rs) != 300 {
		t.Fatalf("%d %v", len(rs), err)
	}
	if sel := f.Select(rs, Query{Text: "fedora"}); len(sel) != MaxResults {
		t.Errorf("%d selected", len(sel))
	}
}
