package notify

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"downloadcenter/internal/core"
	"downloadcenter/internal/i18n"
	"downloadcenter/internal/qts"
)

// zh puts a backend message (written in English, or in Traditional Chinese by
// 1.0.x) into the language of the chat replies and notifications, which are
// Traditional Chinese.
func zh(msg string) string { return i18n.T("TCH", msg) }

// EventTypes lists every event type of INTEGRATION.md §3.
var EventTypes = []string{
	"task.added", "task.started", "task.paused", "task.resumed", "task.completed", "task.seeding_finished",
	"task.moved", "task.failed", "task.removed", "task.merged", "task.source_switched", "account.expiring",
	"queue.idle", "disk.low", "schedule.changed", "engine.down", "engine.up",
	"security.token_created", "security.token_rejected",
}

// DefaultEvents are selected for a new channel when none are given.
var DefaultEvents = []string{"task.completed", "task.failed", "disk.low"}

var modeNames = map[string]string{"full": "全速", "limited": "限速", "off": "暫停"}

// Title is the one-line headline of an event.
func Title(e core.Event) string {
	name := ""
	if e.Task != nil {
		name = e.Task.Name
		if name == "" {
			name = e.Task.ID
		}
	}
	str := func(k string) string {
		if e.Data == nil {
			return ""
		}
		if v, ok := e.Data[k].(string); ok {
			return v
		}
		return ""
	}
	switch e.Type {
	case "task.added":
		return "已加入：" + name
	case "task.started":
		return "開始下載：" + name
	case "task.paused":
		return "已暫停：" + name
	case "task.resumed":
		return "已繼續：" + name
	case "task.completed":
		return "下載完成：" + name
	case "task.seeding_finished":
		return "做種完成：" + name
	case "task.moved":
		return "已搬移：" + name
	case "task.failed":
		return "下載失敗：" + name
	case "task.removed":
		return "已刪除：" + name
	case "task.merged":
		return "已合併來源：" + name
	case "task.source_switched":
		return "已切換來源：" + name
	case "account.expiring":
		return "網站帳號即將到期或流量用完"
	case "queue.idle":
		return "所有下載都已完成"
	case "disk.low":
		return "剩餘空間不足：" + str("folder")
	case "schedule.changed":
		m := modeNames[str("mode")]
		if m == "" {
			m = str("mode")
		}
		return "排程切換為" + m
	case "engine.down":
		return "下載引擎停止回應：" + str("engine")
	case "engine.up":
		return "下載引擎已恢復：" + str("engine")
	case "security.token_created":
		return "已建立存取權杖：" + str("name")
	case "security.token_rejected":
		return "拒絕了無效的存取權杖（" + str("ip") + "）"
	case "notify.disabled":
		return "通知頻道已停用：" + str("channel")
	case "test":
		return "Download Center 測試訊息"
	}
	if name != "" {
		return e.Type + "：" + name
	}
	return e.Type
}

// Body is the detail text of an event (may be empty).
func Body(e core.Event) string {
	var lines []string
	if e.Task != nil {
		var parts []string
		if e.Task.Size > 0 {
			parts = append(parts, SizeH(e.Task.Size))
		}
		if e.Type == "task.completed" && e.Task.Duration > 0 {
			parts = append(parts, "花了 "+DurationH(e.Task.Duration))
		}
		if len(parts) > 0 {
			lines = append(lines, strings.Join(parts, "，"))
		}
		if e.Task.Folder != "" && (e.Type == "task.completed" || e.Type == "task.moved" || e.Type == "task.seeding_finished") {
			lines = append(lines, "位置："+e.Task.Folder)
		}
	}
	if e.Type == "task.failed" && e.Data != nil {
		if er, ok := e.Data["error"].(map[string]any); ok {
			if msg, ok := er["message"].(string); ok && msg != "" {
				lines = append(lines, zh(msg))
			}
		}
	}
	if e.Type == "disk.low" && e.Data != nil {
		if f, ok := toInt(e.Data["free"]); ok && f >= 0 {
			lines = append(lines, "剩餘 "+SizeH(f)+"，下載到這裡的任務已暫停")
		}
	}
	if e.Owner != "" && e.Task != nil {
		lines = append(lines, "擁有者："+e.Owner)
	}
	if e.Type == "notify.disabled" && e.Data != nil {
		if m, ok := e.Data["error"].(string); ok && m != "" {
			lines = append(lines, "連續 20 次傳送失敗，最後的錯誤："+zh(m))
		}
	}
	if e.Type == "test" {
		lines = append(lines, "這個頻道設定正確。")
	}
	return strings.Join(lines, "\n")
}

// Text is Title plus Body.
func Text(e core.Event) string {
	b := Body(e)
	if b == "" {
		return Title(e)
	}
	return Title(e) + "\n" + b
}

func toInt(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	}
	return 0, false
}

// SizeH formats bytes with 1024 steps.
func SizeH(b int64) string {
	if b < 1024 {
		return fmt.Sprintf("%d B", b)
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	f := float64(b) / 1024
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if f >= 100 {
		return fmt.Sprintf("%.0f %s", f, units[i])
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

// SpeedH formats bytes/s.
func SpeedH(b int64) string {
	if b <= 0 {
		return "0 KB/s"
	}
	return SizeH(b) + "/s"
}

// DurationH formats seconds in zh-TW.
func DurationH(s int64) string {
	if s < 60 {
		return fmt.Sprintf("%d 秒", s)
	}
	if s < 3600 {
		return fmt.Sprintf("%d 分 %d 秒", s/60, s%60)
	}
	if s < 86400 {
		return fmt.Sprintf("%d 小時 %d 分", s/3600, (s%3600)/60)
	}
	return fmt.Sprintf("%d 天 %d 小時", s/86400, (s%86400)/3600)
}

// --- templates ({{event.title}}, {{task.size_h}}, {{fields.url}} ...) ---

var tplRe = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.]+)\s*\}\}`)

// Vars are the values a template can reference.
type Vars map[string]string

// EventVars builds the template variables of an event.
func EventVars(e core.Event, uiURL string, fields map[string]string) Vars {
	v := Vars{
		"event.id":    strconv.FormatInt(e.ID, 10),
		"event.type":  e.Type,
		"event.time":  e.Time,
		"event.title": Title(e),
		"event.body":  Body(e),
		"event.text":  Text(e),
		"owner":       e.Owner,
		"nas.name":    qts.Hostname(),
		"ui_url":      uiURL,
	}
	if e.Task != nil {
		v["task.id"] = e.Task.ID
		v["task.name"] = e.Task.Name
		v["task.kind"] = e.Task.Kind
		v["task.size"] = strconv.FormatInt(e.Task.Size, 10)
		v["task.size_h"] = SizeH(e.Task.Size)
		v["task.folder"] = e.Task.Folder
		v["task.duration_s"] = strconv.FormatInt(e.Task.Duration, 10)
		v["task.duration"] = v["task.duration_s"]
		v["task.duration_h"] = DurationH(e.Task.Duration)
	}
	for k, val := range e.Data {
		switch x := val.(type) {
		case string:
			v["event.data."+k] = x
		case float64, int64, int, bool:
			v["event.data."+k] = fmt.Sprint(x)
		}
	}
	for k, val := range fields {
		v["fields."+k] = val
	}
	return v
}

// Render substitutes {{name}} placeholders. enc encodes values (nil = raw);
// fields.* are never encoded (they are the configured base values such as
// the target URL). Unknown names become empty.
func Render(tpl string, v Vars, enc func(string) string) string {
	return tplRe.ReplaceAllStringFunc(tpl, func(m string) string {
		name := tplRe.FindStringSubmatch(m)[1]
		val := v[name]
		if enc != nil && !strings.HasPrefix(name, "fields.") {
			return enc(val)
		}
		return val
	})
}

// RenderURL renders a URL template with percent-encoded values.
func RenderURL(tpl string, v Vars) string { return Render(tpl, v, url.QueryEscape) }

// RenderHeader renders a header value (no line breaks).
func RenderHeader(tpl string, v Vars) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(Render(tpl, v, nil))
}

// RenderJSON renders every string inside a JSON value; the result is then
// marshalled, so substituted values are JSON-escaped.
func RenderJSON(body any, v Vars) any {
	switch b := body.(type) {
	case string:
		return Render(b, v, nil)
	case map[string]any:
		out := map[string]any{}
		for k, x := range b {
			out[k] = RenderJSON(x, v)
		}
		return out
	case []any:
		out := make([]any, len(b))
		for i, x := range b {
			out[i] = RenderJSON(x, v)
		}
		return out
	}
	return body
}
