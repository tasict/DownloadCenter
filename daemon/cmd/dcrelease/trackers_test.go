package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"downloadcenter/internal/trackers"
)

func TestBuildTrackers(t *testing.T) {
	up := true
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up {
			http.Error(w, "down", 502)
			return
		}
		w.Write([]byte("udp://a.example.org:6969/announce\n\nhttp://10.0.0.1/announce\n\nhttps://b.example.org:443/announce\n\nudp://lan.example.org:1/announce\n"))
	}))
	defer src.Close()
	published := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"schema":1,"generated":1,"source":"newtrackon","trackers":["udp://old.example.org:1/announce"]}`))
	}))
	defer published.Close()
	c := &http.Client{Timeout: 5 * time.Second}
	// lan.example.org resolves inside a LAN: never published
	lookup := func(_ context.Context, host string) ([]net.IP, error) {
		if host == "lan.example.org" {
			return []net.IP{net.ParseIP("10.1.2.3")}, nil
		}
		return []net.IP{net.ParseIP("203.0.113.9")}, nil
	}

	b, from, err := buildTrackers(c, src.URL, published.URL, time.Unix(1_800_000_000, 0), lookup)
	list, perr := trackers.ParseFeed(b)
	if err != nil || perr != nil || from != src.URL || len(list) != 2 || list[1] != "https://b.example.org:443/announce" {
		t.Fatalf("from the source: %s %q %v %v", from, list, err, perr)
	}
	// The source fails: the published list stays
	up = false
	b, from, err = buildTrackers(c, src.URL, published.URL, time.Now(), lookup)
	list, _ = trackers.ParseFeed(b)
	if err != nil || from != published.URL || len(list) != 1 {
		t.Fatalf("from prev: %s %q %v", from, list, err)
	}
	// Both fail: nothing to write
	if b, _, err := buildTrackers(c, src.URL, src.URL, time.Now(), lookup); err == nil || b != nil {
		t.Fatalf("both failed: %q %v", b, err)
	}
	if _, _, err := buildTrackers(c, src.URL, "", time.Now(), lookup); err == nil {
		t.Fatal("no prev")
	}
}
