package api

import (
	"html"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/netutil"
)

var (
	linkRe   = regexp.MustCompile(`(?i)\b(?:https?|s?ftps?|scp|thunder|flashget|qqdl)://[^\s<>"'` + "`" + `\x{3000}-\x{303f}\x{ff00}-\x{ffef}]+|magnet:\?[^\s<>"']+`)
	hrefRe   = regexp.MustCompile(`(?i)\b(?:href|src|data-url|data-href)\s*=\s*["']([^"']+)["']`)
	trimTail = ".,;:!?)]}>'\"、。，"
)

// fileExts are extensions treated as downloads when extracting from pages.
var fileExts = map[string]bool{
	"iso": true, "img": true, "zip": true, "rar": true, "7z": true, "gz": true, "tgz": true, "xz": true, "bz2": true, "zst": true, "tar": true,
	"exe": true, "msi": true, "dmg": true, "pkg": true, "deb": true, "rpm": true, "apk": true, "appimage": true,
	"mp4": true, "mkv": true, "avi": true, "mov": true, "webm": true, "m4v": true, "ts": true, "flv": true, "wmv": true,
	"mp3": true, "flac": true, "m4a": true, "aac": true, "ogg": true, "wav": true, "opus": true,
	"pdf": true, "epub": true, "mobi": true, "cbz": true, "cbr": true, "torrent": true, "bin": true, "qpkg": true,
	"jpg": true, "jpeg": true, "png": true, "gif": true, "webp": true, "srt": true, "ass": true, "doc": true, "docx": true, "xls": true, "xlsx": true, "ppt": true, "pptx": true,
}

type link struct {
	URL    string `json:"url"`
	Kind   string `json:"kind"` // url | magnet | torrent | page
	Name   string `json:"name"`
	Ext    string `json:"ext"`
	Hoster string `json:"hoster,omitempty"`
}

func classify(raw string, hosterMatch func(string) string) link {
	raw = core.UnwrapLink(raw)
	l := link{URL: raw}
	if strings.HasPrefix(strings.ToLower(raw), "magnet:") {
		l.Kind = "magnet"
		if u, err := url.Parse(raw); err == nil {
			l.Name = u.Query().Get("dn")
		}
		return l
	}
	u, err := url.Parse(raw)
	if err != nil {
		l.Kind = "url"
		return l
	}
	base := path.Base(u.Path)
	if un, err := url.PathUnescape(base); err == nil {
		base = un
	}
	l.Name = base
	if base == "/" || base == "." || base == "" {
		l.Name = u.Host
	}
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(base), "."))
	if strings.HasSuffix(strings.ToLower(base), ".tar.gz") {
		ext = "tar.gz"
	}
	l.Ext = ext
	switch {
	case ext == "torrent":
		l.Kind = "torrent"
	case strings.HasSuffix(u.Path, "/") || u.Path == "" || ext == "html" || ext == "htm" || ext == "php" || ext == "asp" || ext == "aspx":
		l.Kind = "page"
	default:
		l.Kind = "url"
	}
	if hosterMatch != nil {
		if h := hosterMatch(raw); h != "" {
			l.Hoster, l.Kind = h, "url"
		}
	}
	return l
}

func cleanLink(s string) string {
	s = strings.TrimRight(html.UnescapeString(strings.TrimSpace(s)), trimTail)
	return s
}

func (s *Server) hosterMatch(raw string) string {
	if s.M.Hosters == nil {
		return ""
	}
	if svc, ok := s.M.Hosters.Match(raw); ok {
		return svc
	}
	return ""
}

// extract finds download links in pasted text, or in a web page fetched by
// the server.
func (s *Server) extract(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	var b struct {
		Text string `json:"text"`
		URL  string `json:"url"`
	}
	if err := Decode(r, &b); err != nil {
		Error(w, 400, "bad_request", err.Error())
		return
	}
	seen := map[string]bool{}
	var out []link
	add := func(raw string, onlyFiles bool) {
		raw = cleanLink(raw)
		if raw == "" || seen[raw] || len(out) >= 2000 {
			return
		}
		l := classify(raw, s.hosterMatch)
		if onlyFiles && l.Kind == "page" {
			return
		}
		if onlyFiles && l.Kind == "url" && l.Hoster == "" && !fileExts[l.Ext] && l.Ext != "tar.gz" {
			return
		}
		seen[raw] = true
		out = append(out, l)
	}
	if b.URL == "" {
		for _, m := range linkRe.FindAllString(b.Text, -1) {
			add(m, false)
		}
		if out == nil {
			out = []link{}
		}
		OK(w, map[string]any{"links": out})
		return
	}
	pu, err := url.Parse(strings.TrimSpace(b.URL))
	if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") {
		Error(w, 400, "url_not_supported", "Only http/https web pages can be read")
		return
	}
	// The page is read through the proxy a download of it would use
	proxy := ""
	if pr, err := s.M.ProxyFor("", pu.String(), p.Admin); err != nil {
		Fail(w, err)
		return
	} else if pr != nil {
		proxy = s.M.ProxyURL(pr.ID)
	}
	cl := netutil.Client(20*time.Second, !p.Admin, proxy)
	req, _ := http.NewRequest("GET", pu.String(), nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; DownloadCenter/1.0)")
	resp, err := cl.Do(req)
	if err != nil {
		Error(w, 502, "fetch_failed", "Cannot read this web page: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		Error(w, 502, "fetch_failed", "The web page responded with "+resp.Status)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	baseURL := resp.Request.URL
	for _, m := range hrefRe.FindAllStringSubmatch(string(body), -1) {
		ref := core.UnwrapLink(html.UnescapeString(m[1]))
		if strings.HasPrefix(strings.ToLower(ref), "magnet:") {
			add(ref, true)
			continue
		}
		u, err := baseURL.Parse(ref)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "ftp") {
			continue
		}
		u.Fragment = ""
		add(u.String(), true)
	}
	for _, m := range linkRe.FindAllString(string(body), -1) {
		add(m, true)
	}
	if out == nil {
		out = []link{}
	}
	OK(w, map[string]any{"links": out, "page": baseURL.String()})
}

func itoa(n int) string { return strconv.Itoa(n) }
