package i18n

import "testing"

func TestT(t *testing.T) {
	setDict("ZZZ", map[string]string{
		"找不到這個任務":         "Task not found",
		"錯誤：{0}":          "Error: {0}",
		"{0}：{1}":         "{0}: {1}",
		"連線逾時":            "Connection timed out",
		"連線逾時（{0}）":       "Connection timed out ({0})",
		"無法加入使用者 {0}：{1}": "Cannot add user {0}: {1}",
		"至少要保留一位系統管理者":    "Keep at least one administrator",
		"空的翻譯":            "",
	})
	cases := []struct{ in, want string }{
		{"找不到這個任務", "Task not found"},
		// template with a nested message that is itself a template
		{"錯誤：連線逾時（timeout after 30s）", "Error: Connection timed out (timeout after 30s)"},
		// the template with more fixed text wins over the generic one
		{"無法加入使用者 bob：至少要保留一位系統管理者", "Cannot add user bob: Keep at least one administrator"},
		{"alice：至少要保留一位系統管理者", "alice: Keep at least one administrator"},
		// unknown text and non-CJK text pass unchanged
		{"沒有這個訊息", "沒有這個訊息"},
		{"HTTP 503 Service Unavailable", "HTTP 503 Service Unavailable"},
		// empty translations are ignored
		{"空的翻譯", "空的翻譯"},
	}
	for _, c := range cases {
		if got := T("ZZZ", c.in); got != c.want {
			t.Errorf("T(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, lang := range []string{"", "TCH", "tch"} {
		if got := T(lang, "找不到這個任務"); got != "找不到這個任務" {
			t.Errorf("T(%q) changed the source text: %q", lang, got)
		}
	}
	if Norm("esm") != "SPA" || Norm(" eng ") != "ENG" || Norm("TCH") != "" {
		t.Error("Norm")
	}
}
