package netutil

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

func TestStrict(t *testing.T) {
	refused := []string{
		"127.0.0.1", "127.8.9.10", "0.0.0.0", "0.1.2.3", "169.254.169.254", "10.0.0.1", "10.255.255.255",
		"172.16.0.1", "172.31.255.255", "192.168.1.1", "100.64.0.1", "100.127.255.254", "192.0.0.8", "192.0.2.1",
		"198.18.0.1", "198.51.100.7", "203.0.113.5", "224.0.0.1", "239.255.255.250", "240.0.0.1", "255.255.255.255",
		"::", "::1", "::127.0.0.1", "::ffff:127.0.0.1", "::ffff:192.168.1.1", "::ffff:100.64.0.1", "64:ff9b::7f00:1",
		"64:ff9b:1::a00:1", "2002:c0a8:101::1", "2001:0:4136:e378:8000:63bf:3fff:fdd2", "2001:db8::1",
		"fc00::1", "fd12:3456::1", "fe80::1", "fec0::1", "ff02::1", "100::1",
	}
	allowed := []string{"1.1.1.1", "93.184.216.34", "100.63.255.255", "100.128.0.0", "172.32.0.1", "192.169.0.1",
		"2606:4700:4700::1111", "2a00:1450:4001:80b::200e", "::ffff:8.8.8.8"}
	for _, s := range refused {
		if !Strict(net.ParseIP(s)) {
			t.Errorf("%s allowed", s)
		}
	}
	for _, s := range allowed {
		if Strict(net.ParseIP(s)) {
			t.Errorf("%s refused", s)
		}
	}
	if !Strict(nil) || !Strict(net.IP{1, 2, 3}) {
		t.Error("malformed address allowed")
	}
	// The NAS's own addresses
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !Strict(ipn.IP) {
				t.Errorf("own address %s allowed", ipn.IP)
			}
		}
	}
	for _, p := range []string{"80", "443"} {
		if !StrictPort(p) {
			t.Errorf("port %s refused", p)
		}
	}
	for _, p := range []string{"22", "8080", "0", "", "0080", "443 ", "65536", "-80"} {
		if StrictPort(p) {
			t.Errorf("port %q allowed", p)
		}
	}
}

func TestResolveChecked(t *testing.T) {
	old := lookupIP
	defer func() { lookupIP = old }()
	answers := map[string][]string{
		"public.example":  {"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"},
		"mixed.example":   {"93.184.216.34", "192.168.1.1"},
		"rebind.example":  {"10.0.0.5"},
		"mapped.example":  {"::ffff:127.0.0.1"},
		"cgnat.example":   {"100.64.3.4"},
		"nothing.example": {},
	}
	asked := 0
	lookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
		asked++
		l, ok := answers[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		var out []net.IP
		for _, s := range l {
			out = append(out, net.ParseIP(s))
		}
		return out, nil
	}
	ctx := context.Background()
	ips, err := ResolveChecked(ctx, "public.example", Strict)
	if err != nil || len(ips) != 2 {
		t.Fatalf("public: %v %v", ips, err)
	}
	for _, h := range []string{"mixed.example", "rebind.example", "mapped.example", "cgnat.example"} {
		if _, err := ResolveChecked(ctx, h, Strict); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("%s: %v", h, err)
		}
	}
	if _, err := ResolveChecked(ctx, "nothing.example", Strict); err == nil {
		t.Error("a name without addresses")
	}
	// Literal forms and localhost never reach the resolver
	asked = 0
	for _, h := range []string{"127.0.0.1", "2130706433", "0x7f.1", "0177.0.0.1", "127.1", "10.1.0xb01", "::1", "[::1]",
		"localhost", "LOCALHOST.", "a.localhost", "192.168.1.1", "0xa9.254.169.254"} {
		if _, err := ResolveChecked(ctx, h, Strict); err == nil {
			t.Errorf("%s allowed", h)
		}
	}
	if asked != 1 { // "[::1]" is not a literal and goes to the resolver, which fails
		t.Errorf("resolver asked %d times", asked)
	}
	if ips, err := ResolveChecked(ctx, "8.8.8.8", Strict); err != nil || ips[0].String() != "8.8.8.8" {
		t.Errorf("literal public address: %v %v", ips, err)
	}
}

// The strict dialer refuses before connecting: a listener on the refused
// address sees nothing, whatever the spelling of the address or the port.
func TestDialStrict(t *testing.T) {
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	hits := make(chan struct{}, 10)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			hits <- struct{}{}
			c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	targets := []string{"127.0.0.1", "2130706433", "localhost", "0.0.0.0"}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() {
				targets = append(targets, ipn.IP.String())
			}
		}
	}
	ctx := context.Background()
	for _, h := range targets {
		// The listener's port is allowed here so that only the address is judged
		if c, err := DialChecked(ctx, h, port, Strict); err == nil || !errors.Is(err, ErrNotAllowed) {
			if c != nil {
				c.Close()
			}
			t.Errorf("%s: %v", h, err)
		}
		if _, err := DialStrict(ctx, h, port); err == nil || !strings.Contains(err.Error(), "port") {
			t.Errorf("%s port %s: %v", h, port, err)
		}
	}
	// Ports: refused before any lookup
	for _, p := range []string{"22", "8080", "25"} {
		if _, err := DialStrict(ctx, "example.org", p); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("port %s: %v", p, err)
		}
	}
	select {
	case <-hits:
		t.Error("the listener was reached")
	default:
	}
	// A permissive check connects to the address it checked
	c, err := DialChecked(ctx, "127.0.0.1", port, func(net.IP) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	<-hits
}
