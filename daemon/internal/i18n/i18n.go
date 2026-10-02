// Package i18n translates the user-facing messages of the backend. Messages
// are written in Traditional Chinese in the code (the UI's source language);
// lang/<code>.json maps each source string, or a template with {0}, {1}...
// for the parts that vary, to the translation. Language codes are QTS's
// (ENG, SCH, JPN ...); TCH and unknown strings pass through unchanged.
package i18n

import (
	"embed"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"
)

//go:embed lang/*.json
var files embed.FS

type template struct {
	key   string
	re    *regexp.Regexp
	names []string
}

type dict struct {
	exact     map[string]string
	templates []template
}

var (
	mu    sync.Mutex
	dicts = map[string]*dict{}
	phRe  = regexp.MustCompile(`\{(\w+)\}`)
)

// Aliases maps QTS codes without their own dictionary.
var aliases = map[string]string{"ESM": "SPA"}

func load(lang string) *dict {
	mu.Lock()
	defer mu.Unlock()
	if d, ok := dicts[lang]; ok {
		return d
	}
	var m map[string]string
	if b, err := files.ReadFile("lang/" + lang + ".json"); err == nil {
		json.Unmarshal(b, &m)
	}
	d := build(m)
	dicts[lang] = d
	return d
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
	// The template with the most fixed text wins ("錯誤：{0}" before "{0}：{1}")
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
	return template{key: key, re: regexp.MustCompile(b.String()), names: names}
}

// Norm turns a request's language into a dictionary code ("" = source).
func Norm(lang string) string {
	lang = strings.ToUpper(strings.TrimSpace(lang))
	if a, ok := aliases[lang]; ok {
		lang = a
	}
	if lang == "" || lang == "TCH" {
		return ""
	}
	return lang
}

// T translates a message. Variable parts of a template are translated too
// when they are messages themselves (e.g. an error inside "錯誤：{0}").
func T(lang, msg string) string {
	lang = Norm(lang)
	if lang == "" || msg == "" || !hasHan(msg) {
		return msg
	}
	d := load(lang)
	if d.exact == nil || len(d.exact) == 0 {
		d = load("ENG")
	}
	return tr(d, msg, 0)
}

func tr(d *dict, msg string, depth int) string {
	if v, ok := d.exact[msg]; ok {
		return v
	}
	if depth > 2 {
		return msg
	}
	for _, t := range d.templates {
		m := t.re.FindStringSubmatch(msg)
		if m == nil {
			continue
		}
		out := d.exact[t.key]
		for i, name := range t.names {
			out = strings.ReplaceAll(out, "{"+name+"}", tr(d, m[i+1], depth+1))
		}
		return out
	}
	return msg
}

func hasHan(s string) bool {
	for _, r := range s {
		if r >= 0x3000 && r <= 0x9FFF || r >= 0xFF00 && r <= 0xFFEF {
			return true
		}
	}
	return false
}
