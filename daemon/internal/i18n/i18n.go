// Package i18n translates the user-facing messages of the backend. Messages
// are written in English in the code (the source language of the UI as
// well); lang/<code>.json maps each source string, or a template with {0},
// {1}... for the parts that vary, to the translation. Language codes are
// QTS's (TCH, SCH, JPN ...); English, languages without a dictionary and
// strings without a translation come out as written.
//
// Versions up to 1.0.x wrote their messages in Traditional Chinese, and the
// database keeps what they wrote (task errors and logs, account errors). Such
// a message is first mapped back to its English source through the TCH
// dictionary, so it still comes out in the reader's language.
//
// Only the backend's own messages (backend-keys.json) are looked up: the
// UI strings in the same dictionaries are for the browser, and their short
// templates could match unrelated messages.
package i18n

import (
	"embed"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
)

//go:embed lang/*.json backend-keys.json
var files embed.FS

// Source is the language the messages are written in.
const Source = "ENG"

// legacyLang is the language of the messages written by versions up to 1.0.x.
const legacyLang = "TCH"

type template struct {
	key   string
	re    *regexp.Regexp
	names []string
	bare  bool // the fixed text is punctuation only, as in "{0}: {1}"
}

type dict struct {
	exact     map[string]string
	templates []template
}

var (
	mu        sync.Mutex
	dicts     = map[string]*dict{}
	backend   map[string]bool
	legacy    *dict             // TCH reversed: Traditional Chinese -> English
	legacySrc map[string]string // tests: replaces the TCH dictionary for legacy lookups
	phRe      = regexp.MustCompile(`\{(\w+)\}`)
)

// Aliases maps QTS codes without their own dictionary.
var aliases = map[string]string{"ESM": "SPA"}

// rawDict is a shipped dictionary as it is, UI strings included.
func rawDict(lang string) map[string]string {
	var m map[string]string
	if b, err := files.ReadFile("lang/" + lang + ".json"); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

// readDict is the part of a shipped dictionary with the backend's messages
// (called with mu held).
func readDict(lang string) map[string]string {
	if backend == nil {
		var keys []string
		b, _ := files.ReadFile("backend-keys.json")
		json.Unmarshal(b, &keys)
		backend = map[string]bool{}
		for _, k := range keys {
			backend[k] = true
		}
	}
	m := map[string]string{}
	for k, v := range rawDict(lang) {
		if backend[k] {
			m[k] = v
		}
	}
	return m
}

func load(lang string) *dict {
	mu.Lock()
	defer mu.Unlock()
	if d, ok := dicts[lang]; ok {
		return d
	}
	d := build(readDict(lang))
	dicts[lang] = d
	return d
}

func loadLegacy() *dict {
	mu.Lock()
	defer mu.Unlock()
	if legacy == nil {
		src := legacySrc
		if src == nil {
			src = readDict(legacyLang)
		}
		rev := map[string]string{}
		for en, zh := range src {
			if zh != "" {
				rev[zh] = en
			}
		}
		legacy = build(rev)
	}
	return legacy
}

func build(m map[string]string) *dict {
	d := &dict{exact: map[string]string{}}
	for k, v := range m {
		if v == "" {
			continue
		}
		if phRe.MatchString(k) {
			d.templates = append(d.templates, compile(k))
		}
		d.exact[k] = v
	}
	// The template with the most fixed text wins ("Error: {0}" before "{0}: {1}")
	fixed := func(k string) int { return len(phRe.ReplaceAllString(k, "")) }
	sort.Slice(d.templates, func(i, j int) bool {
		fi, fj := fixed(d.templates[i].key), fixed(d.templates[j].key)
		if fi != fj {
			return fi > fj
		}
		return d.templates[i].key < d.templates[j].key
	})
	return d
}

// setDict installs a dictionary in memory (tests).
func setDict(lang string, m map[string]string) {
	mu.Lock()
	dicts[lang] = build(m)
	if lang == legacyLang {
		legacy, legacySrc = nil, m
	}
	mu.Unlock()
}

func compile(key string) template {
	var names []string
	var b strings.Builder
	b.WriteString("^")
	last := 0
	for _, m := range phRe.FindAllStringSubmatchIndex(key, -1) {
		b.WriteString(regexp.QuoteMeta(key[last:m[0]]))
		b.WriteString("(.+?)")
		names = append(names, key[m[2]:m[3]])
		last = m[1]
	}
	b.WriteString(regexp.QuoteMeta(key[last:]))
	b.WriteString("$")
	bare := !strings.ContainsFunc(phRe.ReplaceAllString(key, ""), unicode.IsLetter)
	return template{key: key, re: regexp.MustCompile(b.String()), names: names, bare: bare}
}

// known reports whether lang has a dictionary.
func known(lang string) bool {
	mu.Lock()
	_, ok := dicts[lang]
	mu.Unlock()
	if ok {
		return true
	}
	_, err := files.ReadFile("lang/" + lang + ".json")
	return err == nil
}

// Norm turns a request's language into a dictionary code: Source for an
// empty, English or unknown language.
func Norm(lang string) string {
	lang = strings.ToUpper(strings.TrimSpace(lang))
	if a, ok := aliases[lang]; ok {
		lang = a
	}
	if lang == "" || lang == Source || !known(lang) {
		return Source
	}
	return lang
}

// T translates a message. Variable parts of a template are translated too
// when they are messages themselves (e.g. an error inside "Error: {0}").
func T(lang, msg string) string {
	lang = Norm(lang)
	if msg == "" {
		return msg
	}
	var d *dict
	if lang != Source {
		d = load(lang)
		if out, ok := tr(d, msg, 0); ok {
			return out
		}
	}
	if !hasHan(msg) {
		return msg
	}
	// Not a source message: one stored by 1.0.x, in Traditional Chinese
	if lang == legacyLang {
		return msg
	}
	en, ok := tr(loadLegacy(), msg, 0)
	if !ok || d == nil {
		return en
	}
	out, _ := tr(d, en, 0)
	return out
}

// tr looks msg up in d; ok is false when nothing matched (msg is returned).
// A template whose fixed text is punctuation only is used only when one of
// its parts translates, so that raw errors such as "dial tcp: lookup x"
// keep their own punctuation.
func tr(d *dict, msg string, depth int) (string, bool) {
	if v, ok := d.exact[msg]; ok {
		return v, true
	}
	if depth > 2 {
		return msg, false
	}
	for _, t := range d.templates {
		m := t.re.FindStringSubmatch(msg)
		if m == nil {
			continue
		}
		out, any := d.exact[t.key], false
		for i, name := range t.names {
			part, ok := tr(d, m[i+1], depth+1)
			any = any || ok
			out = strings.ReplaceAll(out, "{"+name+"}", part)
		}
		if t.bare && !any {
			continue
		}
		return out, true
	}
	return msg, false
}

func hasHan(s string) bool {
	for _, r := range s {
		if r >= 0x3000 && r <= 0x9FFF || r >= 0xFF00 && r <= 0xFFEF {
			return true
		}
	}
	return false
}
