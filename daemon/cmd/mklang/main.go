// mklang keeps the translations in step with the code.
//
//	go run ./cmd/mklang keys <repo>  collect the UI strings (every DC.t('…') literal in shared/web/js and
//	                                  shared/web/widgets, and the texts of the Deskpane widget manifest), the backend
//	                                  messages (internal/i18n/backend-keys.json) and the widget document's texts
//	                                  (internal/i18n/widget-keys.json) into internal/i18n/source.json
//	go run ./cmd/mklang js <repo>    write shared/web/js/lang/<CODE>.js and shared/web/widgets/lang/<CODE>.json from
//	                                  internal/i18n/lang/<CODE>.json (the strings each part uses), write the Deskpane
//	                                  manifest shared/deskpane/widgets.json from cmd/mklang/widgets.json with every
//	                                  language, and report what each language is missing
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DC.t('…' or "…" as the first argument; the key must be a plain literal
var callRe = regexp.MustCompile(`DC\.t\(\s*('(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*")`)

type source struct {
	UI      []string `json:"ui"`
	Backend []string `json:"backend"`
	Widget  []string `json:"widget"`
}

func unquote(lit string) string {
	body := lit[1 : len(lit)-1]
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c == '\\' && i+1 < len(body) {
			i++
			switch body[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(body[i])
			}
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// scriptKeys are the DC.t literals of the scripts in shared/web/<dir>.
func scriptKeys(root, dir string) []string {
	seen := map[string]bool{}
	files, _ := filepath.Glob(filepath.Join(root, "shared", "web", dir, "*.js"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range callRe.FindAllStringSubmatch(string(b), -1) {
			seen[unquote(m[1])] = true
		}
	}
	return sorted(seen)
}

func sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func readList(p string) []string {
	var l []string
	if b, err := os.ReadFile(p); err == nil {
		json.Unmarshal(b, &l)
	}
	return l
}

func writeJSON(p string, v any) {
	b, _ := json.MarshalIndent(v, "", "\t")
	if err := os.WriteFile(p, append(b, '\n'), 0644); err != nil {
		panic(err)
	}
}

// The Deskpane manifest: the template's texts (titles, descriptions, option
// and choice labels) are English; the shipped manifest gives each of them
// as {"en": …, "<QTS code>": …} for every language with a translation.
// Deskpane looks a text up by the BCP 47 tag of the user's QTS language,
// then by the QTS code itself, then "en".

const manifestTemplate = "daemon/cmd/mklang/widgets.json"
const manifestOut = "shared/deskpane/widgets.json"

// The manifest, in the order of Deskpane's guide. A text is a string in the
// template and an object of translations in the shipped file.
type manifest struct {
	V       int      `json:"v"`
	Widgets []widget `json:"widgets"`
}

type widget struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Element     string   `json:"element,omitempty"`
	Title       any      `json:"title"`
	Description any      `json:"description,omitempty"`
	Sizes       any      `json:"sizes"`
	Shells      []string `json:"shells,omitempty"`
	URL         string   `json:"url"`
	Refresh     int      `json:"refresh,omitempty"`
	Admin       bool     `json:"admin,omitempty"`
	Options     []option `json:"options,omitempty"`
}

type option struct {
	ID      string   `json:"id"`
	Type    string   `json:"type"`
	Label   any      `json:"label"`
	Choices []choice `json:"choices,omitempty"`
	Default any      `json:"default,omitempty"`
	Max     int      `json:"max,omitempty"`
}

type choice struct {
	Value string `json:"value"`
	Label any    `json:"label"`
}

func readTemplate(root string) manifest {
	b, err := os.ReadFile(filepath.Join(root, manifestTemplate))
	if err != nil {
		panic(err)
	}
	var m manifest
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		panic(fmt.Sprintf("%s: %v", manifestTemplate, err))
	}
	return m
}

// manifestTexts calls fn for every translatable text of the manifest and
// replaces it with what fn returns.
func manifestTexts(m *manifest, fn func(string) any) {
	text := func(v *any) {
		if s, ok := (*v).(string); ok && s != "" {
			*v = fn(s)
		}
	}
	for i := range m.Widgets {
		w := &m.Widgets[i]
		text(&w.Title)
		text(&w.Description)
		for j := range w.Options {
			o := &w.Options[j]
			text(&o.Label)
			for k := range o.Choices {
				text(&o.Choices[k].Label)
			}
		}
	}
}

func manifestKeys(root string) []string {
	seen := map[string]bool{}
	m := readTemplate(root)
	manifestTexts(&m, func(s string) any { seen[s] = true; return s })
	return sorted(seen)
}

// buildManifest is the shipped manifest for the dictionaries dicts (QTS code -> dictionary).
func buildManifest(root string, dicts map[string]map[string]string) []byte {
	m := readTemplate(root)
	manifestTexts(&m, func(s string) any {
		out := map[string]string{"en": s}
		for code, d := range dicts {
			if v := d[s]; v != "" {
				out[code] = v
			}
		}
		return out
	})
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "\t")
	if err := enc.Encode(m); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func readDicts(dir string) map[string]map[string]string {
	out := map[string]map[string]string{}
	files, _ := filepath.Glob(filepath.Join(dir, "lang", "*.json"))
	for _, f := range files {
		code := strings.TrimSuffix(filepath.Base(f), ".json")
		var m map[string]string
		b, err := os.ReadFile(f)
		if err != nil || json.Unmarshal(b, &m) != nil {
			fmt.Printf("%s: unreadable\n", code)
			continue
		}
		out[code] = m
	}
	return out
}

func subset(d map[string]string, keys []string) (map[string]string, int) {
	out := map[string]string{}
	missing := 0
	for _, k := range keys {
		if v := d[k]; v != "" {
			out[k] = v
		} else {
			missing++
		}
	}
	return out, missing
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: mklang keys|js <repo root>")
		os.Exit(2)
	}
	root := os.Args[2]
	dir := filepath.Join(root, "daemon", "internal", "i18n")
	switch os.Args[1] {
	case "keys":
		ui := map[string]bool{}
		for _, l := range [][]string{scriptKeys(root, "js"), scriptKeys(root, "widgets"), manifestKeys(root)} {
			for _, k := range l {
				ui[k] = true
			}
		}
		src := source{UI: sorted(ui), Backend: readList(filepath.Join(dir, "backend-keys.json")), Widget: readList(filepath.Join(dir, "widget-keys.json"))}
		writeJSON(filepath.Join(dir, "source.json"), src)
		fmt.Printf("%d UI strings, %d backend messages, %d widget texts\n", len(src.UI), len(src.Backend), len(src.Widget))
	case "js":
		ui, wkeys := scriptKeys(root, "js"), scriptKeys(root, "widgets")
		dicts := readDicts(dir)
		codes := make([]string, 0, len(dicts))
		for c := range dicts {
			codes = append(codes, c)
		}
		sort.Strings(codes)
		for _, code := range codes {
			out, _ := subset(dicts[code], ui)
			js, _ := json.Marshal(out)
			content := "/* Generated by daemon/cmd/mklang from daemon/internal/i18n/lang/" + code + ".json. Do not edit. */\nwindow.DC_DICT = " + string(js) + ";\n"
			if err := os.WriteFile(filepath.Join(root, "shared", "web", "js", "lang", code+".js"), []byte(content), 0644); err != nil {
				panic(err)
			}
			wout, wmissing := subset(dicts[code], wkeys)
			wj, _ := json.Marshal(wout)
			if err := os.WriteFile(filepath.Join(root, "shared", "web", "widgets", "lang", code+".json"), append(wj, '\n'), 0644); err != nil {
				panic(err)
			}
			fmt.Printf("%s: %d/%d UI strings, %d/%d widget strings translated\n", code, len(out), len(ui), len(wout), len(wout)+wmissing)
		}
		if err := os.WriteFile(filepath.Join(root, manifestOut), buildManifest(root, dicts), 0644); err != nil {
			panic(err)
		}
		fmt.Println("wrote " + manifestOut)
	}
}
