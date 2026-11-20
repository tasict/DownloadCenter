package search

import (
	"encoding/base32"
	"encoding/hex"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"downloadcenter/internal/torrent"
	"downloadcenter/internal/trackers"
)

// Result is one search result. Everything in it came from a third-party
// site and was checked here: a title without control characters, an
// infohash of 40 hex digits, a magnet link built by dcd, http(s) addresses
// only. Unknown numbers are nil, an unknown date 0.
type Result struct {
	Title    string   `json:"title"`
	Size     *int64   `json:"size,omitempty"`
	Seeders  *int64   `json:"seeders,omitempty"`
	Leechers *int64   `json:"leechers,omitempty"`
	WebSeed  bool     `json:"webseed,omitempty"`
	Date     int64    `json:"date,omitempty"` // Unix seconds
	Category string   `json:"category,omitempty"`
	Sources  []string `json:"sources"`
	Page     string   `json:"page,omitempty"`
	InfoHash string   `json:"infohash,omitempty"` // lower-case hex
	Magnet   string   `json:"magnet,omitempty"`
	// TorrentURL and Ref are set by the search job: the .torrent address
	// when it may be shown (no secret in it, not the administrator's
	// server), and the code to fetch the .torrent through dcd.
	TorrentURL string `json:"torrent_url,omitempty"`
	Ref        string `json:"ref,omitempty"`

	// Torrent is the .torrent address as the source gave it; it may carry
	// the source's API key, so it is never encoded.
	Torrent string `json:"-"`
	// TorrentSource is the id of the source Torrent came from.
	TorrentSource string `json:"-"`
	// Trackers are the trackers of the magnet link (at most MaxTrackers).
	Trackers []string `json:"-"`
}

// raw is an item as read from an answer, before any check.
type raw struct {
	title, size, infohash, magnet, torrent, page, date, seeders, leechers string
	category                                                              string // our category, "" when unknown
	peers                                                                 string // torznab: seeders + leechers
}

// normalise turns an item into a result; ok is false for an item without
// a title or without anything to add (an infohash or a .torrent address).
func (p *Plugin) normalise(r raw, base *url.URL) (Result, bool) {
	res := Result{Title: cleanTitle(r.title), Sources: []string{p.ID}, WebSeed: p.WebSeed, Category: r.category}
	if res.Title == "" {
		return res, false
	}
	res.InfoHash = normHash(r.infohash)
	magnet := strings.TrimSpace(r.magnet)
	link := strings.TrimSpace(r.torrent)
	if isMagnet(link) {
		// Torznab servers put the magnet link where the .torrent address goes
		// when there is no .torrent
		if magnet == "" {
			magnet = link
		}
		link = ""
	}
	if magnet != "" {
		if h, tr := parseMagnet(magnet); h != "" {
			if res.InfoHash == "" {
				res.InfoHash = h
			}
			res.Trackers = tr
		}
	}
	res.Torrent = cleanURL(link, base)
	if res.Torrent != "" {
		res.TorrentSource = p.ID
	}
	res.Page = cleanURL(r.page, base)
	if res.InfoHash == "" && res.Torrent == "" {
		return res, false
	}
	if res.InfoHash != "" && !(p.Prefer == "torrent" && res.Torrent != "") {
		res.Magnet = torrent.MagnetURI(res.InfoHash, res.Title, res.Trackers)
	}
	if n, ok := number(r.size); ok && n > 0 {
		res.Size = &n
	}
	seeders, sok := number(r.seeders)
	if sok {
		res.Seeders = &seeders
	}
	if n, ok := number(r.leechers); ok {
		res.Leechers = &n
	} else if peers, ok := number(r.peers); ok && sok {
		n := max(peers-seeders, 0)
		res.Leechers = &n
	}
	res.Date = parseDate(r.date)
	return res, true
}

// cleanTitle drops control and bidirectional formatting characters, turns
// runs of white space into one space and cuts the title to MaxTitle
// characters.
func cleanTitle(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case unicode.IsControl(r), r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0x200e, r == 0x200f, r == 0xfeff:
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > MaxTitle {
		s = strings.TrimSpace(string([]rune(s)[:MaxTitle]))
	}
	return s
}

// normHash accepts 40 hex digits or 32 base32 characters and returns lower
// case hex, "" otherwise.
func normHash(s string) string {
	s = strings.TrimSpace(s)
	switch len(s) {
	case 40:
		if _, err := hex.DecodeString(s); err == nil {
			return strings.ToLower(s)
		}
	case 32:
		if b, err := base32.StdEncoding.DecodeString(strings.ToUpper(s)); err == nil && len(b) == 20 {
			return hex.EncodeToString(b)
		}
	}
	return ""
}

func isMagnet(s string) bool {
	return len(s) >= 7 && strings.EqualFold(s[:7], "magnet:")
}

// parseMagnet reads the btih infohash and the usable trackers (http, https
// or udp, at most MaxTrackers) of a magnet link; everything else in it is
// dropped, the link is rebuilt by normalise.
func parseMagnet(s string) (string, []string) {
	if !isMagnet(s) || len(s) > 16<<10 {
		return "", nil
	}
	_, rq, _ := strings.Cut(s, "?")
	rq, _, _ = strings.Cut(rq, "#")
	q, _ := url.ParseQuery(rq) // the pairs that parse, even if some do not
	hash := ""
	for _, xt := range q["xt"] {
		if h, ok := strings.CutPrefix(xt, "urn:btih:"); ok {
			if hash = normHash(h); hash != "" {
				break
			}
		}
	}
	if hash == "" {
		return "", nil
	}
	return hash, addTrackers(nil, q["tr"])
}

// addTrackers appends the usable trackers of add that list lacks, up to
// MaxTrackers: valid http, https or udp addresses on hosts that may be
// handed on (trackers.Public), never one inside the network or on the NAS.
func addTrackers(list, add []string) []string {
	for _, t := range add {
		if len(list) >= MaxTrackers {
			break
		}
		t = strings.TrimSpace(t)
		if !trackers.Public(t) {
			continue
		}
		dup := false
		for _, have := range list {
			if have == t {
				dup = true
				break
			}
		}
		if !dup {
			list = append(list, t)
		}
	}
	return list
}

// cleanURL is an http or https address (relative ones resolved against
// base), "" for anything else.
func cleanURL(s string, base *url.URL) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxURL || !printable(s) || strings.ContainsAny(s, " \\") {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	if !u.IsAbs() && base != nil {
		u = base.ResolveReference(u)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" || u.Opaque != "" || u.User != nil {
		return ""
	}
	return u.String()
}

// number reads a count or a size: a JSON number or a numeric string. ok is
// false when there is none or it is negative.
func number(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, n >= 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || f < 0 || f >= 1<<62 {
		return 0, false
	}
	return int64(f), true
}

// Date forms read in answers; dates without a zone are UTC.
var dateLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	time.RFC1123Z,
	time.RFC1123,
	"Mon, 2 Jan 2006 15:04:05 -0700",
	"Mon, 2 Jan 2006 15:04:05 MST",
	"Mon, 2 Jan 2006 15:04:05",
	"Mon, 2 Jan 2006 15:04 -0700",
	"2 Jan 2006 15:04:05 -0700",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// parseDate reads RFC 3339, RFC 1123 (and the usual RSS variants of it,
// with or without a zone), 2006-01-02 or Unix seconds (milliseconds when
// the number is too large for seconds); 0 when it is none of these.
func parseDate(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if n, ok := number(s); ok {
		if n > 1e11 {
			n /= 1000
		}
		return n
	}
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			if t.Unix() > 0 {
				return t.Unix()
			}
			return 0
		}
	}
	return 0
}

// Select applies a search to the results of one answer (Results): for a
// source matched on the NAS the titles that have every word of the
// keyword, the category of results that have none, at most MaxResults. The
// results given are not changed, so they can be kept between searches.
//
// Results of a source without a category field have none: they get the
// category the search asked for when the source declares it (that is why
// the source was asked), else the source's only category if it declares
// just one, else "other".
func (p *Plugin) Select(rs []Result, q Query) []Result {
	out := []Result{}
	for _, r := range rs {
		if len(out) == MaxResults {
			break
		}
		if p.Match == "local" && !MatchTitle(r.Title, q.Text) {
			continue
		}
		if r.Category == "" {
			r.Category = p.defaultCategory(q.Category)
		}
		r.Sources = append([]string{}, r.Sources...)
		r.Trackers = append([]string(nil), r.Trackers...)
		out = append(out, r)
	}
	return out
}

func (p *Plugin) defaultCategory(asked string) string {
	if _, ok := p.Categories[asked]; ok && validCategory(asked) {
		return asked
	}
	if len(p.Categories) == 1 {
		for c := range p.Categories {
			return c
		}
	}
	return "other"
}

// MatchTitle reports whether title has every word of keyword, ignoring
// case.
func MatchTitle(title, keyword string) bool {
	t := strings.ToLower(title)
	words := strings.Fields(strings.ToLower(keyword))
	if len(words) == 0 {
		return false
	}
	for _, w := range words {
		if !strings.Contains(t, w) {
			return false
		}
	}
	return true
}

// Merge joins the results of several sources: results with the same
// infohash become one, listing every source, with the highest seeder and
// leecher counts and the trackers of all (up to MaxTrackers); the first
// source's title, size, date, page and .torrent address are kept and the
// gaps filled from the others. A merged result offers a magnet link when
// any of its sources does (one that prefers .torrent files may share the
// result with one that gives magnet links). Results without an infohash are
// never merged. The order is the order of first appearance.
func Merge(lists [][]Result) []Result {
	out := []Result{}
	at := map[string]int{}
	for _, list := range lists {
		for _, r := range list {
			if i, ok := at[r.InfoHash]; ok && r.InfoHash != "" {
				out[i] = join(out[i], r)
				continue
			}
			r.Sources = append([]string{}, r.Sources...)
			r.Trackers = append([]string(nil), r.Trackers...)
			if r.InfoHash != "" {
				at[r.InfoHash] = len(out)
			}
			out = append(out, r)
		}
	}
	return out
}

func join(a, b Result) Result {
	for _, s := range b.Sources {
		dup := false
		for _, have := range a.Sources {
			dup = dup || have == s
		}
		if !dup {
			a.Sources = append(a.Sources, s)
		}
	}
	a.Seeders = maxCount(a.Seeders, b.Seeders)
	a.Leechers = maxCount(a.Leechers, b.Leechers)
	a.WebSeed = a.WebSeed || b.WebSeed
	if a.Size == nil {
		a.Size = b.Size
	}
	if a.Date == 0 {
		a.Date = b.Date
	}
	if a.Page == "" {
		a.Page = b.Page
	}
	if a.Category == "" || (a.Category == "other" && b.Category != "") {
		a.Category = b.Category
	}
	if a.Torrent == "" && b.Torrent != "" {
		a.Torrent, a.TorrentSource, a.Ref, a.TorrentURL = b.Torrent, b.TorrentSource, b.Ref, b.TorrentURL
	}
	magnet := a.Magnet != "" || b.Magnet != ""
	a.Trackers = addTrackers(a.Trackers, b.Trackers)
	a.Magnet = ""
	if magnet {
		a.Magnet = torrent.MagnetURI(a.InfoHash, a.Title, a.Trackers)
	}
	return a
}

func maxCount(a, b *int64) *int64 {
	if a == nil || (b != nil && *b > *a) {
		a = b
	}
	if a == nil {
		return nil
	}
	n := *a
	return &n
}
