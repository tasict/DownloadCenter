package trackers

import (
	"net"
	"testing"
)

// Public: a tracker that may be handed on, as in a magnet link anyone gets
func TestPublic(t *testing.T) {
	own := ""
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
				own = ipn.IP.String()
			}
		}
	}
	for _, raw := range []string{"udp://127.0.0.1:6969", "http://localhost/a", "http://tracker.localhost/a", "udp://192.168.1.5:80",
		"http://[::1]/a", "udp://0x7f.1:1", "http://169.254.169.254/", "udp://[fd00::1]:1", "ftp://tracker.example.org/", "udp://"} {
		if Public(raw) {
			t.Errorf("%s is public", raw)
		}
	}
	if own != "" && Public("udp://"+own+":6969") {
		t.Errorf("the NAS's own %s is public", own)
	}
	for _, raw := range []string{"udp://tracker.example.org:1337/announce", "https://tracker.example.org/announce", "udp://93.184.216.34:6969"} {
		if !Public(raw) {
			t.Errorf("%s is not public", raw)
		}
	}
}
