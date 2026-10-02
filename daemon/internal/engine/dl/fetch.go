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
	transient bool // worth another connection attempt
	perConn   bool // may only mean "no more connections" (other connections still work)
	restart   bool // start the file over (changed on the server, ranges ignored)
	noRanges  bool // with restart: the server ignores ranges
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
// checks every address it connects to.
func (t *task) request(ctx context.Context, rng string, head bool) (xreq, error) {
	raw := t.uri()
	r := xreq{URL: raw, Range: rng, Head: head, User: t.req.User, Pass: t.req.Pass, Proxy: t.req.Proxy, Guard: t.req.Guard}
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
	if r.Proxy != "" && r.Guard {
		u, err := url.Parse(raw)
		if err != nil {
			return r, &dlError{code: engine.ErrUnknown, msg: "bad URL"}
		}
		if err := netutil.CheckHost(ctx, u.Hostname(), strings.HasPrefix(r.Proxy, "socks5h:")); err != nil {
			return r, &dlError{code: engine.ErrUnknown, msg: err.Error()}
		}
	}
	return r, nil
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
	return t.e.sc.transfer(ctx, r)
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
	x, cancel, err := t.e.sc.transfer(ctx, r)
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
// tool's exit codes) to the engine's codes.
func curlError(code int, msg string) *dlError {
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
	}
	return &dlError{code: engine.ErrUnknown, msg: msg}
}
