package netutil

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Rule is how a guarded request checks its target. A host for which Strict
// reports true gets the strict rules: public unicast addresses only, ports
// 80 and 443, and a name with one refused address is refused as a whole.
// Any other host gets the usual guard: the Forbidden addresses are left out
// and any port is fine.
type Rule struct {
	Strict func(host string) bool
}

// Guard is the usual guard for every host.
var Guard = &Rule{}

func (r *Rule) strict(host string) bool { return r.Strict != nil && r.Strict(host) }

func notAllowed(what string) error {
	return fmt.Errorf("address %s is not allowed: %w", what, ErrNotAllowed)
}

// CheckLiteral refuses what the rule refuses without asking a resolver: a
// literal address it refuses (any form the C library takes, see HostIP),
// localhost, and for a strict host a port other than 80 and 443.
func (r *Rule) CheckLiteral(host, port string) error {
	strict := r.strict(host)
	if strict && !StrictPort(port) {
		return fmt.Errorf("port %s is not allowed: %w", port, ErrNotAllowed)
	}
	if ip := HostIP(host); ip != nil {
		if Forbidden(ip) || strict && Strict(ip) {
			return notAllowed(ip.String())
		}
		return nil
	}
	if h := strings.ToLower(strings.TrimSuffix(host, ".")); h == "" || h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return notAllowed(host)
	}
	return nil
}

// resolve is the addresses of host the rule lets through, in the
// resolver's order.
func (r *Rule) resolve(ctx context.Context, host, port string) ([]net.IP, error) {
	if err := r.CheckLiteral(host, port); err != nil {
		return nil, err
	}
	if r.strict(host) {
		return ResolveChecked(ctx, host, Strict)
	}
	if ip := HostIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	ips, err := lookupIP(ctx, host)
	if err != nil {
		return nil, err
	}
	var ok []net.IP
	var lastErr error = errors.New("no address")
	for _, ip := range ips {
		if Forbidden(ip) {
			lastErr = notAllowed(ip.String())
			continue
		}
		ok = append(ok, ip)
	}
	if len(ok) == 0 {
		return nil, lastErr
	}
	return ok, nil
}

// failRT fails every request: a proxy setting that cannot be used never
// turns into a direct connection.
type failRT struct{ err error }

func (f failRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		req.Body.Close()
	}
	return nil, f.err
}

// ErrBadProxy is a proxy address that cannot be used.
var ErrBadProxy = errors.New("the proxy address cannot be used")

// ErrTunnelRefused is an http proxy that refused a tunnel to the port of a
// checked request (many proxies allow port 443 only): the request fails,
// the proxy is never asked by name instead.
var ErrTunnelRefused = errors.New("the proxy refused a tunnel, which a checked request needs")

// connectThrough opens a tunnel to target (an address and port) through the
// http proxy u on c.
func connectThrough(c net.Conn, u *url.URL, target string) error {
	req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n"
	if u.User != nil {
		pass, _ := u.User.Password()
		req += "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(u.User.Username()+":"+pass)) + "\r\n"
	}
	if _, err := c.Write([]byte(req + "\r\n")); err != nil {
		return err
	}
	br := bufio.NewReaderSize(c, 4096)
	resp, err := http.ReadResponse(br, &http.Request{Method: "CONNECT"})
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		_, port, _ := net.SplitHostPort(target)
		return fmt.Errorf("%w (port %s, HTTP %d)", ErrTunnelRefused, port, resp.StatusCode)
	}
	if br.Buffered() > 0 {
		// Nothing may come before what we send through the tunnel
		return errors.New("the proxy sent data before the tunnel was used")
	}
	return nil
}

// dialThrough connects to one checked address of host through the http
// proxy u; with tlsName it also makes the TLS handshake with that name.
func dialThrough(ctx context.Context, d *net.Dialer, u *url.URL, r *Rule, host, port string, useTLS bool) (net.Conn, error) {
	ips, err := r.resolve(ctx, host, port)
	if err != nil {
		return nil, err
	}
	c, err := d.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	if err := connectThrough(c, u, net.JoinHostPort(ips[0].String(), port)); err != nil {
		c.Close()
		return nil, err
	}
	var out net.Conn = c
	if useTLS {
		tc := tls.Client(c, &tls.Config{ServerName: host, NextProtos: []string{"http/1.1"}, RootCAs: RootCAs()})
		if err := tc.HandshakeContext(ctx); err != nil {
			c.Close()
			return nil, err
		}
		out = tc
	}
	c.SetDeadline(time.Time{})
	return out, nil
}
