package auth

import (
	"errors"
	"testing"
)

func TestUpdateToken(t *testing.T) {
	s, _ := newTestService(t, &fakeQTS{registered: map[string]bool{"DownloadCenter": true}, allow: map[string]bool{"joe": true}})
	tok := &Token{Owner: "joe", Name: "bot", Scopes: []string{"tasks:read", "tasks:add"}}
	value, err := s.CreateToken(tok, false)
	if err != nil {
		t.Fatal(err)
	}
	ed := s.TokenOf(tok.ID, "joe")
	ed.Name, ed.Scopes, ed.RateLimit = " renamed ", []string{"tasks:read"}, 30
	if err := s.UpdateToken(ed, false); err != nil {
		t.Fatal(err)
	}
	got := s.TokenOf(tok.ID, "joe")
	if got.Name != "renamed" || len(got.Scopes) != 1 || got.RateLimit != 30 {
		t.Fatalf("after update: %+v", got)
	}
	// The token value keeps working, with the new scopes
	p, err := s.FromToken(value, "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if sc := p.Scopes(); len(sc) != 1 || sc[0] != "tasks:read" {
		t.Fatalf("scopes after update: %v", sc)
	}
	// Same rules as a new token; nobody edits another user's token
	ed.Scopes = []string{"settings:write"}
	if err := s.UpdateToken(ed, false); err == nil {
		t.Fatal("a regular user's token got a settings scope")
	}
	other := *got
	other.Owner = "mallory"
	if err := s.UpdateToken(&other, false); !errors.Is(err, ErrNoToken) {
		t.Fatalf("editing another user's token: %v", err)
	}
}

// search:use is a scope of its own: regular users may have it, nothing
// else implies it, and a session can search.
func TestSearchScope(t *testing.T) {
	s, _ := newTestService(t, &fakeQTS{registered: map[string]bool{"DownloadCenter": true}, allow: map[string]bool{"joe": true}})
	full := &Token{Owner: "joe", Name: "full", Scopes: []string{"tasks:read", "tasks:add", "tasks:control", "tasks:remove", "files:delete", "stats:read", "events:read", "notify:manage"}}
	value, err := s.CreateToken(full, false)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := s.FromToken(value, "192.0.2.1"); err != nil || p.Can("search:use") {
		t.Fatalf("other scopes imply search:use: %v", err)
	}
	search := &Token{Owner: "joe", Name: "search", Scopes: []string{"search:use"}}
	value, err = s.CreateToken(search, false)
	if err != nil {
		t.Fatalf("regular user's search token: %v", err)
	}
	if p, err := s.FromToken(value, "192.0.2.1"); err != nil || !p.Can("search:use") || p.Can("tasks:add") {
		t.Fatalf("search token: %v", err)
	}
	if p := (&Principal{User: "joe"}); !p.Can("search:use") {
		t.Fatal("a session cannot search")
	}
}
