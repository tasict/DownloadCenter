package netutil

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test binary act as a child with a system store of the
// test's own (SSL_CERT_FILE, SSL_CERT_DIR): Go reads the system's
// authorities once per process.
func TestMain(m *testing.M) {
	if os.Getenv("DC_TEST_ROOTS") != "" {
		os.Exit(rootsChild())
	}
	os.Exit(m.Run())
}

// testCA is a certificate authority that signs server certificates for
// 127.0.0.1 and public.example.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T, name string) *testCA {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return &testCA{cert: c, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// server is an https server with a certificate of the CA.
func (ca *testCA) server(t *testing.T) *httptest.Server {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "server"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"public.example"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	s.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}

// rootsChild makes requests to the servers it is given and prints
// name=ok or name=fail for each.
func rootsChild() int {
	SetExtraRoots(os.Getenv("DC_TEST_BUNDLE"))
	lookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
		if host == "public.example" {
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	var out []string
	for _, spec := range strings.Fields(os.Getenv("DC_TEST_TARGETS")) {
		// name|url|proxy|guard
		f := strings.Split(spec, "|")
		cl := &http.Client{Transport: Transport(5*time.Second, f[3] == "1", f[2]), Timeout: 5 * time.Second}
		res := "ok"
		if resp, err := cl.Get(f[1]); err != nil {
			res = "fail"
		} else {
			resp.Body.Close()
		}
		out = append(out, f[0]+"="+res)
	}
	fmt.Println(strings.Join(out, " "))
	return 0
}

// spec: a server is trusted when its authority is in the system's store or
// in the package's CA list; a missing list leaves the system's
func TestExtraRoots(t *testing.T) {
	bundleCA, systemCA, otherCA := newTestCA(t, "bundle"), newTestCA(t, "system"), newTestCA(t, "other")
	inBundle, inSystem, unknown := bundleCA.server(t), systemCA.server(t), otherCA.server(t)
	dir := t.TempDir()
	sysFile, bundle := filepath.Join(dir, "system.pem"), filepath.Join(dir, "cacert.pem")
	os.WriteFile(sysFile, systemCA.pem, 0o600)
	os.WriteFile(bundle, append([]byte("# a CA list\n"), bundleCA.pem...), 0o600)
	// A proxy that tunnels to the server with the bundle's authority,
	// whatever the target
	p := newRecordingProxy(t)
	p.backend = strings.TrimPrefix(inBundle.URL, "https://")
	targets := strings.Join([]string{
		"bundle|" + inBundle.URL + "/||0", "system|" + inSystem.URL + "/||0", "other|" + unknown.URL + "/||0",
		"bundle_via_proxy|" + inBundle.URL + "/|" + p.srv.URL + "|0",
		"bundle_checked_via_proxy|https://public.example/|" + p.srv.URL + "|1",
	}, " ")
	run := func(bundlePath string) map[string]string {
		cmd := exec.Command(os.Args[0])
		cmd.Env = []string{"DC_TEST_ROOTS=1", "DC_TEST_BUNDLE=" + bundlePath, "DC_TEST_TARGETS=" + targets,
			"SSL_CERT_FILE=" + sysFile, "SSL_CERT_DIR=" + filepath.Join(dir, "none")}
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, b)
		}
		got := map[string]string{}
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		for _, kv := range strings.Fields(lines[len(lines)-1]) {
			k, v, _ := strings.Cut(kv, "=")
			got[k] = v
		}
		t.Logf("%s", b)
		return got
	}
	want := map[string]string{"bundle": "ok", "system": "ok", "other": "fail", "bundle_via_proxy": "ok", "bundle_checked_via_proxy": "ok"}
	if got := run(bundle); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("with the list: %v", got)
	}
	want = map[string]string{"bundle": "fail", "system": "ok", "other": "fail", "bundle_via_proxy": "fail", "bundle_checked_via_proxy": "fail"}
	for _, missing := range []string{filepath.Join(dir, "missing.pem"), sysFile + ".empty"} {
		if missing == sysFile+".empty" {
			os.WriteFile(missing, []byte("no certificate here\n"), 0o600)
		}
		if got := run(missing); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("without a usable list (%s): %v", filepath.Base(missing), got)
		}
	}
}

// The pool is the system's and the list's; without a list nothing changes.
func TestExtraRootsState(t *testing.T) {
	defer extraRoots.Store(nil)
	ca := newTestCA(t, "bundle")
	path := filepath.Join(t.TempDir(), "cacert.pem")
	os.WriteFile(path, ca.pem, 0o600)
	SetExtraRoots(path)
	if RootCAs() == nil || string(ExtraRootsPEM()) != string(ca.pem) || TLSConfig().RootCAs != RootCAs() {
		t.Error("list not loaded")
	}
	SetExtraRoots(path + ".missing")
	if RootCAs() != nil || ExtraRootsPEM() != nil || TLSConfig().RootCAs != nil {
		t.Error("a missing list left something")
	}
}
