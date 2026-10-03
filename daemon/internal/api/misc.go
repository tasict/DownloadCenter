package api

import (
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/engine"
	"downloadcenter/internal/qts"
)

// AccountVerifier checks file-hosting accounts (implemented by hosters).
type AccountVerifier interface {
	Verify(a *core.Account, secret string) (map[string]any, error)
	Services() []map[string]any
}

// Verifier is set by main when the hosters package is available.
var Verifier AccountVerifier

func (s *Server) engineCaps() map[string]engine.Caps {
	out := map[string]engine.Caps{}
	for name, e := range s.M.Engines {
		if e != nil {
			out[name] = e.Caps()
		}
	}
	if s.M.URL != nil {
		out[s.M.URL.Name()] = s.M.URL.Caps()
	}
	return out
}

// btInfo is the torrent engine's name and capabilities (empty without dc-bt).
func (s *Server) btInfo() (string, engine.Caps) {
	if bt := s.M.BTEngine(); bt != nil {
		return bt.Name(), bt.Caps()
	}
	return "", engine.Caps{}
}

// proxyChoices lists what the add dialog offers: the profiles this user may
// pick, the defaults and whether "no proxy" is allowed.
func (s *Server) proxyChoices(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	px := s.M.Settings().Proxy
	list := []map[string]any{}
	sites := false
	for _, pr := range px.Profiles {
		if pr.Sites != "" {
			sites = true
		}
		if p.Admin || pr.ForUsers {
			list = append(list, map[string]any{"id": pr.ID, "name": pr.Name, "type": pr.Type})
		}
	}
	name := func(id string) string {
		if pr := px.Profile(id); pr != nil {
			return pr.Name
		}
		return ""
	}
	OK(w, map[string]any{"profiles": list, "default": name(px.URLDefault), "torrents": name(px.BT),
		"can_direct": p.Admin || !px.RequireForUsers, "by_site": sites})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	u, _ := s.Auth.GetUser(p.User)
	prefs := map[string]any{}
	if u != nil {
		prefs = u.Prefs
	}
	st := s.M.Settings()
	home := ""
	if !p.Admin {
		home = s.M.DisplayPath(p.User, s.M.UserDownloadDir(p.User))
	}
	btName, btCaps := s.btInfo()
	var urlCaps engine.Caps
	if s.M.URL != nil {
		urlCaps = s.M.URL.Caps()
	}
	resp := map[string]any{
		"user": p.User, "role": p.Role, "admin": p.Admin, "qts_admin": p.QTSAdmin, "via": p.Via,
		"scopes": p.Scopes(), "tasks": map[bool]string{true: "all", false: "own"}[p.AllTasks],
		"prefs": prefs, "home_folder": home,
		"bt_engine": btName, "caps": btCaps, "url_caps": urlCaps, "engines": s.engineCaps(),
		"defaults": map[string]any{
			"folder":      s.M.DisplayPath(p.User, st.TempDir),
			"move_to":     s.M.DisplayPath(p.User, st.MoveDir),
			"auto_remove": st.AutoRemove,
		},
		"nas":     map[string]any{"hostname": qts.Hostname(), "firmware": qts.Firmware(), "version": s.Version, "time": time.Now().Unix(), "tz_offset": tzOffset()},
		"hosters": hosterServices(w),
	}
	if p.Via == "token" && p.Token != nil {
		resp["token"] = map[string]any{"id": p.Token.ID, "name": p.Token.Name, "expires_at": p.Token.ExpiresAt,
			"folders": p.Token.Folders, "sources": p.Token.Sources, "ip_allow": p.Token.IPAllow, "rate_limit": p.Token.RateLimit}
	}
	for k, v := range s.Extra {
		if p.Admin {
			resp[k] = v
		}
	}
	OK(w, resp)
}

func tzOffset() int {
	_, off := time.Now().Zone()
	return off
}

// hosterServices lists the file-hosting services with their labels and
// help in the response's language.
func hosterServices(w http.ResponseWriter) []map[string]any {
	if Verifier == nil {
		return []map[string]any{}
	}
	var out []map[string]any
	for _, svc := range Verifier.Services() {
		c := map[string]any{}
		for k, v := range svc {
			if s, ok := v.(string); ok && (k == "title" || k == "secret_label" || k == "help") {
				v = Tr(w, s)
			}
			c[k] = v
		}
		out = append(out, c)
	}
	return out
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	var down, up int64
	active := 0
	for _, t := range s.M.List() {
		if !p.SeesOwner(t.Owner) {
			continue
		}
		down += t.DownRate
		up += t.UpRate
		if t.State == core.StDownloading {
			active++
		}
	}
	mode, next, nextMode := s.M.ScheduleState()
	st := s.M.Settings()
	sched := map[string]any{"enabled": st.Schedule.Enabled, "mode": mode}
	if !next.IsZero() {
		sched["next_change"] = next.Unix()
		sched["next_mode"] = nextMode
	}
	var folders []map[string]any
	seen := map[string]bool{}
	addF := func(real string) {
		if real == "" || seen[real] {
			return
		}
		seen[real] = true
		folders = append(folders, map[string]any{"path": s.M.DisplayPath(p.User, real), "free": core.FreeSpace(real)})
	}
	if p.Admin {
		addF(st.TempDir)
		addF(st.MoveDir)
	} else {
		addF(s.M.UserDownloadDir(p.User))
	}
	resp := map[string]any{"down_rate": down, "up_rate": up, "downloading": active, "schedule": sched, "folders": folders}
	if p.Admin {
		resp["engines"] = s.M.EngineHealth()
	}
	OK(w, resp)
}

// folders lists sub-folders for the folder pickers. Regular users only ever
// get their home Download folder; a token limited to some folders sees those
// and what lies below them. free_only=1 answers the free space alone.
func (s *Server) folders(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	q := r.URL.Query()
	path := strings.Trim(q.Get("path"), "/")
	freeOnly := q.Get("free_only") == "1"
	if !p.Admin {
		home := s.M.UserDownloadDir(p.User)
		if freeOnly {
			OK(w, map[string]any{"free": core.FreeSpace(home)})
			return
		}
		wr := core.Writable(home)
		OK(w, map[string]any{"folders": []core.Folder{{Name: filepath.Base(home), Path: s.M.DisplayPath(p.User, home), Writable: wr, Choosable: wr}}})
		return
	}
	if path == "" {
		if len(p.Folders) > 0 {
			OK(w, map[string]any{"folders": s.allowedFolders(p)})
		} else {
			OK(w, map[string]any{"folders": s.M.SharedFolders()})
		}
		return
	}
	real, err := s.M.ResolvePath(p.User, path)
	if err != nil {
		Error(w, 404, "folder_not_found", "找不到這個資料夾")
		return
	}
	if !s.folderAllowed(p, real) {
		Error(w, 403, "folder_not_allowed", "不能使用這個資料夾")
		return
	}
	if freeOnly {
		OK(w, map[string]any{"free": core.FreeSpace(real)})
		return
	}
	out, err := core.ListFolders(real, path)
	if err != nil {
		Error(w, 404, "folder_not_found", "找不到這個資料夾")
		return
	}
	OK(w, map[string]any{"folders": out, "free": core.FreeSpace(real), "writable": core.Writable(real), "choosable": core.Choosable(real)})
}

// allowedFolders is the top level of the picker for a token limited to some
// folders: those folders, named by their whole path.
func (s *Server) allowedFolders(p *auth.Principal) []core.Folder {
	out := []core.Folder{}
	for _, f := range p.Folders {
		real, err := s.M.ResolvePath(p.User, f)
		if err != nil {
			continue
		}
		d := s.M.DisplayPath(p.User, real)
		c := core.Choosable(real)
		out = append(out, core.Folder{Name: d, Path: d, Writable: c || core.Writable(real), Choosable: c, Free: core.FreeSpace(real)})
	}
	return out
}

// folderAllowed applies a token's folder allowlist to a real path.
func (s *Server) folderAllowed(p *auth.Principal, real string) bool {
	if len(p.Folders) == 0 {
		return true
	}
	for _, f := range p.Folders {
		if fr, err := s.M.ResolvePath(p.User, f); err == nil && (real == fr || strings.HasPrefix(real, fr+"/")) {
			return true
		}
	}
	return false
}

func (s *Server) makeFolder(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	var b struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := Decode(r, &b); err != nil {
		Error(w, 400, "bad_request", err.Error())
		return
	}
	np, err := s.M.MakeFolder(p.User, b.Path, strings.TrimSpace(b.Name))
	switch {
	case err == nil:
		OK(w, map[string]any{"path": np})
	case errors.Is(err, core.ErrBadName):
		Error(w, 400, "bad_name", "資料夾名稱不能以 . @ # 開頭或以空格、句點結尾，也不能包含 / \\ | : ? < > * \"")
	case errors.Is(err, core.ErrExists):
		Error(w, 409, "folder_exists", "已經有同名的資料夾")
	case errors.Is(err, core.ErrReadOnly):
		Error(w, 403, "folder_read_only", "這個資料夾無法寫入")
	case errors.Is(err, core.ErrNoFolder):
		Error(w, 404, "folder_not_found", "找不到這個資料夾")
	case errors.Is(err, core.ErrFolder):
		Error(w, 403, "folder_not_allowed", "不能使用這個資料夾")
	default:
		log.Printf("api: make folder %q in %q: %v", b.Name, b.Path, err)
		Error(w, 500, "failed", "無法建立資料夾")
	}
}

func (s *Server) miscRoutes() {
	s.Route("GET /me", "", 0, s.me)
	s.Route("GET /proxies", "tasks:add", 0, s.proxyChoices)
	s.Route("GET /me/portrait", "", Session, s.portrait)
	s.Route("PUT /me/prefs", "", Session, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		var b map[string]any
		if err := Decode(r, &b); err != nil {
			Error(w, 400, "bad_request", err.Error())
			return
		}
		if err := s.Auth.SetPrefs(p.User, b); err != nil {
			Error(w, 400, "failed", err.Error())
			return
		}
		OK(w, map[string]any{"ok": true})
	})
	s.Route("POST /logout", "", Session, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		s.Auth.ForgetSID(p.SID)
		OK(w, map[string]any{"ok": true})
	})
	s.Route("GET /stats", "stats:read", 0, s.stats)
	s.Route("GET /folders", "tasks:add", 0, s.folders)
	s.Route("POST /folders", "", AdminOnly|Session, s.makeFolder)

	// Users (administrators)
	s.Route("GET /users", "", AdminOnly|Session, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		users := s.Auth.Users()
		if users == nil {
			users = []auth.User{}
		}
		OK(w, map[string]any{"users": users, "accounts": qts.Accounts(), "homes_enabled": qts.HomesRoot() != ""})
	})
	s.Route("POST /users", "", AdminOnly|Session, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		var b struct {
			Name  string   `json:"name"`
			Names []string `json:"names"`
			Role  string   `json:"role"`
		}
		if err := Decode(r, &b); err != nil {
			Error(w, 400, "bad_request", err.Error())
			return
		}
		if b.Name != "" {
			b.Names = append(b.Names, b.Name)
		}
		for _, n := range b.Names {
			if err := s.Auth.AddUser(n, b.Role); err != nil {
				Error(w, 400, "failed", n+"："+err.Error())
				return
			}
		}
		OK(w, map[string]any{"ok": true})
	})
	s.Route("PATCH /users/{name}", "", AdminOnly|Session, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		var b struct {
			Role string `json:"role"`
		}
		Decode(r, &b)
		if err := s.Auth.SetRole(r.PathValue("name"), b.Role); err != nil {
			Error(w, 400, "failed", err.Error())
			return
		}
		OK(w, map[string]any{"ok": true})
	})
	s.Route("DELETE /users/{name}", "", AdminOnly|Session, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		if r.PathValue("name") == p.User {
			Error(w, 400, "failed", "不能移除自己")
			return
		}
		if err := s.Auth.RemoveUser(r.PathValue("name")); err != nil {
			Error(w, 400, "failed", err.Error())
			return
		}
		OK(w, map[string]any{"ok": true})
	})

	// Site and file-hosting accounts (each user their own)
	s.Route("GET /accounts", "", Session, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		list := s.M.Accounts(p.User)
		if list == nil {
			list = []*core.Account{}
		}
		OK(w, map[string]any{"accounts": list, "kinds": core.AccountKinds, "services": hosterServices(w)})
	})
	s.Route("POST /accounts", "", Session, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		var b struct {
			Kind     string `json:"kind"`
			Host     string `json:"host"`
			Username string `json:"username"`
			Secret   string `json:"secret"`
			Enabled  *bool  `json:"enabled"`
		}
		if err := Decode(r, &b); err != nil {
			Error(w, 400, "bad_request", err.Error())
			return
		}
		a := &core.Account{Owner: p.User, Kind: b.Kind, Host: b.Host, Username: b.Username, Enabled: b.Enabled == nil || *b.Enabled}
		if err := s.M.SaveAccount(a, b.Secret); err != nil {
			Error(w, 400, "failed", err.Error())
			return
		}
		if a.Kind != "site" && Verifier != nil {
			if info, err := Verifier.Verify(a, b.Secret); err == nil {
				s.M.SetAccountInfo(a.ID, info)
			} else {
				s.M.SetAccountInfo(a.ID, map[string]any{"error": err.Error(), "verified_at": time.Now().Unix()})
			}
		}
		na, _ := s.M.Account(a.ID)
		OK(w, map[string]any{"account": na})
	})
	s.Route("PATCH /accounts/{id}", "", Session, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		a, err := s.M.Account(r.PathValue("id"))
		if err != nil || a.Owner != p.User {
			Error(w, 404, "not_found", "找不到這個帳號")
			return
		}
		var b struct {
			Host     *string `json:"host"`
			Username *string `json:"username"`
			Secret   string  `json:"secret"`
			Enabled  *bool   `json:"enabled"`
		}
		if err := Decode(r, &b); err != nil {
			Error(w, 400, "bad_request", err.Error())
			return
		}
		if b.Host != nil {
			a.Host = *b.Host
		}
		if b.Username != nil {
			a.Username = *b.Username
		}
		if b.Enabled != nil {
			a.Enabled = *b.Enabled
		}
		if err := s.M.SaveAccount(a, b.Secret); err != nil {
			Error(w, 400, "failed", err.Error())
			return
		}
		na, _ := s.M.Account(a.ID)
		OK(w, map[string]any{"account": na})
	})
	s.Route("DELETE /accounts/{id}", "", Session, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		a, err := s.M.Account(r.PathValue("id"))
		if err != nil || a.Owner != p.User {
			Error(w, 404, "not_found", "找不到這個帳號")
			return
		}
		s.M.DeleteAccount(a.ID)
		OK(w, map[string]any{"ok": true})
	})
	s.Route("POST /accounts/{id}/verify", "", Session, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		a, err := s.M.Account(r.PathValue("id"))
		if err != nil || a.Owner != p.User {
			Error(w, 404, "not_found", "找不到這個帳號")
			return
		}
		if a.Kind == "site" || Verifier == nil {
			OK(w, map[string]any{"account": a})
			return
		}
		info, verr := Verifier.Verify(a, s.M.AccountSecret(a.ID))
		if verr != nil {
			info = map[string]any{"error": verr.Error(), "verified_at": time.Now().Unix()}
		}
		s.M.SetAccountInfo(a.ID, info)
		na, _ := s.M.Account(a.ID)
		OK(w, map[string]any{"account": na})
	})

	// Personal access tokens (created in the UI only)
	s.Route("GET /tokens", "", Session, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		list := s.Auth.Tokens(p.User)
		if list == nil {
			list = []*auth.Token{}
		}
		OK(w, map[string]any{"tokens": list, "scopes": auth.AllScopes})
	})
	s.Route("POST /tokens", "", Session, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		var b struct {
			Name      string   `json:"name"`
			Scopes    []string `json:"scopes"`
			Tasks     string   `json:"tasks"`
			Folders   []string `json:"folders"`
			Sources   []string `json:"sources"`
			IPAllow   []string `json:"ip_allow"`
			Days      int      `json:"expires_days"`
			RateLimit int      `json:"rate_limit"`
		}
		if err := Decode(r, &b); err != nil {
			Error(w, 400, "bad_request", err.Error())
			return
		}
		t := &auth.Token{Owner: p.User, Name: b.Name, Scopes: b.Scopes, Tasks: b.Tasks, Folders: b.Folders, Sources: b.Sources,
			IPAllow: b.IPAllow, RateLimit: b.RateLimit}
		for _, f := range t.Folders {
			if _, err := s.M.ResolvePath(p.User, f); err != nil {
				Error(w, 400, "folder_not_found", "找不到資料夾 "+f)
				return
			}
		}
		if b.Days > 0 {
			t.ExpiresAt = time.Now().Add(time.Duration(b.Days) * 24 * time.Hour).Unix()
		}
		value, err := s.Auth.CreateToken(t, p.Admin)
		if err != nil {
			Error(w, 400, "failed", err.Error())
			return
		}
		s.M.Emit(core.Event{Type: "security.token_created", Data: map[string]any{"token": t.ID, "owner": p.User, "name": t.Name}})
		OK(w, map[string]any{"token": t, "value": value})
	})
	// Edit what a token may do; expires_days absent keeps the expiry, 0 means
	// never, n days counts from now
	s.Route("PATCH /tokens/{id}", "", Session, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		t := s.Auth.TokenOf(r.PathValue("id"), p.User)
		if t == nil {
			Error(w, 404, "not_found", "找不到這個權杖")
			return
		}
		var b struct {
			Name      *string   `json:"name"`
			Scopes    *[]string `json:"scopes"`
			Tasks     *string   `json:"tasks"`
			Folders   *[]string `json:"folders"`
			Sources   *[]string `json:"sources"`
			IPAllow   *[]string `json:"ip_allow"`
			Days      *int      `json:"expires_days"`
			RateLimit *int      `json:"rate_limit"`
		}
		if err := Decode(r, &b); err != nil {
			Error(w, 400, "bad_request", err.Error())
			return
		}
		if b.Name != nil {
			t.Name = *b.Name
		}
		if b.Scopes != nil {
			t.Scopes = *b.Scopes
		}
		if b.Tasks != nil {
			t.Tasks = *b.Tasks
		}
		if b.Folders != nil {
			t.Folders = *b.Folders
			for _, f := range t.Folders {
				if _, err := s.M.ResolvePath(p.User, f); err != nil {
					Error(w, 400, "folder_not_found", "找不到資料夾 "+f)
					return
				}
			}
		}
		if b.Sources != nil {
			t.Sources = *b.Sources
		}
		if b.IPAllow != nil {
			t.IPAllow = *b.IPAllow
		}
		if b.Days != nil {
			t.ExpiresAt = 0
			if *b.Days > 0 {
				t.ExpiresAt = time.Now().Add(time.Duration(*b.Days) * 24 * time.Hour).Unix()
			}
		}
		if b.RateLimit != nil {
			t.RateLimit = *b.RateLimit
		}
		if err := s.Auth.UpdateToken(t, p.Admin); err != nil {
			Error(w, 400, "failed", err.Error())
			return
		}
		OK(w, map[string]any{"token": s.Auth.TokenOf(t.ID, p.User)})
	})
	s.Route("POST /tokens/{id}/regenerate", "", Session, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		value, err := s.Auth.RegenerateToken(r.PathValue("id"), p.User)
		if err != nil {
			Error(w, 404, "not_found", "找不到這個權杖")
			return
		}
		OK(w, map[string]any{"value": value})
	})
	s.Route("DELETE /tokens/{id}", "", Session, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		s.Auth.RevokeToken(r.PathValue("id"), p.User)
		OK(w, map[string]any{"ok": true})
	})
	s.Route("GET /tokens/audit", "", Session, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		owner := p.User
		if p.Admin && r.URL.Query().Get("all") == "1" {
			owner = ""
		}
		list := s.Auth.AuditLog(owner, 200)
		if list == nil {
			list = []auth.AuditEntry{}
		}
		OK(w, map[string]any{"audit": list})
	})
}
