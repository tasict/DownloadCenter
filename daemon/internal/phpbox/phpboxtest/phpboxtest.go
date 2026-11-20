// Package phpboxtest builds plugin packages for tests from source strings,
// so that no binary package is kept in the repository. Only tests import
// it.
package phpboxtest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"sort"
	"sync"
)

// Entry is a file of a package; Type is tar.TypeReg when 0.
type Entry struct {
	Name string
	Body string
	Type byte
	Link string
}

// Files are the entries for a map of names to contents, sorted.
func Files(m map[string]string) []Entry {
	var names []string
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []Entry
	for _, n := range names {
		out = append(out, Entry{Name: n, Body: m[n]})
	}
	return out
}

// Tar packs entries.
func Tar(entries []Entry) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		h := &tar.Header{Name: e.Name, Mode: 0o644, Size: int64(len(e.Body)), Typeflag: e.Type, Linkname: e.Link}
		if e.Type == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Typeflag != tar.TypeReg {
			h.Size = 0
		}
		if h.Typeflag == tar.TypeDir {
			h.Mode = 0o755
		}
		tw.WriteHeader(h)
		if h.Typeflag == tar.TypeReg {
			tw.Write([]byte(e.Body))
		}
	}
	tw.Close()
	return buf.Bytes()
}

// DLM packs a search module: a gzip'd tar.
func DLM(entries []Entry) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(Tar(entries))
	zw.Close()
	return buf.Bytes()
}

var (
	keyOnce sync.Once
	key     *rsa.PrivateKey
)

// Key is a 1024-bit key made once per test binary.
func Key() *rsa.PrivateKey {
	keyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 1024)
		if err != nil {
			panic(err)
		}
		key = k
	})
	return key
}

// NewKey is another 1024-bit key.
func NewKey() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		panic(err)
	}
	return k
}

// Addon packs an add-on as its packer does: the tar with the public key
// in it, then the signature of the hex SHA-1 of the tar and a new line.
func Addon(entries []Entry, k *rsa.PrivateKey) []byte {
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		panic(err)
	}
	body := Tar(append(entries, Entry{Name: "addon.key", Body: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}))
	sum := sha1.Sum(body)
	sig, err := rsa.SignPKCS1v15(nil, k, crypto.Hash(0), []byte(hex.EncodeToString(sum[:])+"\n"))
	if err != nil {
		panic(err)
	}
	return append(body, sig...)
}
