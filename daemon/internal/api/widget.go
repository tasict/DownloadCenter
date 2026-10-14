package api

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/i18n"
)

// The Deskpane widget "Downloads" (shared/deskpane/widgets.json): Deskpane
// fetches this document with the user's QTS session while the widget is on
// screen and draws it with its own templates (a hero figure, a status
// sentence, a list, a sparkline). Every text is plain text, cut here to the
// lengths Deskpane has room for.

type wdgValue struct {
	Text     string   `json:"text"`
	Caption  string   `json:"caption,omitempty"`
	Progress *float64 `json:"progress,omitempty"`
}

type wdgStatus struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

type wdgItem struct {
	Title    string   `json:"title"`
	Detail   string   `json:"detail,omitempty"`
	Value    string   `json:"value,omitempty"`
	Progress *float64 `json:"progress,omitempty"`
	Level    string   `json:"level,omitempty"`
	Icon     string   `json:"icon,omitempty"`
	Open     string   `json:"open,omitempty"`
}

type wdgSeries struct {
	Values []float64 `json:"values"`
	Label  string    `json:"label"`
}

type wdgDoc struct {
	Value   *wdgValue  `json:"value,omitempty"`
	Status  *wdgStatus `json:"status,omitempty"`
	Items   []wdgItem  `json:"items,omitempty"`
	Series  *wdgSeries `json:"series,omitempty"`
	Open    string     `json:"open"`
	Refresh int        `json:"refresh"`
	Empty   string     `json:"empty,omitempty"`
}

// Rows per widget size, as many as Deskpane has room for: at most 3 in
// small and 4 in medium; in large, below the hero and the sparkline, 3 (it
// would draw 5 and cut the last ones off), else 6.
var wdgRows = map[string]int{"s": 3, "m": 4, "l": 6}

const wdgRowsUnderChart = 3

func (s *Server) widgetRoutes() {
	s.Route("GET /widget/queue", "", Session, s.widgetQueue)
}

func (s *Server) widgetQueue(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	q := r.URL.Query()
	size := q.Get("size")
	if wdgRows[size] == 0 {
		size = "m"
	}
	shell := q.Get("shell")
	if shell != "phone" {
		shell = "desktop"
	}
	s.countWidget(p.User, "wdg_queue_"+size, "wdg_queue_"+shell)
	OK(w, s.queueDoc(p, widgetLang(r), size, q.Get("show") == "all", s.M.List(), time.Now()))
}

// queueDoc builds the document for p at one size from tasks (in queue order).
func (s *Server) queueDoc(p *auth.Principal, lang, size string, all bool, tasks []*core.Task, now time.Time) wdgDoc {
	tr := func(key string, args ...string) string { return i18n.F(lang, key, args...) }
	var active, failed, waiting, paused, finished []*core.Task
	var down, done, total int64
	for _, t := range tasks {
		if !p.SeesOwner(t.Owner) {
			continue
		}
		down += t.DownRate
		switch t.State {
		case core.StDownloading, core.StMetadata, core.StChecking, core.StMoving:
			active = append(active, t)
			if t.Size > 0 {
				done += min(t.DoneBytes, t.Size)
				total += t.Size
			}
		case core.StError:
			failed = append(failed, t)
		case core.StQueued:
			waiting = append(waiting, t)
		case core.StPaused:
			paused = append(paused, t)
		case core.StSeeding, core.StDone:
			finished = append(finished, t)
		}
	}
	ranks := s.M.QueueRanks()
	// List is in queue order; waiting tasks go by their place in the queue,
	// finished ones newest first
	sort.SliceStable(waiting, func(i, j int) bool { return rankOf(ranks, waiting[i]) < rankOf(ranks, waiting[j]) })
	sort.SliceStable(finished, func(i, j int) bool { return finished[i].FinishedAt > finished[j].FinishedAt })
	inProgress := len(active) + len(failed) + len(waiting) + len(paused)

	doc := wdgDoc{Open: Base + "/#tasks", Refresh: 60}
	if len(active) > 0 {
		doc.Refresh = 15
	}
	if len(active) > 0 || down > 0 {
		v := &wdgValue{Text: cut(fspeed(down), 16)}
		switch {
		case len(active) > 0 && len(waiting) > 0:
			v.Caption = tr("{0} downloading, {1} waiting", strconv.Itoa(len(active)), strconv.Itoa(len(waiting)))
		case len(active) > 0:
			v.Caption = tr("{0} downloading", strconv.Itoa(len(active)))
		}
		v.Caption = cut(v.Caption, 60)
		if total > 0 {
			f := math.Round(float64(done)/float64(total)*1000) / 1000
			v.Progress = &f
		}
		doc.Value = v
	}
	doc.Status = s.queueStatus(p, tr, size, len(failed), inProgress, len(finished), now)
	if doc.Status != nil {
		doc.Status.Text = cut(doc.Status.Text, 80)
	}

	if size != "s" {
		hist := s.M.RateHistory(p.SeesOwner)
		var vals []float64
		moved := false
		for _, b := range hist {
			moved = moved || b > 0
			vals = append(vals, math.Round(float64(b)/(1<<20)*100)/100)
		}
		if moved {
			doc.Series = &wdgSeries{Values: vals, Label: cut(tr("Download speed, last 15 minutes"), 40)}
		}
	}

	list := append(append(append(active, failed...), waiting...), paused...)
	if all {
		list = append(list, finished...)
	}
	rows := wdgRows[size]
	if size == "l" && doc.Series != nil {
		rows = wdgRowsUnderChart
	}
	for _, t := range list {
		if len(doc.Items) == rows {
			break
		}
		doc.Items = append(doc.Items, s.queueItem(p, tr, lang, t, ranks, now))
	}

	switch {
	case len(failed) > 0:
		doc.Open = Base + "/#tasks/error"
	case len(active) > 0:
		doc.Open = Base + "/#tasks/down"
	}
	if inProgress+len(finished) == 0 {
		doc.Empty = tr("No downloads yet")
	} else {
		doc.Empty = tr("Nothing is in progress")
	}
	doc.Empty = cut(doc.Empty, 80)
	return doc
}

// queueStatus is the one sentence worth a look, if any: a stopped engine,
// failed downloads, low disk space, the schedule pausing or limiting
// downloads, or everything finished.
func (s *Server) queueStatus(p *auth.Principal, tr func(string, ...string) string, size string, failed, inProgress, finished int, now time.Time) *wdgStatus {
	if p.Admin {
		for name, st := range s.M.EngineHealth() {
			if st != "down" {
				continue
			}
			if name == "libtorrent" {
				return &wdgStatus{"bad", tr("The torrent engine is not running")}
			}
			return &wdgStatus{"bad", tr("The URL download engine is not running")}
		}
	}
	if failed == 1 {
		return &wdgStatus{"bad", tr("1 download failed")}
	}
	if failed > 1 {
		return &wdgStatus{"bad", tr("{0} downloads failed", strconv.Itoa(failed))}
	}
	st := s.M.Settings()
	if st.DiskLowMB > 0 {
		dirs := []string{st.TempDir, st.MoveDir}
		if !p.Admin {
			dirs = []string{s.M.UserDownloadDir(p.User)}
		}
		for _, d := range dirs {
			if d == "" {
				continue
			}
			if free := s.freeSpace(d, now); free >= 0 && free < int64(st.DiskLowMB)<<20 {
				return &wdgStatus{"warn", tr("Low on space: {0} free in {1}", fsize(free), s.M.DisplayPath(p.User, d))}
			}
		}
	}
	mode, next, _ := s.M.ScheduleState()
	until := ""
	if !next.IsZero() && next.Sub(now) <= 24*time.Hour {
		until = next.Format("15:04")
	}
	switch {
	case mode == "off" && until != "":
		return &wdgStatus{"warn", tr("Paused by the schedule until {0}", until)}
	case mode == "off":
		return &wdgStatus{"warn", tr("Paused by the schedule")}
	case mode == "limited" && size != "s" && until != "":
		return &wdgStatus{"warn", tr("Limited speed until {0}", until)}
	case mode == "limited" && size != "s":
		return &wdgStatus{"warn", tr("Limited speed by the schedule")}
	}
	if inProgress == 0 && finished > 0 {
		return &wdgStatus{"ok", tr("All downloads finished")}
	}
	return nil
}

// queueItem is one row: the task's name, what it is doing, its progress.
func (s *Server) queueItem(p *auth.Principal, tr func(string, ...string) string, lang string, t *core.Task, ranks map[string]int, now time.Time) wdgItem {
	name := t.Name
	if name == "" {
		name = t.Source
	}
	it := wdgItem{Title: cut(name, 120), Open: Base + "/#task/" + t.Hash}
	frac := func() {
		if t.Size > 0 {
			f := math.Round(t.Progress()*10) / 1000
			it.Progress = &f
		}
	}
	switch t.State {
	case core.StMetadata:
		it.Icon, it.Detail = "download", tr("Getting the file list")
	case core.StChecking:
		it.Icon, it.Detail = "refresh", tr("Checking the files")
		frac()
	case core.StMoving:
		it.Icon, it.Detail = "folder", tr("Moving the files")
	case core.StDownloading:
		it.Icon = "download"
		switch {
		case t.Size <= 0:
			it.Detail = tr("{0} downloaded", fsize(t.DoneBytes))
		case t.ETA() > 0:
			it.Detail = tr("{0} of {1}, {2}", fsize(t.DoneBytes), fsize(t.Size), feta(tr, t.ETA()))
		default:
			it.Detail = tr("{0} of {1}", fsize(t.DoneBytes), fsize(t.Size))
		}
		if t.Size > 0 {
			it.Value = strconv.Itoa(int(t.Progress())) + "%"
		}
		frac()
	case core.StError:
		it.Icon, it.Level = "alert", "bad"
		it.Detail = i18n.T(lang, t.ErrorMsg)
		if it.Detail == "" {
			it.Detail = tr("Failed")
		}
	case core.StQueued:
		it.Icon = "clock"
		switch r := rankOf(ranks, t); {
		case t.SchedPaused:
			it.Detail = tr("Paused by the schedule")
		case r > 0 && r < math.MaxInt:
			it.Detail = tr("Waiting, #{0} in the queue", strconv.Itoa(r))
		default:
			it.Detail = tr("Waiting")
		}
		frac()
	case core.StPaused:
		it.Icon = "pause"
		switch {
		case t.SchedPaused:
			it.Detail = tr("Paused by the schedule")
		case t.WakeTime > now.Unix():
			it.Detail = tr("Paused until {0}", clock(time.Unix(t.WakeTime, 0), now))
		default:
			it.Detail = tr("Paused")
		}
		frac()
	case core.StSeeding:
		it.Icon, it.Detail = "upload", tr("Seeding, ratio {0}", fmt.Sprintf("%.2f", t.Ratio()))
	case core.StDone:
		it.Icon, it.Detail = "check", tr("Finished, {0}", fsize(t.Size))
	}
	if t.Owner != p.User {
		it.Detail = tr("{0}: {1}", t.Owner, it.Detail)
	}
	it.Detail = cut(it.Detail, 120)
	it.Value = cut(it.Value, 24)
	return it
}

func rankOf(ranks map[string]int, t *core.Task) int {
	if r, ok := ranks[t.Hash]; ok && r > 0 {
		return r
	}
	return math.MaxInt
}

// clock is a time of day, with the date when it is not within a day.
func clock(t, now time.Time) string {
	if t.Sub(now) <= 24*time.Hour {
		return t.Format("15:04")
	}
	return t.Format("01-02 15:04")
}

// widgetLang is the language of a widget request: the language chosen in
// QTS (nas_lang), else the browser's (Accept-Language, by QTS's table),
// else Deskpane's own (lang, only en or zh-TW), else English.
func widgetLang(r *http.Request) string {
	if c, err := r.Cookie("nas_lang"); err == nil {
		if v := strings.ToUpper(strings.TrimSpace(c.Value)); v != "" && v != "AUTO" {
			return i18n.Norm(v)
		}
	}
	if al := r.Header.Get("Accept-Language"); al != "" {
		first, _, _ := strings.Cut(al, ",")
		first, _, _ = strings.Cut(first, ";")
		if code := i18n.FromTag(first); code != "" {
			return i18n.Norm(code)
		}
	}
	if code := i18n.FromTag(r.URL.Query().Get("lang")); code != "" {
		return i18n.Norm(code)
	}
	return i18n.Source
}

// cut shortens s to max characters (not bytes), ending in "…".
func cut(s string, max int) string {
	if strings.Count(s, "")-1 <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}

// Sizes, speeds and time left as the web UI writes them (dom.js fsize,
// fspeed, feta).
func fsize(b int64) string {
	const kb, mb, gb, tb = 1 << 10, 1 << 20, 1 << 30, 1 << 40
	f := float64(b)
	switch {
	case b >= tb:
		return fmt.Sprintf("%.2f TB", f/tb)
	case b >= 10*gb:
		return fmt.Sprintf("%.1f GB", f/gb)
	case b >= gb:
		return fmt.Sprintf("%.2f GB", f/gb)
	case b >= 100*mb:
		return fmt.Sprintf("%.0f MB", f/mb)
	case b >= mb:
		return fmt.Sprintf("%.1f MB", f/mb)
	case b >= kb:
		return fmt.Sprintf("%d KB", int64(math.Round(f/kb)))
	}
	return fmt.Sprintf("%d B", max(b, 0))
}

func fspeed(b int64) string {
	if b <= 0 {
		return "0 KB/s"
	}
	return fsize(b) + "/s"
}

func feta(tr func(string, ...string) string, s int64) string {
	switch {
	case s < 60:
		return tr("{0} s left", strconv.FormatInt(s, 10))
	case s < 3600:
		return tr("{0} min left", strconv.FormatInt(s/60, 10))
	case s < 2*86400:
		return tr("{0} h {1} min left", strconv.FormatInt(s/3600, 10), strconv.FormatInt(s%3600/60, 10))
	case s > 30*86400:
		return tr("More than 30 days left")
	}
	return tr("{0} days left", strconv.FormatInt(s/86400, 10))
}

// freeSpace caches the free space of a folder for a minute: the widget asks
// every 15 seconds, for every user who has it on screen.
func (s *Server) freeSpace(dir string, now time.Time) int64 {
	wdgMu.Lock()
	defer wdgMu.Unlock()
	if c, ok := wdgFree[dir]; ok && now.Sub(c.at) < time.Minute {
		return c.free
	}
	free := core.FreeSpace(dir)
	wdgFree[dir] = freeEntry{free, now}
	return free
}

type freeEntry struct {
	free int64
	at   time.Time
}

var (
	wdgMu   sync.Mutex
	wdgFree = map[string]freeEntry{}
	wdgSeen = map[string]string{} // user + counter -> day it was last counted
)

// countWidget counts each widget counter once per user and day, so the
// statistics say how many people have the widget at which size, not how
// often Deskpane refreshes it.
func (s *Server) countWidget(user string, keys ...string) {
	day := time.Now().Format("2006-01-02")
	for _, k := range keys {
		wdgMu.Lock()
		first := wdgSeen[user+"\x00"+k] != day
		wdgSeen[user+"\x00"+k] = day
		wdgMu.Unlock()
		if first {
			Count(k)
		}
	}
}
