package importer

import (
	"bufio"
	"encoding/base64"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"downloadcenter/internal/core"
)

// ini is a parsed ds.conf: section -> key -> value.
type ini map[string]map[string]string

func parseINI(r io.Reader) ini {
	out := ini{}
	sec := ""
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			sec = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			if out[sec] == nil {
				out[sec] = map[string]string{}
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || sec == "" {
			continue
		}
		out[sec][strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}

func (c ini) str(sec, key string) (string, bool) {
	v, ok := c[sec][key]
	return v, ok
}

func (c ini) num(sec, key string, cur int) int {
	if v, ok := c[sec][key]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return int(f)
		}
	}
	return cur
}

func (c ini) flag(sec, key string, cur bool) bool {
	if v, ok := c[sec][key]; ok {
		v = strings.ToLower(v)
		return v == "true" || v == "1" || v == "yes"
	}
	return cur
}

// officialDays converts the official schedule (day 0 = Sunday) to ours
// (day 0 = Monday).
func officialDays(off [7]string) [7]string {
	var out [7]string
	for n := 0; n < 7; n++ {
		out[(n+6)%7] = off[n]
	}
	return out
}

// settingsResult tells the caller what applySettings did.
type settingsResult struct {
	TempShare  string
	MoveShare  string
	ProxySocks bool
	ProxyID    string // profile the official SOCKS5 proxy went to
	ProxyPass  string
	Warnings   []string
}

// applySettings maps ds.conf onto s. Folders are returned as share names
// for the caller to resolve; SOCKS5 is applied only when allowed.
func applySettings(c ini, s *core.Settings, socksOK, importPorts bool) settingsResult {
	var r settingsResult
	s.Schedule.Enabled = c.flag("global", "schedule_enable", s.Schedule.Enabled)
	var off [7]string
	cur := officialFromOurs(s.Schedule.Days)
	for n := 0; n < 7; n++ {
		off[n] = cur[n]
		if v, ok := c.str("global", "schedule"+strconv.Itoa(n)); ok && len(v) == 24 && strings.Trim(v, "012") == "" {
			off[n] = v
		}
	}
	s.Schedule.Days = officialDays(off)
	r.TempShare, _ = c.str("global", "down_folder")
	r.MoveShare, _ = c.str("global", "move_folder")

	s.HTTP.MaxNum = c.num("http", "max_num", s.HTTP.MaxNum)
	s.HTTP.MaxDown = c.num("http", "max_down_rate", s.HTTP.MaxDown)
	s.FTP.MaxNum = c.num("ftp", "max_num", s.FTP.MaxNum)
	s.FTP.MaxDown = c.num("ftp", "max_down_rate", s.FTP.MaxDown)
	s.BT.MaxNum = c.num("bt", "max_num", s.BT.MaxNum)
	s.BT.MaxDown = c.num("bt", "max_down_rate", s.BT.MaxDown)
	s.BT.MaxUp = c.num("bt", "max_up_rate", s.BT.MaxUp)
	t := &s.Torrent
	t.DHT = c.flag("bt", "dht", t.DHT)
	t.LSD = c.flag("bt", "lsd", t.LSD)
	t.Encrypt = c.flag("bt", "encrypt", t.Encrypt)
	t.UPnP = c.flag("bt", "upnp_forward", t.UPnP) || c.flag("bt", "nat", false)
	if v, ok := c.str("bt", "share_ratio"); ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			t.SeedRatio = f
		}
	}
	t.SeedTime = c.num("bt", "share_time", t.SeedTime)
	if t.SeedTime == 0 {
		// Official 0 = seed forever, ratio ignored
		t.SeedRatio = 0
	}
	t.MaxConn = c.num("bt", "max_conn", t.MaxConn)
	t.TorrentMaxConn = c.num("bt", "torrent_max_conn", t.TorrentMaxConn)
	t.TorrentMaxUp = c.num("bt", "torrent_max_up", t.TorrentMaxUp)
	t.PeerMode = c.num("bt", "peer_mode", t.PeerMode)
	if v, _ := c.str("bt", "peer_id"); len(v) == 2 {
		t.PeerID = v
	}
	if v, _ := c.str("bt", "peer_version"); v != "" {
		t.PeerVersion = v
	}
	if v, _ := c.str("bt", "peer_agent"); v != "" {
		t.PeerAgent = v
	}
	if importPorts {
		pf, pt := c.num("bt", "port_from", 0), c.num("bt", "port_to", 0)
		if pf > 0 && pf <= 65535 && pt >= pf && pt <= 65535 {
			t.PortFrom, t.PortTo = pf, pt
		}
	} else {
		r.Warnings = append(r.Warnings, "連入埠沿用 Download Center 的設定（官方版仍啟用，避免埠衝突）")
	}
	if c.num("bt", "proxy_type", 0) == 2 {
		if socksOK {
			// The official SOCKS5 proxy becomes a profile used by torrents
			pr := s.Proxy.TorrentSocks5("px-ds", "Download Station SOCKS5")
			pr.Host, _ = c.str("bt", "proxy_hostname")
			pr.Port = c.num("bt", "proxy_port", 1080)
			pr.User = ""
			s.Proxy.Force = c.flag("bt", "proxy_force", false)
			s.Proxy.ApplyPeers, s.Proxy.ApplyTrackers = true, true
			if c.flag("bt", "proxy_auth", false) {
				pr.User, _ = c.str("bt", "proxy_username")
				pw, _ := c.str("bt", "proxy_password")
				r.ProxyPass = decodeMaybe(pw)
			}
			s.Proxy.SetProfile(pr)
			s.Proxy.BT = pr.ID
			r.ProxySocks, r.ProxyID = true, pr.ID
		} else {
			r.Warnings = append(r.Warnings, "SOCKS5 設定未匯入（目前的種子引擎不支援 SOCKS5）")
		}
	}
	return r
}

func officialFromOurs(days [7]string) [7]string {
	var out [7]string
	for n := 0; n < 7; n++ {
		out[n] = days[(n+6)%7]
	}
	return out
}

// decodeMaybe returns the base64-decoded value when it decodes to text,
// else the value itself.
func decodeMaybe(v string) string {
	if b, err := base64.StdEncoding.DecodeString(v); err == nil && utf8.Valid(b) && len(b) > 0 {
		return string(b)
	}
	return v
}
