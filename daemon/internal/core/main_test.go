package core

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"downloadcenter/internal/qts"
)

// TestMain keeps every test away from the NAS's real folders: no shared
// folder or home folder of the NAS is seen by the housekeeping, and a
// task's private staging folder is made beside its own (scratch)
// destination, unless a test sets up its own.
func TestMain(m *testing.M) {
	shareList = func() []qts.Share { return nil }
	homesRoot = func() string { return "" }
	var mu sync.Mutex
	bases := map[string]bool{}
	stagingBase = func(dir string) (string, error) {
		if !filepath.IsAbs(dir) {
			return "", errNoStaging
		}
		b := filepath.Dir(filepath.Clean(dir))
		mu.Lock()
		bases[b] = true
		mu.Unlock()
		return b, nil
	}
	isStagingBase = func(p string) bool {
		mu.Lock()
		defer mu.Unlock()
		return bases[filepath.Clean(p)]
	}
	os.Exit(m.Run())
}
