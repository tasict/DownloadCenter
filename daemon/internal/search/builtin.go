package search

import (
	"embed"
	"fmt"
	"sort"
	"sync"
)

// The built-in sources: sites whose content may be freely distributed and
// that have a documented API or feed giving infohashes or .torrent files.
// They are defined in the plugin format and validated like any plugin.
//
//go:embed builtin/*.json
var builtinFiles embed.FS

var (
	builtinOnce sync.Once
	builtins    []*Plugin
	builtinErr  error // the first definition that did not load (tests)
)

func loadBuiltins() {
	entries, _ := builtinFiles.ReadDir("builtin")
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	seen := map[string]bool{}
	for _, n := range names {
		b, err := builtinFiles.ReadFile("builtin/" + n)
		var p *Plugin
		if err == nil {
			p, err = Parse(b)
		}
		if err == nil && seen[p.ID] {
			err = fmt.Errorf("id %s used twice", p.ID)
		}
		if err != nil {
			if builtinErr == nil {
				builtinErr = fmt.Errorf("built-in search source %s: %v", n, err)
			}
			continue
		}
		seen[p.ID] = true
		builtins = append(builtins, p)
	}
}

// Builtins are the built-in sources, sorted by file name. The definitions
// are shared: callers must not change them.
func Builtins() []*Plugin {
	builtinOnce.Do(loadBuiltins)
	return builtins
}

// Builtin is the built-in source with this id, nil if there is none.
func Builtin(id string) *Plugin {
	for _, p := range Builtins() {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// IsBuiltin reports whether id is a built-in source's.
func IsBuiltin(id string) bool { return Builtin(id) != nil }
