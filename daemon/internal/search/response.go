package search

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ErrUnreadable is an answer that cannot be read as the plugin says
// (errors wrap it with the details).
var ErrUnreadable = errors.New("Could not read the answer")

func unreadable(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrUnreadable, fmt.Sprintf(format, a...))
}

// Results reads an answer of the source: every usable item, normalised, in
// the source's order. base is the address the answer came from (relative
// addresses in it are resolved against it). The results do not depend on
// the keyword or the category, so a catalogue's can be kept and given to
// Select for each search. A torznab (or any XML) answer that is an
// <error> is a *TorznabError.
func (p *Plugin) Results(body []byte, base string) ([]Result, error) {
	b, _ := url.Parse(base)
	var items []raw
	var err error
	switch p.Type {
	case TypeJSON:
		items, err = p.readJSON(body)
	case TypeRSS, TypeTorznab:
		items, err = p.readFeed(body)
	default:
		err = unreadable("unknown plugin type")
	}
	if err != nil {
		return nil, err
	}
	out := []Result{}
	for _, it := range items {
		if r, ok := p.normalise(it, b); ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// readJSON reads the items of a json answer.
func (p *Plugin) readJSON(body []byte) ([]raw, error) {
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	var doc any
	if err := d.Decode(&doc); err != nil {
		return nil, unreadable("%v", err)
	}
	path, _ := parsePath(p.Response.Items)
	list, ok := lookup(doc, path).([]any)
	if !ok {
		return nil, unreadable("%q is not a list", p.Response.Items)
	}
	if len(list) > MaxItems {
		list = list[:MaxItems]
	}
	out := make([]raw, 0, len(list))
	for _, item := range list {
		get := func(name string) string {
			ps, err := parsePath(name)
			if err != nil {
				return ""
			}
			return scalar(lookup(item, ps))
		}
		if r, ok := p.extract(get); ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// lookup follows a path in a decoded JSON value: names in objects, numbers
// in lists.
func lookup(v any, path []string) any {
	for _, k := range path {
		switch t := v.(type) {
		case map[string]any:
			v = t[k]
		case []any:
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 || i >= len(t) {
				return nil
			}
			v = t[i]
		default:
			return nil
		}
	}
	return v
}

// scalar is a JSON value as text: strings as they are, numbers as written,
// booleans as true/false, the first such value of a list; "" for anything
// else.
func scalar(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	case []any:
		for _, e := range t {
			switch e.(type) {
			case string, json.Number, bool:
				return scalar(e)
			}
		}
	}
	return ""
}

// truthy is a required value that is there: not missing, null, false, 0 or
// empty.
func truthy(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || s == "false" {
		return false
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && f == 0 {
		return false
	}
	return true
}

// extract reads the fields of one item of a json or rss answer through
// get (a path or value name -> its text); ok is false for an item that
// lacks a required value.
func (p *Plugin) extract(get func(string) string) (raw, bool) {
	r := p.Response
	for _, name := range r.Require {
		if !truthy(get(name)) {
			return raw{}, false
		}
	}
	f := r.Fields
	out := raw{
		title:    f.Title.value(get, false),
		size:     f.Size.value(get, false),
		infohash: f.InfoHash.value(get, false),
		magnet:   f.Magnet.value(get, true),
		torrent:  f.Torrent.value(get, true),
		page:     f.Page.value(get, true),
		date:     f.Date.value(get, false),
		seeders:  f.Seeders.value(get, false),
		leechers: f.Leechers.value(get, false),
	}
	if f.Category != nil {
		out.category = p.categoryOf(f.Category.value(get, false))
	}
	return out, true
}

// categoryOf maps a source's category value to ours through the plugin's
// categories (the first of ours, in display order, with that value);
// "other" when none has it.
func (p *Plugin) categoryOf(v string) string {
	v = strings.TrimSpace(v)
	if v != "" {
		for _, c := range Categories {
			if want, ok := p.Categories[c]; ok && want != "" && strings.EqualFold(want, v) {
				return c
			}
		}
	}
	return "other"
}

// value is the field's text for an item: the named value, or the template
// filled in (values URL-path-escaped for address fields, as they are for
// text fields), then the replacement made once. An address template with
// an empty value, or a text template whose values are all empty, gives ""
// (no address built around a missing identifier).
func (f *Field) value(get func(string) string, address bool) string {
	if f == nil {
		return ""
	}
	var s string
	if strings.ContainsAny(f.From, "{}") {
		t, err := parseTemplate(f.From)
		if err != nil {
			return ""
		}
		empty, filled := false, false
		s = t.expand(func(name string) string {
			v := strings.TrimSpace(get(name))
			if v == "" {
				empty = true
			} else {
				filled = true
			}
			if address {
				return url.PathEscape(v)
			}
			return v
		})
		if (address && empty) || !filled {
			return ""
		}
	} else {
		s = get(f.From)
	}
	if len(f.Replace) == 2 {
		s = strings.Replace(s, f.Replace[0], f.Replace[1], 1)
	}
	return s
}
