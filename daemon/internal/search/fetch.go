package search

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"downloadcenter/internal/netutil"
)

// Limits of the requests made to search sources.
const (
	fetchTimeout  = 15 * time.Second
	maxRedirects  = 3
	limitSearch   = 2 << 20  // bytes of a search answer
	limitPlugin   = 64 << 10 // bytes of a plugin file
	limitTorrent  = 16 << 20 // bytes of a .torrent
	userAgentLink = "https://github.com/tasict/DownloadCenter"
)

// Failure reasons: the only texts a failed request is reported with, to
// callers and in the log. Nothing from the request (address, keyword,
// settings) or from the answer is in them, except a Torznab server's own
// error text, cleaned (see torznabReason).
const (
	ReasonTimeout     = "Timed out"
	ReasonConnect     = "Could not connect"
	ReasonRefused     = "The server refused the API key"
	ReasonUnreadable  = "Could not read the answer"
	ReasonNotAllowed  = "This address is not allowed: a search source cannot be on this NAS or a local address"
	ReasonBusy        = "The source is busy, try again later"
	ReasonTooLarge    = "The answer is too large"
	ReasonHTTP        = "The source answered with HTTP {0}"
	ReasonTorznab     = "The source reported an error: {0}"
	ReasonNeedsSetup  = "Fill in the settings of this source first"
	ReasonNoProxy     = "A proxy is required for this address, and none can be used"
	ReasonNotTorrent  = "This source did not give a valid .torrent file"
	ReasonPluginHTTPS = "The plugin address must be an https address" // a validation error
)

// Failure is a request that failed: Reason is one of the Reason texts with
// its argument put in; Detail is what the log gets besides (our own words
// and numbers only: no address, keyword, secret or text of the source).
type Failure struct {
	Reason string
	Detail string
	// template is the Reason text before a source's own words were put in:
	// what the log gets instead of Reason
	template string
}

func (f *Failure) Error() string { return f.Reason }

func fail(reason, detail string) *Failure { return &Failure{Reason: reason, Detail: detail} }

// errRedirectScheme is a redirect to something other than http or https.
var errRedirectScheme = errors.New("redirect to another kind of address")

// errTooManyRedirects is a fourth redirect.
var errTooManyRedirects = errors.New("too many redirects")

// errNotAllowed is a redirect to a literal address on the NAS or localhost.
var errNotAllowed = errors.New("address not allowed")

// errNoProxy is a hop for which no proxy can be used while one is required.
var errNoProxy = errors.New("no proxy")

// fetcher makes every outbound request of the package: one GET, address
// guard always on (loopback, link-local, unspecified, multicast and the
// NAS's own addresses are refused after name resolution and after every
// redirect; a request can ask for the strict rules), the proxy a regular
// user's request to each address would use, chosen again for every
// redirect, fixed limits, no cookies and no credentials of the caller.
type fetcher struct {
	version string
	// proxy is the proxy URL for an address ("" = direct): the "auto" rule
	// for a regular user, whoever asks (core.Manager.ProxyFor).
	proxy   func(rawURL string) (string, error)
	timeout time.Duration
	// wrap lets tests answer some hosts in the process; the guarded
	// transport handles every other request.
	wrap func(http.RoundTripper) http.RoundTripper
}

func newFetcher(version string, proxy func(string) (string, error)) *fetcher {
	if proxy == nil {
		proxy = func(string) (string, error) { return "", nil }
	}
	return &fetcher{version: version, proxy: proxy, timeout: fetchTimeout}
}

// request is one GET.
type request struct {
	url     string
	headers map[string]string // plugin headers (sent to the plugin's host only)
	accept  string
	limit   int64
	// Conditional request (catalogue feeds)
	etag, modified string
	// rule checks the targets (nil: the usual guard)
	rule *netutil.Rule
}

// response is what a request got. A redirect to a magnet link is not
// followed: Magnet holds it and Body is empty.
type response struct {
	Status   int
	Body     []byte
	Magnet   string
	URL      string // the address the answer came from
	ETag     string
	Modified string
}

func (f *fetcher) userAgent() string {
	return "DownloadCenter/" + f.version + " (+" + userAgentLink + ")"
}

// port is the port of u, the scheme's when it has none.
func port(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

// hopRT sends each request (the first and every redirect) with the proxy
// the rule for its own address chooses.
type hopRT struct {
	f    *fetcher
	rule *netutil.Rule
	by   map[string]http.RoundTripper // proxy -> transport, for this fetch
}

func (h *hopRT) RoundTrip(req *http.Request) (*http.Response, error) {
	proxy, err := h.f.proxy(req.URL.String())
	if err != nil {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, errNoProxy
	}
	rt := h.by[proxy]
	if rt == nil {
		rt = netutil.TransportRule(h.f.timeout, proxy, h.rule)
		h.by[proxy] = rt
	}
	return rt.RoundTrip(req)
}

// fetch makes the request. Every error is a *Failure.
func (f *fetcher) fetch(ctx context.Context, rq request) (*response, error) {
	u, err := url.Parse(rq.url)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, fail(ReasonConnect, "not an http address")
	}
	rule := rq.rule
	if rule == nil {
		rule = netutil.Guard
	}
	// Literal hosts and ports the rule refuses are refused before anything
	// else happens
	if rule.CheckLiteral(u.Hostname(), port(u)) != nil {
		return nil, fail(ReasonNotAllowed, "")
	}
	var rt http.RoundTripper = &hopRT{f: f, rule: rule, by: map[string]http.RoundTripper{}}
	if f.wrap != nil {
		rt = f.wrap(rt)
	}
	cl := &http.Client{
		Timeout:   f.timeout,
		Transport: rt,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// The previous address (keyword, API key) goes nowhere else
			req.Header.Del("Referer")
			if strings.EqualFold(req.URL.Scheme, "magnet") {
				return http.ErrUseLastResponse
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errRedirectScheme
			}
			if len(via) > maxRedirects {
				return errTooManyRedirects
			}
			// Literal addresses are refused here, names after resolution
			if rule.CheckLiteral(req.URL.Hostname(), port(req.URL)) != nil {
				return errNotAllowed
			}
			// The plugin's headers go to its own host and scheme only
			if !strings.EqualFold(req.URL.Host, u.Host) || req.URL.Scheme != u.Scheme {
				for k := range rq.headers {
					req.Header.Del(k)
				}
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, "GET", rq.url, nil)
	if err != nil {
		return nil, fail(ReasonConnect, "bad request")
	}
	// One request per transport: nothing is kept open afterwards
	req.Close = true
	for k, v := range rq.headers {
		req.Header.Set(k, v)
	}
	if rq.accept != "" && req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", rq.accept)
	}
	req.Header.Set("User-Agent", f.userAgent())
	if rq.etag != "" {
		req.Header.Set("If-None-Match", rq.etag)
	}
	if rq.modified != "" {
		req.Header.Set("If-Modified-Since", rq.modified)
	}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, classify(ctx, err)
	}
	defer resp.Body.Close()
	out := &response{Status: resp.StatusCode, URL: resp.Request.URL.String(), ETag: resp.Header.Get("ETag"), Modified: resp.Header.Get("Last-Modified")}
	if resp.StatusCode/100 == 3 {
		if loc := resp.Header.Get("Location"); isMagnet(loc) {
			out.Magnet = loc
			return out, nil
		}
		if resp.StatusCode != http.StatusNotModified {
			return nil, fail(ReasonConnect, "redirect without a usable address")
		}
		return out, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, rq.limit+1))
	if err != nil {
		return nil, classify(ctx, err)
	}
	if int64(len(body)) > rq.limit {
		return nil, fail(ReasonTooLarge, "")
	}
	out.Body = body
	return out, nil
}

// classify turns a transport error into a Failure without the address the
// error carries.
func classify(ctx context.Context, err error) *Failure {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var ne net.Error
	switch {
	case errors.Is(err, errTooManyRedirects):
		return fail(ReasonConnect, "too many redirects")
	case errors.Is(err, errNotAllowed), errors.Is(err, netutil.ErrNotAllowed):
		return fail(ReasonNotAllowed, "")
	case errors.Is(err, errNoProxy):
		return fail(ReasonNoProxy, "")
	case errors.Is(err, netutil.ErrTunnelRefused):
		// The proxy cannot be used for this address
		return fail(ReasonNoProxy, "tunnel refused")
	case errors.Is(err, errRedirectScheme):
		return fail(ReasonConnect, "redirect to another kind of address")
	case errors.Is(err, context.Canceled):
		return fail(ReasonConnect, "cancelled")
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout(), ctx.Err() == context.DeadlineExceeded:
		return fail(ReasonTimeout, "")
	case strings.Contains(err.Error(), " is not allowed"):
		// netutil's guard: a forbidden address after resolution or redirect
		return fail(ReasonNotAllowed, "")
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return fail(ReasonConnect, "name not resolved")
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return fail(ReasonConnect, op.Op)
	}
	return fail(ReasonConnect, "")
}

// statusFailure is the Failure of an answer that is not a success: 429 is
// busy, 401 and 403 a refused key when the source has one.
func statusFailure(status int, hasKey bool) *Failure {
	switch {
	case status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable:
		return fail(ReasonBusy, fmt.Sprint(status))
	case (status == http.StatusUnauthorized || status == http.StatusForbidden) && hasKey:
		return fail(ReasonRefused, fmt.Sprint(status))
	}
	return fail(strings.Replace(ReasonHTTP, "{0}", fmt.Sprint(status), 1), "")
}

var spaceRe = regexp.MustCompile(`\s+`)

// torznabReason is the Failure of a Torznab error: a refused key, a busy
// source, or the server's own text without control characters, addresses,
// the keyword or secrets, cut to 120 characters.
func torznabReason(e *TorznabError, redact []string) *Failure {
	switch e.Kind() {
	case "auth":
		return fail(ReasonRefused, fmt.Sprintf("torznab %d", e.Code))
	case "busy":
		return fail(ReasonBusy, fmt.Sprintf("torznab %d", e.Code))
	}
	d := Redact(cleanTitle(e.Description), redact)
	if r := []rune(d); len(r) > 120 {
		d = string(r[:120]) + "…"
	}
	if d == "" {
		d = fmt.Sprint(e.Code)
	}
	f := fail(strings.Replace(ReasonTorznab, "{0}", d, 1), fmt.Sprintf("torznab %d", e.Code))
	f.template = ReasonTorznab
	return f
}

var urlRe = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://\S+|\?\S+`)

// Redact removes addresses, query strings and the given values (secrets,
// the keyword; matched ignoring case, also URL-escaped) from a text.
func Redact(s string, values []string) string {
	s = urlRe.ReplaceAllString(s, "…")
	for _, v := range values {
		v = strings.TrimSpace(v)
		if len(v) < 2 {
			continue
		}
		for _, form := range []string{v, url.QueryEscape(v), url.PathEscape(v)} {
			re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(form))
			s = re.ReplaceAllString(s, "…")
		}
	}
	return spaceRe.ReplaceAllString(strings.TrimSpace(s), " ")
}
