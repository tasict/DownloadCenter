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
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

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

// Request body limits. Before the caller is known only the first
// preAuthBody bytes are read (enough for Misc/Login and for the sid), by at
// most maxPreAuth requests at once; the rest only once the session is good,
// up to maxBody (maxTorrentBody for Task/AddTorrent, which uploads files).
const (
	preAuthBody    = 64 << 10
	maxBody        = 1 << 20
	maxTorrentBody = 16 << 20
	maxPreAuth     = 32
)

// Time a client has to send the start of a body, and the rest of it once
// known (variables for tests).
var (
	preAuthTime  = 10 * time.Second
	postAuthTime = 120 * time.Second
)

var (
	preAuthSlots = newSlots(maxPreAuth, 4)
	// Official clients send a torrent's sid after its file: such an upload
	// is read whole before the session is known, two at a time at most
	lateSIDSlots = newSlots(2, 1)
)

// slots bounds the requests not yet known that read a body: so many at
// once in all (waiting a while for a place), so many per client address
// (refused at once beyond that).
type slots struct {
	all   chan struct{}
	each  int
	mu    sync.Mutex
	taken map[string]int
}

func newSlots(all, each int) *slots {
	return &slots{all: make(chan struct{}, all), each: each, taken: map[string]int{}}
}

// take gets a place for a client address ("" = not known: counted in all
// only).
func (s *slots) take(src string) bool {
	if src != "" {
		s.mu.Lock()
		if s.taken[src] >= s.each {
			s.mu.Unlock()
			return false
		}
		s.taken[src]++
		s.mu.Unlock()
	}
	if acquire(s.all) {
		return true
	}
	s.release(src)
	return false
}

// give returns a place taken for src.
func (s *slots) give(src string) {
	<-s.all
	s.release(src)
}

func (s *slots) release(src string) {
	if src == "" {
		return
	}
	s.mu.Lock()
	if s.taken[src]--; s.taken[src] <= 0 {
		delete(s.taken, src)
	}
	s.mu.Unlock()
}

// sourceOf is the client address places are counted against: "" when it
// is not known, as when the proxy's client address cannot be believed and
// every request comes from loopback (then only the places in all count).
func sourceOf(r *http.Request) string {
	ip := net.ParseIP(api.ClientIP(r))
	if ip == nil || ip.IsLoopback() {
		return ""
	}
	return ip.String()
}

// readBody reads a POST body on from what was read already (have), up to
// limit bytes in all; complete reports that the end of the body was reached
// within the limit (otherwise one byte more than the limit was read).
func readBody(r *http.Request, have []byte, limit int) (body []byte, complete bool, err error) {
	if r.Method != "POST" || r.Body == nil {
		return have, true, nil
	}
	if len(have) > limit {
		return have, false, nil
	}
	rest, err := io.ReadAll(io.LimitReader(r.Body, int64(limit-len(have))+1))
	body = append(have, rest...)
	if err != nil {
		return body, false, err
	}
	return body, len(body) <= limit, nil
}

// parseRequest takes the parameters from the query and from a POST body
// (urlencoded or multipart). Of a body that is not complete, the pairs or
// parts that are cut off are left out, and files are not kept.
func parseRequest(r *http.Request, body []byte, complete bool) (params, []upFile) {
	p := params{}
	p.addQuery(r.URL.RawQuery)
	if r.Method != "POST" || len(body) == 0 {
		return p, nil
	}
	mt, mp, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if strings.HasPrefix(mt, "multipart/") {
		boundary := mp["boundary"]
		if boundary == "" {
			boundary = boundaryFromBody(body)
		}
		if boundary == "" {
			return p, nil
		}
		var files []upFile
		mr := multipart.NewReader(bytes.NewReader(body), boundary)
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			data, err := io.ReadAll(io.LimitReader(part, maxTorrentBody))
			if err == nil {
				if part.FileName() != "" {
					if complete {
						files = append(files, upFile{name: part.FileName(), data: data})
					}
				} else {
					p.add(part.FormName(), string(data))
				}
			}
			part.Close()
		}
		return p, files
	}
	s := string(body)
	if !complete {
		// The last pair may be cut off
		if i := strings.LastIndexByte(s, '&'); i >= 0 {
			s = s[:i]
		} else {
			s = ""
		}
	}
	p.addQuery(s)
	return p, nil
}

func isMultipart(r *http.Request) bool {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return strings.HasPrefix(mt, "multipart/")
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
		"Task/AddTracker":     {fn: s.addTracker}, // Download Center's own
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
	bodyRead := r.Method != "POST" || r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0
	answer := func(res result) {
		if code, _ := res["error"].(int); code < 0 {
			api.Count("v4_err_neg" + strconv.Itoa(-code))
		} else if code > 0 {
			api.Count("v4_err_" + strconv.Itoa(code))
		}
		if !bodyRead {
			// The rest of the body is not waited for: the connection ends
			w.Header().Set("Connection", "close")
		}
		s.write(w, r, res)
	}
	c := &call{s: s, w: w, r: r}
	if ns == "Rss" || ns == "Addon" || !ok {
		answer(fail(errAPINotExists))
		return
	}
	name := ns + "/" + action
	limit := maxBody
	if name == "Task/AddTorrent" {
		limit = maxTorrentBody
	}
	// Until the caller is known, only the start of the body is read, by a
	// bounded number of requests at once (in all and per client address)
	// and within a time limit (a client sending slowly keeps its place no
	// longer than that). A small body of known length needs no place.
	hasBody := r.Method == "POST" && r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0
	needSlot := hasBody && !(r.ContentLength > 0 && r.ContentLength <= preAuthBody)
	src := sourceOf(r)
	if needSlot && !preAuthSlots.take(src) {
		answer(fail(errException))
		return
	}
	rc := http.NewResponseController(w)
	rc.SetReadDeadline(time.Now().Add(preAuthTime))
	body, complete, err := readBody(r, nil, preAuthBody)
	c.p, c.files = parseRequest(r, body, complete)
	if err == nil && !complete && !ep.public && name == "Task/AddTorrent" && isMultipart(r) && c.p.get("sid") == "" && !hasSIDCookie(r) {
		// Official clients send a torrent's sid after its file
		if lateSIDSlots.take(src) {
			body, complete, err = readBody(r, body, limit)
			lateSIDSlots.give(src)
			c.p, c.files = parseRequest(r, body, complete)
		}
	}
	bodyRead = bodyRead || err == nil && complete
	code := errOK
	if err == nil && !ep.public {
		code = c.authenticate()
	}
	if needSlot {
		preAuthSlots.give(src)
	}
	rc.SetReadDeadline(time.Now().Add(postAuthTime))
	if err != nil || ep.public && !complete {
		answer(fail(errParameter))
		return
	}
	if !ep.public {
		if code != errOK {
			answer(fail(code))
			return
		}
		if !complete {
			if body, complete, err = readBody(r, body, limit); err != nil || !complete {
				answer(fail(errParameter))
				return
			}
			bodyRead = true
			c.p, c.files = parseRequest(r, body, true)
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

// acquire takes a place in a bounded set of requests, waiting a while.
func acquire(sem chan struct{}) bool {
	select {
	case sem <- struct{}{}:
		return true
	default:
	}
	t := time.NewTimer(10 * time.Second)
	defer t.Stop()
	select {
	case sem <- struct{}{}:
		return true
	case <-t.C:
		return false
	}
}

func hasSIDCookie(r *http.Request) bool {
	for _, name := range []string{"QDS_SID", "NAS_SID"} {
		if ck, err := r.Cookie(name); err == nil && ck.Value != "" {
			return true
		}
	}
	return false
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
	if xf := r.Header.Get("X-Forwarded-Host"); xf != "" && api.FromProxy(r) {
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
