package phpbox

import (
	"os"
	"testing"
)

// The sandbox starts /proc/self/exe as its helper: in tests that is the
// test binary, which must then act as the helper.
func TestMain(m *testing.M) {
	if IsHelper() {
		os.Exit(Helper())
	}
	switch os.Getenv(filterChild) {
	case "1":
		os.Exit(filterChildMain())
	case "status":
		b, _ := os.ReadFile("/proc/self/status")
		os.Stdout.Write(b)
		os.Exit(0)
	}
	os.Exit(m.Run())
}
