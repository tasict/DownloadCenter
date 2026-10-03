package v4

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/qts"
	"downloadcenter/internal/torrent"
)

// stateCode maps a package state to the V4 integers (§5.2).
func stateCode(t *core.Task) int {
	switch t.State {
	case core.StQueued:
		return 0
	case core.StPaused:
		return 1
	case core.StMoving:
		return 3
	case core.StError:
		return 4
	case core.StDone:
		return 5
	case core.StSeeding:
		return 100
	case core.StChecking:
		return 102
	case core.StMetadata:
		return 103
	case core.StDownloading:
		return 104
	}
	return 0
}

func typeString(kind string) string {
	switch kind {
	case core.KindBT:
		return "BT"
	case core.KindFTP:
		return "FTP"
	case core.KindHTTP:
		return "HTTP"
	}
	return "Unknown"
}

// errCode maps a task's error to the V4 error table.
func errCode(t *core.Task) int {
	if t.State != core.StError {
		return 0
	}
	switch {
	case t.ErrorCode == "move":
		return errMoveFail
	case t.ErrorCode == "dl_9" || t.ErrorCode == "aria2_9":
		return errSpace
	case t.Kind == core.KindBT && t.IsMagnet():
		return errMagnetFail
	case t.Kind == core.KindBT && (t.ErrorCode == "dl_25" || t.ErrorCode == "aria2_25" || t.ErrorCode == "aria2_26"):
		return errTorrentFormat
	case t.Kind != core.KindBT:
		return errURLDownloadFail
	}
	return errException
}

func fmtTime(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04:05")
}

var uidCache sync.Map

func uidOf(user string) int {
	if u, ok := uidCache.Load(user); ok {
		return u.(int)
	}
	uid, _, _ := qts.Lookup(user)
	uidCache.Store(user, uid)
	return uid
}

// record converts a task to the V4 task record (§5.1).
func (c *call) record(t *core.Task) map[string]any {
	m := c.s.m
	viewer := c.who.User
	path := m.DisplayPath(viewer, t.TempDir)
	switch {
	case t.State == core.StDone && t.DataPath != "":
		path = m.DisplayPath(viewer, filepath.Dir(t.DataPath))
	case t.Kind == core.KindBT:
		path = m.DisplayPath(viewer, t.SaveDir())
	case t.State != core.StDone:
		path = m.DisplayPath(viewer, t.WorkDir)
	}
	move := m.DisplayPath(viewer, t.MoveDir)
	if move == "" {
		move = m.DisplayPath(viewer, t.TempDir)
	}
	source := t.Source
	if t.Options.Hoster != "" && t.Options.OrigURL != "" {
		source = t.Options.OrigURL
	}
	category := 0
	if t.Kind == core.KindBT {
		category = 1
	}
	eta := t.ETA()
	if eta < 0 {
		eta = 0
	}
	prio := t.Position
	if t.State == core.StDone || t.State == core.StSeeding {
		prio = 0
	}
	files := t.FilesTotal
	if files == 0 && t.Kind != core.KindBT {
		files = 1
	}
	chosen := t.FilesChosen
	if chosen == 0 {
		chosen = files
	}
	return map[string]any{
		"hash": t.Hash, "type": typeString(t.Kind), "category": category, "source": source,
		"source_name": t.Name, "path": path, "temp": m.DisplayPath(viewer, t.TempDir), "move": move,
		"is_folder": b2i(t.IsFolder), "size": t.Size, "done": t.DoneBytes, "down_size": t.DoneBytes,
		"up_size": t.UpTotal, "total_down": t.DownTotal, "total_up": t.UpTotal,
		"down_rate": t.DownRate, "up_rate": t.UpRate, "progress": t.Progress(), "share": t.Ratio(),
		"state": stateCode(t), "error": errCode(t), "eta": eta, "activity_time": t.ActiveSecs,
		"create_time": fmtTime(t.CreatedAt), "start_time": fmtTime(t.StartedAt), "finish_time": fmtTime(t.FinishedAt),
		"wakeup_time": fmtTime(t.WakeTime), "priority": prio, "peers": t.Peers, "seeds": t.Seeds,
		"total_files": files, "choose_files": chosen, "comment": t.Comment,
		"uid": uidOf(t.Owner), "username": t.Owner, "caller": t.Caller, "caller_meta": "",
	}
}

// statusOf returns the status-filter groups a task belongs to.
func statusMatch(t *core.Task, status string) bool {
	switch status {
	case "", "all":
		return true
	case "downloading":
		switch t.State {
		case core.StQueued, core.StDownloading, core.StMetadata, core.StChecking, core.StMoving:
			return true
		}
	case "waiting":
		return t.State == core.StQueued
	case "seeding":
		return t.State == core.StSeeding
	case "paused":
		return t.State == core.StPaused
	case "stopped":
		return t.State == core.StError
	case "completed":
		return t.State == core.StDone
	case "active":
		return t.DownRate > 0 || t.UpRate > 0
	case "inactive":
		return t.DownRate == 0 && t.UpRate == 0
	}
	return false
}

func (c *call) visible() []*core.Task {
	var out []*core.Task
	for _, t := range c.s.m.List() {
		if c.who.SeesOwner(t.Owner) {
			out = append(out, t)
		}
	}
	return out
}

func counters(ts []*core.Task) map[string]any {
	st := map[string]any{}
	var down, up int64
	for _, k := range []string{"all", "downloading", "waiting", "seeding", "paused", "stopped", "completed", "active", "inactive"} {
		n := 0
		for _, t := range ts {
			if statusMatch(t, k) {
				n++
			}
		}
		st[k] = n
	}
	for _, t := range ts {
		down += t.DownRate
		up += t.UpRate
	}
	st["down_rate"], st["up_rate"] = down, up
	return st
}

// sortV4 sorts by any task record field (field/direction of Task/Query).
func sortV4(ts []*core.Task, field, direction string, rec func(*core.Task) map[string]any) {
	desc := strings.EqualFold(direction, "desc")
	key := func(t *core.Task) any {
		switch field {
		case "", "priority":
			return int64(t.Position)
		case "save_as":
			if t.Kind == core.KindBT {
				return strings.ToLower(t.SaveDir() + "/" + t.Name)
			}
			return strings.ToLower(t.TempDir + "/" + t.Name)
		case "eta":
			if e := t.ETA(); e >= 0 {
				return e
			}
			return int64(1 << 62)
		case "start_time":
			return t.StartedAt
		case "create_time":
			return t.CreatedAt
		case "finish_time":
			return t.FinishedAt
		case "wakeup_time":
			return t.WakeTime
		}
		v, ok := rec(t)[field]
		if !ok {
			return int64(t.Position)
		}
		return v
	}
	less := func(a, b any) bool {
		switch x := a.(type) {
		case string:
			y, _ := b.(string)
			return strings.ToLower(x) < strings.ToLower(y)
		default:
			return toFloat(a) < toFloat(b)
		}
	}
	sort.SliceStable(ts, func(i, j int) bool {
		a, b := key(ts[i]), key(ts[j])
		if desc {
			return less(b, a)
		}
		return less(a, b)
	})
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case float64:
		return x
	case bool:
		if x {
			return 1
		}
	}
	return 0
}

func (s *service) query(c *call) result {
	all := c.visible()
	typ := strings.ToLower(c.p.get("type"))
	status := strings.ToLower(c.p.get("status"))
	var list []*core.Task
	for _, t := range all {
		if typ != "" && typ != "all" && typ != t.Kind {
			continue
		}
		if !statusMatch(t, status) {
			continue
		}
		list = append(list, t)
	}
	sortV4(list, c.p.get("field"), c.p.get("direction"), c.record)
	total := len(list)
	from, _ := strconv.Atoi(c.p.get("from"))
	limit, err := strconv.Atoi(c.p.get("limit"))
	if err != nil || limit <= 0 {
		limit = 50
	}
	if from < 0 {
		from = 0
	}
	if from > len(list) {
		from = len(list)
	}
	end := from + limit
	if end > len(list) {
		end = len(list)
	}
	data := make([]map[string]any, 0, end-from)
	for _, t := range list[from:end] {
		data = append(data, c.record(t))
	}
	return result{"total": total, "data": data, "status": counters(all)}
}

func (s *service) status(c *call) result {
	st := counters(c.visible())
	res := result{"status": st}
	for k, v := range st {
		res[k] = v
	}
	return res
}

func (s *service) detail(c *call) result {
	var data []map[string]any
	for _, h := range c.p.all("hash") {
		t, code := c.owned(h)
		if code != errOK {
			return fail(code)
		}
		data = append(data, c.record(t))
	}
	if data == nil {
		return fail(errParamNotFound)
	}
	return result{"total": len(data), "data": data}
}

// addOptions builds core options from temp/move; regular users get their
// home Download folder whatever they send (the folder rule is enforced by
// the manager).
func (c *call) addOptions() (core.AddOptions, int) {
	o := core.AddOptions{Owner: c.who.User, Admin: c.who.Admin, AutoRemove: "default", Caller: c.p.get("caller")}
	if o.Caller == "" {
		o.Caller = "Download Station"
	}
	if len(o.Caller) > 32 {
		o.Caller = o.Caller[:32]
	}
	if !c.who.Admin {
		return o, errOK
	}
	if t := c.p.get("temp"); t != "" {
		if _, err := c.s.m.ResolvePath(c.who.User, t); err != nil {
			return o, errTempNotFolder
		}
		o.TempDir = t
	}
	if c.p.has("move") {
		mv := c.p.get("move")
		if mv != "" {
			if _, err := c.s.m.ResolvePath(c.who.User, mv); err != nil {
				return o, errMoveNotFolder
			}
		}
		o.MoveDir, o.MoveSet = mv, true
	}
	return o, errOK
}

func addError(err error) result {
	var dup *core.DupError
	switch {
	case errors.As(err, &dup):
		return failReason(errDuplicate, "這個任務已在清單中")
	case errors.Is(err, core.ErrFolder), errors.Is(err, core.ErrReadOnly):
		return fail(errFolderDenied)
	case errors.Is(err, core.ErrBadURL):
		return fail(errURLNotSupported)
	case errors.Is(err, core.ErrBadMagnet):
		return fail(errMagnetFormat)
	case errors.Is(err, core.ErrBadTorrent):
		return fail(errTorrentFormat)
	}
	return failReason(errException, err.Error())
}

func (s *service) addURL(c *call) result {
	urls := c.p.all("url")
	if len(urls) == 0 {
		return fail(errParamNotFound)
	}
	o, code := c.addOptions()
	if code != errOK {
		return fail(code)
	}
	switch cfg := c.p.get("config"); {
	case cfg == "-1":
		o.AccountMode = "none"
	case cfg != "" && cfg != "0":
		if a, err := s.m.Account(cfg); err == nil && a.Owner == c.who.User {
			o.AccountMode, o.AccountID = "id", a.ID
		}
	case c.p.get("user") != "":
		o.AccountMode = "manual"
		o.ManualUser = fixDoubleUTF8(c.p.get("user"))
		o.ManualPass = fixDoubleUTF8(ezDecode(c.p.get("pass")))
	}
	var last result
	added := 0
	var dupNames []string
	for _, raw := range urls {
		raw = core.UnwrapLink(raw)
		if raw == "" {
			continue
		}
		var err error
		if strings.HasPrefix(strings.ToLower(raw), "magnet:") {
			_, err = s.m.AddMagnet(raw, o)
		} else {
			_, err = s.m.AddURL(normalizeScheme(raw), o)
		}
		if err != nil {
			var dup *core.DupError
			if errors.As(err, &dup) {
				dupNames = append(dupNames, raw)
			}
			last = addError(err)
			continue
		}
		added++
	}
	if last != nil && added == 0 || len(dupNames) > 0 {
		if len(dupNames) > 0 {
			return failReason(errDuplicate, strings.Join(dupNames, "\n"))
		}
		return last
	}
	return result{}
}

// normalizeScheme rejects qqdl/thunder/flashget links that did not decode
// (UnwrapLink runs first) and returns the URL unchanged otherwise.
func normalizeScheme(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		switch strings.ToLower(u.Scheme) {
		case "qqdl", "thunder", "flashget":
			return ""
		}
	}
	return raw
}

func (s *service) addTorrent(c *call) result {
	if len(c.files) == 0 {
		return fail(errTorrentMissing)
	}
	o, code := c.addOptions()
	if code != errOK {
		return fail(code)
	}
	show := c.p.get("show") == "1"
	var data []map[string]any
	var last result
	for _, f := range c.files {
		res, err := s.m.AddTorrent(f.data, o)
		if err != nil {
			last = addError(err)
			var dup *core.DupError
			if errors.As(err, &dup) {
				last = failReason(errDuplicate, f.name)
			}
			continue
		}
		if show {
			data = append(data, map[string]any{"hash": res.ID, "name": res.Name, "files": c.fileList(res.ID)})
		}
	}
	if last != nil && (len(c.files) == 1 || (show && data == nil)) {
		return last
	}
	if show {
		if data == nil {
			data = []map[string]any{}
		}
		return result{"data": data}
	}
	return result{}
}

func (c *call) fileList(hash string) []map[string]any {
	out := []map[string]any{}
	t := c.s.m.Live(hash)
	for _, f := range c.s.m.Files(hash) {
		done := f.Size > 0 && f.Done >= f.Size
		if t != nil && t.State == core.StDone && f.Priority > 0 {
			done = true
		}
		out = append(out, map[string]any{"no": f.Index, "filename": f.Path, "size": f.Size, "priority": f.Priority, "done": b2i(done)})
	}
	if len(out) == 0 && t != nil && t.Kind != core.KindBT {
		out = append(out, map[string]any{"no": 0, "filename": t.Name, "size": t.Size, "priority": 1, "done": b2i(t.State == core.StDone)})
	}
	return out
}

func (s *service) start(c *call) result {
	hs, code := c.hashes(false)
	if code != errOK {
		return fail(code)
	}
	for _, h := range hs {
		s.m.Resume(h, "v4")
	}
	return result{}
}

func (s *service) stop(c *call) result {
	hs, code := c.hashes(false)
	if code != errOK {
		return fail(code)
	}
	for _, h := range hs {
		s.m.Pause(h, 0, "v4")
	}
	return result{}
}

func (s *service) pause(c *call) result {
	hs, code := c.hashes(false)
	if code != errOK {
		return fail(code)
	}
	minutes, _ := strconv.Atoi(c.p.get("time"))
	for _, h := range hs {
		s.m.Pause(h, minutes, "v4")
	}
	return result{}
}

func (s *service) remove(c *call) result {
	hs, code := c.hashes(c.p.get("completed") == "1")
	if code != errOK {
		return fail(code)
	}
	clean := c.p.get("clean") == "1"
	moving := false
	for _, h := range hs {
		if err := s.m.Remove(h, clean, false); err != nil && strings.Contains(err.Error(), "moving") {
			moving = true
		}
	}
	if moving {
		return fail(errFilesMoving)
	}
	return result{}
}

func (s *service) priority(c *call) result {
	h := c.p.get("hash")
	if _, code := c.owned(h); code != errOK {
		return fail(code)
	}
	where := c.p.get("priority")
	switch where {
	case "top", "up", "down", "bottom":
	default:
		return fail(errParameter)
	}
	if err := s.m.Move(h, where, nil); err != nil {
		return fail(errException)
	}
	return result{}
}

func (s *service) getFile(c *call) result {
	var data []map[string]any
	for _, h := range c.p.all("hash") {
		t, code := c.owned(h)
		if code != errOK {
			return fail(code)
		}
		data = append(data, map[string]any{"hash": t.Hash, "name": t.Name, "files": c.fileList(t.Hash)})
	}
	if data == nil {
		return fail(errParamNotFound)
	}
	return result{"data": data}
}

// setFile applies positional priorities: the n-th value is for file no = n.
func (s *service) setFile(c *call) result {
	h := c.p.get("hash")
	t, code := c.owned(h)
	if code != errOK {
		return fail(code)
	}
	if t.Kind != core.KindBT {
		return result{}
	}
	prio := map[int]int{}
	for i, v := range c.p.all("priority") {
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return fail(errParameter)
		}
		prio[i] = n
	}
	if len(prio) == 0 {
		return fail(errParamNotFound)
	}
	if err := s.m.SetFiles(h, prio); err != nil {
		return failReason(errParameter, err.Error())
	}
	return result{}
}

// getTorrentFile streams the stored .torrent (raw, not JSON).
func (s *service) getTorrentFile(c *call) result {
	h := c.p.get("hash")
	t, code := c.owned(h)
	if code != errOK {
		return fail(code)
	}
	b, err := os.ReadFile(filepath.Join(s.m.TorrentDir(), t.Hash+".torrent"))
	if err != nil {
		return fail(errTorrentMissing)
	}
	name := t.Name
	if meta, err := torrent.Parse(b); err == nil && meta.Name != "" {
		name = meta.Name
	}
	w := c.w
	w.Header().Set("Content-Type", "application/x-bittorrent")
	w.Header().Set("Content-Disposition", disposition(name+".torrent"))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	w.Write(b)
	return nil
}

func disposition(name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 32 || r > 126 || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	return `attachment; filename="` + ascii + `"; filename*=UTF-8''` + url.PathEscape(name)
}

var _ = auth.ErrNotOnList
