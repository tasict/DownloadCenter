// Package search finds torrents by keyword through search plugins. A plugin
// is a declarative JSON definition that dcd interprets: one GET to a fixed
// host, and how to read the fields of each result out of the JSON, RSS or
// Atom answer. The torznab type needs no definition beyond its id and name:
// the administrator gives the server address and API key, and the answer is
// read with the Torznab rules. A plugin cannot run code, choose another
// method, reach another host or read another plugin's settings.
//
// This file holds the definition and its validation; request.go builds the
// request, response.go and feed.go read the answer, result.go normalises
// and merges the results, builtin.go loads the definitions built in.
package search

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"downloadcenter/internal/netutil"
)

// Limits of a definition and of what is read from an answer.
const (
	SchemaVersion = 1        // the only plugin schema there is
	MaxPluginSize = 64 << 10 // bytes of a plugin file
	MaxResults    = 100      // results taken from one source per search
	MaxTitle      = 300      // characters of a result title
	MaxTrackers   = 20       // trackers in a rebuilt magnet link
	MaxItems      = 5000     // items read from one answer (catalogue feeds included)

	maxURL      = 2048 // characters of a request, result or homepage address
	maxHeaders  = 10
	maxSettings = 10
	maxValue    = 1024 // characters of a header or setting value
)

// Plugin types.
const (
	TypeJSON    = "json"
	TypeRSS     = "rss" // RSS 2.0, RSS 1.0 and Atom (OPDS included)
	TypeTorznab = "torznab"
	// Plugins from another NAS: programs run by phpbox (never in a plugin
	// file: Validate refuses these types)
	TypeDLM   = "dlm"
	TypeAddon = "qnap-addon"
)

// Categories are the result categories, in display order. A search asks for
// one of them or for CategoryAll.
var Categories = []string{"video", "audio", "software", "books", "other"}

// CategoryAll asks every category.
const CategoryAll = "all"

// Plugin is a search plugin definition. Field names are the JSON keys of the
// plugin file; the file may not have any other key.
type Plugin struct {
	Schema      int    `json:"schema"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
	Homepage    string `json:"homepage,omitempty"`
	Type        string `json:"type"`
	// Match is "" (the site matches the keyword) or "local": the source has
	// no keyword search, dcd fetches its whole catalogue and keeps the items
	// whose title has every word of the keyword.
	Match string `json:"match,omitempty"`
	// Prefer is "magnet" (or empty) or "torrent": a result that has both an
	// infohash and a .torrent address is offered as a .torrent only (sources
	// whose torrents rely on web seeds: a magnet link would never get the
	// file list).
	Prefer string `json:"prefer,omitempty"`
	// WebSeed: the source's torrents carry web seeds, so they download even
	// without seeders.
	WebSeed bool `json:"webseed,omitempty"`
	// DefaultEnabled is whether a built-in source starts enabled (nil:
	// true). Sources an administrator adds always start disabled.
	DefaultEnabled *bool             `json:"default_enabled,omitempty"`
	Request        *Request          `json:"request,omitempty"`
	Settings       []Setting         `json:"settings,omitempty"`
	Categories     map[string]string `json:"categories,omitempty"`
	TestQuery      string            `json:"test_query,omitempty"`
	Response       *Response         `json:"response,omitempty"`

	// code describes a plugin from another NAS (nil for the others)
	code *codeState
}

// RunsCode reports a plugin from another NAS: a program, not a
// description.
func (p *Plugin) RunsCode() bool { return p.code != nil }

// Request is the GET a json or rss plugin makes. URL is a template (see
// template.go); every variable is replaced URL-query-escaped.
type Request struct {
	URL string `json:"url"`
	// Headers are sent as they are written; a value may use {setting:<id>}
	// (put in as it is, setting values cannot hold control characters).
	Headers map[string]string `json:"headers,omitempty"`
	// Strip lists characters taken out of the keyword (each becomes a
	// space) before it goes into the address, for query languages that
	// treat them as operators.
	Strip string `json:"strip,omitempty"`
}

// Setting is a value the administrator fills in for a plugin.
type Setting struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Secret   bool   `json:"secret,omitempty"`
	Required bool   `json:"required,omitempty"`
}

// Response says how to read a json or rss answer.
type Response struct {
	// Items is the path of the result list in a json answer ("" for an
	// answer that is the list itself); rss answers use their items or
	// entries and leave it empty.
	Items string `json:"items,omitempty"`
	// Require lists values (paths, or rss value names) an item must have:
	// an item where one of them is missing, null, false, 0 or empty is
	// skipped (a catalogue that lists releases no longer offered).
	Require []string `json:"require,omitempty"`
	Fields  Fields   `json:"fields"`
}

// Fields maps the parts of a result to values of an item.
type Fields struct {
	Title    *Field `json:"title,omitempty"`
	Size     *Field `json:"size,omitempty"`
	InfoHash *Field `json:"infohash,omitempty"`
	Magnet   *Field `json:"magnet,omitempty"`
	Torrent  *Field `json:"torrent,omitempty"`
	Page     *Field `json:"page,omitempty"`
	Date     *Field `json:"date,omitempty"`
	Seeders  *Field `json:"seeders,omitempty"`
	Leechers *Field `json:"leechers,omitempty"`
	Category *Field `json:"category,omitempty"`
}

// Field is where a value comes from: a path (json) or value name (rss), or
// a template when it holds a "{". In a plugin file it is that string, or
// {"from": "...", "replace": ["old", "new"]} to replace the first
// occurrence of old in the value once (no patterns).
type Field struct {
	From    string
	Replace []string
}

// UnmarshalJSON reads either form, refusing unknown keys in the object.
func (f *Field) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		f.Replace = nil
		return json.Unmarshal(b, &f.From)
	}
	var o struct {
		From    string   `json:"from"`
		Replace []string `json:"replace"`
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&o); err != nil {
		return err
	}
	f.From, f.Replace = o.From, o.Replace
	return nil
}

// MarshalJSON writes the short form when there is nothing to replace.
func (f Field) MarshalJSON() ([]byte, error) {
	var v any = f.From
	if f.Replace != nil {
		v = struct {
			From    string   `json:"from"`
			Replace []string `json:"replace"`
		}{f.From, f.Replace}
	}
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(b.Bytes()), nil
}

// InvalidError is a definition that fails validation. Msg is a fixed English
// sentence whose variable parts {0}, {1} are Args (field names and values
// from the definition); Error() is the sentence with them put in, the form
// the backend dictionaries translate.
type InvalidError struct {
	Msg  string
	Args []string
}

func (e *InvalidError) Error() string {
	s := e.Msg
	for i, a := range e.Args {
		s = strings.Replace(s, "{"+strconv.Itoa(i)+"}", a, 1)
	}
	return s
}

func invalid(msg string, args ...string) error { return &InvalidError{Msg: msg, Args: args} }

// Messages used for many fields; the argument is the field's path in the
// definition ("request.url", "response.fields.title").
const (
	msgMissing     = "The field {0} is missing"
	msgTooLong     = "The field {0} is too long"
	msgNotValid    = "The field {0} is not valid"
	msgUnsupported = "The field {0} has an unsupported value: {1}"
	msgCannotHave  = "A source of type {0} cannot have the field {1}"
)

// ErrExists is a plugin whose id is already taken (a built-in source's id,
// or another plugin's when the store checks it).
var ErrExists = errors.New("A search source with this id already exists")

// Parse reads and validates a plugin file. Unknown keys, anything after
// the object and files over MaxPluginSize are refused.
func Parse(b []byte) (*Plugin, error) {
	if len(b) > MaxPluginSize {
		return nil, invalid("The plugin is larger than 64 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	var p Plugin
	if err := d.Decode(&p); err != nil {
		if name, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
			return nil, invalid("Unknown field {0}", name)
		}
		return nil, invalid("The plugin is not valid JSON: {0}", strings.TrimPrefix(err.Error(), "json: "))
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, invalid("The plugin is not valid JSON: {0}", "data after the object")
	}
	if err := Validate(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ParseCustom is Parse for a plugin an administrator adds: its id may not
// be a built-in source's (ErrExists).
func ParseCustom(b []byte) (*Plugin, error) {
	p, err := Parse(b)
	if err != nil {
		return nil, err
	}
	if IsBuiltin(p.ID) {
		return nil, ErrExists
	}
	return p, nil
}

// Marshal is the definition as it is stored and exported (addresses
// written as they are, without escaping & < >).
func (p *Plugin) Marshal() ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	e.SetIndent("", "  ")
	if err := e.Encode(p); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

var (
	idRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,39}$`)
	settingRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	tokenRe   = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]{1,64}$")
	hostRe    = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*\.?$`)
)

// forbiddenHeaders may not be set by a plugin: they belong to the
// connection, to dcd (User-Agent, Accept-Encoding) or to a proxy.
var forbiddenHeaders = map[string]bool{
	"host": true, "cookie": true, "cookie2": true, "content-length": true, "transfer-encoding": true,
	"connection": true, "keep-alive": true, "upgrade": true, "te": true, "trailer": true, "expect": true,
	"forwarded": true, "user-agent": true, "accept-encoding": true,
}

// Validate checks a definition. Built-in definitions go through it as well.
func Validate(p *Plugin) error {
	if p.Schema != SchemaVersion {
		return invalid(msgUnsupported, "schema", strconv.Itoa(p.Schema))
	}
	if !idRe.MatchString(p.ID) {
		return invalid("The id must be 2 to 40 lower-case letters, digits or hyphens, starting with a letter or digit")
	}
	if err := text("name", p.Name, 60, true); err != nil {
		return err
	}
	if err := text("version", p.Version, 20, false); err != nil {
		return err
	}
	if err := text("description", p.Description, 500, false); err != nil {
		return err
	}
	if err := text("test_query", p.TestQuery, 200, false); err != nil {
		return err
	}
	if p.Homepage != "" && !httpURL(p.Homepage) {
		return invalid(msgNotValid, "homepage")
	}
	switch p.Prefer {
	case "", "magnet", "torrent":
	default:
		return invalid(msgUnsupported, "prefer", p.Prefer)
	}
	switch p.Type {
	case TypeTorznab:
		return validateTorznab(p)
	case TypeJSON, TypeRSS:
	default:
		return invalid(msgUnsupported, "type", p.Type)
	}
	switch p.Match {
	case "", "local":
	default:
		return invalid(msgUnsupported, "match", p.Match)
	}
	if err := validateSettings(p.Settings); err != nil {
		return err
	}
	if err := validateCategories(p.Categories); err != nil {
		return err
	}
	if p.Request == nil {
		return invalid(msgMissing, "request.url")
	}
	if err := validateRequest(p); err != nil {
		return err
	}
	if p.Response == nil {
		return invalid("The field mapping must give a title")
	}
	return validateResponse(p)
}

func validateTorznab(p *Plugin) error {
	switch {
	case p.Request != nil:
		return invalid(msgCannotHave, "torznab", "request")
	case p.Response != nil:
		return invalid(msgCannotHave, "torznab", "response")
	case p.Settings != nil:
		return invalid(msgCannotHave, "torznab", "settings")
	case p.Categories != nil:
		return invalid(msgCannotHave, "torznab", "categories")
	case p.Match != "":
		return invalid(msgCannotHave, "torznab", "match")
	}
	return nil
}

// text checks a plain text field: length in characters, no control
// characters, present when required.
func text(field, s string, max int, required bool) error {
	if required && strings.TrimSpace(s) == "" {
		return invalid(msgMissing, field)
	}
	if utf8.RuneCountInString(s) > max {
		return invalid(msgTooLong, field)
	}
	if !printable(s) {
		return invalid(msgNotValid, field)
	}
	return nil
}

func printable(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return false
		}
	}
	return true
}

// httpURL reports an absolute http or https address with a host.
func httpURL(s string) bool {
	if len(s) > maxURL || !printable(s) || strings.ContainsAny(s, " ") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.Opaque == ""
}

func validateSettings(list []Setting) error {
	if len(list) > maxSettings {
		return invalid(msgTooLong, "settings")
	}
	seen := map[string]bool{}
	for _, s := range list {
		if !settingRe.MatchString(s.ID) {
			return invalid(msgNotValid, "settings."+s.ID)
		}
		if seen[s.ID] {
			return invalid(msgNotValid, "settings."+s.ID)
		}
		seen[s.ID] = true
		if err := text("settings."+s.ID+".label", s.Label, 60, true); err != nil {
			return err
		}
	}
	return nil
}

func validateCategories(m map[string]string) error {
	for k, v := range m {
		if !validCategory(k) {
			return invalid(msgUnsupported, "categories", k)
		}
		if err := text("categories."+k, v, 200, false); err != nil {
			return err
		}
	}
	return nil
}

func validCategory(c string) bool {
	for _, k := range Categories {
		if c == k {
			return true
		}
	}
	return false
}

// requestVars are the variables of a request address besides {setting:<id>}.
var requestVars = map[string]bool{"query": true, "limit": true, "page": true, "category": true}

func validateRequest(p *Plugin) error {
	r := p.Request
	raw := r.URL
	if raw == "" {
		return invalid(msgMissing, "request.url")
	}
	if len(raw) > maxURL || !printable(raw) || strings.ContainsAny(raw, " #\\") {
		return invalid("The request address is not valid")
	}
	t, err := parseTemplate(raw)
	if err != nil {
		return invalid("The request address is not valid")
	}
	settings := map[string]bool{}
	for _, s := range p.Settings {
		settings[s.ID] = true
	}
	hasQuery := false
	for _, v := range t.vars() {
		if id, ok := strings.CutPrefix(v, "setting:"); ok && settings[id] {
			continue
		}
		if !requestVars[v] {
			return invalid("Unknown variable {0}", v)
		}
		hasQuery = hasQuery || v == "query"
	}
	if p.Match == "local" && hasQuery {
		return invalid("The request address of a source matched on the NAS cannot contain the search keyword")
	}
	if p.Match == "" && !hasQuery {
		return invalid("The request address must contain the search keyword")
	}
	rest, ok := strings.CutPrefix(raw, "https://")
	if !ok {
		return invalid("The request address must use https")
	}
	// The host is the literal text up to the path or query: no variable,
	// no user name, nothing a value could change.
	authority := rest
	if i := strings.IndexAny(rest, "/?"); i >= 0 {
		authority = rest[:i]
	}
	if strings.ContainsAny(authority, "{}") {
		return invalid("The host name of the request address must be written out, not taken from a variable")
	}
	if strings.Contains(authority, "@") {
		return invalid("The request address is not valid")
	}
	if strings.HasPrefix(authority, "[") {
		return invalid("The request address must name a host, not an IP address or localhost")
	}
	host := authority
	if h, port, err := splitPort(authority); err == nil {
		host = h
		if port != "" {
			if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
				return invalid("The request address is not valid")
			}
		}
	} else {
		return invalid("The request address is not valid")
	}
	if host == "" || !hostRe.MatchString(host) || netutil.HostIP(strings.TrimSuffix(host, ".")) != nil || isLocalhost(host) {
		return invalid("The request address must name a host, not an IP address or localhost")
	}
	// Put sample values in and check that the address still parses to the
	// same host.
	sample := t.expand(func(string) string { return url.QueryEscape("a.b-c/d?e#f") })
	u, err := url.Parse(sample)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || !strings.EqualFold(u.Hostname(), host) {
		return invalid("The host name of the request address must be written out, not taken from a variable")
	}
	if len(r.Headers) > maxHeaders {
		return invalid(msgTooLong, "request.headers")
	}
	for name, value := range r.Headers {
		lname := strings.ToLower(name)
		if !tokenRe.MatchString(name) {
			return invalid(msgNotValid, "request.headers."+name)
		}
		if forbiddenHeaders[lname] || strings.HasPrefix(lname, "proxy-") || strings.HasPrefix(lname, "x-forwarded-") {
			return invalid("The header {0} is not allowed", name)
		}
		if len(value) > maxValue || !printable(value) {
			return invalid(msgNotValid, "request.headers."+name)
		}
		ht, err := parseTemplate(value)
		if err != nil {
			return invalid(msgNotValid, "request.headers."+name)
		}
		for _, v := range ht.vars() {
			if id, ok := strings.CutPrefix(v, "setting:"); !ok || !settings[id] {
				return invalid("Unknown variable {0}", v)
			}
		}
	}
	return text("request.strip", r.Strip, 64, false)
}

// splitPort splits "host" or "host:port".
func splitPort(a string) (string, string, error) {
	i := strings.LastIndexByte(a, ':')
	if i < 0 {
		return a, "", nil
	}
	if strings.Contains(a[:i], ":") {
		return "", "", errors.New("bad host")
	}
	return a[:i], a[i+1:], nil
}

func isLocalhost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	return h == "localhost" || strings.HasSuffix(h, ".localhost")
}

// rssNames are the values of an rss item a field can name, besides
// attr:<name> (a torznab or newznab attribute) and ns:<name> (the text of
// the first child element with that local name, whatever its namespace).
var rssNames = map[string]bool{
	"title": true, "link": true, "guid": true, "pubDate": true, "enclosure.url": true, "enclosure.length": true,
}

var (
	nsNameRe  = regexp.MustCompile(`^(attr|ns):[A-Za-z_][A-Za-z0-9_.-]{0,63}$`)
	pathSegRe = regexp.MustCompile(`^[^.{}\s\x00-\x1f\x7f]{1,100}$`)
)

// validSource checks a path (json) or value name (rss).
func validSource(typ, s string) bool {
	if typ == TypeRSS {
		return rssNames[s] || nsNameRe.MatchString(s)
	}
	_, err := parsePath(s)
	return err == nil && s != ""
}

// parsePath splits a json path: names separated by dots, a number indexes
// a list. "" is the answer itself.
func parsePath(s string) ([]string, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ".")
	if len(parts) > 10 {
		return nil, errors.New("path too deep")
	}
	for _, p := range parts {
		if !pathSegRe.MatchString(p) {
			return nil, errors.New("bad path")
		}
	}
	return parts, nil
}

func validateResponse(p *Plugin) error {
	r := p.Response
	if p.Type == TypeRSS && r.Items != "" {
		return invalid(msgCannotHave, "rss", "response.items")
	}
	if _, err := parsePath(r.Items); err != nil {
		return invalid("The path {0} is not valid", r.Items)
	}
	if len(r.Require) > 10 {
		return invalid(msgTooLong, "response.require")
	}
	for _, s := range r.Require {
		if !validSource(p.Type, s) {
			return invalid("The path {0} is not valid", s)
		}
	}
	f := r.Fields
	if f.Title == nil {
		return invalid("The field mapping must give a title")
	}
	if f.InfoHash == nil && f.Magnet == nil && f.Torrent == nil {
		return invalid("The field mapping must give an infohash, a magnet link or a .torrent address")
	}
	for _, nf := range f.list() {
		if err := validateField(p.Type, nf.name, nf.f); err != nil {
			return err
		}
	}
	return nil
}

type namedField struct {
	name string
	f    *Field
}

// list is the fields that are set, in a fixed order.
func (f *Fields) list() []namedField {
	all := []namedField{
		{"title", f.Title}, {"size", f.Size}, {"infohash", f.InfoHash}, {"magnet", f.Magnet}, {"torrent", f.Torrent},
		{"page", f.Page}, {"date", f.Date}, {"seeders", f.Seeders}, {"leechers", f.Leechers}, {"category", f.Category},
	}
	out := all[:0]
	for _, nf := range all {
		if nf.f != nil {
			out = append(out, nf)
		}
	}
	return out
}

func validateField(typ, name string, f *Field) error {
	if f.From == "" || len(f.From) > 512 || !printable(f.From) {
		return invalid(msgNotValid, "response.fields."+name)
	}
	if f.Replace != nil {
		if len(f.Replace) != 2 || f.Replace[0] == "" {
			return invalid(msgNotValid, "response.fields."+name+".replace")
		}
		for _, s := range f.Replace {
			if len(s) > 200 || !printable(s) {
				return invalid(msgNotValid, "response.fields."+name+".replace")
			}
		}
	}
	if !strings.ContainsAny(f.From, "{}") {
		if !validSource(typ, f.From) {
			return invalid("The path {0} is not valid", f.From)
		}
		return nil
	}
	t, err := parseTemplate(f.From)
	if err != nil {
		return invalid(msgNotValid, "response.fields."+name)
	}
	vars := t.vars()
	if len(vars) == 0 || len(vars) > 10 {
		return invalid(msgNotValid, "response.fields."+name)
	}
	for _, v := range vars {
		if !validSource(typ, v) {
			return invalid("Unknown variable {0}", v)
		}
	}
	return nil
}

// EnabledByDefault reports whether a built-in source starts enabled.
func (p *Plugin) EnabledByDefault() bool { return p.DefaultEnabled == nil || *p.DefaultEnabled }

// SettingDefs are the settings an administrator fills in: the plugin's, or
// for a torznab source the server address and the API key.
func (p *Plugin) SettingDefs() []Setting {
	if p.Type == TypeTorznab {
		return []Setting{
			{ID: "url", Label: "Server address", Required: true},
			{ID: "apikey", Label: "API key", Secret: true},
		}
	}
	return p.Settings
}

// CheckSettings checks values for the settings: only declared ids, no
// control characters, at most 1024 characters, and for a torznab source a
// server address that is an http or https URL without user name or
// fragment. Missing required values are not an error here (see Complete).
func (p *Plugin) CheckSettings(values map[string]string) error {
	defs := map[string]bool{}
	for _, s := range p.SettingDefs() {
		defs[s.ID] = true
	}
	for id, v := range values {
		if !defs[id] {
			return invalid("Unknown setting {0}", id)
		}
		if len(v) > maxValue || !printable(v) {
			return invalid(msgNotValid, "settings."+id)
		}
	}
	if p.Type == TypeTorznab {
		if v := values["url"]; v != "" {
			if _, err := torznabBase(v); err != nil {
				return err
			}
		}
	}
	return nil
}

// Complete reports whether every required setting has a value.
func (p *Plugin) Complete(values map[string]string) bool {
	for _, s := range p.SettingDefs() {
		if s.Required && values[s.ID] == "" {
			return false
		}
	}
	return true
}

// SecretValues are the values of the secret settings that are set: they
// must not reach a log, an error message or an API answer.
func (p *Plugin) SecretValues(values map[string]string) []string {
	var out []string
	for _, s := range p.SettingDefs() {
		if v := values[s.ID]; s.Secret && v != "" {
			out = append(out, v)
		}
	}
	return out
}

// Host is the host the plugin connects to (for a torznab source, the one
// in its server address setting; "" when there is none yet).
func (p *Plugin) Host(values map[string]string) string {
	if p.code != nil {
		return strings.Join(p.code.sites(true), ", ")
	}
	raw := ""
	if p.Type == TypeTorznab {
		raw = values["url"]
	} else if p.Request != nil {
		raw = strings.SplitN(strings.TrimPrefix(p.Request.URL, "https://"), "/", 2)[0]
		raw = "https://" + strings.SplitN(raw, "?", 2)[0]
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// HeaderNames are the names of the headers the plugin sends, sorted.
func (p *Plugin) HeaderNames() []string {
	var out []string
	if p.Request != nil {
		for k := range p.Request.Headers {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// SupportedCategories are the categories the source has results in: the
// keys of its categories map, or all of them when it has none (torznab
// sources always have all).
func (p *Plugin) SupportedCategories() []string {
	if p.Type == TypeTorznab || len(p.Categories) == 0 {
		return append([]string{}, Categories...)
	}
	var out []string
	for _, c := range Categories {
		if _, ok := p.Categories[c]; ok {
			out = append(out, c)
		}
	}
	return out
}

// Asks reports whether a search in category (CategoryAll or "" for every
// category) goes to this source.
func (p *Plugin) Asks(category string) bool {
	if category == "" || category == CategoryAll {
		return true
	}
	for _, c := range p.SupportedCategories() {
		if c == category {
			return true
		}
	}
	return false
}

// ValidCategory checks the category of a search ("" means all).
func ValidCategory(category string) bool {
	return category == "" || category == CategoryAll || validCategory(category)
}
