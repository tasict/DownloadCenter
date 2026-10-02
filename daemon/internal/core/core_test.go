package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestModeAt(t *testing.T) {
	s := Schedule{Enabled: true}
	for i := range s.Days {
		s.Days[i] = strings.Repeat("1", 24)
	}
	// Monday 03:00 off, Sunday 22:00 limited
	s.Days[0] = "0000" + strings.Repeat("1", 20)
	s.Days[6] = strings.Repeat("1", 22) + "22"
	mon := time.Date(2026, 9, 28, 3, 30, 0, 0, time.Local) // a Monday
	if mon.Weekday() != time.Monday {
		t.Fatal("test date is not a Monday")
	}
	if got := modeAt(s, mon); got != "off" {
		t.Errorf("Monday 03:30 = %s, want off", got)
	}
	if got := modeAt(s, mon.Add(2*time.Hour)); got != "full" {
		t.Errorf("Monday 05:30 = %s, want full", got)
	}
	sun := time.Date(2026, 10, 4, 22, 10, 0, 0, time.Local)
	if got := modeAt(s, sun); got != "limited" {
		t.Errorf("Sunday 22:10 = %s, want limited", got)
	}
	s.Enabled = false
	if got := modeAt(s, mon); got != "full" {
		t.Errorf("disabled schedule = %s, want full", got)
	}
}

func TestNormalizeURL(t *testing.T) {
	cases := map[string]string{
		"HTTP://Example.COM:80/a/B?x=1#frag": "http://example.com/a/B?x=1",
		"https://example.com:443/f.iso":      "https://example.com/f.iso",
		"https://example.com:8443/f.iso":     "https://example.com:8443/f.iso",
		"ftp://Host:21/pub/x":                "ftp://host/pub/x",
	}
	for in, want := range cases {
		if got := NormalizeURL(in); got != want {
			t.Errorf("NormalizeURL(%q) = %q, want %q", in, got, want)
		}
	}
	if URLHash("https://example.com/f.iso#a") != URLHash("HTTPS://EXAMPLE.com:443/f.iso") {
		t.Error("equivalent URLs must have the same hash")
	}
	if KindOfURL("magnet:?xt=urn:btih:abc") != "" || KindOfURL("ftp://h/x") != KindFTP || KindOfURL("https://h/x") != KindHTTP {
		t.Error("KindOfURL")
	}
}

func TestUniquePathAndMove(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	os.MkdirAll(src, 0755)
	os.WriteFile(filepath.Join(src, "a.tar.gz"), []byte("x"), 0644)
	dst := filepath.Join(dir, "dst")
	os.MkdirAll(dst, 0755)
	os.WriteFile(filepath.Join(dst, "a.tar.gz"), []byte("old"), 0644)
	p, err := moveURLResult(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p) != "a (1).tar.gz" {
		t.Errorf("conflict name = %s", filepath.Base(p))
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "a.tar.gz")); string(b) != "old" {
		t.Error("existing file was overwritten")
	}
}

func TestSafeToDelete(t *testing.T) {
	for _, p := range []string{"/", "/share", "/share/Download", "/etc/passwd", "/share/CACHEDEV1_DATA"} {
		if safeToDelete(p) {
			t.Errorf("safeToDelete(%q) = true", p)
		}
	}
}

func TestPeerIdentity(t *testing.T) {
	p, a := peerIdentity(BTSettings{PeerMode: 3})
	if p != "-TR2940-" || a != "Transmission/2.94" {
		t.Errorf("mode 3 = %q %q", p, a)
	}
	p, _ = peerIdentity(BTSettings{PeerMode: 0, PeerID: "LT", PeerVersion: "1.2.18"})
	if p != "-LT1218-" {
		t.Errorf("custom = %q", p)
	}
}
