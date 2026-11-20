package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The client address Apache passes on is believed only with the key it
// proves itself with; anyone else on the loopback port is just loopback.
func TestClientIPNeedsProxyKey(t *testing.T) {
	dir := t.TempDir()
	if err := LoadProxyAuth(dir); err != nil {
		t.Fatal(err)
	}
	defer SetProxyAuth("")
	b, err := os.ReadFile(filepath.Join(dir, ProxyAuthFile))
	st, _ := os.Stat(filepath.Join(dir, ProxyAuthFile))
	if err != nil || !regexp.MustCompile(`^[0-9a-f]{32}\n$`).Match(b) || st.Mode().Perm() != 0600 {
		t.Fatalf("key file %q %v %v", b, err, st.Mode())
	}
	key := ReadProxyAuth(dir)
	// Kept across starts
	if LoadProxyAuth(dir); ReadProxyAuth(dir) != key {
		t.Error("key changed at the next start")
	}
	req := func(auth string) string {
		r := httptest.NewRequest("GET", "/x", nil)
		r.RemoteAddr = "127.0.0.1:5000"
		r.Header.Set("X-DC-Remote", "1.2.3.4")
		r.Header.Set("X-Forwarded-For", "5.6.7.8")
		if auth != "" {
			r.Header.Set("X-DC-Auth", auth)
		}
		return ClientIP(r)
	}
	if ip := req(""); ip != "127.0.0.1" {
		t.Errorf("no key: %s", ip)
	}
	if ip := req("00000000000000000000000000000000"); ip != "127.0.0.1" {
		t.Errorf("wrong key: %s", ip)
	}
	if ip := req(key); ip != "1.2.3.4" {
		t.Errorf("with the key: %s", ip)
	}
	SetProxyAuth("")
	if ip := req(""); ip != "127.0.0.1" {
		t.Errorf("no key configured: %s", ip)
	}
}
