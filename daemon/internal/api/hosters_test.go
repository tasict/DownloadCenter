package api

import (
	"fmt"
	"testing"

	"downloadcenter/internal/core"
)

type fakeVerifier struct{}

func (fakeVerifier) Verify(*core.Account, string) (map[string]any, error) {
	return map[string]any{}, nil
}
func (fakeVerifier) Services() []map[string]any {
	return []map[string]any{
		{"id": "1fichier", "title": "1fichier", "secret_label": "API key"},
		{"id": "gdrive", "title": "Google Drive", "no_account": true},
	}
}

// Services without accounts are offered to the add dialog (/me) but not
// to the account form (/accounts).
func TestNoAccountServices(t *testing.T) {
	old := Verifier
	Verifier = fakeVerifier{}
	t.Cleanup(func() { Verifier = old })
	s := fixServer(t)
	s.DevUser = "admin"
	ids := func(v any) string {
		var l []string
		list, _ := v.([]any)
		for _, x := range list {
			m, _ := x.(map[string]any)
			l = append(l, fmt.Sprint(m["id"]))
		}
		return fmt.Sprint(l)
	}
	if code, me := call(s, "GET", "/me", ""); code != 200 || ids(me["hosters"]) != "[1fichier gdrive]" {
		t.Errorf("/me hosters: %d %v", code, me["hosters"])
	}
	if code, acc := call(s, "GET", "/accounts", ""); code != 200 || ids(acc["services"]) != "[1fichier]" {
		t.Errorf("/accounts services: %d %v", code, acc["services"])
	}
}
