//go:build linux

package phpbox

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// testInside is an add-on that reports, as its one result's title, what
// it sees of its own process; with grow it then tries to take that many
// MiB of memory, past PHP's own limit.
const testInside = `<?php
class DcInside implements ISite, ISearch
{
    public function __construct($url = null, $username = null, $password = null, $meta = null)
    {
    }

    public function Search($keyword, $limit, $category)
    {
        $lim = posix_getrlimit();
        $o = array('uid' => posix_getuid(), 'as' => (string)$lim['soft totalmem']);
        $grow = (int)$keyword;
        if ($grow > 0) {
            ini_set('memory_limit', '-1');
            $s = str_repeat('x', $grow << 20);
            $o['grew'] = strlen($s);
        }
        $l = new SearchLink();
        $l->name = json_encode($o);
        $l->link = 'magnet:?xt=urn:btih:` + hashA + `';
        return array($l);
    }
}
`

func insidePlugin(t *testing.T) Plugin {
	meta := `{"name":"Inside","class":"DcInside","addon":"inside.php","version":100,"interface":["ISearch"],"description":{"ENG":"Looks around"}}`
	return install(t, "inside.addon", addonOf(t, files(map[string]string{"addon.json": meta, "inside.php": testInside}), true), nil)
}

type inside struct {
	UID  int    `json:"uid"`
	AS   string `json:"as"`
	Grew int    `json:"grew"`
}

func lookInside(t *testing.T, r *Runtime, p Plugin, grow string) (*inside, error) {
	t.Helper()
	out, err := r.Search(context.Background(), p, Query{Keyword: grow})
	if err != nil {
		return nil, err
	}
	if len(out.Results) != 1 {
		t.Fatalf("%+v", out)
	}
	var o inside
	if err := json.Unmarshal([]byte(out.Results[0].Title), &o); err != nil {
		t.Fatal(err)
	}
	return &o, nil
}

// spec: the plugin's process has a memory limit of its own that PHP
// settings cannot lift: taking 1 GiB or 2 GiB fails the run
func TestMemoryLimit(t *testing.T) {
	r := boxRuntime(t)
	before := mounts(t)
	p := insidePlugin(t)
	o, err := lookInside(t, r, p, "0")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("inside: %+v", o)
	if o.AS != "805306368" {
		t.Errorf("address space limit %q", o.AS)
	}
	// A little more than PHP's own limit is fine
	if o, err := lookInside(t, r, p, "200"); err != nil || o.Grew != 200<<20 {
		t.Errorf("200 MiB: %+v %v", o, err)
	}
	for _, grow := range []string{"1024", "2048"} {
		if o, err := lookInside(t, r, p, grow); !errors.Is(err, ErrFailed) {
			t.Errorf("%s MiB: %+v %v", grow, o, err)
		}
	}
	noTrace(t, r, before)
}

// spec: two runs at the same time have two different uids
func TestSlotUIDs(t *testing.T) {
	r := boxRuntime(t)
	before := mounts(t)
	p := insidePlugin(t)
	if ok, _, reason := r.Check(context.Background()); !ok {
		t.Fatal(reason)
	}
	// Each run holds its slot until the other has started: a plugin that
	// waits for a file the other one writes would need a shared folder, so
	// both simply take long enough to overlap
	slow := strings.Replace(testInside, "$grow = (int)$keyword;", "usleep(700000); $grow = (int)$keyword;", 1)
	meta := `{"name":"Inside","class":"DcInside","addon":"inside.php","version":100,"interface":["ISearch"],"description":{"ENG":"Looks around"}}`
	p = install(t, "slow.addon", addonOf(t, files(map[string]string{"addon.json": meta, "inside.php": slow}), true), nil)
	var mu sync.Mutex
	uids := map[int]bool{}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, err := lookInside(t, r, p, "0")
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			uids[o.UID] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	if !uids[boxUID] || !uids[boxUID-1] {
		t.Errorf("uids %v", uids)
	}
	noTrace(t, r, before)
}

// spec: the self-check fails when the syscall filter or the memory limit
// is missing in the box
func TestCheckNeedsFilterAndLimit(t *testing.T) {
	r := boxRuntime(t)
	lay, _, _ := discover()
	ro := r.run(context.Background(), lay, "", "probe.php", true, request{Mode: "probe", Paths: []string{"/share"}, Direct: "192.0.2.1:80"})
	a, _, err := parseAnswer(ro.answer)
	if err != nil {
		t.Fatal(err)
	}
	var p probeView
	if err := json.Unmarshal(a.Probe, &p); err != nil {
		t.Fatal(err)
	}
	if !r.judge(lay, &p, ro.relay, int(ro.uid)) {
		t.Fatalf("the real box does not pass: %s", ro.answer)
	}
	for name, change := range map[string]func(*probeView){
		"no filter":       func(p *probeView) { p.Seccomp = 0 },
		"strict mode":     func(p *probeView) { p.Seccomp = 1 },
		"FIFO made":       func(p *probeView) { p.FifoMade, p.FifoErrno = true, 0 },
		"FIFO other err":  func(p *probeView) { p.FifoErrno = int(syscall.EACCES) },
		"no memory limit": func(p *probeView) { p.AddressSpace = "unlimited" },
		"another limit":   func(p *probeView) { p.AddressSpace = "4294967296" },
		"more processes":  func(p *probeView) { p.MaxProcesses = "64" },
		"root":            func(p *probeView) { p.UID = 0 },
	} {
		q := p
		change(&q)
		if r.judge(lay, &q, ro.relay, int(ro.uid)) {
			t.Errorf("%s passes", name)
		}
	}
	if r.judge(lay, &p, ro.relay, 0) {
		t.Error("uid 0 expected and passed")
	}
}

// testThread is an add-on that makes curl look up a name itself, which
// takes a thread of its own, and reports curl's error and its limits.
const testThread = `<?php
class DcThread implements ISite, ISearch
{
    public function __construct($url = null, $username = null, $password = null, $meta = null)
    {
    }

    public function Search($keyword, $limit, $category)
    {
        $ch = curl_init('http://example.invalid/');
        curl_setopt($ch, CURLOPT_PROXY, '');
        curl_setopt($ch, CURLOPT_NOPROXY, '*');
        curl_setopt($ch, CURLOPT_TIMEOUT, 3);
        curl_setopt($ch, CURLOPT_RETURNTRANSFER, true);
        curl_exec($ch);
        $lim = posix_getrlimit();
        $l = new SearchLink();
        $l->name = json_encode(array('error' => curl_error($ch), 'maxproc' => (string)$lim['soft maxproc']));
        $l->link = 'magnet:?xt=urn:btih:` + hashA + `';
        return array($l);
    }
}
`

// spec: the plugin's process cannot start another process or thread, so
// the memory limit of one process is the slot's
func TestOneProcess(t *testing.T) {
	r := boxRuntime(t)
	meta := `{"name":"Thread","class":"DcThread","addon":"thread.php","version":100,"interface":["ISearch"],"description":{"ENG":"thread"}}`
	p := install(t, "thread.addon", addonOf(t, files(map[string]string{"addon.json": meta, "thread.php": testThread}), true), nil)
	out, err := r.Search(context.Background(), p, Query{Keyword: "x"})
	if err != nil || len(out.Results) != 1 {
		t.Fatalf("%v %+v", err, out)
	}
	var o struct{ Error, Maxproc string }
	json.Unmarshal([]byte(out.Results[0].Title), &o)
	t.Logf("%+v", o)
	if o.Maxproc != "1" || !strings.Contains(o.Error, "thread") {
		t.Errorf("%+v", o)
	}
}

// The slots' uids: the default pair, another pair in the reserved range,
// never anything outside it
func TestSlotUIDRange(t *testing.T) {
	for base, want := range map[uint32][2]uint32{0: {59999, 59998}, 59997: {59997, 59996}, 59991: {59991, 59990},
		59990: {59999, 59998}, 1000: {59999, 59998}, 60000: {59999, 59998}} {
		r := New(Config{UIDBase: base})
		if got := [2]uint32{r.uidOf(0), r.uidOf(1)}; got != want {
			t.Errorf("base %d: %v", base, got)
		}
	}
}
