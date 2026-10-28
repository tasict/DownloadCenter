package netutil

import (
	"context"
	"testing"
)

func TestHostIP(t *testing.T) {
	for _, c := range []struct{ host, want string }{
		{"127.0.0.1", "127.0.0.1"},
		{"2130706433", "127.0.0.1"},
		{"0x7f000001", "127.0.0.1"},
		{"0x7f.1", "127.0.0.1"},
		{"127.1", "127.0.0.1"},
		{"127.0.1", "127.0.0.1"},
		{"0177.0.0.1", "127.0.0.1"},
		{"127.000.000.001", "127.0.0.1"},
		{"10.1.0xb01", "10.1.11.1"},
		{"::1", "::1"},
		{"::ffff:127.0.0.1", "127.0.0.1"},
		{"example.org", ""},
		{"1.2.3.4.5", ""},
		{"256.0.0.1", ""},
		{"1.0x1000000", ""},
		{"0x", ""},
		{"08.1.1.1", ""},
		{"1e3", ""},
		{"", ""},
	} {
		got := ""
		if ip := HostIP(c.host); ip != nil {
			got = ip.String()
		}
		if got != c.want {
			t.Errorf("HostIP(%q) = %q, want %q", c.host, got, c.want)
		}
	}
	for _, h := range []string{"2130706433", "0x7f.1", "127.1", "a.localhost", "LOCALHOST."} {
		if CheckHost(context.Background(), h, true) == nil {
			t.Errorf("CheckHost(%q) allowed", h)
		}
	}
}
