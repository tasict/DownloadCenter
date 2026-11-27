package dl

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"downloadcenter/internal/engine"
)

// leafServer serves fs over HTTPS with only the server's own certificate,
// no intermediates (what cee5.copycase.com does).
func leafServer(t *testing.T, fs *fileServer, leaf *node) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(fs)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leaf.der}, PrivateKey: leaf.key}}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// chainEngine starts an engine that trusts the given roots (as the
// package's list) and answers caIssuers addresses from issuers.
func chainEngine(t *testing.T, issuers map[string][]byte, roots ...*node) (*Engine, *fetchLog) {
	t.Helper()
	dir, err := os.MkdirTemp("", "dl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	var list []byte
	for _, r := range roots {
		list = append(list, r.pem()...)
	}
	ca := filepath.Join(t.TempDir(), "cacert.pem")
	os.WriteFile(ca, list, 0644)
	e := New(dcdlBin(t), dir)
	e.sc.caFile = ca
	fl := &fetchLog{}
	e.chains.fetch = func(ctx context.Context, u, proxy string) ([]byte, error) {
		fl.mu.Lock()
		fl.urls = append(fl.urls, u)
		fl.mu.Unlock()
		if b, ok := issuers[u]; ok {
			return b, nil
		}
		return nil, errors.New("not found")
	}
	e.Logf = func(ref, msg string) {
		fl.mu.Lock()
		fl.logs = append(fl.logs, msg)
		fl.mu.Unlock()
	}
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Stop() })
	return e, fl
}

type fetchLog struct {
	mu   sync.Mutex
	urls []string
	logs []string
}

func (f *fetchLog) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.urls)
}

func errorOf(t *testing.T, e *Engine, ref string) string {
	t.Helper()
	return waitFor(t, e, ref, engine.Error, 15*time.Second).ErrorMsg
}

// A server that leaves out its intermediate is completed from the address
// its certificate names, and the task log says so.
func TestAIACompletesChain(t *testing.T) {
	root := newCA(t, "Root", nil, "")
	inter := newCA(t, "Inter", root, "")
	leaf := newLeaf(t, inter, "http://ca.example/inter.crt", nil, time.Time{}, time.Time{})
	data := randomData(t, 3<<20)
	srv := leafServer(t, &fileServer{data: data, ranges: true}, leaf)
	e, fl := chainEngine(t, map[string][]byte{"http://ca.example/inter.crt": inter.der}, root)
	out := t.TempDir()
	e.Add("a1", engine.AddRequest{URIs: []string{srv.URL + "/f.bin"}, Dir: out})
	waitFor(t, e, "a1", engine.Complete, 15*time.Second)
	checkFile(t, filepath.Join(out, "f.bin"), data)
	if fl.count() != 1 {
		t.Fatalf("fetched %d times: %v", fl.count(), fl.urls)
	}
	if len(fl.logs) != 1 || !strings.Contains(fl.logs[0], "The server 127.0.0.1 does not send its complete certificate chain") {
		t.Fatalf("log: %q", fl.logs)
	}
	// The next task to the host carries the candidate at once
	e.Add("a2", engine.AddRequest{URIs: []string{srv.URL + "/g.bin"}, Dir: t.TempDir()})
	waitFor(t, e, "a2", engine.Complete, 15*time.Second)
	if fl.count() != 1 {
		t.Fatalf("fetched again: %v", fl.urls)
	}
}

// Two levels missing: each issuer names the next one.
func TestAIATwoLevels(t *testing.T) {
	root := newCA(t, "Root", nil, "")
	i1 := newCA(t, "Inter 1", root, "")
	i2 := newCA(t, "Inter 2", i1, "http://ca.example/i1.crt")
	leaf := newLeaf(t, i2, "http://ca.example/i2.p7c", nil, time.Time{}, time.Time{})
	srv := leafServer(t, &fileServer{data: []byte("two levels")}, leaf)
	e, fl := chainEngine(t, map[string][]byte{"http://ca.example/i2.p7c": p7c(t, i2.der), "http://ca.example/i1.crt": i1.pem()}, root)
	out := t.TempDir()
	e.Add("t1", engine.AddRequest{URIs: []string{srv.URL + "/f.bin"}, Dir: out})
	waitFor(t, e, "t1", engine.Complete, 15*time.Second)
	checkFile(t, filepath.Join(out, "f.bin"), []byte("two levels"))
	if fl.count() != 2 {
		t.Fatalf("fetched: %v", fl.urls)
	}
}

// Fetched certificates never become authorities: a chain an attacker
// supplies in full (its own intermediate of the same name, and that one's
// self-signed root) still fails.
func TestAIAForgedIssuer(t *testing.T) {
	root := newCA(t, "Root", nil, "")
	evil := newCA(t, "Root", nil, "")                               // same name, not trusted
	fake := newCA(t, "Inter", evil, "http://evil.example/root.crt") // signs the server's certificate
	leaf := newLeaf(t, fake, "http://evil.example/inter.crt", nil, time.Time{}, time.Time{})
	srv := leafServer(t, &fileServer{data: []byte("x")}, leaf)
	e, fl := chainEngine(t, map[string][]byte{"http://evil.example/inter.crt": fake.der, "http://evil.example/root.crt": evil.der}, root)
	e.Add("f1", engine.AddRequest{URIs: []string{srv.URL + "/f.bin"}, Dir: t.TempDir()})
	msg := errorOf(t, e, "f1")
	if !strings.HasPrefix(msg, "The certificate of 127.0.0.1 cannot be verified") {
		t.Fatalf("message: %q", msg)
	}
	if fl.count() == 0 {
		t.Fatal("the forged issuer was not even tried")
	}
	// A self-signed authority handed out as the issuer is not trusted either
	self := newCA(t, "Self", nil, "")
	leaf2 := newLeaf(t, self, "http://evil.example/self.crt", nil, time.Time{}, time.Time{})
	srv2 := leafServer(t, &fileServer{data: []byte("x")}, leaf2)
	e2, _ := chainEngine(t, map[string][]byte{"http://evil.example/self.crt": self.der}, root)
	e2.Add("f2", engine.AddRequest{URIs: []string{srv2.URL + "/f.bin"}, Dir: t.TempDir()})
	if msg := errorOf(t, e2, "f2"); !strings.HasPrefix(msg, "The certificate of 127.0.0.1 cannot be verified") {
		t.Fatalf("self-signed: %q", msg)
	}
}

// Completing the chain changes nothing else: an expired certificate or
// one for another name still fails, with its own reason.
func TestAIAStillChecks(t *testing.T) {
	root := newCA(t, "Root", nil, "")
	inter := newCA(t, "Inter", root, "")
	issuers := map[string][]byte{"http://ca.example/inter.crt": inter.der}
	expired := newLeaf(t, inter, "http://ca.example/inter.crt", nil, time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour))
	other := newLeaf(t, inter, "http://ca.example/inter.crt", []string{"other.example"}, time.Time{}, time.Time{})
	s1 := leafServer(t, &fileServer{data: []byte("x")}, expired)
	s2 := leafServer(t, &fileServer{data: []byte("x")}, other)
	e, fl := chainEngine(t, issuers, root)
	e.Add("x1", engine.AddRequest{URIs: []string{s1.URL + "/f.bin"}, Dir: t.TempDir()})
	if msg := errorOf(t, e, "x1"); msg != "The certificate of 127.0.0.1 has expired or is not valid yet; check the NAS's date and time" {
		t.Fatalf("expired: %q", msg)
	}
	if fl.count() != 1 {
		t.Fatalf("the issuer was not fetched first: %v", fl.urls)
	}
	e.Add("x2", engine.AddRequest{URIs: []string{s2.URL + "/f.bin"}, Dir: t.TempDir()})
	if msg := errorOf(t, e, "x2"); msg != "The certificate of 127.0.0.1 was issued for another host name" {
		t.Fatalf("name: %q", msg)
	}
}

// A task that skips the check downloads from a self-signed, an expired
// and a misnamed server; a verifying task to the same server afterwards
// does not ride on its connection.
func TestSkipCertCheck(t *testing.T) {
	root := newCA(t, "Root", nil, "")
	unknown := newCA(t, "Unknown", nil, "")
	selfSigned := leafServer(t, &fileServer{data: []byte("self")}, newLeaf(t, unknown, "", nil, time.Time{}, time.Time{}))
	expired := leafServer(t, &fileServer{data: []byte("old")}, newLeaf(t, root, "", nil, time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour)))
	misnamed := leafServer(t, &fileServer{data: []byte("name")}, newLeaf(t, root, "", []string{"other.example"}, time.Time{}, time.Time{}))
	e, fl := chainEngine(t, nil, root)
	for i, c := range []struct {
		srv  *httptest.Server
		data string
	}{{selfSigned, "self"}, {expired, "old"}, {misnamed, "name"}} {
		ref := "s" + string(rune('1'+i))
		out := t.TempDir()
		e.Add(ref, engine.AddRequest{URIs: []string{c.srv.URL + "/f.bin"}, Dir: out, SkipCertCheck: true})
		waitFor(t, e, ref, engine.Complete, 15*time.Second)
		checkFile(t, filepath.Join(out, "f.bin"), []byte(c.data))
	}
	if fl.count() != 0 {
		t.Fatalf("a skipping task fetched issuers: %v", fl.urls)
	}
	// Same server, same dc-dl, right after: the pooled connection and the
	// TLS session of the skipping task are not reused
	e.Add("v1", engine.AddRequest{URIs: []string{selfSigned.URL + "/f.bin"}, Dir: t.TempDir()})
	if msg := errorOf(t, e, "v1"); !strings.HasPrefix(msg, "The certificate of 127.0.0.1 cannot be verified") {
		t.Fatalf("verifying task: %q", msg)
	}
}

// dc-dl 1.5 reports its new abilities and refuses a malformed chain.
func TestSidecarChainFields(t *testing.T) {
	s := startDCDL(t)
	if v := s.versions(); v == nil || !v.Chain || !v.SkipCertCheck {
		t.Fatalf("version: %+v", v)
	}
	for _, line := range []string{
		`{"url":"https://127.0.0.1:1/","chain":["@@@@"]}`,
		`{"url":"https://127.0.0.1:1/","chain":"AAAA"}`,
		`{"url":"https://127.0.0.1:1/","chain":[1]}`,
		`{"url":"https://127.0.0.1:1/","chain":["","","","","","","","",""]}`,
	} {
		types, last := rawRequest(t, s, line)
		var e eframe
		json.Unmarshal(last, &e)
		if !strings.HasSuffix(types, "E") || e.Error != "bad chain" {
			t.Errorf("%s: %s %s", line, types, last)
		}
	}
}

// The system's hashed folder is folded into dc-dl's CA file: a CA only
// there is still trusted (by its hash names only; other files there never
// were), and libcurl then keeps one parsed store for every connection.
func TestHashedFolderCA(t *testing.T) {
	inDir := newCA(t, "In the folder", nil, "")
	notHash := newCA(t, "Not a hash name", nil, "")
	listed := newCA(t, "Listed", nil, "")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "0123abcd.0"), inDir.pem(), 0644)
	os.WriteFile(filepath.Join(dir, "other.pem"), notHash.pem(), 0644)
	sys := filepath.Join(t.TempDir(), "system.pem")
	os.WriteFile(sys, nil, 0644)
	t.Setenv("SSL_CERT_FILE", sys)
	t.Setenv("SSL_CERT_DIR", dir)
	a := leafServer(t, &fileServer{data: []byte("a")}, newLeaf(t, inDir, "", nil, time.Time{}, time.Time{}))
	b := leafServer(t, &fileServer{data: []byte("b")}, newLeaf(t, notHash, "", nil, time.Time{}, time.Time{}))
	c1 := leafServer(t, &fileServer{data: []byte("c")}, newLeaf(t, listed, "", nil, time.Time{}, time.Time{}))
	c2 := leafServer(t, &fileServer{data: []byte("c")}, newLeaf(t, listed, "", nil, time.Time{}, time.Time{}))
	e, _ := chainEngine(t, nil, listed)
	e.Add("h1", engine.AddRequest{URIs: []string{a.URL + "/f"}, Dir: t.TempDir()})
	waitFor(t, e, "h1", engine.Complete, 15*time.Second)
	e.Add("h2", engine.AddRequest{URIs: []string{b.URL + "/f"}, Dir: t.TempDir()})
	errorOf(t, e, "h2")
	e.Add("h3", engine.AddRequest{URIs: []string{c1.URL + "/f"}, Dir: t.TempDir()})
	waitFor(t, e, "h3", engine.Complete, 15*time.Second)
	// The CA cache: with the file emptied, a new connection to another
	// server is still verified from the store parsed before
	if err := os.WriteFile(filepath.Join(e.sc.dataDir, "run", "dc-dl-ca.pem"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	e.Add("h4", engine.AddRequest{URIs: []string{c2.URL + "/f"}, Dir: t.TempDir()})
	waitFor(t, e, "h4", engine.Complete, 15*time.Second)
}
