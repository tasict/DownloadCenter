package netutil

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestDialRace(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	// The first address refuses; the race moves on to the one that answers
	start := time.Now()
	c, err := dialRace(context.Background(), &net.Dialer{Timeout: 5 * time.Second}, "tcp",
		[]net.IP{net.ParseIP("127.0.0.2"), net.ParseIP("127.0.0.1")}, port)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
	if _, err := dialRace(context.Background(), &net.Dialer{Timeout: time.Second}, "tcp", []net.IP{net.ParseIP("127.0.0.1")}, "1"); err == nil {
		t.Fatal("closed port connected")
	}
}
