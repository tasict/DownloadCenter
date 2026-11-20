package netutil

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubLookup answers names from a table for the test's duration.
func stubLookup(t *testing.T, answers map[string][]string) {
	t.Helper()
	old := lookupIP
	t.Cleanup(func() { lookupIP = old })
	lookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
		a, ok := answers[host]
		if !ok {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		var out []net.IP
		for _, s := range a {
			out = append(out, net.ParseIP(s))
		}
		return out, nil
	}
}

// recordingProxy is an http proxy that notes what it is asked for and
// answers plain requests itself. Tunnels it refuses, or with backend set
// connects to backend whatever the target.
type recordingProxy struct {
	mu      sync.Mutex
	seen    []string // "GET <request target> Host=<host>" or "CONNECT <target>"
	srv     *httptest.Server
	backend string
}

func newRecordingProxy(t *testing.T) *recordingProxy {
	p := &recordingProxy{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		if r.Method == "CONNECT" {
			p.seen = append(p.seen, "CONNECT "+r.Host)
		} else {
			p.seen = append(p.seen, r.Method+" "+r.RequestURI+" Host="+r.Host)
		}
		backend := p.backend
		p.mu.Unlock()
		if r.Method == "CONNECT" {
			if backend == "" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			up, err := net.Dial("tcp", backend)
			if err != nil {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			c, brw, _ := w.(http.Hijacker).Hijack()
			brw.WriteString("HTTP/1.1 200 Connection established\r\n\r\n")
			brw.Flush()
			go func() { io.Copy(up, c); up.Close() }()
			io.Copy(c, up)
			c.Close()
			return
		}
		io.WriteString(w, "proxied")
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *recordingProxy) requests() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}

func (p *recordingProxy) reset() {
	p.mu.Lock()
	p.seen = nil
	p.mu.Unlock()
}

// spec: through an http proxy a guarded request goes in a tunnel to the
// address that was checked, not by a name the proxy would resolve again
func TestGuardPinsHTTPProxy(t *testing.T) {
	stubLookup(t, map[string][]string{"public.example": {"93.184.216.34"}, "inside.example": {"127.0.0.1"}})
	var got []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.RequestURI+" Host="+r.Host)
		io.WriteString(w, "origin")
	}))
	defer backend.Close()
	p := newRecordingProxy(t)
	p.backend = strings.TrimPrefix(backend.URL, "http://")
	cl := &http.Client{Transport: Transport(5*time.Second, true, p.srv.URL), Timeout: 5 * time.Second}
	resp, err := cl.Get("http://public.example/file?x=1")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "origin" || resp.Request.URL.Host != "public.example" || strings.Join(got, "") != "GET /file?x=1 Host=public.example" {
		t.Errorf("through the tunnel: %q %s %v", b, resp.Request.URL, got)
	}
	if _, err := cl.Get("http://inside.example/"); err == nil || !strings.Contains(err.Error(), "is not allowed") {
		t.Errorf("inside: %v", err)
	}
	// https: the tunnel goes to the address, the handshake is made with the
	// name (this backend speaks no TLS)
	if _, err := cl.Get("https://public.example/file"); err == nil {
		t.Error("https to a plain server worked")
	}
	if g := strings.Join(p.requests(), " | "); g != "CONNECT 93.184.216.34:80 | CONNECT 93.184.216.34:443" {
		t.Errorf("the proxy was asked for %s", g)
	}

	// A proxy that only tunnels to 443: a checked plain http request fails
	// with a clear reason; the proxy never gets the name to resolve
	p.backend = ""
	for _, cl := range []*http.Client{
		{Transport: Transport(5*time.Second, true, p.srv.URL), Timeout: 5 * time.Second},
		{Transport: TransportRule(5*time.Second, p.srv.URL, &Rule{Strict: func(string) bool { return true }}), Timeout: 5 * time.Second},
	} {
		p.reset()
		for i := 0; i < 2; i++ {
			_, err := cl.Get("http://public.example/file?x=1")
			if !errors.Is(err, ErrTunnelRefused) || !strings.Contains(err.Error(), "port 80") {
				t.Errorf("refused tunnel: %v", err)
			}
		}
		if g := strings.Join(p.requests(), " | "); g != "CONNECT 93.184.216.34:80 | CONNECT 93.184.216.34:80" {
			t.Errorf("the proxy was asked for %s", g)
		}
	}

	// Without the guard the name goes to the proxy as before
	p.reset()
	cl = &http.Client{Transport: Transport(5*time.Second, false, p.srv.URL), Timeout: 5 * time.Second}
	if resp, err := cl.Get("http://inside.example/file"); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
	}
	if g := strings.Join(p.requests(), " | "); g != "GET http://inside.example/file Host=inside.example" {
		t.Errorf("unguarded: %s", g)
	}
}

// socksTarget runs a SOCKS5 proxy that notes the target of one CONNECT and
// refuses it.
func socksTarget(t *testing.T) (string, <-chan string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				h := make([]byte, 2)
				io.ReadFull(br, h)
				io.ReadFull(br, make([]byte, h[1]))
				c.Write([]byte{5, 0})
				req := make([]byte, 4)
				io.ReadFull(br, req)
				var target string
				switch req[3] {
				case 1:
					b := make([]byte, 4)
					io.ReadFull(br, b)
					target = net.IP(b).String()
				case 4:
					b := make([]byte, 16)
					io.ReadFull(br, b)
					target = net.IP(b).String()
				case 3:
					l := make([]byte, 1)
					io.ReadFull(br, l)
					b := make([]byte, l[0])
					io.ReadFull(br, b)
					target = "name:" + string(b)
				}
				pb := make([]byte, 2)
				io.ReadFull(br, pb)
				got <- target + ":" + itoa(int(binary.BigEndian.Uint16(pb)))
				c.Write([]byte{5, 2, 0, 1, 0, 0, 0, 0, 0, 0})
			}(c)
		}
	}()
	return ln.Addr().String(), got
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// spec: a socks5 proxy is handed the checked address; socks5h resolves names
// itself, so only literal addresses are judged there
func TestGuardPinsSOCKS5(t *testing.T) {
	stubLookup(t, map[string][]string{"public.example": {"93.184.216.34"}})
	addr, got := socksTarget(t)
	cl := &http.Client{Transport: Transport(5*time.Second, true, "socks5://"+addr), Timeout: 5 * time.Second}
	cl.Get("http://public.example:8080/")
	if g := <-got; g != "93.184.216.34:8080" {
		t.Errorf("socks5 was asked for %s", g)
	}
	cl = &http.Client{Transport: Transport(5*time.Second, true, "socks5h://"+addr), Timeout: 5 * time.Second}
	cl.Get("http://public.example:8080/")
	if g := <-got; g != "name:public.example:8080" {
		t.Errorf("socks5h was asked for %s", g)
	}
	if _, err := cl.Get("http://127.1:8080/"); err == nil || !strings.Contains(err.Error(), "is not allowed") {
		t.Errorf("literal through socks5h: %v", err)
	}
}

// spec: a proxy setting that cannot be used fails the request; it never
// goes direct
func TestBadProxyFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "direct") }))
	defer srv.Close()
	for _, p := range []string{"http://[::1", "socks4://127.0.0.1:1080", "ftp://127.0.0.1:21", "http://", "://x"} {
		for _, guard := range []bool{false, true} {
			cl := &http.Client{Transport: Transport(5*time.Second, guard, p), Timeout: 5 * time.Second}
			resp, err := cl.Get(srv.URL)
			if err == nil {
				resp.Body.Close()
				t.Errorf("%q (guard %v) went direct", p, guard)
			} else if !errors.Is(err, ErrBadProxy) {
				t.Errorf("%q: %v", p, err)
			}
		}
	}
}

// spec: the strict rules for some hosts, the usual guard for the others
func TestRule(t *testing.T) {
	stubLookup(t, map[string][]string{
		"public.example": {"93.184.216.34"},
		"mixed.example":  {"93.184.216.34", "192.168.1.1"},
		"lan.example":    {"192.168.1.20"},
		"loop.example":   {"127.0.0.1", "192.168.1.20"},
	})
	r := &Rule{Strict: func(h string) bool { return h != "lan.example" && h != "192.168.1.20" }}
	for _, c := range []struct {
		host, port string
		ok         bool
	}{
		{"public.example", "443", true}, {"public.example", "8080", false}, {"mixed.example", "443", false},
		{"10.0.0.1", "80", false}, {"100.64.0.1", "80", false}, {"[::ffff:0:a00:1]", "80", false}, {"8.8.8.8", "80", true},
		{"localhost", "80", false}, {"lan.example", "9117", true}, {"192.168.1.20", "9117", true}, {"loop.example", "80", false},
	} {
		host := strings.Trim(c.host, "[]")
		_, err := r.resolve(context.Background(), host, c.port)
		if (err == nil) != c.ok {
			t.Errorf("%s:%s: %v", c.host, c.port, err)
		}
		if err != nil && !errors.Is(err, ErrNotAllowed) && !strings.Contains(err.Error(), "no address") {
			t.Errorf("%s: %v", c.host, err)
		}
	}
	// The usual guard leaves out forbidden addresses and keeps the others
	if ips, err := Guard.resolve(context.Background(), "loop.example", "80"); err != nil || len(ips) != 1 || ips[0].String() != "192.168.1.20" {
		t.Errorf("guard: %v %v", ips, err)
	}
	if err := r.CheckLiteral("public.example", "22"); err == nil {
		t.Error("port 22 of a strict host")
	}
	if err := r.CheckLiteral("lan.example", "22"); err != nil {
		t.Errorf("port 22 of the other host: %v", err)
	}
}

// spec: the IPv4-translated range leads to no public host
func TestStrictTranslated(t *testing.T) {
	for _, s := range []string{"::ffff:0:a00:1", "::ffff:0:808:808", "::ffff:0:0:0"} {
		if !Strict(net.ParseIP(s)) {
			t.Errorf("%s allowed", s)
		}
	}
	if Strict(net.ParseIP("::ffff:8.8.8.8")) {
		t.Error("an IPv4-mapped public address is that public address")
	}
}
