package notify

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"downloadcenter/internal/api"
	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
)

// Reply is the answer of a chat command.
type Reply struct {
	OK      bool           `json:"ok"`
	Reply   string         `json:"reply"`
	Task    map[string]any `json:"task,omitempty"`
	Buttons []Button       `json:"buttons,omitempty"`
}

type listCtx struct {
	ids     []string
	expires time.Time
}

var stateNames = map[string]string{
	core.StQueued: "等待中", core.StDownloading: "下載中", core.StMetadata: "取得檔案清單", core.StChecking: "檢查中",
	core.StPaused: "已暫停", core.StSeeding: "做種中", core.StMoving: "搬移中", core.StDone: "已完成", core.StError: "錯誤",
}

const helpText = `可以用的指令：
/list [下載中|已完成|錯誤] 列出任務
/add <網址或磁力連結> 加入下載（可以多行）
/pause <編號|all>、/resume <編號|all>、/retry <編號> 暫停、繼續、重試
/del <編號> 從清單移除（檔案保留）
/speed 目前速度、/disk 剩餘空間
/limit <2M|500K|off> 下載速度上限、/sched on|off 排程開關
/help 這份說明`

var linkLineRe = regexp.MustCompile(`(?i)(?:https?|s?ftps?|scp|thunder|flashget|qqdl)://\S+|magnet:\?\S+`)

func deny(scope string) Reply {
	return Reply{Reply: "沒有權限執行這個指令（需要 " + scope + "）。"}
}

// Run executes one chat command for principal p. caller identifies the
// conversation (list numbers are kept per caller for 10 minutes).
func (s *Service) Run(p *auth.Principal, caller, text string) Reply {
	text = strings.TrimSpace(text)
	if text == "" {
		return Reply{Reply: helpText}
	}
	cmd, rest, _ := strings.Cut(text, " ")
	if i := strings.IndexByte(cmd, '\n'); i >= 0 {
		rest = cmd[i+1:] + " " + rest
		cmd = cmd[:i]
	}
	cmd = strings.ToLower(cmd)
	if at := strings.IndexByte(cmd, '@'); at > 0 { // Telegram "/list@MyBot"
		cmd = cmd[:at]
	}
	rest = strings.TrimSpace(rest)
	// A bare link is an /add
	if !strings.HasPrefix(cmd, "/") && linkLineRe.MatchString(text) {
		cmd, rest = "/add", text
	}
	switch cmd {
	case "/help", "/start":
		return Reply{OK: true, Reply: helpText}
	case "/list":
		if !p.Can("tasks:read") {
			return deny("tasks:read")
		}
		return s.cmdList(p, caller, rest)
	case "/add":
		if !p.Can("tasks:add") {
			return deny("tasks:add")
		}
		return s.cmdAdd(p, rest)
	case "/pause", "/resume", "/retry":
		if !p.Can("tasks:control") {
			return deny("tasks:control")
		}
		return s.cmdControl(p, caller, cmd, rest)
	case "/del", "/delete", "/rm":
		if !p.Can("tasks:remove") {
			return deny("tasks:remove")
		}
		return s.cmdDel(p, caller, rest)
	case "/speed":
		if !p.Can("stats:read") {
			return deny("stats:read")
		}
		return s.cmdSpeed(p)
	case "/disk":
		if !p.Can("stats:read") {
			return deny("stats:read")
		}
		return s.cmdDisk(p)
	case "/limit":
		if !p.Can("settings:write") {
			return deny("settings:write")
		}
		return s.cmdLimit(rest)
	case "/sched":
		if !p.Can("settings:write") {
			return deny("settings:write")
		}
		return s.cmdSched(rest)
	case "/link":
		return Reply{Reply: "這個帳號已經連結。"}
	}
	return Reply{Reply: "看不懂這個指令。傳送 /help 看說明。"}
}

func (s *Service) visible(p *auth.Principal) []*core.Task {
	var out []*core.Task
	for _, t := range s.m.List() {
		if p.SeesOwner(t.Owner) {
			out = append(out, t)
		}
	}
	return out
}

func progressLine(t *core.Task) string {
	st := stateNames[t.State]
	if t.UserPaused && t.State != core.StError && t.State != core.StDone {
		st = "已暫停"
	}
	switch t.State {
	case core.StDownloading:
		line := fmt.Sprintf("%.0f%% %s", t.Progress(), SpeedH(t.DownRate))
		if eta := t.ETA(); eta >= 0 {
			line += "，剩 " + DurationH(eta)
		}
		return line
	case core.StSeeding:
		return fmt.Sprintf("做種中 ↑%s，分享率 %.2f", SpeedH(t.UpRate), t.Ratio())
	case core.StError:
		return "錯誤：" + zh(t.ErrorMsg)
	}
	if t.State != core.StDone && t.Size > 0 {
		return fmt.Sprintf("%s %.0f%%", st, t.Progress())
	}
	return st
}

func (s *Service) cmdList(p *auth.Principal, caller, filter string) Reply {
	var list []*core.Task
	for _, t := range s.visible(p) {
		switch filter {
		case "下載中", "downloading":
			if t.State == core.StDone || t.State == core.StError || t.State == core.StSeeding {
				continue
			}
		case "已完成", "完成", "done", "completed":
			if t.State != core.StDone && t.State != core.StSeeding {
				continue
			}
		case "錯誤", "error", "failed":
			if t.State != core.StError {
				continue
			}
		}
		list = append(list, t)
	}
	if len(list) == 0 {
		return Reply{OK: true, Reply: "沒有符合的任務。"}
	}
	var b strings.Builder
	ids := make([]string, 0, len(list))
	for i, t := range list {
		if i >= 30 {
			fmt.Fprintf(&b, "…還有 %d 個任務\n", len(list)-30)
			break
		}
		ids = append(ids, t.Hash)
		fmt.Fprintf(&b, "%d. %s — %s\n", i+1, t.Name, progressLine(t))
	}
	s.mu.Lock()
	s.lists[caller] = listCtx{ids: ids, expires: s.now().Add(10 * time.Minute)}
	s.mu.Unlock()
	return Reply{OK: true, Reply: strings.TrimSpace(b.String())}
}

// resolve finds a task by list number or id prefix.
func (s *Service) resolve(p *auth.Principal, caller, arg string) (*core.Task, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return nil, errors.New("請指定任務編號（先用 /list 列出）")
	}
	if n, err := strconv.Atoi(arg); err == nil && len(arg) < 4 {
		s.mu.Lock()
		ctx, ok := s.lists[caller]
		s.mu.Unlock()
		if !ok || s.now().After(ctx.expires) {
			return nil, errors.New("編號已過期，請重新 /list")
		}
		if n < 1 || n > len(ctx.ids) {
			return nil, errors.New("沒有這個編號")
		}
		t := s.m.Live(ctx.ids[n-1])
		if t == nil || !p.SeesOwner(t.Owner) {
			return nil, errors.New("這個任務已不在清單中")
		}
		return t, nil
	}
	arg = strings.ToLower(arg)
	if len(arg) >= 4 {
		var found *core.Task
		for _, t := range s.visible(p) {
			if strings.HasPrefix(t.Hash, arg) {
				if found != nil {
					return nil, errors.New("符合的任務不只一個，請多打幾個字")
				}
				found = t
			}
		}
		if found != nil {
			return found, nil
		}
	}
	return nil, errors.New("找不到這個任務")
}

func taskRef(t *core.Task) map[string]any {
	return map[string]any{"id": t.Hash, "name": t.Name, "state": t.State}
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func (s *Service) cmdAdd(p *auth.Principal, text string) Reply {
	links := linkLineRe.FindAllString(text, 50)
	if len(links) == 0 {
		return Reply{Reply: "沒有找到網址或磁力連結。用法：/add <網址>"}
	}
	o := core.AddOptions{Owner: p.User, Admin: p.Admin, AutoRemove: "default", Caller: "Chat"}
	if p.Via == "token" && p.Token != nil {
		o.Caller = "API: " + p.Token.Name
	}
	if len(p.Folders) > 0 {
		o.TempDir = p.Folders[0]
	}
	var lines []string
	var last *core.AddResult
	for _, l := range links {
		var res *core.AddResult
		var err error
		// thunder://, flashget:// and qqdl:// carry the real link, maybe a magnet
		l = core.UnwrapLink(l)
		if strings.HasPrefix(strings.ToLower(l), "magnet:") {
			if !p.SourceAllowed("magnet") {
				lines = append(lines, "不允許加入磁力連結")
				continue
			}
			res, err = s.m.AddMagnet(l, o)
		} else {
			if !p.SourceAllowed("url") {
				lines = append(lines, "不允許加入網址")
				continue
			}
			if b := s.m.FetchTorrentLink(l, o.Proxy, p.Admin); b != nil {
				res, err = s.m.AddTorrent(b, o)
			} else {
				res, err = s.m.AddURL(l, o)
			}
		}
		switch {
		case err != nil && errors.Is(err, core.ErrDuplicate):
			name := l
			if res != nil && res.Name != "" {
				name = res.Name
			}
			lines = append(lines, "已在清單中："+name)
		case err != nil:
			lines = append(lines, "無法加入："+addError(err))
		case res.Merged:
			lines = append(lines, "已併入既有任務："+res.Name)
			last = res
		default:
			lines = append(lines, "已加入："+firstNonEmpty(res.Name, l)+"（"+s.queuePos(res.ID)+"）")
			last = res
		}
	}
	r := Reply{OK: last != nil, Reply: strings.Join(lines, "\n")}
	if last != nil {
		if t := s.m.Live(last.ID); t != nil {
			r.Task = taskRef(t)
			r.Buttons = []Button{{"暫停", "/pause " + short(t.Hash)}}
		} else {
			r.Task = map[string]any{"id": last.ID, "name": last.Name}
		}
	}
	return r
}

func addError(err error) string {
	switch {
	case errors.Is(err, core.ErrBadURL):
		return "不支援這種網址"
	case errors.Is(err, core.ErrBadMagnet):
		return "磁力連結格式不正確"
	case errors.Is(err, core.ErrFolder):
		return "不能使用這個資料夾"
	case errors.Is(err, core.ErrBadTorrent):
		return "種子檔格式不正確"
	case errors.Is(err, core.ErrNoURL):
		return "這台 NAS 無法下載這種網址（下載元件 dc-dl 無法使用）"
	case errors.Is(err, core.ErrNoBT):
		return "這台 NAS 無法下載種子（缺少 BT 引擎）"
	}
	return zh(err.Error())
}

// queuePos describes where a new task waits ("等待中，第 2 位").
func (s *Service) queuePos(id string) string {
	t := s.m.Live(id)
	if t == nil {
		return "等待中"
	}
	n := 0
	for _, o := range s.m.List() {
		if o.Kind == t.Kind && (o.State == core.StQueued || o.State == core.StDownloading || o.State == core.StMetadata) && !o.UserPaused {
			n++
			if o.Hash == id {
				break
			}
		}
	}
	if t.UserPaused {
		return "已暫停"
	}
	return fmt.Sprintf("等待中，第 %d 位", n)
}

func (s *Service) cmdControl(p *auth.Principal, caller, cmd, arg string) Reply {
	verb := map[string]string{"/pause": "已暫停", "/resume": "已繼續", "/retry": "已重試"}[cmd]
	do := func(t *core.Task) error {
		switch cmd {
		case "/pause":
			return s.m.Pause(t.Hash, 0, "chat")
		case "/resume":
			return s.m.Resume(t.Hash, "chat")
		}
		return s.m.Retry(t.Hash)
	}
	if strings.EqualFold(strings.TrimSpace(arg), "all") {
		if cmd == "/retry" {
			return Reply{Reply: "/retry 要指定一個任務編號。"}
		}
		n := 0
		for _, t := range s.visible(p) {
			if t.State == core.StDone || t.State == core.StMoving {
				continue
			}
			if cmd == "/pause" && t.UserPaused || cmd == "/resume" && !t.UserPaused && t.State != core.StError {
				continue
			}
			if do(t) == nil {
				n++
			}
		}
		return Reply{OK: true, Reply: fmt.Sprintf("%s %d 個任務", verb, n)}
	}
	t, err := s.resolve(p, caller, arg)
	if err != nil {
		return Reply{Reply: zh(err.Error())}
	}
	if err := do(t); err != nil {
		return Reply{Reply: "失敗：" + zh(api.ErrorText(err))}
	}
	r := Reply{OK: true, Reply: verb + "：" + t.Name, Task: taskRef(t)}
	if cmd == "/pause" {
		r.Buttons = []Button{{"繼續", "/resume " + short(t.Hash)}}
	} else {
		r.Buttons = []Button{{"暫停", "/pause " + short(t.Hash)}}
	}
	return r
}

func (s *Service) cmdDel(p *auth.Principal, caller, arg string) Reply {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		return Reply{Reply: "用法：/del <編號>"}
	}
	confirm := len(fields) > 1 && strings.EqualFold(fields[1], "confirm")
	t, err := s.resolve(p, caller, fields[0])
	if err != nil {
		return Reply{Reply: zh(err.Error())}
	}
	key := caller + ":" + t.Hash
	if !confirm {
		s.mu.Lock()
		s.confirm[key] = t.Hash
		s.mu.Unlock()
		cmd := "/del " + fields[0] + " confirm"
		return Reply{OK: true, Reply: "要從清單移除「" + t.Name + "」嗎？檔案會保留。確認請傳送 " + cmd, Task: taskRef(t),
			Buttons: []Button{{"確認移除", "/del " + short(t.Hash) + " confirm"}}}
	}
	if err := s.m.Remove(t.Hash, false, false); err != nil {
		return Reply{Reply: "失敗：" + zh(api.ErrorText(err))}
	}
	s.mu.Lock()
	delete(s.confirm, key)
	s.mu.Unlock()
	return Reply{OK: true, Reply: "已從清單移除：" + t.Name + "（檔案保留）"}
}

func (s *Service) cmdSpeed(p *auth.Principal) Reply {
	var down, up int64
	active := 0
	for _, t := range s.visible(p) {
		down += t.DownRate
		up += t.UpRate
		if t.State == core.StDownloading {
			active++
		}
	}
	mode, next, nextMode := s.m.ScheduleState()
	line := fmt.Sprintf("↓ %s  ↑ %s，%d 個下載中\n排程：%s", SpeedH(down), SpeedH(up), active, modeNames[mode])
	if !next.IsZero() {
		line += fmt.Sprintf("，%s 起%s", next.Format("01/02 15:04"), modeNames[nextMode])
	}
	return Reply{OK: true, Reply: line}
}

func (s *Service) cmdDisk(p *auth.Principal) Reply {
	st := s.m.Settings()
	var dirs []string
	if p.Admin {
		dirs = []string{st.TempDir}
		if st.MoveDir != "" && st.MoveDir != st.TempDir {
			dirs = append(dirs, st.MoveDir)
		}
	} else {
		dirs = []string{s.m.UserDownloadDir(p.User)}
	}
	var lines []string
	for _, d := range dirs {
		free := core.FreeSpace(d)
		if free < 0 {
			continue
		}
		lines = append(lines, s.m.DisplayPath(p.User, d)+"：剩 "+SizeH(free))
	}
	if len(lines) == 0 {
		return Reply{Reply: "無法取得剩餘空間。"}
	}
	return Reply{OK: true, Reply: strings.Join(lines, "\n")}
}

// ParseRate parses "2M", "500K", "1.5MB/s", "off" into KB/s (0 = off).
func ParseRate(s string) (int, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "OFF" || s == "0" || s == "NONE" || s == "關" {
		return 0, nil
	}
	s = strings.TrimSuffix(strings.TrimSuffix(s, "/S"), "B")
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "G"):
		mult, s = 1024*1024, strings.TrimSuffix(s, "G")
	case strings.HasSuffix(s, "M"):
		mult, s = 1024, strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "K"):
		s = strings.TrimSuffix(s, "K")
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f <= 0 {
		return 0, errors.New("速度格式不正確，例如 2M、500K 或 off")
	}
	return int(f*mult + 0.5), nil
}

func (s *Service) cmdLimit(arg string) Reply {
	kb, err := ParseRate(arg)
	if err != nil {
		return Reply{Reply: zh(err.Error())}
	}
	st := s.m.Settings()
	st.HTTP.MaxDown, st.FTP.MaxDown, st.BT.MaxDown = kb, kb, kb
	if err := s.m.SaveSettings(st); err != nil {
		return Reply{Reply: "失敗：" + zh(api.ErrorText(err))}
	}
	if kb == 0 {
		return Reply{OK: true, Reply: "已取消下載速度上限"}
	}
	return Reply{OK: true, Reply: "下載速度上限（網址、FTP、種子各自）：" + SpeedH(int64(kb)*1024)}
}

func (s *Service) cmdSched(arg string) Reply {
	st := s.m.Settings()
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "on", "開":
		st.Schedule.Enabled = true
	case "off", "關":
		st.Schedule.Enabled = false
	default:
		state := "關閉"
		if st.Schedule.Enabled {
			state = "開啟"
		}
		return Reply{OK: true, Reply: "排程目前" + state + "。用法：/sched on|off"}
	}
	if err := s.m.SaveSettings(st); err != nil {
		return Reply{Reply: "失敗：" + zh(api.ErrorText(err))}
	}
	if st.Schedule.Enabled {
		return Reply{OK: true, Reply: "已開啟排程"}
	}
	return Reply{OK: true, Reply: "已關閉排程，一律全速"}
}

func callerKey(p *auth.Principal) string {
	if p.Via == "token" && p.Token != nil {
		return "tok:" + p.Token.ID
	}
	return "user:" + p.User
}

func (s *Service) commandRoutes(srv *api.Server) {
	srv.Route("POST /commands", "", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		var b struct {
			Text   string `json:"text"`
			Locale string `json:"locale"`
		}
		if err := api.Decode(r, &b); err != nil {
			api.Error(w, 400, "bad_request", err.Error())
			return
		}
		if strings.TrimSpace(b.Text) == "" {
			api.Error(w, 400, "bad_request", "text is required")
			return
		}
		api.OK(w, s.Run(p, callerKey(p), b.Text))
	})
}

// routes registers every route of the package.
func (s *Service) routes(srv *api.Server) {
	s.channelRoutes(srv)
	s.adapterRoutes(srv)
	s.commandRoutes(srv)
	s.lineRoutes(srv)
}
