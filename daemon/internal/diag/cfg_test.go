package diag

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCfg(t *testing.T) {
	p := filepath.Join(t.TempDir(), "platform.conf")
	os.WriteFile(p, []byte("[Platform]\nX = 1\nDISPLAY_NAME = TS-664\n[System]\nVersion = 5.2.11\n"), 0600)
	if got := cfg(p, "", "DISPLAY_NAME"); got != "TS-664" {
		t.Errorf("no section: %q", got)
	}
	if got := cfg(p, "System", "Version"); got != "5.2.11" {
		t.Errorf("section: %q", got)
	}
	if got := cfg(p, "System", "X"); got != "" {
		t.Errorf("other section: %q", got)
	}
}
