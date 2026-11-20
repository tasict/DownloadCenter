package search

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MaxSources is the number of search sources there can be, built-in ones
// included.
const MaxSources = 50

var (
	// ErrLimit is a source that would be one too many.
	ErrLimit = errors.New("There can be at most 50 search sources")
	// ErrBuiltin is a change only custom sources allow.
	ErrBuiltin = errors.New("Built-in search sources cannot be deleted or replaced")
	// ErrNoSource is an unknown source id.
	ErrNoSource = errors.New("Search source not found")
)

// Source is a search source as the store has it: its definition, whether
// it is on, and its setting values (secrets included: never give these to
// an API caller).
type Source struct {
	Plugin   *Plugin
	Builtin  bool
	Enabled  bool
	Settings map[string]string
	// Audience is who may search it: AudienceAll or AudienceAdmins.
	Audience string
}

// Who may use a source.
const (
	AudienceAll    = "all"
	AudienceAdmins = "admins"
)

// Kind is "builtin", "torznab", "dlm", "qnap-addon" or "custom".
func (s Source) Kind() string {
	switch {
	case s.Builtin:
		return "builtin"
	case s.Plugin.Type == TypeTorznab, s.Plugin.Type == TypeDLM, s.Plugin.Type == TypeAddon:
		return s.Plugin.Type
	}
	return "custom"
}

// SecretBearing reports a source with a secret setting that has a value:
// its results may carry what identifies the account (a passkey).
func (s Source) SecretBearing() bool { return len(s.Plugin.SecretValues(s.Settings)) > 0 }

// Visible reports whether a caller may search the source.
func (s Source) Visible(admin bool) bool { return admin || s.Audience == AudienceAll }

// defaultAudience: programs, Torznab servers and sources with a secret
// setting are for administrators, the others for everyone.
func defaultAudience(p *Plugin) string {
	if p.code != nil || p.Type == TypeTorznab {
		return AudienceAdmins
	}
	for _, d := range p.SettingDefs() {
		if d.Secret {
			return AudienceAdmins
		}
	}
	return AudienceAll
}

// Ready reports a source that takes part in searches: enabled and with
// every required setting.
func (s Source) Ready() bool { return s.Enabled && s.Plugin.Complete(s.Settings) }

// Store keeps the sources in <data>/search: plugins/<id>.json (the custom
// definitions as validated and written again) and state.json (enabled
// flags and setting values), all 0600 in 0700 directories, written to a
// temporary file and renamed.
type Store struct {
	dir string

	mu      sync.Mutex
	custom  map[string]*Plugin
	state   map[string]*sourceState
	changed func(id string) // a source's definition or settings changed
}

type sourceState struct {
	// Enabled is nil until someone switches the source (built-ins then
	// follow their definition's default).
	Enabled  *bool             `json:"enabled,omitempty"`
	Settings map[string]string `json:"settings,omitempty"`
	// Audience is "" until changed (the default then applies)
	Audience string `json:"audience,omitempty"`
	// Code is a plugin from another NAS: what its package says, and who
	// agreed to run it
	Code *codeState `json:"code,omitempty"`
}

type stateFile struct {
	Sources map[string]*sourceState `json:"sources"`
}

// OpenStore loads the store under dir (created when missing). A state file
// that cannot be read is moved aside, not overwritten: it holds API keys.
func OpenStore(dir string) (*Store, error) {
	s := &Store{dir: dir, custom: map[string]*Plugin{}, state: map[string]*sourceState{}}
	if err := os.MkdirAll(filepath.Join(dir, "plugins"), 0o700); err != nil {
		return nil, err
	}
	os.Chmod(dir, 0o700)
	os.Chmod(filepath.Join(dir, "plugins"), 0o700)
	if b, err := os.ReadFile(s.statePath()); err == nil {
		var f stateFile
		if err := json.Unmarshal(b, &f); err != nil {
			bad := s.statePath() + ".bad-" + strconv.FormatInt(time.Now().Unix(), 10)
			os.Rename(s.statePath(), bad)
			log.Printf("search: state.json cannot be read, moved to %s", filepath.Base(bad))
		} else if f.Sources != nil {
			for id, st := range f.Sources {
				if st != nil {
					s.state[id] = st
				}
			}
		}
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "plugins"))
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, "plugins", e.Name()))
		if err != nil {
			continue
		}
		p, err := Parse(b)
		switch {
		case err != nil:
			// The rule it breaks, not the plugin's own text
			var ie *InvalidError
			why := "it cannot be read"
			if errors.As(err, &ie) {
				why = ie.Msg
			}
			log.Printf("search: plugin %s cannot be used: %s", id, why)
		case p.ID != id:
			log.Printf("search: plugin file %s holds the id %s, skipped", e.Name(), p.ID)
		case IsBuiltin(id):
			// A later version made a built-in source with this id: keep the
			// file, use the built-in one
			log.Printf("search: plugin %s has the id of a built-in source, skipped", id)
		default:
			s.custom[id] = p
		}
	}
	s.loadCode()
	return s, nil
}

func (s *Store) statePath() string { return filepath.Join(s.dir, "state.json") }

func (s *Store) pluginPath(id string) string { return filepath.Join(s.dir, "plugins", id+".json") }

// writeFile writes a file 0600 through a temporary file and a rename.
func writeFile(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// saveLocked writes state.json (s.mu held).
func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(stateFile{Sources: s.state}, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(s.statePath(), b)
}

func (s *Store) sourceLocked(id string) (Source, bool) {
	p := Builtin(id)
	builtin := p != nil
	if p == nil {
		p = s.custom[id]
	}
	if p == nil {
		return Source{}, false
	}
	src := Source{Plugin: p, Builtin: builtin, Settings: map[string]string{}}
	st := s.state[id]
	switch {
	case st != nil && st.Enabled != nil:
		src.Enabled = *st.Enabled
	case builtin:
		src.Enabled = p.EnabledByDefault()
	}
	if st != nil {
		for k, v := range st.Settings {
			src.Settings[k] = v
		}
	}
	src.Audience = defaultAudience(p)
	if st != nil && (st.Audience == AudienceAll || st.Audience == AudienceAdmins) {
		src.Audience = st.Audience
	}
	return src, true
}

// Sources are all the sources: the built-in ones (in their order), then
// the custom ones by name.
func (s *Store) Sources() []Source {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Source
	for _, p := range Builtins() {
		src, _ := s.sourceLocked(p.ID)
		out = append(out, src)
	}
	ids := make([]string, 0, len(s.custom))
	for id := range s.custom {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := strings.ToLower(s.custom[ids[i]].Name), strings.ToLower(s.custom[ids[j]].Name)
		if a != b {
			return a < b
		}
		return ids[i] < ids[j]
	})
	for _, id := range ids {
		src, _ := s.sourceLocked(id)
		out = append(out, src)
	}
	return out
}

// Get is one source.
func (s *Store) Get(id string) (Source, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sourceLocked(id)
}

// sameShape reports whether next may take over what is saved for prev (on
// or off, audience, setting values): the same type, the same request
// address (requestShape), the same way of building result addresses
// (linkShape) and the same settings with the same secret flags. Never for
// a program: what was saved for one never goes to other code.
func sameShape(prev, next *Plugin) bool {
	if prev == nil || next == nil || prev.code != nil || next.code != nil || prev.Type != next.Type ||
		requestShape(prev) != requestShape(next) || linkShape(prev) != linkShape(next) {
		return false
	}
	a, b := prev.SettingDefs(), next.SettingDefs()
	if len(a) != len(b) {
		return false
	}
	secret := map[string]bool{}
	for _, d := range a {
		secret[d.ID] = d.Secret
	}
	for _, d := range b {
		if was, ok := secret[d.ID]; !ok || was != d.Secret {
			return false
		}
	}
	return true
}

var templateVar = regexp.MustCompile(`\{[^{}]*\}`)

// requestShape is the whole address a plugin file sends its request to,
// its variables taken out: scheme, host and port (in lower case), path and
// parameters ("" for torznab, whose address is a setting). Another script
// on the same host is another source.
func requestShape(p *Plugin) string {
	if p.Request == nil {
		return ""
	}
	scheme, rest, _ := strings.Cut(p.Request.URL, "://")
	host, path := rest, ""
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		host, path = rest[:i], rest[i:]
	}
	return strings.ToLower(scheme+"://"+host) + templateVar.ReplaceAllString(path, "{}")
}

// linkShape is how a plugin file builds the addresses of its results
// (magnet link, .torrent and page: paths, templates and replacements).
func linkShape(p *Plugin) string {
	if p.Response == nil {
		return ""
	}
	f := p.Response.Fields
	b, _ := json.Marshal([]*Field{f.Magnet, f.Torrent, f.Page, f.InfoHash})
	return string(b)
}

// Install adds a custom plugin, or with replace puts a new definition in
// place of a custom one with the same id. A definition of the same shape
// (sameShape) keeps the source's state, audience and setting values; any
// other starts anew, switched off, with the default audience and only the
// values given. New plugins are enabled as asked. A plugin file never
// takes the id of a program.
func (s *Store) Install(p *Plugin, enabled, replace bool, settings map[string]string) error {
	if IsBuiltin(p.ID) {
		return ErrExists
	}
	if err := p.CheckSettings(settings); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, exists := s.custom[p.ID]
	if exists && !replace {
		return ErrExists
	}
	if exists && (old.code != nil || old.Type != p.Type && (old.Type == TypeTorznab || p.Type == TypeTorznab)) {
		return ErrExists
	}
	if !exists && len(Builtins())+len(s.custom) >= MaxSources {
		return ErrLimit
	}
	b, err := p.Marshal()
	if err != nil {
		return err
	}
	if err := writeFile(s.pluginPath(p.ID), b); err != nil {
		return err
	}
	st := s.state[p.ID]
	if !exists || st == nil || !sameShape(old, p) {
		on := enabled && !exists
		st = &sourceState{Enabled: &on}
		s.state[p.ID] = st
	}
	s.custom[p.ID] = p
	// Keep the values of the settings the definition still has
	keep := map[string]string{}
	for _, d := range p.SettingDefs() {
		if v, ok := st.Settings[d.ID]; ok {
			keep[d.ID] = v
		}
		if v := settings[d.ID]; v != "" {
			keep[d.ID] = strings.TrimSpace(v)
		}
	}
	st.Settings = keep
	err = s.saveLocked()
	s.notify(p.ID)
	return err
}

// Update switches a source on or off, changes its setting values (an empty
// value for a secret keeps the saved one, for anything else clears it) and
// renames a Torznab source.
func (s *Store) Update(id string, enabled *bool, settings map[string]string, name *string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, ok := s.sourceLocked(id)
	if !ok {
		return ErrNoSource
	}
	p := src.Plugin
	if err := p.CheckSettings(settings); err != nil {
		return err
	}
	if name != nil && *name != p.Name {
		if p.Type != TypeTorznab || src.Builtin {
			// Other plugins are named by their definition
			return invalid(msgNotValid, "name")
		}
		np := *p
		np.Name = strings.TrimSpace(*name)
		if err := Validate(&np); err != nil {
			return err
		}
		b, err := np.Marshal()
		if err != nil {
			return err
		}
		if err := writeFile(s.pluginPath(id), b); err != nil {
			return err
		}
		s.custom[id] = &np
	}
	st := s.state[id]
	if st == nil {
		st = &sourceState{}
		s.state[id] = st
	}
	if enabled != nil {
		v := *enabled
		st.Enabled = &v
	}
	if len(settings) > 0 {
		if st.Settings == nil {
			st.Settings = map[string]string{}
		}
		for _, d := range p.SettingDefs() {
			v, given := settings[d.ID]
			if !given {
				continue
			}
			v = strings.TrimSpace(v)
			switch {
			case v != "":
				st.Settings[d.ID] = v
			case !d.Secret:
				delete(st.Settings, d.ID)
			}
		}
	}
	err := s.saveLocked()
	s.notify(id)
	return err
}

// Delete removes a custom source with its settings.
func (s *Store) Delete(id string) error {
	if IsBuiltin(id) {
		return ErrBuiltin
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.custom[id]
	if !ok {
		return ErrNoSource
	}
	if p.code != nil {
		if err := os.RemoveAll(s.codeDir(id)); err != nil {
			return err
		}
	} else if err := os.Remove(s.pluginPath(id)); err != nil && !os.IsNotExist(err) {
		return err
	}
	delete(s.custom, id)
	delete(s.state, id)
	err := s.saveLocked()
	s.notify(id)
	return err
}

func (s *Store) notify(id string) {
	if s.changed != nil {
		s.changed(id)
	}
}

// NewID makes an id for a source called name that no source has yet.
func (s *Store) NewID(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if b.Len() >= 32 {
			break
		}
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		} else {
			dash = true
		}
	}
	base := b.String()
	if len(base) < 2 {
		base = "torznab"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := base
	for n := 2; IsBuiltin(id) || s.custom[id] != nil; n++ {
		id = base + "-" + strconv.Itoa(n)
	}
	return id
}

// Counts are the numbers the usage statistics report: enabled sources
// (ready to search), custom plugins, Torznab sources and plugins from
// another NAS.
func (s *Store) Counts() (enabled, custom, torznab, code int) {
	for _, src := range s.Sources() {
		if src.Ready() {
			enabled++
		}
		switch src.Kind() {
		case "custom":
			custom++
		case TypeTorznab:
			torznab++
		case TypeDLM, TypeAddon:
			code++
		}
	}
	return
}

// SetAudience sets who may search a source.
func (s *Store) SetAudience(id, audience string) error {
	if audience != AudienceAll && audience != AudienceAdmins {
		return invalid(msgUnsupported, "audience", audience)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sourceLocked(id); !ok {
		return ErrNoSource
	}
	st := s.state[id]
	if st == nil {
		st = &sourceState{}
		s.state[id] = st
	}
	st.Audience = audience
	return s.saveLocked()
}
