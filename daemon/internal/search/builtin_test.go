package search

import (
	"strings"
	"testing"
)

func TestBuiltins(t *testing.T) {
	ps := Builtins()
	if builtinErr != nil {
		t.Fatal(builtinErr)
	}
	ids := []string{}
	for _, p := range ps {
		ids = append(ids, p.ID)
		if err := Validate(p); err != nil {
			t.Errorf("%s: %v", p.ID, err)
		}
		if p.Type == TypeTorznab || len(p.SettingDefs()) != 0 {
			t.Errorf("%s: a built-in source needs nothing filled in", p.ID)
		}
		if !strings.HasPrefix(p.Request.URL, "https://") || p.TestQuery == "" || p.Homepage == "" || p.Description == "" {
			t.Errorf("%s: %+v", p.ID, p)
		}
	}
	if got := strings.Join(ids, " "); got != "archlinux fedora fosstorrents internet-archive kiwix" {
		t.Fatalf("built-in sources: %s", got)
	}
	// FOSS Torrents publishes no terms of use: it starts disabled
	for _, p := range ps {
		if p.EnabledByDefault() != (p.ID != "fosstorrents") {
			t.Errorf("%s enabled: %v", p.ID, p.EnabledByDefault())
		}
	}
	if !IsBuiltin("kiwix") || IsBuiltin("example") || Builtin("nope") != nil {
		t.Error("IsBuiltin")
	}
}

// Every built-in source reads the answer saved from its site.
func TestBuiltinSamples(t *testing.T) {
	samples := map[string]struct{ file, query string }{
		"internet-archive": {"ia.json", "night of the living dead"},
		"kiwix":            {"kiwix.xml", "wikipedia"},
		"fedora":           {"fedora.xml", "fedora"},
		"archlinux":        {"arch.json", "arch linux"},
		"fosstorrents":     {"foss.xml", "ubuntu"},
	}
	for _, p := range Builtins() {
		s, ok := samples[p.ID]
		if !ok {
			t.Errorf("%s: no sample", p.ID)
			continue
		}
		c, err := p.BuildRequest(Query{Text: s.query}, nil)
		if err != nil {
			t.Fatalf("%s: %v", p.ID, err)
		}
		if h := p.Host(nil); !strings.HasPrefix(c.URL, "https://"+h+"/") {
			t.Errorf("%s: %s is not on %s", p.ID, c.URL, h)
		}
		rs, err := p.Results(readData(t, s.file), c.URL)
		if err != nil {
			t.Fatalf("%s: %v", p.ID, err)
		}
		sel := p.Select(rs, Query{Text: s.query})
		if len(sel) == 0 {
			t.Errorf("%s: no result", p.ID)
		}
		for _, r := range sel {
			if r.Title == "" || (r.InfoHash == "" && r.Torrent == "") || r.Category == "" || r.Sources[0] != p.ID {
				t.Errorf("%s: %+v", p.ID, r)
			}
		}
	}
}
