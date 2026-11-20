package safefs

import (
	"os"
	"testing"
)

// TestMain: run by an account other than root (a build server), the tests'
// own folders stand for root's, as nothing can be given to root there.
func TestMain(m *testing.M) {
	if os.Geteuid() != 0 {
		trustedUID = uint32(os.Geteuid())
	}
	os.Exit(m.Run())
}

// needRoot skips the rest of a test that gives files to another account.
func needRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root: gives files to another account")
	}
}
