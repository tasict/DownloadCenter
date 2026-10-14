package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

const repo = "../../.."

// The shipped manifest is what `mklang js` writes from the template and the dictionaries.
func TestManifestUpToDate(t *testing.T) {
	got, err := os.ReadFile(filepath.Join(repo, manifestOut))
	if err != nil {
		t.Fatal(err)
	}
	want := buildManifest(repo, readDicts(filepath.Join(repo, "daemon", "internal", "i18n")))
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is out of date: run go run ./cmd/mklang js ..", manifestOut)
	}
}

// The rules Deskpane's validator applies (Deskpane api/src/widgets.rs), so
// that no widget of ours is left out on a NAS.
func TestManifestRules(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repo, manifestOut))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > 64<<10 {
		t.Fatalf("%d bytes, Deskpane reads at most 64 KiB", len(b))
	}
	var m struct {
		V       int
		Widgets []map[string]json.RawMessage
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.V != 1 || len(m.Widgets) == 0 || len(m.Widgets) > 16 {
		t.Fatalf("v %d, %d widgets", m.V, len(m.Widgets))
	}
	idRe := regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	elemRe := regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)+$`)
	optRe := regexp.MustCompile(`^[a-z][A-Za-z0-9_]{0,31}$`)
	codes := map[string]bool{"en": true}
	for c := range readDicts(filepath.Join(repo, "daemon", "internal", "i18n")) {
		codes[c] = true
	}
	str := func(raw json.RawMessage) string {
		var s string
		json.Unmarshal(raw, &s)
		return s
	}
	text := func(where string, raw json.RawMessage, max int) {
		var tr map[string]string
		if err := json.Unmarshal(raw, &tr); err != nil || tr["en"] == "" {
			t.Errorf("%s: not a text with an English version: %s", where, raw)
			return
		}
		for code, s := range tr {
			if !codes[code] {
				t.Errorf("%s: unknown language %q", where, code)
			}
			if n := utf8.RuneCountInString(strings.TrimSpace(s)); n == 0 || n > max {
				t.Errorf("%s (%s): %d characters, Deskpane allows 1–%d", where, code, n, max)
			}
		}
	}
	seen := map[string]bool{}
	for _, w := range m.Widgets {
		id := str(w["id"])
		if !idRe.MatchString(id) || seen[id] {
			t.Errorf("widget id %q", id)
		}
		seen[id] = true
		kind := str(w["kind"])
		if kind != "data" && kind != "module" {
			t.Errorf("%s: kind %q", id, kind)
		}
		text(id+" title", w["title"], 40)
		if w["description"] != nil {
			text(id+" description", w["description"], 120)
		}
		u := str(w["url"])
		if u == "" || strings.HasPrefix(u, "/") || strings.ContainsAny(u, " \\") || strings.Contains(u, "//") || strings.Contains(strings.ToLower(u), "%2") {
			t.Errorf("%s: url %q must be a path relative to /DownloadCenter/", id, u)
		}
		for _, seg := range strings.Split(u, "/") {
			if seg == ".." || seg == "." {
				t.Errorf("%s: url %q", id, u)
			}
		}
		var sizes map[string][]string
		if err := json.Unmarshal(w["sizes"], &sizes); err != nil {
			var list []string
			if json.Unmarshal(w["sizes"], &list) != nil || len(list) == 0 {
				t.Errorf("%s: sizes %s", id, w["sizes"])
			}
			sizes = map[string][]string{"desktop": list, "phone": list}
		}
		for shell, l := range sizes {
			for _, s := range l {
				if (shell != "desktop" && shell != "phone") || (s != "s" && s != "m" && s != "l") {
					t.Errorf("%s: size %s %s", id, shell, s)
				}
			}
		}
		switch kind {
		case "module":
			el := str(w["element"])
			if !elemRe.MatchString(el) || !strings.HasPrefix(el, "downloadcenter-") || len(el) > 64 {
				t.Errorf("%s: element %q", id, el)
			}
			if _, err := os.Stat(filepath.Join(repo, "shared", "web", filepath.FromSlash(u))); err != nil {
				t.Errorf("%s: %s is not in shared/web", id, u)
			}
			js, _ := os.ReadFile(filepath.Join(repo, "shared", "web", filepath.FromSlash(u)))
			if !bytes.Contains(js, []byte("'"+el+"'")) {
				t.Errorf("%s: %s does not define %s", id, u, el)
			}
		case "data":
			var r int
			json.Unmarshal(w["refresh"], &r)
			if r < 15 || r > 3600 {
				t.Errorf("%s: refresh %d", id, r)
			}
		}
		var opts []map[string]json.RawMessage
		json.Unmarshal(w["options"], &opts)
		if len(opts) > 8 {
			t.Errorf("%s: %d options", id, len(opts))
		}
		for _, o := range opts {
			oid := str(o["id"])
			// Deskpane sets size, shell, lang and theme itself; Download Center refuses credentials in the query
			if !optRe.MatchString(oid) || strings.Contains(" size shell lang theme sid token access_token ", " "+oid+" ") {
				t.Errorf("%s: option id %q", id, oid)
			}
			text(id+"."+oid+" label", o["label"], 40)
			switch typ := str(o["type"]); typ {
			case "choice":
				var cs []struct {
					Value string
					Label json.RawMessage
				}
				json.Unmarshal(o["choices"], &cs)
				if len(cs) == 0 || len(cs) > 32 {
					t.Errorf("%s.%s: %d choices", id, oid, len(cs))
				}
				for _, c := range cs {
					if c.Value == "" || len(c.Value) > 64 {
						t.Errorf("%s.%s: choice value %q", id, oid, c.Value)
					}
					text(id+"."+oid+"="+c.Value, c.Label, 40)
				}
			case "switch", "text":
			default:
				t.Errorf("%s.%s: type %q", id, oid, typ)
			}
		}
	}
}

// The ids are stored in the users' Deskpane layouts: they never change.
func TestManifestIDs(t *testing.T) {
	m := readTemplate(repo)
	var ids []string
	for _, w := range m.Widgets {
		ids = append(ids, w.ID)
		for _, o := range w.Options {
			ids = append(ids, w.ID+"."+o.ID)
		}
	}
	if got := strings.Join(ids, " "); !strings.HasPrefix(got, "queue queue.show add add.start") {
		t.Fatalf("widget or option ids changed: %s", got)
	}
}
