package core

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
)

// Account is a stored site credential or file-hosting account. Secrets
// (password, API key, cookies) are kept in the secrets table and never
// returned.
type Account struct {
	ID        string         `json:"id"`
	Owner     string         `json:"owner"`
	Kind      string         `json:"kind"` // site | 1fichier | rapidgator | realdebrid | alldebrid | cookies
	Host      string         `json:"host"`
	Username  string         `json:"username"`
	Enabled   bool           `json:"enabled"`
	Info      map[string]any `json:"info"`
	Created   int64          `json:"created_at"`
	HasSecret bool           `json:"has_secret"`
}

// AccountKinds lists the supported account kinds.
var AccountKinds = []string{"site", "1fichier", "rapidgator", "realdebrid", "alldebrid", "cookies"}

func scanAccount(s scanner) (*Account, error) {
	a := &Account{}
	var en int
	var info string
	if err := s.Scan(&a.ID, &a.Owner, &a.Kind, &a.Host, &a.Username, &en, &info, &a.Created); err != nil {
		return nil, err
	}
	a.Enabled = en != 0
	json.Unmarshal([]byte(info), &a.Info)
	if a.Info == nil {
		a.Info = map[string]any{}
	}
	return a, nil
}

// Accounts lists the accounts of an owner ("" = everyone).
func (m *Manager) Accounts(owner string) []*Account {
	q := `SELECT id, owner, kind, host, username, enabled, info, created_at FROM accounts`
	var args []any
	if owner != "" {
		q += ` WHERE owner = ?`
		args = append(args, owner)
	}
	rows, err := m.db.Query(q+` ORDER BY kind, host, username`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*Account
	for rows.Next() {
		if a, err := scanAccount(rows); err == nil {
			a.HasSecret = m.db.Secret("account:"+a.ID) != ""
			out = append(out, a)
		}
	}
	return out
}

// Account returns one account.
func (m *Manager) Account(id string) (*Account, error) {
	a, err := scanAccount(m.db.QueryRow(`SELECT id, owner, kind, host, username, enabled, info, created_at FROM accounts WHERE id = ?`, id))
	if err != nil {
		return nil, ErrNotFound
	}
	a.HasSecret = m.db.Secret("account:"+a.ID) != ""
	return a, nil
}

// AccountSecret returns the secret of an account (internal use only).
func (m *Manager) AccountSecret(id string) string { return m.db.Secret("account:" + id) }

// SaveAccount creates or updates an account. An empty secret keeps the
// stored one.
func (m *Manager) SaveAccount(a *Account, secret string) error {
	ok := false
	for _, k := range AccountKinds {
		if a.Kind == k {
			ok = true
		}
	}
	if !ok {
		return errors.New("unknown account kind")
	}
	a.Host = strings.ToLower(strings.TrimSpace(a.Host))
	if a.Kind == "site" && a.Host == "" {
		return errors.New("請填網站")
	}
	if a.ID == "" {
		b := make([]byte, 20)
		rand.Read(b)
		s := sha1.Sum(b)
		a.ID = hex.EncodeToString(s[:])
		a.Created = time.Now().Unix()
	}
	if a.Info == nil {
		a.Info = map[string]any{}
	}
	info, _ := json.Marshal(a.Info)
	if _, err := m.db.X(`INSERT INTO accounts (id, owner, kind, host, username, enabled, info, created_at) VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET kind=excluded.kind, host=excluded.host, username=excluded.username, enabled=excluded.enabled, info=excluded.info`,
		a.ID, a.Owner, a.Kind, a.Host, a.Username, b2i(a.Enabled), string(info), a.Created); err != nil {
		return err
	}
	if secret != "" {
		return m.db.SetSecret("account:"+a.ID, secret)
	}
	return nil
}

// SetAccountInfo stores verification results (plan, expiry, traffic).
func (m *Manager) SetAccountInfo(id string, info map[string]any) {
	b, _ := json.Marshal(info)
	m.db.X(`UPDATE accounts SET info = ? WHERE id = ?`, string(b), id)
}

// DeleteAccount removes an account and its secret.
func (m *Manager) DeleteAccount(id string) error {
	m.db.X(`DELETE FROM accounts WHERE id = ?`, id)
	return m.db.SetSecret("account:"+id, "")
}

// siteCredentials picks the HTTP/FTP credentials of a URL task.
func (m *Manager) siteCredentials(t *Task) (user, pass string, ok bool) {
	switch t.Options.AccountMode {
	case "none":
		return "", "", false
	case "manual":
		s := m.db.Secret("task:" + t.Hash)
		if s == "" {
			return "", "", false
		}
		u, p, _ := strings.Cut(s, "\x00")
		return u, p, true
	case "id":
		a, err := m.Account(t.Options.AccountID)
		if err != nil || a.Owner != t.Owner || !a.Enabled {
			return "", "", false
		}
		return a.Username, m.AccountSecret(a.ID), true
	}
	// auto: a site account of the owner whose host matches
	u, err := url.Parse(t.Source)
	if err != nil {
		return "", "", false
	}
	host := strings.ToLower(u.Hostname())
	for _, a := range m.Accounts(t.Owner) {
		if a.Kind != "site" || !a.Enabled {
			continue
		}
		h := a.Host
		if hu, err := url.Parse(h); err == nil && hu.Host != "" {
			h = hu.Hostname()
		}
		if h == host || strings.HasSuffix(host, "."+h) {
			return a.Username, m.AccountSecret(a.ID), true
		}
	}
	return "", "", false
}
