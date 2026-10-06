// Package v4 is the V4-compatible API of the official Download Station
// (/downloadstation/V4/...) for Qget, Qfile and browser extensions, and the
// switch that points the firmware's Qdownload link at this package.
//
// Wire format: that of the official Download Station V4 API, as its clients
// use it. Every answer is HTTP 200 with a JSON object {"error": <code>, ...};
// Rss/* and Addon/* answer error 2 (API does not exist), which official
// clients already handle quietly.
package v4

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"downloadcenter/internal/api"
	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/qts"
)

// Error codes (v4-api.md §8).
const (
	errServiceDisabled = -1
	errOK              = 0
	errParameter       = 1
	errAPINotExists    = 2
	errParamNotFound   = 3
	errAuthFail        = 4
	errSessionTimeout  = 5
	errPrivilege       = 6
	errPermission      = 7
	errException       = 8
	errFolderNotFound  = 4096
	errFolderDenied    = 4097
	errProcessFail     = 4098
	errNoTask          = 8192
	errTaskNotFound    = 8193
	errNotOwner        = 8194
	errDuplicate       = 8196
	errFilesMoving     = 8197
	errURLNotSupported = 12288
	errURLDownloadFail = 12289
	errMagnetFormat    = 16384
	errTorrentMissing  = 16385
	errTorrentFormat   = 16386
	errMagnetFail      = 16388
	errTempNotFolder   = 20482
	errMoveNotFolder   = 20483
	errSpace           = 20488
	errMoveFail        = 20490
)

type upFile struct {
	name string
	data []byte
}

// params holds request parameters: query and form values merged, trailing
// "[...]" stripped from names, repeated keys kept in order.
type params map[string][]string

func (p params) get(k string) string {
	if v := p[k]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func (p params) has(k string) bool { _, ok := p[k]; return ok }

func (p params) all(k string) []string { return p[k] }

var bracketRe = regexp.MustCompile(`\[[^\]]*\]$`)

func (p params) add(k, v string) {
	k = bracketRe.ReplaceAllString(k, "")
	p[k] = append(p[k], v)
}

// addQuery adds the pairs of an urlencoded string in their original order
// (positional parameters such as SetFile's priority depend on it).
func (p params) addQuery(q string) {
	for _, pair := range strings.Split(q, "&") {
		if pair == "" {
			continue
		}
		k, v, _ := strings.Cut(pair, "=")
		uk, err1 := url.QueryUnescape(k)
		uv, err2 := url.QueryUnescape(v)
		if err1 != nil || err2 != nil {
			log.Printf("v4: skipping a malformed parameter")
			continue
		}
		p.add(uk, uv)
	}
}

// parseRequest reads GET query and POST form or multipart parameters.
func parseRequest(r *http.Request) (params, []upFile, error) {
	p := params{}
	p.addQuery(r.URL.RawQuery)
	if r.Method != "POST" || r.Body == nil {
		return p, nil, nil
	}
	ct := r.Header.Get("Content-Type")
	mt, mp, _ := mime.ParseMediaType(ct)
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		return p, nil, err
	}
	if strings.HasPrefix(mt, "multipart/") {
		boundary := mp["boundary"]
		if boundary == "" {
			boundary = boundaryFromBody(body)
		}
		if boundary == "" {
			return p, nil, nil
		}
		var files []upFile
		mr := multipart.NewReader(bytes.NewReader(body), boundary)
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			data, _ := io.ReadAll(io.LimitReader(part, 32<<20))
			name := bracketRe.ReplaceAllString(part.FormName(), "")
			if part.FileName() != "" {
				files = append(files, upFile{name: part.FileName(), data: data})
				_ = name
			} else {
				p.add(part.FormName(), string(data))
			}
			part.Close()
		}
		return p, files, nil
	}
	p.addQuery(string(body))
	return p, nil, nil
}

// boundaryFromBody takes the boundary from the first line ("--<boundary>"),
// for clients that send multipart/form-data without a boundary parameter.
func boundaryFromBody(body []byte) string {
	line := body
	if i := bytes.IndexByte(body, '\n'); i >= 0 {
		line = body[:i]
	}
	line = bytes.TrimRight(line, "\r")
	if !bytes.HasPrefix(line, []byte("--")) || len(line) < 3 {
		return ""
	}
	return string(line[2:])
}

// handler state of one call.
type call struct {
	s         *service
	w         http.ResponseWriter
	r         *http.Request
	p         params
	files     []upFile
	who       *auth.Principal
	sid       string
	cookieSID bool // the session came from a cookie, not the sid parameter
}

type result map[string]any

func fail(code int) result { return result{"error": code} }

func failReason(code int, reason string) result { return result{"error": code, "reason": reason} }

type endpoint struct {
	public bool // no session needed
	admin  bool
	fn     func(c *call) result
}

type service struct {
	srv  *api.Server
	m    *core.Manager
	au   *auth.Service
	root string
	data string
	eps  map[string]endpoint
}

var pathRe = regexp.MustCompile(`^(?:/DownloadCenter)?/downloadstation/V\d+/([A-Za-z]+)/([A-Za-z0-9]+)/?$`)

// Register adds the V4 routes and the takeover endpoints.
func Register(srv *api.Server, m *core.Manager, au *auth.Service, root, data string) {
	s := &service{srv: srv, m: m, au: au, root: root, data: data}
	s.eps = map[string]endpoint{
		"Misc/Login":          {public: true, fn: s.login},
		"Misc/Logout":         {public: true, fn: s.logout},
		"Misc/Env":            {fn: s.env},
		"Misc/Dir":            {fn: s.dir},
		"Misc/MakeDir":        {admin: true, fn: s.makeDir},
		"Misc/Socks5":         {admin: true, fn: s.socks5},
		"Task/Query":          {fn: s.query},
		"Task/Status":         {fn: s.status},
		"Task/Detail":         {fn: s.detail},
		"Task/AddUrl":         {fn: s.addURL},
		"Task/AddTorrent":     {fn: s.addTorrent},
		"Task/Start":          {fn: s.start},
		"Task/Stop":           {fn: s.stop},
		"Task/Pause":          {fn: s.pause},
		"Task/Remove":         {fn: s.remove},
		"Task/Priority":       {admin: true, fn: s.priority},
		"Task/GetFile":        {fn: s.getFile},
		"Task/SetFile":        {fn: s.setFile},
		"Task/GetTorrentFile": {fn: s.getTorrentFile},
		"Config/Get":          {admin: true, fn: s.configGet},
		"Config/Set":          {admin: true, fn: s.configSet},
		"Account/Query":       {fn: s.accountQuery},
		"Account/Add":         {fn: s.accountAdd},
		"Account/Update":      {fn: s.accountUpdate},
		"Account/Remove":      {fn: s.accountRemove},
	}
	mux := srv.Mux()
	for _, base := range []string{"/downloadstation/", "/DownloadCenter/downloadstation/"} {
		mux.HandleFunc("GET "+base, s.serve)
		mux.HandleFunc("POST "+base, s.serve)
	}
	mux.HandleFunc("GET /downloadstation", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/DownloadCenter/", http.StatusFound)
	})
	s.registerTakeover()
}

func (s *service) serve(w http.ResponseWriter, r *http.Request) {
	mt := pathRe.FindStringSubmatch(r.URL.Path)
	if mt == nil {
		if r.URL.Path == "/downloadstation/" || r.URL.Path == "/DownloadCenter/downloadstation/" {
			http.Redirect(w, r, "/DownloadCenter/", http.StatusFound)
			return
		}
		http.NotFound(w, r)
		return
	}
	ns, action := mt[1], mt[2]
	ep, ok := s.eps[ns+"/"+action]
	// Usage statistics: the endpoint's name from the table (never the
	// requested one) and the error code of the answer
	switch {
	case ns == "Rss" || ns == "Addon":
		api.Count("v4_" + strings.ToLower(ns))
	case ok:
		api.Count("v4_" + strings.ToLower(ns) + "_" + strings.ToLower(action))
	default:
		api.Count("v4_unknown")
	}
	answer := func(res result) {
		if code, _ := res["error"].(int); code < 0 {
			api.Count("v4_err_neg" + strconv.Itoa(-code))
		} else if code > 0 {
			api.Count("v4_err_" + strconv.Itoa(code))
		}
		s.write(w, r, res)
	}
	c := &call{s: s, w: w, r: r}
	var err error
	c.p, c.files, err = parseRequest(r)
	if err != nil {
		answer(fail(errParameter))
		return
	}
	if ns == "Rss" || ns == "Addon" || !ok {
		answer(fail(errAPINotExists))
		return
	}
	if !ep.public {
		if code := c.authenticate(); code != errOK {
			answer(fail(code))
			return
		}
		// A session taken from a cookie (not the sid parameter official
		// clients send) is only good for state changes from the same
		// origin: otherwise any web page could drive the API (CSRF)
		if c.cookieSID && !readOnly[ns+"/"+action] && (r.Method != "POST" || !sameOrigin(r)) {
			answer(fail(errPermission))
			return
		}
		if ep.admin && !c.who.Admin {
			answer(fail(errPermission))
			return
		}
	}
	res := func() (res result) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("v4: panic in %s/%s: %v", ns, action, rec)
				res = fail(errException)
			}
		}()
		return ep.fn(c)
	}()
	if res == nil {
		return // the endpoint wrote a raw answer
	}
	if _, ok := res["error"]; !ok {
		res["error"] = errOK
	}
	answer(res)
}

// write answers JSON with the content type ds.cgi chooses from Accept.
func (s *service) write(w http.ResponseWriter, r *http.Request, res result) {
	acc := r.Header.Get("Accept")
	ct := "text/plain; charset=utf-8"
	if acc == "" || acc == "*/*" || strings.Contains(acc, "application/json") {
		ct = "application/json; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(200)
	json.NewEncoder(w).Encode(res)
}

// sidFrom returns the session id: sid parameter, QDS_SID cookie, NAS_SID cookie.
func (c *call) sidFrom() string {
	if v := c.p.get("sid"); v != "" {
		return v
	}
	for _, name := range []string{"QDS_SID", "NAS_SID"} {
		if ck, err := c.r.Cookie(name); err == nil && ck.Value != "" {
			c.cookieSID = true
			return ck.Value
		}
	}
	return ""
}

// authenticate resolves the session; returns a V4 error code.
func (c *call) authenticate() int {
	sid := c.sidFrom()
	if sid == "" {
		return errSessionTimeout
	}
	// Development only (dcd serve -dev-user): sid "dev" acts as that user
	if c.s.srv.DevUser != "" && sid == "dev" {
		p, err := c.s.au.FromQTS(c.s.srv.DevUser, qts.IsQTSAdmin(c.s.srv.DevUser))
		if err != nil {
			return errPrivilege
		}
		p.Via, p.SID = "v4", sid
		c.who, c.sid = p, sid
		return errOK
	}
	p, err := c.s.au.FromSID(sid, api.ClientIP(c.r), c.r.UserAgent())
	switch err {
	case nil:
	case auth.ErrNotOnList:
		return errPrivilege
	case auth.ErrAuthBackend:
		return errException
	default:
		return errSessionTimeout
	}
	p.Via = "v4"
	c.who, c.sid = p, sid
	return errOK
}

// owned loads a task the caller may act on (8193 / 8194).
func (c *call) owned(hash string) (*core.Task, int) {
	t := c.s.m.Live(hash)
	if t == nil {
		return nil, errTaskNotFound
	}
	if !c.who.SeesOwner(t.Owner) {
		return nil, errNotOwner
	}
	return t, errOK
}

// hashes expands the hash parameter ("all" = every task the caller sees).
func (c *call) hashes(onlyDone bool) ([]string, int) {
	hs := c.p.all("hash")
	var out []string
	if len(hs) == 1 && hs[0] == "all" {
		for _, t := range c.s.m.List() {
			if c.who.SeesOwner(t.Owner) && (!onlyDone || t.State == core.StDone) {
				out = append(out, t.Hash)
			}
		}
		return out, errOK
	}
	for _, h := range hs {
		if h == "" {
			continue
		}
		if _, code := c.owned(h); code != errOK {
			return nil, code
		}
		out = append(out, h)
	}
	if len(out) == 0 {
		return nil, errParamNotFound
	}
	return out, errOK
}

// readOnly endpoints may use a cookie session from any context.
var readOnly = map[string]bool{
	"Misc/Login": true, "Misc/Env": true, "Misc/Dir": true, "Task/Status": true, "Task/Query": true,
	"Task/Detail": true, "Task/GetFile": true, "Task/GetTorrentFile": true, "Config/Get": true, "Account/Query": true,
}

// sameOrigin reports a request sent by a page of this NAS.
func sameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin":
		return true
	case "cross-site", "same-site":
		return false
	}
	host := r.Host
	if xf := r.Header.Get("X-Forwarded-Host"); xf != "" {
		host = strings.TrimSpace(strings.Split(xf, ",")[0])
	}
	for _, h := range []string{r.Header.Get("Origin"), r.Header.Get("Referer")} {
		if h == "" {
			continue
		}
		u, err := url.Parse(h)
		return err == nil && u.Host == host
	}
	return false
}
