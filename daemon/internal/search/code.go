package search

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"downloadcenter/internal/phpbox"
)

// Plugins from another NAS (search modules packed as .dlm, add-ons packed
// as .addon) are programs: phpbox runs them in its sandbox. What their
// package says, and who agreed to run them, is kept in state.json; their
// files in code/<id>/.

// Messages about plugins from another NAS.
const (
	ReasonPluginFailed  = "The plugin failed"
	ReasonPluginTimeout = "The plugin did not finish in time"
	ReasonCannotRun     = "This NAS cannot run this plugin: {0}"
	msgConsent          = "Agree to the terms for this exact file before installing it"
	msgNoAccount        = "This source has no account to check"
	msgNoExport         = "This source has no plugin file to export"
	labelUser           = "User name"
	labelPassword       = "Password"
)

// Consent is who agreed to run a plugin from another NAS, and when (Unix
// seconds).
type Consent struct {
	User string `json:"user"`
	Time int64  `json:"time"`
}

// codeState is an installed plugin from another NAS.
type codeState struct {
	Format         string   `json:"format"` // phpbox.FormatDLM or FormatAddon
	Name           string   `json:"name"`
	DisplayName    string   `json:"display_name,omitempty"`
	Version        string   `json:"version,omitempty"`
	Description    string   `json:"description,omitempty"`
	Site           string   `json:"site,omitempty"`
	Hosts          []string `json:"hosts,omitempty"`
	Entry          string   `json:"entry"`
	Class          string   `json:"class"`
	Account        bool     `json:"account,omitempty"`
	KeyFingerprint string   `json:"key_fingerprint,omitempty"`
	SHA256         string   `json:"sha256"`
	Consent        Consent  `json:"consent"`
}

// sites are the sites the plugin says it uses: a module's site, an
// add-on's hosts (or its website); host names only with hostsOnly.
func (c *codeState) sites(hostsOnly bool) []string {
	var l []string
	if c.Format == phpbox.FormatAddon && len(c.Hosts) > 0 {
		l = append(l, c.Hosts...)
	} else if c.Site != "" {
		l = append(l, c.Site)
	}
	if !hostsOnly {
		return l
	}
	out := []string{}
	for _, s := range l {
		h := s
		if u, err := url.Parse(s); err == nil && u.Hostname() != "" {
			h = u.Hostname()
		}
		out = append(out, h)
	}
	return out
}

func codeFromPackage(p *phpbox.Package, consent Consent) *codeState {
	return &codeState{Format: p.Format, Name: p.Name, DisplayName: p.DisplayName, Version: p.Version, Description: p.Description,
		Site: p.Site, Hosts: p.Hosts, Entry: p.Entry, Class: p.Class, Account: p.Account, KeyFingerprint: p.KeyFingerprint,
		SHA256: p.SHA256, Consent: consent}
}

// codePlugin is how the rest of the package sees an installed plugin from
// another NAS. One with an account has the settings username and password.
func codePlugin(id string, c *codeState) *Plugin {
	p := &Plugin{Schema: SchemaVersion, ID: id, Name: c.Name, Version: c.Version, Description: c.Description, Type: TypeDLM, code: c}
	if c.DisplayName != "" {
		p.Name = c.DisplayName
	}
	if c.Format == phpbox.FormatAddon {
		p.Type = TypeAddon
	}
	if c.Site != "" && httpURL(c.Site) {
		p.Homepage = c.Site
	}
	if c.Account {
		p.Settings = []Setting{{ID: "username", Label: labelUser}, {ID: "password", Label: labelPassword, Secret: true}}
	}
	return p
}

// codeID is the id of a package: by its format and name, so that a new
// version of a plugin replaces the old one.
func codeID(p *phpbox.Package) string {
	prefix := "dlm-"
	if p.Format == phpbox.FormatAddon {
		prefix = "addon-"
	}
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(p.Name) {
		if b.Len() >= 30 {
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
	if b.Len() == 0 {
		b.WriteString("plugin")
	}
	return prefix + b.String()
}

func (s *Store) codeDir(id string) string { return filepath.Join(s.dir, "code", id) }

// loadCode adds the installed plugins from another NAS (state with code
// and its directory there); s.mu need not be held while opening.
func (s *Store) loadCode() {
	for id, st := range s.state {
		if st.Code == nil {
			continue
		}
		if fi, err := os.Stat(s.codeDir(id)); err != nil || !fi.IsDir() {
			log.Printf("search: the files of plugin %s are missing, skipped", id)
			continue
		}
		if IsBuiltin(id) || s.custom[id] != nil {
			log.Printf("search: plugin %s has an id taken by another source, skipped", id)
			continue
		}
		s.custom[id] = codePlugin(id, st.Code)
	}
}

// codeFor is an installed plugin from another NAS by id, with its state.
func (s *Store) codeFor(id string) (*codeState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.custom[id]
	if p == nil || p.code == nil {
		return nil, false
	}
	return p.code, true
}

// InstallCode installs a plugin from another NAS that an administrator
// agreed to run: its files into code/<id>/ (a new directory renamed into
// place), disabled, for administrators. A plugin with the same id is only
// replaced with replace, and then the consent is the new one and the saved
// account is dropped. It returns the id.
func (s *Store) InstallCode(p *phpbox.Package, consent Consent, replace bool) (string, error) {
	id := codeID(p)
	if IsBuiltin(id) {
		return "", ErrExists
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.custom[id]
	switch {
	case old != nil && (old.code == nil || !replace):
		return "", ErrExists
	case old == nil && len(Builtins())+len(s.custom) >= MaxSources:
		return "", ErrLimit
	}
	codeRoot := filepath.Join(s.dir, "code")
	if err := os.MkdirAll(codeRoot, 0o700); err != nil {
		return "", err
	}
	os.Chmod(codeRoot, 0o700)
	b := make([]byte, 6)
	rand.Read(b)
	tmp := filepath.Join(codeRoot, ".new-"+hex.EncodeToString(b))
	if err := p.Extract(tmp); err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	gone := filepath.Join(codeRoot, ".old-"+hex.EncodeToString(b))
	if old != nil {
		if err := os.Rename(s.codeDir(id), gone); err != nil && !os.IsNotExist(err) {
			os.RemoveAll(tmp)
			return "", err
		}
		defer os.RemoveAll(gone)
	}
	if err := os.Rename(tmp, s.codeDir(id)); err != nil {
		os.RemoveAll(tmp)
		if old != nil {
			os.Rename(gone, s.codeDir(id))
		}
		return "", err
	}
	// Everything starts anew, a replacement too: switched off, for
	// administrators, and without the account, which new code is not
	// given until an administrator enters it again
	c := codeFromPackage(p, consent)
	off := false
	s.state[id] = &sourceState{Enabled: &off, Code: c}
	s.custom[id] = codePlugin(id, c)
	err := s.saveLocked()
	s.notify(id)
	return id, err
}

// codeRunner runs plugins from another NAS (*phpbox.Runtime; tests use a
// fake).
type codeRunner interface {
	Check(ctx context.Context) (bool, string, string)
	Available() (bool, string, string)
	Search(ctx context.Context, p phpbox.Plugin, q phpbox.Query) (*phpbox.Output, error)
	Verify(ctx context.Context, p phpbox.Plugin, a phpbox.Account) (bool, *phpbox.Output, error)
}

// runtimeState is the last check of the runner: without one, plugins from
// another NAS never run. A failed check is repeated in the background, at
// most once a minute, when someone wants to know.
func (s *Service) runtimeState() (bool, string, string) {
	if s.code == nil {
		return false, phpbox.CodeNoIsolation, phpbox.ReasonNoIsolation
	}
	ok, code, reason := s.code.Available()
	if !ok {
		now := s.now().Unix()
		if last := s.recheck.Load(); now-last >= 60 && s.recheck.CompareAndSwap(last, now) {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				s.code.Check(ctx)
			}()
		}
	}
	return ok, code, reason
}

// checkRuntime checks now (an administrator inspects or installs a plugin
// from another NAS).
func (s *Service) checkRuntime(ctx context.Context) (bool, string, string) {
	if s.code == nil {
		return false, phpbox.CodeNoIsolation, phpbox.ReasonNoIsolation
	}
	return s.code.Check(ctx)
}

// runnable reports whether a source can run now: always for the
// declarative ones, while the sandbox is available for programs.
func (s *Service) runnable(src Source) bool {
	if !src.Plugin.RunsCode() {
		return true
	}
	ok, _, _ := s.runtimeState()
	return ok
}

func (s *Service) boxPlugin(src Source) phpbox.Plugin {
	c := src.Plugin.code
	return phpbox.Plugin{Format: c.Format, Dir: s.store.codeDir(src.Plugin.ID), Entry: c.Entry, Class: c.Class, Account: c.Account}
}

// account is the account of a plugin, given to it only.
func account(src Source) *phpbox.Account {
	if !src.Plugin.code.Account || src.Settings["username"] == "" {
		return nil
	}
	return &phpbox.Account{Username: src.Settings["username"], Password: src.Settings["password"]}
}

// codeFailure turns a run's error into a source failure: a fixed reason
// only.
func codeFailure(ctx context.Context, err error) error {
	var u *phpbox.Unavailable
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.As(err, &u):
		return fail(strings.Replace(ReasonCannotRun, "{0}", u.Reason, 1), u.Code)
	case errors.Is(err, phpbox.ErrTimeout):
		return fail(ReasonPluginTimeout, "")
	}
	// What the plugin wrote is never shown or logged: it can carry the
	// keyword or the account in any form
	return fail(ReasonPluginFailed, "")
}

// logRun writes what a run of a program did: numbers only.
func logRun(id string, out *phpbox.Output) {
	if out == nil {
		return
	}
	log.Printf("search: plugin %s ran %d ms: %d results, %d connections (%d refused), %d bytes",
		id, out.Duration.Milliseconds(), len(out.Results), out.Relay.Conns, out.Relay.Refused, out.Relay.Bytes)
}

// codeSearch runs a plugin from another NAS and normalises its results
// like any source's.
func (s *Service) codeSearch(ctx context.Context, src Source, q Query) ([]Result, error) {
	if s.code == nil {
		return nil, fail(strings.Replace(ReasonCannotRun, "{0}", phpbox.ReasonNoIsolation, 1), "")
	}
	out, err := s.code.Search(ctx, s.boxPlugin(src), phpbox.Query{Keyword: q.Text, Category: q.Category, Limit: MaxResults, Account: account(src)})
	logRun(src.Plugin.ID, out)
	if err != nil {
		return nil, codeFailure(ctx, err)
	}
	rs := []Result{}
	for _, r := range out.Results {
		if res, ok := src.Plugin.normalise(rawOf(r), nil); ok {
			rs = append(rs, res)
		}
	}
	return rs, nil
}

// rawOf is a plugin's result as an item read from an answer: the link
// goes where its kind says (magnet or .torrent address).
func rawOf(r phpbox.Result) raw {
	out := raw{title: r.Title, infohash: r.Hash, page: r.Page, date: r.Date, category: guessCategory(r.Category)}
	if isMagnet(r.Download) {
		out.magnet = r.Download
	} else {
		out.torrent = r.Download
	}
	seeds, leechs := r.Seeds, r.Leechs
	if seeds == 0 && leechs == 0 {
		// What many plugins give when they do not know: unknown, not none
		seeds, leechs = -1, -1
	}
	for _, n := range []struct {
		v  int64
		to *string
	}{{r.Size, &out.size}, {seeds, &out.seeders}, {leechs, &out.leechers}} {
		if n.v >= 0 {
			*n.to = strconv.FormatInt(n.v, 10)
		}
	}
	return out
}

// guessCategory maps a plugin's own category name to ours by its words;
// "" when nothing fits (Select then makes it other).
func guessCategory(c string) string {
	l := strings.ToLower(c)
	for _, m := range []struct {
		cat   string
		words []string
	}{
		{"books", []string{"book", "comic", "manga", "magazine", "text"}},
		{"audio", []string{"audio", "music", "mp3", "flac", "lossless", "album"}},
		{"video", []string{"movie", "film", "video", "tv", "series", "show", "anime", "documentar"}},
		{"software", []string{"software", "app", "game", "linux", "windows", "mac", "os", "iso", "program"}},
		{"other", []string{"other", "misc"}},
	} {
		for _, w := range m.words {
			if strings.Contains(l, w) {
				return m.cat
			}
		}
	}
	return ""
}

// ErrNoAccount is a check of an account on a source that takes none.
var ErrNoAccount = errors.New(msgNoAccount)

// VerifyAccount asks a plugin from another NAS whether its account works.
// A run that fails is a *Failure.
func (s *Service) VerifyAccount(ctx context.Context, src Source) (bool, error) {
	if !src.Plugin.RunsCode() || !src.Plugin.code.Account {
		return false, ErrNoAccount
	}
	if s.code == nil {
		return false, fail(strings.Replace(ReasonCannotRun, "{0}", phpbox.ReasonNoIsolation, 1), "")
	}
	a := phpbox.Account{Username: src.Settings["username"], Password: src.Settings["password"]}
	ok, out, err := s.code.Verify(ctx, s.boxPlugin(src), a)
	logRun(src.Plugin.ID, out)
	if err != nil {
		return false, codeFailure(ctx, err)
	}
	return ok, nil
}
