package search

import (
	"os"
	"testing"

	"downloadcenter/internal/phpbox"
)

// phpbox starts /proc/self/exe as its sandbox's first process: in these
// tests that is the test binary, which must then act as phpbox's helper.
func TestMain(m *testing.M) {
	if phpbox.IsHelper() {
		os.Exit(phpbox.Helper())
	}
	os.Exit(m.Run())
}
