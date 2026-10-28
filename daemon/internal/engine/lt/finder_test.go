package lt

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"downloadcenter/internal/engine"
	"downloadcenter/internal/torrent"
)

// tracker is a local HTTP tracker on every interface that records the info
// hashes announced to it and answers with one peer nobody can reach.
type tracker struct {
	srv  *httptest.Server
	mu   sync.Mutex
	seen map[string]int
}

func newTracker(t *testing.T) *tracker {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	tr := &tracker{seen: map[string]int{}}
	tr.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tr.mu.Lock()
		tr.seen[r.URL.Query().Get("info_hash")]++
		tr.mu.Unlock()
		// 10.1.11.250:6881, a LAN address nobody answers on: known, never
		// connected (libtorrent drops global peers a loopback tracker names)
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("d8:intervali1800e5:peers6:\x0a\x01\x0b\xfa\x1a\xe1e"))
	}))
	tr.srv.Listener.Close()
	tr.srv.Listener = ln
	tr.srv.Start()
	return tr
}

func (tr *tracker) port() string {
	_, p, _ := net.SplitHostPort(tr.srv.Listener.Addr().String())
	return p
}

func (tr *tracker) count(infohash string) int {
	raw, _ := hexBytes(infohash)
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.seen[string(raw)]
}

func hexBytes(s string) ([]byte, error) {
	out := make([]byte, len(s)/2)
	for i := range out {
		var b byte
		for _, c := range s[2*i : 2*i+2] {
			b <<= 4
			switch {
			case c >= '0' && c <= '9':
				b |= byte(c - '0')
			case c >= 'a' && c <= 'f':
				b |= byte(c - 'a' + 10)
			}
		}
		out[i] = b
	}
	return out, nil
}

func testTorrent(t *testing.T, name, announce string) ([]byte, string) {
	tor := []byte("d8:announce" + itoa(len(announce)) + ":" + announce +
		"4:infod6:lengthi16384e4:name" + itoa(len(name)) + ":" + name + "12:piece lengthi16384e6:pieces20:" + strings.Repeat("x", 20) + "ee")
	meta, err := torrent.Parse(tor)
	if err != nil {
		t.Fatal(err)
	}
	return tor, meta.InfoHash
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	for i := 0; i < 100; i++ {
		if ok() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", what)
}

// ownName is this machine's host name when it resolves to one of its own
// addresses (QTS lists it in /etc/hosts), else "".
func ownName() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	addrs, err := net.LookupHost(h)
	if err != nil || len(addrs) == 0 {
		return ""
	}
	ifs, _ := net.InterfaceAddrs()
	for _, a := range ifs {
		if ip, _, err := net.ParseCIDR(a.String()); err == nil && ip.String() == addrs[0] && !ip.IsLoopback() {
			return h
		}
	}
	return ""
}

// dc-bt 1.4: peer counts, private, refresh_peers, set_trackers and the
// address guard, against a real dc-bt and local trackers.
func TestRealPeerFinder(t *testing.T) {
	bin := dcbtBin(t)
	a, b := newTracker(t), newTracker(t)
	defer a.srv.Close()
	defer b.srv.Close()
	dir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	e := newEngine(bin, dir)
	e.ApplyGlobal(engine.Global{PortFrom: 26901, PortTo: 26909})
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	if !e.Caps().PeerRefresh {
		t.Skip("dc-bt older than 1.4")
	}
	data := filepath.Join(dir, "data")

	// An admin's torrent announces to a tracker on the NAS itself
	torA, hashA := testTorrent(t, "a", "http://127.0.0.1:"+a.port()+"/announce")
	if _, err := e.Add(hashA, engine.AddRequest{Torrent: torA, Dir: data}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "announce of a", func() bool { return a.count(hashA) > 0 })
	var st *engine.Status
	waitUntil(t, "known peer", func() bool {
		st, _ = e.Status(hashA)
		return st != nil && st.KnownPeers > 0
	})
	if st.Private || st.Connections != 0 {
		t.Fatalf("status %+v", st)
	}
	// No connection: the peers are forgotten and the tracker asked again
	before := a.count(hashA)
	ok, err := e.RefreshPeers(hashA)
	if err != nil || !ok {
		t.Fatalf("refresh_peers %v %v", ok, err)
	}
	waitUntil(t, "announce after refresh_peers", func() bool { return a.count(hashA) > before })

	// set_trackers replaces the list and announces to the new one
	if err := e.SetTrackers(hashA, []string{"http://127.0.0.1:" + b.port() + "/announce"}); err != nil {
		t.Fatal(err)
	}
	trs, _ := e.Trackers(hashA)
	if len(trs) != 1 || !strings.Contains(trs[0], ":"+b.port()+"/") {
		t.Fatalf("trackers after set_trackers %q", trs)
	}
	waitUntil(t, "announce to the new tracker", func() bool { return b.count(hashA) > 0 })

	// A regular user's torrent: the literal loopback tracker is dropped
	torG, hashG := testTorrent(t, "g", "http://127.0.0.1:"+a.port()+"/announce")
	if _, err := e.Add(hashG, engine.AddRequest{Torrent: torG, Dir: data, Guard: true, Trackers: []string{"udp://[::1]:6969/announce", "http://localhost:" + a.port() + "/announce"}}); err != nil {
		t.Fatal(err)
	}
	if trs, _ := e.Trackers(hashG); len(trs) != 0 {
		t.Fatalf("guarded torrent kept %q", trs)
	}
	e.AddTrackers(hashG, []string{"http://127.0.0.2:" + a.port() + "/announce"})
	if trs, _ := e.Trackers(hashG); len(trs) != 0 {
		t.Fatalf("guarded torrent took %q", trs)
	}

	// A name that resolves to the NAS: kept, but the filter stops the announce
	name := ownName()
	if name == "" {
		t.Log("host name does not resolve to an own address: name check skipped")
		return
	}
	url := "http://" + name + ":" + b.port() + "/announce"
	torN, hashN := testTorrent(t, "n", url)
	torU, hashU := testTorrent(t, "u", url)
	e.Add(hashN, engine.AddRequest{Torrent: torN, Dir: data, Guard: true})
	e.Add(hashU, engine.AddRequest{Torrent: torU, Dir: data})
	waitUntil(t, "announce of the unguarded torrent by name", func() bool { return b.count(hashU) > 0 })
	time.Sleep(2 * time.Second)
	if n := b.count(hashN); n != 0 {
		t.Fatalf("guarded torrent announced to %s %d times", url, n)
	}
	if trs, _ := e.Trackers(hashN); len(trs) != 1 {
		t.Fatalf("guarded torrent by name: trackers %q", trs)
	}
}

// With an HTTP proxy for trackers that runs on the NAS itself, the guard
// stays on: numeric forms of loopback are removed, set_guard applies to a
// running torrent, and guarded torrents still reach their trackers through
// the proxy (which reaches what it reaches, as for URL downloads).
func TestRealGuardProxy(t *testing.T) {
	bin := dcbtBin(t)
	name := ownName()
	if name == "" {
		t.Skip("host name does not resolve to an own address")
	}
	tr := newTracker(t)
	defer tr.srv.Close()
	var proxied atomic.Int32
	px := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.IsAbs() {
			http.Error(w, "not a proxy request", 400)
			return
		}
		proxied.Add(1)
		out, _ := http.NewRequest(r.Method, r.URL.String(), nil)
		resp, err := http.DefaultTransport.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	defer px.Close()
	_, p, _ := net.SplitHostPort(px.Listener.Addr().String())
	port, _ := strconv.Atoi(p)
	dir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	e := newEngine(bin, dir)
	e.ApplyGlobal(engine.Global{PortFrom: 26901, PortTo: 26909, Proxy: engine.Proxy{Type: "http", Host: "127.0.0.1", Port: port, ApplyTrackers: true}})
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	data := filepath.Join(dir, "data")

	torN, hashN := testTorrent(t, "pn", "http://2130706433:"+tr.port()+"/announce")
	e.Add(hashN, engine.AddRequest{Torrent: torN, Dir: data, Guard: true, Trackers: []string{"udp://0x7f.1:6969/announce", "http://a.localhost:" + tr.port() + "/announce"}})
	if trs, _ := e.Trackers(hashN); len(trs) != 0 {
		t.Fatalf("numeric loopback kept: %q", trs)
	}

	torS, hashS := testTorrent(t, "ps", "http://127.0.0.1:"+tr.port()+"/announce")
	e.Add(hashS, engine.AddRequest{Torrent: torS, Dir: data})
	if trs, _ := e.Trackers(hashS); len(trs) != 1 {
		t.Fatalf("unguarded torrent lost its tracker: %q", trs)
	}
	if err := e.SetGuard(hashS, true); err != nil {
		t.Fatal(err)
	}
	if trs, _ := e.Trackers(hashS); len(trs) != 0 {
		t.Fatalf("set_guard kept %q", trs)
	}

	torG, hashG := testTorrent(t, "pg", "http://"+name+":"+tr.port()+"/announce")
	e.Add(hashG, engine.AddRequest{Torrent: torG, Dir: data, Guard: true})
	waitUntil(t, "announce through the proxy on the NAS", func() bool { return tr.count(hashG) > 0 && proxied.Load() > 0 })
}

// The finding that started D16: a proxy is set, but trackers do not go
// through it. A guarded torrent's tracker named after the NAS must still be
// blocked; an unguarded one with the same tracker reaches it.
func TestRealGuardProxyTrackersDirect(t *testing.T) {
	bin := dcbtBin(t)
	name := ownName()
	if name == "" {
		t.Skip("host name does not resolve to an own address")
	}
	tr := newTracker(t)
	defer tr.srv.Close()
	dir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	e := newEngine(bin, dir)
	e.ApplyGlobal(engine.Global{PortFrom: 26901, PortTo: 26909, Proxy: engine.Proxy{Type: "http", Host: "192.0.2.1", Port: 3128, ApplyTrackers: false}})
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	url := "http://" + name + ":" + tr.port() + "/announce"
	torG, hashG := testTorrent(t, "dg", url)
	torU, hashU := testTorrent(t, "du", url)
	e.Add(hashG, engine.AddRequest{Torrent: torG, Dir: filepath.Join(dir, "data"), Guard: true})
	e.Add(hashU, engine.AddRequest{Torrent: torU, Dir: filepath.Join(dir, "data")})
	waitUntil(t, "announce of the unguarded torrent", func() bool { return tr.count(hashU) > 0 })
	time.Sleep(2 * time.Second)
	if n := tr.count(hashG); n != 0 {
		t.Fatalf("guarded torrent reached the NAS %d times with a proxy set", n)
	}
}

// sourceTracker answers announces with fixed counts, or with a failure.
func sourceTracker(t *testing.T, body string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// dc-bt 1.4: the source fields of the status and the sources command, against
// a real dc-bt and local trackers.
func TestRealSources(t *testing.T) {
	bin := dcbtBin(t)
	dir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	e := newEngine(bin, dir)
	e.ApplyGlobal(engine.Global{PortFrom: 26901, PortTo: 26909, DHT: false})
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	if !e.Caps().PeerRefresh {
		t.Skip("dc-bt older than 1.4")
	}
	data := filepath.Join(dir, "data")
	counts := sourceTracker(t, "d8:completei3e10:incompletei4e8:intervali1800e5:peers0:e")
	gone := sourceTracker(t, "d14:failure reason20:unregistered torrente")
	silent := "http://127.0.0.1:1/announce" // nothing listens: fails
	tor, hash := testTorrent(t, "s", counts.URL+"/announce")
	if _, err := e.Add(hash, engine.AddRequest{Torrent: tor, Dir: data, Trackers: []string{gone.URL + "/announce", silent}}); err != nil {
		t.Fatal(err)
	}
	byURL := map[string]engine.TrackerState{}
	waitUntil(t, "tracker answers", func() bool {
		trs, err := e.Sources(hash)
		if err != nil {
			t.Fatal(err)
		}
		for _, tr := range trs {
			byURL[tr.URL] = tr
		}
		return byURL[counts.URL+"/announce"].Contacted && byURL[gone.URL+"/announce"].Fails > 0 && byURL[silent].Fails > 0
	})
	if c := byURL[counts.URL+"/announce"]; c.Seeds != 3 || c.Leechers != 4 || c.Fails != 0 || c.Message != "" {
		t.Fatalf("counting tracker %+v", c)
	}
	if g := byURL[gone.URL+"/announce"]; g.Message != "unregistered torrent" || g.Seeds != -1 {
		t.Fatalf("failing tracker %+v", g)
	}
	if s := byURL[silent]; s.Message == "" || s.Seeds != -1 {
		t.Fatalf("unreachable tracker %+v", s)
	}
	st, err := e.Status(hash)
	if err != nil {
		t.Fatal(err)
	}
	// Metadata, nobody connected: no copy; DHT off: not announced
	if st.Availability != 0 || st.DHTUp || st.DHTReplyAt != 0 {
		t.Fatalf("status %+v", st)
	}
	// Peers that are the NAS itself do not count as found
	if own := ownIPv4(); own != nil {
		self := sourceTracker(t, "d8:intervali1800e5:peers6:"+string(own)+"\x1a\xe1e")
		other := sourceTracker(t, "d8:intervali1800e5:peers6:\x0a\x01\x0b\xfa\x1a\xe1e")
		torS, hashS := testTorrent(t, "self", self.URL+"/announce")
		torO, hashO := testTorrent(t, "other", other.URL+"/announce")
		e.Add(hashS, engine.AddRequest{Torrent: torS, Dir: data})
		e.Add(hashO, engine.AddRequest{Torrent: torO, Dir: data})
		waitUntil(t, "a peer that is not the NAS", func() bool {
			st, _ := e.Status(hashO)
			return st != nil && st.KnownPeers == 1
		})
		trs, _ := e.Sources(hashS)
		if len(trs) != 1 || !trs[0].Contacted {
			t.Fatalf("self tracker %+v", trs)
		}
		if st, _ := e.Status(hashS); st == nil || st.KnownPeers != 0 {
			t.Fatalf("the NAS itself counted as a peer: %+v", st)
		}
	}
	// DHT nodes hand our own announce back under another address, with our
	// port: a peer on our listen port (one of 26901-26909) does not count
	var echo strings.Builder
	for p := 26901; p <= 26909; p++ {
		echo.WriteString("\x0a\x01\x0b\xfb" + string([]byte{byte(p >> 8), byte(p)}))
	}
	echoT := sourceTracker(t, "d8:intervali1800e5:peers54:"+echo.String()+"e")
	torE, hashE := testTorrent(t, "echo", echoT.URL+"/announce")
	e.Add(hashE, engine.AddRequest{Torrent: torE, Dir: data})
	waitUntil(t, "peers other than our port", func() bool {
		st, _ := e.Status(hashE)
		return st != nil && st.KnownPeers == 8
	})
	time.Sleep(time.Second)
	if st, _ := e.Status(hashE); st.KnownPeers != 8 {
		t.Fatalf("a peer on our own port counted: %+v", st)
	}
	m := "magnet:?xt=urn:btih:2b3f0a5ab8f1c0e6f3d7d1e4b4b7b8c9d0e1f2a3"
	ref, err := e.Add("", engine.AddRequest{Magnet: m, Dir: data})
	if err != nil {
		t.Fatal(err)
	}
	if st, err := e.Status(ref); err != nil || st.Availability != -1 {
		t.Fatalf("magnet status %+v %v", st, err)
	}
}

// ownIPv4 is an IPv4 address of this machine that is not loopback, as 4 bytes.
func ownIPv4() []byte {
	ifs, _ := net.InterfaceAddrs()
	for _, a := range ifs {
		if ip, _, err := net.ParseCIDR(a.String()); err == nil && !ip.IsLoopback() && ip.To4() != nil && !ip.IsLinkLocalUnicast() {
			return ip.To4()
		}
	}
	return nil
}
