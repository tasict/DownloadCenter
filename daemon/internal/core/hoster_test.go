package core

import (
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

// publicResolver answers like a service whose public links need no
// account: every resolve hands out a new direct URL that never serves
// pages, through the owner's account (Resolve) or without one
// (ResolvePublic, only when free).
type publicResolver struct {
	mu       sync.Mutex
	n        int
	free     bool
	accounts int // Resolve calls
	public   int // ResolvePublic calls
}

func (p *publicResolver) Match(string) (string, bool) { return "pub", true }
func (p *publicResolver) next(acct string) *Resolved {
	p.n++
	return &Resolved{URL: "https://direct.example.com/f?n=" + strconv.Itoa(p.n), Name: "f.bin", NoPages: true, Account: acct}
}
func (p *publicResolver) Resolve(owner, accountID, raw, proxy string) (*Resolved, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.accounts++
	return p.next("acct1"), nil
}
func (p *publicResolver) ResolvePublic(raw, proxy string) (*Resolved, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.free {
		return nil, nil
	}
	p.public++
	return p.next(""), nil
}
func (p *publicResolver) counts() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.accounts, p.public
}

// reresolve drops a task's direct URL and waits until it is resolved again.
func reresolve(t *testing.T, m *Manager, tk *Task) {
	t.Helper()
	m.mu.Lock()
	tk.Options.Direct, tk.Options.NoPages = "", false
	_, err := m.buildAdd(tk)
	m.mu.Unlock()
	if !errors.Is(err, errResolving) {
		t.Fatalf("not resolving: %v", err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		m.mu.Lock()
		busy := m.resolving[tk.Hash]
		m.mu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("resolve did not finish")
		}
	}
}

// The resolver's "no pages" reaches the engine request, also after the
// link is resolved again.
func TestNoPagesReachesEngine(t *testing.T) {
	m := urlManager(t)
	m.Hosters = &publicResolver{free: true}
	res, err := m.AddURL("https://share.example.com/f", AddOptions{Owner: "admin", Admin: true, AutoRemove: "default"})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	tk := m.live[res.ID]
	req, err := m.buildAdd(tk)
	m.mu.Unlock()
	if err != nil || !req.NoPages || len(req.URIs) != 1 || req.URIs[0] != "https://direct.example.com/f?n=1" || !tk.Options.NoPages {
		t.Fatalf("first add: %+v %v", req, err)
	}
	reresolve(t, m, tk)
	m.mu.Lock()
	req, err = m.buildAdd(tk)
	m.mu.Unlock()
	if err != nil || !req.NoPages || req.URIs[0] != "https://direct.example.com/f?n=2" {
		t.Fatalf("after resolving again: %+v %v", req, err)
	}
}

// A task added without an account resolves only what needs none, without
// any account, also when it is resolved again.
func TestNoAccountResolvesPublicOnly(t *testing.T) {
	m := urlManager(t)
	none := AddOptions{Owner: "admin", Admin: true, AutoRemove: "default", AccountMode: "none"}

	pub := &publicResolver{free: true}
	m.Hosters = pub
	res, err := m.AddURL("https://share.example.com/a", none)
	if err != nil {
		t.Fatal(err)
	}
	tk := m.live[res.ID]
	if o := tk.Options; o.Hoster != "pub" || o.Direct == "" || o.HosterAcct != "" {
		t.Errorf("public link not resolved: %+v", o)
	}
	reresolve(t, m, tk)
	if a, p := pub.counts(); a != 0 || p != 2 || tk.Options.Direct == "" {
		t.Errorf("an account was used without one: %d account resolves, %d public", a, p)
	}
	// With an account (auto) the owner's accounts are asked
	res, err = m.AddURL("https://share.example.com/a2", AddOptions{Owner: "admin", Admin: true, AutoRemove: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := pub.counts(); a != 1 || m.live[res.ID].Options.HosterAcct != "acct1" {
		t.Errorf("auto did not use the account: %d", a)
	}

	// A service that needs an account, with or without ResolvePublic
	m.Hosters = &publicResolver{free: false}
	res, err = m.AddURL("https://share.example.com/b", none)
	if err != nil {
		t.Fatal(err)
	}
	if o := m.live[res.ID].Options; o.Hoster != "" || o.Direct != "" {
		t.Errorf("account service resolved without an account: %q", o.Hoster)
	}
	m.Hosters = cookieResolver{&Resolved{URL: "https://share.example.com/c", Headers: []string{"Cookie: a=1"}}}
	res, err = m.AddURL("https://share.example.com/c", none)
	if err != nil {
		t.Fatal(err)
	}
	if o := m.live[res.ID].Options; o.Hoster != "" {
		t.Errorf("cookies used without an account: %q", o.Hoster)
	}
}
