package phpbox

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
	"testing"
)

// Test packages are built here from source strings: no binary files are
// kept in the repository.

type entry struct {
	name string
	body string
	typ  byte // tar.TypeReg when 0
	link string
}

func tarOf(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: e.typ, Linkname: e.link}
		if e.typ == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Typeflag != tar.TypeReg {
			h.Size = 0
		}
		if h.Typeflag == tar.TypeDir {
			h.Mode = 0o755
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	return buf.Bytes()
}

func files(m map[string]string) []entry {
	var names []string
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []entry
	for _, n := range names {
		out = append(out, entry{name: n, body: m[n]})
	}
	return out
}

// dlmOf packs a search module: gzip'd tar.
func dlmOf(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(tarOf(t, entries))
	zw.Close()
	return buf.Bytes()
}

var (
	keyOnce sync.Once
	testKey *rsa.PrivateKey
)

// addonKeyPair is a 1024-bit key made once per test binary.
func addonKeyPair(t *testing.T) *rsa.PrivateKey {
	keyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 1024)
		if err != nil {
			panic(err)
		}
		testKey = k
	})
	return testKey
}

func keyPEM(t *testing.T, k *rsa.PrivateKey) string {
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// addonOf packs an add-on the way its packer does: the tar (with the
// public key in it), then the signature of the hex SHA-1 of the tar and a
// new line, made with the private key without a DigestInfo.
func addonOf(t *testing.T, entries []entry, newline bool) []byte {
	t.Helper()
	k := addonKeyPair(t)
	body := tarOf(t, append(entries, entry{name: "addon.key", body: keyPEM(t, k)}))
	sum := sha1.Sum(body)
	msg := hex.EncodeToString(sum[:])
	if newline {
		msg += "\n"
	}
	sig, err := rsa.SignPKCS1v15(nil, k, crypto.Hash(0), []byte(msg))
	if err != nil {
		t.Fatal(err)
	}
	return append(body, sig...)
}
