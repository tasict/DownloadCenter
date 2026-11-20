//go:build !linux

package phpbox

import (
	"context"
	"os"
)

// Without Linux namespaces there is no sandbox, so plugins never run.

type layout struct{ php string }

func discover() (*layout, string, string) {
	if _, err := os.Stat(phpPath); err != nil {
		return nil, CodePHPMissing, ReasonPHPMissing
	}
	return &layout{php: phpPath}, "", ""
}

func (r *Runtime) probe(ctx context.Context, lay *layout) (string, string) {
	return CodeNoIsolation, ReasonNoIsolation
}

func (r *Runtime) run(ctx context.Context, lay *layout, pluginDir, shim string, probe bool, req request) runOut {
	return runOut{err: errNoPHP, stage: "start"}
}

// HelperArg is the hidden subcommand of the sandbox's first process.
const HelperArg = "__phpbox"

// IsHelper reports whether this process was started as the helper.
func IsHelper() bool { return len(os.Args) == 2 && os.Args[1] == HelperArg }

// Helper refuses: there is no sandbox here.
func Helper() int { return 2 }
