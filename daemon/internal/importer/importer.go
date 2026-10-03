// Package importer imports settings, tasks and accounts from the official
// Download Station. It only reads the official files (ds.conf, each
// volume's .torrent/ds.db and <infohash>.torrent), never moves official
// data, and can be run again without duplicating anything.
package importer

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"downloadcenter/internal/api"
	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/qts"

	_ "modernc.org/sqlite"
)

// Paths of the official package (variables for tests).
var (
	ConfPath    = "/etc/config/ds.conf"
	VolumeGlob  = "/share/*/.torrent/ds.db"
	QPKGService = "/sbin/qpkg_service"
)

// Importer reads the official data.
type Importer struct {
	m    *core.Manager
	au   *auth.Service
	work string // scratch folder for database copies (inside data/)
	mu   sync.Mutex
}

// Task is an official task joined with its owner.
type Task struct {
	Hash     string
	Type     int // 0 BT, 1 FTP, 2 HTTP, other = unsupported
	Source   string
	Size     int64
	State    int
	Start    int64
	Finish   int64
	Owner    string
	Created  int64
	Temp     string
	Move     string
	Caller   string
	Volume   string // the volume folder holding ds.db / .torrent
	Selected []int  // nil = all
	Files    int
}

// Account is an official site credential.
type Account struct {
	Owner   string
	Host    string
	User    string
	HasPass bool
	Enabled bool
}

// Volume is one official database.
type Volume struct {
	Path       string `json:"path"`
	Tasks      int    `json:"tasks"`
	Unfinished int    `json:"unfinished"`
	Completed  int    `json:"completed"`
	Accounts   int    `json:"accounts"`
	tasks      []Task
	accounts   []Account
}

// New creates an importer working in dataDir/import.
func New(m *core.Manager, au *auth.Service, dataDir string) *Importer {
	return &Importer{m: m, au: au, work: filepath.Join(dataDir, "import")}
}

// Running reports whether the official daemon (dsd) is running.
func Running() bool {
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		if e.Name()[0] < '0' || e.Name()[0] > '9' {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil || len(b) == 0 {
			continue
		}
		arg0 := string(bytes.SplitN(b, []byte{0}, 2)[0])
		if filepath.Base(arg0) == "dsd" {
			return true
		}
	}
	return false
}

// volumes lists the distinct official databases.
func volumes() []string {
	seen := map[string]bool{}
	var out []string
	matches, _ := filepath.Glob(VolumeGlob)
	for _, p := range matches {
		real, err := filepath.EvalSymlinks(p)
		if err != nil || seen[real] {
			continue
		}
		seen[real] = true
		out = append(out, real)
	}
	sort.Strings(out)
	return out
}

// readVolume copies ds.db (and its WAL) to the scratch folder and reads it,
// so the official files are never opened for writing.
func (im *Importer) readVolume(dbPath string, idx int) (*Volume, error) {
	dir := filepath.Join(im.work, fmt.Sprintf("v%d", idx))
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	for _, suf := range []string{"", "-wal"} {
		b, err := os.ReadFile(dbPath + suf)
		if err != nil {
			if suf == "" {
				return nil, err
			}
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, "ds.db"+suf), b, 0600); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "ds.db"))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	v := &Volume{Path: filepath.Dir(filepath.Dir(dbPath))}
	rows, err := db.Query(`SELECT T.hash, COALESCE(T.type, 6), COALESCE(T.source, ''), COALESCE(T.size, 0), COALESCE(T.state, 0),
		COALESCE(T.start_time, 0), COALESCE(T.finish_time, 0), COALESCE(T.total_files, 0),
		COALESCE(I.username, ''), COALESCE(U.create_time, 0), COALESCE(U.temp, ''), COALESCE(U.move, ''), COALESCE(U.caller, '')
		FROM Task T LEFT JOIN UserTask U ON U.task_hash = T.hash LEFT JOIN UserInfo I ON I.uid = U.uid`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t Task
		var size float64
		if err := rows.Scan(&t.Hash, &t.Type, &t.Source, &size, &t.State, &t.Start, &t.Finish, &t.Files,
			&t.Owner, &t.Created, &t.Temp, &t.Move, &t.Caller); err != nil {
			continue
		}
		t.Size = int64(size)
		t.Hash = strings.ToLower(t.Hash)
		t.Volume = v.Path
		v.tasks = append(v.tasks, t)
	}
	rows.Close()
	// File selection (priority 0 = skipped)
	for i := range v.tasks {
		t := &v.tasks[i]
		if t.Type != 0 {
			continue
		}
		fr, err := db.Query(`SELECT no, priority FROM TaskFile WHERE hash = ? ORDER BY no`, t.Hash)
		if err != nil {
			continue
		}
		var sel []int
		total := 0
		for fr.Next() {
			var no, prio int
			fr.Scan(&no, &prio)
			total++
			if prio > 0 {
				sel = append(sel, no)
			}
		}
		fr.Close()
		if total > 0 && len(sel) < total {
			t.Selected = sel
			if t.Selected == nil {
				t.Selected = []int{}
			}
		}
	}
	ar, err := db.Query(`SELECT COALESCE(I.username, ''), COALESCE(A.host, ''), COALESCE(A.huser, ''), COALESCE(A.hpass, ''), COALESCE(A.enable, 1)
		FROM UserAccount A LEFT JOIN UserInfo I ON I.uid = A.uid`)
	if err == nil {
		for ar.Next() {
			var a Account
			var pass string
			var en int
			ar.Scan(&a.Owner, &a.Host, &a.User, &pass, &en)
			a.HasPass, a.Enabled = pass != "", en != 0
			v.accounts = append(v.accounts, a)
		}
		ar.Close()
	}
	for _, t := range v.tasks {
		v.Tasks++
		if t.State == 5 {
			v.Completed++
		} else {
			v.Unfinished++
		}
	}
	v.Accounts = len(v.accounts)
	return v, nil
}

func (im *Importer) readAll() ([]*Volume, []string) {
	var out []*Volume
	var warns []string
	for i, p := range volumes() {
		v, err := im.readVolume(p, i)
		if err != nil {
			warns = append(warns, fmt.Sprintf("無法讀取第 %d 個磁碟區的官方資料庫：%v", i+1, err))
			continue
		}
		out = append(out, v)
	}
	return out, warns
}

// Detection is the answer of GET /import.
type Detection struct {
	Available    bool           `json:"available"`
	Official     map[string]any `json:"official"`
	Settings     map[string]any `json:"settings"`
	Volumes      []*Volume      `json:"volumes"`
	UsersMissing []string       `json:"users_missing"`
	Warnings     []string       `json:"warnings"`
	Last         map[string]any `json:"last,omitempty"`
	Migrated     bool           `json:"migrated"`
}

// Detect inspects the official data.
func (im *Importer) Detect() *Detection {
	im.mu.Lock()
	defer im.mu.Unlock()
	inst, en := qts.QPKGInstalled("DownloadStation")
	d := &Detection{Official: map[string]any{"installed": inst, "enabled": en, "running": Running()}}
	_, err := os.Stat(ConfPath)
	d.Settings = map[string]any{"found": err == nil}
	vols, warns := im.readAll()
	d.Volumes, d.Warnings = vols, warns
	if d.Volumes == nil {
		d.Volumes = []*Volume{}
	}
	missing := map[string]bool{}
	for _, v := range vols {
		for _, t := range v.tasks {
			if t.Owner != "" && !im.onList(t.Owner) {
				missing[t.Owner] = true
			}
		}
		for _, a := range v.accounts {
			if a.Owner != "" && !im.onList(a.Owner) {
				missing[a.Owner] = true
			}
		}
	}
	d.UsersMissing = []string{}
	for u := range missing {
		d.UsersMissing = append(d.UsersMissing, u)
	}
	sort.Strings(d.UsersMissing)
	var last map[string]any
	im.m.DB().GetJSON("import_last", &last)
	d.Last = last
	// Shown only while the official package is installed and nothing has
	// been migrated yet
	d.Migrated = migrated(last)
	d.Available = inst && (err == nil || len(vols) > 0) && !d.Migrated
	return d
}

func migrated(last map[string]any) bool {
	t, _ := last["time"].(float64)
	return t > 0
}

func (im *Importer) onList(user string) bool {
	_, err := im.au.GetUser(user)
	return err == nil
}

// Request selects what to import.
type Request struct {
	Settings   bool     `json:"settings"`
	Unfinished bool     `json:"unfinished"`
	Completed  bool     `json:"completed"`
	Accounts   bool     `json:"accounts"`
	AddUsers   []string `json:"add_users"`
}

// Summary reports an import run.
type Summary struct {
	Settings      bool     `json:"settings"`
	TasksAdded    int      `json:"tasks_added"`
	TasksSkipped  int      `json:"tasks_skipped"`
	HistoryAdded  int      `json:"history_added"`
	AccountsAdded int      `json:"accounts_added"`
	UsersAdded    int      `json:"users_added"`
	Warnings      []string `json:"warnings"`
	Time          int64    `json:"time"`
}

// ErrRunning is returned while the official daemon runs.
var ErrRunning = errors.New("請先停止官方 Download Station")

// Import runs the import. It refuses while the official daemon runs, so the
// two never write the same temporary files.
func (im *Importer) Import(req Request) (*Summary, error) {
	if Running() {
		return nil, ErrRunning
	}
	im.mu.Lock()
	defer im.mu.Unlock()
	sum := &Summary{Warnings: []string{}, Time: time.Now().Unix()}
	for _, u := range req.AddUsers {
		if im.onList(u) {
			continue
		}
		if err := im.au.AddUser(u, "user"); err != nil {
			sum.Warnings = append(sum.Warnings, fmt.Sprintf("無法加入使用者 %s：%v", u, err))
		} else {
			sum.UsersAdded++
		}
	}
	if req.Settings {
		if err := im.importSettings(sum); err != nil {
			sum.Warnings = append(sum.Warnings, "設定未匯入："+err.Error())
		}
	}
	if req.Unfinished || req.Completed || req.Accounts {
		vols, warns := im.readAll()
		sum.Warnings = append(sum.Warnings, warns...)
		for _, v := range vols {
			for _, t := range v.tasks {
				done := t.State == 5
				if (done && !req.Completed) || (!done && !req.Unfinished) {
					continue
				}
				im.importTask(t, sum)
			}
			if req.Accounts {
				im.importAccounts(v.accounts, sum)
			}
		}
	}
	im.m.DB().PutJSON("import_last", sum)
	return sum, nil
}

func (im *Importer) importSettings(sum *Summary) error {
	f, err := os.Open(ConfPath)
	if err != nil {
		return err
	}
	defer f.Close()
	c := parseINI(f)
	st := im.m.Settings()
	_, enabled := qts.QPKGInstalled("DownloadStation")
	socks := false
	if bt := im.m.BTEngine(); bt != nil {
		socks = bt.Caps().Socks5Peers
	}
	r := applySettings(c, &st, socks, !enabled)
	sum.Warnings = append(sum.Warnings, r.Warnings...)
	if r.TempShare != "" {
		if p, err := im.m.ResolvePath("", r.TempShare); err == nil {
			st.TempDir = p
		} else {
			sum.Warnings = append(sum.Warnings, "找不到官方的下載資料夾，沿用目前的設定")
		}
	}
	if r.MoveShare != "" {
		if p, err := im.m.ResolvePath("", r.MoveShare); err == nil && p != st.TempDir {
			st.MoveDir = p
		} else if err == nil {
			st.MoveDir = ""
		}
	}
	if err := im.m.SaveSettings(st); err != nil {
		return err
	}
	if r.ProxySocks && r.ProxyPass != "" {
		im.m.SetProxyPassword(r.ProxyID, r.ProxyPass)
	}
	sum.Settings = true
	return nil
}

// officialTempFolder finds the official temporary folder of a task:
// <share root>/@DownloadStationTempFiles/<user>/<name>.<hash>.
func officialTempFolder(shareRoot, hash string) string {
	matches, _ := filepath.Glob(filepath.Join(shareRoot, core.OfficialTempName, "*", "*."+hash))
	for _, mt := range matches {
		if st, err := os.Stat(mt); err == nil && st.IsDir() {
			return mt
		}
	}
	return ""
}

// resolveFolder turns an official folder value (share-relative) into a
// real path for the owner.
func (im *Importer) resolveFolder(owner, v string) string {
	if v == "" {
		return ""
	}
	p, err := im.m.ResolvePath(owner, v)
	if err != nil {
		return ""
	}
	return p
}

func shareRootOf(p string) string {
	if sh, ok := qts.ShareOf(p); ok {
		return sh.Path
	}
	return p
}

func (im *Importer) importTask(t Task, sum *Summary) {
	skip := func(msg string) {
		sum.TasksSkipped++
		if msg != "" {
			sum.Warnings = append(sum.Warnings, msg)
		}
	}
	if t.Owner == "" || !im.onList(t.Owner) {
		skip("")
		return
	}
	kind := ""
	switch t.Type {
	case 0:
		kind = core.KindBT
	case 1:
		kind = core.KindFTP
	case 2:
		kind = core.KindHTTP
	case 3, 4, 5:
		// QQDL / Thunder / FlashGet: the real URL is inside the link
		t.Source = core.UnwrapLink(t.Source)
		if kind = core.KindOfURL(t.Source); kind == "" {
			skip("略過一個不支援的任務類型（QQDL / Thunder / FlashGet）")
			return
		}
	default:
		skip("略過一個不支援的任務類型（QQDL / Thunder / FlashGet）")
		return
	}
	hash := t.Hash
	if kind != core.KindBT {
		hash = core.URLHash(t.Source)
	}
	if im.m.HasTask(hash) {
		skip("")
		return
	}
	temp := im.resolveFolder(t.Owner, t.Temp)
	move := im.resolveFolder(t.Owner, t.Move)
	if temp == "" {
		temp = im.m.Settings().TempDir
	}
	if move == temp {
		move = ""
	}
	spec := core.ImportSpec{Hash: hash, Owner: t.Owner, Kind: kind, Source: t.Source, TempDir: temp, MoveDir: move,
		Size: t.Size, Created: t.Created, Started: t.Start, Finished: t.Finish, Caller: t.Caller, Select: t.Selected}
	done := t.State == 5
	if kind == core.KindBT {
		b, err := os.ReadFile(filepath.Join(t.Volume, ".torrent", t.Hash+".torrent"))
		if err == nil {
			spec.Torrent = b
		} else if !strings.HasPrefix(strings.ToLower(t.Source), "magnet:") && !done {
			skip("一個種子任務找不到官方的 .torrent 檔，已略過")
			return
		}
		if fr := filepath.Join(t.Volume, ".torrent", t.Hash+".fastresume"); fileExists(fr) {
			spec.ResumeFile = fr
		}
		if spec.Torrent != nil {
			spec.Source = t.Source
		}
	}
	if done {
		spec.Done = true
		dir := move
		if dir == "" {
			dir = temp
		}
		name := t.Source
		if kind != core.KindBT {
			name = filepath.Base(t.Source)
		} else if strings.HasSuffix(name, ".torrent") {
			name = strings.TrimSuffix(name, ".torrent")
		}
		spec.Name = name
		spec.DataPath = core.FinalPath(dir, name)
		if err := im.m.ImportTask(spec); err != nil {
			skip("")
			return
		}
		sum.HistoryAdded++
		return
	}
	// Unfinished: keep using the official temporary data where it is until
	// the download is complete
	if of := officialTempFolder(shareRootOf(temp), t.Hash); of != "" {
		spec.WorkDir = of
		if kind != core.KindBT {
			if ents, err := os.ReadDir(of); err == nil {
				for _, e := range ents {
					if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
						spec.OutName = e.Name()
						break
					}
				}
			}
		}
	} else if kind == core.KindBT && t.State == 100 && move != "" {
		// Seeding data already moved: seed from the destination
		spec.TempDir, spec.MoveDir = move, ""
	}
	if err := im.m.ImportTask(spec); err != nil {
		var dup *core.DupError
		if errors.As(err, &dup) {
			skip("")
		} else {
			skip("一個任務匯入失敗：" + err.Error())
		}
		return
	}
	sum.TasksAdded++
}

func (im *Importer) importAccounts(list []Account, sum *Summary) {
	warned := false
	for _, a := range list {
		if a.Owner == "" || a.Host == "" || !im.onList(a.Owner) {
			continue
		}
		dup := false
		for _, ex := range im.m.Accounts(a.Owner) {
			if ex.Kind == "site" && strings.EqualFold(ex.Host, a.Host) && ex.Username == a.User {
				dup = true
			}
		}
		if dup {
			continue
		}
		acc := &core.Account{Owner: a.Owner, Kind: "site", Host: a.Host, Username: a.User, Enabled: a.Enabled,
			Info: map[string]any{"imported": true}}
		if err := im.m.SaveAccount(acc, ""); err != nil {
			continue
		}
		sum.AccountsAdded++
		if a.HasPass && !warned {
			warned = true
			sum.Warnings = append(sum.Warnings, "官方版的網站帳號密碼經過加密無法讀取，已匯入網站與帳號名稱，請到 設定 › 網站帳號 重新輸入密碼")
		}
	}
}

// StopOfficial stops and disables the official package (UI button before
// importing), the way App Center does: disabled, it does not start again at
// boot and resume the tasks being imported. Never called automatically.
func StopOfficial() error {
	if st, err := os.Stat(QPKGService); err == nil && !st.IsDir() {
		if out, err := runFor(90*time.Second, QPKGService, "stop", "DownloadStation"); err != nil {
			log.Printf("importer: qpkg_service stop: %v: %s", err, out)
		}
		if out, err := runFor(30*time.Second, QPKGService, "disable", "DownloadStation"); err != nil {
			log.Printf("importer: qpkg_service disable: %v: %s", err, out)
		}
	}
	if Running() {
		// Fallback: the package's own service script (qpkg.conf Shell, e.g.
		// /etc/init.d/dsd.sh)
		script := officialScript()
		if script == "" {
			return errors.New("找不到官方 Download Station 的服務腳本，請到 App Center 停用它")
		}
		if out, err := runFor(90*time.Second, "/bin/sh", script, "stop"); err != nil {
			// One variable part, so the message has a stable template
			return fmt.Errorf("停止官方 Download Station 失敗：%s", strings.TrimSpace(fmt.Sprintf("%v %s", err, strings.TrimSpace(out))))
		}
		exec.Command("/sbin/setcfg", "DownloadStation", "Enable", "FALSE", "-f", "/etc/config/qpkg.conf").Run()
	}
	for i := 0; i < 30 && Running(); i++ {
		time.Sleep(time.Second)
	}
	if Running() {
		return errors.New("官方 Download Station 仍在執行，請到 App Center 停用它")
	}
	return nil
}

// officialScript finds the official service script.
func officialScript() string {
	cands := []string{qts.GetCfg("DownloadStation", "Shell", "/etc/config/qpkg.conf", ""), "/etc/init.d/dsd.sh", "/etc/init.d/DownloadStation.sh"}
	for _, c := range cands {
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

func runFor(d time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// Register adds the import routes.
func Register(srv *api.Server, m *core.Manager, au *auth.Service) {
	im := New(m, au, m.DataDir)
	var last map[string]any
	m.DB().GetJSON("import_last", &last)
	if inst, _ := qts.QPKGInstalled("DownloadStation"); inst && !migrated(last) {
		if _, err := os.Stat(ConfPath); err == nil || len(volumes()) > 0 {
			srv.Extra["import_available"] = true
		}
	}
	srv.Route("GET /import", "", api.AdminOnly|api.Session, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		d := im.Detect()
		d.Warnings = trList(w, d.Warnings)
		if d.Last != nil {
			if ws, ok := d.Last["warnings"].([]any); ok {
				for i, x := range ws {
					if s, ok := x.(string); ok {
						ws[i] = api.Tr(w, s)
					}
				}
			}
		}
		api.OK(w, d)
	})
	srv.Route("POST /import", "", api.AdminOnly|api.Session, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		var req Request
		if err := api.Decode(r, &req); err != nil {
			api.Error(w, 400, "bad_request", err.Error())
			return
		}
		sum, err := im.Import(req)
		if err == ErrRunning {
			api.Error(w, 409, "official_running", err.Error())
			return
		}
		if err != nil {
			api.Error(w, 500, "failed", err.Error())
			return
		}
		out := *sum
		out.Warnings = trList(w, sum.Warnings)
		api.OK(w, map[string]any{"summary": out})
	})
	srv.Route("POST /import/stop-official", "", api.AdminOnly|api.Session, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		if err := StopOfficial(); err != nil {
			api.Error(w, 500, "failed", err.Error())
			return
		}
		api.OK(w, map[string]any{"ok": true, "running": Running()})
	})
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// trList translates warnings into the response's language (the stored
// summary keeps the source text).
func trList(w http.ResponseWriter, l []string) []string {
	if l == nil {
		return l
	}
	out := make([]string, len(l))
	for i, s := range l {
		out[i] = api.Tr(w, s)
	}
	return out
}
