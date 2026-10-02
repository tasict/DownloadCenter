package notify

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"downloadcenter/internal/core"
)

// Field is one setting of a channel service.
type Field struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Type     string `json:"type"` // text | url | secret | number
	Required bool   `json:"required"`
	Help     string `json:"help,omitempty"`
	Default  string `json:"default,omitempty"`
}

// ServiceDef describes a built-in service.
type ServiceDef struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Fields    []Field `json:"fields"`
	Ops       string  `json:"ops"` // yes | https | commands | no
	AdminOnly bool    `json:"admin_only,omitempty"`
	Help      string  `json:"help,omitempty"`
}

// Services are the built-in services.
var Services = []ServiceDef{
	{ID: "telegram", Title: "Telegram", Ops: "yes", Fields: []Field{
		{Key: "bot_token", Label: "Bot token", Type: "secret", Required: true, Help: "向 @BotFather 建立機器人後取得"},
		{Key: "chat_id", Label: "通知送到（chat id）", Type: "text", Help: "留空時送給已連結的聊天帳號"},
	}, Help: "可以在頻道裡操作下載；傳送 .torrent 檔也能加入。不需要對外開放 NAS。"},
	{ID: "discord", Title: "Discord", Ops: "no", Fields: []Field{
		{Key: "webhook_url", Label: "Webhook 網址", Type: "secret", Required: true, Help: "頻道設定 › 整合 › Webhook"},
	}},
	{ID: "line", Title: "LINE", Ops: "https", Fields: []Field{
		{Key: "access_token", Label: "Channel access token", Type: "secret", Required: true},
		{Key: "to", Label: "送給（user / group id）", Type: "text", Required: true},
		{Key: "channel_secret", Label: "Channel secret", Type: "secret", Help: "在頻道裡操作下載時需要，用來驗證 LINE 送來的訊息"},
	}, Help: "LINE Notify 已停止服務，這裡使用 Messaging API。要在頻道裡操作下載，NAS 必須能從外部用 HTTPS 連到。"},
	{ID: "slack", Title: "Slack", Ops: "no", Fields: []Field{
		{Key: "webhook_url", Label: "Incoming webhook 網址", Type: "secret", Required: true},
	}},
	{ID: "ntfy", Title: "ntfy", Ops: "no", Fields: []Field{
		{Key: "server", Label: "伺服器", Type: "url", Default: "https://ntfy.sh"},
		{Key: "topic", Label: "主題", Type: "text", Required: true},
		{Key: "token", Label: "存取權杖", Type: "secret"},
	}},
	{ID: "gotify", Title: "Gotify", Ops: "no", Fields: []Field{
		{Key: "server", Label: "伺服器", Type: "url", Required: true},
		{Key: "token", Label: "App token", Type: "secret", Required: true},
	}},
	{ID: "bark", Title: "Bark", Ops: "no", Fields: []Field{
		{Key: "server", Label: "伺服器", Type: "url", Default: "https://api.day.app"},
		{Key: "device_key", Label: "Device key", Type: "secret", Required: true},
	}},
	{ID: "qts", Title: "QTS 通知中心", Ops: "no", AdminOnly: true, Fields: []Field{},
		Help: "寫進 QTS 的系統事件紀錄，由通知中心依它的規則寄送電子郵件、簡訊或推播。"},
	{ID: "webhook", Title: "Webhook", Ops: "commands", Fields: []Field{
		{Key: "url", Label: "網址", Type: "url", Required: true},
		{Key: "secret", Label: "簽章密鑰", Type: "secret", Help: "留空會自動產生，只顯示一次"},
	}, Help: "以 HMAC-SHA256 簽章傳送事件；要從你的服務操作下載，請呼叫 /api/v1/commands。"},
}

func serviceDef(id string) *ServiceDef {
	for i := range Services {
		if Services[i].ID == id {
			return &Services[i]
		}
	}
	return nil
}

// telegramAPI and lineAPI are variables so tests can point them at a local
// server.
var (
	telegramAPI = "https://api.telegram.org"
	lineAPI     = "https://api.line.me"
)

// send delivers a message through a channel. It returns the HTTP status (0
// when none) and an error for failed attempts.
func (s *Service) send(ch *Channel, msg *Message) (int, error) {
	guard := !s.isAdmin(ch.Owner)
	cl := s.client(guard)
	cfg := s.fullConfig(ch)
	switch {
	case ch.Service == "webhook":
		return s.sendWebhook(cl, ch, cfg, msg)
	case strings.HasPrefix(ch.Service, "adapter:"):
		return s.sendAdapter(cl, ch, cfg, msg)
	}
	switch ch.Service {
	case "telegram":
		return s.sendTelegram(cl, ch, cfg, msg)
	case "discord":
		color := 0x0A84FF
		switch msg.Type {
		case "task.completed", "task.seeding_finished", "task.moved":
			color = 0x34C759
		case "task.failed", "disk.low", "engine.down", "notify.disabled":
			color = 0xFF3B30
		}
		embed := map[string]any{"title": truncate(msg.Title, 250), "description": truncate(msg.Body, 4000), "color": color}
		if u := s.uiURL(); u != "" {
			embed["url"] = u
		}
		return postJSON(cl, cfg["webhook_url"], map[string]any{"username": "Download Center", "embeds": []any{embed}}, nil)
	case "slack":
		return postJSON(cl, cfg["webhook_url"], map[string]any{"text": "*" + msg.Title + "*\n" + msg.Body}, nil)
	case "line":
		return s.linePush(cl, cfg, msg.Text(), msg.Buttons)
	case "ntfy":
		server := strings.TrimRight(firstNonEmpty(cfg["server"], "https://ntfy.sh"), "/")
		req, err := http.NewRequest("POST", server+"/"+url.PathEscape(cfg["topic"]), strings.NewReader(msg.Body))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Title", mimeHeader(msg.Title))
		req.Header.Set("Tags", "arrow_down")
		if cfg["token"] != "" {
			req.Header.Set("Authorization", "Bearer "+cfg["token"])
		}
		if u := s.uiURL(); u != "" {
			req.Header.Set("Click", u)
		}
		return do(cl, req)
	case "gotify":
		server := strings.TrimRight(cfg["server"], "/")
		return postJSON(cl, server+"/message?token="+url.QueryEscape(cfg["token"]),
			map[string]any{"title": msg.Title, "message": msg.Body, "priority": 5}, nil)
	case "bark":
		server := strings.TrimRight(firstNonEmpty(cfg["server"], "https://api.day.app"), "/")
		body := map[string]any{"device_key": cfg["device_key"], "title": msg.Title, "body": firstNonEmpty(msg.Body, msg.Title), "group": "Download Center"}
		if u := s.uiURL(); u != "" {
			body["url"] = u
		}
		return postJSON(cl, server+"/push", body, nil)
	case "qts":
		return 0, qtsLog(msg)
	}
	return 0, errors.New("unknown service " + ch.Service)
}

// mimeHeader encodes a non-ASCII header value (RFC 2047), which ntfy accepts.
func mimeHeader(s string) string {
	for _, r := range s {
		if r > 126 {
			return "=?UTF-8?B?" + b64(s) + "?="
		}
	}
	return s
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func postJSON(cl *http.Client, u string, body any, headers map[string]string) (int, error) {
	if u == "" {
		return 0, errors.New("沒有設定網址")
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", u, bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "DownloadCenter/1.0")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return do(cl, req)
}

func do(cl *http.Client, req *http.Request) (int, error) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "DownloadCenter/1.0")
	}
	resp, err := cl.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("HTTP %d %s", resp.StatusCode, strings.TrimSpace(truncate(string(b), 200)))
	}
	return resp.StatusCode, nil
}

// Sign computes the X-DC-Signature header value.
func Sign(secret string, t int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(t, 10) + "."))
	mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", t, hex.EncodeToString(mac.Sum(nil)))
}

// webhookBody is the JSON posted for an event (§3) or a digest.
func webhookBody(msg *Message) []byte {
	var v any
	switch {
	case msg.Event != nil:
		e := *msg.Event
		m := map[string]any{"id": e.ID, "type": e.Type, "time": e.Time, "title": msg.Title}
		if e.Owner != "" {
			m["owner"] = e.Owner
		}
		if e.Task != nil {
			m["task"] = e.Task
		}
		for k, x := range e.Data {
			if _, exists := m[k]; !exists {
				m[k] = x
			}
		}
		v = m
	case msg.Type == "digest":
		v = map[string]any{"type": "digest", "time": time.Now().Format(time.RFC3339), "title": msg.Title, "events": msg.Events}
	default:
		v = map[string]any{"type": msg.Type, "time": time.Now().Format(time.RFC3339), "title": msg.Title, "message": msg.Body}
	}
	b, _ := json.Marshal(v)
	return b
}

func (s *Service) sendWebhook(cl *http.Client, ch *Channel, cfg map[string]string, msg *Message) (int, error) {
	body := webhookBody(msg)
	req, err := http.NewRequest("POST", cfg["url"], bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	typ := msg.Type
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "DownloadCenter/1.0")
	req.Header.Set("X-DC-Event", typ)
	req.Header.Set("X-DC-Delivery", msg.Delivery)
	req.Header.Set("X-DC-Signature", Sign(cfg["secret"], s.now().Unix(), body))
	return do(cl, req)
}

// qtsLog writes the message into the QTS system event log, which the
// Notification Center forwards according to its own rules. Severity:
// 0 information, 1 warning, 2 error.
func qtsLog(msg *Message) error {
	sev := "0"
	switch msg.Type {
	case "task.failed", "disk.low", "engine.down", "notify.disabled", "security.token_rejected":
		sev = "1"
	}
	text := msg.Title
	if msg.Body != "" {
		text += "（" + strings.ReplaceAll(msg.Body, "\n", "，") + "）"
	}
	return exec.Command(qtsLogArgs(sev, text)[0], qtsLogArgs(sev, text)[1:]...).Run()
}

// qtsLogArgs is the log_tool command line (split out for tests).
func qtsLogArgs(sev, text string) []string {
	return []string{"/sbin/log_tool", "-t" + sev, "-uSystem", "-p127.0.0.1", "-mlocalhost", "-a", "[Download Center] " + truncate(text, 400)}
}

// fullConfig merges the stored non-secret config with the secrets.
func (s *Service) fullConfig(ch *Channel) map[string]string {
	out := map[string]string{}
	for k, v := range ch.Config {
		out[k] = v
	}
	for _, f := range s.fields(ch.Service) {
		if f.Type == "secret" {
			out[f.Key] = s.db.Secret("channel:" + ch.ID + ":" + f.Key)
		}
		if out[f.Key] == "" && f.Default != "" {
			out[f.Key] = f.Default
		}
	}
	return out
}

// fields of a service, including adapters.
func (s *Service) fields(service string) []Field {
	if d := serviceDef(service); d != nil {
		return d.Fields
	}
	if id, ok := strings.CutPrefix(service, "adapter:"); ok {
		if a := s.adapter(id); a != nil {
			return a.Fields
		}
	}
	return nil
}

// checkTarget validates a configured URL for the owner (no internal
// addresses for regular users, http(s) only).
func checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("網址格式不正確")
	}
	return nil
}

// TestChannel sends a test message synchronously and records it.
func (s *Service) TestChannel(ch *Channel) (bool, string) {
	e := core.Event{ID: 0, Type: "test", Time: s.now().Format(time.RFC3339), Owner: ch.Owner}
	msg := &Message{Type: "test", Title: Title(e), Body: Body(e), Event: &e, Vars: EventVars(e, s.uiURL(), nil), Delivery: "test-" + newDeliveryID()}
	if ch.Template != "" {
		msg.Body = Render(ch.Template, msg.Vars, nil)
	}
	start := time.Now()
	status, err := s.send(ch, msg)
	st, errText := "ok", ""
	if err != nil {
		st, errText = "failed", truncate(err.Error(), 300)
	}
	s.db.X(`INSERT INTO deliveries (channel_id, event_id, delivery, attempt, next_at, status, http_status, duration_ms, error, time) VALUES (?, 0, ?, 1, 0, ?, ?, ?, ?, ?)`,
		ch.ID, msg.Delivery, st, status, time.Since(start).Milliseconds(), errText, s.now().Unix())
	s.trim(ch.ID)
	return err == nil, errText
}
