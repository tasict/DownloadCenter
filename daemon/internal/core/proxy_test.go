package core

import (
	"errors"
	"path/filepath"
	"testing"

	"downloadcenter/internal/engine"
	"downloadcenter/internal/store"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "dc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db, dir)
}

func TestProxyFor(t *testing.T) {
	m := testManager(t)
	s := m.Settings()
	s.Proxy.Profiles = []ProxyProfile{
		{ID: "a", Name: "A", Type: "socks5", Host: "10.0.0.1", Port: 1080, RemoteDNS: true, Sites: "example.com", ForUsers: true},
		{ID: "b", Name: "B", Type: "http", Host: "10.0.0.2", Port: 3128, NoProxy: "lan.local,192.168.0.0/16"},
	}
	s.Proxy.URLDefault = "b"
	s.Proxy.RequireForUsers = true
	if err := m.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	m.SetProxyPassword("a", "p:w")
	check := func(choice, link string, admin bool, want string, wantErr error) {
		t.Helper()
		p, err := m.ProxyFor(choice, link, admin)
		got := ""
		if p != nil {
			got = p.ID
		}
		if got != want || !errors.Is(err, wantErr) {
			t.Errorf("ProxyFor(%q, %q, admin=%v) = %q, %v; want %q, %v", choice, link, admin, got, err, want, wantErr)
		}
	}
	check("", "http://dl.example.com/f", true, "a", nil) // site rule
	check("auto", "http://other.org/f", true, "b", nil)  // default
	check("", "http://lan.local/f", true, "", nil)       // no-proxy list of the default
	check("", "http://192.168.1.5/f", false, "", nil)    // no-proxy: allowed even when required
	check("none", "http://other.org/f", true, "", nil)   // admins may go direct
	check("none", "http://other.org/f", false, "", ErrProxyRequired)
	check("b", "http://other.org/f", false, "", ErrProxyNotAllowed) // not offered to users
	check("", "http://other.org/f", false, "b", nil)                // but the default applies to them
	check("gone", "http://other.org/f", true, "", ErrProxyGone)
	if u := m.ProxyURL("a"); u != "socks5h://10.0.0.1:1080" {
		t.Errorf("profile URL without user: %s", u)
	}
	s = m.Settings()
	s.Proxy.Profiles[0].User = "me"
	m.SaveSettings(s)
	if u := m.ProxyURL("a"); u != "socks5h://me:p%3Aw@10.0.0.1:1080" {
		t.Errorf("profile URL with user: %s", u)
	}
	// Deleting a profile clears the references to it
	s = m.Settings()
	s.Proxy.Profiles = s.Proxy.Profiles[:1]
	m.SaveSettings(s)
	if m.Settings().Proxy.URLDefault != "" {
		t.Error("default still points at a deleted profile")
	}
}

func TestMigrateProxy(t *testing.T) {
	m := testManager(t)
	m.db.PutJSON("config", map[string]any{"proxy": map[string]any{
		"type": "socks5", "host": "vpn.lan", "port": 1080, "user": "u", "remote_dns": true,
		"apply_url": true, "apply_trackers": true, "apply_peers": false, "force": true, "no_proxy": "localhost", "notify": false}})
	m.db.SetSecret("proxy:password", "secret")
	m.loadSettings()
	px := m.Settings().Proxy
	if len(px.Profiles) != 1 {
		t.Fatalf("profiles: %+v", px.Profiles)
	}
	p := px.Profiles[0]
	if p.Type != "socks5" || p.Host != "vpn.lan" || p.User != "u" || !p.RemoteDNS || p.NoProxy != "localhost" || !p.ForUsers {
		t.Errorf("profile: %+v", p)
	}
	if px.URLDefault != p.ID || px.BT != p.ID || px.NotifyProfile != "" || !px.ApplyTrackers || px.ApplyPeers || !px.Force {
		t.Errorf("assignments: %+v", px)
	}
	if m.ProxyPassword(p.ID) != "secret" || m.db.Secret("proxy:password") != "" {
		t.Error("password not moved")
	}
	// Loading again changes nothing
	m.loadSettings()
	if len(m.Settings().Proxy.Profiles) != 1 {
		t.Error("migrated twice")
	}
}

func TestAddURLKeepsProxyChoice(t *testing.T) {
	m := testManager(t)
	m.URL = supportsAll{}
	s := m.Settings()
	s.TempDir = t.TempDir()
	s.Proxy.Profiles = []ProxyProfile{{ID: "a", Name: "A", Type: "http", Host: "10.0.0.1", Port: 3128, Sites: "example.com"}}
	m.SaveSettings(s)
	for choice, want := range map[string]string{"none": "none", "a": "a", "auto": "", "": ""} {
		res, err := m.AddURL("http://dl.example.com/"+choice+".bin", AddOptions{Owner: "admin", Admin: true, Proxy: choice, Paused: true})
		if err != nil {
			t.Fatal(err)
		}
		if got := m.Live(res.ID).Options.Proxy; got != want {
			t.Errorf("choice %q stored as %q, want %q", choice, got, want)
		}
	}
}

// supportsAll stands in for the URL engine (only its protocol check is used).
type supportsAll struct{ engine.Engine }

func (supportsAll) Name() string                    { return "builtin" }
func (supportsAll) Supports(string) bool            { return true }
func (supportsAll) ApplyGlobal(engine.Global) error { return nil }
