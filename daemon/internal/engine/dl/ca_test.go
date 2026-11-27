package dl

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"downloadcenter/internal/engine"
)

// tlsServer serves fs over HTTPS on 127.0.0.1 with a certificate signed by
// a new authority; it returns the authority (PEM).
func tlsServer(t *testing.T, fs *fileServer) (*httptest.Server, []byte) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "Test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano() + 1), Subject: pkix.Name{CommonName: "server"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(fs)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
}

// HTTPS servers are verified against the system's certificate authorities
// and the package's list together: one signed by an authority only in the
// list is reached, one signed by an authority only in the system's file
// still is, one signed by neither is not.
func TestCAList(t *testing.T) {
	data := []byte("over https")
	listed, listedCA := tlsServer(t, &fileServer{data: data})
	system, systemCA := tlsServer(t, &fileServer{data: data})
	unknown, _ := tlsServer(t, &fileServer{data: data})
	scratch := t.TempDir()
	list := filepath.Join(scratch, "cacert.pem")
	os.WriteFile(list, listedCA, 0644)
	sys := filepath.Join(scratch, "system.pem")
	os.WriteFile(sys, systemCA, 0644)
	t.Setenv("SSL_CERT_FILE", sys) // the system's file, as dc-dl finds it

	dir, err := os.MkdirTemp("", "dl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	e := New(dcdlBin(t), dir)
	e.sc.caFile = list
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Stop() })
	for _, c := range []struct {
		ref string
		srv *httptest.Server
		ok  bool
	}{{"ca1", listed, true}, {"ca2", system, true}, {"ca3", unknown, false}} {
		out := t.TempDir()
		e.Add(c.ref, engine.AddRequest{URIs: []string{c.srv.URL + "/f.bin"}, Dir: out})
		if !c.ok {
			st := waitFor(t, e, c.ref, engine.Error, 10*time.Second)
			// The server sends no authority dc-dl knows: named, with the reason
			if st.ErrorMsg != "The certificate of 127.0.0.1 cannot be verified: the server does not send its complete certificate chain, or its certificate authority is not trusted" {
				t.Fatalf("unknown authority: %s %s", st.ErrorCode, st.ErrorMsg)
			}
			continue
		}
		waitFor(t, e, c.ref, engine.Complete, 10*time.Second)
		checkFile(t, filepath.Join(out, "f.bin"), data)
	}
	// Whoever can change the merged list could add authorities: only root
	if fi, err := os.Stat(filepath.Join(dir, "run", "dc-dl-ca.pem")); err != nil || fi.Mode().Perm() != 0600 {
		t.Fatalf("merged list: %v %v", fi.Mode(), err)
	}
}

// A certificate libcurl refuses (CURLcode 60) is explained by its reason
// and names the server that sent it: after a redirect, not the link's host.
func TestCertError(t *testing.T) {
	for _, c := range []struct{ reason, want string }{
		{"SSL certificate problem: unable to get local issuer certificate",
			"The certificate of dl.example.net cannot be verified: the server does not send its complete certificate chain, or its certificate authority is not trusted"},
		{"SSL certificate problem: unable to verify the first certificate",
			"The certificate of dl.example.net cannot be verified: the server does not send its complete certificate chain, or its certificate authority is not trusted"},
		{"SSL certificate problem: self-signed certificate",
			"The certificate of dl.example.net cannot be verified: it is self-signed or issued by a certificate authority the NAS does not trust"},
		{"SSL certificate problem: self signed certificate in certificate chain",
			"The certificate of dl.example.net cannot be verified: it is self-signed or issued by a certificate authority the NAS does not trust"},
		{"SSL certificate problem: certificate has expired",
			"The certificate of dl.example.net has expired or is not valid yet; check the NAS's date and time"},
		{"SSL certificate problem: certificate is not yet valid",
			"The certificate of dl.example.net has expired or is not valid yet; check the NAS's date and time"},
		{"SSL: no alternative certificate subject name matches target host name 'dl.example.net'",
			"The certificate of dl.example.net was issued for another host name"},
		{"SSL: certificate subject name 'a.example.net' does not match target hostname 'dl.example.net'",
			"The certificate of dl.example.net was issued for another host name"},
		{"SSL: certificate subject name 'self-signed.has-expired.example' does not match target hostname 'dl.example.net'",
			"The certificate of dl.example.net was issued for another host name"},
		{"SSL certificate problem: certificate revoked",
			"The certificate of dl.example.net cannot be verified (certificate revoked)"},
		{"", "The certificate of dl.example.net cannot be verified (curl error 60)"},
	} {
		e := curlError(60, c.reason, "https://user:pw@dl.example.net:8443/f/x.zip?k=1")
		if e.code != engine.ErrUnknown || e.transient || e.msg != c.want {
			t.Errorf("%q: %s %v %q", c.reason, e.code, e.transient, e.msg)
		}
	}
	if e := curlError(60, "SSL certificate problem: certificate has expired", "https://[2001:db8::1]/f"); !strings.HasPrefix(e.msg, "The certificate of 2001:db8::1 has expired") {
		t.Errorf("IPv6: %q", e.msg)
	}
}

// Versions compare by major and minor; a patch level does not count (so a
// dc-dl 1.3.1 still follows redirects through dcd).
func TestAtLeast(t *testing.T) {
	for v, want := range map[string]bool{"1.2": true, "1.3.1": true, "1.10": true, "2.0": true, "1.1": false, "1.1.9": false, "": false} {
		if atLeast(v, 1, 2) != want {
			t.Errorf("%q: %v", v, !want)
		}
	}
}
