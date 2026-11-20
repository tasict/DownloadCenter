//go:build linux

package phpbox

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"downloadcenter/internal/netutil"
)

type certAuthority struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newAuthority(t *testing.T, name string) *certAuthority {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return &certAuthority{cert: c, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// site is an https server for host with a certificate of the authority.
func (ca *certAuthority) site(t *testing.T, host string) *httptest.Server {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: host}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), DNSNames: []string{host}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hello") }))
	s.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}

// testTLS asks each host over https, with the handle the shim prepares
// and with one of its own, and reports what came back.
const testTLS = `<?php
class DcTLS implements ISite, ISearch
{
    public function __construct($url = null, $username = null, $password = null, $meta = null)
    {
    }

    public function Search($keyword, $limit, $category)
    {
        $o = array();
        foreach (array('bundle.test', 'system.test', 'other.test') as $h) {
            $ajax = new Ajax();
            $o['shim_' . $h] = (string)$ajax->get('https://' . $h . '/');
            $ch = curl_init('https://' . $h . '/');
            curl_setopt($ch, CURLOPT_RETURNTRANSFER, true);
            curl_setopt($ch, CURLOPT_TIMEOUT, 5);
            $o['own_' . $h] = (string)curl_exec($ch);
        }
        $l = new SearchLink();
        $l->name = json_encode($o);
        $l->link = 'magnet:?xt=urn:btih:` + hashA + `';
        return array($l);
    }
}
`

// spec: PHP in the box trusts the system's authorities and the package's
// CA list, with the shim's handles and its own
func TestBoxTrustsMergedList(t *testing.T) {
	r := boxRuntime(t)
	before := mounts(t)
	bundleCA, systemCA, otherCA := newAuthority(t, "bundle"), newAuthority(t, "system"), newAuthority(t, "other")
	sites := map[string]*httptest.Server{"bundle.test": bundleCA.site(t, "bundle.test"), "system.test": systemCA.site(t, "system.test"),
		"other.test": otherCA.site(t, "other.test")}
	r.relay.testDial = func(ctx context.Context, host, port string) (net.Conn, bool) {
		s := sites[host]
		if s == nil || port != "443" {
			return nil, false
		}
		var d net.Dialer
		c, err := d.DialContext(ctx, "tcp", strings.TrimPrefix(s.URL, "https://"))
		return c, err == nil
	}
	// The system's store of this test: one authority in a directory
	dir := t.TempDir()
	sys := filepath.Join(dir, "system")
	os.Mkdir(sys, 0o755)
	os.WriteFile(filepath.Join(sys, "system.pem"), systemCA.pem, 0o644)
	os.Symlink("system.pem", filepath.Join(sys, "abcdef01.0"))
	old := systemCerts
	systemCerts = sys
	defer func() { systemCerts = old }()
	bundle := filepath.Join(dir, "cacert.pem")
	os.WriteFile(bundle, bundleCA.pem, 0o600)
	netutil.SetExtraRoots(bundle)
	defer netutil.SetExtraRoots(filepath.Join(dir, "missing"))

	meta := `{"name":"TLS","class":"DcTLS","addon":"tls.php","version":100,"interface":["ISearch"],"description":{"ENG":"tls"}}`
	p := install(t, "tls.addon", addonOf(t, files(map[string]string{"addon.json": meta, "tls.php": testTLS}), true), nil)
	look := func() map[string]string {
		out, err := r.Search(context.Background(), p, Query{Keyword: "x"})
		if err != nil || len(out.Results) != 1 {
			t.Fatalf("%v %+v", err, out)
		}
		o := map[string]string{}
		json.Unmarshal([]byte(out.Results[0].Title), &o)
		return o
	}
	o := look()
	t.Logf("with the list: %v", o)
	for k, want := range map[string]string{"shim_bundle.test": "hello", "own_bundle.test": "hello", "shim_system.test": "hello",
		"own_system.test": "hello", "shim_other.test": "", "own_other.test": ""} {
		if o[k] != want {
			t.Errorf("%s: %q", k, o[k])
		}
	}
	// The merged file holds each certificate once
	b, _ := os.ReadFile(filepath.Join(r.cfg.Dir, caFileName))
	if strings.Count(string(b), "BEGIN CERTIFICATE") != 2 {
		t.Errorf("merged file:\n%s", b)
	}

	// Without a list the box is as before: the system's directory only
	netutil.SetExtraRoots(filepath.Join(dir, "missing"))
	systemCerts = old
	o = look()
	t.Logf("without: %v", o)
	if o["shim_bundle.test"] != "" || o["own_bundle.test"] != "" || o["shim_system.test"] != "" {
		t.Errorf("without the list: %v", o)
	}
	noTrace(t, r, before)
}
