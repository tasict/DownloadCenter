//go:build linux

package phpbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const hashA = "0123456789abcdef0123456789abcdef01234567"

// The sandbox tests need root and QTS's PHP; elsewhere they are skipped.
func boxRuntime(t *testing.T) *Runtime {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("the sandbox needs root")
	}
	if _, err := os.Stat(PHPPath); err != nil {
		t.Skip("no PHP at " + PHPPath)
	}
	return New(Config{Dir: filepath.Join(t.TempDir(), "phpbox"), Version: "test"})
}

func mounts(t *testing.T) string {
	b, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// noTrace checks that a test left no mount and nothing in the box's
// directory but the empty mount point, and no process of the box's uid.
func noTrace(t *testing.T, r *Runtime, before string) {
	t.Helper()
	if after := mounts(t); after != before {
		t.Errorf("mounts changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	entries, _ := os.ReadDir(r.cfg.Dir)
	for _, e := range entries {
		if e.Name() != "root" && e.Name() != caFileName {
			t.Errorf("left in the box's directory: %s", e.Name())
		}
	}
	if rest, _ := os.ReadDir(filepath.Join(r.cfg.Dir, "root")); len(rest) != 0 {
		t.Errorf("the mount point is not empty: %d entries", len(rest))
	}
	procs, _ := filepath.Glob("/proc/[0-9]*/status")
	for _, p := range procs {
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), fmt.Sprintf("\nUid:\t%d\t", boxUID)) || strings.Contains(string(b), fmt.Sprintf("\nUid:\t%d\t", boxUID-1)) {
			t.Errorf("a process of the box is still there: %s", p)
		}
	}
}

// upstream answers search.test for the test plugins.
func upstream(t *testing.T) *fakeUpstream {
	return newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json":
			if r.URL.Query().Get("q") != "ubuntu linux" || r.URL.Query().Get("u") != "alice" {
				http.Error(w, "bad query", 400)
				return
			}
			fmt.Fprintf(w, `{"data":{"items":[{"name":"Ubuntu JSON","magnet":"magnet:?xt=urn:btih:%s","bytes":123456,"seeders":5,"leechers":"6","added":"2026-01-01","url":"http://search.test/1","ih":"%s","cat":"Linux"},{"name":"Second","magnet":"http://search.test/2.torrent","bytes":"2 GB","seeders":"x"}]}}`, hashA, hashA)
		case "/rss":
			io.WriteString(w, `<?xml version="1.0"?><rss version="2.0" xmlns:t="http://xmlns.ezrss.it/0.1/"><channel>`+
				`<item><title>RSS one</title><link>http://search.test/r1</link><enclosure url="http://search.test/r1.torrent" length="777" type="application/x-bittorrent"/><pubDate>Tue, 03 Feb 2026 04:05:06 +0000</pubDate><t:infoHash>`+hashA+`</t:infoHash><t:seeds>9</t:seeds><t:peers>2</t:peers></item>`+
				`<item><title>RSS two</title><link>http://search.test/r2</link><t:magnetURI>magnet:?xt=urn:btih:`+hashA+`</t:magnetURI></item>`+
				`</channel></rss>`)
		case "/login":
			r.ParseForm()
			if r.PostForm.Get("u") == "alice" && r.PostForm.Get("p") == "secret" {
				http.SetCookie(w, &http.Cookie{Name: "session", Value: "1"})
				io.WriteString(w, "ok")
				return
			}
			io.WriteString(w, "no")
		case "/addon":
			if r.URL.Query().Get("q") != "ubuntu linux" || r.URL.Query().Get("limit") != "100" || r.URL.Query().Get("cat") != "software" {
				http.Error(w, "bad query", 400)
				return
			}
			fmt.Fprintf(w, `[{"name":"Add-on one","link":"magnet:?xt=urn:btih:%s","page":"http://search.test/a1","size":1000,"time":"2026-03-04T05:06:07Z","seeds":7,"peers":8,"cat":"Software"},{"name":"Add-on two","link":"http://search.test/a2.torrent","page":"","size":"5 MB","time":"2026-03-05","seeds":0,"peers":1,"cat":"Software"}]`, hashA)
		default:
			http.NotFound(w, r)
		}
	})
}

// install inspects and extracts a package for a test.
func install(t *testing.T, name string, data []byte, extra map[string]string) Plugin {
	t.Helper()
	p, err := Inspect(name, data)
	if err != nil {
		t.Fatal(err)
	}
	for n, b := range extra {
		p.Files[n] = []byte(b)
	}
	dir := filepath.Join(t.TempDir(), "code")
	if err := p.Extract(dir); err != nil {
		t.Fatal(err)
	}
	return p.Plugin(dir)
}

// The self-check passes on this NAS, and what the box looks like.
func TestProbe(t *testing.T) {
	r := boxRuntime(t)
	before := mounts(t)
	start := time.Now()
	ok, code, reason := r.Check(context.Background())
	t.Logf("check: ok=%v code=%q reason=%q in %v", ok, code, reason, time.Since(start).Round(time.Millisecond))
	lay, _, _ := discover()
	t.Logf("layout: php %s, extensions %s %v, binds %v, links %v", lay.php, lay.extDir, lay.exts, lay.binds, lay.links)
	ro := r.run(context.Background(), lay, "", "probe.php", true, request{Mode: "probe", Paths: []string{"/share", "/etc/config/shadow"}, Direct: "192.0.2.1:80"})
	t.Logf("inside: %s", strings.TrimSpace(string(ro.answer)))
	t.Logf("relay: %+v", ro.relay)
	if !ok {
		t.Fatalf("check failed: stage %s err %v log %q", ro.stage, ro.err, ro.log)
	}
	// A passed check is not run again for a while
	n := r.starts.Load()
	if ok, _, _ := r.Check(context.Background()); !ok || r.starts.Load() != n {
		t.Error("a fresh check ran again")
	}
	noTrace(t, r, before)
}

func TestModuleSearch(t *testing.T) {
	r := boxRuntime(t)
	up := upstream(t)
	r.relay.testDial = up.hook
	before := mounts(t)
	p := install(t, "test.dlm", dlmOf(t, []entry{{name: "INFO", body: testInfo}, {name: "search.php", body: testModule}}), nil)
	out, err := r.Search(context.Background(), p, Query{Keyword: "ubuntu linux", Category: "all", Account: &Account{"alice", "secret"}})
	if err != nil {
		t.Fatalf("%v: %+v", err, out)
	}
	t.Logf("module: %d results in %v, relay %+v", len(out.Results), out.Duration.Round(time.Millisecond), out.Relay)
	byTitle := map[string]Result{}
	for _, res := range out.Results {
		byTitle[res.Title] = res
	}
	if len(out.Results) != 5 {
		t.Fatalf("%+v", out.Results)
	}
	if j := byTitle["Ubuntu JSON"]; j.Download != "magnet:?xt=urn:btih:"+hashA || j.Size != 123456 || j.Seeds != 5 || j.Leechs != 6 ||
		j.Page != "http://search.test/1" || j.Hash != hashA || j.Category != "Linux" || j.Date != "2026-01-01" {
		t.Errorf("json: %+v", j)
	}
	if s := byTitle["Second"]; s.Size != 2<<30 || s.Seeds != -1 || s.Leechs != -1 {
		t.Errorf("json second: %+v", s)
	}
	if d := byTitle["Direct result"]; d.Size != 1572864 || d.Seeds != 3 || d.Leechs != 4 || d.Download != "http://search.test/files/a.torrent" {
		t.Errorf("direct: %+v", d)
	}
	if o := byTitle["RSS one"]; o.Download != "http://search.test/r1.torrent" || o.Size != 777 || o.Hash != hashA || o.Seeds != 9 ||
		o.Leechs != 2 || o.Page != "http://search.test/r1" || o.Date != "Tue, 03 Feb 2026 04:05:06 +0000" {
		t.Errorf("rss: %+v", o)
	}
	if o := byTitle["RSS two"]; o.Download != "magnet:?xt=urn:btih:"+hashA {
		t.Errorf("rss magnet: %+v", o)
	}
	// The module's noise on stdout stayed out of the answer
	if strings.Contains(out.Log, "noise") {
		t.Error("stdout in the log")
	}
	for _, c := range []struct {
		pass string
		want bool
	}{{"secret", true}, {"wrong", false}} {
		ok, _, err := r.Verify(context.Background(), p, Account{"alice", c.pass})
		if err != nil || ok != c.want {
			t.Errorf("verify %s: %v %v", c.pass, ok, err)
		}
	}
	noTrace(t, r, before)
}

func TestAddonSearch(t *testing.T) {
	r := boxRuntime(t)
	up := upstream(t)
	r.relay.testDial = up.hook
	before := mounts(t)
	p := install(t, "test.addon", addonOf(t, []entry{{name: "addon.json", body: testAddonJSON}, {name: "main.php", body: testAddon}}, true), nil)
	out, err := r.Search(context.Background(), p, Query{Keyword: "ubuntu linux", Category: "software", Account: &Account{"alice", "secret"}})
	if err != nil {
		t.Fatalf("%v: %+v", err, out)
	}
	t.Logf("add-on: %d results in %v, relay %+v", len(out.Results), out.Duration.Round(time.Millisecond), out.Relay)
	if len(out.Results) != 2 || out.Verified == nil || !*out.Verified {
		t.Fatalf("%+v", out)
	}
	a := out.Results[0]
	if a.Title != "Add-on one" || a.Download != "magnet:?xt=urn:btih:"+hashA || a.Page != "http://search.test/a1" || a.Size != 1000 ||
		a.Date != "2026-03-04T05:06:07+00:00" || a.Seeds != 7 || a.Leechs != 8 || a.Category != "Software" {
		t.Errorf("%+v", a)
	}
	if b := out.Results[1]; b.Size != 5<<20 || b.Download != "http://search.test/a2.torrent" {
		t.Errorf("%+v", b)
	}
	// A refused account stops the search
	if out, err := r.Search(context.Background(), p, Query{Keyword: "ubuntu linux", Category: "software", Account: &Account{"alice", "wrong"}}); !errors.Is(err, ErrFailed) || out.Error != "the account was refused" {
		t.Errorf("wrong account: %v %+v", err, out)
	}
	if ok, _, err := r.Verify(context.Background(), p, Account{"alice", "secret"}); err != nil || !ok {
		t.Errorf("verify: %v %v", ok, err)
	}
	noTrace(t, r, before)
}

// spec: the plugin cannot read the NAS's files, run commands or reach
// anything but public hosts on ports 80 and 443 through the relay
func TestEscapes(t *testing.T) {
	r := boxRuntime(t)
	up := upstream(t)
	r.relay.testDial = up.hook
	before := mounts(t)
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
	// The listener's port is allowed here so that only its address is judged
	r.relay.ports[lp] = true
	lan := "192.0.2.1"
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() {
				lan = ipn.IP.String()
				break
			}
		}
	}
	targets, _ := json.Marshal(map[string]map[string]string{
		"relay": {
			"loop8080": "http://127.0.0.1:8080/", "loop": "http://127.0.0.1:" + lp + "/", "lan": "http://" + lan + ":" + lp + "/",
			"private": "http://192.168.1.1/", "ssh": "http://example.com:22/", "tunnel_ssh": "http://example.com:22/",
			"tunnel_lan": "http://" + lan + ":" + lp + "/", "fake": "http://search.test/rss",
		},
		"direct": {"lan8080": "http://" + lan + ":8080/", "lan": "http://" + lan + ":" + lp + "/", "loop": "http://127.0.0.1:" + lp + "/"},
	})
	info := strings.NewReplacer(`"search.php"`, `"escape.php"`, `"DcTestSearch"`, `"DcEscape"`, `"accountsupport":true`, `"accountsupport":false`).Replace(testInfo)
	p := install(t, "escape.dlm", dlmOf(t, []entry{{name: "INFO", body: info}, {name: "escape.php", body: testEscape}}), map[string]string{"targets.json": string(targets)})
	out, err := r.Search(context.Background(), p, Query{Keyword: "x"})
	if err != nil || len(out.Results) != 1 {
		t.Fatalf("%v: %+v", err, out)
	}
	t.Logf("seen from inside: %s", out.Results[0].Title)
	t.Logf("relay: %+v", out.Relay)
	var o struct {
		Shadow  any               `json:"shadow"`
		Share   any               `json:"share"`
		Passwd  bool              `json:"passwd"`
		Outside bool              `json:"outside"`
		Write   bool              `json:"write"`
		Call    map[string]string `json:"call"`
		Basedir bool              `json:"basedir"`
		Net     map[string][3]any `json:"net"`
		Direct  map[string]bool   `json:"direct"`
	}
	if err := json.Unmarshal([]byte(out.Results[0].Title), &o); err != nil {
		t.Fatal(err)
	}
	if o.Shadow != false || o.Share != false || o.Passwd || o.Outside || o.Write || o.Basedir {
		t.Errorf("files: %+v", o)
	}
	for f, got := range o.Call {
		if got != "Error" {
			t.Errorf("%s: %s", f, got)
		}
	}
	if len(o.Call) != 10 {
		t.Errorf("calls %v", o.Call)
	}
	for k, v := range o.Net {
		code := fmt.Sprint(v[1])
		if k == "fake" {
			if code != "200" {
				t.Errorf("the allowed request failed: %v", v)
			}
			continue
		}
		if code == "200" {
			t.Errorf("%s reached: %v", k, v)
		}
	}
	for k, reached := range o.Direct {
		if reached {
			t.Errorf("direct %s reached", k)
		}
	}
	if hits.Load() != 0 {
		t.Errorf("the listener was reached %d times", hits.Load())
	}
	if out.Relay.Refused < 7 {
		t.Errorf("refused %d", out.Relay.Refused)
	}
	noTrace(t, r, before)
}

// spec: a plugin that runs too long is killed; an answer over the limit is
// refused
func TestLimits(t *testing.T) {
	r := boxRuntime(t)
	before := mounts(t)
	if ok, _, reason := r.Check(context.Background()); !ok {
		t.Fatal(reason)
	}
	r.wall = 3 * time.Second
	loopInfo := strings.NewReplacer(`"search.php"`, `"loop.php"`, `"DcTestSearch"`, `"DcLoop"`, `"accountsupport":true`, `"accountsupport":false`).Replace(testInfo)
	loop := install(t, "loop.dlm", dlmOf(t, []entry{{name: "INFO", body: loopInfo}, {name: "loop.php", body: testLoop}}), nil)
	start := time.Now()
	_, err := r.Search(context.Background(), loop, Query{Keyword: "x"})
	took := time.Since(start)
	t.Logf("endless loop: %v after %v", err, took.Round(time.Millisecond))
	if !errors.Is(err, ErrTimeout) || took < r.wall || took > r.wall+3*time.Second {
		t.Errorf("loop: %v after %v", err, took)
	}
	floodInfo := strings.NewReplacer(`"search.php"`, `"flood.php"`, `"DcTestSearch"`, `"DcFlood"`, `"accountsupport":true`, `"accountsupport":false`).Replace(testInfo)
	flood := install(t, "flood.dlm", dlmOf(t, []entry{{name: "INFO", body: floodInfo}, {name: "flood.php", body: testFlood}}), nil)
	if _, err := r.Search(context.Background(), flood, Query{Keyword: "x"}); !errors.Is(err, ErrOutput) {
		t.Errorf("flood: %v", err)
	}
	// At most two at a time: four loops take two rounds
	r.wall = 2 * time.Second
	start = time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Search(context.Background(), loop, Query{Keyword: "x"})
		}()
	}
	wg.Wait()
	if took := time.Since(start); took < 2*r.wall {
		t.Errorf("four runs in %v: more than two at a time", took)
	}
	// A caller that gives up while waiting does not run
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Search(ctx, loop, Query{Keyword: "x"}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	noTrace(t, r, before)
}

// A self-check that does not see what it must makes the runtime
// unavailable, and nothing of the plugin runs.
func TestFailedCheckRunsNothing(t *testing.T) {
	r := boxRuntime(t)
	r.skewUID = 4242
	if ok, code, reason := r.Check(context.Background()); ok || code != CodeCheckFailed || reason != ReasonCheckFailed {
		t.Fatalf("%v %s %s", ok, code, reason)
	}
	p := Plugin{Format: FormatDLM, Dir: t.TempDir(), Entry: "x.php", Class: "X"}
	var u *Unavailable
	if _, err := r.Search(context.Background(), p, Query{Keyword: "x"}); !errors.As(err, &u) || u.Code != CodeCheckFailed {
		t.Errorf("search: %v", err)
	}
	if r.plugins.Load() != 0 {
		t.Errorf("%d plugin runs", r.plugins.Load())
	}
	// Fixed (here: the expectation), the next check passes without a restart
	r.skewUID = 0
	if ok, _, reason := r.Check(context.Background()); !ok {
		t.Errorf("after the fix: %s", reason)
	}
}
