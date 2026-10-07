package hosters

import (
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"downloadcenter/internal/core"
)

// --- Google Drive (public "Anyone with the link" files, no account) ---

// Errors shown to users.
var (
	ErrDrivePrivate = errors.New("This Google Drive file is not shared publicly. Ask its owner to share it with “Anyone with the link”")
	ErrDriveQuota   = errors.New("Too many people have downloaded this Google Drive file recently. Try again later")
	ErrDrivePage    = errors.New("Google Drive did not hand out the file (it sent a web page instead)")
	ErrDriveFolder  = errors.New("Google Drive folder links are not supported yet. Open the folder and add the links of its files")
)

// driveLink is a Google Drive share link: a file or folder id and the
// resource key older links need.
type driveLink struct {
	id, key string
	folder  bool
}

var driveIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{10,}$`)

// driveLinkOf recognises the Drive file and folder links people share.
func driveLinkOf(raw string) (driveLink, bool) {
	var l driveLink
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return l, false
	}
	q := u.Query()
	segs := withoutUser(strings.Split(strings.Trim(u.Path, "/"), "/"))
	one := func(s ...string) bool {
		if len(segs) != 1 {
			return false
		}
		for _, x := range s {
			if segs[0] == x {
				return true
			}
		}
		return false
	}
	switch strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.") {
	case "drive.google.com":
		switch {
		case len(segs) >= 3 && segs[0] == "file" && segs[1] == "d":
			l.id = segs[2]
		case one("open", "uc"):
			l.id = q.Get("id")
		case len(segs) >= 3 && segs[0] == "drive" && segs[1] == "folders":
			l.id, l.folder = segs[2], true
		case one("folderview"):
			l.id, l.folder = q.Get("id"), true
		}
	case "docs.google.com":
		if one("uc") {
			l.id = q.Get("id")
		}
	case "drive.usercontent.google.com":
		if one("download", "uc") {
			l.id = q.Get("id")
		}
	}
	l.key = q.Get("resourcekey")
	return l, driveIDRe.MatchString(l.id)
}

// withoutUser drops the "u/<n>" account selector from a Drive path.
func withoutUser(segs []string) []string {
	var out []string
	for i := 0; i < len(segs); i++ {
		if segs[i] == "u" && i+1 < len(segs) {
			if _, err := strconv.Atoi(segs[i+1]); err == nil {
				i++
				continue
			}
		}
		out = append(out, segs[i])
	}
	return out
}

// ResolvePublic resolves a link without any account, for tasks added with
// none: public Drive files. Links of services that need an account give
// (nil, nil), a plain download.
func (s *Service) ResolvePublic(rawURL, proxy string) (*core.Resolved, error) {
	l, ok := driveLinkOf(rawURL)
	if !ok {
		return nil, nil
	}
	c := *s
	c.client = func() *http.Client { return s.proxyClient(proxy) }
	return c.driveResolve(l, nil, "")
}

// driveDownload is where Drive hands out files.
const driveDownload = "https://drive.usercontent.google.com/download"

// driveCookies picks the owner's cookies account for a Drive link, as for
// any other site: the one chosen when adding when it fits, else the first
// that covers the link's site. The Cookie header is the one for Drive's
// download address; an account with no cookie for it is passed over.
func (s *Service) driveCookies(owner, accountID, rawURL string) (*core.Account, string) {
	host := hostOf(rawURL)
	fits := func(a *core.Account) string {
		if a.Kind != Cookies || !a.Enabled || a.Owner != owner || !s.jarCovers(a, host) {
			return ""
		}
		return cookieHeader(s.jar(a), driveDownload)
	}
	mine := s.b.Accounts(owner)
	if accountID != "" {
		for _, a := range mine {
			if a.ID == accountID {
				if h := fits(a); h != "" {
					return a, h
				}
			}
		}
	}
	for _, a := range mine {
		if h := fits(a); h != "" {
			return a, h
		}
	}
	return nil, ""
}

// Redirects the probe does not follow.
var (
	errDriveSignIn  = errors.New("sign-in required")
	errDriveForeign = errors.New("redirect away from Google")
)

// driveHost reports Google's own hosts (and the base host, for tests).
func (s *Service) driveHost(host string) bool {
	host = strings.ToLower(host)
	if b, err := url.Parse(s.base[GDrive]); err == nil && host == strings.ToLower(b.Host) {
		return true
	}
	return host == "google.com" || strings.HasSuffix(host, ".google.com")
}

// driveLocation is the download address of a Drive file.
func (s *Service) driveLocation(l driveLink) string {
	q := url.Values{"id": {l.id}, "export": {"download"}, "confirm": {"t"}}
	if l.key != "" {
		q.Set("resourcekey", l.key)
	}
	return s.base[GDrive] + "?" + q.Encode()
}

// driveResolve asks drive.usercontent.google.com for a file, with the
// cookies of acct when given. Only that address is ever requested: the id
// comes from the link, the host never does.
func (s *Service) driveResolve(l driveLink, acct *core.Account, cookie string) (*core.Resolved, error) {
	if l.folder {
		return nil, ErrDriveFolder
	}
	res, next, err := s.driveProbe(s.driveLocation(l), cookie)
	if next != "" {
		// Still the virus-scan warning: send its form as it is, once
		res, next, err = s.driveProbe(next, cookie)
		if next != "" {
			return nil, ErrDrivePage
		}
	}
	if res != nil && acct != nil {
		res.Headers, res.Account = []string{"Cookie: " + cookie}, acct.ID
	}
	return res, err
}

// driveProbe asks for the first byte. It returns the file, or the address
// the virus-scan warning's form leads to, or why there is no file.
func (s *Service) driveProbe(u, cookie string) (*core.Resolved, string, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, "", ErrDrivePage
	}
	req.Header.Set("User-Agent", "DownloadCenter/1.0")
	req.Header.Set("Range", "bytes=0-0")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	// Google's pages in English, for telling them apart
	req.Header.Set("Accept-Language", "en")
	c := *s.client()
	c.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		switch {
		case len(via) >= 5:
			return errDriveForeign
		case strings.EqualFold(r.URL.Hostname(), "accounts.google.com"):
			return errDriveSignIn
		case !s.driveHost(r.URL.Host):
			return errDriveForeign
		}
		return nil
	}
	resp, err := c.Do(req)
	switch {
	case errors.Is(err, errDriveSignIn):
		return nil, "", ErrDrivePrivate
	case errors.Is(err, errDriveForeign):
		return nil, "", ErrDrivePage
	case err != nil:
		return nil, "", fmt.Errorf("Cannot connect to the file-hosting site: %v", err)
	}
	defer resp.Body.Close()
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	page := ct == "text/html" || ct == "application/xhtml+xml"
	var body string
	if page {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
		body = string(b)
		low := strings.ToLower(body)
		if strings.Contains(low, "quota exceeded") || strings.Contains(low, "too many users have viewed or downloaded this file") {
			return nil, "", ErrDriveQuota
		}
	}
	switch st := resp.StatusCode; {
	case st == http.StatusUnauthorized || st == http.StatusForbidden:
		return nil, "", ErrDrivePrivate
	case st == http.StatusNotFound || st == http.StatusGone:
		return nil, "", ErrFileMissing
	case st == http.StatusTooManyRequests:
		return nil, "", ErrDriveQuota
	case st >= 500:
		return nil, "", ErrTryLater
	case st != http.StatusOK && st != http.StatusPartialContent:
		return nil, "", ErrDrivePage
	case page:
		if next := s.driveForm(body); next != "" {
			return nil, next, nil
		}
		return nil, "", ErrDrivePage
	}
	res := &core.Resolved{URL: u, NoPages: true}
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil {
		res.Name = params["filename"]
	}
	if resp.StatusCode == http.StatusPartialContent {
		if _, tot, ok := strings.Cut(resp.Header.Get("Content-Range"), "/"); ok {
			res.Size, _ = strconv.ParseInt(strings.TrimSpace(tot), 10, 64)
		}
	} else if resp.ContentLength > 0 {
		res.Size = resp.ContentLength
	}
	return res, "", nil
}

var (
	driveFormRe   = regexp.MustCompile(`(?is)<form[^>]*\bid="download-form"[^>]*>(.*?)</form>`)
	driveActionRe = regexp.MustCompile(`(?is)<form[^>]*\baction="([^"]*)"`)
	driveInputRe  = regexp.MustCompile(`(?is)<input[^>]*>`)
	driveAttrRe   = regexp.MustCompile(`(?is)\b(type|name|value)="([^"]*)"`)
)

// driveForm returns the address the virus-scan warning's download form
// submits to, when the page has one that stays on Google.
func (s *Service) driveForm(page string) string {
	m := driveFormRe.FindStringSubmatch(page)
	if m == nil {
		return ""
	}
	a := driveActionRe.FindStringSubmatch(m[0])
	if a == nil {
		return ""
	}
	act, err := url.Parse(html.UnescapeString(a[1]))
	if err != nil || (act.Scheme != "https" && act.Scheme != "http") || !s.driveHost(act.Host) ||
		strings.EqualFold(act.Hostname(), "accounts.google.com") {
		return ""
	}
	q := url.Values{}
	for _, in := range driveInputRe.FindAllString(m[1], -1) {
		attrs := map[string]string{}
		for _, kv := range driveAttrRe.FindAllStringSubmatch(in, -1) {
			attrs[strings.ToLower(kv[1])] = html.UnescapeString(kv[2])
		}
		if strings.EqualFold(attrs["type"], "hidden") && attrs["name"] != "" {
			q.Set(attrs["name"], attrs["value"])
		}
	}
	if q.Get("id") == "" {
		return ""
	}
	act.RawQuery = q.Encode()
	return act.String()
}
