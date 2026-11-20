package search

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"downloadcenter/internal/api"
	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/phpbox"
)

// The service of the running dcd, for Counts and Available.
var (
	currentMu sync.Mutex
	current   *Service
)

// Register opens the store in <data>/search, adds the search API and the
// "search" value of /me, and returns the function that stops every
// search. Requests to sources use the proxy a regular user's request to
// the same address would use, whoever searches.
func Register(srv *api.Server, m *core.Manager, root, data string) func() {
	st, err := OpenStore(filepath.Join(data, "search"))
	if err != nil {
		log.Printf("search: %v", err)
		return func() {}
	}
	proxy := func(raw string) (string, error) {
		pr, err := m.ProxyFor("", raw, false)
		if err != nil || pr == nil {
			return "", err
		}
		return m.ProxyURL(pr.ID), nil
	}
	s := newService(st, newFetcher(srv.Version, proxy))
	// Plugins from another NAS run in phpbox's sandbox, out through its
	// relay with the same proxy rule; whether this NAS can run them is
	// checked in the background, not holding up the start
	box := phpbox.New(phpbox.Config{Dir: filepath.Join(data, "phpbox"), Version: srv.Version, Proxy: proxy})
	s.code = box
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if ok, code, _ := box.Check(ctx); !ok {
			log.Printf("search: plugins from another NAS cannot run here (%s)", code)
		}
	}()
	currentMu.Lock()
	current = s
	currentMu.Unlock()
	s.routes(srv)
	return s.Stop
}

// Counts are the numbers the usage statistics report (zero before
// Register): sources ready to search, custom plugins, Torznab sources and
// plugins from another NAS.
func Counts() (enabled, custom, torznab, code int) {
	currentMu.Lock()
	s := current
	currentMu.Unlock()
	if s == nil {
		return 0, 0, 0, 0
	}
	return s.store.Counts()
}

// Available reports whether any source is ready to search (false before
// Register).
func Available() bool {
	currentMu.Lock()
	s := current
	currentMu.Unlock()
	return s != nil && s.Available()
}

// Messages of the API besides the failure reasons and validation errors.
const (
	msgNoSources     = "No search source is enabled"
	msgSearchMissing = "Search not found"
	msgSourceMissing = "Search source not found"
	msgBuiltin       = "Built-in search sources cannot be deleted or replaced"
	msgLimit         = "There can be at most 50 search sources"
	msgExists        = "A search source with this id already exists"
	msgPluginFailed  = "Could not get the plugin: {0}"
	msgBodyNeeded    = "Give the plugin as JSON or as an https address"
	msgTorznabName   = "Enter a name for the Torznab server"
	msgTorznabURL    = "Enter the address of the Torznab server"
	msgChanged       = "The plugin at this address is not the one that was checked. Check it again."
	msgCheckFirst    = "Check the plugin at this address first, then install it with the sha256 that the check returned"
	labelServer      = "Server address"
)

// sourceView is a source as everyone with search:use sees it.
type sourceView struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Builtin    bool     `json:"builtin"`
	Categories []string `json:"categories"`
}

// settingView is a setting as administrators see it: never a secret's
// value, only whether it is set.
type settingView struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Secret   bool   `json:"secret"`
	Required bool   `json:"required"`
	Set      bool   `json:"set"`
	Value    string `json:"value,omitempty"`
}

// pluginView is a source in Settings › Search.
type pluginView struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Version     string        `json:"version,omitempty"`
	Description string        `json:"description,omitempty"`
	Homepage    string        `json:"homepage,omitempty"`
	Type        string        `json:"type"`
	Kind        string        `json:"kind"` // builtin | custom | torznab | dlm | qnap-addon
	Builtin     bool          `json:"builtin"`
	Enabled     bool          `json:"enabled"`
	NeedsSetup  bool          `json:"needs_setup"`
	Host        string        `json:"host"`
	Headers     []string      `json:"headers"`
	Settings    []settingView `json:"settings"`
	Categories  []string      `json:"categories"`
	Match       string        `json:"match,omitempty"`
	WebSeed     bool          `json:"webseed,omitempty"`
	TestQuery   string        `json:"test_query,omitempty"`
	// Who may search it, and whether a secret setting has a value
	Audience      string `json:"audience"`
	SecretBearing bool   `json:"secret_bearing"`
	// Plugins from another NAS: programs, who agreed to run them, and why
	// they cannot run when they cannot
	RunsCode bool         `json:"runs_code"`
	Consent  *Consent     `json:"consent,omitempty"`
	Runtime  *runtimeView `json:"runtime,omitempty"`
	Sites    []string     `json:"site,omitempty"`
	Key      string       `json:"key_fingerprint,omitempty"`
}

// runtimeView is whether plugins from another NAS can run here.
type runtimeView struct {
	OK     bool   `json:"ok"`
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

func runtimeOf(w http.ResponseWriter, ok bool, code, reason string) *runtimeView {
	if ok {
		return &runtimeView{OK: true}
	}
	return &runtimeView{Code: code, Reason: api.Tr(w, reason)}
}

func settingsView(w http.ResponseWriter, p *Plugin, values map[string]string) []settingView {
	out := []settingView{}
	for _, d := range p.SettingDefs() {
		v := settingView{ID: d.ID, Label: d.Label, Secret: d.Secret, Required: d.Required, Set: values[d.ID] != ""}
		if p.Type == TypeTorznab || p.RunsCode() {
			// Our own labels, not the plugin's
			v.Label = api.Tr(w, d.Label)
		}
		if !d.Secret {
			v.Value = values[d.ID]
		}
		out = append(out, v)
	}
	return out
}

func (s *Service) adminView(w http.ResponseWriter, src Source) pluginView {
	p := src.Plugin
	headers := p.HeaderNames()
	if headers == nil {
		headers = []string{}
	}
	v := pluginView{ID: p.ID, Name: p.Name, Version: p.Version, Description: p.Description, Homepage: p.Homepage,
		Type: p.Type, Kind: src.Kind(), Builtin: src.Builtin, Enabled: src.Enabled, NeedsSetup: !p.Complete(src.Settings),
		Host: p.Host(src.Settings), Headers: headers, Settings: settingsView(w, p, src.Settings),
		Categories: p.SupportedCategories(), Match: p.Match, WebSeed: p.WebSeed, TestQuery: p.TestQuery,
		Audience: src.Audience, SecretBearing: src.SecretBearing(), RunsCode: p.RunsCode()}
	if c := p.code; c != nil {
		consent := c.Consent
		v.Consent, v.Sites, v.Key = &consent, c.sites(false), c.KeyFingerprint
		if ok, code, reason := s.runtimeState(); !ok {
			v.Runtime = runtimeOf(w, ok, code, reason)
		}
	}
	return v
}

// pluginBody is how a plugin is given: its JSON (as text), an https
// address to get it from, or a Torznab server.
type pluginBody struct {
	JSON     string            `json:"json"`
	URL      string            `json:"url"`
	Torznab  *torznabBody      `json:"torznab"`
	Replace  bool              `json:"replace"`
	Settings map[string]string `json:"settings"`
	// A plugin from another NAS: the uploaded file (Base64), its name, and
	// the consent to run it, bound to the file's SHA-256 shown by inspect
	File       string `json:"file"`
	Filename   string `json:"filename"`
	AcceptRisk *bool  `json:"accept_risk"`
	SHA256     string `json:"sha256"`
}

// codePackage reads the uploaded file of a plugin from another NAS.
func codePackage(b *pluginBody) (*phpbox.Package, error) {
	if len(b.File) > (phpbox.MaxPackage/3+1)*4+8 {
		return nil, &phpbox.PackageError{Msg: phpbox.MsgTooLarge}
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b.File))
	if err != nil {
		return nil, &phpbox.PackageError{Msg: phpbox.MsgNotPlugin}
	}
	return phpbox.Inspect(b.Filename, data)
}

type torznabBody struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	APIKey string `json:"apikey"`
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := api.Decode(r, v); err != nil {
		api.Error(w, 400, "bad_request", err.Error())
		return false
	}
	return true
}

// failed answers an error of the search API.
func failed(w http.ResponseWriter, err error) {
	var ie *InvalidError
	var pe *phpbox.PackageError
	var f *Failure
	switch {
	case errors.As(err, &ie):
		api.Error(w, 400, "search_plugin_invalid", ie.Error())
	case errors.As(err, &pe):
		api.Error(w, 400, "search_plugin_invalid", pe.Error())
	case errors.Is(err, ErrNoAccount):
		api.Error(w, 400, "invalid_request", msgNoAccount)
	case errors.Is(err, ErrBadQuery), errors.Is(err, ErrBadCategory):
		api.Error(w, 400, "invalid_request", err.Error())
	case errors.Is(err, ErrNoSources):
		api.Error(w, 409, "search_no_sources", msgNoSources)
	case errors.Is(err, ErrRateLimited):
		api.Error(w, 429, "rate_limited", "Too many requests")
	case errors.Is(err, ErrNotFound):
		api.Error(w, 404, "not_found", msgSearchMissing)
	case errors.Is(err, ErrNoSource):
		api.Error(w, 404, "not_found", msgSourceMissing)
	case errors.Is(err, ErrBuiltin):
		api.Error(w, 409, "search_plugin_builtin", msgBuiltin)
	case errors.Is(err, ErrExists):
		api.Error(w, 409, "search_plugin_exists", msgExists)
	case errors.Is(err, ErrLimit):
		api.Error(w, 409, "search_plugin_limit", msgLimit)
	case errors.As(err, &f):
		api.Error(w, 502, "fetch_failed", strings.Replace(msgPluginFailed, "{0}", f.Reason, 1))
	default:
		log.Printf("search: %v", err)
		api.Error(w, 500, "internal", "Internal error")
	}
}

// plugin reads the plugin of a request: parsed from its JSON or fetched
// from its address. A Torznab body gives a new Torznab source (id from its
// name) with its settings. For a test (test) any id will do, a built-in
// one included: nothing is saved.
// For a plugin from an address, sum is the SHA-256 of what was fetched.
func (s *Service) plugin(ctx context.Context, b *pluginBody, test bool) (p *Plugin, values map[string]string, sum string, err error) {
	parse := ParseCustom
	if test {
		parse = Parse
	}
	switch {
	case b.Torznab != nil:
		name := strings.TrimSpace(b.Torznab.Name)
		if name == "" {
			return nil, nil, "", invalid(msgTorznabName)
		}
		if strings.TrimSpace(b.Torznab.URL) == "" {
			return nil, nil, "", invalid(msgTorznabURL)
		}
		p := &Plugin{Schema: SchemaVersion, ID: s.store.NewID(name), Name: name, Type: TypeTorznab}
		if err := Validate(p); err != nil {
			return nil, nil, "", err
		}
		values := map[string]string{"url": strings.TrimSpace(b.Torznab.URL), "apikey": strings.TrimSpace(b.Torznab.APIKey)}
		if err := p.CheckSettings(values); err != nil {
			return nil, nil, "", err
		}
		return p, values, "", nil
	case b.JSON != "":
		if len(b.JSON) > MaxPluginSize {
			return nil, nil, "", invalid("The plugin is larger than 64 KiB")
		}
		p, err := parse([]byte(b.JSON))
		return p, b.Settings, "", err
	case b.URL != "":
		data, err := s.FetchPlugin(ctx, b.URL)
		if err != nil {
			return nil, nil, "", err
		}
		h := sha256.Sum256(data)
		p, err := parse(data)
		return p, b.Settings, hex.EncodeToString(h[:]), err
	}
	return nil, nil, "", invalid(msgBodyNeeded)
}

func (s *Service) routes(srv *api.Server) {
	srv.MeHooks["search"] = func(p *auth.Principal) any {
		return map[string]any{"available": s.AvailableFor(p.Admin)}
	}

	// Searching: every account with search:use
	srv.Route("GET /search/sources", "search:use", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		out := []sourceView{}
		for _, src := range s.store.Sources() {
			if src.Ready() && src.Visible(p.Admin) && s.runnable(src) {
				out = append(out, sourceView{ID: src.Plugin.ID, Name: src.Plugin.Name, Builtin: src.Builtin, Categories: src.Plugin.SupportedCategories()})
			}
		}
		api.OK(w, map[string]any{"sources": out})
	})
	srv.Route("POST /search", "search:use", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		var b struct {
			Q        string   `json:"q"`
			Category string   `json:"category"`
			Sources  []string `json:"sources"`
		}
		if !decode(w, r, &b) {
			return
		}
		id, err := s.Start(p.User, p.Admin, Query{Text: b.Q, Category: b.Category}, b.Sources)
		if err != nil {
			failed(w, err)
			return
		}
		api.JSON(w, 202, map[string]any{"id": id})
	})
	srv.Route("GET /search/{id}", "search:use", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		v, err := s.View(p.User, r.PathValue("id"))
		if err != nil {
			failed(w, err)
			return
		}
		for i := range v.Sources {
			v.Sources[i].Error = api.Tr(w, v.Sources[i].Error)
		}
		api.OK(w, v)
	})
	srv.Route("DELETE /search/{id}", "search:use", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		if err := s.Cancel(p.User, r.PathValue("id")); err != nil {
			failed(w, err)
			return
		}
		api.OK(w, map[string]any{"ok": true})
	})
	srv.Route("GET /search/{id}/torrent/{ref}", "search:use", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*fetchTimeout)
		defer cancel()
		t, err := s.Torrent(ctx, p.User, p.Admin, r.PathValue("id"), r.PathValue("ref"))
		var f *Failure
		switch {
		case errors.As(err, &f):
			reason := f.Reason
			if reason == ReasonUnreadable || reason == ReasonTooLarge {
				reason = ReasonNotTorrent
			}
			api.Error(w, 502, "search_torrent_failed", reason)
			return
		case err != nil:
			failed(w, err)
			return
		case t.Magnet != "":
			api.OK(w, map[string]any{"magnet": t.Magnet})
			return
		}
		w.Header().Set("Content-Type", "application/x-bittorrent")
		w.Header().Set("Content-Disposition", `attachment; filename="`+t.InfoHash+`.torrent"`)
		w.WriteHeader(200)
		w.Write(t.Data)
	})

	// Managing the sources: administrators in the web UI only
	opts := api.AdminOnly | api.Session
	srv.Route("GET /search/plugins", "", opts, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		out := []pluginView{}
		for _, src := range s.store.Sources() {
			out = append(out, s.adminView(w, src))
		}
		ok, code, reason := s.runtimeState()
		api.OK(w, map[string]any{"sources": out, "limit": MaxSources, "runtime": runtimeOf(w, ok, code, reason)})
	})
	srv.Route("POST /search/plugins/inspect", "", opts, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		var b pluginBody
		if !decode(w, r, &b) {
			return
		}
		if b.Torznab != nil {
			failed(w, invalid(msgBodyNeeded))
			return
		}
		if b.File != "" {
			s.inspectCode(w, r, &b)
			return
		}
		pl, _, sum, err := s.plugin(r.Context(), &b, false)
		if err != nil {
			failed(w, err)
			return
		}
		old, exists := s.store.Get(pl.ID)
		if exists && old.Plugin.RunsCode() {
			// A plugin file cannot replace a program
			failed(w, ErrExists)
			return
		}
		headers := pl.HeaderNames()
		if headers == nil {
			headers = []string{}
		}
		api.OK(w, map[string]any{"id": pl.ID, "name": pl.Name, "version": pl.Version, "description": pl.Description,
			"homepage": pl.Homepage, "type": pl.Type, "match": pl.Match, "hosts": []string{pl.Host(nil)}, "headers": headers,
			"settings": settingsView(w, pl, nil), "categories": pl.SupportedCategories(), "exists": exists,
			"keeps_settings": exists && sameShape(old.Plugin, pl), "sha256": sum})
	})
	srv.Route("POST /search/plugins", "", opts, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		var b pluginBody
		if !decode(w, r, &b) {
			return
		}
		if b.File != "" {
			s.installCode(w, r, p, &b)
			return
		}
		fromURL := b.URL != "" && b.JSON == "" && b.Torznab == nil
		if fromURL && strings.TrimSpace(b.SHA256) == "" {
			api.Error(w, 400, "invalid_request", msgCheckFirst)
			return
		}
		pl, values, sum, err := s.plugin(r.Context(), &b, false)
		if err == nil && fromURL && !strings.EqualFold(strings.TrimSpace(b.SHA256), sum) {
			// What is installed from an address is what was checked
			api.Error(w, 409, "search_plugin_changed", msgChanged)
			return
		}
		if err == nil {
			// A Torznab server added with its form is on at once; a plugin
			// file (of any type) waits until an administrator switches it on
			err = s.store.Install(pl, b.Torznab != nil, b.Replace && b.Torznab == nil, values)
		}
		if err != nil {
			failed(w, err)
			return
		}
		src, _ := s.store.Get(pl.ID)
		api.OK(w, s.adminView(w, src))
	})
	srv.Route("POST /search/plugins/test", "", opts, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		var b pluginBody
		if !decode(w, r, &b) {
			return
		}
		pl, values, _, err := s.plugin(r.Context(), &b, true)
		if err != nil {
			failed(w, err)
			return
		}
		// A new version of an installed plugin is tested with the
		// installed one's settings unless others are given, and only when
		// they would carry over (same address, same secret settings)
		if src, ok := s.store.Get(pl.ID); ok && b.Torznab == nil && values == nil && sameShape(src.Plugin, pl) {
			values = src.Settings
		}
		s.answerTest(w, r, Source{Plugin: pl, Enabled: true, Settings: values})
	})
	srv.Route("PUT /search/plugins/{id}", "", opts, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		var b struct {
			Enabled  *bool             `json:"enabled"`
			Settings map[string]string `json:"settings"`
			Name     *string           `json:"name"`
			Audience *string           `json:"audience"`
		}
		if !decode(w, r, &b) {
			return
		}
		id := r.PathValue("id")
		if b.Name != nil && utf8.RuneCountInString(strings.TrimSpace(*b.Name)) == 0 {
			failed(w, invalid(msgTorznabName))
			return
		}
		if b.Audience != nil && *b.Audience != AudienceAll && *b.Audience != AudienceAdmins {
			api.Error(w, 400, "invalid_request", invalid(msgUnsupported, "audience", *b.Audience).Error())
			return
		}
		if err := s.store.Update(id, b.Enabled, b.Settings, b.Name); err != nil {
			failed(w, err)
			return
		}
		if b.Audience != nil {
			if err := s.store.SetAudience(id, *b.Audience); err != nil {
				failed(w, err)
				return
			}
		}
		src, _ := s.store.Get(id)
		api.OK(w, s.adminView(w, src))
	})
	srv.Route("DELETE /search/plugins/{id}", "", opts, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		if err := s.store.Delete(r.PathValue("id")); err != nil {
			failed(w, err)
			return
		}
		api.OK(w, map[string]any{"ok": true})
	})
	srv.Route("POST /search/plugins/{id}/test", "", opts, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		src, ok := s.store.Get(r.PathValue("id"))
		if !ok {
			failed(w, ErrNoSource)
			return
		}
		s.answerTest(w, r, src)
	})
	srv.Route("POST /search/plugins/{id}/verify", "", opts, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		src, ok := s.store.Get(r.PathValue("id"))
		if !ok {
			failed(w, ErrNoSource)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), testTimeout)
		defer cancel()
		good, err := s.VerifyAccount(ctx, src)
		if errors.Is(err, ErrNoAccount) {
			failed(w, err)
			return
		}
		out := map[string]any{"ok": err == nil && good}
		if err != nil {
			out["error"] = api.Tr(w, reasonOf(err))
		}
		api.OK(w, out)
	})
	// The definition of a source, to save as a file. (Not .../{id}/export:
	// the router cannot tell that from GET /search/{id}/torrent/{ref}.)
	srv.Route("GET /search/plugins/{id}", "", opts, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		src, ok := s.store.Get(r.PathValue("id"))
		if !ok {
			failed(w, ErrNoSource)
			return
		}
		if src.Plugin.RunsCode() {
			api.Error(w, 400, "invalid_request", msgNoExport)
			return
		}
		b, err := src.Plugin.Marshal()
		if err != nil {
			failed(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+src.Plugin.ID+`.json"`)
		w.WriteHeader(200)
		w.Write(b)
	})
}

// testTimeout bounds a test or an account check: a program may take its
// 30 seconds, after waiting for one of the two places to run.
const testTimeout = 45 * time.Second

// inspectCode checks an uploaded plugin from another NAS without keeping
// or running anything, and says whether this NAS can run it.
func (s *Service) inspectCode(w http.ResponseWriter, r *http.Request, b *pluginBody) {
	pkg, err := codePackage(b)
	if err != nil {
		failed(w, err)
		return
	}
	ok, code, reason := s.checkRuntime(r.Context())
	id := codeID(pkg)
	installed, exists := s.store.codeFor(id)
	c := codeFromPackage(pkg, Consent{})
	p := codePlugin(id, c)
	sites := c.sites(false)
	if sites == nil {
		sites = []string{}
	}
	out := map[string]any{"id": id, "kind": p.Type, "runs_code": true, "sha256": pkg.SHA256, "name": p.Name, "version": pkg.Version,
		"description": pkg.Description, "site": sites, "account": pkg.Account, "exists": exists,
		"key_changed": exists && installed.Format == phpbox.FormatAddon && installed.KeyFingerprint != pkg.KeyFingerprint,
		"runtime":     runtimeOf(w, ok, code, reason)}
	if pkg.KeyFingerprint != "" {
		out["key_fingerprint"] = pkg.KeyFingerprint
	}
	if !exists {
		// Another kind of source with this id cannot be replaced
		if _, taken := s.store.Get(id); taken {
			failed(w, ErrExists)
			return
		}
	}
	api.OK(w, out)
}

// installCode installs an uploaded plugin from another NAS: only with the
// administrator's consent to run this exact file, and only where it can
// run. It starts disabled.
func (s *Service) installCode(w http.ResponseWriter, r *http.Request, p *auth.Principal, b *pluginBody) {
	pkg, err := codePackage(b)
	if err != nil {
		failed(w, err)
		return
	}
	if b.AcceptRisk == nil || !*b.AcceptRisk || !strings.EqualFold(strings.TrimSpace(b.SHA256), pkg.SHA256) {
		api.Error(w, 400, "search_consent_required", msgConsent)
		return
	}
	if ok, _, reason := s.checkRuntime(r.Context()); !ok {
		api.Error(w, 409, "search_runtime_unavailable", reason)
		return
	}
	id, err := s.store.InstallCode(pkg, Consent{User: p.User, Time: s.now().Unix()}, b.Replace)
	if err != nil {
		failed(w, err)
		return
	}
	src, _ := s.store.Get(id)
	api.OK(w, s.adminView(w, src))
}

// answerTest asks a source once: {ok, count} or {ok: false, error}.
func (s *Service) answerTest(w http.ResponseWriter, r *http.Request, src Source) {
	ctx, cancel := context.WithTimeout(r.Context(), testTimeout)
	defer cancel()
	start := time.Now()
	n, err := s.Test(ctx, src)
	out := map[string]any{"ok": err == nil, "count": n, "ms": time.Since(start).Milliseconds()}
	if err != nil {
		out["error"] = api.Tr(w, reasonOf(err))
	}
	api.OK(w, out)
}
