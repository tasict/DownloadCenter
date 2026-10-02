package v4

import (
	"strconv"
	"strings"

	"downloadcenter/internal/core"
)

// Official schedule day N (0 = Sunday) is our Days[(N+6)%7] (Monday first).
func ourDay(official int) int { return (official + 6) % 7 }

// OfficialSchedule returns the 7 official day strings (Sunday first).
func OfficialSchedule(days [7]string) [7]string {
	var out [7]string
	for n := 0; n < 7; n++ {
		out[n] = days[ourDay(n)]
	}
	return out
}

// FromOfficialSchedule converts official day strings (Sunday first).
func FromOfficialSchedule(off [7]string) [7]string {
	var out [7]string
	for n := 0; n < 7; n++ {
		out[ourDay(n)] = off[n]
	}
	return out
}

func (s *service) configShape(c *call) result {
	st := s.m.Settings()
	caps := s.btCaps()
	global := map[string]any{"search_enable": 0, "schedule_enable": b2i(st.Schedule.Enabled),
		"down_folder": s.m.DisplayPath(c.who.User, st.TempDir), "move_folder": s.m.DisplayPath(c.who.User, st.MoveDir)}
	for n, d := range OfficialSchedule(st.Schedule.Days) {
		global["schedule"+strconv.Itoa(n)] = d
	}
	pf, pt := st.Torrent.PortFrom, st.Torrent.PortTo
	proxyType, proxyAuth := 0, 0
	host, user := "", ""
	port := 1080
	// The official UI knows one SOCKS5 proxy: the profile torrents use
	if pr := st.Proxy.Profile(st.Proxy.BT); pr != nil && pr.Type == "socks5" && caps.Socks5Peers {
		proxyType = 2
		host, port, user = pr.Host, pr.Port, pr.User
		if user != "" {
			proxyAuth = 1
		}
	}
	ver := s.btVersion()
	agent := st.Torrent.PeerAgent
	if agent == "" {
		agent = "DownloadCenter/" + s.srv.Version
	}
	bt := map[string]any{
		"port_from": pf, "port_to": pt, "upnp_forward": b2i(st.Torrent.UPnP && caps.UPnP), "dht": b2i(st.Torrent.DHT),
		"lsd": b2i(st.Torrent.LSD), "nat": b2i(st.Torrent.UPnP && caps.UPnP), "encrypt": b2i(st.Torrent.Encrypt),
		"max_num": st.BT.MaxNum, "max_up_rate": st.BT.MaxUp, "max_down_rate": st.BT.MaxDown,
		"torrent_max_up": st.Torrent.TorrentMaxUp, "max_conn": st.Torrent.MaxConn, "torrent_max_conn": st.Torrent.TorrentMaxConn,
		"share_time": st.Torrent.SeedTime, "share_ratio": st.Torrent.SeedRatio,
		"proxy_type": proxyType, "proxy_force": b2i(st.Proxy.Force && proxyType == 2), "proxy_hostname": host,
		"proxy_port": port, "proxy_auth": proxyAuth, "proxy_username": user,
		// The stored password is never returned; an empty value means "keep"
		"proxy_password": "",
		"peer_mode":      st.Torrent.PeerMode, "peer_id": st.Torrent.PeerID, "peer_version": firstNonEmpty(st.Torrent.PeerVersion, ver),
		"peer_agent": agent,
	}
	return result{
		"global": global,
		"http":   map[string]any{"max_num": st.HTTP.MaxNum, "max_down_rate": st.HTTP.MaxDown},
		"ftp":    map[string]any{"max_num": st.FTP.MaxNum, "max_down_rate": st.FTP.MaxDown},
		"bt":     bt,
		"rss":    map[string]any{"enable": 0, "check_update_time": 12},
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (s *service) configGet(c *call) result { return s.configShape(c) }

// configSet applies the official settings form (all fields, prefixed with
// the section). Unknown or absent fields keep their current values.
func (s *service) configSet(c *call) result {
	st := s.m.Settings()
	p := c.p
	intv := func(k string, cur int) int {
		if !p.has(k) {
			return cur
		}
		if v, err := strconv.Atoi(strings.TrimSpace(p.get(k))); err == nil {
			return v
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(p.get(k)), 64); err == nil {
			return int(f)
		}
		return cur
	}
	boolv := func(k string, cur bool) bool {
		if !p.has(k) {
			return cur
		}
		v := strings.ToLower(strings.TrimSpace(p.get(k)))
		return v == "1" || v == "true" || v == "on"
	}
	st.Schedule.Enabled = boolv("global_schedule_enable", st.Schedule.Enabled)
	off := OfficialSchedule(st.Schedule.Days)
	for n := 0; n < 7; n++ {
		if v := p.get("global_schedule" + strconv.Itoa(n)); len(v) == 24 && strings.Trim(v, "012") == "" {
			off[n] = v
		}
	}
	st.Schedule.Days = FromOfficialSchedule(off)
	st.HTTP.MaxNum = intv("http_max_num", st.HTTP.MaxNum)
	st.HTTP.MaxDown = intv("http_max_down_rate", st.HTTP.MaxDown)
	st.FTP.MaxNum = intv("ftp_max_num", st.FTP.MaxNum)
	st.FTP.MaxDown = intv("ftp_max_down_rate", st.FTP.MaxDown)
	st.Torrent.PortFrom = intv("bt_port_from", st.Torrent.PortFrom)
	st.Torrent.PortTo = intv("bt_port_to", st.Torrent.PortTo)
	caps := s.btCaps()
	if caps.UPnP {
		st.Torrent.UPnP = boolv("bt_upnp_forward", st.Torrent.UPnP) || boolv("bt_nat", false)
	}
	st.Torrent.DHT = boolv("bt_dht", st.Torrent.DHT)
	st.Torrent.LSD = boolv("bt_lsd", st.Torrent.LSD)
	st.Torrent.Encrypt = boolv("bt_encrypt", st.Torrent.Encrypt)
	st.BT.MaxNum = intv("bt_max_num", st.BT.MaxNum)
	st.BT.MaxUp = intv("bt_max_up_rate", st.BT.MaxUp)
	st.BT.MaxDown = intv("bt_max_down_rate", st.BT.MaxDown)
	st.Torrent.TorrentMaxUp = intv("bt_torrent_max_up", st.Torrent.TorrentMaxUp)
	st.Torrent.MaxConn = intv("bt_max_conn", st.Torrent.MaxConn)
	st.Torrent.TorrentMaxConn = intv("bt_torrent_max_conn", st.Torrent.TorrentMaxConn)
	st.Torrent.SeedTime = intv("bt_share_time", st.Torrent.SeedTime)
	if p.has("bt_share_ratio") {
		if f, err := strconv.ParseFloat(strings.TrimSpace(p.get("bt_share_ratio")), 64); err == nil && f >= 0 {
			st.Torrent.SeedRatio = f
		}
	}
	// Official share_time 0 means "seed forever" (ratio ignored); ours
	// means "no time limit", so forever is time 0 with ratio 0
	if p.has("bt_share_time") && st.Torrent.SeedTime == 0 {
		st.Torrent.SeedRatio = 0
	}
	// Proxy: the official UI knows only "none" and SOCKS5. SOCKS5 needs an
	// engine that supports it; "none" does not wipe an HTTP proxy the
	// official UI cannot show.
	if p.has("bt_proxy_type") {
		switch p.get("bt_proxy_type") {
		case "2":
			if caps.Socks5Peers {
				pr := st.Proxy.TorrentSocks5("px-bt", "SOCKS5")
				pr.Host = strings.TrimSpace(p.get("bt_proxy_hostname"))
				pr.Port = intv("bt_proxy_port", pr.Port)
				st.Proxy.Force = boolv("bt_proxy_force", st.Proxy.Force)
				if boolv("bt_proxy_auth", false) {
					pr.User = p.get("bt_proxy_username")
					if pw := ezDecode(p.get("bt_proxy_password")); pw != "" {
						s.m.SetProxyPassword(pr.ID, pw)
					}
				} else {
					pr.User = ""
					s.m.SetProxyPassword(pr.ID, "")
				}
				st.Proxy.SetProfile(pr)
				st.Proxy.BT = pr.ID
				st.Proxy.ApplyPeers, st.Proxy.ApplyTrackers = true, true
			}
		case "0":
			// Torrents stop using a SOCKS5 profile (the profile itself stays)
			if pr := st.Proxy.Profile(st.Proxy.BT); pr != nil && pr.Type == "socks5" {
				st.Proxy.BT = ""
			}
		}
	}
	if p.has("bt_peer_mode") {
		st.Torrent.PeerMode = intv("bt_peer_mode", st.Torrent.PeerMode)
	}
	if v := p.get("bt_peer_id"); len(v) == 2 {
		st.Torrent.PeerID = v
	}
	if v := p.get("bt_peer_version"); v != "" && len(v) <= 8 {
		st.Torrent.PeerVersion = v
	}
	if v := p.get("bt_peer_agent"); v != "" && len(v) <= 64 {
		st.Torrent.PeerAgent = v
	}
	if err := s.m.SaveSettings(st); err != nil {
		return failReason(errException, err.Error())
	}
	return s.configShape(c)
}

// --- Account (site credentials) ---

func (s *service) accountQuery(c *call) result {
	data := []map[string]any{}
	for _, a := range s.m.Accounts(c.who.User) {
		if a.Kind != "site" {
			continue
		}
		data = append(data, map[string]any{"hash": a.ID, "name": a.Host, "username": a.Username, "enable": b2i(a.Enabled)})
	}
	return result{"data": data}
}

func (s *service) accountAdd(c *call) result {
	name := strings.TrimSpace(c.p.get("name"))
	if name == "" {
		return fail(errParamNotFound)
	}
	a := &core.Account{Owner: c.who.User, Kind: "site", Host: name, Username: c.p.get("username"),
		Enabled: !c.p.has("enable") || c.p.get("enable") == "1" || c.p.get("enable") == "true"}
	if err := s.m.SaveAccount(a, ezDecode(c.p.get("password"))); err != nil {
		return failReason(errParameter, err.Error())
	}
	return result{}
}

func (s *service) accountUpdate(c *call) result {
	a, err := s.m.Account(c.p.get("hash"))
	if err != nil || a.Owner != c.who.User || a.Kind != "site" {
		return fail(errParamNotFound)
	}
	if c.p.has("name") && strings.TrimSpace(c.p.get("name")) != "" {
		a.Host = strings.TrimSpace(c.p.get("name"))
	}
	if c.p.has("username") {
		a.Username = c.p.get("username")
	}
	if c.p.has("enable") {
		a.Enabled = c.p.get("enable") == "1" || c.p.get("enable") == "true"
	}
	if err := s.m.SaveAccount(a, ezDecode(c.p.get("password"))); err != nil {
		return failReason(errParameter, err.Error())
	}
	return result{}
}

func (s *service) accountRemove(c *call) result {
	for _, h := range c.p.all("hash") {
		a, err := s.m.Account(h)
		if err != nil || a.Owner != c.who.User {
			return fail(errParamNotFound)
		}
		s.m.DeleteAccount(a.ID)
	}
	return result{}
}
