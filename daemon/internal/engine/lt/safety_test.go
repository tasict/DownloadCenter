package lt

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"downloadcenter/internal/engine"
)

// A regular user's torrent needs dc-bt 1.5; what dc-bt counts as seeded
// without starting it is reported as complete until it is removed.
func TestUntrustedAdapter(t *testing.T) {
	dir := t.TempDir()
	dcbt := "1.4"
	f := newFake(t, dir, func(req map[string]any) (any, string) {
		switch req["cmd"] {
		case "version":
			return map[string]any{"dcbt": dcbt, "libtorrent": "2.0.15"}, ""
		case "add":
			if req["save_path"] == "/home/u/Download" {
				return map[string]any{"infohash": "08ada5a7a6183aae1e09d831df6748d566095a10", "seed_done": true, "total_wanted": 1234}, ""
			}
			if req["save_path"] == "/elsewhere" {
				return nil, "not a private folder: /elsewhere"
			}
			return map[string]any{"infohash": "08ada5a7a6183aae1e09d831df6748d566095a10"}, ""
		case "status", "remove":
			return nil, "torrent not found"
		case "status_all":
			return map[string]any{"torrents": []any{}}, ""
		}
		return map[string]any{}, ""
	})
	defer f.ln.Close()
	e := newEngine("/bin/true", dir)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	<-f.reqs
	tor := []byte("d4:infod4:name1:xee")
	if _, err := e.Add("x", engine.AddRequest{Torrent: tor, Dir: "/stage", UntrustedDir: true}); err != engine.ErrEngineTooOld {
		t.Fatalf("dc-bt 1.4: %v", err)
	}
	dcbt = "1.5"
	e.Health()
	<-f.reqs
	if _, err := e.Add("x", engine.AddRequest{Torrent: tor, Dir: "/stage", UntrustedDir: true}); err != nil {
		t.Fatal(err)
	}
	if r := <-f.reqs; r["untrusted_dir"] != true || r["save_path"] != "/stage" {
		t.Fatalf("add %v", r)
	}
	if _, err := e.Add("x", engine.AddRequest{Torrent: tor, Dir: "/elsewhere", UntrustedDir: true}); err != engine.ErrFolderNotPrivate {
		t.Fatalf("not private: %v", err)
	}
	<-f.reqs
	ref, err := e.Add("x", engine.AddRequest{Torrent: tor, Dir: "/home/u/Download", UntrustedDir: true})
	if err != nil {
		t.Fatal(err)
	}
	<-f.reqs
	st, err := e.Status(ref)
	<-f.reqs
	if err != nil || st.State != engine.Complete || !st.Seeding || st.Total != 1234 || st.Completed != 1234 || st.Dir != "/home/u/Download" {
		t.Fatalf("status %+v %v", st, err)
	}
	all, _ := e.StatusAll()
	<-f.reqs
	if all[ref] == nil || all[ref].State != engine.Complete {
		t.Fatalf("status_all %+v", all)
	}
	e.Remove(ref)
	<-f.reqs
	all, _ = e.StatusAll()
	<-f.reqs
	if all[ref] != nil {
		t.Fatal("still reported after Remove")
	}
	// A torrent added without UntrustedDir does not say untrusted_dir
	e.Add("x", engine.AddRequest{Torrent: tor, Dir: "/stage"})
	if r := <-f.reqs; r["untrusted_dir"] != nil {
		t.Fatalf("add %v", r)
	}
}

// "Proxy only" without a proxy host is sent as it is, so that dc-bt makes no
// connection rather than direct ones.
func TestSettingsProxyOnlyNoHost(t *testing.T) {
	px := settingsJSON(engine.Global{Proxy: engine.Proxy{Type: "socks5", Force: true}}, "")["proxy"].(map[string]any)
	if px["type"] != "socks5" || px["force"] != true || px["host"] != "" {
		t.Fatalf("proxy %v", px)
	}
	px = settingsJSON(engine.Global{Proxy: engine.Proxy{Type: "socks5"}}, "")["proxy"].(map[string]any)
	if px["type"] != "none" {
		t.Fatalf("no host, not forced: %v", px)
	}
}

// The socket outside data/ (when that path is too long) is only used in a
// folder of dcd's own.
func TestSockPathFallback(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("needs root to give the folder to another account")
	}
	long := filepath.Join(t.TempDir(), strings.Repeat("d", 100))
	e := newEngine("/bin/true", long)
	p := e.sockPath()
	if !strings.HasPrefix(filepath.Base(filepath.Dir(p)), "dc-bt-") {
		t.Fatalf("socket %s", p)
	}
	dir := filepath.Dir(p)
	defer os.RemoveAll(dir)
	os.Chown(dir, 54321, 54321) // someone else's folder of that name
	if q := e.sockPath(); q == p {
		t.Fatalf("socket in a folder of another account: %s", q)
	}
}

// countingSeeder is utSeeder that counts the connections it gets.
func countingSeeder(t *testing.T, data []byte) (string, *atomic.Int32) {
	var n atomic.Int32
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			go utServe(c, data)
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	peer := string([]byte{127, 0, 0, 1, byte(port >> 8), byte(port)})
	tr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("d8:intervali1800e5:peers6:" + peer + "e"))
	}))
	t.Cleanup(tr.Close)
	return tr.URL + "/announce", &n
}

// With "proxy only", peers that cannot go through the proxy are not
// connected to at all, and without a usable proxy nothing is.
func TestRealProxyOnly(t *testing.T) {
	var proxied atomic.Int32
	hp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	defer hp.Close()
	_, ps, _ := net.SplitHostPort(hp.Listener.Addr().String())
	port, _ := strconv.Atoi(ps)
	for _, c := range []struct {
		name  string
		proxy engine.Proxy
	}{
		{"http proxy for trackers", engine.Proxy{Type: "http", Host: "127.0.0.1", Port: port, ApplyTrackers: true, Force: true}},
		{"no proxy host", engine.Proxy{Type: "socks5", Force: true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			bin := dcbtBin(t)
			dir, err := os.MkdirTemp("", "bt")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			e := newEngine(bin, dir)
			e.ApplyGlobal(engine.Global{PortFrom: 26891, PortTo: 26899, Proxy: c.proxy})
			if err := e.Start(); err != nil {
				t.Fatal(err)
			}
			defer e.Stop()
			data := utRandom(utPiece)
			announce, conns := countingSeeder(t, data)
			tor, hash := utTorrent(t, "P", []utFile{{"a.bin", data}}, announce)
			if _, err := e.Add(hash, engine.AddRequest{Torrent: tor, Dir: filepath.Join(dir, "data")}); err != nil {
				t.Fatal(err)
			}
			time.Sleep(6 * time.Second)
			if n := conns.Load(); n != 0 {
				t.Fatalf("%d direct connections to a peer", n)
			}
			t.Logf("tracker requests through the proxy: %d", proxied.Load())
		})
	}
}

// A handed-over file the user cuts short (it is the user's file too) must
// not end dc-bt when a peer asks for what is no longer there.
func TestRealUntrustedTruncate(t *testing.T) {
	e, _ := utStart(t, "")
	d := utScratch(t)
	a := utRandom(4 * utPiece)
	tor, hash := utTorrent(t, "Cut", []utFile{{"a.bin", a}}, "")
	utPut(t, d.stage, "Cut/a.bin", a)
	ref, err := e.Add(hash, engine.AddRequest{Torrent: tor, Dir: d.stage, Check: true, UntrustedDir: true})
	if err != nil {
		t.Fatal(err)
	}
	utSeeding(t, e, ref)
	if st := utMove(t, e, ref, d.dl, ""); st.MoveError != "" {
		t.Fatalf("hand-over: %s", st.MoveError)
	}
	peerBlock(t, hash, 3) // mapped while whole
	if err := os.Truncate(filepath.Join(d.dl, "Cut", "a.bin"), 0); err != nil {
		t.Fatal(err)
	}
	ih, _ := hexBytes(hash)
	for port := 26891; port <= 26899; port++ {
		askBlock("127.0.0.1:"+strconv.Itoa(port), ih, 3)
	}
	time.Sleep(time.Second)
	if err := e.Health(); err != nil {
		t.Fatalf("dc-bt gone: %v", err)
	}
	st, _ := e.Status(ref)
	t.Logf("after the cut: %s %q", st.State, st.ErrorMsg)
}

// A guarded torrent's web seed whose name leads to the NAS is removed: an
// HTTP tracker proxy on the NAS opens the NAS's address to guarded torrents,
// at every port.
func TestRealWebSeedName(t *testing.T) {
	name := ownName()
	if name == "" {
		t.Skip("host name does not resolve to an own address")
	}
	e, dir := utStart(t, "")
	data := utRandom(utPiece)
	tor, hash := utTorrent(t, "W", []utFile{{"a.bin", data}}, "")
	seed := "http://" + name + ":1/"
	tor = append(append(tor[:len(tor)-1:len(tor)-1], []byte("8:url-list"+itoa(len(seed))+":"+seed)...), 'e')
	if _, err := e.Add(hash, engine.AddRequest{Torrent: tor, Dir: filepath.Join(dir, "data"), Guard: true}); err != nil {
		t.Fatal(err)
	}
	utWait(t, "web seed removed", 15*time.Second, func() bool {
		b, _ := os.ReadFile(filepath.Join(dir, "logs", "dc-bt.log"))
		return strings.Contains(string(b), hash+": web seed removed")
	})
}

// Requests with fields of the wrong type, and an infohash that is a path,
// are refused; dc-bt keeps running and deletes nothing outside its folder.
func TestRealBadRequests(t *testing.T) {
	e, dir := utStart(t, "")
	for _, n := range []string{"victim.resume", "victim.json"} {
		os.WriteFile(filepath.Join(dir, n), []byte("keep"), 0600)
	}
	e.disconnect()
	c, err := net.Dial("unix", e.sockPath())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(c)
	for _, line := range []string{
		`{"id":"seven","cmd":"version"}`,
		`[1,2,3]`,
		`{"id":2,"cmd":"remove","infohash":"../victim"}`,
		`{"id":3,"cmd":"pause","infohash":7}`,
		`{"id":4,"cmd":"apply_settings","settings":{"listen_from":"x"}}`,
		`{"id":5,"cmd":"version"}`,
	} {
		io.WriteString(c, line+"\n")
		ans, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("%s: dc-bt stopped answering: %v", line, err)
		}
		t.Logf("%s -> %s", line, strings.TrimSpace(ans))
	}
	for _, n := range []string{"victim.resume", "victim.json"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Fatalf("%s deleted", n)
		}
	}
}

// A client that stops reading must not stop dc-bt: its answers wait (up to
// a limit), and a new client is served at once.
func TestRealSlowClient(t *testing.T) {
	e, _ := utStart(t, "")
	d := utScratch(t)
	for i := 0; i < 20; i++ {
		data := utRandom(utPiece)
		tor, hash := utTorrent(t, "S"+itoa(i), []utFile{{"a.bin", data}}, "")
		if _, err := e.Add(hash, engine.AddRequest{Torrent: tor, Dir: d.stage, Paused: true}); err != nil {
			t.Fatal(err)
		}
	}
	e.disconnect()
	c, err := net.Dial("unix", e.sockPath())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Never read: the socket buffers fill up
	req := strings.Repeat(`{"id":1,"cmd":"status_all"}`+"\n", 100)
	c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	for i := 0; i < 50; i++ {
		if _, err := io.WriteString(c, req); err != nil {
			break
		}
	}
	time.Sleep(time.Second)
	c2, err := net.Dial("unix", e.sockPath())
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	c2.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(c2, `{"id":9,"cmd":"version"}`+"\n")
	r := bufio.NewReader(c2)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("dc-bt does not serve a new client: %v", err)
		}
		if strings.Contains(line, `"id":9`) {
			return
		}
	}
}

// status_all without the file lists: the same torrents, no files.
func TestRealStatusAllFiles(t *testing.T) {
	e, dir := utStart(t, "")
	data := utRandom(utPiece)
	tor, hash := utTorrent(t, "F", []utFile{{"a.bin", data}, {"b.bin", data}}, "")
	if _, err := e.Add(hash, engine.AddRequest{Torrent: tor, Dir: filepath.Join(dir, "data"), Paused: true}); err != nil {
		t.Fatal(err)
	}
	all, err := e.StatusAllFiles(true)
	if err != nil || all[hash] == nil || len(all[hash].Files) != 2 {
		t.Fatalf("with files: %+v %v", all[hash], err)
	}
	all, err = e.StatusAllFiles(false)
	if err != nil || all[hash] == nil || len(all[hash].Files) != 0 || all[hash].Total == 0 {
		t.Fatalf("without files: %+v %v", all[hash], err)
	}
	if all, err = e.StatusAll(); err != nil || len(all[hash].Files) != 2 {
		t.Fatalf("StatusAll: %+v %v", all[hash], err)
	}
}

// dc-bt 1.7: data in the destination is offered to it to take over, what it
// answers is reported, a refusal of required data is ErrFolderNotPrivate,
// and a tracker's certificate error reads as such.
func TestAdoptAdapter(t *testing.T) {
	dir := t.TempDir()
	dcbt := "1.6"
	f := newFake(t, dir, func(req map[string]any) (any, string) {
		switch req["cmd"] {
		case "version":
			return map[string]any{"dcbt": dcbt, "libtorrent": "2.0.15"}, ""
		case "add":
			if req["adopt_root"] == "Pending" {
				return map[string]any{"infohash": "08ada5a7a6183aae1e09d831df6748d566095a10", "adopting": true}, ""
			}
			if req["adopt_required"] == true {
				return nil, "cannot take over the data: none of the torrent's files is in /d/Box"
			}
			return map[string]any{"infohash": "08ada5a7a6183aae1e09d831df6748d566095a10", "adopted": 2}, ""
		case "sources":
			return map[string]any{"trackers": []any{map[string]any{"url": "https://t/announce", "fails": 3,
				"message": "certificate verify failed (SSL routines)"}}}, ""
		}
		return map[string]any{}, ""
	})
	defer f.ln.Close()
	e := newEngine("/bin/true", dir)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	<-f.reqs
	req := engine.AddRequest{Torrent: []byte("d4:infod4:name1:xee"), Dir: "/stage", UntrustedDir: true, AdoptFrom: "/d", AdoptRoot: "Box"}
	e.Add("x", req)
	if r := <-f.reqs; r["adopt_from"] != nil || e.Caps().AdoptData {
		t.Fatalf("dc-bt 1.6 was offered data: %v", r)
	}
	dcbt = "1.7"
	e.Health()
	<-f.reqs
	ref, err := e.Add("x", req)
	if r := <-f.reqs; err != nil || r["adopt_from"] != "/d" || r["adopt_root"] != "Box" || r["adopt_required"] != nil || !e.Adopted(ref) || !e.Caps().AdoptData {
		t.Fatalf("add %v %v adopted %v", r, err, e.Adopted(ref))
	}
	req.AdoptRequired = true
	if _, err := e.Add("x", req); err != engine.ErrFolderNotPrivate {
		t.Fatalf("required: %v", err)
	}
	<-f.reqs
	req.AdoptRoot = "Pending"
	if _, err := e.Add("x", req); err != engine.ErrPending {
		t.Fatalf("still checking: %v", err)
	}
	<-f.reqs
	trs, err := e.Sources("08ada5a7a6183aae1e09d831df6748d566095a10")
	<-f.reqs
	if err != nil || len(trs) != 1 || trs[0].Message != TrackerCertificate {
		t.Fatalf("sources %+v %v", trs, err)
	}
}

// Versions compare by major and minor; a patch level does not count.
func TestVersionAtLeast(t *testing.T) {
	for v, want := range map[string]bool{"1.7": true, "1.7.1": true, "1.10": true, "2.0": true, "1.6": false, "1.6.9": false, "": false, "x": false} {
		if versionAtLeast(v, 1, 7) != want {
			t.Errorf("%q: %v", v, !want)
		}
	}
}
