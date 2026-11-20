package search

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"downloadcenter/internal/netutil"
)

func reason(err error) string {
	var f *Failure
	if errors.As(err, &f) {
		return f.Reason
	}
	if err == nil {
		return ""
	}
	return "not a Failure: " + err.Error()
}

func TestFetchBasics(t *testing.T) {
	n := newFakeNet()
	n.handle("src.example.org", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "track", Value: "1"})
		switch r.URL.Path {
		case "/ok":
			w.Write([]byte("hello"))
		case "/big":
			w.Write(make([]byte, limitSearch+10))
		case "/away":
			http.Redirect(w, r, "https://other.example.org/landed?x=1", http.StatusFound)
		case "/magnet":
			w.Header().Set("Location", "magnet:?xt=urn:btih:"+hashA+"&dn=x")
			w.WriteHeader(http.StatusMovedPermanently)
		case "/slow":
			<-r.Context().Done()
		case "/chain3", "/chain4":
			hops := 3
			if r.URL.Path == "/chain4" {
				hops = 4
			}
			http.Redirect(w, r, fmt.Sprintf("https://src.example.org/hop?left=%d", hops-1), http.StatusFound)
		case "/hop":
			left := r.URL.Query().Get("left")
			if left == "0" {
				w.Write([]byte("end"))
				return
			}
			var k int
			fmt.Sscan(left, &k)
			http.Redirect(w, r, fmt.Sprintf("https://src.example.org/hop?left=%d", k-1), http.StatusFound)
		case "/ftp":
			http.Redirect(w, r, "ftp://src.example.org/x", http.StatusFound)
		}
	})
	n.handle("other.example.org", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("other")) })
	f := n.fetcher()
	f.timeout = 300 * time.Millisecond
	ctx := context.Background()
	get := func(path string, limit int64) (*response, error) {
		return f.fetch(ctx, request{url: "https://src.example.org" + path + "?q=private+keyword&key=SECRET",
			headers: map[string]string{"X-Api-Key": "SECRET"}, accept: "application/json", limit: limit})
	}

	resp, err := get("/ok", limitSearch)
	if err != nil || string(resp.Body) != "hello" || resp.Status != 200 {
		t.Fatalf("%+v %v", resp, err)
	}
	r := n.requests()[0]
	if r.Method != "GET" || r.Header.Get("User-Agent") != "DownloadCenter/9.9.9 (+https://github.com/tasict/DownloadCenter)" ||
		r.Header.Get("Accept") != "application/json" || r.Header.Get("X-Api-Key") != "SECRET" || r.Header.Get("Cookie") != "" {
		t.Errorf("request headers %v", r.Header)
	}

	// spec: an answer over the limit fails
	if _, err := get("/big", limitSearch); reason(err) != ReasonTooLarge {
		t.Errorf("big: %v", err)
	}
	// spec: a source that does not answer in time fails as timed out
	start := time.Now()
	if _, err := get("/slow", limitSearch); reason(err) != ReasonTimeout {
		t.Errorf("slow: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("timeout took %v", time.Since(start))
	}
	// A redirect to a magnet link is handed back, not followed
	resp, err = get("/magnet", limitSearch)
	if err != nil || !strings.HasPrefix(resp.Magnet, "magnet:?xt=urn:btih:"+hashA) {
		t.Errorf("magnet: %+v %v", resp, err)
	}
	// Three redirects are followed, a fourth is not; only to http(s)
	if resp, err := get("/chain3", limitSearch); err != nil || string(resp.Body) != "end" {
		t.Errorf("3 redirects: %+v %v", resp, err)
	}
	if _, err := get("/chain4", limitSearch); reason(err) != ReasonConnect {
		t.Errorf("4 redirects: %v", err)
	}
	if _, err := get("/ftp", limitSearch); reason(err) != ReasonConnect {
		t.Errorf("ftp redirect: %v", err)
	}

	// On another host: no plugin header, no Referer, no cookie
	n.seen = nil
	resp, err = get("/away", limitSearch)
	if err != nil || string(resp.Body) != "other" || !strings.HasPrefix(resp.URL, "https://other.example.org/landed") {
		t.Fatalf("away: %+v %v", resp, err)
	}
	hop := n.requests()[1]
	if hop.URL.Host != "other.example.org" || hop.Header.Get("X-Api-Key") != "" || hop.Header.Get("Referer") != "" ||
		hop.Header.Get("Cookie") != "" || hop.Header.Get("User-Agent") == "" {
		t.Errorf("redirected request headers %v", hop.Header)
	}
	// On the same host the redirect keeps them, still without a Referer
	n.seen = nil
	get("/chain3", limitSearch)
	for _, r := range n.requests() {
		if r.Header.Get("X-Api-Key") != "SECRET" || r.Header.Get("Referer") != "" {
			t.Errorf("%s: %v", r.URL.Path, r.Header)
		}
	}
}

// A request to an address on the NAS never reaches it, whoever asks and
// however the address is reached.
func TestFetchGuard(t *testing.T) {
	var hits atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte("internal"))
	}))
	defer local.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(local.URL, "http://"))

	n := newFakeNet()
	targets := []string{
		"http://127.0.0.1:" + port + "/",
		"http://localhost:" + port + "/",
		"http://[::1]:" + port + "/",
		"http://169.254.169.254/latest/meta-data/",
		"http://2130706433:" + port + "/",
		"http://0.0.0.0:" + port + "/",
	}
	// The NAS's own addresses (here, the machine running the test)
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
				targets = append(targets, "http://"+ipn.IP.String()+":"+port+"/")
				break
			}
		}
	}
	// A name for one of them in the hosts file (resolved without DNS): the
	// guard checks the address it resolves to
	if name := localName(); name != "" {
		targets = append(targets, "http://"+name+":"+port+"/")
	} else {
		t.Log("no local name in the hosts file besides localhost")
	}
	for _, target := range targets {
		u, _ := url.Parse(target)
		n.through[u.Host] = true
	}
	n.handle("src.example.org", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Query().Get("to"), http.StatusFound)
	})
	f := n.fetcher()
	for _, target := range targets {
		// Asked directly
		_, err := f.fetch(context.Background(), request{url: target, limit: limitSearch})
		if reason(err) != ReasonNotAllowed {
			t.Errorf("%s: %v", target, err)
		}
		// Through a redirect of a public source
		_, err = f.fetch(context.Background(), request{url: "https://src.example.org/r?to=" + url.QueryEscape(target), limit: limitSearch})
		if reason(err) != ReasonNotAllowed {
			t.Errorf("redirect to %s: %v", target, err)
		}
	}
	// The transport fetch uses checks a name after resolving it (here
	// localhost, which the hosts file resolves), and its error is
	// recognised
	req, _ := http.NewRequest("GET", "http://localhost:"+port+"/", nil)
	if _, err := netutil.Transport(fetchTimeout, true, "").RoundTrip(req); err == nil || classify(context.Background(), err).Reason != ReasonNotAllowed {
		t.Errorf("transport: %v", err)
	}
	if hits.Load() != 0 {
		t.Errorf("the local server was reached %d times", hits.Load())
	}
}

// localName is a name in /etc/hosts, other than localhost, for a loopback
// address or one of this machine's.
func localName() string {
	b, err := os.ReadFile("/etc/hosts")
	if err != nil {
		return ""
	}
	own := map[string]bool{}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				own[ipn.IP.String()] = true
			}
		}
	}
	// Every address the name has must be local, or the test would connect
	// somewhere else
	local := map[string]bool{}
	var names []string
	for _, line := range strings.Split(string(b), "\n") {
		line, _, _ = strings.Cut(line, "#")
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		ip := net.ParseIP(f[0])
		for _, name := range f[1:] {
			l := strings.ToLower(name)
			if l == "localhost" || strings.HasSuffix(l, ".localhost") || net.ParseIP(name) != nil {
				continue
			}
			ok, seen := local[l]
			if !seen {
				names = append(names, l)
				ok = true
			}
			local[l] = ok && ip != nil && (ip.IsLoopback() || own[ip.String()])
		}
	}
	for _, n := range names {
		if local[n] {
			return n
		}
	}
	return ""
}

func TestStatusFailure(t *testing.T) {
	cases := []struct {
		status int
		key    bool
		want   string
	}{
		{429, false, ReasonBusy},
		{503, true, ReasonBusy},
		{401, true, ReasonRefused},
		{403, true, ReasonRefused},
		{403, false, "The source answered with HTTP 403"},
		{500, true, "The source answered with HTTP 500"},
	}
	for _, c := range cases {
		if got := statusFailure(c.status, c.key).Reason; got != c.want {
			t.Errorf("%d: %q", c.status, got)
		}
	}
}

func TestRedact(t *testing.T) {
	got := Redact("Bad q=Private Keyword for http://h/x?apikey=SECRET; key SECRET%2F1, see ?a=b", []string{"SECRET/1", "private keyword", "SECRET"})
	for _, bad := range []string{"Private", "Keyword", "SECRET", "http", "a=b"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q in %q", bad, got)
		}
	}
	te := &TorznabError{Code: 201, Description: "Incorrect parameter: q=private keyword apikey=SECRET\n\x07"}
	f := torznabReason(te, []string{"SECRET", "private keyword"})
	if strings.Contains(f.Reason, "SECRET") || strings.Contains(f.Reason, "private") || strings.Contains(f.Reason, "\x07") ||
		!strings.HasPrefix(f.Reason, "The source reported an error: Incorrect parameter") {
		t.Errorf("%q", f.Reason)
	}
	if f := torznabReason(&TorznabError{Code: 100, Description: "Invalid API Key"}, nil); f.Reason != ReasonRefused {
		t.Errorf("%q", f.Reason)
	}
	long := torznabReason(&TorznabError{Code: 900, Description: strings.Repeat("x", 500)}, nil)
	if len([]rune(long.Reason)) > 200 {
		t.Errorf("%d characters", len([]rune(long.Reason)))
	}
}

// spec: requests use the proxy the regular-user rule gives; a proxy that
// cannot be used or reached fails the source, never a direct connection
func TestFetchProxy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	ln.Close()
	target := "http://203.0.113.10/api?t=search"
	var asked []string
	n := newFakeNet()
	n.through["203.0.113.10"] = true
	f := n.fetcher()
	f.proxy = func(raw string) (string, error) {
		asked = append(asked, raw)
		return "http://" + closed, nil
	}
	start := time.Now()
	_, err = f.fetch(context.Background(), request{url: target, limit: limitSearch})
	if reason(err) != ReasonConnect || time.Since(start) > 5*time.Second {
		t.Errorf("unreachable proxy: %v after %v", err, time.Since(start))
	}
	if len(asked) != 1 || asked[0] != target {
		t.Errorf("proxy asked for %v", asked)
	}
	f.proxy = func(string) (string, error) {
		return "", errors.New("The administrator requires regular users to use a proxy")
	}
	if _, err := f.fetch(context.Background(), request{url: target, limit: limitSearch}); reason(err) != ReasonNoProxy {
		t.Errorf("proxy required: %v", err)
	}
}

// spec: every redirect is sent with the proxy chosen for its own address:
// a hop for which none can be used fails, it never goes direct
func TestFetchProxyPerHop(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	ln.Close()
	n := newFakeNet()
	n.through["203.0.113.10"] = true
	// The first host answers in the process (no proxy: the site rules send
	// it direct); its redirect goes to a host that must use a proxy
	n.handle("first.example.org", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://203.0.113.10/next", http.StatusFound)
	})
	f := n.fetcher()
	var asked []string
	f.proxy = func(raw string) (string, error) {
		asked = append(asked, raw)
		if strings.Contains(raw, "203.0.113.10") {
			return "", errors.New("The administrator requires regular users to use a proxy")
		}
		return "", nil
	}
	if _, err := f.fetch(context.Background(), request{url: "https://first.example.org/s", limit: limitSearch}); reason(err) != ReasonNoProxy {
		t.Errorf("redirect without a usable proxy: %v", err)
	}
	f.proxy = func(raw string) (string, error) {
		asked = append(asked, raw)
		if strings.Contains(raw, "203.0.113.10") {
			return "http://" + closed, nil
		}
		return "", nil
	}
	start := time.Now()
	if _, err := f.fetch(context.Background(), request{url: "https://first.example.org/s", limit: limitSearch}); reason(err) != ReasonConnect || time.Since(start) > 5*time.Second {
		t.Errorf("redirect through its proxy: %v", err)
	}
	if got := strings.Join(asked, " "); got != "http://203.0.113.10/next http://203.0.113.10/next" {
		t.Errorf("proxy asked for %s", got)
	}
}

// spec: the plugin's headers go to its own host and scheme only: not after
// a redirect to http on the same host
func TestFetchHeadersOnDowngrade(t *testing.T) {
	n := newFakeNet()
	var plainKey string
	n.handle("src.example.org", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Scheme == "https" || r.TLS != nil || r.URL.Path == "/s" {
			http.Redirect(w, r, "http://src.example.org/plain", http.StatusFound)
			return
		}
		plainKey = r.Header.Get("X-Api-Key")
		w.Write([]byte("ok"))
	})
	f := n.fetcher()
	if _, err := f.fetch(context.Background(), request{url: "https://src.example.org/s", headers: map[string]string{"X-Api-Key": "SECRET"}, limit: limitSearch}); err != nil {
		t.Fatal(err)
	}
	if plainKey != "" {
		t.Errorf("the key went over http: %q", plainKey)
	}
	reqs := n.requests()
	if len(reqs) != 2 || reqs[0].Header.Get("X-Api-Key") != "SECRET" {
		t.Errorf("%d requests", len(reqs))
	}
}

// spec: a proxy that refuses the tunnel a checked request needs fails the
// source with the proxy reason; the proxy is not asked by name
func TestFetchTunnelRefused(t *testing.T) {
	var asked []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.Method+" "+r.Host)
		if r.Method == "CONNECT" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Write([]byte("by name"))
	}))
	defer proxy.Close()
	n := newFakeNet()
	n.through["203.0.113.10"] = true
	f := n.fetcher()
	f.proxy = func(string) (string, error) { return proxy.URL, nil }
	if _, err := f.fetch(context.Background(), request{url: "http://203.0.113.10/api", limit: limitSearch}); reason(err) != ReasonNoProxy {
		t.Errorf("%v", err)
	}
	if strings.Join(asked, " ") != "CONNECT 203.0.113.10:80" {
		t.Errorf("the proxy was asked for %v", asked)
	}
}
