package netutil

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"log"
	"os"
	"sync/atomic"
)

// The certificate authorities dcd trusts: the system's, and a CA list that
// comes with the package (an older firmware's own list lacks authorities
// that sites use today). The list is added to the system's, never put in
// its place.

type roots struct {
	pool *x509.CertPool // the system's and the list's
	pem  []byte         // the list as read
}

var extraRoots atomic.Pointer[roots]

// SetExtraRoots loads the CA list at path (PEM) for every TLS connection
// netutil makes from now on. A list that is missing or holds no
// certificate leaves the system's authorities alone, with one line in the
// log. The pool is built once and only read afterwards, so connections
// share it.
func SetExtraRoots(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Printf("tls: no CA list at %s, using the system's certificate authorities only", path)
		extraRoots.Store(nil)
		return
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	n := 0
	for rest := b; ; {
		var block *pem.Block
		if block, rest = pem.Decode(rest); block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		if c, err := x509.ParseCertificate(block.Bytes); err == nil {
			pool.AddCert(c)
			n++
		}
	}
	if n == 0 {
		log.Printf("tls: the CA list at %s holds no certificate, using the system's certificate authorities only", path)
		extraRoots.Store(nil)
		return
	}
	log.Printf("tls: trusting the system's certificate authorities and %d from %s", n, path)
	extraRoots.Store(&roots{pool: pool, pem: b})
}

// RootCAs is the pool servers are verified against: the system's
// authorities and the package's list, or nil (the system's alone) without
// a list.
func RootCAs() *x509.CertPool {
	if r := extraRoots.Load(); r != nil {
		return r.pool
	}
	return nil
}

// ExtraRootsPEM is the package's CA list as loaded (nil without one), for
// TLS clients that are not Go's.
func ExtraRootsPEM() []byte {
	if r := extraRoots.Load(); r != nil {
		return r.pem
	}
	return nil
}

// TLSConfig is a client configuration with RootCAs, for a transport that
// is not netutil's.
func TLSConfig() *tls.Config { return &tls.Config{RootCAs: RootCAs()} }
