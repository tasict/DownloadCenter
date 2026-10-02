// Package torrent parses .torrent files (bencode) and magnet links.
package torrent

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
)

// File is one file of a torrent.
type File struct {
	Index int    `json:"index"`
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Root  string `json:"-"`             // BitTorrent v2 pieces root (hex), if present
	Pad   bool   `json:"pad,omitempty"` // BEP 47 padding file (engines still count it)
}

// Meta is the parsed content of a .torrent.
type Meta struct {
	InfoHash    string   `json:"infohash"` // v1 SHA-1 (hex); v2-only torrents use the truncated SHA-256
	InfoHashV2  string   `json:"infohash_v2,omitempty"`
	Name        string   `json:"name"`
	Files       []File   `json:"files"`
	Size        int64    `json:"size"`
	PieceLength int64    `json:"piece_length"`
	Trackers    []string `json:"trackers"`
	Comment     string   `json:"comment"`
	Private     bool     `json:"private"`
	IsFolder    bool     `json:"is_folder"`
	WebSeeds    []string `json:"web_seeds,omitempty"`
}

var ErrFormat = errors.New("torrent: invalid format")

// Parse parses a .torrent file.
func Parse(b []byte) (*Meta, error) {
	d := &decoder{b: b}
	v, err := d.value()
	if err != nil {
		return nil, err
	}
	root, ok := v.(map[string]any)
	if !ok {
		return nil, ErrFormat
	}
	info, ok := root["info"].(map[string]any)
	if !ok || d.infoEnd <= d.infoStart {
		return nil, ErrFormat
	}
	raw := b[d.infoStart:d.infoEnd]
	m := &Meta{}
	s1 := sha1.Sum(raw)
	m.InfoHash = hex.EncodeToString(s1[:])
	if mv, _ := info["meta version"].(int64); mv == 2 {
		s2 := sha256.Sum256(raw)
		m.InfoHashV2 = hex.EncodeToString(s2[:])
		if _, hasPieces := info["pieces"]; !hasPieces {
			m.InfoHash = m.InfoHashV2[:40]
		}
	}
	m.Name = str(info["name.utf-8"])
	if m.Name == "" {
		m.Name = str(info["name"])
	}
	m.Name = SafeName(m.Name)

	m.PieceLength, _ = info["piece length"].(int64)
	if p, _ := info["private"].(int64); p == 1 {
		m.Private = true
	}
	m.Comment = str(root["comment.utf-8"])
	if m.Comment == "" {
		m.Comment = str(root["comment"])
	}
	// Trackers: announce-list tiers first, then announce
	seen := map[string]bool{}
	addTr := func(t string) {
		t = strings.TrimSpace(t)
		if t != "" && !seen[t] {
			seen[t] = true
			m.Trackers = append(m.Trackers, t)
		}
	}
	if al, ok := root["announce-list"].([]any); ok {
		for _, tier := range al {
			if l, ok := tier.([]any); ok {
				for _, t := range l {
					addTr(str(t))
				}
			}
		}
	}
	addTr(str(root["announce"]))
	switch ws := root["url-list"].(type) {
	case string:
		m.WebSeeds = []string{ws}
	case []any:
		for _, w := range ws {
			if s := str(w); s != "" {
				m.WebSeeds = append(m.WebSeeds, s)
			}
		}
	}
	if files, ok := info["files"].([]any); ok {
		m.IsFolder = true
		idx := 0
		for _, f := range files {
			fm, ok := f.(map[string]any)
			if !ok {
				continue
			}
			pl, _ := fm["path.utf-8"].([]any)
			if pl == nil {
				pl, _ = fm["path"].([]any)
			}
			parts := []string{}
			for _, p := range pl {
				parts = append(parts, sanitize(str(p)))
			}
			size, _ := fm["length"].(int64)
			pad := strings.Contains(str(fm["attr"]), "p")
			m.Files = append(m.Files, File{Index: idx, Path: path.Join(append([]string{sanitize(m.Name)}, parts...)...), Size: size, Pad: pad})
			if !pad {
				m.Size += size
			}
			idx++
		}
	} else if ft, ok := info["file tree"].(map[string]any); ok && info["length"] == nil {
		m.IsFolder = true
		var walk func(prefix string, node map[string]any)
		idx := 0
		walk = func(prefix string, node map[string]any) {
			keys := make([]string, 0, len(node))
			for k := range node {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				child, _ := node[k].(map[string]any)
				if child == nil {
					continue
				}
				if leaf, ok := child[""].(map[string]any); ok && k != "" {
					size, _ := leaf["length"].(int64)
					root := ""
					if r, ok := leaf["pieces root"].(string); ok {
						root = hex.EncodeToString([]byte(r))
					}
					m.Files = append(m.Files, File{Index: idx, Path: path.Join(prefix, sanitize(k)), Size: size, Root: root})
					m.Size += size
					idx++
					continue
				}
				walk(path.Join(prefix, sanitize(k)), child)
			}
		}
		walk(sanitize(m.Name), ft)
		if len(m.Files) == 1 && m.Files[0].Path == path.Join(sanitize(m.Name), sanitize(m.Name)) {
			m.Files[0].Path = sanitize(m.Name)
			m.IsFolder = false
		}
	} else {
		size, _ := info["length"].(int64)
		m.Files = []File{{Index: 0, Path: sanitize(m.Name), Size: size}}
		m.Size = size
	}
	if m.Name == "" || len(m.Files) == 0 {
		return nil, ErrFormat
	}
	return m, nil
}

// SafeName makes a name usable as a single path component: no separators,
// no control characters, never "." or "..".
func SafeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' {
			return '_'
		}
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if s == "." || s == ".." || strings.Trim(s, ".") == "" && s != "" {
		s = "_"
	}
	return s
}

func sanitize(s string) string { return SafeName(s) }

func str(v any) string {
	s, _ := v.(string)
	return s
}

type decoder struct {
	b                  []byte
	pos                int
	depth              int
	infoStart, infoEnd int
}

func (d *decoder) value() (any, error) {
	if d.pos >= len(d.b) {
		return nil, ErrFormat
	}
	d.depth++
	defer func() { d.depth-- }()
	if d.depth > 64 {
		return nil, ErrFormat
	}
	switch c := d.b[d.pos]; {
	case c == 'i':
		end := bytes.IndexByte(d.b[d.pos:], 'e')
		if end < 0 {
			return nil, ErrFormat
		}
		n, err := strconv.ParseInt(string(d.b[d.pos+1:d.pos+end]), 10, 64)
		if err != nil {
			return nil, ErrFormat
		}
		d.pos += end + 1
		return n, nil
	case c == 'l':
		d.pos++
		var out []any
		for d.pos < len(d.b) && d.b[d.pos] != 'e' {
			v, err := d.value()
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		if d.pos >= len(d.b) {
			return nil, ErrFormat
		}
		d.pos++
		return out, nil
	case c == 'd':
		d.pos++
		out := map[string]any{}
		for d.pos < len(d.b) && d.b[d.pos] != 'e' {
			k, err := d.value()
			if err != nil {
				return nil, err
			}
			ks, ok := k.(string)
			if !ok {
				return nil, ErrFormat
			}
			start := d.pos
			v, err := d.value()
			if err != nil {
				return nil, err
			}
			if ks == "info" && d.depth == 1 {
				d.infoStart, d.infoEnd = start, d.pos
			}
			out[ks] = v
		}
		if d.pos >= len(d.b) {
			return nil, ErrFormat
		}
		d.pos++
		return out, nil
	case c >= '0' && c <= '9':
		colon := bytes.IndexByte(d.b[d.pos:], ':')
		if colon < 0 {
			return nil, ErrFormat
		}
		n, err := strconv.Atoi(string(d.b[d.pos : d.pos+colon]))
		if err != nil || n < 0 || d.pos+colon+1+n > len(d.b) {
			return nil, ErrFormat
		}
		s := string(d.b[d.pos+colon+1 : d.pos+colon+1+n])
		d.pos += colon + 1 + n
		return s, nil
	}
	return nil, ErrFormat
}

// Magnet is a parsed magnet link.
type Magnet struct {
	InfoHash string // hex, lower case (v1, or truncated v2)
	Name     string
	Trackers []string
	WebSeeds []string
}

// ParseMagnet parses a magnet: URI.
func ParseMagnet(s string) (*Magnet, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Scheme != "magnet" {
		return nil, errors.New("magnet: invalid link")
	}
	q := u.Query()
	m := &Magnet{Name: SafeName(q.Get("dn")), Trackers: q["tr"], WebSeeds: q["ws"]}
	for _, xt := range q["xt"] {
		switch {
		case strings.HasPrefix(xt, "urn:btih:"):
			h := strings.TrimPrefix(xt, "urn:btih:")
			switch len(h) {
			case 40:
				if _, err := hex.DecodeString(h); err == nil {
					m.InfoHash = strings.ToLower(h)
				}
			case 32:
				if b, err := base32.StdEncoding.DecodeString(strings.ToUpper(h)); err == nil {
					m.InfoHash = hex.EncodeToString(b)
				}
			}
		case strings.HasPrefix(xt, "urn:btmh:1220") && m.InfoHash == "":
			h := strings.TrimPrefix(xt, "urn:btmh:1220")
			if len(h) == 64 {
				m.InfoHash = strings.ToLower(h[:40])
			}
		}
	}
	if m.InfoHash == "" {
		return nil, errors.New("magnet: missing info hash")
	}
	return m, nil
}

// MagnetURI builds a magnet link for an info hash.
func MagnetURI(infoHash, name string, trackers []string) string {
	v := url.Values{}
	if name != "" {
		v.Set("dn", name)
	}
	for _, t := range trackers {
		v.Add("tr", t)
	}
	q := v.Encode()
	if q != "" {
		q = "&" + q
	}
	return fmt.Sprintf("magnet:?xt=urn:btih:%s%s", infoHash, q)
}
