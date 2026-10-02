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
// code and a zh-TW message.
func engineError(code, msg string) (string, string) {
	texts := map[string]string{
		"1":                   "下載失敗（未知的錯誤）",
		"2":                   "連線逾時",
		"3":                   "找不到檔案（404）",
		"4":                   "多次找不到檔案",
		"5":                   "下載速度太慢，已中止",
		"6":                   "網路發生問題",
		"8":                   "伺服器不支援續傳",
		"9":                   "磁碟空間不足",
		"10":                  "分段大小與控制檔不符",
		"11":                  "已經在下載同一個檔案",
		"12":                  "已經在下載同一個種子",
		"13":                  "目的地已有同名檔案",
		"14":                  "無法重新命名檔案",
		"15":                  "無法開啟已存在的檔案",
		"16":                  "無法建立檔案",
		"17":                  "讀寫檔案失敗",
		"18":                  "無法建立資料夾",
		"19":                  "無法解析網址的主機名稱",
		"21":                  "FTP 指令失敗",
		"22":                  "伺服器回應不正確",
		"23":                  "重新導向次數過多",
		"24":                  "需要登入或帳號密碼錯誤。到 設定 › 網站帳號 確認這個網站的帳號密碼",
		"25":                  "種子檔格式不正確",
		"26":                  "種子檔已損壞",
		"27":                  "磁力連結格式不正確",
		"28":                  "參數不正確",
		"29":                  "伺服器忙碌中（503），請稍後再試",
		"32":                  "檔案校驗失敗",
		engine.ErrNoTransport: "這台 NAS 無法下載這種網址（下載元件 dc-dl 無法使用）",
		engine.ErrHostKey:     "SFTP 伺服器的主機金鑰和上次連線時不同，為了安全已停止下載",
	}
	t, ok := texts[code]
	if !ok {
		t = "下載失敗"
	}
	if msg != "" && !strings.Contains(t, msg) && code != engine.ErrNoTransport && code != engine.ErrHostKey {
		t += "（" + truncate(msg, 160) + "）"
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
