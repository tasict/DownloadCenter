package phpbox

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeUpstream is a web server tests reach through the relay as
// search.test; nothing else is answered by the test hook.
type fakeUpstream struct {
	srv     *httptest.Server
	mu      sync.Mutex
	headers []http.Header
}

func newFakeUpstream(t *testing.T, h http.HandlerFunc) *fakeUpstream {
	f := &fakeUpstream{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.headers = append(f.headers, r.Header.Clone())
		f.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// hook answers search.test (port 80 or 443) with the fake upstream.
func (f *fakeUpstream) hook(ctx context.Context, host, port string) (net.Conn, bool) {
	if host != "search.test" {
		return nil, false
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", strings.TrimPrefix(f.srv.URL, "http://"))
	if err != nil {
		return nil, false
	}
	return c, true
}

// boxConn is a connection as the box opens it: one end goes to the
// session.
func boxConn(s *session) net.Conn {
	a, b := net.Pipe()
	s.serve(b)
	return a
}

func roundTrip(t *testing.T, s *session, raw string) (*http.Response, string) {
	t.Helper()
	c := boxConn(s)
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	go io.WriteString(c, raw)
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		return nil, ""
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func TestRelayForward(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Keep-Alive", "timeout=5")
		w.Header().Set("Proxy-Authenticate", "Basic")
		io.WriteString(w, "hello "+r.URL.Query().Get("q"))
	})
	r := newRelay(nil)
	r.testDial = up.hook
	s := r.session(context.Background(), false)
	defer s.close()
	resp, body := roundTrip(t, s, "GET http://search.test/s?q=ubuntu HTTP/1.1\r\nHost: search.test\r\nProxy-Authorization: Basic eDp5\r\n"+
		"Proxy-Connection: keep-alive\r\nConnection: X-Secret\r\nX-Secret: 1\r\nX-Plain: 2\r\n\r\n")
	if resp == nil || resp.StatusCode != 200 || body != "hello ubuntu" {
		t.Fatalf("%v %q", resp, body)
	}
	if resp.Header.Get("Keep-Alive") != "" || resp.Header.Get("Proxy-Authenticate") != "" {
		t.Errorf("answer headers %v", resp.Header)
	}
	h := up.headers[0]
	if h.Get("Proxy-Authorization") != "" || h.Get("Proxy-Connection") != "" || h.Get("X-Secret") != "" || h.Get("X-Plain") != "2" {
		t.Errorf("forwarded headers %v", h)
	}
	// Not a proxy request
	if resp, _ := roundTrip(t, s, "GET /s HTTP/1.1\r\nHost: search.test\r\n\r\n"); resp == nil || resp.StatusCode != 400 {
		t.Errorf("origin form: %v", resp)
	}
	if resp, _ := roundTrip(t, s, "GET ftp://search.test/x HTTP/1.1\r\nHost: search.test\r\n\r\n"); resp == nil || resp.StatusCode != 400 {
		t.Errorf("ftp: %v", resp)
	}
}

func TestRelayConnect(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "tunnelled") })
	r := newRelay(nil)
	r.testDial = up.hook
	s := r.session(context.Background(), false)
	defer s.close()
	c := boxConn(s)
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	go io.WriteString(c, "CONNECT search.test:443 HTTP/1.1\r\nHost: search.test:443\r\n\r\n")
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: "CONNECT"})
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("%v %v", resp, err)
	}
	go io.WriteString(c, "GET / HTTP/1.1\r\nHost: search.test\r\nConnection: close\r\n\r\n")
	resp, err = http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "tunnelled" {
		t.Errorf("%q", b)
	}
}

// spec: the relay refuses the LAN, the NAS, loopback and other ports, and
// the refused target sees nothing
func TestRelayRefuses(t *testing.T) {
	var hits atomic.Int32
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			hits.Add(1)
			c.Close()
		}
	}()
	_, lp, _ := net.SplitHostPort(ln.Addr().String())
	r := newRelay(nil)
	// The listener's port is allowed here so that only the address is judged
	r.ports[lp] = true
	targets := []string{"127.0.0.1:" + lp, "localhost:" + lp, "2130706433:" + lp, "[::1]:" + lp, "0.0.0.0:" + lp,
		"127.0.0.1:8080", "192.168.1.1:80", "10.0.0.1:443", "172.16.5.4:80", "100.64.1.1:80", "169.254.169.254:80",
		"[fd00::1]:80", "[::ffff:127.0.0.1]:" + lp, "example.com:22", "example.com:8080", "example.com:25"}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() {
				targets = append(targets, net.JoinHostPort(ipn.IP.String(), lp))
			}
		}
	}
	refused := 0
	for _, target := range targets {
		// A run opens at most 30 connections: one run per target
		s := r.session(context.Background(), false)
		host := target
		if h, _, err := net.SplitHostPort(target); err == nil {
			host = h
		}
		if resp, _ := roundTrip(t, s, "GET http://"+target+"/ HTTP/1.1\r\nHost: "+host+"\r\n\r\n"); resp == nil || resp.StatusCode != 403 {
			t.Errorf("GET %s: %v", target, resp)
		}
		if resp, _ := roundTrip(t, s, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n"); resp == nil || resp.StatusCode != 403 {
			t.Errorf("CONNECT %s: %v", target, resp)
		}
		refused += s.stats().Refused
		s.close()
	}
	if hits.Load() != 0 {
		t.Errorf("the listener was reached %d times", hits.Load())
	}
	if refused != 2*len(targets) {
		t.Errorf("refused %d of %d", refused, 2*len(targets))
	}
}

// spec: the proxy is the regular user's; without one usable, nothing goes
// out directly
func TestRelayProxy(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := ln.Addr().String()
	ln.Close()
	var asked []string
	r := newRelay(func(target string) (string, error) {
		asked = append(asked, target)
		if strings.Contains(target, "93.184.216.35") {
			return "", io.ErrUnexpectedEOF
		}
		if strings.Contains(target, "93.184.216.36") {
			return "socks5h://" + closed, nil
		}
		return "http://" + closed, nil
	})
	s := r.session(context.Background(), false)
	defer s.close()
	start := time.Now()
	for _, target := range []string{"93.184.216.34:80", "93.184.216.35:443", "93.184.216.36:443"} {
		if resp, _ := roundTrip(t, s, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n"); resp == nil || resp.StatusCode != 403 {
			t.Errorf("%s: %v", target, resp)
		}
	}
	// A proxy that resolves names gets literal addresses checked
	if resp, _ := roundTrip(t, s, "CONNECT 10.0.0.1:443 HTTP/1.1\r\nHost: 10.0.0.1:443\r\n\r\n"); resp == nil || resp.StatusCode != 403 {
		t.Errorf("10.0.0.1: %v", resp)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("took %v: something tried to connect directly", time.Since(start))
	}
	if len(asked) != 4 || asked[0] != "https://93.184.216.34:80" {
		t.Errorf("asked %v", asked)
	}
}

// spec: 30 connections and 16 MiB a run
func TestRelayLimits(t *testing.T) {
	big := strings.Repeat("x", 1<<20)
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 20; i++ {
			if _, err := io.WriteString(w, big); err != nil {
				return
			}
		}
	})
	r := newRelay(nil)
	r.testDial = up.hook
	s := r.session(context.Background(), false)
	defer s.close()
	resp, body := roundTrip(t, s, "GET http://search.test/big HTTP/1.1\r\nHost: search.test\r\n\r\n")
	if resp == nil || len(body) >= 20<<20 || len(body) > relayBytes {
		t.Errorf("big answer: %v, %d bytes", resp, len(body))
	}
	if st := s.stats(); st.Bytes < relayBytes-1<<20 || st.Bytes > relayBytes+1<<20 {
		t.Errorf("bytes %d", st.Bytes)
	}
	// The budget is spent: the run gets nothing more
	if resp, _ := roundTrip(t, s, "GET http://search.test/small HTTP/1.1\r\nHost: search.test\r\n\r\n"); resp != nil && resp.StatusCode == 200 {
		t.Error("request after the budget")
	}

	s2 := r.session(context.Background(), false)
	defer s2.close()
	for i := 0; i < relayConns; i++ {
		c := boxConn(s2)
		defer c.Close()
	}
	c := boxConn(s2)
	c.SetDeadline(time.Now().Add(2 * time.Second))
	go io.WriteString(c, "GET http://search.test/ HTTP/1.1\r\nHost: search.test\r\n\r\n")
	if _, err := http.ReadResponse(bufio.NewReader(c), nil); err == nil {
		t.Error("31st connection served")
	}
	if st := s2.stats(); st.Conns != relayConns+1 || st.Refused != 1 {
		t.Errorf("%+v", st)
	}
}
