// Package hosters resolves file-hosting share links into direct URLs with
// the owner's account (1fichier, Rapidgator, Real-Debrid, AllDebrid,
// cookies.txt) and verifies accounts.
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
)

var defaultBase = map[string]string{
	OneFichier: "https://api.1fichier.com/v1",
	Rapidgator: "https://rapidgator.net/api/v2",
	RealDebrid: "https://api.real-debrid.com/rest/1.0",
	AllDebrid:  "https://api.alldebrid.com/v4",
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
	ErrMega        = errors.New("MEGA 目前不支援（使用自有的端對端加密）")
	ErrNoAccount   = errors.New("需要驗證碼或等待倒數的免費下載不支援，請到 設定 › 網站帳號 新增這個網站的免空帳號")
	ErrExpired     = errors.New("免空帳號已過期，請到 設定 › 網站帳號 更新")
	ErrTraffic     = errors.New("免空帳號今天的流量已用完")
	ErrFileMissing = errors.New("檔案不存在或已被刪除")
	ErrBadLogin    = errors.New("免空帳號驗證失敗，請確認帳號密碼或 API 金鑰")
	ErrHostNotSup  = errors.New("這個免空帳號不支援這個網站")
	ErrTryLater    = errors.New("免空網站暫時無法取得下載位址，請稍後再試")
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
		{"id": OneFichier, "title": "1fichier", "secret_label": "API 金鑰", "needs_username": false,
			"hosts": hosterDomains[OneFichier][:3], "help": "在 1fichier 的帳號設定 › API 產生金鑰（需要 Premium 或 Access 方案）。"},
		{"id": Rapidgator, "title": "Rapidgator", "secret_label": "密碼", "needs_username": true,
			"hosts": hosterDomains[Rapidgator], "help": "用 Rapidgator 的帳號（電子郵件）與密碼登入官方 API；需要 Premium 帳號。"},
		{"id": RealDebrid, "title": "Real-Debrid", "secret_label": "API 權杖", "needs_username": false,
			"hosts": []string{"real-debrid.com"}, "help": "到 real-debrid.com/apitoken 複製 API 權杖。一個帳號可以下載多個免空網站。"},
		{"id": AllDebrid, "title": "AllDebrid", "secret_label": "API 金鑰", "needs_username": false,
			"hosts": []string{"alldebrid.com"}, "help": "到 alldebrid.com/apikeys 建立 API 金鑰。一個帳號可以下載多個免空網站。"},
		{"id": Cookies, "title": "其他網站（Cookie）", "secret_label": "cookies.txt 內容", "needs_username": false,
			"hosts": []string{}, "help": "用瀏覽器擴充功能匯出登入後的 cookies.txt（Netscape 格式）並貼上；適用其他需要登入的網站。"},
	}
}

func sum(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:8])
}
