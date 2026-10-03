package v4

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"downloadcenter/internal/api"
	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/engine"
	"downloadcenter/internal/netutil"
	"downloadcenter/internal/qts"
)

// folders returns the default temp and move folders shown to the caller.
func (c *call) folders() (temp, move string) {
	m := c.s.m
	if !c.who.Admin {
		home := m.DisplayPath(c.who.User, m.UserDownloadDir(c.who.User))
		return home, home
	}
	st := m.Settings()
	temp = m.DisplayPath(c.who.User, st.TempDir)
	move = m.DisplayPath(c.who.User, st.MoveDir)
	if move == "" {
		move = temp
	}
	return temp, move
}

func (c *call) userEnv() result {
	temp, move := c.folders()
	return result{"sid": c.sid, "user": c.who.User, "admin": b2i(c.who.Admin), "temp": temp, "move": move, "explorer": 1}
}

// login: parameterless = probe with the forwarded cookies; with sid = reuse
// a QTS session; with user/pass = forward to authLogin.cgi (credentials are
// never stored or logged).
func (s *service) login(c *call) result {
	user := c.p.get("user")
	pass := c.p.get("pass")
	if user == "" || !c.p.has("pass") {
		if code := c.authenticate(); code != errOK {
			if code == errSessionTimeout {
				return fail(errAuthFail)
			}
			return fail(code)
		}
		return c.userEnv()
	}
	user = fixDoubleUTF8(user)
	pwd := base64.StdEncoding.EncodeToString([]byte(ezDecode(pass)))
	a, err := qts.PasswordLogin(user, pwd, api.ClientIP(c.r), c.r.UserAgent())
	if err != nil {
		return fail(errException)
	}
	if !a.Passed() || a["authSid"] == "" {
		// Wrong credentials, expired password or a second factor required:
		// the official clients cannot do 2-step verification
		return fail(errAuthFail)
	}
	name := a.User()
	if name == "" {
		name = user
	}
	p, err := s.au.FromQTS(name, a.IsAdmin())
	if err == auth.ErrNotOnList {
		qts.Logout(a["authSid"], api.ClientIP(c.r), c.r.UserAgent())
		return fail(errPrivilege)
	}
	if err != nil {
		return fail(errException)
	}
	p.Via, p.SID = "v4", a["authSid"]
	c.who, c.sid = p, a["authSid"]
	return c.userEnv()
}

func (s *service) logout(c *call) result {
	if sid := c.sidFrom(); sid != "" && sid != "dev" {
		qts.Logout(sid, api.ClientIP(c.r), c.r.UserAgent())
		s.au.ForgetSID(sid)
	}
	return result{}
}

func (s *service) env(c *call) result {
	res := c.userEnv()
	delete(res, "sid")
	ver := ""
	if e := s.m.BTEngine(); e != nil {
		ver = e.Version()
	}
	build := ""
	if exe, err := os.Executable(); err == nil {
		if st, err := os.Stat(exe); err == nil {
			build = st.ModTime().Format("20060102")
		}
	}
	res["firmware"] = qts.Firmware()
	res["app"] = s.srv.Version
	res["build"] = build
	res["libtorrent"] = ver
	res["task_limit"] = 50
	res["localtime"] = time.Now().Format("2006-01-02T15:04:05")
	res["demo"] = 0
	res["isGeneric"] = 0
	res["home"] = b2i(qts.HomeDir(c.who.User) != "")
	return res
}

type dirEntry struct {
	Dir       string `json:"dir"`
	Path      string `json:"path"`
	Writtable int    `json:"writtable"`
	Temporary int    `json:"temporary"`
}

// dir lists folders: top level = shares (administrators) or the home
// Download folder (regular users, whose tasks always go there, so there is
// nothing below it to choose).
func (s *service) dir(c *call) result {
	m := s.m
	path := strings.Trim(c.p.get("path"), "/")
	if !c.who.Admin {
		home := m.UserDownloadDir(c.who.User)
		if path == "" {
			return result{"data": []dirEntry{{Dir: filepath.Base(home), Path: m.DisplayPath(c.who.User, home), Writtable: 1, Temporary: 1}}}
		}
		real, err := m.ResolvePath(c.who.User, path)
		if err != nil {
			return fail(errFolderNotFound)
		}
		if !core.SameDir(real, home) {
			return fail(errFolderDenied)
		}
		return result{"data": []dirEntry{}}
	}
	if path == "" {
		out := []dirEntry{}
		for _, f := range m.SharedFolders() {
			if f.Path == "homes" && qts.HomeDir(c.who.User) != "" {
				out = append(out, dirEntry{Dir: "home", Path: "home", Writtable: 1, Temporary: 1})
			}
			out = append(out, entryOf(f))
		}
		return result{"data": out}
	}
	real, err := m.ResolvePath(c.who.User, path)
	if err != nil {
		return fail(errFolderNotFound)
	}
	ls, err := core.ListFolders(real, path)
	if err != nil {
		return fail(errFolderNotFound)
	}
	out := make([]dirEntry, 0, len(ls))
	for _, f := range ls {
		out = append(out, entryOf(f))
	}
	return result{"data": out}
}

func entryOf(f core.Folder) dirEntry {
	w := 0
	if f.Writable {
		w = 1
	}
	return dirEntry{Dir: f.Name, Path: f.Path, Writtable: w, Temporary: w}
}

func (s *service) makeDir(c *call) result {
	if !c.who.Admin {
		return fail(errFolderDenied)
	}
	_, err := s.m.MakeFolder(c.who.User, c.p.get("path"), strings.TrimSpace(c.p.get("name")))
	switch {
	case err == nil, errors.Is(err, core.ErrExists):
		return result{}
	case errors.Is(err, core.ErrBadName):
		return fail(errParameter)
	case errors.Is(err, core.ErrNoFolder):
		return fail(errFolderNotFound)
	case errors.Is(err, core.ErrFolder), errors.Is(err, core.ErrReadOnly):
		return fail(errFolderDenied)
	}
	return fail(errProcessFail)
}

// socks5 tests a SOCKS5 proxy. Only engines that can use SOCKS5 (libtorrent)
// offer it; without dc-bt the call answers "API does not exist".
func (s *service) socks5(c *call) result {
	if !s.btCaps().Socks5Peers {
		return fail(errAPINotExists)
	}
	port, _ := strconv.Atoi(c.p.get("port"))
	host := c.p.get("host")
	if host == "" || port <= 0 || port > 65535 {
		return fail(errParameter)
	}
	user := c.p.get("user")
	pass := ""
	if c.p.has("pass") {
		pass = ezDecode(c.p.get("pass"))
	}
	_, err := netutil.TestProxy("socks5", host, port, user, pass)
	if code := netutil.SocksCode(err); code != 0 {
		return fail(code)
	}
	return result{}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// btCaps and btVersion tolerate a manager without a torrent engine (tests).
func (s *service) btCaps() engine.Caps {
	if e := s.m.BTEngine(); e != nil {
		return e.Caps()
	}
	return engine.Caps{}
}

func (s *service) btVersion() string {
	if e := s.m.BTEngine(); e != nil {
		return e.Version()
	}
	return ""
}
