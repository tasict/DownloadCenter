package lt

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"downloadcenter/internal/engine"
	"downloadcenter/internal/torrent"
)

// dcbtBin is the dc-bt the tests run against: $DC_BT, or the one
// tools/build-dcbt.sh built for this machine.
func dcbtBin(t *testing.T) string {
	b := os.Getenv("DC_BT")
	if b == "" {
		arch := map[string]string{"amd64": "x86_64", "arm64": "arm_64", "arm": "arm-x41"}[runtime.GOARCH]
		b, _ = filepath.Abs("../../../../tools/cache/dc-bt-" + arch)
	}
	if _, err := os.Stat(b); err != nil {
		t.Skip("dc-bt is not built (sh tools/build-dcbt.sh)")
	}
	return b
}

// A private torrent announces to a local tracker with libtorrent's own
// identity, then, after the identity changes and dc-bt starts again, with
// the new one; no announce mixes a peer id and a User-Agent of the two.
func TestRealIdentity(t *testing.T) {
	bin := dcbtBin(t)
	var mu sync.Mutex
	var seen [][2]string
	tracker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, [2]string{r.URL.Query().Get("peer_id"), r.UserAgent()})
		mu.Unlock()
		w.Write([]byte("d8:intervali1800e5:peers0:e"))
	}))
	defer tracker.Close()
	announce := tracker.URL + "/announce"
	tor := []byte("d8:announce" + itoa(len(announce)) + ":" + announce +
		"4:infod6:lengthi16384e4:name1:a12:piece lengthi16384e6:pieces20:" + strings.Repeat("x", 20) + "7:privatei1eee")
	meta, err := torrent.Parse(tor)
	if err != nil || !meta.Private {
		t.Fatalf("test torrent: %v", err)
	}
	dir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	e := newEngine(bin, dir)
	g := engine.Global{PortFrom: 26891, PortTo: 26899}
	e.ApplyGlobal(g)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	wantID, wantAgent := e.OwnIdentity()
	if !strings.HasPrefix(wantID, "-LT") || !strings.HasPrefix(wantAgent, "libtorrent/") {
		t.Fatalf("own identity %q %q", wantID, wantAgent)
	}
	add := func() {
		if _, err := e.Add(meta.InfoHash, engine.AddRequest{Torrent: tor, Dir: filepath.Join(dir, "data")}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor := func(id, agent string) {
		for i := 0; i < 100; i++ {
			mu.Lock()
			for _, s := range seen {
				if strings.HasPrefix(s[0], id) && s[1] == agent {
					mu.Unlock()
					return
				}
			}
			mu.Unlock()
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("no announce as %s %s: %q", id, agent, seen)
	}
	add()
	waitFor(wantID, wantAgent)

	g.PeerID, g.PeerAgent = "-TR2940-", "Transmission/2.94"
	if err := e.ApplyGlobal(g); err == nil || !strings.Contains(err.Error(), "restart required") {
		t.Fatalf("new identity without a restart: %v", err)
	}
	// dc-bt brings its torrents back from their saved state as soon as it starts
	mu.Lock()
	seen = nil
	mu.Unlock()
	if err := e.Restart(g); err != nil {
		t.Fatal(err)
	}
	add()
	waitFor("-TR2940-", "Transmission/2.94")
	mu.Lock()
	defer mu.Unlock()
	// The old dc-bt says goodbye as itself, the new one announces as Transmission: never a mix
	for _, s := range seen {
		old := strings.HasPrefix(s[0], wantID) && s[1] == wantAgent
		tr := strings.HasPrefix(s[0], "-TR2940-") && s[1] == "Transmission/2.94"
		if !old && !tr {
			t.Errorf("mixed identity: %q", seen)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
