package v4

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"downloadcenter/internal/api"
	"downloadcenter/internal/auth"
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
// Download folder (regular users).
func (s *service) dir(c *call) result {
	m := s.m
	path := strings.Trim(c.p.get("path"), "/")
	var out []dirEntry
	if !c.who.Admin {
		home := m.UserDownloadDir(c.who.User)
		hd := m.DisplayPath(c.who.User, home)
		if path == "" {
			return result{"data": []dirEntry{{Dir: filepath.Base(home), Path: hd, Writtable: 1, Temporary: 1}}}
		}
		real, err := m.ResolvePath(c.who.User, path)
		if err != nil {
			return fail(errFolderNotFound)
		}
		if real != home && !strings.HasPrefix(real, home+"/") {
			return fail(errFolderDenied)
		}
		return result{"data": listDirs(real, path)}
	}
	if path == "" {
		for _, sh := range qts.Shares() {
			out = append(out, dirEntry{Dir: sh.Name, Path: sh.Name, Writtable: 1, Temporary: 1})
		}
		if qts.HomeDir(c.who.User) != "" {
			out = append(out, dirEntry{Dir: "home", Path: "home", Writtable: 1, Temporary: 1})
		}
		if qts.HomesRoot() != "" {
			out = append(out, dirEntry{Dir: "homes", Path: "homes", Writtable: 1, Temporary: 1})
		}
		if out == nil {
			out = []dirEntry{}
		}
		return result{"data": out}
	}
	real, err := m.ResolvePath(c.who.User, path)
	if err != nil {
		return fail(errFolderNotFound)
	}
	return result{"data": listDirs(real, path)}
}

func listDirs(real, display string) []dirEntry {
	out := []dirEntry{}
	ents, _ := os.ReadDir(real)
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, ".") || strings.HasPrefix(n, "@") || strings.HasPrefix(n, "#") || strings.HasPrefix(n, "Network Recycle Bin") {
			continue
		}
		if fi, err := os.Stat(filepath.Join(real, n)); err != nil || !fi.IsDir() {
			continue
		}
		out = append(out, dirEntry{Dir: n, Path: display + "/" + n, Writtable: 1, Temporary: 1})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Dir) < strings.ToLower(out[j].Dir) })
	return out
}

func (s *service) makeDir(c *call) result {
	name := c.p.get("name")
	if name == "" || strings.ContainsAny(name, "/|\\:?<>*\"") || name == "." || name == ".." {
		return fail(errParameter)
	}
	real, err := s.m.ResolvePath(c.who.User, strings.Trim(c.p.get("path"), "/"))
	if err != nil {
		return fail(errFolderNotFound)
	}
	if !c.who.Admin {
		home := s.m.UserDownloadDir(c.who.User)
		if real != home && !strings.HasPrefix(real, home+"/") {
			return fail(errFolderDenied)
		}
	}
	if err := os.Mkdir(filepath.Join(real, name), 0777); err != nil {
		if os.IsExist(err) {
			return result{}
		}
		return fail(errProcessFail)
	}
	return result{}
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
