package search

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// TorznabError is an answer that is a Torznab (Newznab) <error> element,
// whatever the HTTP status it came with.
type TorznabError struct {
	Code        int
	Description string
}

func (e *TorznabError) Error() string {
	return fmt.Sprintf("Torznab error %d: %s", e.Code, e.Description)
}

// Kind sorts the error: "auth" for the credential and account errors
// (100-199), "busy" for the request and download limits (500, 501), ""
// for the rest.
func (e *TorznabError) Kind() string {
	switch {
	case e.Code >= 100 && e.Code < 200:
		return "auth"
	case e.Code == 500 || e.Code == 501:
		return "busy"
	}
	return ""
}

const (
	atomNS  = "http://www.w3.org/2005/Atom"
	rss1NS  = "http://purl.org/rss/1.0/"
	maxText = 64 << 10 // characters kept of one element's text
	maxKids = 200      // child elements kept of one item
	maxDeep = 64       // element nesting
)

// element is a child element of an item: its name, attributes and all the
// text inside it.
type element struct {
	space, local string
	attrs        []xml.Attr
	text         string
}

func (e *element) attr(name string) string {
	for _, a := range e.attrs {
		if a.Name.Local == name {
			return strings.TrimSpace(a.Value)
		}
	}
	return ""
}

// plain reports an element of RSS 2.0 (no namespace), RSS 1.0 or Atom.
func (e *element) plain() bool { return e.space == "" || e.space == atomNS || e.space == rss1NS }

// item is an RSS <item> or Atom <entry>: its child elements in order.
type item []element

// readFeed reads the items of an RSS 2.0, RSS 1.0 or Atom answer.
func (p *Plugin) readFeed(body []byte) ([]raw, error) {
	items, err := parseFeed(body)
	if err != nil {
		return nil, err
	}
	out := make([]raw, 0, len(items))
	for _, it := range items {
		if p.Type == TypeTorznab {
			out = append(out, it.torznab())
		} else if r, ok := p.extract(it.get); ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// parseFeed walks the document: an <error> root is a *TorznabError, a root
// other than rss, RDF or feed is not a feed; every item or entry (at any
// depth, not inside another) is kept with its direct children. Entities
// other than XML's and HTML's are not expanded.
func parseFeed(body []byte) ([]item, error) {
	d := xml.NewDecoder(bytes.NewReader(body))
	d.Strict = true
	d.Entity = xml.HTMLEntity
	d.CharsetReader = charsetReader
	var (
		items     []item
		cur       item
		child     *element
		text      strings.Builder
		depth     int
		itemDepth int // depth of the open item, 0 for none
		root      = true
	)
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, unreadable("%v", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if depth > maxDeep {
				return nil, unreadable("elements nested too deep")
			}
			if root {
				root = false
				switch t.Name.Local {
				case "error":
					e := &element{attrs: t.Attr}
					code, _ := strconv.Atoi(e.attr("code"))
					return nil, &TorznabError{Code: code, Description: e.attr("description")}
				case "rss", "RDF", "feed":
				default:
					return nil, unreadable("not an RSS or Atom feed")
				}
			}
			switch {
			case itemDepth == 0 && (t.Name.Local == "item" || t.Name.Local == "entry"):
				itemDepth, cur = depth, nil
			case itemDepth > 0 && depth == itemDepth+1 && len(cur) < maxKids:
				child = &element{space: t.Name.Space, local: t.Name.Local, attrs: t.Copy().Attr}
				text.Reset()
			}
		case xml.CharData:
			if child != nil && text.Len() < maxText {
				text.Write(t)
			}
		case xml.EndElement:
			if child != nil && depth == itemDepth+1 {
				child.text = strings.TrimSpace(text.String())
				cur = append(cur, *child)
				child = nil
			}
			if itemDepth > 0 && depth == itemDepth {
				items = append(items, cur)
				itemDepth = 0
				if len(items) == MaxItems {
					return items, nil
				}
			}
			depth--
		}
	}
	if root {
		return nil, unreadable("empty answer")
	}
	return items, nil
}

// charsetReader reads the encodings besides UTF-8 that feeds use: ASCII and
// Latin-1 (Windows-1252 read as Latin-1).
func charsetReader(label string, in io.Reader) (io.Reader, error) {
	switch strings.ToLower(label) {
	case "utf-8", "utf8", "us-ascii", "ascii":
		return in, nil
	case "iso-8859-1", "iso8859-1", "latin1", "latin-1", "windows-1252", "cp1252":
		b, err := io.ReadAll(in)
		if err != nil {
			return nil, err
		}
		out := make([]byte, 0, len(b)*2)
		for _, c := range b {
			out = utf8.AppendRune(out, rune(c))
		}
		return bytes.NewReader(out), nil
	}
	return nil, fmt.Errorf("unsupported encoding %q", label)
}

// get is the value an rss field names (see rssNames).
func (it item) get(name string) string {
	switch name {
	case "title":
		return it.text(func(e *element) bool { return e.plain() && e.local == "title" })
	case "guid":
		return it.text(func(e *element) bool { return e.plain() && (e.local == "guid" || e.local == "id") })
	case "link":
		if s := it.text(func(e *element) bool { return e.plain() && e.local == "link" }); s != "" {
			return s
		}
		return it.atomLink()
	case "pubDate":
		for _, n := range []string{"pubDate", "published", "issued", "updated", "date"} {
			if s := it.text(func(e *element) bool { return e.local == n }); s != "" {
				return s
			}
		}
		return ""
	case "enclosure.url":
		return it.enclosure("url", "href")
	case "enclosure.length":
		return it.enclosure("length", "length")
	}
	if n, ok := strings.CutPrefix(name, "attr:"); ok {
		return it.attr(n)
	}
	if n, ok := strings.CutPrefix(name, "ns:"); ok {
		return it.text(func(e *element) bool { return e.local == n })
	}
	return ""
}

// text is the text of the first child that ok accepts and has some.
func (it item) text(ok func(*element) bool) string {
	for i := range it {
		if e := &it[i]; ok(e) && e.text != "" {
			return e.text
		}
	}
	return ""
}

// atomLink is the href of the entry's page link: rel missing or
// "alternate", the text/html one first.
func (it item) atomLink() string {
	first := ""
	for i := range it {
		e := &it[i]
		if !e.plain() || e.local != "link" {
			continue
		}
		rel, href := e.attr("rel"), e.attr("href")
		if href == "" || (rel != "" && rel != "alternate") {
			continue
		}
		if strings.HasPrefix(e.attr("type"), "text/html") {
			return href
		}
		if first == "" {
			first = href
		}
	}
	return first
}

// enclosure reads the RSS <enclosure> (attribute rss) or else the Atom link
// whose rel is an acquisition or enclosure (attribute atom).
func (it item) enclosure(rss, atom string) string {
	for i := range it {
		if e := &it[i]; e.plain() && e.local == "enclosure" {
			return e.attr(rss)
		}
	}
	for i := range it {
		e := &it[i]
		if !e.plain() || e.local != "link" || e.attr("href") == "" {
			continue
		}
		if rel := e.attr("rel"); strings.Contains(rel, "acquisition") || strings.Contains(rel, "enclosure") {
			return e.attr(atom)
		}
	}
	return ""
}

// attr is the value of the first torznab:attr or newznab:attr with name.
func (it item) attr(name string) string {
	for i := range it {
		if e := &it[i]; e.local == "attr" && e.attr("name") == name {
			return e.attr("value")
		}
	}
	return ""
}

// torznab reads an item of a Torznab answer.
func (it item) torznab() raw {
	r := raw{
		title:    it.get("title"),
		infohash: it.attr("infohash"),
		magnet:   it.attr("magneturl"),
		seeders:  it.attr("seeders"),
		leechers: it.attr("leechers"),
		peers:    it.attr("peers"),
		date:     it.get("pubDate"),
	}
	for _, s := range []string{it.get("ns:size"), it.get("enclosure.length"), it.attr("size")} {
		if s != "" {
			r.size = s
			break
		}
	}
	if r.torrent = it.get("enclosure.url"); r.torrent == "" {
		r.torrent = it.get("link")
	}
	// The details page. Not the guid: servers pass a tracker's guid on as it
	// is, and it can be the tracker's own download address with a passkey
	r.page = it.get("ns:comments")
	r.category = "other"
	var cats []string
	for i := range it {
		e := &it[i]
		if e.local == "attr" && e.attr("name") == "category" {
			cats = append(cats, e.attr("value"))
		}
	}
	for i := range it {
		if e := &it[i]; e.plain() && e.local == "category" {
			cats = append(cats, e.text)
		}
	}
	for _, c := range cats {
		if n, err := strconv.Atoi(strings.TrimSpace(c)); err == nil {
			if m := torznabCategory(n); m != "" {
				r.category = m
				break
			}
		}
	}
	return r
}

// torznabCategory maps a standard Newznab category (by its thousands) to
// ours; "" for site-specific ones (100000 and up) and unknown numbers.
func torznabCategory(n int) string {
	if n < 1000 || n >= 9000 {
		return ""
	}
	switch n / 1000 {
	case 1, 4: // console, PC
		return "software"
	case 2, 5: // movies, TV
		return "video"
	case 3:
		return "audio"
	case 7:
		return "books"
	}
	return "other" // XXX, other
}
