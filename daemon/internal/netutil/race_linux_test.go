//go:build linux

package netutil

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// A host with two addresses on the loopback network: 127.0.0.2 is the bad
// one, 127.0.0.1 the good one, on the same port.

// tlsServer is a TLS server on 127.0.0.1 and how to trust it.
func tlsServer(t *testing.T) (port string, cfg *tls.Config) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv.Listener = ln
	// The handshakes the race abandons are expected
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	_, port, _ = net.SplitHostPort(ln.Addr().String())
	return port, &tls.Config{RootCAs: pool, ServerName: "example.com"}
}

// silent accepts connections on 127.0.0.2 and never says anything: the
// connection is made, the TLS handshake never ends.
func silent(t *testing.T, port string) {
	ln, err := net.Listen("tcp", "127.0.0.2:"+port)
	if err != nil {
		t.Skip("127.0.0.2 is not usable here")
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() })
		}
	}()
}

// hanging makes connections to 127.0.0.2 hang: a listener whose queue is
// full drops the next SYNs, so the connect never completes.
func hanging(t *testing.T, port string) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Close(fd) })
	p, _ := strconv.Atoi(port)
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Port: p, Addr: [4]byte{127, 0, 0, 2}}); err != nil {
		t.Skip("127.0.0.2 is not usable here")
	}
	if err := syscall.Listen(fd, 0); err != nil {
		t.Fatal(err)
	}
	// Fill the queue
	for i := 0; i < 3; i++ {
		d := net.Dialer{Timeout: 200 * time.Millisecond}
		if c, err := d.Dial("tcp", "127.0.0.2:"+port); err == nil {
			t.Cleanup(func() { c.Close() })
		}
	}
	d := net.Dialer{Timeout: 300 * time.Millisecond}
	if c, err := d.Dial("tcp", "127.0.0.2:"+port); err == nil {
		c.Close()
		t.Skip("could not make a connect hang")
	}
}

var (
	bad  = net.ParseIP("127.0.0.2")
	good = net.ParseIP("127.0.0.1")
)

func remoteIP(c net.Conn) string {
	h, _, _ := net.SplitHostPort(c.RemoteAddr().String())
	return h
}

// The first address connects but never finishes the TLS handshake: the
// next one is tried after handshakeStagger and wins.
func TestTLSRaceHandshakeStalls(t *testing.T) {
	port, cfg := tlsServer(t)
	silent(t, port)
	start := time.Now()
	c, err := dialTLSRace(context.Background(), &net.Dialer{Timeout: 15 * time.Second}, []net.IP{bad, good}, port, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	took := time.Since(start)
	t.Logf("handshake stall: connected to %s after %v", remoteIP(c), took.Round(time.Millisecond))
	if remoteIP(c) != "127.0.0.1" || took < handshakeStagger || took > handshakeStagger+2*time.Second {
		t.Errorf("%s after %v", remoteIP(c), took)
	}
}

// The first address never connects: the next one is tried after
// connectStagger, for TLS and for plain connections (the relay's dialer).
func TestRaceConnectHangs(t *testing.T) {
	port, cfg := tlsServer(t)
	hanging(t, port)
	start := time.Now()
	c, err := dialTLSRace(context.Background(), &net.Dialer{Timeout: 15 * time.Second}, []net.IP{bad, good}, port, cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	t.Logf("connect hangs, TLS: connected to %s after %v", remoteIP(c), time.Since(start).Round(time.Millisecond))
	if remoteIP(c) != "127.0.0.1" || time.Since(start) > time.Second {
		t.Errorf("TLS: %s after %v", remoteIP(c), time.Since(start))
	}
	old := lookupIP
	defer func() { lookupIP = old }()
	lookupIP = func(context.Context, string) ([]net.IP, error) { return []net.IP{bad, good}, nil }
	start = time.Now()
	c, err = DialChecked(context.Background(), "two.example", port, func(net.IP) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	t.Logf("connect hangs, checked dialer: connected to %s after %v", remoteIP(c), time.Since(start).Round(time.Millisecond))
	if remoteIP(c) != "127.0.0.1" || time.Since(start) > time.Second {
		t.Errorf("checked dialer: %s after %v", remoteIP(c), time.Since(start))
	}
}

// The resolver's order decides which family goes first.
func TestInterleave(t *testing.T) {
	v4a, v4b := net.ParseIP("192.0.2.1"), net.ParseIP("192.0.2.2")
	v6a, v6b := net.ParseIP("2001:db8::1"), net.ParseIP("2001:db8::2")
	for _, c := range []struct {
		in, want []net.IP
	}{
		{[]net.IP{v4a, v6a}, []net.IP{v4a, v6a}},
		{[]net.IP{v6a, v4a}, []net.IP{v6a, v4a}},
		{[]net.IP{v4a, v4b, v6a, v6b}, []net.IP{v4a, v6a, v4b, v6b}},
		{[]net.IP{v6a, v6b, v4a}, []net.IP{v6a, v4a, v6b}},
	} {
		got := interleave(c.in)
		for i := range got {
			if !got[i].Equal(c.want[i]) {
				t.Errorf("%v: %v", c.in, got)
				break
			}
		}
	}
	if _, err := dialTLSRace(context.Background(), &net.Dialer{}, nil, "443", &tls.Config{}); err == nil {
		t.Error("no address")
	}
	// Every address fails: the last error comes back
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()
	_, err := dialTLSRace(context.Background(), &net.Dialer{}, []net.IP{good}, port, &tls.Config{})
	var op *net.OpError
	if !errors.As(err, &op) {
		t.Errorf("closed port: %v", err)
	}
}
