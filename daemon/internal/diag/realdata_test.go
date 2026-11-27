package diag

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"downloadcenter/internal/core"
	"downloadcenter/internal/store"
)

// TestRealData exports a copy of an installed package's data folder
// (DC_REAL_DATA=<copy of data/>; never the live folder) and looks for
// anything the export must not show. Only counts and file names are
// printed, never what was found.
func TestRealData(t *testing.T) {
	data := os.Getenv("DC_REAL_DATA")
	if data == "" {
		t.Skip("DC_REAL_DATA not set")
	}
	db, err := store.Open(filepath.Join(data, "dc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := core.New(db, data) // never started: nothing is resumed or moved
	var buf bytes.Buffer
	if err := Export(&buf, Source{M: m, Root: filepath.Dir(data), Data: data, Version: "test", Started: time.Now()}); err != nil {
		t.Fatal(err)
	}
	files := unzip(t, buf.Bytes())
	var terms []core.DiagTerm
	kinds := map[int]string{}
	for _, d := range m.DiagTerms() {
		if usable(d) {
			kinds[len(terms)] = d.Kind
			terms = append(terms, d)
		}
	}
	var pats, repl []string
	for i, d := range terms {
		pats = append(pats, d.Text)
		repl = append(repl, "\x00"+kinds[i]+"\x00")
	}
	mt := newMatcher(pats)
	placeholder := regexp.MustCompile(`<[^<>\n]{1,40}>`)
	hexRe := regexp.MustCompile(`\b(?:[0-9a-fA-F]{64}|[0-9a-fA-F]{40})\b`)
	urlRe := regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]{1,15}://([^\s/"'<>]*)`)
	var names []string
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	leaks := 0
	for _, n := range names {
		text := placeholder.ReplaceAllString(files[n], "")
		out, hits := mt.replace(text, repl)
		byKind := map[string]int{}
		for _, k := range []string{"task", "name", "link", "host", "account"} {
			byKind[k] = strings.Count(out, "\x00"+k+"\x00")
		}
		ips := 0
		r := &Redactor{}
		if r.rules(text) != text {
			// count what the rules would still change: addresses, hashes, links
			ips = r.Rules
		}
		hx := len(hexRe.FindAllString(text, -1))
		urls := 0
		for _, u := range urlRe.FindAllStringSubmatch(text, -1) {
			if !loopback(u[1]) {
				urls++
			}
		}
		fmt.Printf("%-18s %8d bytes  names %d (task %d name %d link %d host %d account %d)  hex %d  links %d  rules-left %d\n",
			n, len(files[n]), hits, byKind["task"], byKind["name"], byKind["link"], byKind["host"], byKind["account"], hx, urls, ips)
		leaks += hits + hx + urls
	}
	fmt.Printf("terms %d\n", len(terms))
	if i := strings.LastIndex(files["summary.txt"], "Export:"); i >= 0 {
		fmt.Print(files["summary.txt"][i:])
	}
	if leaks > 0 {
		t.Errorf("%d possible leaks", leaks)
	}
}
