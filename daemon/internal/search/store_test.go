package search

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

func TestStorePersists(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Built-in sources start as their definitions say
	for _, src := range st.Sources()[:len(Builtins())] {
		if !src.Builtin || src.Enabled != src.Plugin.EnabledByDefault() {
			t.Errorf("%s: %+v", src.Plugin.ID, src)
		}
	}
	p, _ := ParseCustom([]byte(jsonSource("mine", "api.example.org")))
	if err := st.Install(p, false, false, nil); err != nil {
		t.Fatal(err)
	}
	if src, _ := st.Get("mine"); src.Enabled || src.Kind() != "custom" {
		t.Errorf("new plugin %+v", src)
	}
	on, off := true, false
	if err := st.Update("mine", &on, map[string]string{"key": "SECRET"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.Update("kiwix", &off, nil, nil); err != nil {
		t.Fatal(err)
	}
	tz := &Plugin{Schema: 1, ID: st.NewID("Jackett"), Name: "Jackett", Type: TypeTorznab}
	if err := st.Install(tz, true, false, map[string]string{"url": "http://192.168.1.20:9117/api/v2.0/indexers/all/results/torznab", "apikey": "JKEY"}); err != nil {
		t.Fatal(err)
	}
	// An empty secret keeps the saved one; an empty plain value clears it
	if err := st.Update("jackett", nil, map[string]string{"apikey": "", "url": "http://192.168.1.21:9117/x"}, nil); err != nil {
		t.Fatal(err)
	}

	// spec: after an upgrade (a new store on the same files) everything is there
	st2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	mine, ok := st2.Get("mine")
	if !ok || !mine.Enabled || mine.Settings["key"] != "SECRET" || !mine.Ready() {
		t.Errorf("mine after reload: %+v", mine)
	}
	if k, _ := st2.Get("kiwix"); k.Enabled {
		t.Error("kiwix enabled again")
	}
	j, ok := st2.Get("jackett")
	if !ok || !j.Enabled || j.Kind() != "torznab" || j.Settings["apikey"] != "JKEY" || j.Settings["url"] != "http://192.168.1.21:9117/x" {
		t.Errorf("jackett after reload: %+v", j)
	}
	for _, f := range []string{"state.json", "plugins/mine.json", "plugins/jackett.json"} {
		if m := mode(t, filepath.Join(dir, f)); m != 0o600 {
			t.Errorf("%s: %o", f, m)
		}
	}
	for _, d := range []string{".", "plugins"} {
		if m := mode(t, filepath.Join(dir, d)); m != 0o700 {
			t.Errorf("%s: %o", d, m)
		}
	}
	// The definition file holds no setting value
	b, _ := os.ReadFile(filepath.Join(dir, "plugins", "jackett.json"))
	if strings.Contains(string(b), "JKEY") || strings.Contains(string(b), "192.168") {
		t.Errorf("definition %s", b)
	}
	enabled, custom, torznab, code := st2.Counts()
	if enabled != len(Builtins())-2+2 || custom != 1 || torznab != 1 || code != 0 { // FOSS Torrents and Kiwix off
		t.Errorf("counts %d %d %d %d", enabled, custom, torznab, code)
	}

	// Renaming a Torznab source, not another plugin
	name := "Jackett at home"
	if err := st2.Update("jackett", nil, nil, &name); err != nil {
		t.Fatal(err)
	}
	if j, _ := st2.Get("jackett"); j.Plugin.Name != name {
		t.Errorf("name %q", j.Plugin.Name)
	}
	if err := st2.Update("mine", nil, nil, &name); err == nil {
		t.Error("plugin renamed")
	}
	if err := st2.Update("mine", nil, map[string]string{"nope": "x"}, nil); err == nil {
		t.Error("unknown setting accepted")
	}

	// Deleting: custom sources only
	if err := st2.Delete("kiwix"); !errors.Is(err, ErrBuiltin) {
		t.Errorf("delete built-in: %v", err)
	}
	if err := st2.Delete("nothere"); !errors.Is(err, ErrNoSource) {
		t.Errorf("delete unknown: %v", err)
	}
	if err := st2.Delete("mine"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "plugins", "mine.json")); !os.IsNotExist(err) {
		t.Error("plugin file kept")
	}
	if _, ok := st2.Get("mine"); ok {
		t.Error("plugin kept")
	}
}

func TestStoreRules(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Built-in sources cannot be replaced
	ia := *Builtin("internet-archive")
	if err := st.Install(&ia, true, true, nil); !errors.Is(err, ErrExists) {
		t.Errorf("replace built-in: %v", err)
	}
	p, _ := ParseCustom([]byte(jsonSource("mine", "api.example.org")))
	st.Install(p, false, false, map[string]string{"key": "K"})
	if err := st.Install(p, false, false, nil); !errors.Is(err, ErrExists) {
		t.Errorf("install twice: %v", err)
	}
	on := true
	st.Update("mine", &on, nil, nil)
	// A new version keeps the state and settings
	p2, _ := ParseCustom([]byte(strings.Replace(jsonSource("mine", "api.example.org"), `"name":"MINE"`, `"name":"Mine 2"`, 1)))
	if err := st.Install(p2, false, true, nil); err != nil {
		t.Fatal(err)
	}
	if src, _ := st.Get("mine"); !src.Enabled || src.Settings["key"] != "K" || src.Plugin.Name != "Mine 2" {
		t.Errorf("replaced %+v", src)
	}

	// At most MaxSources, built-in ones included
	for i := len(Builtins()) + 1; i < MaxSources; i++ {
		p, err := ParseCustom([]byte(jsonSource(fmt.Sprintf("p%d", i), "api.example.org")))
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Install(p, false, false, nil); err != nil {
			t.Fatalf("source %d: %v", i, err)
		}
	}
	if len(st.Sources()) != MaxSources {
		t.Fatalf("%d sources", len(st.Sources()))
	}
	p, _ = ParseCustom([]byte(jsonSource("one-more", "api.example.org")))
	if err := st.Install(p, false, false, nil); !errors.Is(err, ErrLimit) {
		t.Errorf("51st source: %v", err)
	}

	// Ids for Torznab servers
	for name, want := range map[string]string{"My Jackett": "my-jackett", "Kiwix": "kiwix-2", "!!": "torznab", "mine": "mine-2", "Prowlarr (LAN)": "prowlarr-lan"} {
		if got := st.NewID(name); got != want {
			t.Errorf("NewID(%q) = %q, want %q", name, got, want)
		}
	}
	if id := st.NewID(strings.Repeat("a", 80)); !idRe.MatchString(id) {
		t.Errorf("long name: %q", id)
	}
}

func TestStoreDamagedFiles(t *testing.T) {
	dir := t.TempDir()
	st, _ := OpenStore(dir)
	p, _ := ParseCustom([]byte(jsonSource("mine", "api.example.org")))
	st.Install(p, true, false, map[string]string{"key": "K"})
	// A broken state file is kept aside, not overwritten
	os.WriteFile(filepath.Join(dir, "state.json"), []byte("{broken"), 0o600)
	// A plugin file that a later version made a built-in id is skipped, not deleted
	b, _ := Builtin("fedora").Marshal()
	os.WriteFile(filepath.Join(dir, "plugins", "fedora.json"), b, 0o600)
	// A file whose id is not its name, and one that is not valid
	os.WriteFile(filepath.Join(dir, "plugins", "other.json"), []byte(jsonSource("different", "api.example.org")), 0o600)
	os.WriteFile(filepath.Join(dir, "plugins", "bad.json"), []byte(`{"script":1}`), 0o600)
	st2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "state.json.bad-*"))
	if len(matches) != 1 {
		t.Errorf("state kept aside: %v", matches)
	}
	if src, ok := st2.Get("mine"); !ok || src.Enabled {
		t.Errorf("mine: %+v %v", src, ok)
	}
	if src, _ := st2.Get("fedora"); !src.Builtin {
		t.Error("fedora replaced")
	}
	if _, err := os.Stat(filepath.Join(dir, "plugins", "fedora.json")); err != nil {
		t.Error("fedora.json deleted")
	}
	for _, id := range []string{"different", "other", "bad"} {
		if _, ok := st2.Get(id); ok {
			t.Errorf("%s loaded", id)
		}
	}
}
