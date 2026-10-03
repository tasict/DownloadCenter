package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/netutil"
)

// settingsView converts real paths to display paths for the UI.
func (s *Server) settingsView(p *auth.Principal) map[string]any {
	st := s.M.Settings()
	b, _ := json.Marshal(st)
	var m map[string]any
	json.Unmarshal(b, &m)
	m["temp_dir"] = s.M.DisplayPath(p.User, st.TempDir)
	m["move_dir"] = s.M.DisplayPath(p.User, st.MoveDir)
	if tor, ok := m["torrent"].(map[string]any); ok {
		// The torrent engine ("" when dc-bt is missing)
		tor["engine"], _ = s.btInfo()
	}
	if px, ok := m["proxy"].(map[string]any); ok {
		if list, ok := px["profiles"].([]any); ok {
			for _, it := range list {
				if pr, ok := it.(map[string]any); ok {
					id, _ := pr["id"].(string)
					pr["has_password"] = s.M.ProxyPassword(id) != ""
				}
			}
		}
	}
	return m
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	OK(w, map[string]any{
		"settings":        s.settingsView(p),
		"engines":         s.engineCaps(),
		"engine_versions": s.engineVersions(),
		"health":          s.M.EngineHealth(),
	})
}

func (s *Server) engineVersions() map[string]string {
	out := map[string]string{}
	for name, e := range s.M.Engines {
		if e != nil {
			out[name] = e.Version()
		}
	}
	if s.M.URL != nil {
		out[s.M.URL.Name()] = s.M.URL.Version()
	}
	return out
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		Error(w, 400, "bad_request", err.Error())
		return
	}
	view := s.settingsView(p)
	vb, _ := json.Marshal(view)
	st := core.Settings{}
	json.Unmarshal(vb, &st)
	var patch map[string]json.RawMessage
	if err := json.Unmarshal(body, &patch); err != nil {
		Error(w, 400, "bad_request", "JSON 格式不正確")
		return
	}
	if raw, ok := patch["settings"]; ok {
		json.Unmarshal(raw, &patch)
	}
	// Proxy passwords: {"profile id": "password"}; "" leaves one unchanged
	var proxyPass map[string]string
	if raw, ok := patch["proxy_passwords"]; ok {
		json.Unmarshal(raw, &proxyPass)
		delete(patch, "proxy_passwords")
	}
	pb, _ := json.Marshal(patch)
	if err := json.Unmarshal(pb, &st); err != nil {
		Error(w, 400, "bad_request", "設定格式不正確："+err.Error())
		return
	}
	// Display paths back to real paths
	if st.TempDir, err = s.M.ResolvePath(p.User, st.TempDir); err != nil {
		Error(w, 400, "folder_not_found", "找不到暫存位置的資料夾")
		return
	}
	if unusableFolder(w, st.TempDir) {
		return
	}
	if st.MoveDir != "" {
		if st.MoveDir, err = s.M.ResolvePath(p.User, st.MoveDir); err != nil {
			Error(w, 400, "folder_not_found", "找不到「完成後移至」的資料夾")
			return
		}
		if unusableFolder(w, st.MoveDir) {
			return
		}
	}
	if st.Torrent.PortFrom >= 6881 && st.Torrent.PortFrom <= 6889 {
		if inst, en := officialRunning(); inst && en {
			Error(w, 400, "port_in_use", "官方 Download Station 正在使用 6881–6889，請改用其他埠")
			return
		}
	}
	old := s.M.Settings()
	if err := s.M.SaveSettings(st); err != nil {
		Error(w, 500, "failed", err.Error())
		return
	}
	saved := s.M.Settings()
	for id, pw := range proxyPass {
		if pw != "" && saved.Proxy.Profile(id) != nil {
			s.M.SetProxyPassword(id, pw)
		}
	}
	// Passwords of removed profiles go with them
	for _, pr := range old.Proxy.Profiles {
		if saved.Proxy.Profile(pr.ID) == nil {
			s.M.SetProxyPassword(pr.ID, "")
		}
	}
	if len(proxyPass) > 0 {
		// New passwords reach the engines (torrents use a profile too)
		s.M.SaveSettings(saved)
	}
	s.getSettings(w, r, p)
}

// OfficialRunning is set by main (avoids importing qts here twice).
var officialRunning = func() (bool, bool) { return false, false }

// SetOfficialCheck installs the official package detector.
func SetOfficialCheck(f func() (bool, bool)) { officialRunning = f }

func (s *Server) proxyTest(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	var b struct {
		Profile  string  `json:"profile"` // id of a saved profile (its password is used)
		Type     string  `json:"type"`
		Host     string  `json:"host"`
		Port     int     `json:"port"`
		User     string  `json:"user"`
		Password *string `json:"password"`
	}
	if err := Decode(r, &b); err != nil {
		Error(w, 400, "bad_request", err.Error())
		return
	}
	st := s.M.Settings()
	pass := ""
	// The stored password only goes to the server it was saved for
	if pr := st.Proxy.Profile(b.Profile); pr != nil && b.Host == pr.Host && b.Port == pr.Port && b.User == pr.User {
		pass = s.M.ProxyPassword(pr.ID)
	}
	if b.Password != nil && *b.Password != "" {
		pass = *b.Password
	}
	if b.Host == "" || b.Port <= 0 || b.Port > 65535 {
		Error(w, 400, "bad_request", "請填代理伺服器與埠")
		return
	}
	res, err := netutil.TestProxy(b.Type, b.Host, b.Port, b.User, pass)
	if err != nil && res == nil {
		Error(w, 400, "failed", err.Error())
		return
	}
	OK(w, res)
}

// portTest asks an external service whether the torrent ports are
// reachable from the internet (only after the administrator allowed it).
func (s *Server) portTest(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	st := s.M.Settings()
	// Consent is given per test (the external service learns the NAS's
	// public address and ports) and never stored
	var b struct {
		Consent bool `json:"consent"`
	}
	Decode(r, &b)
	if !b.Consent {
		Error(w, 403, "consent_required", "測試連入埠需要你同意讓外部服務檢查 NAS 的對外 IP 與埠")
		return
	}
	// libtorrent listens on the first port of the range (the others are only
	// tried when it is taken), so that is the port to check. IPv4 only: the
	// check service does not probe IPv6 addresses
	port := st.Torrent.PortFrom
	v4, err4 := checkPort("tcp4", port)
	if err4 != nil {
		Error(w, 502, "failed", err4.Error())
		return
	}
	OK(w, map[string]any{"ip": v4.IP, "results": []map[string]any{{"port": port, "reachable": v4.Reachable}}})
}

type portCheck struct {
	IP        string `json:"ip"`
	Port      int    `json:"port"`
	Reachable bool   `json:"reachable"`
}

// checkPort asks ifconfig.co whether the port is reachable from the internet
// at the address it sees (network picks the address family).
func checkPort(network string, port int) (*portCheck, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	cl := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		},
		TLSHandshakeTimeout: 10 * time.Second,
	}}
	resp, err := cl.Get(fmt.Sprintf("https://ifconfig.co/port/%d", port))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var v portCheck
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&v); err != nil || v.IP == "" {
		return nil, fmt.Errorf("port check: unexpected answer (HTTP %d)", resp.StatusCode)
	}
	return &v, nil
}

func (s *Server) settingsRoutes() {
	s.Route("GET /settings", "settings:read", AdminOnly, s.getSettings)
	s.Route("PUT /settings", "settings:write", AdminOnly, s.putSettings)
	s.Route("POST /settings/proxy-test", "settings:write", AdminOnly, s.proxyTest)
	s.Route("POST /settings/port-test", "settings:write", AdminOnly, s.portTest)
	s.Route("GET /schedule", "settings:read", AdminOnly, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		st := s.M.Settings()
		mode, next, nextMode := s.M.ScheduleState()
		resp := map[string]any{"enabled": st.Schedule.Enabled, "days": st.Schedule.Days, "mode": mode}
		if !next.IsZero() {
			resp["next_change"], resp["next_mode"] = next.Unix(), nextMode
		}
		OK(w, resp)
	})
	s.Route("PUT /schedule", "settings:write", AdminOnly, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		var b struct {
			Enabled *bool     `json:"enabled"`
			Days    *[]string `json:"days"`
		}
		if err := Decode(r, &b); err != nil {
			Error(w, 400, "bad_request", err.Error())
			return
		}
		st := s.M.Settings()
		if b.Enabled != nil {
			st.Schedule.Enabled = *b.Enabled
		}
		if b.Days != nil {
			if len(*b.Days) != 7 {
				Error(w, 400, "bad_request", "days must have 7 entries (Monday first)")
				return
			}
			for i, d := range *b.Days {
				if len(d) != 24 || strings.Trim(d, "012") != "" {
					Error(w, 400, "bad_request", "each day has 24 characters of 0, 1 or 2")
					return
				}
				st.Schedule.Days[i] = d
			}
		}
		if err := s.M.SaveSettings(st); err != nil {
			Error(w, 500, "failed", err.Error())
			return
		}
		OK(w, map[string]any{"ok": true, "enabled": st.Schedule.Enabled, "days": st.Schedule.Days})
	})
}

// unusableFolder answers for a folder that cannot hold downloads: on a
// read-only volume, or the root of the homes share.
func unusableFolder(w http.ResponseWriter, real string) bool {
	switch {
	case !core.Writable(real):
		Error(w, 400, "folder_read_only", "這個資料夾無法寫入")
	case !core.Choosable(real):
		Error(w, 400, "folder_not_allowed", "不能使用這個資料夾")
	default:
		return false
	}
	return true
}
