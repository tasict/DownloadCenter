package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// ProxyAuthFile, in the data folder, holds the value Apache and ds.cgi send
// in the X-DC-Auth header: 32 hexadecimal characters and a newline (0600).
// It proves that a request came through them, so that the client address
// and scheme they pass on (X-DC-Remote, X-DC-Scheme, X-Forwarded-*) can be
// believed. Any other process on the NAS can reach the loopback port too.
const ProxyAuthFile = "proxy_auth"

var (
	proxyKey     atomic.Value // string
	untrustedLog sync.Once
)

// ReadProxyAuth reads the value from the data folder ("" when the file is
// missing or not in its form).
func ReadProxyAuth(dataDir string) string {
	b, err := os.ReadFile(filepath.Join(dataDir, ProxyAuthFile))
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(b))
	if !validProxyKey(v) {
		return ""
	}
	return v
}

func validProxyKey(v string) bool {
	if len(v) != 32 {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil
}

// LoadProxyAuth reads the value, making a new one when the file is missing
// or not in its form, and makes it the one requests must carry.
func LoadProxyAuth(dataDir string) error {
	v := ReadProxyAuth(dataDir)
	if v == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		v = hex.EncodeToString(b)
		p := filepath.Join(dataDir, ProxyAuthFile)
		tmp := p + ".new"
		os.Remove(tmp)
		if err := os.WriteFile(tmp, []byte(v+"\n"), 0600); err != nil {
			return err
		}
		if err := os.Rename(tmp, p); err != nil {
			return err
		}
	}
	os.Chmod(filepath.Join(dataDir, ProxyAuthFile), 0600)
	SetProxyAuth(v)
	return nil
}

// SetProxyAuth sets the value requests must carry ("" = none is believed).
func SetProxyAuth(v string) { proxyKey.Store(v) }

// FromProxy reports a request that carries the proxy's value in X-DC-Auth.
func FromProxy(r *http.Request) bool {
	k, _ := proxyKey.Load().(string)
	got := r.Header.Get("X-DC-Auth")
	return k != "" && got != "" && subtle.ConstantTimeCompare([]byte(k), []byte(got)) == 1
}

// errNoProxyKey is logged once when proxy headers arrive without the value.
var errNoProxyKey = errors.New("a request carries proxy headers (X-DC-Remote or X-Forwarded-For) without a valid X-DC-Auth: they are not believed; check that the Apache configuration of Download Center is current")

func noteUntrusted(r *http.Request) {
	if r.Header.Get("X-DC-Remote") != "" || r.Header.Get("X-Forwarded-For") != "" {
		untrustedLog.Do(func() { log.Printf("api: %v", errNoProxyKey) })
	}
}
