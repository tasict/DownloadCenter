package search

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const examplePlugin = `{
  "schema": 1,
  "id": "example",
  "name": "Example",
  "version": "1.0",
  "type": "json",
  "request": {
    "url": "https://api.example.org/search?q={query}&rows={limit}&cat={category}&key={setting:apikey}",
    "headers": {"Accept": "application/json"}
  },
  "settings": [{"id": "apikey", "label": "API key", "secret": true, "required": true}],
  "categories": {"video": "movies", "audio": "audio"},
  "test_query": "ubuntu",
  "response": {
    "items": "response.docs",
    "fields": {
      "title": "title",
      "size": "item_size",
      "infohash": "btih",
      "torrent": "https://example.org/download/{identifier}/{identifier}.torrent",
      "page": "https://example.org/details/{identifier}",
      "date": "publicdate",
      "seeders": "seeders",
      "leechers": "leechers",
      "category": "mediatype"
    }
  }
}`

// variant is the example plugin with a change made to its decoded form.
func variant(t *testing.T, change func(m map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(examplePlugin), &m); err != nil {
		t.Fatal(err)
	}
	change(m)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func req(m map[string]any) map[string]any { return m["request"].(map[string]any) }
func fields(m map[string]any) map[string]any {
	return m["response"].(map[string]any)["fields"].(map[string]any)
}

func setURL(u string) func(map[string]any) {
	return func(m map[string]any) { req(m)["url"] = u }
}

func setHeader(name string) func(map[string]any) {
	return func(m map[string]any) { req(m)["headers"] = map[string]any{name: "x"} }
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name   string
		change func(m map[string]any)
		want   string // part of the message; "" = valid
	}{
		{"valid", func(map[string]any) {}, ""},
		// spec: a plugin with an unknown field is refused, naming the field
		{"unknown field script", func(m map[string]any) { m["script"] = "alert(1)" }, `Unknown field "script"`},
		{"unknown field in request", func(m map[string]any) { req(m)["method"] = "POST" }, `Unknown field "method"`},
		{"unknown field in a field object", func(m map[string]any) {
			fields(m)["title"] = map[string]any{"from": "title", "regex": ".*"}
		}, `Unknown field "regex"`},
		{"unknown field in fields", func(m map[string]any) { fields(m)["exec"] = "x" }, `Unknown field "exec"`},
		{"schema 2", func(m map[string]any) { m["schema"] = 2 }, "The field schema has an unsupported value: 2"},
		{"id upper case", func(m map[string]any) { m["id"] = "Example" }, "The id must be"},
		{"id one character", func(m map[string]any) { m["id"] = "x" }, "The id must be"},
		{"id leading hyphen", func(m map[string]any) { m["id"] = "-ex" }, "The id must be"},
		{"id too long", func(m map[string]any) { m["id"] = strings.Repeat("a", 41) }, "The id must be"},
		{"id 40 characters", func(m map[string]any) { m["id"] = strings.Repeat("a", 40) }, ""},
		{"no name", func(m map[string]any) { m["name"] = " " }, "The field name is missing"},
		{"name with control character", func(m map[string]any) { m["name"] = "a\u0007b" }, "The field name is not valid"},
		{"unknown type", func(m map[string]any) { m["type"] = "html" }, "The field type has an unsupported value: html"},
		// spec: the host must be written out
		{"host from a setting", func(m map[string]any) {
			m["settings"] = []any{map[string]any{"id": "host", "label": "Host"}}
			req(m)["url"] = "https://{setting:host}/api?q={query}"
		}, "The host name of the request address must be written out"},
		{"host suffix from a variable", setURL("https://example.{query}/api"), "must be written out"},
		{"port from a variable", setURL("https://example.org:{limit}/api?q={query}"), "must be written out"},
		// spec: json and rss plugins use https
		{"http json plugin", setURL("http://api.example.org/search?q={query}"), "The request address must use https"},
		{"ftp address", setURL("ftp://api.example.org/search?q={query}"), "must use https"},
		{"ip address", setURL("https://192.0.2.10/search?q={query}"), "not an IP address or localhost"},
		{"loopback address", setURL("https://127.0.0.1/search?q={query}"), "not an IP address or localhost"},
		{"decimal address", setURL("https://2130706433/search?q={query}"), "not an IP address or localhost"},
		{"hex address", setURL("https://0x7f.1/search?q={query}"), "not an IP address or localhost"},
		{"ipv6 address", setURL("https://[::1]/search?q={query}"), "not an IP address or localhost"},
		{"localhost", setURL("https://localhost/search?q={query}"), "not an IP address or localhost"},
		{"localhost with a dot", setURL("https://localhost./search?q={query}"), "not an IP address or localhost"},
		{"name under localhost", setURL("https://nas.localhost/search?q={query}"), "not an IP address or localhost"},
		{"user name", setURL("https://user@api.example.org/search?q={query}"), "The request address is not valid"},
		{"fragment", setURL("https://api.example.org/search?q={query}#x"), "The request address is not valid"},
		{"port", setURL("https://api.example.org:8443/search?q={query}"), ""},
		{"bad port", setURL("https://api.example.org:99999/search?q={query}"), "The request address is not valid"},
		{"unknown variable", setURL("https://api.example.org/search?q={query}&x={foo}"), "Unknown variable foo"},
		{"undeclared setting", setURL("https://api.example.org/search?q={query}&k={setting:token}"), "Unknown variable setting:token"},
		{"unbalanced brace", setURL("https://api.example.org/search?q={query"), "The request address is not valid"},
		{"no keyword", setURL("https://api.example.org/search?all=1"), "must contain the search keyword"},
		{"keyword in a local match source", func(m map[string]any) { m["match"] = "local" }, "cannot contain the search keyword"},
		{"local match source", func(m map[string]any) {
			m["match"] = "local"
			req(m)["url"] = "https://api.example.org/catalogue.json"
		}, ""},
		{"unknown match", func(m map[string]any) { m["match"] = "remote" }, "The field match has an unsupported value: remote"},
		{"unknown prefer", func(m map[string]any) { m["prefer"] = "both" }, "The field prefer has an unsupported value: both"},
		// spec: forbidden custom headers
		{"header Host", setHeader("Host"), "The header Host is not allowed"},
		{"header Cookie", setHeader("Cookie"), "The header Cookie is not allowed"},
		{"header Content-Length", setHeader("Content-Length"), "not allowed"},
		{"header Transfer-Encoding", setHeader("Transfer-Encoding"), "not allowed"},
		{"header Connection", setHeader("connection"), "not allowed"},
		{"header Proxy-Authorization", setHeader("Proxy-Authorization"), "not allowed"},
		{"header X-Forwarded-For", setHeader("X-Forwarded-For"), "not allowed"},
		{"header User-Agent", setHeader("User-Agent"), "not allowed"},
		{"header with a space", setHeader("X Bad"), "The field request.headers.X Bad is not valid"},
		{"header with a setting", func(m map[string]any) {
			req(m)["headers"] = map[string]any{"Authorization": "Bearer {setting:apikey}"}
		}, ""},
		{"header with the keyword", func(m map[string]any) {
			req(m)["headers"] = map[string]any{"X-Query": "{query}"}
		}, "Unknown variable query"},
		{"header value with a line break", func(m map[string]any) {
			req(m)["headers"] = map[string]any{"X-Key": "a\r\nCookie: b"}
		}, "The field request.headers.X-Key is not valid"},
		// spec: a title and something to add
		{"no title", func(m map[string]any) { delete(fields(m), "title") }, "must give a title"},
		{"no link", func(m map[string]any) {
			delete(fields(m), "infohash")
			delete(fields(m), "torrent")
		}, "must give an infohash, a magnet link or a .torrent address"},
		{"magnet only", func(m map[string]any) {
			delete(fields(m), "infohash")
			delete(fields(m), "torrent")
			fields(m)["magnet"] = "magnet"
		}, ""},
		{"no response", func(m map[string]any) { delete(m, "response") }, "must give a title"},
		{"no request", func(m map[string]any) { delete(m, "request") }, "The field request.url is missing"},
		{"replace with one string", func(m map[string]any) {
			fields(m)["torrent"] = map[string]any{"from": "url", "replace": []any{".meta4"}}
		}, "The field response.fields.torrent.replace is not valid"},
		{"replace with three strings", func(m map[string]any) {
			fields(m)["torrent"] = map[string]any{"from": "url", "replace": []any{"a", "b", "c"}}
		}, "The field response.fields.torrent.replace is not valid"},
		{"replace", func(m map[string]any) {
			fields(m)["torrent"] = map[string]any{"from": "url", "replace": []any{".meta4", ".torrent"}}
		}, ""},
		{"bad path", func(m map[string]any) { fields(m)["title"] = "a..b" }, "The path a..b is not valid"},
		{"template with a bad path", func(m map[string]any) { fields(m)["page"] = "https://x.org/{a..b}" }, "Unknown variable a..b"},
		{"unknown category", func(m map[string]any) { m["categories"] = map[string]any{"movies": "x"} }, "The field categories has an unsupported value: movies"},
		{"category all", func(m map[string]any) { m["categories"] = map[string]any{"all": "x"} }, "The field categories has an unsupported value: all"},
		{"bad setting id", func(m map[string]any) {
			m["settings"] = []any{map[string]any{"id": "Api Key", "label": "Key"}}
		}, "The field settings.Api Key is not valid"},
		{"setting twice", func(m map[string]any) {
			s := map[string]any{"id": "apikey", "label": "Key"}
			m["settings"] = []any{s, s}
		}, "The field settings.apikey is not valid"},
		{"homepage javascript", func(m map[string]any) { m["homepage"] = "javascript:alert(1)" }, "The field homepage is not valid"},
		{"description too long", func(m map[string]any) { m["description"] = strings.Repeat("x", 501) }, "The field description is too long"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := Parse(variant(t, c.change))
			if c.want == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if p == nil {
					t.Fatal("no plugin")
				}
				return
			}
			var ie *InvalidError
			if !errors.As(err, &ie) {
				t.Fatalf("got %v, want an InvalidError with %q", err, c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %q, want %q", err, c.want)
			}
		})
	}
}

func TestValidateRSS(t *testing.T) {
	base := `{"schema":1,"id":"feed","name":"Feed","type":"rss","request":{"url":"https://feed.example.org/rss?q={query}"},
		"response":{%s"fields":{"title":"title","torrent":%s}}}`
	cases := []struct {
		items, torrent, want string
	}{
		{"", `"enclosure.url"`, ""},
		{"", `"link"`, ""},
		{"", `"attr:magneturl"`, ""},
		{"", `"ns:infoHash"`, ""},
		{"", `"https://x.org/{guid}.torrent"`, ""},
		{"", `"description"`, "The path description is not valid"},
		{"", `"attr:"`, "The path attr: is not valid"},
		{`"items":"channel.item",`, `"link"`, "A source of type rss cannot have the field response.items"},
	}
	for _, c := range cases {
		_, err := Parse([]byte(strings.Replace(strings.Replace(base, "%s", c.items, 1), "%s", c.torrent, 1)))
		if c.want == "" && err != nil {
			t.Errorf("%s: refused: %v", c.torrent, err)
		}
		if c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: got %v, want %q", c.torrent, err, c.want)
		}
	}
}

// spec: a plugin file over 64 KiB is refused
func TestParseTooLarge(t *testing.T) {
	b := variant(t, func(m map[string]any) { m["description"] = strings.Repeat("x", 100<<10) })
	_, err := Parse(b)
	var ie *InvalidError
	if !errors.As(err, &ie) || !strings.Contains(err.Error(), "64 KiB") {
		t.Fatalf("got %v", err)
	}
}

func TestParseNotJSON(t *testing.T) {
	for _, s := range []string{"", "[]", "{", examplePlugin + "{}", examplePlugin + " x"} {
		_, err := Parse([]byte(s))
		var ie *InvalidError
		if !errors.As(err, &ie) {
			t.Errorf("%q: got %v", s, err)
		}
	}
	if _, err := Parse([]byte(examplePlugin + "\n")); err != nil {
		t.Errorf("trailing new line: %v", err)
	}
}

// spec: a plugin with a built-in source's id cannot be added
func TestParseCustomBuiltinID(t *testing.T) {
	b := variant(t, func(m map[string]any) { m["id"] = "internet-archive" })
	if _, err := Parse(b); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := ParseCustom(b); !errors.Is(err, ErrExists) {
		t.Fatalf("ParseCustom: got %v, want ErrExists", err)
	}
	if _, err := ParseCustom([]byte(examplePlugin)); err != nil {
		t.Fatalf("ParseCustom: %v", err)
	}
}

func TestTorznabDefinition(t *testing.T) {
	p, err := Parse([]byte(`{"schema":1,"id":"jackett","name":"Jackett","type":"torznab"}`))
	if err != nil {
		t.Fatal(err)
	}
	if defs := p.SettingDefs(); len(defs) != 2 || defs[0].ID != "url" || !defs[0].Required || defs[1].ID != "apikey" || !defs[1].Secret {
		t.Errorf("settings %+v", defs)
	}
	if got := p.SupportedCategories(); len(got) != len(Categories) {
		t.Errorf("categories %v", got)
	}
	for _, extra := range []string{
		`"request":{"url":"https://x.org/?q={query}"}`,
		`"response":{"fields":{"title":"title","link":"link"}}`,
		`"settings":[{"id":"url","label":"URL"}]`,
		`"categories":{"video":"2000"}`,
		`"match":"local"`,
	} {
		if _, err := Parse([]byte(`{"schema":1,"id":"jackett","name":"Jackett","type":"torznab",` + extra + `}`)); err == nil {
			t.Errorf("accepted %s", extra)
		}
	}
}

func TestSettings(t *testing.T) {
	p, err := Parse([]byte(examplePlugin))
	if err != nil {
		t.Fatal(err)
	}
	if p.Complete(nil) || !p.Complete(map[string]string{"apikey": "k"}) {
		t.Error("Complete")
	}
	if err := p.CheckSettings(map[string]string{"apikey": "k"}); err != nil {
		t.Error(err)
	}
	if err := p.CheckSettings(map[string]string{"other": "k"}); err == nil {
		t.Error("unknown setting accepted")
	}
	if err := p.CheckSettings(map[string]string{"apikey": "a\nb"}); err == nil {
		t.Error("line break accepted")
	}
	if got := p.SecretValues(map[string]string{"apikey": "SECRET"}); !reflect.DeepEqual(got, []string{"SECRET"}) {
		t.Errorf("secrets %v", got)
	}
	if h := p.Host(nil); h != "api.example.org" {
		t.Errorf("host %q", h)
	}
	if got := p.SupportedCategories(); !reflect.DeepEqual(got, []string{"video", "audio"}) {
		t.Errorf("categories %v", got)
	}
	if !p.Asks("") || !p.Asks(CategoryAll) || !p.Asks("video") || p.Asks("books") {
		t.Error("Asks")
	}

	tz, _ := Parse([]byte(`{"schema":1,"id":"jackett","name":"Jackett","type":"torznab"}`))
	for _, u := range []string{"http://192.168.1.20:9117/api/v2.0/indexers/all/results/torznab", "https://jackett.example.org/torznab?x=1"} {
		if err := tz.CheckSettings(map[string]string{"url": u, "apikey": "k"}); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	for _, u := range []string{"ftp://192.168.1.20/", "192.168.1.20:9117", "http://user:pw@192.168.1.20/", "http:///x", "javascript:alert(1)"} {
		if err := tz.CheckSettings(map[string]string{"url": u}); err == nil {
			t.Errorf("%s accepted", u)
		}
	}
	if h := tz.Host(map[string]string{"url": "http://192.168.1.20:9117/x"}); h != "192.168.1.20" {
		t.Errorf("torznab host %q", h)
	}
	if tz.Complete(map[string]string{"apikey": "k"}) || !tz.Complete(map[string]string{"url": "http://h/"}) {
		t.Error("torznab Complete")
	}
}

// A stored or exported definition reads back as the same plugin.
func TestMarshalRoundTrip(t *testing.T) {
	srcs := [][]byte{[]byte(examplePlugin), variant(t, func(m map[string]any) {
		fields(m)["torrent"] = map[string]any{"from": "https://x.org/?a={url}&b=1", "replace": []any{"&b=1", "&b=2"}}
		fields(m)["page"] = "https://x.org/?a={url}&b=<1>"
		m["default_enabled"] = false
	})}
	for _, p := range Builtins() {
		b, _ := p.Marshal()
		srcs = append(srcs, b)
	}
	for _, src := range srcs {
		p, err := Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		b, err := p.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `\`+`u0026`) {
			t.Errorf("%s: escaped &: %s", p.ID, b)
		}
		q, err := Parse(b)
		if err != nil {
			t.Fatalf("%s: %v\n%s", p.ID, err, b)
		}
		if !reflect.DeepEqual(p, q) {
			t.Errorf("%s changed:\n%+v\n%+v", p.ID, p, q)
		}
	}
}

func TestFieldForms(t *testing.T) {
	var f Field
	if err := json.Unmarshal([]byte(`"title"`), &f); err != nil || f.From != "title" || f.Replace != nil {
		t.Errorf("string form: %+v %v", f, err)
	}
	if err := json.Unmarshal([]byte(`{"from":"u","replace":["a","b"]}`), &f); err != nil || f.From != "u" || len(f.Replace) != 2 {
		t.Errorf("object form: %+v %v", f, err)
	}
	if b, _ := json.Marshal(Field{From: "x"}); string(b) != `"x"` {
		t.Errorf("marshal %s", b)
	}
	if err := json.Unmarshal([]byte(`{"from":"u","with":1}`), &f); err == nil {
		t.Error("unknown key accepted")
	}
}

func TestInvalidErrorMessage(t *testing.T) {
	e := &InvalidError{Msg: msgUnsupported, Args: []string{"type", "html"}}
	if e.Error() != "The field type has an unsupported value: html" {
		t.Error(e.Error())
	}
}
