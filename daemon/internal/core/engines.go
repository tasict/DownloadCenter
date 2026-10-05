package core

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"downloadcenter/internal/engine"
	"downloadcenter/internal/qts"
)

// ErrNeedsRestart is returned by ApplyGlobal when options only take effect
// after an engine restart.
var ErrNeedsRestart = errors.New("restart required")

// GlobalFor builds the global options of an engine from the settings.
func (m *Manager) GlobalFor(name string) engine.Global {
	s := m.Settings()
	g := engine.Global{
		DHT: s.Torrent.DHT, LSD: s.Torrent.LSD, PEX: s.Torrent.PEX, UPnP: s.Torrent.UPnP, Encrypt: s.Torrent.Encrypt,
		MaxConn: s.Torrent.MaxConn, TorrentMaxConn: s.Torrent.TorrentMaxConn, TorrentMaxUp: int64(s.Torrent.TorrentMaxUp) * 1024,
		SeedRatio: s.Torrent.SeedRatio, SeedTime: s.Torrent.SeedTime,
		PortFrom: s.Torrent.PortFrom, PortTo: s.Torrent.PortTo,
	}
	g.PeerID, g.PeerAgent = peerIdentity(s.Torrent)
	// Torrents share one profile: libtorrent's proxy is per session
	px := s.Proxy
	if p := px.Profile(px.BT); p != nil && p.Host != "" {
		g.Proxy = engine.Proxy{Type: p.Type, Host: p.Host, Port: p.Port, User: p.User, Pass: m.ProxyPassword(p.ID),
			RemoteDNS: p.RemoteDNS, ApplyTrackers: px.ApplyTrackers, ApplyPeers: px.ApplyPeers,
			Force: px.Force, NoProxy: p.NoProxy}
		if g.Proxy.Force {
			g.UPnP = false
		}
	}
	return g
}

// peerIdentity maps the official peer_mode to a peer id prefix and agent.
func peerIdentity(b BTSettings) (prefix, agent string) {
	switch b.PeerMode {
	case 0:
		ver := strings.ReplaceAll(b.PeerVersion, ".", "")
		for len(ver) < 4 {
			ver += "0"
		}
		id := b.PeerID
		if len(id) != 2 {
			id = "DC"
		}
		return fmt.Sprintf("-%s%s-", id, ver[:4]), b.PeerAgent
	case 2:
		return "-DE13C0-", "Deluge/1.3.12 (http://deluge-torrent.org)"
	case 3:
		return "-TR2940-", "Transmission/2.94"
	case 4:
		return "-UM1870-", "uTorrentMac/1870(41339)"
	}
	return "", ""
}

// applyEngines pushes new settings into running engines.
func (m *Manager) applyEngines(old Settings) {
	seen := map[engine.Engine]bool{}
	for name, e := range m.Engines {
		if e == nil || seen[e] {
			continue
		}
		seen[e] = true
		g := m.GlobalFor(name)
		if err := e.ApplyGlobal(g); err != nil {
			if strings.Contains(err.Error(), "restart required") {
				go m.restartEngine(e, g)
			} else {
				log.Printf("core: apply %s: %v", name, err)
			}
		}
	}
	if m.URL != nil && !seen[m.URL] {
		m.URL.ApplyGlobal(m.GlobalFor(m.URL.Name()))
	}
	m.mu.Lock()
	m.applied = map[string][2]int64{}
	m.mu.Unlock()
}

// restartEngine restarts an engine; tasks are re-added by the next tick.
func (m *Manager) restartEngine(e engine.Engine, g engine.Global) {
	log.Printf("core: restarting %s for new settings", e.Name())
	m.mu.Lock()
	m.restarting[e.Name()] = true
	for h, t := range m.live {
		if m.engineOf(t) == e {
			delete(m.running, h)
			delete(m.applied, h)
		}
	}
	m.mu.Unlock()
	if err := e.Restart(g); err != nil {
		log.Printf("core: restart %s: %v", e.Name(), err)
	}
	m.mu.Lock()
	delete(m.restarting, e.Name())
	delete(m.down, e.Name())
	m.mu.Unlock()
	m.Kick()
}

// tempRoots lists the @DownloadCenterTemp folders that may exist.
func (m *Manager) tempRoots() []string {
	var out []string
	for _, sh := range qts.Shares() {
		out = append(out, filepath.Join(sh.Path, TempDirName))
	}
	if hr := qts.HomesRoot(); hr != "" {
		if ents, err := os.ReadDir(hr); err == nil {
			for _, e := range ents {
				if e.IsDir() {
					out = append(out, filepath.Join(hr, e.Name(), "Download", TempDirName))
				}
			}
		}
	}
	return out
}

func urlUserinfo(user, pass string) string {
	return url.UserPassword(user, pass).String()
}

// engineError maps an engine error code (engine.Err*) to the task's error
// code and a message.
func engineError(code, msg string) (string, string) {
	texts := map[string]string{
		"1":                   "Download failed (unknown error)",
		"2":                   "Connection timed out",
		"3":                   "File not found (404)",
		"4":                   "File not found too many times",
		"5":                   "Download too slow; aborted",
		"6":                   "Network problem",
		"8":                   "The server does not support resuming",
		"9":                   "Not enough disk space",
		"10":                  "Piece size does not match the control file",
		"11":                  "The same file is already being downloaded",
		"12":                  "The same torrent is already being downloaded",
		"13":                  "A file with the same name exists at the destination",
		"14":                  "Cannot rename file",
		"15":                  "Cannot open existing file",
		"16":                  "Cannot create file",
		"17":                  "File read/write failed",
		"18":                  "Cannot create folder",
		"19":                  "Cannot resolve the URL's host name",
		"21":                  "FTP command failed",
		"22":                  "Invalid server response",
		"23":                  "Too many redirects",
		"24":                  "Sign-in required, or wrong user name or password. Check this site's account under Settings › Site accounts",
		"25":                  "Invalid torrent file format",
		"26":                  "Torrent file is corrupted",
		"27":                  "Invalid magnet link format",
		"28":                  "Invalid parameter",
		"29":                  "The server is busy (503). Try again later",
		"32":                  "File checksum failed",
		engine.ErrNoTransport: "This NAS cannot download this kind of URL (the download component dc-dl is unavailable)",
		engine.ErrHostKey:     "The SFTP server's host key differs from the last connection; the download was stopped for safety",
	}
	t, ok := texts[code]
	if !ok {
		t = "Download failed"
	}
	if msg != "" && !strings.Contains(t, msg) && code != engine.ErrNoTransport && code != engine.ErrHostKey {
		t += " (" + truncate(msg, 160) + ")"
	}
	if code == "" {
		code = "engine"
	} else {
		code = "dl_" + code
	}
	return code, t
}

// isControlFile reports engine bookkeeping files in a URL task's folder
// (the URL engine's .<name>.dcdl and aria2's <name>.aria2 from before).
func isControlFile(name string) bool {
	return strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".dcdl") || strings.HasSuffix(name, ".aria2")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
