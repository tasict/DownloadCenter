package phpbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// spec: without QTS's PHP nothing runs, the reason says why, and once PHP
// is there the next check finds it without a restart.
func TestNoPHPRunsNothing(t *testing.T) {
	old := phpPath
	defer func() { phpPath = old }()
	phpPath = filepath.Join(t.TempDir(), "php")
	r := New(Config{Dir: filepath.Join(t.TempDir(), "phpbox")})
	if ok, code, reason := r.Available(); ok || code != CodeUnchecked || reason != ReasonUnchecked {
		t.Errorf("before a check: %v %s %s", ok, code, reason)
	}
	ok, code, reason := r.Check(context.Background())
	if ok || code != CodePHPMissing || reason != ReasonPHPMissing {
		t.Fatalf("%v %s %s", ok, code, reason)
	}
	p := Plugin{Format: FormatDLM, Dir: t.TempDir(), Entry: "x.php", Class: "X"}
	var u *Unavailable
	if _, err := r.Search(context.Background(), p, Query{Keyword: "x"}); !errors.As(err, &u) || u.Code != CodePHPMissing || u.Reason != ReasonPHPMissing {
		t.Errorf("search: %v", err)
	}
	if _, _, err := r.Verify(context.Background(), p, Account{"a", "b"}); !errors.As(err, &u) || u.Code != CodePHPMissing {
		t.Errorf("verify: %v", err)
	}
	if r.starts.Load() != 0 || r.plugins.Load() != 0 {
		t.Errorf("%d helper starts, %d plugin runs", r.starts.Load(), r.plugins.Load())
	}
	// PHP without curl
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	os.MkdirAll(filepath.Join(dir, "modules"), 0o755)
	os.WriteFile(filepath.Join(dir, "bin", "php"), []byte("#!/bin/sh\n"), 0o755)
	phpPath = filepath.Join(dir, "bin", "php")
	if ok, code, reason := r.Check(context.Background()); ok || code != CodePHPBroken || reason != ReasonPHPNoCurl || r.starts.Load() != 0 {
		t.Errorf("no curl: %v %s %s", ok, code, reason)
	}
	// Back to the real path: the next check runs again
	phpPath = old
	ok, code, _ = r.Check(context.Background())
	if _, err := os.Stat(PHPPath); err != nil || os.Geteuid() != 0 || runtime.GOOS != "linux" {
		if ok {
			t.Error("available without PHP, root or Linux")
		}
		return
	}
	if !ok {
		t.Errorf("with PHP back: %s", code)
	}
}
