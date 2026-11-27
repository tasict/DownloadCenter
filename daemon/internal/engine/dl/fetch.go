package dl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"downloadcenter/internal/engine"
	"downloadcenter/internal/netutil"
	"downloadcenter/internal/torrent"
)

// Error codes of the transfer layer itself (dc-dl unreachable, address
// refused by the guard, SFTP host key changed).
const (
	engineDownCode    = engine.ErrNetwork
	engineBlockedCode = engine.ErrUnknown
	engineHostKeyCode = engine.ErrHostKey
)

// dlError is a download failure in the engine's error vocabulary.
type dlError struct {
	code      string
	msg       string
	transient bool        // worth another connection attempt
	perConn   bool        // may only mean "no more connections" (other connections still work)
	restart   bool        // start the file over (changed on the server, ranges ignored)
	noRanges  bool        // with restart: the server ignores ranges
	issuer    *needIssuer // a certificate without an issuer, which may be fetched (aia.go)
}

func (e *dlError) Error() string { return e.code + ": " + e.msg }

// stream is the body of one connection.
type stream struct {
	body   io.ReadCloser
	cancel context.CancelFunc
}

func (s *stream) Close() {
	s.cancel()
	s.body.Close()
}

// info is what the first response says about the remote file.
type info struct {
	name     string
	size     int64
	ranges   bool
	etag     string
	modified string
}

// scheme returns the lower-case URL scheme.
func scheme(u string) string {
	s, _, _ := strings.Cut(u, "://")
	return strings.ToLower(s)
}

func isHTTP(u string) bool {
	s := scheme(u)
	return s == "http" || s == "https"
}

// request builds the dc-dl request for this task. With a proxy the target
// is checked here (dc-dl only sees the proxy's address); without one dc-dl
// checks every address it connects to (see also fetchHTTP).
func (t *task) request(ctx context.Context, rng string, head bool) (xreq, error) {
	raw := t.uri()
	r := xreq{URL: raw, Range: rng, Head: head, User: t.req.User, Pass: t.req.Pass, Proxy: t.req.Proxy, Guard: t.req.Guard, SkipCertCheck: t.req.SkipCertCheck}
	if isHTTP(raw) {
		// Transparent compression would break byte ranges
		r.Headers = append(append([]string{}, t.req.Headers...), "Accept-Encoding: identity")
	}
	if r.User == "" {
		if u, err := url.Parse(raw); err == nil && u.User != nil {
			r.User = u.User.Username()
			r.Pass, _ = u.User.Password()
		}
	}
	return r, checkTarget(ctx, r)
}

// checkTarget: a guarded request through a proxy may only go where a direct
// one could (the proxy reaches the target, dc-dl cannot check it).
func checkTarget(ctx context.Context, r xreq) error {
	if r.Proxy == "" || !r.Guard {
		return nil
	}
	u, err := url.Parse(r.URL)
	if err != nil {
		return &dlError{code: engine.ErrUnknown, msg: "bad URL"}
	}
	if err := netutil.CheckHost(ctx, u.Hostname(), strings.HasPrefix(r.Proxy, "socks5h:")); err != nil {
		return &dlError{code: engine.ErrUnknown, msg: err.Error()}
	}
	return nil
}

const maxRedirects = 10

// Request headers that still go to another host after a redirect. Every
// other one (cookies, tokens, whatever the task added) stays with the host
// it was meant for, like the credentials.
var redirectSafe = map[string]bool{"accept": true, "accept-encoding": true, "accept-language": true, "if-range": true}

// fetchHTTP sends an HTTP request and follows its redirects here, one at a
// time (dc-dl 1.2 does not follow them when asked): each new address is
// checked like the first, and a request to another host (or from https to
// http) keeps neither credentials nor headers outside redirectSafe.
func (t *task) fetchHTTP(ctx context.Context, r xreq) (*xfer, context.CancelFunc, error) {
	if !t.e.sc.followsItself() {
		// An older dc-dl: libcurl follows the redirects itself
		return t.transfer(ctx, r)
	}
	no := false
	r.Follow = &no
	for hops := 0; ; hops++ {
		x, cancel, err := t.transfer(ctx, r)
		if err != nil {
			return nil, nil, err
		}
		loc := x.h.header("Location")
		if !isRedirect(x.h.Status) || loc == "" {
			return x, cancel, nil
		}
		cancel()
		x.Close()
		if hops >= maxRedirects {
			return nil, nil, &dlError{code: engine.ErrRedirects, msg: "Maximum (10) redirects followed"}
		}
		next, err := redirectTarget(r.URL, loc)
		if err != nil {
			return nil, nil, err
		}
		if !sameOrigin(r.URL, next) {
			var keep []string
			for _, h := range r.Headers {
				if k, _, ok := strings.Cut(h, ":"); ok && redirectSafe[strings.ToLower(strings.TrimSpace(k))] {
					keep = append(keep, h)
				}
			}
			r.Headers, r.User, r.Pass = keep, "", ""
		}
		r.URL = next
		if err := checkTarget(ctx, r); err != nil {
			return nil, nil, err
		}
	}
}

func isRedirect(status int) bool {
	switch status {
	case 301, 302, 303, 307, 308:
		return true
	}
	return false
}

// redirectTarget resolves a Location against the URL that answered it:
// http or https only, without credentials.
func redirectTarget(from, loc string) (string, error) {
	base, err := url.Parse(from)
	if err != nil {
		return "", &dlError{code: engine.ErrUnknown, msg: "bad URL"}
	}
	ref, err := url.Parse(strings.TrimSpace(loc))
	if err != nil {
		return "", &dlError{code: engine.ErrBadResponse, msg: "bad redirect"}
	}
	u := base.ResolveReference(ref)
	if s := strings.ToLower(u.Scheme); s != "http" && s != "https" || u.Host == "" {
		return "", &dlError{code: engine.ErrUnknown, msg: "unsupported protocol"}
	}
	u.User, u.Fragment = nil, ""
	return u.String(), nil
}

// sameOrigin: the same host and port, and not from https down to http.
func sameOrigin(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	if err1 != nil || err2 != nil {
		return false
	}
	sa, sb := strings.ToLower(ua.Scheme), strings.ToLower(ub.Scheme)
	port := func(u *url.URL, s string) string {
		if p := u.Port(); p != "" {
			return p
		}
		if s == "https" {
			return "443"
		}
		return "80"
	}
	return strings.EqualFold(ua.Hostname(), ub.Hostname()) && port(ua, sa) == port(ub, sb) && !(sa == "https" && sb == "http")
}

// probe opens the first connection. For HTTP (and SCP, which has no
// ranges) its body is the start of the download; FTP and SFTP ask for the
// size first and open no stream.
func (t *task) probe(ctx context.Context) (*stream, *info, error) {
	raw := t.uri()
	switch scheme(raw) {
	case "http", "https":
		return t.openHTTP(ctx, 0, -1, true)
	case "scp":
		x, cancel, err := t.send(ctx, "", false)
		if err != nil {
			return nil, nil, err
		}
		u, _ := url.Parse(raw)
		return &stream{body: x, cancel: cancel}, &info{name: urlName(u), size: x.h.Length}, nil
	}
	x, cancel, err := t.send(ctx, "", true)
	if err != nil {
		return nil, nil, err
	}
	// libcurl reports the size and date of FTP files as "Content-Length:" and
	// "Last-Modified:" lines in the body of a NOBODY request
	body, _ := io.ReadAll(io.LimitReader(x, 8192))
	cancel()
	x.Close()
	lines := append(append([]string{}, x.h.Headers...), strings.Split(strings.ReplaceAll(string(body), "\r", ""), "\n")...)
	h := &hframe{Headers: lines}
	u, _ := url.Parse(raw)
	inf := &info{name: urlName(u), size: x.h.Length, modified: h.header("Last-Modified")}
	if n, err := strconv.ParseInt(h.header("Content-Length"), 10, 64); err == nil {
		inf.size = n
	}
	if inf.modified == "" && x.h.Filetime > 0 {
		inf.modified = time.Unix(x.h.Filetime, 0).UTC().Format(http.TimeFormat)
	}
	// FTP REST and SFTP seeks are near universal; a server that refuses
	// them makes the transfer fail with a range error, which restarts the
	// file without ranges
	inf.ranges = inf.size > 0
	return nil, inf, nil
}

// open requests [from, to) (to < 0: until the end).
func (t *task) open(ctx context.Context, from, to int64) (*stream, error) {
	if isHTTP(t.uri()) {
		s, _, err := t.openHTTP(ctx, from, to, false)
		return s, err
	}
	x, cancel, err := t.send(ctx, byteRange(from, to), false)
	if err != nil {
		return nil, err
	}
	return &stream{body: x, cancel: cancel}, nil
}

func byteRange(from, to int64) string {
	if to > 0 {
		return fmt.Sprintf("%d-%d", from, to-1)
	}
	if from > 0 {
		return fmt.Sprintf("%d-", from)
	}
	return ""
}

func (t *task) send(ctx context.Context, rng string, head bool) (*xfer, context.CancelFunc, error) {
	r, err := t.request(ctx, rng, head)
	if err != nil {
		return nil, nil, err
	}
	return t.transfer(ctx, r)
}

// transfer sends r to dc-dl. When the server's certificate cannot be
// verified for want of an issuer, the issuer is fetched from the address
// the certificate names and r is sent again with it as a candidate, up to
// maxChainLevels times. Requests to a host carry the candidates found for
// it before.
func (t *task) transfer(ctx context.Context, r xreq) (*xfer, context.CancelFunc, error) {
	sc := t.e.sc
	// What dc-dl can do is known once it runs (a crashed one is started
	// again here; when that fails, transfer reports it)
	if sc.ensure() == nil && r.SkipCertCheck && !sc.skipsCheck() {
		return nil, nil, &dlError{code: engine.ErrUnknown, msg: "This download needs a newer download engine"}
	}
	if r.SkipCertCheck || !sc.chains() {
		return sc.transfer(ctx, r)
	}
	host := ""
	if u, err := url.Parse(r.URL); err == nil {
		host = u.Hostname()
	}
	tried := map[string]bool{}
	for level := 0; ; level++ {
		r.Chain = t.e.chains.forHost(host)
		x, cancel, err := sc.transfer(ctx, r)
		var de *dlError
		if err == nil || level >= maxChainLevels || !errors.As(err, &de) || de.issuer == nil || tried[de.issuer.Cert] || host == "" {
			return x, cancel, err
		}
		tried[de.issuer.Cert] = true
		if !t.e.chains.complete(ctx, host, de.issuer, r.Proxy) {
			return nil, nil, err
		}
		t.mu.Lock()
		first := !t.chained[host]
		if t.chained == nil {
			t.chained = map[string]bool{}
		}
		t.chained[host] = true
		t.mu.Unlock()
		if first {
			t.e.logf(t.ref, "The server "+host+" does not send its complete certificate chain; the missing certificate was fetched from its certificate authority")
		}
	}
}

func (t *task) openHTTP(ctx context.Context, from, to int64, probe bool) (*stream, *info, error) {
	rng := byteRange(from, to)
	if rng == "" {
		rng = "0-"
	}
	r, err := t.request(ctx, rng, false)
	if err != nil {
		return nil, nil, err
	}
	t.mu.Lock()
	validator := t.validator()
	t.mu.Unlock()
	if !probe && validator != "" {
		r.Headers = append(r.Headers, "If-Range: "+validator)
	}
	x, cancel, err := t.fetchHTTP(ctx, r)
	if err != nil {
		return nil, nil, err
	}
	fail := func(e *dlError) (*stream, *info, error) {
		cancel()
		x.Close()
		return nil, nil, e
	}
	h := &x.h
	if h.Status >= 400 {
		if h.Status == http.StatusRequestedRangeNotSatisfiable && !probe {
			return fail(&dlError{code: engine.ErrBadResponse, msg: "status=416", restart: true})
		}
		return fail(statusError(h.Status))
	}
	if h.Status != http.StatusOK && h.Status != http.StatusPartialContent {
		return fail(&dlError{code: engine.ErrBadResponse, msg: "status=" + strconv.Itoa(h.Status)})
	}
	// Where the address never serves pages, a page is an error message
	// (a quota or sign-in page): starting the file over would replace it
	if t.req.NoPages && isPage(h.header("Content-Type")) {
		return fail(&dlError{code: engine.ErrBadResponse, msg: "the server sent a web page instead of the file"})
	}
	start, total, partial := contentRange(h)
	if probe {
		inf := &info{name: responseName(h), size: h.Length, etag: h.header("ETag"), modified: h.header("Last-Modified")}
		if partial {
			inf.size, inf.ranges = total, total > 0 && start == 0
		} else if strings.EqualFold(h.header("Accept-Ranges"), "bytes") {
			inf.ranges = true
		}
		return &stream{body: x, cancel: cancel}, inf, nil
	}
	t.mu.Lock()
	size := t.size
	t.mu.Unlock()
	if !partial {
		// The whole file came back: with If-Range that means it changed,
		// without it the server ignores ranges
		return fail(&dlError{code: engine.ErrNoResume, msg: "the server sent the whole file", restart: true, noRanges: validator == ""})
	}
	if start != from || (total > 0 && total != size) {
		return fail(&dlError{code: engine.ErrNoResume, msg: "the file size changed", restart: true})
	}
	return &stream{body: x, cancel: cancel}, nil, nil
}

// validator is the If-Range value: a strong ETag, else Last-Modified.
// Caller holds t.mu.
func (t *task) validator() string {
	if t.etag != "" && !strings.HasPrefix(t.etag, "W/") {
		return t.etag
	}
	return t.modified
}

// contentRange parses "Content-Range: bytes a-b/total" of a 206.
func contentRange(h *hframe) (start, total int64, ok bool) {
	if h.Status != http.StatusPartialContent {
		return 0, -1, false
	}
	v, found := strings.CutPrefix(h.header("Content-Range"), "bytes ")
	if !found {
		return 0, -1, false
	}
	rng, tot, _ := strings.Cut(v, "/")
	a, _, _ := strings.Cut(rng, "-")
	start, err := strconv.ParseInt(strings.TrimSpace(a), 10, 64)
	if err != nil {
		return 0, -1, false
	}
	total, err = strconv.ParseInt(strings.TrimSpace(tot), 10, 64)
	if err != nil {
		total = -1
	}
	return start, total, true
}

// isPage reports whether a Content-Type is a web page.
func isPage(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	return err == nil && (mt == "text/html" || mt == "application/xhtml+xml")
}

// responseName picks the file name: Content-Disposition, else the last
// path segment of the final URL.
func responseName(h *hframe) string {
	if cd := h.header("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			if n := cleanName(params["filename"]); n != "" {
				return n
			}
		}
	}
	u, _ := url.Parse(h.URL)
	return urlName(u)
}

func urlName(u *url.URL) string {
	if u != nil {
		if n := cleanName(path.Base(u.Path)); n != "" && n != "/" {
			return n
		}
	}
	return "index.html"
}

func cleanName(s string) string {
	if strings.Contains(s, "%") {
		if u, err := url.PathUnescape(s); err == nil {
			s = u
		}
	}
	s = path.Base(strings.ReplaceAll(s, "\\", "/"))
	if s == "." || s == "/" {
		return ""
	}
	return torrent.SafeName(s)
}

func statusError(code int) *dlError {
	msg := "status=" + strconv.Itoa(code)
	switch {
	case code == 404 || code == 410:
		return &dlError{code: engine.ErrNotFoundURL, msg: msg}
	case code == 401 || code == 403:
		return &dlError{code: engine.ErrAuth, msg: msg, perConn: true}
	case code == 429 || code == 503 || code == 509:
		return &dlError{code: engine.ErrServerBusy, msg: msg, transient: true, perConn: true}
	case code == 500 || code == 502 || code == 504:
		return &dlError{code: engine.ErrBadResponse, msg: msg, transient: true}
	}
	return &dlError{code: engine.ErrBadResponse, msg: msg}
}

// netError maps errors that did not come from dc-dl.
func netError(err error, viaProxy bool) *dlError {
	var de *dlError
	if errors.As(err, &de) {
		return de
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &dlError{code: engine.ErrTimeout, msg: "timeout", transient: true}
	}
	return &dlError{code: engine.ErrNetwork, msg: err.Error(), transient: true}
}

// fileError maps local file errors.
func fileError(code string, err error) *dlError {
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) {
		return &dlError{code: engine.ErrDiskFull, msg: err.Error()}
	}
	return &dlError{code: code, msg: err.Error()}
}

// curlError maps a libcurl result (CURLcode, the same numbers as the curl
// tool's exit codes) to the engine's codes; server is the address the
// failed request went to.
func curlError(code int, msg, server string) *dlError {
	if msg == "" {
		msg = "curl error " + strconv.Itoa(code)
	}
	switch code {
	case 6:
		return &dlError{code: engine.ErrResolve, msg: msg, transient: true}
	case 5, 97:
		return &dlError{code: engine.ErrNetwork, msg: "Proxy: " + msg, transient: true}
	case 7:
		return &dlError{code: engine.ErrNetwork, msg: "Failed to establish connection: " + msg, transient: true}
	case 28:
		return &dlError{code: engine.ErrTimeout, msg: msg, transient: true}
	case 9, 67:
		return &dlError{code: engine.ErrAuth, msg: msg}
	case 19, 78:
		return &dlError{code: engine.ErrNotFoundURL, msg: msg}
	case 33, 36:
		return &dlError{code: engine.ErrNoResume, msg: msg, restart: true, noRanges: true}
	case 18, 55, 56, 92:
		return &dlError{code: engine.ErrNetwork, msg: msg, transient: true}
	case 47:
		return &dlError{code: engine.ErrRedirects, msg: msg}
	case 8, 13, 14, 17, 30, 31:
		return &dlError{code: engine.ErrFTPCommand, msg: msg}
	case 1:
		return &dlError{code: engine.ErrUnknown, msg: "unsupported protocol"}
	case 60:
		return certError(server, msg)
	}
	return &dlError{code: engine.ErrUnknown, msg: msg}
}

// verifyCode is OpenSSL's number after its verify message, " (23)".
var verifyCode = regexp.MustCompile(` \(\d+\)$`)

// certError explains a server certificate libcurl did not accept by the
// reason in its message (OpenSSL's verify result or libcurl's host name
// check) and names the server, which after a redirect is not the link's
// host. The usual causes need different remedies: a server that leaves
// out its intermediate certificates (browsers fetch or keep those, libcurl
// does not), a private authority or an intercepting proxy, the NAS's clock,
// a certificate for another name.
func certError(server, reason string) *dlError {
	host := server
	if u, err := url.Parse(server); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	r := strings.ToLower(reason)
	var tpl string
	switch {
	// First: this message quotes names, which may contain the other words
	case strings.Contains(r, "subject name") || strings.Contains(r, "hostname mismatch"):
		tpl = "The certificate of {0} was issued for another host name"
	case strings.Contains(r, "local issuer") || strings.Contains(r, "get issuer certificate") || strings.Contains(r, "verify the first certificate"):
		tpl = "The certificate of {0} cannot be verified: the server does not send its complete certificate chain, or its certificate authority is not trusted"
	case strings.Contains(r, "self-signed") || strings.Contains(r, "self signed"):
		tpl = "The certificate of {0} cannot be verified: it is self-signed or issued by a certificate authority the NAS does not trust"
	case strings.Contains(r, "has expired") || strings.Contains(r, "not yet valid"):
		tpl = "The certificate of {0} has expired or is not valid yet; check the NAS's date and time"
	default:
		tpl = "The certificate of {0} cannot be verified ({1})"
		// libcurl words it by where the check failed: in the handshake, or
		// after it (dc-dl 1.5 checks the chain itself, see aia.go)
		reason = strings.TrimPrefix(reason, "SSL certificate problem: ")
		reason = strings.TrimPrefix(reason, "SSL certificate OpenSSL verify result: ")
		reason = verifyCode.ReplaceAllString(reason, "")
		if reason == "" {
			reason = "curl error 60"
		}
	}
	return &dlError{code: engine.ErrUnknown, msg: strings.NewReplacer("{0}", host, "{1}", reason).Replace(tpl)}
}
