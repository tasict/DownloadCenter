package i18n

import "testing"

// withDicts installs dictionaries for one test and drops them afterwards, so
// that the shipped ones load again.
func withDicts(t *testing.T, m map[string]map[string]string) {
	for lang, d := range m {
		setDict(lang, d)
	}
	t.Cleanup(func() {
		mu.Lock()
		for lang := range m {
			delete(dicts, lang)
		}
		legacy, legacySrc = nil, nil
		mu.Unlock()
	})
}

func TestT(t *testing.T) {
	withDicts(t, map[string]map[string]string{
		"ZZZ": {
			"Task not found":                  "Tâche introuvable",
			"Error: {0}":                      "Erreur : {0}",
			"{0}: {1}":                        "{0} : {1}",
			"Connection timed out":            "Délai dépassé",
			"Connection timed out ({0})":      "Délai dépassé ({0})",
			"Cannot add user {0}: {1}":        "Impossible d'ajouter {0} : {1}",
			"Keep at least one administrator": "Gardez au moins un administrateur",
			"Empty translation":               "",
		},
		"TCH": {
			"Task not found":                  "找不到這個任務",
			"Error: {0}":                      "錯誤：{0}",
			"{0}: {1}":                        "{0}：{1}",
			"Connection timed out":            "連線逾時",
			"Connection timed out ({0})":      "連線逾時（{0}）",
			"Keep at least one administrator": "至少要保留一位系統管理者",
			"Cannot add user {0}: {1}":        "無法加入使用者 {0}：{1}",
		},
	})
	cases := []struct{ lang, in, want string }{
		{"ZZZ", "Task not found", "Tâche introuvable"},
		// a template with a nested message that is itself a template
		{"ZZZ", "Error: Connection timed out (timeout after 30s)", "Erreur : Délai dépassé (timeout after 30s)"},
		// the template with more fixed text wins over the generic one
		{"ZZZ", "Cannot add user bob: Keep at least one administrator", "Impossible d'ajouter bob : Gardez au moins un administrateur"},
		{"ZZZ", "alice: Keep at least one administrator", "alice : Gardez au moins un administrateur"},
		// a punctuation-only template needs a part that translates: raw errors keep their own punctuation
		{"ZZZ", "dial tcp: lookup example.com: no such host", "dial tcp: lookup example.com: no such host"},
		{"TCH", "dial tcp: lookup example.com: no such host", "dial tcp: lookup example.com: no such host"},
		// unknown text passes unchanged, empty translations are ignored
		{"ZZZ", "No such message", "No such message"},
		{"ZZZ", "Empty translation", "Empty translation"},
		// English is the source: nothing to look up
		{"", "Task not found", "Task not found"},
		{"ENG", "Task not found", "Task not found"},
		{"TCH", "Task not found", "找不到這個任務"},
		// messages stored by 1.0.x are Traditional Chinese
		{"ZZZ", "錯誤：連線逾時（timeout after 30s）", "Erreur : Délai dépassé (timeout after 30s)"},
		{"", "找不到這個任務", "Task not found"},
		{"ENG", "alice：至少要保留一位系統管理者", "alice: Keep at least one administrator"},
		{"TCH", "找不到這個任務", "找不到這個任務"},
		{"ZZZ", "沒有這個訊息", "沒有這個訊息"},
		// a name in another script inside an English message is not taken for an old message
		{"TCH", "Cannot add user 小明: Keep at least one administrator", "無法加入使用者 小明：至少要保留一位系統管理者"},
		{"TCH", "小明: Keep at least one administrator", "小明：至少要保留一位系統管理者"},
	}
	for _, c := range cases {
		if got := T(c.lang, c.in); got != c.want {
			t.Errorf("T(%q, %q) = %q, want %q", c.lang, c.in, got, c.want)
		}
	}
	if Norm("esm") != "SPA" || Norm(" eng ") != "ENG" || Norm("") != "ENG" || Norm("tch") != "TCH" || Norm("XYZ") != "ENG" {
		t.Error("Norm")
	}
}
