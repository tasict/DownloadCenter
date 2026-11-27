package core

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"downloadcenter/internal/qts"
	"downloadcenter/internal/torrent"
)

// The diagnostic export (Settings › About and updates › Download logs)
// shows tasks by a code instead of their id: an HMAC of the task's hash
// under a key of this NAS (secret diag_key), cut to 8 hex digits. The same
// task has the same code in every export of this NAS, so lines about one
// task can be followed, but whoever gets the file cannot tell which
// content it was.

const diagKeySecret = "diag_key"

func (m *Manager) diagKey() []byte {
	if k, err := hex.DecodeString(m.db.Secret(diagKeySecret)); err == nil && len(k) == 32 {
		return k
	}
	k := make([]byte, 32)
	rand.Read(k)
	m.db.SetSecret(diagKeySecret, hex.EncodeToString(k))
	return k
}

func diagCode(key []byte, hash string) string {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(strings.ToLower(hash)))
	return hex.EncodeToString(h.Sum(nil))[:8]
}

// DiagCode is the code of a task in the diagnostic export.
func (m *Manager) DiagCode(hash string) string { return diagCode(m.diagKey(), hash) }

// TaskByCode finds the task with a diagnostic code, also among the removed
// tasks still kept in the history (removedAt > 0).
func (m *Manager) TaskByCode(code string) (hash string, removedAt int64, ok bool) {
	code = strings.ToLower(strings.TrimSpace(code))
	if len(code) != 8 {
		return "", 0, false
	}
	key := m.diagKey()
	rows, err := m.db.Query(`SELECT hash, removed_at FROM tasks ORDER BY removed_at`)
	if err != nil {
		return "", 0, false
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		var at int64
		if rows.Scan(&h, &at) == nil && diagCode(key, h) == code {
			return h, at, true
		}
	}
	return "", 0, false
}

// DiagTerm is a string the diagnostic export must not show.
type DiagTerm struct {
	Text string
	Kind string // task (an id), name, link, host, account
	Hash string // the task it belongs to ("" for accounts)
}

// DiagTerms lists what the database knows that tells what was downloaded
// or by whom: every task's ids, names, links and hosts, its files and the
// folders below its shared folder, the trackers and files of its .torrent,
// merged sources, and every account name (QTS accounts, home folders,
// site accounts, token names). Removed tasks still in the history count.
func (m *Manager) DiagTerms() []DiagTerm {
	var out []DiagTerm
	add := func(text, kind, hash string) {
		if text = strings.TrimSpace(text); text != "" {
			out = append(out, DiagTerm{text, kind, hash})
		}
	}
	var addLink func(link, hash string)
	addLink = func(link, hash string) {
		add(link, "link", hash)
		if u, err := url.Parse(link); err == nil {
			add(u.Hostname(), "host", hash)
			if u.Scheme == "magnet" {
				q := u.Query()
				add(q.Get("dn"), "name", hash)
				for _, k := range []string{"tr", "ws", "xs", "as"} {
					for _, v := range q[k] {
						addLink(v, hash)
					}
				}
			}
		}
	}
	addPath := func(p, hash string) {
		for _, c := range m.belowShare(p) {
			add(c, "name", hash)
		}
	}
	rows, err := m.db.Query(`SELECT hash, owner, source, name, engine_ref, comment, caller, account, temp_dir, move_dir, work_dir, data_path FROM tasks`)
	if err == nil {
		for rows.Next() {
			var h, owner, src, name, ref, comment, caller, acct, temp, move, work, data string
			if rows.Scan(&h, &owner, &src, &name, &ref, &comment, &caller, &acct, &temp, &move, &work, &data) != nil {
				continue
			}
			add(h, "task", h)
			if ref != h {
				add(ref, "task", h)
			}
			add(owner, "account", "")
			addLink(src, h)
			add(name, "name", h)
			add(comment, "name", h)
			add(acct, "account", "")
			if strings.HasPrefix(caller, "API:") {
				add(strings.TrimPrefix(caller, "API:"), "account", "")
			}
			for _, p := range []string{temp, move, work, data} {
				addPath(p, h)
			}
			if b, err := os.ReadFile(m.torrentPath(h)); err == nil {
				if meta, err := torrent.Parse(b); err == nil {
					add(meta.InfoHashV2, "task", h)
					add(meta.Name, "name", h)
					add(meta.Comment, "name", h)
					for _, tr := range append(meta.Trackers, meta.WebSeeds...) {
						addLink(tr, h)
					}
					for _, f := range meta.Files {
						for _, c := range strings.Split(f.Path, "/") {
							add(c, "name", h)
						}
					}
				}
			}
		}
		rows.Close()
	}
	if rows, err := m.db.Query(`SELECT hash, path FROM task_files`); err == nil {
		for rows.Next() {
			var h, p string
			if rows.Scan(&h, &p) == nil {
				for _, c := range strings.Split(p, "/") {
					add(c, "name", h)
				}
			}
		}
		rows.Close()
	}
	if rows, err := m.db.Query(`SELECT task_hash, source_hash, source, name FROM task_sources`); err == nil {
		for rows.Next() {
			var h, sh, src, name string
			if rows.Scan(&h, &sh, &src, &name) == nil {
				add(sh, "task", h)
				addLink(src, h)
				add(name, "name", h)
			}
		}
		rows.Close()
	}
	if rows, err := m.db.Query(`SELECT owner, host, username FROM accounts`); err == nil {
		for rows.Next() {
			var owner, host, user string
			if rows.Scan(&owner, &host, &user) == nil {
				add(owner, "account", "")
				add(host, "host", "")
				add(user, "account", "")
			}
		}
		rows.Close()
	}
	for _, q := range []string{`SELECT name FROM users`, `SELECT name FROM tokens`, `SELECT owner FROM tokens`} {
		if rows, err := m.db.Query(q); err == nil {
			for rows.Next() {
				var n string
				if rows.Scan(&n) == nil {
					add(n, "account", "")
				}
			}
			rows.Close()
		}
	}
	if hr := homesRoot(); hr != "" {
		if ents, err := os.ReadDir(hr); err == nil {
			for _, e := range ents {
				add(e.Name(), "account", "")
			}
		}
	}
	return out
}

// belowShare is the path components of p below its shared folder (below
// the account's folder for home folders, the account itself included).
func (m *Manager) belowShare(p string) []string {
	if p == "" || !filepath.IsAbs(p) {
		return nil
	}
	p = filepath.Clean(p)
	base := ""
	if hr := homesRoot(); hr != "" && inside(p, hr) {
		base = hr
	} else if sh, ok := qts.ShareOf(p); ok {
		base = sh.Path
	}
	if base == "" {
		return nil
	}
	rel, err := filepath.Rel(base, p)
	if err != nil || rel == "." {
		return nil
	}
	return strings.Split(rel, "/")
}

// DiagCounts is the number of tasks in each state (numbers only).
func (m *Manager) DiagCounts() map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int{}
	for _, t := range m.live {
		out[t.State]++
	}
	return out
}
