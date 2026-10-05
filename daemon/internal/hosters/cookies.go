package hosters

import (
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"downloadcenter/internal/core"
)

// cookie is one line of a Netscape cookies.txt.
type cookie struct {
	Domain     string
	Subdomains bool
	Path       string
	Secure     bool
	Expires    int64 // 0 = session cookie
	Name       string
	Value      string
}

// parseCookies reads the Netscape cookies.txt format (as exported by
// browser extensions and curl); "#HttpOnly_" prefixes are honoured.
func parseCookies(text string) []cookie {
	var out []cookie
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "#HttpOnly_") {
			line = strings.TrimPrefix(line, "#HttpOnly_")
		} else if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 7 {
			// Some exporters use runs of spaces
			f = strings.Fields(line)
			if len(f) < 7 {
				continue
			}
			f = append(f[:6], strings.Join(f[6:], " "))
		}
		exp, _ := strconv.ParseInt(strings.TrimSpace(f[4]), 10, 64)
		d := strings.ToLower(strings.TrimSpace(f[0]))
		c := cookie{
			Domain:     strings.TrimPrefix(d, "."),
			Subdomains: strings.EqualFold(f[1], "TRUE") || strings.HasPrefix(d, "."),
			Path:       f[2],
			Secure:     strings.EqualFold(f[3], "TRUE"),
			Expires:    exp,
			Name:       f[5],
			Value:      strings.TrimSpace(f[6]),
		}
		if c.Domain == "" || c.Name == "" {
			continue
		}
		if c.Path == "" {
			c.Path = "/"
		}
		out = append(out, c)
	}
	return out
}

func (c cookie) matchesHost(host string) bool {
	host = strings.ToLower(host)
	if host == c.Domain || host == "www."+c.Domain {
		return true
	}
	return c.Subdomains && strings.HasSuffix(host, "."+c.Domain)
}

func (c cookie) expired(now int64) bool { return c.Expires > 0 && c.Expires < now }

// cookieHeader builds the Cookie header value for a URL.
func cookieHeader(cs []cookie, raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	now := time.Now().Unix()
	var sel []cookie
	for _, c := range cs {
		if c.expired(now) || !c.matchesHost(host) {
			continue
		}
		if c.Secure && u.Scheme != "https" {
			continue
		}
		if !strings.HasPrefix(p, c.Path) {
			continue
		}
		sel = append(sel, c)
	}
	// More specific paths first, like browsers
	sort.SliceStable(sel, func(i, j int) bool { return len(sel[i].Path) > len(sel[j].Path) })
	var parts []string
	seen := map[string]bool{}
	for _, c := range sel {
		if seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

func (s *Service) jar(a *core.Account) []cookie {
	secret := s.b.AccountSecret(a.ID)
	if secret == "" {
		return nil
	}
	h := sum(secret)
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.cookies[a.ID]; ok && j.sum == h {
		return j.cookies
	}
	cs := parseCookies(secret)
	s.cookies[a.ID] = cookieJar{sum: h, cookies: cs}
	return cs
}

// jarCovers reports whether the account has a live cookie for host. When the
// account names a host, only that site (and its subdomains) is covered.
func (s *Service) jarCovers(a *core.Account, host string) bool {
	if a.Host != "" {
		h := strings.TrimPrefix(strings.ToLower(a.Host), "www.")
		if u, err := url.Parse(h); err == nil && u.Host != "" {
			h = strings.TrimPrefix(u.Hostname(), "www.")
		}
		if host != h && !strings.HasSuffix(host, "."+h) {
			return false
		}
	}
	now := time.Now().Unix()
	for _, c := range s.jar(a) {
		if !c.expired(now) && c.matchesHost(host) {
			return true
		}
	}
	return false
}

func (s *Service) cookieResolve(a *core.Account, raw string) (*core.Resolved, error) {
	h := cookieHeader(s.jar(a), raw)
	if h == "" {
		return nil, errors.New("cookies.txt has no valid cookies for this site. Export it again")
	}
	return &core.Resolved{URL: raw, Name: nameFromURL(raw), Headers: []string{"Cookie: " + h}}, nil
}

func (s *Service) cookieVerify(a *core.Account, secret string) (map[string]any, error) {
	cs := parseCookies(secret)
	if len(cs) == 0 {
		return nil, errors.New("Cannot understand this cookies.txt. Export it in Netscape format")
	}
	now := time.Now().Unix()
	domains := map[string]bool{}
	live := 0
	var exp int64
	for _, c := range cs {
		if c.expired(now) {
			continue
		}
		live++
		domains[c.Domain] = true
		// The jar is usable until its last cookie expires
		if c.Expires > exp {
			exp = c.Expires
		}
	}
	var dl []string
	for d := range domains {
		dl = append(dl, d)
	}
	sort.Strings(dl)
	info := map[string]any{"plan": "Cookie", "premium": live > 0, "expires_at": exp, "traffic_left": int64(-1),
		"cookies": live, "domains": dl}
	if live == 0 {
		info["error"] = "All cookies have expired. Export them again"
	}
	return info, nil
}
