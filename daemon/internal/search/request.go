package search

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// Query is one search as a source sees it.
type Query struct {
	Text     string // the keyword as the user typed it (trimmed)
	Category string // "", CategoryAll or one of Categories
}

// Call is the request to make to a source: a GET of URL with Headers
// (besides the User-Agent and Accept that the fetch sets).
type Call struct {
	URL     string
	Headers map[string]string
}

// ErrEmptyQuery is a keyword with nothing left once the plugin's strip
// characters are taken out: there is nothing to ask the source.
var ErrEmptyQuery = errors.New("Nothing to search for")

// Torznab category numbers asked for each of our categories.
var torznabCats = map[string]string{
	"video":    "2000,5000",
	"audio":    "3000",
	"software": "4000",
	"books":    "7000",
	"other":    "8000",
}

// BuildRequest is the request for q with the source's settings. Every
// variable of the address is URL-query-escaped: {query} (after strip),
// {limit} (MaxResults), {page} (1), {category} (the categories value of
// the category asked, "" for all or one without a value) and
// {setting:<id>}. Header values take {setting:<id>} as it is. A source
// matched on the NAS gets the same request whatever q is.
func (p *Plugin) BuildRequest(q Query, values map[string]string) (*Call, error) {
	if p.Type == TypeTorznab {
		return p.torznabRequest(q, values)
	}
	keyword := q.Text
	if p.Request.Strip != "" {
		keyword = strings.Map(func(r rune) rune {
			if strings.ContainsRune(p.Request.Strip, r) {
				return ' '
			}
			return r
		}, keyword)
	}
	keyword = strings.Join(strings.Fields(keyword), " ")
	if keyword == "" && p.Match == "" {
		return nil, ErrEmptyQuery
	}
	cat := ""
	if q.Category != "" && q.Category != CategoryAll {
		cat = p.Categories[q.Category]
	}
	t, err := parseTemplate(p.Request.URL)
	if err != nil {
		return nil, err
	}
	u := t.expand(func(name string) string {
		switch name {
		case "query":
			return url.QueryEscape(keyword)
		case "limit":
			return strconv.Itoa(MaxResults)
		case "page":
			return "1"
		case "category":
			return url.QueryEscape(cat)
		}
		return url.QueryEscape(values[strings.TrimPrefix(name, "setting:")])
	})
	c := &Call{URL: u}
	if len(p.Request.Headers) > 0 {
		c.Headers = map[string]string{}
		for k, v := range p.Request.Headers {
			ht, err := parseTemplate(v)
			if err != nil {
				return nil, err
			}
			c.Headers[k] = ht.expand(func(name string) string {
				return values[strings.TrimPrefix(name, "setting:")]
			})
		}
	}
	return c, nil
}

// torznabBase checks a Torznab server address: http or https, a host, no
// user name or fragment. A query it carries is kept (the search's own
// parameters replace those with the same names), except an API key: the
// address is not a secret setting, the key must go in its own.
func torznabBase(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" ||
		u.Opaque != "" || u.User != nil || u.Fragment != "" || len(raw) > maxURL {
		return nil, invalid("The server address must be an http or https address")
	}
	for k := range u.Query() {
		if strings.EqualFold(k, "apikey") {
			return nil, invalid("Enter the API key in its own field, not in the server address")
		}
	}
	return u, nil
}

// torznabRequest is <url>?t=search&q=…&apikey=…&limit=100[&cat=…].
func (p *Plugin) torznabRequest(q Query, values map[string]string) (*Call, error) {
	u, err := torznabBase(values["url"])
	if err != nil {
		return nil, err
	}
	keyword := strings.Join(strings.Fields(q.Text), " ")
	if keyword == "" {
		return nil, ErrEmptyQuery
	}
	v := u.Query()
	v.Set("t", "search")
	v.Set("q", keyword)
	v.Set("limit", strconv.Itoa(MaxResults))
	if k := values["apikey"]; k != "" {
		v.Set("apikey", k)
	}
	v.Del("cat")
	if c := torznabCats[q.Category]; c != "" {
		v.Set("cat", c)
	}
	u.RawQuery = v.Encode()
	return &Call{URL: u.String()}, nil
}
