// Package diag builds the diagnostic export (Settings › About and updates
// › Download logs): the logs of Download Center's background programs,
// mainly their errors, for reporting a problem. The logs on disk are kept
// as they are (only root can read them); what the export holds must not
// tell what anyone downloaded or who they are: the export replaces every
// name the database knows (tasks, files, links, hosts, accounts) and then,
// by rules, every link, magnet link, hash, address and the part of a path
// below its shared folder. Lines older than the task history are left out,
// because the tasks they name may no longer be in the database.
package diag

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"downloadcenter/internal/core"
)

// Redactor removes what an export must not show, line by line.
type Redactor struct {
	ac   *matcher
	repl []string
	cut  time.Time
	keep bool // the age of the current line (lines without a time follow the last one)

	Dict, Rules, Dropped int // names replaced, rule replacements, lines left out
}

// Words of Download Center's own messages that never stand in for a name,
// whatever a task or account happens to be called.
var stopWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`admin administrator administrators guest everyone public homes home share shares data logs log
		download downloads downloading downloaded center torrent torrents magnet engine engines task tasks file files folder folders
		temp tmp seed seeding seeds peer peers tracker trackers error errors failed started stopped stopping ready running restart
		restarted version update updates http https ftp ftps sftp scp none null true false default unknown libtorrent dc-bt dc-dl dcd
		qpkg multimedia web recordings container station`) {
		stopWords[w] = true
	}
}

// usable reports a term long and specific enough to be replaced wherever
// it appears: short words, numbers and the words of our own messages would
// wreck the log without hiding anything.
func usable(t core.DiagTerm) bool {
	min := 4
	switch t.Kind {
	case "task", "link":
		min = 1
	case "account":
		min = 3
	}
	if len(t.Text) < min || stopWords[strings.ToLower(t.Text)] {
		return false
	}
	for _, r := range t.Text {
		if !(r >= '0' && r <= '9') && !strings.ContainsRune(" .,:;-_+()[]/", r) {
			return true
		}
	}
	return false
}

// NewRedactor replaces terms (code gives a task's diagnostic code) and
// leaves out lines older than cut.
func NewRedactor(terms []core.DiagTerm, code func(hash string) string, cut time.Time) *Redactor {
	r := &Redactor{cut: cut}
	accounts := map[string]string{}
	codes := map[string]string{}
	codeOf := func(h string) string {
		if c, ok := codes[h]; ok {
			return c
		}
		c := code(h)
		codes[h] = c
		return c
	}
	seen := map[string]bool{}
	var pats []string
	for _, t := range terms {
		if !usable(t) {
			continue
		}
		key := strings.ToLower(t.Text)
		if seen[key] {
			continue
		}
		seen[key] = true
		var s string
		switch t.Kind {
		case "task":
			s = "<task " + codeOf(t.Hash) + ">"
		case "link":
			s = "<link of task " + codeOf(t.Hash) + ">"
		case "name":
			if t.Hash != "" {
				s = "<name in task " + codeOf(t.Hash) + ">"
			} else {
				s = "<name>"
			}
		case "host":
			s = "<host>"
		default:
			a, ok := accounts[key]
			if !ok {
				a = "<account " + strconv.Itoa(len(accounts)+1) + ">"
				accounts[key] = a
			}
			s = a
		}
		pats = append(pats, t.Text)
		r.repl = append(r.repl, s)
	}
	r.ac = newMatcher(pats)
	return r
}

var lineTime = regexp.MustCompile(`^(\d{4})[/-](\d{2})[/-](\d{2}) (\d{2}):(\d{2}):(\d{2})`)

// Line returns the line as the export shows it, or false when it is left
// out (older than the history, or following such a line).
func (r *Redactor) Line(s string) (string, bool) {
	if m := lineTime.FindStringSubmatch(s); m != nil {
		n := make([]int, 6)
		for i := range n {
			n[i], _ = strconv.Atoi(m[i+1])
		}
		at := time.Date(n[0], time.Month(n[1]), n[2], n[3], n[4], n[5], 0, time.Local)
		r.keep = !at.Before(r.cut)
	}
	if !r.keep {
		r.Dropped++
		return "", false
	}
	return r.Text(s), true
}

// Text replaces names and then applies the rules, whatever the line's age.
func (r *Redactor) Text(s string) string {
	s, n := r.ac.replace(s, r.repl)
	r.Dict += n
	return r.rules(s)
}

var (
	reURL    = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]{1,15})://([^\s/"'<>]*)([^\s"'<>]*)`)
	reMagnet = regexp.MustCompile(`(?i)\bmagnet:\?[^\s"'<>]*`)
	reHash   = regexp.MustCompile(`\b(?:[0-9a-fA-F]{64}|[0-9a-fA-F]{40})\b`)
	reIPv4   = regexp.MustCompile(`\d{1,3}(?:\.\d{1,3}){3}(?:\.\d+)*`)
	reIPv6   = regexp.MustCompile(`(?i)(?:\b(?:[0-9a-f]{1,4}:){3,7}[0-9a-f]{1,4}\b|(?:\b[0-9a-f]{1,4})?(?::[0-9a-f]{1,4}){0,6}::(?:[0-9a-f]{1,4}\b(?::[0-9a-f]{1,4}\b){0,6})?)(?:%[a-z0-9_.-]+)?`)
	reShare  = regexp.MustCompile(`/share/[^\s"'<>]*`)
	reQuoted = regexp.MustCompile(`"[^"\n]{1,300}"`)
	reVolume = regexp.MustCompile(`(?i)^(?:CACHEDEV\d+|ZFS\d+|USB\w*|external\w*|\w+)_DATA$`)
)

func (r *Redactor) rules(s string) string {
	s = r.replace(reMagnet, s, func(string, string) string { return "<magnet>" })
	s = r.replace(reURL, s, func(m, _ string) string {
		p := reURL.FindStringSubmatch(m)
		host, rest := p[2], p[3]
		if loopback(host) {
			if rest != "" {
				return p[1] + "://" + host + "/…"
			}
			return m
		}
		return p[1] + "://<host>/…"
	})
	s = r.replace(reShare, s, func(m, _ string) string { return sharePath(m) })
	s = r.replace(reHash, s, func(string, string) string { return "<hash>" })
	s = r.replace(reIPv6, s, func(m, _ string) string {
		if m == "::1" || m == "::" || !strings.ContainsAny(m, "0123456789abcdefABCDEF") {
			return m
		}
		return "<IP>"
	})
	s = r.replace(reIPv4, s, func(m, before string) string {
		// Part of a longer number, or five parts and more: not an address
		if strings.Count(m, ".") != 3 || before != "" && strings.ContainsAny(before[len(before)-1:], "0123456789.") {
			return m
		}
		if strings.HasPrefix(m, "127.") || m == "0.0.0.0" {
			return m
		}
		// Version numbers with four parts, right after the word that names
		// them (libtorrent 2.0.15.0, libtorrent/2.0.15.0); nothing else
		b := strings.ToLower(before)
		for _, w := range []string{"libtorrent ", "libtorrent/", "version "} {
			if strings.HasSuffix(b, w) {
				return m
			}
		}
		return "<IP>"
	})
	s = r.replace(reQuoted, s, func(m, _ string) string {
		if strings.HasPrefix(m, `"<`) && strings.HasSuffix(m, `>"`) {
			return m
		}
		return `"…"`
	})
	return s
}

// replace applies f to every match of re (f also gets the text before the
// match); every changed match counts as a rule replacement.
func (r *Redactor) replace(re *regexp.Regexp, s string, f func(m, before string) string) string {
	idx := re.FindAllStringIndex(s, -1)
	if idx == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, ix := range idx {
		m := s[ix[0]:ix[1]]
		out := f(m, s[:ix[0]])
		if out != m {
			r.Rules++
		}
		b.WriteString(s[last:ix[0]])
		b.WriteString(out)
		last = ix[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

func loopback(host string) bool {
	h := strings.ToLower(host)
	if i := strings.LastIndexByte(h, '@'); i >= 0 {
		return false // credentials in the address: never shown
	}
	if strings.HasPrefix(h, "[") {
		h = strings.TrimPrefix(strings.SplitN(h, "]", 2)[0], "[")
	} else if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	return h == "localhost" || h == "::1" || strings.HasPrefix(h, "127.")
}

// sharePath keeps a /share path down to its shared folder (the homes
// share without the account), our own folders under it and placeholders;
// everything below is cut.
func sharePath(p string) string {
	comps := strings.Split(strings.TrimPrefix(p, "/share/"), "/")
	keep := 1
	if reVolume.MatchString(comps[0]) && len(comps) > 1 {
		keep = 2
		if strings.HasPrefix(comps[1], ".") {
			return p // .qpkg and the like: the package's own files
		}
	}
	if keep > len(comps) {
		keep = len(comps)
	}
	out := comps[:keep]
	if strings.EqualFold(out[len(out)-1], "homes") {
		if len(comps) > keep {
			return "/share/" + strings.Join(out, "/") + "/…"
		}
		return p
	}
	for _, c := range comps[keep:] {
		if c == core.TempDirName || c == "" || strings.HasPrefix(c, "<") && strings.HasSuffix(strings.TrimSuffix(c, ".seed"), ">") {
			out = append(out, c)
			continue
		}
		out = append(out, "…")
		break
	}
	return "/share/" + strings.Join(out, "/")
}

// matcher finds many names in a line at once: names are indexed by their
// first four bytes (ASCII letters folded), so thousands of file names cost
// one pass over the line and memory in proportion to the names.
type matcher struct {
	byPrefix map[[4]byte][]int32
	short    map[string][]int32 // names shorter than four bytes
	pats     []string           // folded
}

func fold(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}

func foldString(s string) string {
	b := []byte(s)
	for i := range b {
		b[i] = fold(b[i])
	}
	return string(b)
}

func newMatcher(pats []string) *matcher {
	m := &matcher{byPrefix: map[[4]byte][]int32{}, short: map[string][]int32{}}
	for i, p := range pats {
		f := foldString(p)
		m.pats = append(m.pats, f)
		if len(f) >= 4 {
			k := [4]byte{f[0], f[1], f[2], f[3]}
			m.byPrefix[k] = append(m.byPrefix[k], int32(i))
		} else {
			m.short[f] = append(m.short[f], int32(i))
		}
	}
	return m
}

func wordByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// bounded: a name that starts or ends with a letter or digit only matches
// where the line has no letter or digit next to it, so that "photos" does
// not hide part of "photoshop.log".
func bounded(s, pat string, start int) bool {
	end := start + len(pat)
	if wordByte(pat[0]) && start > 0 && wordByte(s[start-1]) {
		return false
	}
	if wordByte(pat[len(pat)-1]) && end < len(s) && wordByte(s[end]) {
		return false
	}
	return true
}

// replace puts repl[i] in place of every occurrence of name i, the longest
// one where several start at the same place.
func (m *matcher) replace(s string, repl []string) (string, int) {
	if len(m.pats) == 0 {
		return s, 0
	}
	ls := foldString(s)
	var b strings.Builder
	last, n := 0, 0
	for i := 0; i < len(ls); {
		best, bestLen := -1, 0
		try := func(cands []int32) {
			for _, p := range cands {
				pat := m.pats[p]
				if len(pat) > bestLen && i+len(pat) <= len(ls) && ls[i:i+len(pat)] == pat && bounded(ls, pat, i) {
					best, bestLen = int(p), len(pat)
				}
			}
		}
		if i+4 <= len(ls) {
			try(m.byPrefix[[4]byte{ls[i], ls[i+1], ls[i+2], ls[i+3]}])
		}
		for l := 3; best < 0 && l >= 1; l-- {
			if i+l <= len(ls) {
				try(m.short[ls[i:i+l]])
			}
		}
		if best < 0 {
			i++
			continue
		}
		b.WriteString(s[last:i])
		b.WriteString(repl[best])
		i += bestLen
		last = i
		n++
	}
	if n == 0 {
		return s, 0
	}
	b.WriteString(s[last:])
	return b.String(), n
}
