package search

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"downloadcenter/internal/phpbox"
)

// spec: a rebuilt magnet link keeps only trackers on public hosts: no
// localhost, no literal loopback, link-local, private or NAS address
func TestMagnetTrackersFiltered(t *testing.T) {
	own := "192.0.2.77"
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() {
				own = ipn.IP.String()
				break
			}
		}
	}
	bad := []string{"udp://127.0.0.1:6969", "http://localhost/announce", "udp://192.168.1.5:80", "http://[::1]:8080/a",
		"udp://10.0.0.1:1", "http://169.254.169.254/latest", "udp://0x7f.1:6969", "http://" + own + ":8000/announce", "udp://[fd00::1]:1"}
	good := "udp://tracker.example.org:1337"
	m := "magnet:?xt=urn:btih:" + hashA
	for _, b := range bad {
		m += "&tr=" + url.QueryEscape(b)
	}
	m += "&tr=" + url.QueryEscape(good)
	_, tr := parseMagnet(m)
	if len(tr) != 1 || tr[0] != good {
		t.Errorf("trackers kept: %v", tr)
	}
	p := &Plugin{ID: "x"}
	r, ok := p.normalise(raw{title: "T", magnet: m}, nil)
	if !ok || strings.Count(r.Magnet, "&tr=") != 1 {
		t.Errorf("magnet %s", r.Magnet)
	}
}

// spec: a plugin from another NAS that gives 0 seeders and 0 leechers does
// not know them; 0 seeders with leechers stays 0
func TestCodeZeroCounts(t *testing.T) {
	r := rawOf(phpbox.Result{Title: "A", Download: "magnet:?xt=urn:btih:" + hashA, Size: 1, Seeds: 0, Leechs: 0})
	if r.seeders != "" || r.leechers != "" {
		t.Errorf("0/0: %+v", r)
	}
	r = rawOf(phpbox.Result{Title: "A", Download: "magnet:?xt=urn:btih:" + hashA, Size: 1, Seeds: 0, Leechs: 4})
	if r.seeders != "0" || r.leechers != "4" {
		t.Errorf("0/4: %+v", r)
	}
	r = rawOf(phpbox.Result{Title: "A", Download: "magnet:?xt=urn:btih:" + hashA, Size: 1, Seeds: 3, Leechs: 0})
	if r.seeders != "3" || r.leechers != "0" {
		t.Errorf("3/0: %+v", r)
	}
}

// spec: a plugin installed from an address is the one that was checked:
// the install carries the SHA-256 the check reported
func TestURLInstallBound(t *testing.T) {
	e := newAPIEnv(t)
	first := `{"schema":1,"id":"remote","name":"Remote","type":"json","request":{"url":"https://good.example.org/s?q={query}"},
		"response":{"items":"items","fields":{"title":"t","infohash":"h"}}}`
	second := strings.Replace(first, "good.example.org", "evil.example.org", 1)
	var n atomic.Int32
	e.n.handle("plugins.example.org", func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Write([]byte(first))
			return
		}
		w.Write([]byte(second))
	})
	code, out, _ := e.call(t, "admin", "", "POST", "/search/plugins/inspect", map[string]any{"url": "https://plugins.example.org/p.json"})
	sum := sha256.Sum256([]byte(first))
	if code != 200 || out["sha256"] != hex.EncodeToString(sum[:]) || out["hosts"].([]any)[0] != "good.example.org" {
		t.Fatalf("inspect: %d %v", code, out)
	}
	// The address now gives another file
	code, out, _ = e.call(t, "admin", "", "POST", "/search/plugins", map[string]any{"url": "https://plugins.example.org/p.json", "sha256": out["sha256"]})
	if code != 409 || errCode(out) != "search_plugin_changed" {
		t.Errorf("changed: %d %v", code, out)
	}
	if _, ok := e.s.store.Get("remote"); ok {
		t.Fatal("installed what was not checked")
	}
	// Without the hash: the address was not checked first; nothing is
	// fetched
	before := n.Load()
	if code, out, _ := e.call(t, "admin", "", "POST", "/search/plugins", map[string]any{"url": "https://plugins.example.org/p.json"}); code != 400 || errCode(out) != "invalid_request" || errMsg(out) != msgCheckFirst {
		t.Errorf("no hash: %d %v", code, out)
	}
	if n.Load() != before {
		t.Error("fetched without a hash")
	}
	// The same file as checked installs
	sum = sha256.Sum256([]byte(second))
	if code, out, _ := e.call(t, "admin", "", "POST", "/search/plugins", map[string]any{"url": "https://plugins.example.org/p.json", "sha256": strings.ToUpper(hex.EncodeToString(sum[:]))}); code != 200 || out["host"] != "evil.example.org" {
		t.Errorf("same: %d %v", code, out)
	}
	// Pasted JSON needs no hash: the administrator saw it
	j := strings.Replace(first, `"remote"`, `"pasted"`, 1)
	if code, out, _ := e.call(t, "admin", "", "POST", "/search/plugins", map[string]any{"json": j}); code != 200 {
		t.Errorf("json: %d %v", code, out)
	}
}
