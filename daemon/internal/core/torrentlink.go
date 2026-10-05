package core

import (
	"io"
	"net/url"
	"strings"
	"time"

	"downloadcenter/internal/netutil"
	"downloadcenter/internal/torrent"
)

// FetchTorrentLink downloads a link to a .torrent file, so that every way of
// adding a link (web UI and REST API, V4 clients, chat commands) adds it as a
// torrent instead of a plain file. It returns nil for any other link and for
// one that cannot be fetched or is not a torrent. The proxy is the one the
// task would use; regular users' requests never reach the NAS or local
// addresses. Never call it with m.mu held: it is a network request.
func (m *Manager) FetchTorrentLink(src, proxyChoice string, admin bool) []byte {
	u, err := url.Parse(src)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !strings.HasSuffix(strings.ToLower(u.Path), ".torrent") {
		return nil
	}
	pr, err := m.ProxyFor(proxyChoice, src, admin)
	if err != nil {
		return nil
	}
	proxy := ""
	if pr != nil {
		proxy = m.ProxyURL(pr.ID)
	}
	resp, err := netutil.Client(30*time.Second, !admin, proxy).Get(src)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil
	}
	if _, err := torrent.Parse(b); err != nil {
		return nil
	}
	return b
}
