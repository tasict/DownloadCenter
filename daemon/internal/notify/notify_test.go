package notify

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"downloadcenter/internal/api"
	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/engine"
	"downloadcenter/internal/netutil"
	"downloadcenter/internal/store"
)

// stubBT stands in for libtorrent: the manager loop does not run in these
// tests, only the engine name is used.
type stubBT struct{ engine.Engine }

func (stubBT) Name() string { return "libtorrent" }

// stubURL stands in for the URL engine: the manager loop does not run in
// these tests, only the protocol check is used.
type stubURL struct{ engine.Engine }

func (stubURL) Name() string         { return "builtin" }
func (stubURL) Supports(string) bool { return true }

type env struct {
	s   *Service
	m   *core.Manager
	au  *auth.Service
	now time.Time
	adm map[string]bool
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "dc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	m := core.New(db, dir)
	m.URL = stubURL{}
	m.Engines["libtorrent"] = stubBT{}
	au := auth.New(db)
	e := &env{m: m, au: au, now: time.Date(2026, 10, 2, 12, 0, 0, 0, time.Local), adm: map[string]bool{}}
	s := New(m, au, dir)
	s.now = func() time.Time { return e.now }
	s.isAdmin = func(u string) bool { return e.adm[u] }
	s.client = func(guard bool) *http.Client { return netutil.Client(5*time.Second, guard, "") }
	e.s = s
	for _, u := range []string{"boss", "alice", "bob"} {
		db.X(`INSERT INTO users (name, role, qts_admin, created_at) VALUES (?, ?, 0, 0)`, u, map[bool]string{true: "admin", false: "user"}[u == "boss"])
	}
	e.adm["boss"] = true
	return e
}

func (e *env) channel(t *testing.T, owner, service string, cfg map[string]string, secrets map[string]string) *Channel {
	t.Helper()
	c := &Channel{ID: auth.RandomID("ch_", 8), Owner: owner, Service: service, Name: service, Config: cfg, Enabled: true,
		Scope: "own", Events: []string{}, State: map[string]any{}, CreatedAt: e.now.Unix()}
	if err := e.s.saveChannel(c); err != nil {
		t.Fatal(err)
	}
	e.s.storeSecrets(c.ID, secrets)
	return c
}

func (e *env) emit(typ, owner string) core.Event {
	ev := e.m.Emit(core.Event{Type: typ, Owner: owner, Task: &core.EventTask{ID: "c9e1aa", Name: `debian "13".iso`, Kind: "url", Size: 663748608, Folder: "Download", Duration: 212}})
	e.s.Dispatch(ev)
	return ev
}

type recorder struct {
	mu     sync.Mutex
	reqs   []*http.Request
	bodies [][]byte
	status int
}

func (r *recorder) handler(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	r.bodies = append(r.bodies, b)
	st := r.status
	r.mu.Unlock()
	if st == 0 {
		st = 200
	}
	w.WriteHeader(st)
	w.Write([]byte(`{"ok":true,"result":{}}`))
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reqs)
}

func TestWebhookSignature(t *testing.T) {
	e := newEnv(t)
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()
	e.channel(t, "boss", "webhook", map[string]string{"url": srv.URL}, map[string]string{"secret": "s3cret"})
	ev := e.emit("task.completed", "boss")
	e.s.ProcessDeliveries()
	if rec.count() != 1 {
		t.Fatalf("want 1 request, got %d", rec.count())
	}
	req, body := rec.reqs[0], rec.bodies[0]
	if req.Header.Get("X-DC-Event") != "task.completed" || req.Header.Get("X-DC-Delivery") == "" {
		t.Fatalf("headers: %v", req.Header)
	}
	sig := req.Header.Get("X-DC-Signature")
	parts := strings.Split(sig, ",")
	ts := strings.TrimPrefix(parts[0], "t=")
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	if parts[1] != "v1="+hex.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("bad signature %s", sig)
	}
	var got map[string]any
	json.Unmarshal(body, &got)
	if got["type"] != "task.completed" || int64(got["id"].(float64)) != ev.ID || got["task"] == nil {
		t.Fatalf("body %s", body)
	}
}

func TestRetryBackoffAndDisable(t *testing.T) {
	e := newEnv(t)
	rec := &recorder{status: 500}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()
	c := e.channel(t, "boss", "webhook", map[string]string{"url": srv.URL}, map[string]string{"secret": "x"})
	e.emit("task.failed", "boss")
	e.s.ProcessDeliveries()
	steps := []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour}
	for i, d := range steps {
		var next int64
		var st string
		e.s.db.QueryRow(`SELECT next_at, status FROM deliveries WHERE channel_id = ?`, c.ID).Scan(&next, &st)
		if st != "pending" || next != e.now.Add(d).Unix() {
			t.Fatalf("step %d: status %s next %d want %d", i, st, next, e.now.Add(d).Unix())
		}
		// Not due yet: nothing happens
		e.s.ProcessDeliveries()
		if rec.count() != i+1 {
			t.Fatalf("step %d: early retry", i)
		}
		e.now = e.now.Add(d)
		e.s.ProcessDeliveries()
	}
	var st string
	var attempt int
	e.s.db.QueryRow(`SELECT status, attempt FROM deliveries WHERE channel_id = ?`, c.ID).Scan(&st, &attempt)
	if st != "failed" || attempt != 6 {
		t.Fatalf("final status %s attempt %d", st, attempt)
	}
	// 20 consecutive failures disable the channel
	for i := 0; i < 20; i++ {
		e.emit("task.failed", "boss")
	}
	e.s.ProcessDeliveries()
	if ch := e.s.channel(c.ID); ch.Enabled {
		t.Fatalf("channel should be disabled after %d failures", ch.FailCount)
	}
	found := false
	for _, ev := range e.m.Events(0, 500) {
		if ev.Type == "notify.disabled" && ev.Owner == "boss" {
			found = true
		}
	}
	if !found {
		t.Fatal("no notify.disabled event")
	}
}

func TestSSRFGuardForRegularUsers(t *testing.T) {
	e := newEnv(t)
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()
	c := e.channel(t, "alice", "webhook", map[string]string{"url": srv.URL}, map[string]string{"secret": "x"})
	e.emit("task.completed", "alice")
	e.s.ProcessDeliveries()
	if rec.count() != 0 {
		t.Fatal("a regular user's webhook reached loopback")
	}
	var errText string
	e.s.db.QueryRow(`SELECT error FROM deliveries WHERE channel_id = ?`, c.ID).Scan(&errText)
	if !strings.Contains(errText, "not allowed") {
		t.Fatalf("error %q", errText)
	}
}

func TestVisibilityAndFilter(t *testing.T) {
	e := newEnv(t)
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()
	own := e.channel(t, "boss", "webhook", map[string]string{"url": srv.URL}, map[string]string{"secret": "x"})
	all := e.channel(t, "boss", "webhook", map[string]string{"url": srv.URL}, map[string]string{"secret": "x"})
	all.Scope = "all"
	all.Events = []string{"task.completed"}
	e.s.saveChannel(all)
	e.emit("task.completed", "alice") // only the "all" channel
	e.emit("task.added", "alice")     // filtered out everywhere
	e.emit("engine.down", "")         // admin-only, own channel takes it (no filter)
	count := func(id string) int {
		var n int
		e.s.db.QueryRow(`SELECT COUNT(*) FROM deliveries WHERE channel_id = ?`, id).Scan(&n)
		return n
	}
	if count(own.ID) != 1 || count(all.ID) != 1 {
		t.Fatalf("own %d all %d", count(own.ID), count(all.ID))
	}
}

func TestQuietHours(t *testing.T) {
	loc := time.Local
	at := func(h, m int) time.Time { return time.Date(2026, 10, 2, h, m, 0, 0, loc) }
	end, q := QuietUntil("22:00-07:00", at(23, 30))
	if !q || !end.Equal(time.Date(2026, 10, 3, 7, 0, 0, 0, loc)) {
		t.Fatalf("23:30 -> %v %v", end, q)
	}
	end, q = QuietUntil("22:00-07:00", at(6, 59))
	if !q || !end.Equal(at(7, 0)) {
		t.Fatalf("06:59 -> %v %v", end, q)
	}
	if _, q = QuietUntil("22:00-07:00", at(12, 0)); q {
		t.Fatal("noon is not quiet")
	}
	if _, q = QuietUntil("13:00-14:00", at(13, 30)); !q {
		t.Fatal("13:30 is quiet")
	}
	if _, q = QuietUntil("bogus", at(13, 30)); q {
		t.Fatal("bad spec")
	}
	// A delivery during quiet hours waits for the end
	e := newEnv(t)
	c := e.channel(t, "boss", "webhook", map[string]string{"url": "https://example.invalid/"}, map[string]string{"secret": "x"})
	c.Quiet = "11:00-13:00"
	e.s.saveChannel(c)
	e.emit("task.completed", "boss")
	var next int64
	e.s.db.QueryRow(`SELECT next_at FROM deliveries WHERE channel_id = ?`, c.ID).Scan(&next)
	if next != at(13, 0).Unix() {
		t.Fatalf("next_at %d", next)
	}
}

func TestDigest(t *testing.T) {
	e := newEnv(t)
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()
	c := e.channel(t, "boss", "webhook", map[string]string{"url": srv.URL}, map[string]string{"secret": "x"})
	c.Digest = 10
	e.s.saveChannel(c)
	// Events carry real timestamps; pin "now" to them
	e.now = time.Now()
	for i := 0; i < 3; i++ {
		e.emit("task.completed", "boss")
	}
	e.s.flushDigests()
	e.s.ProcessDeliveries()
	if rec.count() != 0 {
		t.Fatal("digest sent too early")
	}
	e.now = e.now.Add(11 * time.Minute)
	e.s.flushDigests()
	e.s.ProcessDeliveries()
	if rec.count() != 1 {
		t.Fatalf("want one digest, got %d", rec.count())
	}
	var body map[string]any
	json.Unmarshal(rec.bodies[0], &body)
	if body["type"] != "digest" || len(body["events"].([]any)) != 3 {
		t.Fatalf("digest body %s", rec.bodies[0])
	}
	if d := e.s.deliveries(c.ID); len(d) != 1 || d[0].EventType != "digest" || d[0].Status != "ok" {
		t.Fatalf("deliveries %+v", d)
	}
}

func TestTemplates(t *testing.T) {
	ev := core.Event{ID: 7, Type: "task.completed", Owner: "alice", Task: &core.EventTask{ID: "abc", Name: `a "b" & c`, Size: 1536, Duration: 125}}
	v := EventVars(ev, "https://nas/DownloadCenter/", map[string]string{"url": "https://hook.example/x?y=1"})
	if got := Render("{{task.name}}（{{task.size_h}}，{{task.duration_h}}）", v, nil); got != `a "b" & c（1.5 KB，2 分 5 秒）` {
		t.Fatalf("render %q", got)
	}
	if got := RenderURL("{{fields.url}}&n={{task.name}}", v); got != "https://hook.example/x?y=1&n=a+%22b%22+%26+c" {
		t.Fatalf("url %q", got)
	}
	b, _ := json.Marshal(RenderJSON(map[string]any{"text": "{{event.title}}", "n": 3}, v))
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil || back["text"] != `下載完成：a "b" & c` {
		t.Fatalf("json %s", b)
	}
	if RenderHeader("x\r\n{{owner}}", v) != "x  alice" {
		t.Fatal("header newline")
	}
}

func TestAdapter(t *testing.T) {
	e := newEnv(t)
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()
	a, err := ParseManifest([]byte(`{"adapter":"mattermost","title":"Mattermost","fields":[{"key":"url","label":"URL","type":"url","secret":true}],
		"request":{"method":"POST","url":"{{fields.url}}","headers":{"Content-Type":"application/json"},"body":{"text":"{{event.title}}\n{{task.name}}（{{task.size_h}}）"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	st := storedAdapter{Adapter: *a, FieldsJSON: a.Fields}
	m, _ := json.Marshal(st)
	e.s.db.X(`INSERT INTO adapters (id, title, manifest, created_at) VALUES (?, ?, ?, 0)`, a.ID, a.Title, string(m))
	e.channel(t, "boss", "adapter:mattermost", map[string]string{}, map[string]string{"url": srv.URL})
	e.emit("task.completed", "boss")
	e.s.ProcessDeliveries()
	if rec.count() != 1 {
		t.Fatalf("adapter requests %d", rec.count())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.bodies[0], &body); err != nil || !strings.Contains(body["text"], `debian "13".iso（633 MB）`) {
		t.Fatalf("adapter body %s", rec.bodies[0])
	}
	for _, bad := range []string{`{"adapter":"x y","title":"t","request":{"url":"https://a"}}`, `{"adapter":"telegram","title":"t","request":{"url":"https://a"}}`,
		`{"adapter":"ok","title":"t","request":{"url":"file:///etc/passwd"}}`, `{"adapter":"ok","title":"t","request":{"method":"DELETE","url":"https://a"}}`} {
		if _, err := ParseManifest([]byte(bad)); err == nil {
			t.Fatalf("manifest accepted: %s", bad)
		}
	}
}

func TestCommandsAndPermissions(t *testing.T) {
	e := newEnv(t)
	ro, err := e.au.ForChat("alice", []string{"tasks:read"})
	if err != nil {
		t.Fatal(err)
	}
	if r := e.s.Run(ro, "c1", "/add https://example.com/a.iso"); r.OK || !strings.Contains(r.Reply, "tasks:add") {
		t.Fatalf("read-only add: %+v", r)
	}
	if r := e.s.Run(ro, "c1", "/limit 2M"); r.OK || !strings.Contains(r.Reply, "settings:write") {
		t.Fatalf("limit: %+v", r)
	}
	if r := e.s.Run(ro, "c1", "/help"); !r.OK || !strings.Contains(r.Reply, "/list") {
		t.Fatalf("help: %+v", r)
	}
	if r := e.s.Run(ro, "c1", "/list"); !r.OK {
		t.Fatalf("list: %+v", r)
	}
	if r := e.s.Run(ro, "c1", "/nope"); r.OK {
		t.Fatal("unknown command ok")
	}
	if r := e.s.Run(ro, "c1", "/pause 1"); r.OK {
		t.Fatal("pause without scope")
	}
	for in, want := range map[string]int{"2M": 2048, "500K": 500, "1.5MB/s": 1536, "off": 0} {
		if got, err := ParseRate(in); err != nil || got != want {
			t.Fatalf("ParseRate(%s) = %d %v", in, got, err)
		}
	}
	if _, err := ParseRate("fast"); err == nil {
		t.Fatal("ParseRate accepted garbage")
	}
	// Numbered list resolution with an expired context
	e.s.lists["c2"] = listCtx{ids: []string{"x"}, expires: e.now.Add(-time.Second)}
	if _, err := e.s.resolve(ro, "c2", "1"); err == nil || !strings.Contains(err.Error(), "過期") {
		t.Fatalf("expired list: %v", err)
	}
}

func TestPairing(t *testing.T) {
	e := newEnv(t)
	c := e.channel(t, "alice", "telegram", map[string]string{}, map[string]string{"bot_token": "T"})
	p, _ := e.au.FromQTS("alice", false)
	if _, err := e.s.newPair(c, p, nil); err == nil {
		t.Fatal("pairing without operate")
	}
	c.Operate = true
	e.s.saveChannel(c)
	pair, err := e.s.newPair(c, p, []string{"tasks:read", "settings:write"})
	if err != nil || len(pair.Code) != 6 || pair.Command != "/link "+pair.Code {
		t.Fatalf("pair %+v %v", pair, err)
	}
	if _, err := e.s.Link("other", "42", pair.Code); err == nil {
		t.Fatal("code of another channel accepted")
	}
	if r := e.s.chatCommand(c, "tg", "42", "/list"); !strings.Contains(r.Reply, "/link") {
		t.Fatalf("unlinked reply %q", r.Reply)
	}
	if r := e.s.chatCommand(c, "tg", "42", "/link "+pair.Code); !r.OK {
		t.Fatalf("link: %+v", r)
	}
	lp := e.s.linked(c.ID, "42")
	if lp == nil || lp.User != "alice" || !lp.Can("tasks:read") || lp.Can("settings:write") || lp.Can("tasks:add") {
		t.Fatalf("linked principal %+v", lp)
	}
	if _, err := e.s.Link(c.ID, "43", pair.Code); err == nil {
		t.Fatal("code reused")
	}
	pair2, _ := e.s.newPair(c, p, nil)
	e.now = e.now.Add(11 * time.Minute)
	if _, err := e.s.Link(c.ID, "44", pair2.Code); err == nil {
		t.Fatal("expired code accepted")
	}
}

func TestTelegramSendAndLink(t *testing.T) {
	e := newEnv(t)
	var mu sync.Mutex
	var sent []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		if strings.HasSuffix(r.URL.Path, "/sendMessage") && strings.HasPrefix(r.URL.Path, "/botTOK/") {
			sent = append(sent, b)
		}
		mu.Unlock()
		w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer srv.Close()
	old := telegramAPI
	telegramAPI = srv.URL
	defer func() { telegramAPI = old }()
	c := e.channel(t, "boss", "telegram", map[string]string{"chat_id": "99"}, map[string]string{"bot_token": "TOK"})
	c.Operate = true
	e.s.saveChannel(c)
	e.emit("task.failed", "boss")
	e.s.ProcessDeliveries()
	mu.Lock()
	if len(sent) != 1 || sent[0]["chat_id"] != "99" || !strings.Contains(sent[0]["text"].(string), "下載失敗") || sent[0]["reply_markup"] == nil {
		t.Fatalf("sent %+v", sent)
	}
	mu.Unlock()
	// An incoming /link from a chat
	p, _ := e.au.FromQTS("boss", true)
	pair, _ := e.s.newPair(c, p, nil)
	u := tgUpdate{UpdateID: 5, Message: &tgMessage{From: tgUser{ID: 77}, Text: "/link " + pair.Code}}
	u.Message.Chat.ID = 77
	e.s.handleTelegram(c, e.s.fullConfig(c), e.s.client(false), u)
	mu.Lock()
	last := sent[len(sent)-1]
	mu.Unlock()
	if last["chat_id"] != "77" || !strings.Contains(last["text"].(string), "已連結") {
		t.Fatalf("link reply %+v", last)
	}
	if e.s.linked(c.ID, "77") == nil {
		t.Fatal("not linked")
	}
}

func TestLineWebhookSignature(t *testing.T) {
	e := newEnv(t)
	c := e.channel(t, "boss", "line", map[string]string{"to": "U1"}, map[string]string{"access_token": "A", "channel_secret": "S"})
	srv := api.New(e.m, e.au, t.TempDir(), "test")
	e.s.routes(srv)
	body := []byte(`{"events":[]}`)
	for _, tc := range []struct {
		sig  string
		want int
	}{{"bogus", 401}, {LineSignature("S", body), 200}} {
		req := httptest.NewRequest("POST", api.APIBase+"/line/"+c.ID, bytes.NewReader(body))
		req.Header.Set("X-Line-Signature", tc.sig)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatalf("sig %s: %d %s", tc.sig, w.Code, w.Body.String())
		}
	}
}

func TestRESTRoutes(t *testing.T) {
	e := newEnv(t)
	srv := api.New(e.m, e.au, t.TempDir(), "test")
	srv.DevUser = "alice"
	e.s.routes(srv)
	call := func(method, path string, body any) (int, map[string]any) {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, api.APIBase+path, rd)
		req.RemoteAddr = "127.0.0.1:5555"
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	code, out := call("POST", "/webhooks", map[string]any{"url": "https://example.com/hook", "events": []string{"task.completed"}})
	if code != 200 || out["secret"] == "" || out["secret"] == nil {
		t.Fatalf("create webhook %d %v", code, out)
	}
	id := out["channel"].(map[string]any)["id"].(string)
	code, out = call("GET", "/channels", nil)
	if code != 200 || len(out["channels"].([]any)) != 1 || len(out["services"].([]any)) < 8 || len(out["events"].([]any)) != len(EventTypes) {
		t.Fatalf("list %d %v", code, out)
	}
	ch := out["channels"].([]any)[0].(map[string]any)
	if ch["secret_fields_set"].([]any)[0] != "secret" {
		t.Fatalf("secret flag %v", ch)
	}
	if _, has := ch["config"].(map[string]any)["secret"]; has {
		t.Fatal("secret leaked in config")
	}
	// Regular users cannot use the QTS service
	if code, _ = call("POST", "/channels", map[string]any{"service": "qts"}); code != 400 {
		t.Fatalf("qts for regular user: %d", code)
	}
	// Missing required field
	if code, _ = call("POST", "/channels", map[string]any{"service": "discord"}); code != 400 {
		t.Fatalf("discord without url: %d", code)
	}
	code, out = call("POST", "/channels", map[string]any{"service": "telegram", "config": map[string]any{"bot_token": "x"}, "operate": true})
	if code != 200 || out["pair"] == nil {
		t.Fatalf("telegram with operate %d %v", code, out)
	}
	tg := out["channel"].(map[string]any)["id"].(string)
	if code, out = call("POST", "/channels/"+tg+"/pair", nil); code != 200 || len(out["code"].(string)) != 6 {
		t.Fatalf("pair %d %v", code, out)
	}
	if code, _ = call("PATCH", "/channels/"+id, map[string]any{"quiet": "bad"}); code != 400 {
		t.Fatalf("bad quiet accepted: %d", code)
	}
	if code, _ = call("PATCH", "/channels/"+id, map[string]any{"quiet": "22:00-07:00", "digest": 15}); code != 200 {
		t.Fatalf("patch %d", code)
	}
	if code, out = call("GET", "/channels/"+id+"/deliveries", nil); code != 200 {
		t.Fatalf("deliveries %d", code)
	}
	if code, out = call("POST", "/commands", map[string]any{"text": "/help"}); code != 200 || out["ok"] != true {
		t.Fatalf("commands %d %v", code, out)
	}
	if code, _ = call("POST", "/adapters", map[string]any{"manifest": map[string]any{}}); code != 403 {
		t.Fatalf("regular user imported an adapter: %d", code)
	}
	if code, _ = call("DELETE", "/channels/"+id, nil); code != 200 {
		t.Fatalf("delete %d", code)
	}
	if code, _ = call("DELETE", "/channels/"+id, nil); code != 404 {
		t.Fatalf("delete twice %d", code)
	}
}

func TestQTSLogArgs(t *testing.T) {
	a := qtsLogArgs("1", "下載失敗：x")
	if a[0] != "/sbin/log_tool" || a[1] != "-t1" || a[len(a)-1] != "[Download Center] 下載失敗：x" {
		t.Fatalf("%v", a)
	}
}

func TestCommandFlow(t *testing.T) {
	e := newEnv(t)
	p, err := e.au.FromQTS("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	r := e.s.Run(p, "c", "/add https://example.com/files/a.iso\nmagnet:?xt=urn:btih:08ada5a7a6183aae1e09d831df6748d566095a10&dn=Sintel")
	if !r.OK || !strings.Contains(r.Reply, "已加入：a.iso") || !strings.Contains(r.Reply, "Sintel") || r.Task == nil {
		t.Fatalf("add: %+v", r)
	}
	if r = e.s.Run(p, "c", "https://example.com/files/a.iso"); !strings.Contains(r.Reply, "已在清單中") {
		t.Fatalf("duplicate: %+v", r)
	}
	if r = e.s.Run(p, "c", "/list"); !r.OK || !strings.Contains(r.Reply, "1. a.iso") {
		t.Fatalf("list: %+v", r)
	}
	if r = e.s.Run(p, "c", "/pause 1"); !r.OK || !strings.Contains(r.Reply, "已暫停：a.iso") {
		t.Fatalf("pause: %+v", r)
	}
	if r = e.s.Run(p, "c", "/del 2"); !strings.Contains(r.Reply, "confirm") {
		t.Fatalf("del ask: %+v", r)
	}
	if r = e.s.Run(p, "c", "/del 2 confirm"); !r.OK {
		t.Fatalf("del: %+v", r)
	}
	if len(e.m.List()) != 1 {
		t.Fatalf("tasks left %d", len(e.m.List()))
	}
	// Another user does not see alice's tasks
	pb, _ := e.au.FromQTS("bob", false)
	if r = e.s.Run(pb, "c", "/list"); !strings.Contains(r.Reply, "沒有符合") {
		t.Fatalf("bob list: %+v", r)
	}
	if r = e.s.Run(pb, "x", "/pause "+e.m.List()[0].Hash[:8]); r.OK {
		t.Fatal("bob paused alice's task")
	}
}
