// Package netutil holds outbound networking helpers: an HTTP client that
// refuses internal addresses after DNS resolution, proxy tests (HTTP and
// SOCKS5, including UDP ASSOCIATE) and a minimal SOCKS5 dialer.
package netutil

import (
	"context"
	"crypto/tls"
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

// interleave orders addresses for racing (RFC 8305): the families take
// turns, starting with the family of the first address. The resolver has
// sorted them by RFC 6724, which puts IPv4 first on a NAS whose IPv6 has no
// global address (a ULA behind NAT66 often is the broken path).
func interleave(ips []net.IP) []net.IP {
	var first, second []net.IP
	for _, ip := range ips {
		if (ip.To4() == nil) == (ips[0].To4() == nil) {
			first = append(first, ip)
		} else {
			second = append(second, ip)
		}
	}
	var order []net.IP
	for i := 0; i < len(first) || i < len(second); i++ {
		if i < len(first) {
			order = append(order, first[i])
		}
		if i < len(second) {
			order = append(order, second[i])
		}
	}
	return order
}

// dialRace connects to the first address that answers, like the standard
// dialer does for host names (RFC 8305): the families take turns (see
// interleave), a new attempt starts every 250 ms while earlier ones are
// pending, so one unreachable family does not cost a whole connect
// timeout.
func dialRace(ctx context.Context, d *net.Dialer, network string, ips []net.IP, port string) (net.Conn, error) {
	if len(ips) == 0 {
		return nil, errors.New("no address")
	}
	order := interleave(ips)
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

// Waits of dialTLSRace before it tries the next address as well: while
// the connection is not made, and while a made connection's TLS handshake
// is not done (a path that lets the handshake's small packets through and
// drops the server's answer, as broken IPv6 does).
const (
	connectStagger   = 250 * time.Millisecond
	handshakeStagger = 2 * time.Second
)

// dialTLSRace is dialRace up to a finished TLS handshake: the winner is the
// first address whose handshake completes, so an address that connects but
// never answers the handshake does not hold up the others.
func dialTLSRace(ctx context.Context, d *net.Dialer, ips []net.IP, port string, cfg *tls.Config) (net.Conn, error) {
	if len(ips) == 0 {
		return nil, errors.New("no address")
	}
	order := interleave(ips)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type event struct {
		i         int
		conn      net.Conn
		err       error
		connected bool
	}
	events := make(chan event, 2*len(order))
	next, pending := 0, 0
	var wait <-chan time.Time
	start := func() {
		i := next
		next++
		pending++
		go func() {
			c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(order[i].String(), port))
			if err != nil {
				events <- event{i: i, err: err}
				return
			}
			events <- event{i: i, connected: true}
			tc := tls.Client(c, cfg.Clone())
			if err := tc.HandshakeContext(ctx); err != nil {
				c.Close()
				events <- event{i: i, err: err}
				return
			}
			events <- event{i: i, conn: tc}
		}()
		wait = time.After(connectStagger)
	}
	start()
	var lastErr error
	for pending > 0 {
		if next == len(order) {
			wait = nil
		}
		select {
		case e := <-events:
			if e.connected {
				if e.i == next-1 {
					wait = time.After(handshakeStagger)
				}
				continue
			}
			pending--
			if e.err == nil {
				// Close the connections that lose the race
				go func(n int) {
					for n > 0 {
						if o := <-events; !o.connected {
							n--
							if o.conn != nil {
								o.conn.Close()
							}
						}
					}
				}(pending)
				return e.conn, nil
			}
			lastErr = e.err
			if next < len(order) {
				start()
			}
		case <-wait:
			start()
		case <-ctx.Done():
			// A handshake that finishes now must not stay open
			go func(n int) {
				for n > 0 {
					if o := <-events; !o.connected {
						n--
						if o.conn != nil {
							o.conn.Close()
						}
					}
				}
			}(pending)
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

// ErrTooManyRedirects is returned when a redirect chain is too long.
var ErrTooManyRedirects = errors.New("too many redirects")

// Transport is the round tripper behind Client: header timeout, address
// guard (the usual one, see Guard) and proxy.
func Transport(headerTimeout time.Duration, guard bool, proxyURL string) http.RoundTripper {
	var r *Rule
	if guard {
		r = Guard
	}
	return TransportRule(headerTimeout, proxyURL, r)
}

// TransportRule is Transport with the targets checked by r (nil: none).
// proxyURL ("" = direct) is an http://, socks5:// or socks5h:// proxy; one
// that cannot be used fails every request instead of going direct. Through
// an http or socks5 proxy a checked target is handed to the proxy as the
// address that was checked, so the proxy cannot resolve its name to
// another one; a socks5h proxy resolves names itself, so only literal
// addresses, localhost and ports can be checked there.
func TransportRule(headerTimeout time.Duration, proxyURL string, r *Rule) http.RoundTripper {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	// resolve is the target's addresses in the resolver's order, without
	// the ones the rule refuses
	resolve := func(ctx context.Context, host, port string) ([]net.IP, error) {
		if r != nil {
			return r.resolve(ctx, host, port)
		}
		if ip := HostIP(host); ip != nil {
			return []net.IP{ip}, nil
		}
		return lookupIP(ctx, host)
	}
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := resolve(ctx, host, port)
		if err != nil {
			return nil, err
		}
		return dialRace(ctx, dialer, network, ips, port)
	}
	// HTTPS without a proxy races the addresses up to the TLS handshake
	dialTLS := func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := resolve(ctx, host, port)
		if err != nil {
			return nil, err
		}
		return dialTLSRace(ctx, dialer, ips, port, &tls.Config{ServerName: host, NextProtos: []string{"http/1.1"}, RootCAs: RootCAs()})
	}
	tr := &http.Transport{
		// Where Go makes the TLS handshake itself (through a proxy)
		TLSClientConfig:       TLSConfig(),
		DialContext:           dial,
		DialTLSContext:        dialTLS,
		Proxy:                 nil,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: headerTimeout,
		MaxIdleConns:          10,
		IdleConnTimeout:       60 * time.Second,
	}
	if proxyURL == "" {
		return tr
	}
	u, err := url.Parse(proxyURL)
	if err != nil || u.Host == "" {
		return failRT{ErrBadProxy}
	}
	tr.DialTLSContext = nil
	switch u.Scheme {
	case "https":
		// A proxy reached over TLS: only for unguarded requests (the
		// settings never make one)
		if r != nil {
			return failRT{ErrBadProxy}
		}
		tr.Proxy = http.ProxyURL(u)
		tr.DialContext = dialer.DialContext
		return tr
	case "http":
		// The proxy is dialled as it is: it is often on the LAN
		tr.Proxy = http.ProxyURL(u)
		tr.DialContext = dialer.DialContext
		if r == nil {
			return tr
		}
		// Guarded: every request, plain http too, goes in a CONNECT tunnel
		// to the checked address, so that the proxy never gets a name it
		// could resolve to another address; a refused tunnel fails the
		// request (ErrTunnelRefused)
		tunnel := tr.Clone()
		tunnel.Proxy = nil
		tunnel.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			return dialThrough(ctx, dialer, u, r, host, port, false)
		}
		tunnel.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			return dialThrough(ctx, dialer, u, r, host, port, true)
		}
		return tunnel
	case "socks5", "socks5h":
		user, pass := "", ""
		if u.User != nil {
			user = u.User.Username()
			pass, _ = u.User.Password()
		}
		remoteDNS := u.Scheme == "socks5h"
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if r != nil {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				if remoteDNS {
					// The proxy resolves names: resolving here would leak
					// the name to the local resolver
					if err := r.CheckLiteral(host, port); err != nil {
						return nil, err
					}
				} else {
					ips, err := r.resolve(ctx, host, port)
					if err != nil {
						return nil, err
					}
					addr = net.JoinHostPort(ips[0].String(), port)
				}
			}
			return DialSOCKS5(u.Host, user, pass, addr, 15*time.Second)
		}
		return tr
	}
	return failRT{ErrBadProxy}
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

// CheckHost refuses a host that is or resolves to a forbidden address.
// With literalOnly, names other than localhost are not resolved.
func CheckHost(ctx context.Context, host string, literalOnly bool) error {
	if ip := HostIP(host); ip != nil {
		if Forbidden(ip) {
			return fmt.Errorf("address %s is not allowed", ip)
		}
		return nil
	}
	if h := strings.ToLower(strings.TrimSuffix(host, ".")); h == "localhost" || strings.HasSuffix(h, ".localhost") {
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
	if ip := HostIP(host); ip != nil {
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

// HostIP is the address a host names by itself: an IP literal, or IPv4 in
// any form the C library's resolver takes as one (2130706433, 0x7f.1 and
// 127.1 are all 127.0.0.1), as dc-dl and dc-bt would connect to it; nil for
// a name. The rules are inet_aton's: one to four parts, each decimal, octal
// (a leading 0) or hexadecimal (0x), the last one filling the bytes left.
func HostIP(host string) net.IP {
	if ip := net.ParseIP(host); ip != nil {
		return ip
	}
	parts := strings.Split(host, ".")
	if len(parts) > 4 {
		return nil
	}
	var v [4]uint64
	for i, p := range parts {
		n, ok := atonPart(p)
		if !ok {
			return nil
		}
		v[i] = n
	}
	// The last part fills the remaining bytes
	last := len(parts) - 1
	limit := uint64(1)<<(8*uint(4-last)) - 1
	if v[last] > limit {
		return nil
	}
	ip := make(net.IP, 4)
	for i := 0; i < last; i++ {
		if v[i] > 255 {
			return nil
		}
		ip[i] = byte(v[i])
	}
	for i := 3; i >= last; i-- {
		ip[i] = byte(v[last] >> (8 * uint(3-i)))
	}
	return ip.To16()
}

// atonPart reads one part of an inet_aton address: it starts with a digit.
func atonPart(p string) (uint64, bool) {
	if p == "" || p[0] < '0' || p[0] > '9' {
		return 0, false
	}
	base, digits := uint64(10), p
	switch {
	case len(p) > 1 && (p[1] == 'x' || p[1] == 'X') && p[0] == '0':
		base, digits = 16, p[2:]
	case len(p) > 1 && p[0] == '0':
		base, digits = 8, p[1:]
	}
	if digits == "" {
		return 0, false
	}
	var n uint64
	for _, c := range digits {
		var d uint64
		switch {
		case c >= '0' && c <= '9':
			d = uint64(c - '0')
		case c >= 'a' && c <= 'f':
			d = uint64(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = uint64(c-'A') + 10
		default:
			return 0, false
		}
		if d >= base {
			return 0, false
		}
		n = n*base + d
		if n > 0xffffffff {
			return 0, false
		}
	}
	return n, true
}
