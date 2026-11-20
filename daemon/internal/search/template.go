package search

import (
	"errors"
	"strings"
)

// A template is text with variables in braces: "https://x.org/?q={query}",
// "Arch Linux {version}". A variable's name is everything between the
// braces; braces cannot be written literally and do not nest.
type template []segment

type segment struct {
	text  string // literal text, or the variable's name
	isVar bool
}

var errTemplate = errors.New("unbalanced braces")

func parseTemplate(s string) (template, error) {
	var t template
	for s != "" {
		open := strings.IndexByte(s, '{')
		if close := strings.IndexByte(s, '}'); close >= 0 && (open < 0 || close < open) {
			return nil, errTemplate
		}
		if open < 0 {
			t = append(t, segment{text: s})
			break
		}
		if open > 0 {
			t = append(t, segment{text: s[:open]})
		}
		s = s[open+1:]
		end := strings.IndexByte(s, '}')
		if end <= 0 || strings.IndexByte(s[:end], '{') >= 0 {
			return nil, errTemplate
		}
		t = append(t, segment{text: s[:end], isVar: true})
		s = s[end+1:]
	}
	return t, nil
}

// vars are the variable names, in order.
func (t template) vars() []string {
	var out []string
	for _, s := range t {
		if s.isVar {
			out = append(out, s.text)
		}
	}
	return out
}

// expand puts value(name) in place of each variable.
func (t template) expand(value func(name string) string) string {
	var b strings.Builder
	for _, s := range t {
		if s.isVar {
			b.WriteString(value(s.text))
		} else {
			b.WriteString(s.text)
		}
	}
	return b.String()
}
