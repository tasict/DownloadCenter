package core

import (
	"net"
	"strconv"
	"strings"

	"downloadcenter/internal/qts"
)

// TypeLimits are the per-protocol queue and speed settings. Speeds are KB/s,
// 0 = unlimited. Limited* apply during "limited" schedule hours.
type TypeLimits struct {
	MaxNum      int `json:"max_num"`
	MaxDown     int `json:"max_down"`
	MaxUp       int `json:"max_up"`
	LimitedDown int `json:"limited_down"`
	LimitedUp   int `json:"limited_up"`
}

// Schedule is the 7x24 plan. Days[0] is Monday; each day has 24 characters:
// '0' paused, '1' full speed, '2' limited.
type Schedule struct {
	Enabled bool      `json:"enabled"`
	Days    [7]string `json:"days"`
}

// BTSettings are the torrent settings.
type BTSettings struct {
	// Listening ports of libtorrent (the key names date from when aria2
	// had a range of its own)
	PortFrom         int     `json:"lt_port_from"`
	PortTo           int     `json:"lt_port_to"`
	DHT              bool    `json:"dht"`
	LSD              bool    `json:"lsd"`
	PEX              bool    `json:"pex"`
	UPnP             bool    `json:"upnp"`
	Encrypt          bool    `json:"encrypt"`
	SeedRatio        float64 `json:"seed_ratio"`
	SeedTime         int     `json:"seed_time"` // minutes; -1 no seeding, 0 no time limit (with SeedRatio 0: forever)
	MaxConn          int     `json:"max_conn"`
	TorrentMaxConn   int     `json:"torrent_max_conn"`
	TorrentMaxUp     int     `json:"torrent_max_up"` // KB/s
	PeerMode         int     `json:"peer_mode"`
	PeerID           string  `json:"peer_id"`
	PeerVersion      string  `json:"peer_version"`
	PeerAgent        string  `json:"peer_agent"`
	PortCheckAllowed bool    `json:"port_check_allowed"` // unused: consent is asked per test
	StallMinutes     int     `json:"stall_minutes"`      // content-merged tasks switch source after this
}

// ProxyProfile is one saved proxy. Its password is the secret
// "proxy:<id>".
type ProxyProfile struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"` // http | socks5
	Host      string `json:"host"`
	Port      int    `json:"port"`
	User      string `json:"user"`
	RemoteDNS bool   `json:"remote_dns"` // socks5h: the proxy resolves names
	Sites     string `json:"sites"`      // "auto" tasks for these hosts use this profile
	NoProxy   string `json:"no_proxy"`   // hosts and networks reached directly
	ForUsers  bool   `json:"for_users"`  // regular users may pick it
}

// ProxySettings: the profiles and which one each kind of connection uses
// ("" = direct). URL tasks choose per task ("auto", "none" or a profile
// id); torrents share one profile, since libtorrent's proxy is per session.
type ProxySettings struct {
	Profiles        []ProxyProfile `json:"profiles"`
	URLDefault      string         `json:"url_default"`
	RequireForUsers bool           `json:"require_for_users"` // regular users' URL tasks never go direct
	BT              string         `json:"bt"`
	ApplyTrackers   bool           `json:"apply_trackers"`
	ApplyPeers      bool           `json:"apply_peers"`
	Force           bool           `json:"force"` // torrents: no UPnP, no DHT/uTP without UDP through the proxy
	NotifyProfile   string         `json:"notify_profile"`

	// The single proxy of versions up to 0.9.1, converted to a profile on load
	OldType      string `json:"type,omitempty"`
	OldHost      string `json:"host,omitempty"`
	OldPort      int    `json:"port,omitempty"`
	OldUser      string `json:"user,omitempty"`
	OldRemoteDNS bool   `json:"remote_dns,omitempty"`
	OldApplyURL  bool   `json:"apply_url,omitempty"`
	OldNoProxy   string `json:"no_proxy,omitempty"`
	OldNotify    bool   `json:"notify,omitempty"`
}

// SetProfile replaces the profile with the same id, or adds it.
func (p *ProxySettings) SetProfile(pr ProxyProfile) {
	for i := range p.Profiles {
		if p.Profiles[i].ID == pr.ID {
			p.Profiles[i] = pr
			return
		}
	}
	p.Profiles = append(p.Profiles, pr)
}

// TorrentSocks5 is the SOCKS5 profile torrents use, for settings that know
// only one SOCKS5 proxy (official ds.conf, V4 Config): the current torrent
// profile when it is SOCKS5, else the profile with fallbackID, else a new
// one under that id.
func (p *ProxySettings) TorrentSocks5(fallbackID, name string) ProxyProfile {
	if pr := p.Profile(p.BT); pr != nil && pr.Type == "socks5" {
		return *pr
	}
	if pr := p.Profile(fallbackID); pr != nil {
		c := *pr
		c.Type = "socks5"
		return c
	}
	return ProxyProfile{ID: fallbackID, Name: name, Type: "socks5", Port: 1080, RemoteDNS: true, NoProxy: DefaultNoProxy}
}

// DefaultNoProxy is the no-proxy list of a new profile.
const DefaultNoProxy = "localhost,127.0.0.0/8,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,169.254.0.0/16"

// Profile returns the profile with this id.
func (p *ProxySettings) Profile(id string) *ProxyProfile {
	if id == "" {
		return nil
	}
	for i := range p.Profiles {
		if p.Profiles[i].ID == id {
			return &p.Profiles[i]
		}
	}
	return nil
}

// Settings is the package configuration (settings row "config").
type Settings struct {
	TempDir     string        `json:"temp_dir"` // real path
	MoveDir     string        `json:"move_dir"` // "" = do not move
	AutoRemove  string        `json:"auto_remove"`
	HTTP        TypeLimits    `json:"http"`
	FTP         TypeLimits    `json:"ftp"`
	BT          TypeLimits    `json:"bt"`
	Schedule    Schedule      `json:"schedule"`
	Torrent     BTSettings    `json:"torrent"`
	Proxy       ProxySettings `json:"proxy"`
	DiskLowMB   int           `json:"disk_low_mb"`
	V4Takeover  bool          `json:"v4_takeover"`
	ExternalURL string        `json:"external_url"` // public base URL for links in notifications
	HistoryDays int           `json:"history_days"`
}

// DefaultSettings returns the defaults for a new installation.
func DefaultSettings() Settings {
	full := strings.Repeat("1", 24)
	s := Settings{
		HTTP: TypeLimits{MaxNum: 3},
		FTP:  TypeLimits{MaxNum: 2},
		BT:   TypeLimits{MaxNum: 5},
		Torrent: BTSettings{
			PortFrom: 16891, PortTo: 16899,
			DHT: true, LSD: true, PEX: true, SeedRatio: 1.5, SeedTime: 0, MaxConn: 300,
			PeerMode: 1, PeerID: "DC", PeerVersion: "1.0", StallMinutes: 30,
		},
		Proxy:       ProxySettings{ApplyTrackers: true, ApplyPeers: true, Force: true},
		HistoryDays: 90,
	}
	for i := range s.Schedule.Days {
		s.Schedule.Days[i] = full
	}
	for _, sh := range qts.Shares() {
		if strings.EqualFold(sh.Name, "Download") {
			s.TempDir = sh.Path
		}
	}
	if s.TempDir == "" {
		s.TempDir = qts.PublicDir()
	}
	return s
}

func (s *Settings) normalize() {
	for _, p := range []*string{&s.Torrent.PeerID, &s.Torrent.PeerVersion, &s.Torrent.PeerAgent, &s.ExternalURL} {
		*p = printable(*p)
	}
	s.Proxy.normalize()
	if len(s.Torrent.PeerAgent) > 64 {
		s.Torrent.PeerAgent = s.Torrent.PeerAgent[:64]
	}
	if len(s.Torrent.PeerID) != 2 {
		s.Torrent.PeerID = "DC"
	}
	d := DefaultSettings()
	for i := range s.Schedule.Days {
		if len(s.Schedule.Days[i]) != 24 || strings.Trim(s.Schedule.Days[i], "012") != "" {
			s.Schedule.Days[i] = d.Schedule.Days[i]
		}
	}
	fix := func(t *TypeLimits, def int) {
		if t.MaxNum <= 0 {
			t.MaxNum = def
		}
		if t.MaxNum > 50 {
			t.MaxNum = 50
		}
	}
	fix(&s.HTTP, 3)
	fix(&s.FTP, 2)
	fix(&s.BT, 5)
	if s.Torrent.PortFrom <= 0 || s.Torrent.PortFrom > 65535 {
		s.Torrent.PortFrom, s.Torrent.PortTo = d.Torrent.PortFrom, d.Torrent.PortTo
	}
	if s.Torrent.PortTo < s.Torrent.PortFrom {
		s.Torrent.PortTo = s.Torrent.PortFrom
	}
	if s.Torrent.StallMinutes <= 0 {
		s.Torrent.StallMinutes = 30
	}
	switch s.AutoRemove {
	case "", "completed", "seeded":
	default:
		s.AutoRemove = ""
	}
	if s.TempDir == "" {
		s.TempDir = d.TempDir
	}
	if s.HistoryDays <= 0 {
		s.HistoryDays = 90
	}
}

// Settings returns a copy of the current settings.
func (m *Manager) Settings() Settings {
	m.smu.RLock()
	defer m.smu.RUnlock()
	return m.settings
}

func (m *Manager) loadSettings() {
	s := DefaultSettings()
	m.db.GetJSON("config", &s)
	migrated := m.migrateProxy(&s)
	s.normalize()
	if migrated {
		m.db.PutJSON("config", s)
	}
	m.smu.Lock()
	m.settings = s
	m.smu.Unlock()
}

// SaveSettings stores new settings and applies them to the engines.
func (m *Manager) SaveSettings(s Settings) error {
	s.normalize()
	old := m.Settings()
	if err := m.db.PutJSON("config", s); err != nil {
		return err
	}
	m.smu.Lock()
	m.settings = s
	m.smu.Unlock()
	if old.Schedule.Enabled != s.Schedule.Enabled || old.Schedule.Days != s.Schedule.Days {
		mode := m.scheduleMode()
		m.Emit(Event{Type: "schedule.changed", Data: map[string]any{"mode": mode, "by": "settings"}})
		// Reported here: the next tick does not report the same switch again
		m.mu.Lock()
		m.schedMode = mode
		m.mu.Unlock()
	}
	m.applyEngines(old)
	m.refreshProxies()
	m.Kick()
	return nil
}

// normalize drops broken profiles and references to missing ones.
func (p *ProxySettings) normalize() {
	var out []ProxyProfile
	seen := map[string]bool{}
	for _, pr := range p.Profiles {
		pr.ID = printable(pr.ID)
		if pr.ID == "" || seen[pr.ID] || len(pr.ID) > 32 {
			continue
		}
		seen[pr.ID] = true
		for _, f := range []*string{&pr.Name, &pr.Host, &pr.User, &pr.Sites, &pr.NoProxy} {
			*f = printable(*f)
		}
		if pr.Type != "socks5" {
			pr.Type = "http"
		}
		if pr.Port <= 0 || pr.Port > 65535 {
			pr.Port = map[string]int{"http": 8080, "socks5": 1080}[pr.Type]
		}
		if pr.Name == "" {
			pr.Name = pr.Host
		}
		out = append(out, pr)
	}
	p.Profiles = out
	for _, id := range []*string{&p.URLDefault, &p.BT, &p.NotifyProfile} {
		if p.Profile(*id) == nil {
			*id = ""
		}
	}
}

// migrateProxy turns the single proxy of older versions into a profile.
// Caller holds nothing; runs once at load.
func (m *Manager) migrateProxy(s *Settings) bool {
	p := &s.Proxy
	if p.OldType == "" && p.OldHost == "" {
		return false
	}
	if (p.OldType == "http" || p.OldType == "socks5") && p.OldHost != "" && len(p.Profiles) == 0 {
		pr := ProxyProfile{ID: "px1", Name: "代理伺服器", Type: p.OldType, Host: p.OldHost, Port: p.OldPort, User: p.OldUser,
			RemoteDNS: p.OldRemoteDNS, NoProxy: p.OldNoProxy, ForUsers: true}
		p.Profiles = append(p.Profiles, pr)
		if p.OldApplyURL {
			p.URLDefault = pr.ID
		}
		if p.ApplyTrackers || p.ApplyPeers {
			p.BT = pr.ID
		}
		if p.OldNotify {
			p.NotifyProfile = pr.ID
		}
		if pw := m.db.Secret("proxy:password"); pw != "" {
			m.db.SetSecret("proxy:"+pr.ID, pw)
		}
	}
	m.db.SetSecret("proxy:password", "")
	p.OldType, p.OldHost, p.OldPort, p.OldUser, p.OldRemoteDNS, p.OldApplyURL, p.OldNoProxy, p.OldNotify = "", "", 0, "", false, false, "", false
	return true
}

// ProxyPassword returns the stored password of a profile.
func (m *Manager) ProxyPassword(id string) string { return m.db.Secret("proxy:" + id) }

// SetProxyPassword stores the password of a profile ("" removes it).
func (m *Manager) SetProxyPassword(id, pw string) error { return m.db.SetSecret("proxy:"+id, pw) }

// ProxyURL is the proxy URL of a profile ("" for none or a missing one).
func (m *Manager) ProxyURL(id string) string {
	s := m.Settings()
	return m.profileURL(s.Proxy.Profile(id))
}

func (m *Manager) profileURL(p *ProxyProfile) string {
	if p == nil || p.Host == "" {
		return ""
	}
	scheme := "http"
	if p.Type == "socks5" {
		scheme = "socks5"
		if p.RemoteDNS {
			scheme = "socks5h"
		}
	}
	auth := ""
	if p.User != "" {
		auth = urlUserinfo(p.User, m.ProxyPassword(p.ID)) + "@"
	}
	return scheme + "://" + auth + net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
}

// DefaultURLProxy is the proxy of URL downloads that choose nothing
// (page link extraction, hoster account checks).
func (m *Manager) DefaultURLProxy() string { return m.ProxyURL(m.Settings().Proxy.URLDefault) }

// NotifyProxy is the proxy of notifications and webhooks.
func (m *Manager) NotifyProxy() string { return m.ProxyURL(m.Settings().Proxy.NotifyProfile) }

// printable drops control characters (settings end up in engine config
// files and command lines).
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
}

// TaskProxy reports the proxy choice of a URL task ("auto", "none" or a
// profile id) and what it resolves to now (nil profile = direct).
func (m *Manager) TaskProxy(t *Task) (choice string, p *ProxyProfile, err error) {
	choice = t.Options.Proxy
	if choice == "" {
		choice = "auto"
	}
	link := t.Source
	if t.Options.OrigURL != "" {
		link = t.Options.OrigURL
	}
	p, err = m.ProxyFor(t.Options.Proxy, link, m.isAdminOwner(t.Owner))
	return choice, p, err
}

// SetTaskProxy changes the proxy of a URL task. A running download
// reconnects through the new proxy and continues from its control file.
func (m *Manager) SetTaskProxy(hash, choice string) error {
	if choice == "auto" {
		choice = ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.live[hash]
	if !ok {
		return ErrNotFound
	}
	if t.Kind == KindBT {
		return ErrUnsupported
	}
	link := t.Source
	if t.Options.OrigURL != "" {
		link = t.Options.OrigURL
	}
	if _, err := m.ProxyFor(choice, link, m.isAdminOwner(t.Owner)); err != nil {
		return err
	}
	if t.Options.Proxy == choice {
		return nil
	}
	t.Options.Proxy = choice
	// A file-hosting link is resolved again through the new proxy
	t.Options.Direct = ""
	if t.State == StDone || m.moving[hash] {
		m.markDirty(t)
		return nil
	}
	if e := m.engineOf(t); e != nil && t.EngineRef != "" {
		e.Remove(t.EngineRef)
	}
	t.EngineRef = ""
	if t.State == StError {
		t.State, t.ErrorCode, t.ErrorMsg = StQueued, "", ""
	}
	delete(m.running, hash)
	delete(m.applied, hash)
	m.markDirty(t)
	m.Log(hash, "Switched to the new proxy, reconnecting")
	go m.Kick()
	return nil
}
