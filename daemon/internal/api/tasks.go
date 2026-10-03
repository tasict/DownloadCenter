package api

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/netutil"
	"downloadcenter/internal/torrent"
)

// TaskJSON is the public shape of a task.
type TaskJSON struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Kind        string         `json:"kind"`  // url | torrent | magnet
	Proto       string         `json:"proto"` // http | ftp | bt
	Source      string         `json:"source"`
	Owner       string         `json:"owner"`
	State       string         `json:"state"`
	UserPaused  bool           `json:"user_paused"`
	SchedPaused bool           `json:"sched_paused"`
	WakeTime    int64          `json:"wake_time"`
	Position    int            `json:"position"`
	Size        int64          `json:"size"`
	Done        int64          `json:"done"`
	Progress    float64        `json:"progress"`
	DownRate    int64          `json:"down_rate"`
	UpRate      int64          `json:"up_rate"`
	ETA         int64          `json:"eta"`
	Peers       int            `json:"peers"`
	Seeds       int            `json:"seeds"`
	Ratio       float64        `json:"ratio"`
	UpTotal     int64          `json:"up_total"`
	ActiveSecs  int64          `json:"active_secs"`
	CreatedAt   int64          `json:"created_at"`
	StartedAt   int64          `json:"started_at"`
	FinishedAt  int64          `json:"finished_at"`
	Error       map[string]any `json:"error"`
	Folder      string         `json:"folder"`
	MoveTo      string         `json:"move_to"`
	Location    string         `json:"location"`
	InTemp      bool           `json:"in_temp,omitempty"` // location is the task's temporary folder (not complete yet)
	AutoRemove  string         `json:"auto_remove"`
	FilesTotal  int            `json:"files_total"`
	FilesChosen int            `json:"files_chosen"`
	IsFolder    bool           `json:"is_folder"`
	Comment     string         `json:"comment"`
	Engine      string         `json:"engine"`
	Hoster      string         `json:"hoster,omitempty"`
	Sequential  bool           `json:"sequential"`
	Imported    bool           `json:"imported,omitempty"`
	Caller      string         `json:"caller"`
	Proxy       string         `json:"proxy,omitempty"`      // URL tasks: auto, none or a profile id
	ProxyName   string         `json:"proxy_name,omitempty"` // detail: the profile in use ("" = direct)
	ProxyError  string         `json:"proxy_error,omitempty"`
	RemovedAt   int64          `json:"removed_at,omitempty"`
	Bitfield    string         `json:"bitfield,omitempty"`
	Pieces      int            `json:"pieces,omitempty"`
}

func (s *Server) taskJSON(p *auth.Principal, t *core.Task, withBits bool) TaskJSON {
	j := TaskJSON{
		ID: t.Hash, Name: t.Name, Proto: t.Kind, Source: t.Source, Owner: t.Owner, State: t.State,
		UserPaused: t.UserPaused, SchedPaused: t.SchedPaused, WakeTime: t.WakeTime, Position: t.Position,
		Size: t.Size, Done: t.DoneBytes, Progress: round1(t.Progress()), DownRate: t.DownRate, UpRate: t.UpRate,
		ETA: t.ETA(), Peers: t.Peers, Seeds: t.Seeds, Ratio: round2(t.Ratio()), UpTotal: t.UpTotal,
		ActiveSecs: t.ActiveSecs, CreatedAt: t.CreatedAt, StartedAt: t.StartedAt, FinishedAt: t.FinishedAt,
		AutoRemove: t.AutoRemove, FilesTotal: t.FilesTotal, FilesChosen: t.FilesChosen, IsFolder: t.IsFolder,
		Comment: t.Comment, Engine: t.Engine, Hoster: t.Options.Hoster, Sequential: t.Options.Sequential,
		Imported: t.Imported, Caller: t.Caller, RemovedAt: t.RemovedAt,
	}
	switch {
	case t.Kind != core.KindBT:
		j.Kind = "url"
	case t.IsMagnet():
		j.Kind = "magnet"
	default:
		j.Kind = "torrent"
	}
	if t.Options.Hoster != "" && t.Options.OrigURL != "" {
		j.Source = t.Options.OrigURL
	}
	viewer := p.User
	j.Folder = s.M.DisplayPath(viewer, t.TempDir)
	j.MoveTo = s.M.DisplayPath(viewer, t.MoveDir)
	switch {
	case t.DataPath != "":
		j.Location = s.M.DisplayPath(viewer, t.DataPath)
	case t.Kind == core.KindBT && t.Name != "":
		j.Location = s.M.DisplayPath(viewer, t.SaveDir()+"/"+t.Name)
	default:
		j.Location = s.M.DisplayPath(viewer, t.WorkDir)
	}
	j.InTemp = t.InTemp()
	if t.State == core.StError {
		j.Error = map[string]any{"code": t.ErrorCode, "message": t.ErrorMsg}
	}
	if t.Kind != core.KindBT {
		j.Proxy = t.Options.Proxy
		if j.Proxy == "" {
			j.Proxy = "auto"
		}
		if withBits {
			j.ProxyName, j.ProxyError = s.M.TaskProxyName(t)
		}
	}
	if withBits {
		j.Bitfield, j.Pieces = t.Bitfield, t.NumPieces
	}
	return j
}

func round1(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }
func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

// visibleTask returns a live task the caller may see.
func (s *Server) visibleTask(w http.ResponseWriter, p *auth.Principal, id string) *core.Task {
	t := s.M.Live(id)
	if t == nil || !p.SeesOwner(t.Owner) {
		Error(w, 404, "not_found", "找不到這個任務")
		return nil
	}
	return t
}

// stateGroup maps a state to the UI filter groups.
func stateGroup(t *core.Task) string {
	switch t.State {
	case core.StDownloading, core.StMetadata:
		return "downloading"
	case core.StQueued:
		return "waiting"
	case core.StChecking, core.StMoving:
		return "downloading"
	}
	return t.State
}

var stateOrder = map[string]int{core.StError: 0, core.StDownloading: 1, core.StMetadata: 1, core.StChecking: 2, core.StMoving: 3,
	core.StSeeding: 4, core.StQueued: 5, core.StPaused: 6, core.StDone: 7}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	q := r.URL.Query()
	state, kind, text := q.Get("state"), q.Get("kind"), strings.ToLower(q.Get("q"))
	all := s.M.List()
	counts := map[string]int{"all": 0, "downloading": 0, "waiting": 0, "paused": 0, "seeding": 0, "done": 0, "error": 0}
	var downRate, upRate int64
	var out []*core.Task
	for _, t := range all {
		if !p.SeesOwner(t.Owner) {
			continue
		}
		if q.Get("owner") != "" && t.Owner != q.Get("owner") {
			continue
		}
		g := stateGroup(t)
		counts["all"]++
		counts[g]++
		downRate += t.DownRate
		upRate += t.UpRate
		if state != "" && state != "all" && g != state && t.State != state {
			continue
		}
		if kind != "" && kind != "all" {
			k := s.taskJSON(p, t, false).Kind
			if kind != k && kind != t.Kind {
				continue
			}
		}
		if text != "" && !strings.Contains(strings.ToLower(t.Name+" "+t.Source), text) {
			continue
		}
		out = append(out, t)
	}
	sortTasks(out, q.Get("sort"), q.Get("order"))
	// Cursor pagination: after = id of the last task of the previous page
	if after := q.Get("after"); after != "" {
		for i, t := range out {
			if t.Hash == after {
				out = out[i+1:]
				break
			}
		}
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[len(out)-1].Hash
	}
	res := make([]TaskJSON, 0, len(out))
	for _, t := range out {
		res = append(res, s.taskJSON(p, t, false))
	}
	OK(w, map[string]any{"tasks": res, "next": next, "counts": counts, "down_rate": downRate, "up_rate": upRate})
}

func sortTasks(ts []*core.Task, by, order string) {
	desc := order == "desc"
	var less func(a, b *core.Task) bool
	switch by {
	case "status":
		less = func(a, b *core.Task) bool { return stateOrder[a.State] < stateOrder[b.State] }
	case "progress":
		less = func(a, b *core.Task) bool { return a.Progress() < b.Progress() }
	case "eta":
		less = func(a, b *core.Task) bool {
			ea, eb := a.ETA(), b.ETA()
			if ea < 0 {
				return false
			}
			if eb < 0 {
				return true
			}
			return ea < eb
		}
	case "elapsed":
		less = func(a, b *core.Task) bool { return a.ActiveSecs < b.ActiveSecs }
	case "name":
		less = func(a, b *core.Task) bool { return strings.ToLower(a.Name) < strings.ToLower(b.Name) }
	case "size":
		less = func(a, b *core.Task) bool { return a.Size < b.Size }
	case "created":
		less = func(a, b *core.Task) bool { return a.CreatedAt < b.CreatedAt }
	default:
		less = func(a, b *core.Task) bool { return a.Position < b.Position }
	}
	sort.SliceStable(ts, func(i, j int) bool {
		if desc {
			return less(ts[j], ts[i])
		}
		return less(ts[i], ts[j])
	})
}

// addBody is the JSON body of POST /tasks.
type addBody struct {
	Source     string   `json:"source"`
	Sources    []string `json:"sources"`
	Folder     *string  `json:"folder"`
	MoveTo     *string  `json:"move_to"`
	Files      any      `json:"files"` // "all" or [indices]
	AutoRemove *string  `json:"auto_remove"`
	Start      *bool    `json:"start"`
	Account    *struct {
		Mode string `json:"mode"`
		ID   string `json:"id"`
		User string `json:"user"`
		Pass string `json:"pass"`
	} `json:"account"`
	ContentOf string   `json:"content_of"`
	Headers   []string `json:"headers"`
	Name      string   `json:"name"`
	Proxy     string   `json:"proxy"` // URL tasks: "" or "auto", "none" or a proxy profile id
}

func (b *addBody) options(p *auth.Principal) core.AddOptions {
	o := core.AddOptions{Owner: p.User, Admin: p.Admin, AutoRemove: "default", Caller: callerOf(p), ContentOf: b.ContentOf, Proxy: b.Proxy}
	if b.Folder != nil {
		o.TempDir = *b.Folder
	}
	if b.MoveTo != nil {
		o.MoveDir, o.MoveSet = *b.MoveTo, true
	}
	if b.AutoRemove != nil {
		o.AutoRemove = *b.AutoRemove
	}
	if b.Start != nil && !*b.Start {
		o.Paused = true
	}
	if b.Account != nil {
		o.AccountMode, o.AccountID, o.ManualUser, o.ManualPass = b.Account.Mode, b.Account.ID, b.Account.User, b.Account.Pass
	}
	if arr, ok := b.Files.([]any); ok {
		o.Select = []int{}
		for _, v := range arr {
			if f, ok := v.(float64); ok {
				o.Select = append(o.Select, int(f))
			}
		}
	}
	if p.Admin && len(b.Headers) > 0 {
		o.Headers = b.Headers
	}
	o.OutName = sanitizeName(b.Name)
	return o
}

func sanitizeName(n string) string {
	n = strings.TrimSpace(strings.ReplaceAll(n, "/", "_"))
	if n == "." || n == ".." {
		return ""
	}
	return n
}

func callerOf(p *auth.Principal) string {
	switch p.Via {
	case "token":
		return "API: " + p.Token.Name
	case "chat":
		return "Chat"
	}
	return "Download Center"
}

// checkFolders applies a token's folder allowlist.
func (s *Server) checkFolders(p *auth.Principal, o *core.AddOptions) bool {
	if len(p.Folders) == 0 {
		return true
	}
	ok := func(d string) bool {
		if d == "" {
			return true
		}
		real, err := s.M.ResolvePath(p.User, d)
		if err != nil {
			return false
		}
		for _, f := range p.Folders {
			if fr, err := s.M.ResolvePath(p.User, f); err == nil && (real == fr || strings.HasPrefix(real, fr+"/")) {
				return true
			}
		}
		return false
	}
	t := o.TempDir
	if t == "" {
		t = s.M.Settings().TempDir
	}
	return ok(t) && ok(o.MoveDir)
}

type addOut struct {
	Source string `json:"source"`
	*core.AddResult
	Error map[string]string `json:"error,omitempty"`
}

func (s *Server) addOne(p *auth.Principal, src string, o core.AddOptions) addOut {
	src = core.UnwrapLink(src)
	out := addOut{Source: src}
	var res *core.AddResult
	var err error
	low := strings.ToLower(src)
	switch {
	case strings.HasPrefix(low, "magnet:"):
		if !p.SourceAllowed("magnet") {
			out.Error = map[string]string{"code": "source_not_allowed", "message": "The token may not add magnet links"}
			return out
		}
		res, err = s.M.AddMagnet(src, o)
	default:
		if !p.SourceAllowed("url") {
			out.Error = map[string]string{"code": "source_not_allowed", "message": "The token may not add URLs"}
			return out
		}
		if b := s.fetchTorrentURL(p, src, o.Proxy); b != nil {
			res, err = s.M.AddTorrent(b, o)
		} else {
			res, err = s.M.AddURL(src, o)
		}
	}
	out.AddResult = res
	if err != nil {
		code := "failed"
		switch {
		case err == core.ErrFolder:
			code = "folder_not_allowed"
		case err == core.ErrOtherOwner:
			code = "duplicate_other_owner"
		case strings.Contains(err.Error(), "duplicate"):
			code = "duplicate"
		case err == core.ErrBadURL:
			code = "url_not_supported"
		case err == core.ErrBadMagnet:
			code = "magnet_invalid"
		}
		msg := err.Error()
		if code == "duplicate_other_owner" {
			msg = "其他使用者已經在下載這個種子"
		} else if code == "duplicate" {
			msg = "這個任務已在清單中"
		} else if code == "folder_not_allowed" {
			msg = "不能使用這個資料夾"
		}
		out.Error = map[string]string{"code": code, "message": msg}
	}
	return out
}

func (s *Server) addTasks(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	var b addBody
	if err := Decode(r, &b); err != nil {
		Error(w, 400, "bad_request", err.Error())
		return
	}
	srcs := b.Sources
	if b.Source != "" {
		srcs = append([]string{b.Source}, srcs...)
	}
	if len(srcs) == 0 {
		Error(w, 400, "bad_request", "沒有要加入的連結")
		return
	}
	if len(srcs) > 500 {
		Error(w, 400, "bad_request", "一次最多 500 個連結")
		return
	}
	o := b.options(p)
	if !s.checkFolders(p, &o) {
		Error(w, 403, "folder_not_allowed", "The token may not use this folder")
		return
	}
	var results []addOut
	for _, src := range srcs {
		results = append(results, s.addOne(p, src, o))
	}
	trResults(w, results)
	if len(results) == 1 {
		r0 := results[0]
		if r0.Error != nil {
			status := 400
			switch r0.Error["code"] {
			case "duplicate", "duplicate_other_owner":
				status = 409
			case "folder_not_allowed", "source_not_allowed":
				status = 403
			}
			body := map[string]any{"error": r0.Error}
			if r0.AddResult != nil {
				body["id"] = r0.ID
			}
			JSON(w, status, body)
			return
		}
		OK(w, map[string]any{"id": r0.ID, "name": r0.Name, "merged": r0.Merged, "results": results})
		return
	}
	OK(w, map[string]any{"results": results})
}

func (s *Server) addTorrentUpload(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	if !p.SourceAllowed("torrent") {
		Error(w, 403, "source_not_allowed", "The token may not add torrent files")
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		Error(w, 400, "bad_request", "上傳的格式不正確")
		return
	}
	defer r.MultipartForm.RemoveAll()
	b := addBody{}
	f := r.MultipartForm.Value
	if v, ok := f["folder"]; ok {
		b.Folder = &v[0]
	}
	if v, ok := f["move_to"]; ok {
		b.MoveTo = &v[0]
	}
	if v, ok := f["auto_remove"]; ok {
		b.AutoRemove = &v[0]
	}
	if v, ok := f["start"]; ok {
		st := v[0] != "false" && v[0] != "0"
		b.Start = &st
	}
	if v, ok := f["files"]; ok && v[0] != "" && v[0] != "all" {
		var arr []any
		for _, x := range strings.Split(v[0], ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
				arr = append(arr, float64(n))
			}
		}
		b.Files = arr
	}
	if v, ok := f["content_of"]; ok {
		b.ContentOf = v[0]
	}
	o := b.options(p)
	if !s.checkFolders(p, &o) {
		Error(w, 403, "folder_not_allowed", "The token may not use this folder")
		return
	}
	files := r.MultipartForm.File["file"]
	files = append(files, r.MultipartForm.File["file[]"]...)
	if len(files) == 0 {
		Error(w, 400, "bad_request", "沒有收到 .torrent 檔")
		return
	}
	var results []addOut
	for _, fh := range files {
		fp, err := fh.Open()
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(fp, 16<<20))
		fp.Close()
		out := addOut{Source: fh.Filename}
		res, err := s.M.AddTorrent(data, o)
		out.AddResult = res
		if err != nil {
			code := "failed"
			if err == core.ErrBadTorrent {
				code = "torrent_invalid"
			} else if err == core.ErrFolder {
				code = "folder_not_allowed"
			}
			out.Error = map[string]string{"code": code, "message": errMsg(err)}
		}
		results = append(results, out)
	}
	trResults(w, results)
	if len(results) == 1 && results[0].Error != nil {
		status := 400
		if results[0].Error["code"] == "folder_not_allowed" {
			status = 403
		}
		JSON(w, status, map[string]any{"error": results[0].Error})
		return
	}
	resp := map[string]any{"results": results}
	if len(results) == 1 {
		resp["id"], resp["name"], resp["merged"] = results[0].ID, results[0].Name, results[0].Merged
	}
	OK(w, resp)
}

func errMsg(err error) string {
	switch err {
	case core.ErrBadTorrent:
		return "種子檔格式不正確"
	case core.ErrFolder:
		return "不能使用這個資料夾"
	case core.ErrBadMagnet:
		return "磁力連結格式不正確"
	}
	return err.Error()
}

func (s *Server) checkTasks(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	var items []core.CheckItem
	folder := ""
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			Error(w, 400, "bad_request", "上傳的格式不正確")
			return
		}
		defer r.MultipartForm.RemoveAll()
		for _, fh := range append(r.MultipartForm.File["file"], r.MultipartForm.File["file[]"]...) {
			fp, err := fh.Open()
			if err != nil {
				continue
			}
			data, _ := io.ReadAll(io.LimitReader(fp, 16<<20))
			fp.Close()
			items = append(items, core.CheckItem{Source: fh.Filename, Torrent: data})
		}
		if v := r.MultipartForm.Value["folder"]; len(v) > 0 {
			folder = v[0]
		}
	} else {
		var b struct {
			Sources []string `json:"sources"`
			Folder  string   `json:"folder"`
		}
		if err := Decode(r, &b); err != nil {
			Error(w, 400, "bad_request", err.Error())
			return
		}
		for _, src := range b.Sources {
			items = append(items, core.CheckItem{Source: strings.TrimSpace(src)})
		}
		folder = b.Folder
	}
	dest := ""
	if p.Admin {
		if folder != "" {
			dest, _ = s.M.ResolvePath(p.User, folder)
		} else {
			dest = s.M.Settings().TempDir
		}
	} else {
		dest = s.M.UserDownloadDir(p.User)
	}
	OK(w, map[string]any{"items": s.M.Check(p.User, p.Admin, items, dest)})
}

// probe returns the file list of a magnet (async) or an uploaded .torrent.
func (s *Server) probe(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			Error(w, 400, "bad_request", "上傳的格式不正確")
			return
		}
		defer r.MultipartForm.RemoveAll()
		fhs := append(r.MultipartForm.File["file"], r.MultipartForm.File["file[]"]...)
		if len(fhs) == 0 {
			Error(w, 400, "bad_request", "沒有收到 .torrent 檔")
			return
		}
		fp, err := fhs[0].Open()
		if err != nil {
			Error(w, 400, "bad_request", err.Error())
			return
		}
		data, _ := io.ReadAll(io.LimitReader(fp, 16<<20))
		fp.Close()
		meta, err := torrent.Parse(data)
		if err != nil {
			Error(w, 400, "torrent_invalid", "種子檔格式不正確")
			return
		}
		OK(w, map[string]any{"state": "ready", "infohash": meta.InfoHash, "torrent": metaJSON(meta), "free": s.freeFor(p, r.MultipartForm.Value["folder"])})
		return
	}
	var b struct {
		Magnet string `json:"magnet"`
		Cancel bool   `json:"cancel"`
		Folder string `json:"folder"`
	}
	if err := Decode(r, &b); err != nil {
		Error(w, 400, "bad_request", err.Error())
		return
	}
	if b.Cancel {
		if mg, err := torrent.ParseMagnet(b.Magnet); err == nil {
			s.M.CancelProbe(mg.InfoHash)
		}
		OK(w, map[string]any{"ok": true})
		return
	}
	meta, hash, err := s.M.ProbeMagnet(b.Magnet)
	if err != nil {
		Fail(w, err)
		return
	}
	if meta == nil {
		OK(w, map[string]any{"state": "pending", "infohash": hash})
		return
	}
	OK(w, map[string]any{"state": "ready", "infohash": hash, "torrent": metaJSON(meta), "free": s.freeFor(p, []string{b.Folder})})
}

func (s *Server) freeFor(p *auth.Principal, folder []string) int64 {
	dir := s.M.Settings().TempDir
	if !p.Admin {
		dir = s.M.UserDownloadDir(p.User)
	} else if len(folder) > 0 && folder[0] != "" {
		if d, err := s.M.ResolvePath(p.User, folder[0]); err == nil {
			dir = d
		}
	}
	return core.FreeSpace(dir)
}

func metaJSON(m *torrent.Meta) map[string]any {
	files := make([]map[string]any, 0, len(m.Files))
	for _, f := range m.Files {
		if f.Pad {
			continue
		}
		files = append(files, map[string]any{"index": f.Index, "path": f.Path, "size": f.Size})
	}
	return map[string]any{"name": m.Name, "size": m.Size, "files": files, "trackers": len(m.Trackers), "comment": m.Comment,
		"private": m.Private, "is_folder": m.IsFolder, "infohash": m.InfoHash}
}

func (s *Server) taskRoutes() {
	s.Route("GET /tasks", "tasks:read", 0, s.listTasks)
	s.Route("POST /tasks", "tasks:add", 0, s.addTasks)
	s.Route("POST /tasks/torrent", "tasks:add", 0, s.addTorrentUpload)
	s.Route("POST /tasks/check", "tasks:add", 0, s.checkTasks)
	s.Route("POST /tasks/probe", "tasks:add", 0, s.probe)
	s.Route("POST /tasks/extract", "tasks:add", 0, s.extract)
	s.Route("POST /tasks/bulk", "", 0, s.bulk)
	s.Route("GET /tasks/{id}", "tasks:read", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		t := s.visibleTask(w, p, r.PathValue("id"))
		if t == nil {
			return
		}
		j := s.taskJSON(p, t, true)
		OK(w, map[string]any{"task": j, "sources": s.M.Sources(t.Hash), "log": s.M.TaskLog(t.Hash, 50),
			"trackers": t.Options.Trackers})
	})
	s.Route("GET /tasks/{id}/files", "tasks:read", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		t := s.visibleTask(w, p, r.PathValue("id"))
		if t == nil {
			return
		}
		files := s.M.Files(t.Hash)
		if files == nil {
			files = []core.FileRow{}
		}
		OK(w, map[string]any{"files": files})
	})
	s.Route("GET /tasks/{id}/peers", "tasks:read", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		t := s.visibleTask(w, p, r.PathValue("id"))
		if t == nil {
			return
		}
		peers, _ := s.M.Peers(t.Hash)
		// The task's own rates come with the peers so the detail chart samples both at once
		OK(w, map[string]any{"peers": peers, "down_rate": t.DownRate, "up_rate": t.UpRate})
	})
	s.Route("GET /tasks/{id}/folder", "tasks:read", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		t := s.visibleTask(w, p, r.PathValue("id"))
		if t == nil {
			return
		}
		dir, file := core.FolderOf(t)
		OK(w, map[string]any{"path": s.M.DisplayPath(p.User, dir), "file": file})
	})
	s.Route("GET /tasks/{id}/log", "tasks:read", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		t := s.visibleTask(w, p, r.PathValue("id"))
		if t == nil {
			return
		}
		OK(w, map[string]any{"log": s.M.TaskLog(t.Hash, 200)})
	})
	s.Route("GET /tasks/{id}/torrent", "tasks:read", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		t := s.visibleTask(w, p, r.PathValue("id"))
		if t == nil {
			return
		}
		data, err := os.ReadFile(s.M.TorrentDir() + "/" + t.Hash + ".torrent")
		if err != nil {
			Error(w, 404, "not_found", "這個任務沒有 .torrent 檔")
			return
		}
		w.Header().Set("Content-Type", "application/x-bittorrent")
		w.Header().Set("Content-Disposition", contentDisposition("attachment", t.Name+".torrent"))
		w.Write(data)
	})
	act := func(name string, fn func(t *core.Task, r *http.Request) error) Handler {
		return func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			t := s.visibleTask(w, p, r.PathValue("id"))
			if t == nil {
				return
			}
			if err := fn(t, r); err != nil {
				Fail(w, err)
				return
			}
			nt := s.M.Live(t.Hash)
			if nt == nil {
				OK(w, map[string]any{"ok": true})
				return
			}
			OK(w, map[string]any{"ok": true, "task": s.taskJSON(p, nt, false)})
		}
	}
	s.Route("POST /tasks/{id}/pause", "tasks:control", 0, act("pause", func(t *core.Task, r *http.Request) error {
		var b struct {
			Minutes int `json:"minutes"`
		}
		Decode(r, &b)
		return s.M.Pause(t.Hash, b.Minutes, "user")
	}))
	s.Route("POST /tasks/{id}/resume", "tasks:control", 0, act("resume", func(t *core.Task, r *http.Request) error {
		return s.M.Resume(t.Hash, "user")
	}))
	s.Route("POST /tasks/{id}/retry", "tasks:control", 0, act("retry", func(t *core.Task, r *http.Request) error {
		return s.M.Retry(t.Hash)
	}))
	s.Route("PATCH /tasks/{id}", "tasks:control", 0, s.patchTask)
	s.Route("DELETE /tasks/{id}", "tasks:remove", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		t := s.visibleTask(w, p, r.PathValue("id"))
		if t == nil {
			return
		}
		del := r.URL.Query().Get("delete_files") == "true" || r.URL.Query().Get("delete_files") == "1"
		if del && !p.Can("files:delete") {
			Error(w, 403, "insufficient_scope", "Deleting data needs the files:delete scope")
			return
		}
		if err := s.M.Remove(t.Hash, del, false); err != nil {
			Fail(w, err)
			return
		}
		OK(w, map[string]any{"ok": true})
	})
	s.Route("POST /tasks/{id}/undo", "tasks:remove", Session, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		id := r.PathValue("id")
		hist := s.M.History(p.User, 50)
		if p.Admin {
			hist = s.M.History("", 200)
		}
		for _, t := range hist {
			if t.Hash == id {
				if err := s.M.Undo(id); err != nil {
					Fail(w, err)
					return
				}
				OK(w, map[string]any{"ok": true})
				return
			}
		}
		Error(w, 404, "not_found", "已無法復原")
	})
	s.Route("GET /history", "tasks:read", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		owner := p.User
		if p.Admin && p.AllTasks {
			owner = ""
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		var out []TaskJSON
		for _, t := range s.M.History(owner, limit) {
			out = append(out, s.taskJSON(p, t, false))
		}
		if out == nil {
			out = []TaskJSON{}
		}
		OK(w, map[string]any{"tasks": out})
	})
}

func (s *Server) patchTask(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	t := s.visibleTask(w, p, r.PathValue("id"))
	if t == nil {
		return
	}
	var b struct {
		Position   any     `json:"position"`
		Files      any     `json:"files"`
		MaxDown    *int64  `json:"max_download"`
		MaxUp      *int64  `json:"max_upload"`
		Sequential *bool   `json:"sequential"`
		AutoRemove *string `json:"auto_remove"`
		Proxy      *string `json:"proxy"` // URL tasks: auto, none or a profile id
	}
	if err := Decode(r, &b); err != nil {
		Error(w, 400, "bad_request", err.Error())
		return
	}
	if b.Position != nil {
		where := ""
		switch v := b.Position.(type) {
		case string:
			where = v
		case float64:
			where = strconv.Itoa(int(v))
		}
		if err := s.M.Move(t.Hash, where, func(o *core.Task) bool { return p.SeesOwner(o.Owner) }); err != nil {
			Fail(w, err)
			return
		}
	}
	if b.Files != nil {
		prio := map[int]int{}
		switch v := b.Files.(type) {
		case []any:
			for _, f := range s.M.Files(t.Hash) {
				prio[f.Index] = 0
			}
			for _, x := range v {
				if n, ok := x.(float64); ok {
					prio[int(n)] = 1
				}
			}
		case map[string]any:
			for k, x := range v {
				if n, err := strconv.Atoi(k); err == nil {
					if f, ok := x.(float64); ok {
						prio[n] = int(f)
					}
				}
			}
		}
		if err := s.M.SetFiles(t.Hash, prio); err != nil {
			Fail(w, err)
			return
		}
	}
	if b.MaxDown != nil || b.MaxUp != nil {
		d, u := t.Options.MaxDown, t.Options.MaxUp
		if b.MaxDown != nil {
			d = *b.MaxDown
		}
		if b.MaxUp != nil {
			u = *b.MaxUp
		}
		s.M.SetTaskLimits(t.Hash, d, u)
	}
	if b.Sequential != nil {
		if err := s.M.SetSequential(t.Hash, *b.Sequential); err != nil {
			Fail(w, err)
			return
		}
	}
	if b.AutoRemove != nil {
		if err := s.M.SetAutoRemove(t.Hash, *b.AutoRemove); err != nil {
			Fail(w, err)
			return
		}
	}
	if b.Proxy != nil {
		if err := s.M.SetProxy(t.Hash, *b.Proxy); err != nil {
			Fail(w, err)
			return
		}
	}
	nt := s.M.Live(t.Hash)
	if nt == nil {
		OK(w, map[string]any{"ok": true})
		return
	}
	OK(w, map[string]any{"ok": true, "task": s.taskJSON(p, nt, false)})
}

// bulk applies an action to several tasks: {"ids": [...] | "all", "action":
// "pause"|"resume"|"retry"|"remove"|"top"|"up"|"down", "delete_files", "minutes", "completed"}.
func (s *Server) bulk(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	var b struct {
		IDs         any    `json:"ids"`
		Action      string `json:"action"`
		DeleteFiles bool   `json:"delete_files"`
		Minutes     int    `json:"minutes"`
		Completed   bool   `json:"completed"`
	}
	if err := Decode(r, &b); err != nil {
		Error(w, 400, "bad_request", err.Error())
		return
	}
	var ids []string
	if v, ok := b.IDs.(string); ok && v == "all" {
		for _, t := range s.M.List() {
			if p.SeesOwner(t.Owner) && (!b.Completed || t.State == core.StDone) {
				ids = append(ids, t.Hash)
			}
		}
	} else if arr, ok := b.IDs.([]any); ok {
		for _, x := range arr {
			if id, ok := x.(string); ok {
				if t := s.M.Live(id); t != nil && p.SeesOwner(t.Owner) {
					ids = append(ids, id)
				}
			}
		}
	}
	need := "tasks:control"
	if b.Action == "remove" {
		need = "tasks:remove"
	}
	if !p.Can(need) {
		Error(w, 403, "insufficient_scope", "The token lacks the scope "+need)
		return
	}
	if b.Action == "remove" && b.DeleteFiles && !p.Can("files:delete") {
		Error(w, 403, "insufficient_scope", "The token lacks the scope files:delete")
		return
	}
	n := 0
	// Moving several tasks keeps their relative order
	if b.Action == "top" || b.Action == "down" || b.Action == "bottom" {
		for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
			ids[i], ids[j] = ids[j], ids[i]
		}
	}
	for _, id := range ids {
		var err error
		switch b.Action {
		case "pause":
			err = s.M.Pause(id, b.Minutes, "user")
		case "resume", "start":
			err = s.M.Resume(id, "user")
		case "retry":
			err = s.M.Retry(id)
		case "remove":
			err = s.M.Remove(id, b.DeleteFiles, false)
		case "top", "up", "down", "bottom":
			err = s.M.Move(id, b.Action, func(o *core.Task) bool { return p.SeesOwner(o.Owner) })
		default:
			Error(w, 400, "bad_request", "unknown action")
			return
		}
		if err == nil {
			n++
		}
	}
	OK(w, map[string]any{"ok": true, "count": n})
}

// fetchTorrentURL downloads a .torrent link so it is added as a torrent
// task instead of a plain file. It returns nil for anything else.
func (s *Server) fetchTorrentURL(p *auth.Principal, src, proxyChoice string) []byte {
	u, err := url.Parse(src)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !strings.HasSuffix(strings.ToLower(u.Path), ".torrent") {
		return nil
	}
	pr, err := s.M.ProxyFor(proxyChoice, src, p.Admin)
	if err != nil {
		return nil
	}
	proxy := ""
	if pr != nil {
		proxy = s.M.ProxyURL(pr.ID)
	}
	resp, err := netutil.Client(30*time.Second, !p.Admin, proxy).Get(src)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil
	}
	if _, err := torrent.Parse(b); err != nil {
		return nil
	}
	return b
}

// trResults translates the error messages of add results.
func trResults(w http.ResponseWriter, results []addOut) {
	for i := range results {
		if results[i].Error != nil {
			results[i].Error["message"] = Tr(w, results[i].Error["message"])
		}
	}
}
