package i18n

import "testing"

// The shipped dictionaries: a few messages of each kind must translate.
func TestShippedDictionaries(t *testing.T) {
	cases := []struct{ lang, in, want string }{
		{"ENG", "找不到這個任務", "Task not found"},
		{"ENG", "錯誤：找不到這個任務", "Error: Task not found"},
		{"JPN", "找不到這個任務", "このタスクが見つかりません"},
		{"ESM", "找不到這個任務", "No se encontró la tarea"},
		{"TCH", "找不到這個任務", "找不到這個任務"},
		{"CZE", "找不到這個任務", "Task not found"},
	}
	for _, c := range cases {
		if got := T(c.lang, c.in); got != c.want {
			t.Errorf("T(%s, %q) = %q, want %q", c.lang, c.in, got, c.want)
		}
	}
}
