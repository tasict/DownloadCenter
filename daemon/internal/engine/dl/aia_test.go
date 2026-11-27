package dl

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// node is a certificate of a test PKI with its key.
type node struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	der  []byte
}

var serial atomic.Int64

// newCA makes an authority named name, signed by parent (nil: a root);
// aia is the caIssuers address it names ("" none).
func newCA(t *testing.T, name string, parent *node, aia string) *node {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano() + serial.Add(1)), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	if aia != "" {
		tpl.IssuingCertificateURL = []string{aia}
	}
	pc, pk := tpl, key
	if parent != nil {
		pc, pk = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, pc, &key.PublicKey, pk)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return &node{cert: c, key: key, der: der}
}

// newLeaf makes a server certificate for 127.0.0.1 (or the names given),
// valid from notBefore to notAfter (zero: around now).
func newLeaf(t *testing.T, parent *node, aia string, names []string, notBefore, notAfter time.Time) *node {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if notBefore.IsZero() {
		notBefore, notAfter = time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano() + serial.Add(1)), Subject: pkix.Name{CommonName: "server"},
		NotBefore: notBefore, NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if names == nil {
		tpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	} else {
		tpl.DNSNames = names
	}
	if aia != "" {
		tpl.IssuingCertificateURL = []string{aia}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, parent.cert, &key.PublicKey, parent.key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return &node{cert: c, key: key, der: der}
}

func (n *node) pem() []byte { return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: n.der}) }

// p7c wraps certificates in a PKCS#7 SignedData like a .p7c file.
func p7c(t *testing.T, certs ...[]byte) []byte {
	t.Helper()
	var all []byte
	for _, c := range certs {
		all = append(all, c...)
	}
	data, _ := asn1.Marshal(struct{ T asn1.ObjectIdentifier }{asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}})
	sd, err := asn1.Marshal(struct {
		Version int
		Digests asn1.RawValue
		Content asn1.RawValue
		Certs   asn1.RawValue
		Signers asn1.RawValue
	}{1, asn1.RawValue{Tag: asn1.TagSet, IsCompound: true}, asn1.RawValue{FullBytes: data},
		asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: all},
		asn1.RawValue{Tag: asn1.TagSet, IsCompound: true}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := asn1.Marshal(struct {
		T       asn1.ObjectIdentifier
		Content asn1.RawValue
	}{oidSignedData, asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: sd}})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// An issuer comes as DER, PEM or PKCS#7; anything else gives nothing.
func TestParseIssuer(t *testing.T) {
	root := newCA(t, "Root", nil, "")
	inter := newCA(t, "Inter", root, "")
	for name, c := range map[string]struct {
		body []byte
		n    int
	}{
		"der":   {inter.der, 1},
		"pem":   {append(inter.pem(), root.pem()...), 2},
		"p7c":   {p7c(t, inter.der, root.der), 2},
		"junk":  {[]byte("<html>not found</html>"), 0},
		"empty": {nil, 0},
		"half":  {inter.der[:len(inter.der)/2], 0},
	} {
		got := parseCerts(c.body)
		if len(got) != c.n {
			t.Errorf("%s: %d certificates, want %d", name, len(got), c.n)
		}
		if c.n > 0 && len(got) > 0 && !got[0].Equal(inter.cert) {
			t.Errorf("%s: first is %s", name, got[0].Subject)
		}
	}
}

func need(n *node, urls ...string) *needIssuer {
	return &needIssuer{Cert: base64.StdEncoding.EncodeToString(n.der), URLs: urls}
}

// Only a CA certificate whose key signed the reported certificate is kept;
// an address that gave nothing usable is not asked again for a while.
func TestIssuerMustSign(t *testing.T) {
	root := newCA(t, "Root", nil, "")
	inter := newCA(t, "Inter", root, "")
	other := newCA(t, "Inter", root, "") // same name, another key
	leaf := newLeaf(t, inter, "http://ca.example/inter.crt", nil, time.Time{}, time.Time{})
	c := newChains()
	var calls atomic.Int32
	c.fetch = func(ctx context.Context, u, proxy string) ([]byte, error) {
		calls.Add(1)
		return other.der, nil
	}
	ctx := context.Background()
	if c.complete(ctx, "dl.example", need(leaf, "http://ca.example/inter.crt"), "") {
		t.Fatal("an issuer that did not sign was taken")
	}
	if c.complete(ctx, "dl.example", need(leaf, "http://ca.example/inter.crt"), ""); calls.Load() != 1 {
		t.Fatalf("asked %d times within the negative cache", calls.Load())
	}
	if len(c.forHost("dl.example")) != 0 {
		t.Fatal("a candidate was kept")
	}
	// https addresses are never fetched (and would need a verified chain)
	c2 := newChains()
	c2.fetch = func(ctx context.Context, u, proxy string) ([]byte, error) {
		t.Errorf("fetched %s", u)
		return nil, errors.New("no")
	}
	c2.complete(ctx, "dl.example", need(leaf, "https://ca.example/inter.crt"), "")

	c3 := newChains()
	c3.fetch = func(ctx context.Context, u, proxy string) ([]byte, error) { return inter.der, nil }
	if !c3.complete(ctx, "DL.example", need(leaf, "http://ca.example/inter.crt"), "") || len(c3.forHost("dl.example")) != 1 {
		t.Fatal("the real issuer was not kept for the host")
	}
}

// Connections failing together fetch the issuer once.
func TestAIASingleFlight(t *testing.T) {
	root := newCA(t, "Root", nil, "")
	inter := newCA(t, "Inter", root, "")
	leaf := newLeaf(t, inter, "http://ca.example/inter.crt", nil, time.Time{}, time.Time{})
	c := newChains()
	release := make(chan struct{})
	var calls atomic.Int32
	c.fetch = func(ctx context.Context, u, proxy string) ([]byte, error) {
		calls.Add(1)
		<-release
		return inter.der, nil
	}
	var wg sync.WaitGroup
	var ok atomic.Int32
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c.complete(context.Background(), "dl.example", need(leaf, "http://ca.example/inter.crt"), "") {
				ok.Add(1)
			}
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 || ok.Load() != 4 {
		t.Fatalf("%d fetches, %d completed", calls.Load(), ok.Load())
	}
}

// The address a certificate names is fetched over http from public
// addresses at ports 80 and 443 only, without credentials.
func TestAIAStrictRefusesPrivate(t *testing.T) {
	c := newChains()
	for _, u := range []string{
		"http://192.168.1.1/ca.crt", "http://127.0.0.1/ca.crt", "http://10.0.0.1:80/ca.crt", "http://[::1]/ca.crt",
		"http://169.254.169.254/latest", "http://localhost/ca.crt", "http://ca.example:8080/ca.crt",
		"https://ca.example/ca.crt", "ftp://ca.example/ca.crt", "http://user:pw@ca.example/ca.crt",
	} {
		if _, err := c.get(context.Background(), u, ""); err == nil {
			t.Errorf("%s: fetched", u)
		}
	}
}

// The fetch is never less proxied than the download: a task's own proxy is
// used; a direct task uses the rule for the certificate's address, and
// when that rule requires a proxy and none applies nothing is fetched.
func TestAIAProxy(t *testing.T) {
	root := newCA(t, "Root", nil, "")
	inter := newCA(t, "Inter", root, "")
	leaf := newLeaf(t, inter, "http://ca.example/inter.crt", nil, time.Time{}, time.Time{})
	for _, c := range []struct {
		task, rule, want string
		ruleErr          bool
	}{
		{"socks5h://10.0.0.1:1080", "", "socks5h://10.0.0.1:1080", false},
		{"socks5h://10.0.0.1:1080", "http://10.0.0.2:3128", "socks5h://10.0.0.1:1080", false},
		{"", "http://10.0.0.2:3128", "http://10.0.0.2:3128", false},
		{"", "", "", false},
		{"", "", "-", true},
	} {
		ch := newChains()
		ch.proxy = func(string) (string, error) {
			if c.ruleErr {
				return "", errors.New("proxy required")
			}
			return c.rule, nil
		}
		got := "-"
		ch.fetch = func(ctx context.Context, u, proxy string) ([]byte, error) { got = proxy; return inter.der, nil }
		ch.complete(context.Background(), "dl.example", need(leaf, "http://ca.example/inter.crt"), c.task)
		if got != c.want {
			t.Errorf("task %q, rule %q (error %v): fetched through %q, want %q", c.task, c.rule, c.ruleErr, got, c.want)
		}
	}
}
