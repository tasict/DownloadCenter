// Package hosters resolves file-hosting share links into direct URLs with
// the owner's account (1fichier, Rapidgator, Real-Debrid, AllDebrid,
// cookies.txt) or without one (public Google Drive files), and verifies
// accounts.
package hosters

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"downloadcenter/internal/core"
	"downloadcenter/internal/netutil"
)

// backend is the part of core.Manager the resolvers use (a fake in tests).
type backend interface {
	Accounts(owner string) []*core.Account
	Account(id string) (*core.Account, error)
	AccountSecret(id string) string
	SetAccountInfo(id string, info map[string]any)
	Emit(e core.Event) core.Event
	Settings() core.Settings
	DefaultURLProxy() string
}

// Service implements core.Resolver and api.AccountVerifier.
type Service struct {
	b backend

	// API base URLs (overridden in tests).
	base map[string]string
	// client builds the HTTP client of a call (overridden in tests); proxyClient
	// the one for a given proxy URL ("" = direct).
	client      func() *http.Client
	proxyClient func(proxy string) *http.Client

	*shared
}

// shared is the state every copy of a Service uses (Resolve works on a
// copy with its own HTTP client).
type shared struct {
	mu       sync.Mutex
	hosts    map[string]hostList  // debrid service -> supported domains
	cookies  map[string]cookieJar // account id -> parsed cookies.txt
	warned   map[string]string    // account id -> day of the last account.expiring event
	rgTokens map[string]rgToken   // account id -> Rapidgator session token
}

type hostList struct {
	domains map[string]bool
	at      time.Time
}

type cookieJar struct {
	sum     string
	cookies []cookie
}

// Services handled here (the core handles "site").
const (
	OneFichier = "1fichier"
	Rapidgator = "rapidgator"
	RealDebrid = "realdebrid"
	AllDebrid  = "alldebrid"
	Cookies    = "cookies"
	Mega       = "mega"
	GDrive     = "gdrive"
)

var defaultBase = map[string]string{
	OneFichier: "https://api.1fichier.com/v1",
	Rapidgator: "https://rapidgator.net/api/v2",
	RealDebrid: "https://api.real-debrid.com/rest/1.0",
	AllDebrid:  "https://api.alldebrid.com/v4",
	GDrive:     driveDownload,
}

// Known domains of the direct hosters.
var hosterDomains = map[string][]string{
	OneFichier: {"1fichier.com", "alterupload.com", "cjoint.net", "desfichiers.com", "dfichiers.com", "megadl.fr",
		"mesfichiers.org", "piecejointe.net", "pjointe.com", "tenvoi.com", "dl4free.com"},
	Rapidgator: {"rapidgator.net", "rg.to", "rapidgator.asia"},
	Mega:       {"mega.nz", "mega.co.nz", "mega.io"},
}

func New(m *core.Manager) *Service { return newWith(m) }

func newWith(b backend) *Service {
	s := &Service{
		b:    b,
		base: map[string]string{},
		shared: &shared{hosts: map[string]hostList{}, cookies: map[string]cookieJar{},
			warned: map[string]string{}, rgTokens: map[string]rgToken{}},
	}
	for k, v := range defaultBase {
		s.base[k] = v
	}
	s.client = s.defaultClient
	s.proxyClient = func(proxy string) *http.Client { return netutil.Client(20*time.Second, false, proxy) }
	return s
}

// defaultClient uses the default proxy of URL downloads (account checks);
// resolving a task's link uses the task's proxy instead (see Resolve).
func (s *Service) defaultClient() *http.Client {
	return netutil.Client(20*time.Second, false, s.b.DefaultURLProxy())
}

func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

// domainIn reports whether host is d or a subdomain of a domain in set.
func domainIn(host string, set map[string]bool) bool {
	for h := host; h != ""; {
		if set[h] {
			return true
		}
		i := strings.IndexByte(h, '.')
		if i < 0 {
			break
		}
		h = h[i+1:]
	}
	return false
}

func knownHoster(host string) string {
	for svc, ds := range hosterDomains {
		for _, d := range ds {
			if host == d || strings.HasSuffix(host, "."+d) {
				return svc
			}
		}
	}
	return ""
}

// Match reports the service of a supported share link. Known hoster
// domains always match; other domains match only when some user has a
// debrid or cookies account that covers them (Resolve then falls back to
// a plain download for owners without such an account).
func (s *Service) Match(rawURL string) (string, bool) {
	host := hostOf(rawURL)
	if host == "" {
		return "", false
	}
	if _, ok := driveLinkOf(rawURL); ok {
		return GDrive, true
	}
	if svc := knownHoster(host); svc != "" {
		return svc, true
	}
	all := s.b.Accounts("")
	for _, a := range all {
		if a.Enabled && a.Kind == Cookies && s.jarCovers(a, host) {
			return Cookies, true
		}
	}
	for _, kind := range []string{RealDebrid, AllDebrid} {
		for _, a := range all {
			if a.Enabled && a.Kind == kind && s.debridCovers(kind, a, host) {
				return kind, true
			}
		}
	}
	return "", false
}

// Errors shown to users.
var (
	ErrMega        = errors.New("MEGA is not supported yet (it uses its own end-to-end encryption)")
	ErrNoAccount   = errors.New("Free downloads that require a captcha or a countdown are not supported. Add a file-hosting account for this site under Settings › Site accounts")
	ErrExpired     = errors.New("File-hosting account expired. Update it under Settings › Site accounts")
	ErrTraffic     = errors.New("The file-hosting account has used up today's traffic")
	ErrFileMissing = errors.New("The file does not exist or has been deleted")
	ErrBadLogin    = errors.New("File-hosting account verification failed. Check the user name and password or API key")
	ErrHostNotSup  = errors.New("This file-hosting account does not support this site")
	ErrTryLater    = errors.New("The file-hosting site cannot provide a download address right now. Try again later")
)

// accountError marks failures caused by the account (expired, traffic),
// which are recorded on the account and announced.
type accountError struct {
	err   error
	state string // expired | traffic
}

func (e *accountError) Error() string { return e.err.Error() }
func (e *accountError) Unwrap() error { return e.err }

// Resolve returns the direct download for a share link with one of the
// owner's accounts; another user's account is never used. The service is
// asked through proxy ("" = direct), the same one the download uses: many
// services tie the direct link to the address that asked for it.
func (s *Service) Resolve(owner, accountID, rawURL, proxy string) (*core.Resolved, error) {
	c := *s
	c.client = func() *http.Client { return s.proxyClient(proxy) }
	return c.resolve(owner, accountID, rawURL)
}

func (s *Service) resolve(owner, accountID, rawURL string) (*core.Resolved, error) {
	host := hostOf(rawURL)
	if host == "" {
		return nil, core.ErrBadURL
	}
	// Drive files: public ones need no account; a cookies account for the
	// site is used like for any other site
	if l, ok := driveLinkOf(rawURL); ok {
		if l.folder {
			return nil, ErrDriveFolder
		}
		a, cookie := s.driveCookies(owner, accountID, rawURL)
		return s.driveResolve(l, a, cookie)
	}
	svc := knownHoster(host)
	if svc == Mega {
		return nil, ErrMega
	}
	mine := s.b.Accounts(owner)
	var cands []*core.Account
	// The account picked in the dialog, if it is the owner's and fits
	if accountID != "" {
		for _, a := range mine {
			if a.ID == accountID && a.Enabled && a.Owner == owner && s.covers(a, svc, host) {
				cands = append(cands, a)
			}
		}
	}
	if len(cands) == 0 {
		// Direct account of the hoster, then debrid accounts, then cookies
		order := []string{svc, RealDebrid, AllDebrid, Cookies}
		for _, kind := range order {
			if kind == "" {
				continue
			}
			for _, a := range mine {
				if a.Kind == kind && a.Enabled && a.Owner == owner && s.covers(a, svc, host) {
					cands = append(cands, a)
				}
			}
		}
	}
	if len(cands) == 0 {
		if svc != "" {
			return nil, ErrNoAccount
		}
		// A domain matched only because of someone else's account: plain URL
		return &core.Resolved{URL: rawURL}, nil
	}
	var firstErr error
	for _, a := range cands {
		res, err := s.resolveWith(a, rawURL)
		if err == nil {
			res.Account = a.ID
			return res, nil
		}
		var ae *accountError
		if errors.As(err, &ae) {
			s.markAccount(a, ae)
		}
		if firstErr == nil {
			firstErr = err
		}
		// A missing file will not appear with another account
		if errors.Is(err, ErrFileMissing) {
			break
		}
	}
	return nil, firstErr
}

func (s *Service) covers(a *core.Account, svc, host string) bool {
	switch a.Kind {
	case OneFichier, Rapidgator:
		return a.Kind == svc
	case RealDebrid, AllDebrid:
		return s.debridCovers(a.Kind, a, host)
	case Cookies:
		return s.jarCovers(a, host)
	}
	return false
}

func (s *Service) resolveWith(a *core.Account, rawURL string) (*core.Resolved, error) {
	secret := s.b.AccountSecret(a.ID)
	if secret == "" {
		return nil, ErrBadLogin
	}
	switch a.Kind {
	case OneFichier:
		return s.fichierResolve(secret, rawURL)
	case Rapidgator:
		return s.rgResolve(a, secret, rawURL)
	case RealDebrid:
		return s.rdResolve(secret, rawURL)
	case AllDebrid:
		return s.adResolve(secret, rawURL)
	case Cookies:
		return s.cookieResolve(a, rawURL)
	}
	return nil, core.ErrUnsupported
}

// Verify checks an account and returns plan, expiry and traffic.
func (s *Service) Verify(a *core.Account, secret string) (map[string]any, error) {
	if secret == "" {
		secret = s.b.AccountSecret(a.ID)
	}
	var info map[string]any
	var err error
	switch a.Kind {
	case OneFichier:
		info, err = s.fichierVerify(secret)
	case Rapidgator:
		info, err = s.rgVerify(a, secret)
	case RealDebrid:
		info, err = s.rdVerify(secret)
	case AllDebrid:
		info, err = s.adVerify(secret)
	case Cookies:
		info, err = s.cookieVerify(a, secret)
	default:
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	info["verified_at"] = time.Now().Unix()
	s.maybeWarn(a, info)
	return info, nil
}

// markAccount records an expired / out-of-traffic account and announces it.
func (s *Service) markAccount(a *core.Account, ae *accountError) {
	info := map[string]any{}
	for k, v := range a.Info {
		info[k] = v
	}
	info["error"] = ae.err.Error()
	info["verified_at"] = time.Now().Unix()
	if ae.state == "traffic" {
		info["traffic_left"] = 0
	}
	if ae.state == "expired" {
		info["premium"] = false
	}
	s.b.SetAccountInfo(a.ID, info)
	s.warnOnce(a, ae.state)
}

// maybeWarn emits account.expiring when an account expires within 7 days or
// has no traffic left.
func (s *Service) maybeWarn(a *core.Account, info map[string]any) {
	if exp, ok := info["expires_at"].(int64); ok && exp > 0 && time.Until(time.Unix(exp, 0)) < 7*24*time.Hour {
		s.warnOnce(a, "expiring")
		return
	}
	if tl, ok := info["traffic_left"].(int64); ok && tl == 0 {
		s.warnOnce(a, "traffic")
	}
}

func (s *Service) warnOnce(a *core.Account, reason string) {
	day := time.Now().Format("2006-01-02")
	s.mu.Lock()
	if s.warned[a.ID] == day {
		s.mu.Unlock()
		return
	}
	s.warned[a.ID] = day
	s.mu.Unlock()
	s.b.Emit(core.Event{Type: "account.expiring", Owner: a.Owner, Data: map[string]any{
		"account": a.ID, "service": a.Kind, "username": a.Username, "reason": reason,
	}})
}

// Services lists the supported services for the UI.
func (s *Service) Services() []map[string]any {
	return []map[string]any{
		{"id": OneFichier, "title": "1fichier", "secret_label": "API key", "needs_username": false,
			"hosts": hosterDomains[OneFichier][:3], "help": "Generate a key under Account settings › API in 1fichier (requires a Premium or Access plan)."},
		{"id": Rapidgator, "title": "Rapidgator", "secret_label": "Password", "needs_username": true,
			"hosts": hosterDomains[Rapidgator], "help": "Signs in to the official API with your Rapidgator account (email) and password; requires a Premium account."},
		{"id": RealDebrid, "title": "Real-Debrid", "secret_label": "API token", "needs_username": false,
			"hosts": []string{"real-debrid.com"}, "help": "Copy the API token from real-debrid.com/apitoken. One account can download from many file-hosting sites."},
		{"id": AllDebrid, "title": "AllDebrid", "secret_label": "API key", "needs_username": false,
			"hosts": []string{"alldebrid.com"}, "help": "Create an API key at alldebrid.com/apikeys. One account can download from many file-hosting sites."},
		{"id": Cookies, "title": "Other sites (cookies)", "secret_label": "cookies.txt content", "needs_username": false,
			"hosts": []string{}, "help": "Export cookies.txt (Netscape format) after signing in, using a browser extension, and paste it here; works for other sites that require sign-in."},
		// Public links only: there is no account to save
		{"id": GDrive, "title": "Google Drive", "no_account": true,
			"hosts": []string{"drive.google.com", "drive.usercontent.google.com"}},
	}
}

func sum(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:8])
}
