// Package netutil holds outbound networking helpers: an HTTP client that
// refuses internal addresses after DNS resolution, proxy tests (HTTP and
// SOCKS5, including UDP ASSOCIATE) and a minimal SOCKS5 dialer.
package netutil

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Forbidden reports loopback, link-local, unspecified, multicast and the
// NAS's own addresses.
func Forbidden(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
				return true
			}
		}
	}
	return false
}

// Private reports RFC 1918 / ULA addresses.
func Private(ip net.IP) bool { return ip.IsPrivate() }

// Client returns an HTTP client. With guard, connections to forbidden
// addresses fail after resolution (no DNS rebinding through redirects
// either). proxyURL ("" = direct) is an http:// or socks5:// proxy.
func Client(timeout time.Duration, guard bool, proxyURL string) *http.Client {
	return &http.Client{Timeout: timeout, Transport: Transport(timeout, guard, proxyURL), CheckRedirect: maxRedirects(5)}
}

func maxRedirects(n int) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= n {
			return ErrTooManyRedirects
		}
		return nil
	}
}

// dialRace connects to the first address that answers, like the standard
// dialer does for host names (RFC 8305): IPv6 and IPv4 addresses take
// turns, a new attempt starts every 250 ms while earlier ones are pending,
// so one unreachable family does not cost a whole connect timeout.
func dialRace(ctx context.Context, d *net.Dialer, network string, ips []net.IP, port string) (net.Conn, error) {
	var v6, v4 []net.IP
	for _, ip := range ips {
		if ip.To4() == nil {
			v6 = append(v6, ip)
		} else {
			v4 = append(v4, ip)
		}
	}
	var order []net.IP
	for i := 0; i < len(v6) || i < len(v4); i++ {
		if i < len(v6) {
			order = append(order, v6[i])
		}
		if i < len(v4) {
			order = append(order, v4[i])
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type res struct {
		c   net.Conn
		err error
	}
	results := make(chan res, len(order))
	next, pending := 0, 0
	start := func() {
		addr := net.JoinHostPort(order[next].String(), port)
		next++
		pending++
		go func() {
			c, err := d.DialContext(ctx, network, addr)
			results <- res{c, err}
		}()
	}
	start()
	var lastErr error
	for pending > 0 {
		var wait <-chan time.Time
		if next < len(order) {
			wait = time.After(250 * time.Millisecond)
		}
		select {
		case r := <-results:
			pending--
			if r.err == nil {
				// Close the connections that lose the race
				go func(n int) {
					for ; n > 0; n-- {
						if o := <-results; o.c != nil {
							o.c.Close()
						}
					}
				}(pending)
				return r.c, nil
			}
			lastErr = r.err
			if next < len(order) {
				start()
			}
		case <-wait:
			start()
		}
	}
	return nil, lastErr
}

// ErrTooManyRedirects is returned when a redirect chain is too long.
var ErrTooManyRedirects = errors.New("too many redirects")

// Transport is the round tripper behind Client: header timeout, address
// guard and proxy.
func Transport(headerTimeout time.Duration, guard bool, proxyURL string) http.RoundTripper {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		var ok []net.IP
		var lastErr error = errors.New("no address")
		for _, ip := range ips {
			if guard && Forbidden(ip.IP) {
				lastErr = fmt.Errorf("address %s is not allowed", ip.IP)
				continue
			}
			ok = append(ok, ip.IP)
		}
		if len(ok) == 0 {
			return nil, lastErr
		}
		return dialRace(ctx, dialer, network, ok, port)
	}
	tr := &http.Transport{
		DialContext:           dial,
		Proxy:                 nil,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: headerTimeout,
		MaxIdleConns:          10,
		IdleConnTimeout:       60 * time.Second,
	}
	if proxyURL != "" {
		if u, err := url.Parse(proxyURL); err == nil {
			switch u.Scheme {
			case "http", "https":
				tr.Proxy = http.ProxyURL(u)
				tr.DialContext = dialer.DialContext
			case "socks5", "socks5h":
				user, pass := "", ""
				if u.User != nil {
					user = u.User.Username()
					pass, _ = u.User.Password()
				}
				tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
					return DialSOCKS5(u.Host, user, pass, addr, 15*time.Second)
				}
			}
		}
	}
	if guard && proxyURL != "" {
		// Through a proxy the dialer never sees the target: check it here
		return guardRT{next: tr, remoteDNS: strings.HasPrefix(proxyURL, "socks5h:")}
	}
	return tr
}

// SOCKS5 error kinds, matching the official V4 error codes 4608-4617.
var (
	ErrSocksVersion    = errors.New("socks5: unsupported version")      // 4608
	ErrSocksAuthMethod = errors.New("socks5: unsupported auth method")  // 4609
	ErrSocksAuthVer    = errors.New("socks5: unsupported auth version") // 4610
	ErrSocksAuth       = errors.New("socks5: authentication failed")    // 4611
	ErrSocksUserNeeded = errors.New("socks5: username required")        // 4612
	ErrSocksFailure    = errors.New("socks5: server connection failed") // 4613
	ErrSocksTimeout    = errors.New("socks5: connection timeout")       // 4617
)

func socksHandshake(c net.Conn, user, pass string) error {
	methods := []byte{0x00}
	if user != "" {
		methods = []byte{0x00, 0x02}
	}
	if _, err := c.Write(append([]byte{5, byte(len(methods))}, methods...)); err != nil {
		return ErrSocksFailure
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(c, resp); err != nil {
		return timeoutOr(err, ErrSocksFailure)
	}
	if resp[0] != 5 {
		return ErrSocksVersion
	}
	switch resp[1] {
	case 0x00:
		return nil
	case 0x02:
		if user == "" {
			return ErrSocksUserNeeded
		}
		req := []byte{1, byte(len(user))}
		req = append(req, user...)
		req = append(req, byte(len(pass)))
		req = append(req, pass...)
		if _, err := c.Write(req); err != nil {
			return ErrSocksFailure
		}
		ar := make([]byte, 2)
		if _, err := io.ReadFull(c, ar); err != nil {
			return timeoutOr(err, ErrSocksFailure)
		}
		if ar[0] != 1 {
			return ErrSocksAuthVer
		}
		if ar[1] != 0 {
			return ErrSocksAuth
		}
		return nil
	case 0xff:
		if user == "" {
			return ErrSocksUserNeeded
		}
		return ErrSocksAuthMethod
	}
	return ErrSocksAuthMethod
}

func timeoutOr(err, other error) error {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return ErrSocksTimeout
	}
	return other
}

func socksAddr(addr string) ([]byte, error) {
	host, portS, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, _ := strconv.Atoi(portS)
	var b []byte
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			b = append([]byte{1}, ip4...)
		} else {
			b = append([]byte{4}, ip...)
		}
	} else {
		b = append([]byte{3, byte(len(host))}, host...)
	}
	return binary.BigEndian.AppendUint16(b, uint16(port)), nil
}

func readSocksReply(c net.Conn) (net.IP, int, error) {
	h := make([]byte, 4)
	if _, err := io.ReadFull(c, h); err != nil {
		return nil, 0, timeoutOr(err, ErrSocksFailure)
	}
	if h[0] != 5 {
		return nil, 0, ErrSocksVersion
	}
	if h[1] != 0 {
		return nil, 0, fmt.Errorf("%w (reply %d)", ErrSocksFailure, h[1])
	}
	var ip net.IP
	switch h[3] {
	case 1:
		b := make([]byte, 4)
		io.ReadFull(c, b)
		ip = net.IP(b)
	case 4:
		b := make([]byte, 16)
		io.ReadFull(c, b)
		ip = net.IP(b)
	case 3:
		l := make([]byte, 1)
		io.ReadFull(c, l)
		b := make([]byte, int(l[0]))
		io.ReadFull(c, b)
	}
	p := make([]byte, 2)
	io.ReadFull(c, p)
	return ip, int(binary.BigEndian.Uint16(p)), nil
}

// DialSOCKS5 connects to addr through a SOCKS5 proxy (host names are
// resolved by the proxy).
func DialSOCKS5(proxy, user, pass, addr string, timeout time.Duration) (net.Conn, error) {
	c, err := net.DialTimeout("tcp", proxy, timeout)
	if err != nil {
		return nil, timeoutOr(err, ErrSocksFailure)
	}
	c.SetDeadline(time.Now().Add(timeout))
	if err := socksHandshake(c, user, pass); err != nil {
		c.Close()
		return nil, err
	}
	a, err := socksAddr(addr)
	if err != nil {
		c.Close()
		return nil, err
	}
	if _, err := c.Write(append([]byte{5, 1, 0}, a...)); err != nil {
		c.Close()
		return nil, ErrSocksFailure
	}
	if _, _, err := readSocksReply(c); err != nil {
		c.Close()
		return nil, err
	}
	c.SetDeadline(time.Time{})
	return c, nil
}

// ProxyResult is the outcome of a proxy test.
type ProxyResult struct {
	OK    bool   `json:"ok"`
	IP    string `json:"ip,omitempty"`
	UDP   *bool  `json:"udp,omitempty"`
	Error string `json:"error,omitempty"`
}

// TestProxy connects through the proxy to an IP echo service.
func TestProxy(kind, host string, port int, user, pass string) (*ProxyResult, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	var pu string
	auth := ""
	if user != "" {
		auth = url.UserPassword(user, pass).String() + "@"
	}
	switch kind {
	case "http":
		pu = "http://" + auth + addr
	case "socks5":
		pu = "socks5://" + auth + addr
		// Check the handshake first for a precise error
		c, err := DialSOCKS5(addr, user, pass, "api.ipify.org:443", 15*time.Second)
		if err != nil {
			return &ProxyResult{Error: err.Error()}, err
		}
		c.Close()
	default:
		return nil, errors.New("unknown proxy type")
	}
	cl := Client(20*time.Second, false, pu)
	resp, err := cl.Get("https://api.ipify.org")
	if err != nil {
		return &ProxyResult{Error: err.Error()}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 100))
	res := &ProxyResult{OK: true, IP: strings.TrimSpace(string(b))}
	if kind == "socks5" {
		udp := TestUDPAssociate(addr, user, pass)
		res.UDP = &udp
	}
	return res, nil
}

// TestUDPAssociate asks a SOCKS5 proxy for a UDP relay.
func TestUDPAssociate(proxy, user, pass string) bool {
	c, err := net.DialTimeout("tcp", proxy, 10*time.Second)
	if err != nil {
		return false
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if err := socksHandshake(c, user, pass); err != nil {
		return false
	}
	if _, err := c.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return false
	}
	_, port, err := readSocksReply(c)
	return err == nil && port > 0
}

// SocksCode maps a SOCKS5 test error to the official V4 error code.
func SocksCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrSocksVersion):
		return 4608
	case errors.Is(err, ErrSocksAuthMethod):
		return 4609
	case errors.Is(err, ErrSocksAuthVer):
		return 4610
	case errors.Is(err, ErrSocksAuth):
		return 4611
	case errors.Is(err, ErrSocksUserNeeded):
		return 4612
	case errors.Is(err, ErrSocksTimeout):
		return 4617
	}
	return 4613
}

// guardRT refuses requests whose target resolves to a forbidden address
// (used when a proxy hides the target from the dialer). With remoteDNS the
// proxy resolves names, so only literal addresses are checked: resolving
// here would leak the name to the local resolver.
type guardRT struct {
	next      http.RoundTripper
	remoteDNS bool
}

func (g guardRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := CheckHost(req.Context(), req.URL.Hostname(), g.remoteDNS); err != nil {
		return nil, err
	}
	return g.next.RoundTrip(req)
}

// CheckHost refuses a host that is or resolves to a forbidden address.
// With literalOnly, names other than localhost are not resolved.
func CheckHost(ctx context.Context, host string, literalOnly bool) error {
	if ip := net.ParseIP(host); ip != nil {
		if Forbidden(ip) {
			return fmt.Errorf("address %s is not allowed", ip)
		}
		return nil
	}
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return fmt.Errorf("address %s is not allowed", host)
	}
	if literalOnly {
		return nil
	}
	_, err := AllowedIP(ctx, host)
	return err
}

// AllowedIP resolves host and returns its first address that is not
// forbidden.
func AllowedIP(ctx context.Context, host string) (net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if Forbidden(ip) {
			return nil, fmt.Errorf("address %s is not allowed", ip)
		}
		return ip, nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var lastErr error = errors.New("no address")
	for _, ip := range ips {
		if Forbidden(ip.IP) {
			lastErr = fmt.Errorf("address %s is not allowed", ip.IP)
			continue
		}
		return ip.IP, nil
	}
	return nil, lastErr
}
