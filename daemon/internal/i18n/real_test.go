package i18n

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
)

// The shipped dictionaries: a few messages of each kind must translate.
func TestShippedDictionaries(t *testing.T) {
	cases := []struct{ lang, in, want string }{
		{"", "Task not found", "Task not found"},
		{"ENG", "Error: Task not found", "Error: Task not found"},
		{"JPN", "Task not found", "このタスクが見つかりません"},
		{"ESM", "Task not found", "No se encontró la tarea"},
		{"TCH", "Task not found", "找不到這個任務"},
		{"TCH", "Error: Task not found", "錯誤：找不到這個任務"},
		{"CZE", "Task not found", "Task not found"},
		// written by 1.0.x
		{"", "找不到這個任務", "Task not found"},
		{"ENG", "錯誤：找不到這個任務", "Error: Task not found"},
		{"JPN", "找不到這個任務", "このタスクが見つかりません"},
		{"TCH", "找不到這個任務", "找不到這個任務"},
		{"JPN", "dial tcp: lookup example.com: no such host", "dial tcp: lookup example.com: no such host"},
	}
	for _, c := range cases {
		if got := T(c.lang, c.in); got != c.want {
			t.Errorf("T(%s, %q) = %q, want %q", c.lang, c.in, got, c.want)
		}
	}
}

// Every UI string and backend message (source.json, written by
// `go run ./cmd/mklang keys ..`) is translated into every language, with
// the same placeholders.
func TestDictionariesComplete(t *testing.T) {
	var src struct{ UI, Backend []string }
	b, err := os.ReadFile("source.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &src); err != nil {
		t.Fatal(err)
	}
	keys := append(src.UI, src.Backend...)
	langs, _ := files.ReadDir("lang")
	if len(langs) < 12 {
		t.Fatalf("%d dictionaries", len(langs))
	}
	placeholders := func(s string) string {
		p := phRe.FindAllString(s, -1)
		sort.Strings(p)
		return strings.Join(p, " ")
	}
	for _, f := range langs {
		lang := strings.TrimSuffix(f.Name(), ".json")
		d := rawDict(lang)
		var missing, wrong []string
		for _, k := range keys {
			v, ok := d[k]
			if !ok || v == "" {
				missing = append(missing, k)
			} else if placeholders(v) != placeholders(k) {
				wrong = append(wrong, k)
			}
		}
		if len(missing) > 0 {
			t.Errorf("%s: %d untranslated, e.g. %q", lang, len(missing), missing[0])
		}
		if len(wrong) > 0 {
			t.Errorf("%s: %d translations with other placeholders, e.g. %q", lang, len(wrong), wrong[0])
		}
	}
}
