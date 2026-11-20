package core

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"downloadcenter/internal/qts"
	"downloadcenter/internal/safefs"
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
	if os.Geteuid() != 0 {
		// Not run by root (a build server): the tests' own folders and files
		// stand for root's, as nothing can be given to root there
		rootUID, rootGID = uint32(os.Geteuid()), uint32(os.Getegid())
		openRootDir = openOwnDir
	}
	os.Exit(m.Run())
}

// openOwnDir is safefs.OpenRootDir for tests not run by root: every
// component must belong to root or to the account running the tests.
func openOwnDir(p string) (*safefs.Dir, error) {
	if !filepath.IsAbs(p) {
		return nil, safefs.ErrName
	}
	d, err := safefs.OpenDir("/")
	if err != nil {
		return nil, err
	}
	for _, c := range append([]string{""}, strings.Split(strings.TrimPrefix(filepath.Clean(p), "/"), "/")...) {
		if c != "" {
			nd, err := d.Sub(c)
			d.Close()
			if err != nil {
				return nil, err
			}
			d = nd
		}
		if st, err := d.Stat(); err != nil || st.Uid != 0 && st.Uid != rootUID {
			d.Close()
			return nil, safefs.ErrOwner
		}
	}
	return d, nil
}

// needShareTree skips a test whose scratch folder dir is not where data is
// ever deleted (safeToDelete: below /share).
func needShareTree(t *testing.T, dir string) {
	t.Helper()
	if !strings.HasPrefix(filepath.Clean(dir), "/share/") {
		t.Skip("needs scratch folders below /share: data is only deleted there")
	}
}

// needRoot skips the rest of a test that gives files to another account.
func needRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root: gives files to another account")
	}
}
