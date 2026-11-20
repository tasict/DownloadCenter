package phpbox

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"downloadcenter/internal/netutil"
)

// The relay is the sandbox's only way out: an HTTP proxy in dcd (CONNECT
// tunnels and absolute-URI requests) that receives the connections a box
// opens to 127.0.0.1 inside its network namespace. Every target is checked
// with netutil's strict rules (public unicast addresses, ports 80 and 443)
// after resolution, and the checked address is the one connected to.
const (
	relayConns = 30       // connections a run may open
	relayBytes = 16 << 20 // bytes a run may move, both directions together
	relayIdle  = 20 * time.Second
	probeHost  = "probe.invalid" // answered by the relay itself during the probe
	probeBody  = "dcd-probe-ok"
)

var errBudget = errors.New("relay budget used up")

// relay holds what every session shares.
type relay struct {
	// proxy is the upstream proxy for a target ("http://host:port"), ""
	// for a direct connection: the rule of a regular user's request,
	// whoever runs the plugin. An error means no connection at all.
	proxy func(target string) (string, error)
	// ports are the target ports allowed (80 and 443; tests add one).
	ports map[string]bool
	// testDial answers some hosts in tests; nil in production.
	testDial func(ctx context.Context, host, port string) (net.Conn, bool)
}

func newRelay(proxy func(string) (string, error)) *relay {
	if proxy == nil {
		proxy = func(string) (string, error) { return "", nil }
	}
	return &relay{proxy: proxy, ports: map[string]bool{"80": true, "443": true}}
}

// RelayStats is what a run did through the relay.
type RelayStats struct {
	Conns   int   // connections accepted
	Refused int   // requests refused (target, port, budget)
	Bytes   int64 // bytes moved
	Probe   bool  // the probe's plain request arrived
	Connect bool  // the probe's CONNECT arrived
}

// session is the relay of one run.
type session struct {
	r     *relay
	ctx   context.Context
	stop  context.CancelFunc
	probe bool

	conns   atomic.Int32
	refused atomic.Int32
	bytes   atomic.Int64 // through the box's side
	upBytes atomic.Int64 // read from targets (bounds what dcd buffers)
	sawGet  atomic.Bool
	sawConn atomic.Bool

	mu   sync.Mutex
	open map[net.Conn]bool
	wg   sync.WaitGroup
}

func (r *relay) session(ctx context.Context, probe bool) *session {
	ctx, stop := context.WithCancel(ctx)
	return &session{r: r, ctx: ctx, stop: stop, probe: probe, open: map[net.Conn]bool{}}
}

func (s *session) stats() RelayStats {
	return RelayStats{Conns: int(s.conns.Load()), Refused: int(s.refused.Load()), Bytes: s.bytes.Load(),
		Probe: s.sawGet.Load(), Connect: s.sawConn.Load()}
}

// track keeps a connection to close with the session; false when the
// session is over.
func (s *session) track(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return false
	}
	s.open[c] = true
	return true
}

func (s *session) untrack(c net.Conn) {
	s.mu.Lock()
	delete(s.open, c)
	s.mu.Unlock()
	c.Close()
}

// close ends the session: every connection is closed.
func (s *session) close() {
	s.stop()
	s.mu.Lock()
	for c := range s.open {
		c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// serve handles a connection the box opened.
func (s *session) serve(c net.Conn) {
	if s.conns.Add(1) > relayConns || !s.track(c) {
		s.refused.Add(1)
		c.Close()
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.untrack(c)
		s.handle(&meter{Conn: c, s: s, ctr: &s.bytes, write: true})
	}()
}

// meter keeps a connection from idling and counts what goes through it
// against the run's budget. The box's side counts both directions: every
// byte the box sends or receives passes there once. A target's side counts
// what is read from it, so that what dcd reads before passing it on (an
// answer's headers) is bounded too.
type meter struct {
	net.Conn
	s     *session
	ctr   *atomic.Int64
	write bool // writes count too
}

func (m *meter) Read(p []byte) (int, error) {
	m.Conn.SetDeadline(time.Now().Add(relayIdle))
	n, err := m.Conn.Read(p)
	if m.ctr.Add(int64(n)) > relayBytes {
		m.s.stop()
		return n, errBudget
	}
	return n, err
}

func (m *meter) Write(p []byte) (int, error) {
	m.Conn.SetDeadline(time.Now().Add(relayIdle))
	if m.write && m.ctr.Add(int64(len(p))) > relayBytes {
		m.s.stop()
		return 0, errBudget
	}
	return m.Conn.Write(p)
}

// refuse answers a request the relay will not carry.
func (s *session) refuse(w io.Writer, status int) {
	s.refused.Add(1)
	io.WriteString(w, "HTTP/1.1 "+strconv.Itoa(status)+" "+http.StatusText(status)+"\r\nConnection: close\r\nContent-Length: 0\r\n\r\n")
}

func (s *session) handle(c net.Conn) {
	br := bufio.NewReaderSize(c, 4096)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	if req.Method == http.MethodConnect {
		s.connect(c, br, req)
		return
	}
	if !req.URL.IsAbs() || req.URL.Scheme != "http" || req.URL.Host == "" {
		s.refuse(c, http.StatusBadRequest)
		return
	}
	s.forward(c, req)
}

// target splits and checks host:port: an allowed port, no user name.
func (s *session) target(hostport, defPort string) (string, string, bool) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		host, port = hostport, defPort
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if host == "" || !s.r.ports[port] || strings.ContainsAny(host, "@/\\ ") {
		return "", "", false
	}
	return host, port, true
}

// dial connects to the target for the box: through the upstream proxy the
// rules choose, or directly; the target is checked either way and never
// reached any other way.
func (s *session) dial(host, port, scheme string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()
	if s.r.testDial != nil {
		if c, ok := s.r.testDial(ctx, host, port); ok {
			return c, nil
		}
	}
	proxy, err := s.r.proxy(scheme + "://" + net.JoinHostPort(host, port))
	if err != nil {
		return nil, err
	}
	if proxy == "" {
		return netutil.DialChecked(ctx, host, port, netutil.Strict)
	}
	pu, err := url.Parse(proxy)
	if err != nil {
		return nil, err
	}
	addr := host
	if pu.Scheme == "socks5h" {
		// The proxy resolves the name: only a literal address can be judged
		if ip := netutil.HostIP(host); ip != nil {
			if netutil.Strict(ip) {
				return nil, netutil.ErrNotAllowed
			}
		} else if l := strings.ToLower(strings.TrimSuffix(host, ".")); l == "localhost" || strings.HasSuffix(l, ".localhost") {
			return nil, netutil.ErrNotAllowed
		}
	} else {
		ips, err := netutil.ResolveChecked(ctx, host, netutil.Strict)
		if err != nil {
			return nil, err
		}
		addr = ips[0].String()
	}
	target := net.JoinHostPort(addr, port)
	switch pu.Scheme {
	case "socks5", "socks5h":
		user, pass := "", ""
		if pu.User != nil {
			user = pu.User.Username()
			pass, _ = pu.User.Password()
		}
		return netutil.DialSOCKS5(pu.Host, user, pass, target, 15*time.Second)
	case "http":
		return connectThrough(ctx, pu, target)
	}
	return nil, errors.New("unsupported proxy")
}

// connectThrough opens a tunnel to target through an HTTP proxy.
func connectThrough(ctx context.Context, pu *url.URL, target string) (net.Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", pu.Host)
	if err != nil {
		return nil, err
	}
	c.SetDeadline(time.Now().Add(15 * time.Second))
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: target}, Host: target, Header: http.Header{}}
	if pu.User != nil {
		pass, _ := pu.User.Password()
		req.SetBasicAuth(pu.User.Username(), pass)
		req.Header["Proxy-Authorization"] = req.Header["Authorization"]
		delete(req.Header, "Authorization")
	}
	if err := req.Write(c); err != nil {
		c.Close()
		return nil, err
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, req)
	if err != nil || resp.StatusCode != http.StatusOK || br.Buffered() > 0 {
		c.Close()
		if err == nil {
			err = errors.New("the proxy refused the tunnel")
		}
		return nil, err
	}
	c.SetDeadline(time.Time{})
	return c, nil
}

// connect handles a CONNECT tunnel (HTTPS).
func (s *session) connect(c net.Conn, br *bufio.Reader, req *http.Request) {
	host, port, ok := s.target(req.Host, "")
	if !ok {
		s.refuse(c, http.StatusForbidden)
		return
	}
	if s.probe && host == probeHost {
		s.sawConn.Store(true)
		io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
		return
	}
	up, err := s.dial(host, port, "https")
	if err != nil {
		s.refuse(c, http.StatusForbidden)
		return
	}
	if !s.track(up) {
		up.Close()
		return
	}
	defer s.untrack(up)
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	um := &meter{Conn: up, s: s, ctr: &s.upBytes}
	done := make(chan struct{}, 2)
	go func() {
		// What the box sent along with the CONNECT goes first
		if n := br.Buffered(); n > 0 {
			b, _ := br.Peek(n)
			um.Write(b)
		}
		io.Copy(um, c)
		up.Close()
		done <- struct{}{}
	}()
	go func() {
		io.Copy(c, up)
		c.Close()
		done <- struct{}{}
	}()
	<-done
	<-done
}

// hop are the headers that belong to one connection, not the request.
var hop = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade"}

func stripHop(h http.Header) {
	for _, f := range h.Values("Connection") {
		for _, name := range strings.Split(f, ",") {
			h.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range hop {
		h.Del(name)
	}
	for name := range h {
		if strings.HasPrefix(strings.ToLower(name), "proxy-") {
			delete(h, name)
		}
	}
}

// forward carries one plain HTTP request and its answer, then closes.
func (s *session) forward(c net.Conn, req *http.Request) {
	host, port, ok := s.target(req.URL.Host, "80")
	if !ok || req.URL.User != nil {
		s.refuse(c, http.StatusForbidden)
		return
	}
	if s.probe && host == probeHost {
		s.sawGet.Store(true)
		io.WriteString(c, "HTTP/1.1 200 OK\r\nConnection: close\r\nContent-Type: text/plain\r\nContent-Length: "+strconv.Itoa(len(probeBody))+"\r\n\r\n"+probeBody)
		return
	}
	up, err := s.dial(host, port, "http")
	if err != nil {
		s.refuse(c, http.StatusForbidden)
		return
	}
	if !s.track(up) {
		up.Close()
		return
	}
	defer s.untrack(up)
	um := &meter{Conn: up, s: s, ctr: &s.upBytes}
	out := req.Clone(s.ctx)
	out.RequestURI = ""
	stripHop(out.Header)
	out.Close = true
	if err := out.Write(um); err != nil {
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(um), out)
	if err != nil {
		s.refuse(c, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	stripHop(resp.Header)
	resp.Close = true
	resp.Write(c)
}
