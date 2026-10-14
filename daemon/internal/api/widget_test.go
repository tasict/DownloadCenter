package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
)

const gib = 1 << 30

func wtask(id, owner, state string, pos int, mod func(*core.Task)) *core.Task {
	t := &core.Task{Hash: fmt.Sprintf("%040s", id), Owner: owner, Name: "task " + id, State: state, Position: pos, Kind: "url", CreatedAt: int64(pos)}
	if mod != nil {
		mod(t)
	}
	return t
}

func downloading(rate, done, size int64) func(*core.Task) {
	return func(t *core.Task) { t.DownRate, t.DoneBytes, t.Size = rate, done, size }
}

var (
	alice = &auth.Principal{User: "alice", Via: "session"}
	admin = &auth.Principal{User: "admin", Admin: true, AllTasks: true, Via: "session"}
)

// A regular user's document counts their own tasks only; an administrator's
// names the owner of someone else's task.
func TestQueueDocOwnership(t *testing.T) {
	s := fixServer(t)
	now := time.Now()
	tasks := []*core.Task{
		wtask("a1", "alice", core.StDownloading, 1, downloading(3<<20, gib, 2*gib)),
		wtask("b1", "bob", core.StDownloading, 2, downloading(5<<20, gib, 2*gib)),
		wtask("b2", "bob", core.StQueued, 3, nil),
	}
	d := s.queueDoc(alice, "ENG", "m", false, tasks, now)
	if d.Value == nil || d.Value.Text != "3.0 MB/s" || d.Value.Caption != "1 downloading" {
		t.Fatalf("alice hero %+v", d.Value)
	}
	if len(d.Items) != 1 || d.Items[0].Title != "task a1" || strings.Contains(d.Items[0].Detail, "alice") {
		t.Fatalf("alice items %+v", d.Items)
	}
	d = s.queueDoc(admin, "ENG", "m", false, tasks, now)
	if d.Value.Text != "8.0 MB/s" || d.Value.Caption != "2 downloading, 1 waiting" {
		t.Fatalf("admin hero %+v", d.Value)
	}
	if !strings.HasPrefix(d.Items[1].Detail, "bob: ") || strings.HasPrefix(d.Items[0].Detail, "alice") == false {
		t.Fatalf("owner prefixes: %q, %q", d.Items[0].Detail, d.Items[1].Detail)
	}
	if *d.Value.Progress != 0.5 {
		t.Errorf("aggregate progress %v", *d.Value.Progress)
	}
}

// Rows go downloading, failed, waiting, paused (then finished, newest first,
// with show=all), as many as the size has room for.
func TestQueueDocRows(t *testing.T) {
	s := fixServer(t)
	now := time.Now()
	var tasks []*core.Task
	for i := 1; i <= 6; i++ {
		tasks = append(tasks, wtask(fmt.Sprint("d", i), "alice", core.StDownloading, 10+i, downloading(1<<20, 3800<<20, 6100<<20)))
	}
	tasks = append(tasks,
		wtask("p1", "alice", core.StPaused, 1, nil),
		wtask("w1", "alice", core.StQueued, 2, nil),
		wtask("e1", "alice", core.StError, 3, func(t *core.Task) { t.ErrorMsg = "Connection timed out" }),
		wtask("f1", "alice", core.StDone, 4, func(t *core.Task) { t.FinishedAt = 100; t.Size = gib }),
		wtask("f2", "alice", core.StSeeding, 5, func(t *core.Task) { t.FinishedAt = 200; t.UpTotal = gib; t.DownTotal = gib }),
	)
	d := s.queueDoc(alice, "ENG", "m", false, tasks, now)
	if len(d.Items) != 4 || d.Items[0].Title != "task d1" || d.Items[3].Title != "task d4" {
		t.Fatalf("medium rows %+v", d.Items)
	}
	// 3.7 GB of 6.0 GB at 1 MB/s: 2300 s left
	if got := d.Items[0].Detail; got != "3.71 GB of 5.96 GB, 38 min left" {
		t.Errorf("detail %q", got)
	}
	if d.Items[0].Value != "62%" || d.Items[0].Icon != "download" || d.Items[0].Open != "/DownloadCenter/#task/"+tasks[0].Hash {
		t.Errorf("row %+v", d.Items[0])
	}
	d = s.queueDoc(alice, "ENG", "l", true, tasks, now)
	var order []string
	for _, it := range d.Items {
		order = append(order, strings.TrimPrefix(it.Title, "task "))
	}
	if strings.Join(order, " ") != "d1 d2 d3 d4 d5 d6" {
		t.Errorf("large rows %v", order)
	}
	d = s.queueDoc(alice, "ENG", "l", true, tasks[6:], now)
	order = order[:0]
	for _, it := range d.Items {
		order = append(order, strings.TrimPrefix(it.Title, "task ")+"="+it.Icon)
	}
	if strings.Join(order, " ") != "e1=alert w1=clock p1=pause f2=upload f1=check" {
		t.Errorf("show all %v", order)
	}
	if d.Items[0].Level != "bad" || d.Items[0].Detail != "Connection timed out" || d.Items[3].Detail != "Seeding, ratio 1.00" {
		t.Errorf("rows %+v", d.Items)
	}
	if d.Value != nil || d.Refresh != 60 || d.Open != "/DownloadCenter/#tasks/error" {
		t.Errorf("idle document: value %+v, refresh %d, open %s", d.Value, d.Refresh, d.Open)
	}
	if d := s.queueDoc(alice, "ENG", "s", false, tasks, now); len(d.Items) != 3 || d.Refresh != 15 || d.Open != "/DownloadCenter/#tasks/error" {
		t.Errorf("small: %d rows, refresh %d, open %s", len(d.Items), d.Refresh, d.Open)
	}
}

// One status sentence, the most important one.
func TestQueueDocStatus(t *testing.T) {
	s := fixServer(t)
	now := time.Now()
	failed := func(n int) []*core.Task {
		var l []*core.Task
		for i := 0; i < n; i++ {
			l = append(l, wtask(fmt.Sprint("e", i), "alice", core.StError, i, nil))
		}
		return l
	}
	if st := s.queueDoc(alice, "ENG", "m", false, failed(2), now).Status; st == nil || st.Level != "bad" || st.Text != "2 downloads failed" {
		t.Errorf("two failed: %+v", st)
	}
	if st := s.queueDoc(alice, "TCH", "m", false, failed(1), now).Status; st == nil || st.Text == "1 download failed" {
		t.Errorf("not translated: %+v", st)
	}
	done := []*core.Task{wtask("f", "alice", core.StDone, 1, nil)}
	d := s.queueDoc(alice, "ENG", "m", false, done, now)
	if d.Status == nil || d.Status.Level != "ok" || d.Status.Text != "All downloads finished" || len(d.Items) != 0 {
		t.Errorf("all finished: %+v %+v", d.Status, d.Items)
	}
	if d := s.queueDoc(alice, "ENG", "m", false, nil, now); d.Status != nil || d.Empty != "No downloads yet" {
		t.Errorf("no tasks: %+v %q", d.Status, d.Empty)
	}

	// The schedule: off now, back to full speed at the next hour
	st := s.M.Settings()
	st.Schedule.Enabled = true
	for i := range st.Schedule.Days {
		st.Schedule.Days[i] = strings.Repeat("1", 24)
	}
	day := (int(now.Weekday()) + 6) % 7
	b := []byte(st.Schedule.Days[day])
	b[now.Hour()] = '0'
	st.Schedule.Days[day] = string(b)
	if err := s.M.SaveSettings(st); err != nil {
		t.Fatal(err)
	}
	if now.Hour() < 23 {
		want := "Paused by the schedule until " + now.Truncate(time.Hour).Add(time.Hour).Format("15:04")
		if st := s.queueDoc(alice, "ENG", "s", false, nil, now).Status; st == nil || st.Level != "warn" || st.Text != want {
			t.Errorf("schedule: %+v, want %q", st, want)
		}
	}
	if st := s.queueDoc(alice, "ENG", "m", false, failed(2), now).Status; st == nil || st.Level != "bad" {
		t.Errorf("failed before the schedule: %+v", st)
	}
	// Limited speed is left out in the small size
	b[now.Hour()] = '2'
	st.Schedule.Days[day] = string(b)
	s.M.SaveSettings(st)
	if st := s.queueDoc(alice, "ENG", "s", false, nil, now).Status; st != nil {
		t.Errorf("limited in small: %+v", st)
	}
	if st := s.queueDoc(alice, "ENG", "l", false, nil, now).Status; st == nil || !strings.HasPrefix(st.Text, "Limited speed") {
		t.Errorf("limited in large: %+v", st)
	}

	// Low on space: below the threshold, named as a shared folder
	st.Schedule.Enabled = false
	st.DiskLowMB = 1 << 40 // more than any disk
	st.TempDir = t.TempDir()
	if err := s.M.SaveSettings(st); err != nil {
		t.Fatal(err)
	}
	if st := s.queueDoc(admin, "ENG", "m", false, nil, now).Status; st == nil || st.Level != "warn" || !strings.HasPrefix(st.Text, "Low on space: ") {
		t.Errorf("low on space: %+v", st)
	}
}

// Texts are cut to Deskpane's lengths, by characters.
func TestQueueDocLengths(t *testing.T) {
	s := fixServer(t)
	long := strings.Repeat("下", 300)
	d := s.queueDoc(alice, "ENG", "m", false, []*core.Task{wtask("x", "alice", core.StPaused, 1, func(t *core.Task) { t.Name = long })}, time.Now())
	if n := utf8.RuneCountInString(d.Items[0].Title); n != 120 || !utf8.ValidString(d.Items[0].Title) {
		t.Errorf("title: %d characters", n)
	}
	// A thousand tasks still make a small document
	var tasks []*core.Task
	for i := 0; i < 1000; i++ {
		tasks = append(tasks, wtask(fmt.Sprint(i), "alice", core.StDownloading, i, func(t *core.Task) { t.Name = long; t.Size = gib }))
	}
	b, _ := json.Marshal(s.queueDoc(alice, "ENG", "l", true, tasks, time.Now()))
	if len(b) > 64<<10 {
		t.Errorf("%d bytes", len(b))
	}
}

func TestWidgetLang(t *testing.T) {
	for _, c := range []struct{ cookie, accept, lang, want string }{
		{"JPN", "de-DE", "en", "JPN"},
		{"auto", "de-DE,de;q=0.9", "en", "GER"},
		{"", "", "zh-TW", "TCH"},
		{"", "en-US", "zh-TW", "TCH"},
		{"", "", "", "ENG"},
		{"CZE", "", "", "ENG"},
		{"ESM", "", "", "SPA"},
	} {
		r := httptest.NewRequest("GET", "/x?lang="+c.lang, nil)
		if c.cookie != "" {
			r.Header.Set("Cookie", "nas_lang="+c.cookie)
		}
		if c.accept != "" {
			r.Header.Set("Accept-Language", c.accept)
		}
		if got := widgetLang(r); got != c.want {
			t.Errorf("%+v: %s", c, got)
		}
	}
}

// The document is for the web session only, and counted once a day.
func TestWidgetQueueRoute(t *testing.T) {
	s := fixServer(t)
	s.DevUser = "admin"
	got := counted(t)
	for i := 0; i < 3; i++ {
		if code, out := call(s, "GET", "/widget/queue?size=l&shell=phone&lang=en&theme=dark&show=active", ""); code != 200 || out["open"] != "/DownloadCenter/#tasks" {
			t.Fatalf("session: %d %v", code, out)
		}
	}
	if got["wdg_queue_l"] != 1 || got["wdg_queue_phone"] != 1 || len(got) != 2 {
		t.Errorf("counted %v", got)
	}
	_, out := call(s, "POST", "/tokens", `{"name":"w","scopes":["tasks:read"]}`)
	tok, _ := out["value"].(string)
	if code, out := call(s, "GET", "/widget/queue", "", "Authorization", "Bearer "+tok); code != 403 || out["error"].(map[string]any)["code"] != "session_required" {
		t.Errorf("token: %d %v", code, out)
	}
	s.DevUser = ""
	if code, _ := call(s, "GET", "/widget/queue", ""); code != 401 {
		t.Errorf("signed out: %d", code)
	}
}

// Only a web session can say a task comes from a Deskpane widget.
func TestCallerOfWidget(t *testing.T) {
	r := httptest.NewRequest("POST", "/x", nil)
	r.Header.Set("X-DC-Via", "widget")
	if got := callerOf(&auth.Principal{Via: "session"}, r); got != "Deskpane" {
		t.Errorf("session: %q", got)
	}
	if got := callerOf(&auth.Principal{Via: "token", Token: &auth.Token{Name: "bot"}}, r); got != "API: bot" {
		t.Errorf("token: %q", got)
	}
	if got := callerOf(&auth.Principal{Via: "session"}, httptest.NewRequest("POST", "/x", nil)); got != "Download Center" {
		t.Errorf("plain: %q", got)
	}
}

func TestFormats(t *testing.T) {
	tr := func(k string, a ...string) string {
		for i, v := range a {
			k = strings.ReplaceAll(k, fmt.Sprintf("{%d}", i), v)
		}
		return k
	}
	for b, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1536: "2 KB", 5 << 20: "5.0 MB", 150 << 20: "150 MB", 3 * gib / 2: "1.50 GB", 12 * gib: "12.0 GB", 3 << 40: "3.00 TB"} {
		if got := fsize(b); got != want {
			t.Errorf("fsize(%d) = %q, want %q", b, got, want)
		}
	}
	if fspeed(0) != "0 KB/s" || fspeed(12_400_000) != "11.8 MB/s" {
		t.Errorf("fspeed %q %q", fspeed(0), fspeed(12_400_000))
	}
	for s, want := range map[int64]string{42: "42 s left", 600: "10 min left", 7300: "2 h 1 min left", 3 * 86400: "3 days left", 40 * 86400: "More than 30 days left"} {
		if got := feta(tr, s); got != want {
			t.Errorf("feta(%d) = %q, want %q", s, got, want)
		}
	}
	if cut("abcdef", 4) != "abc…" || cut("abc", 4) != "abc" {
		t.Error("cut")
	}
}
