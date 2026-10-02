package auth

import (
	"errors"
	"path/filepath"
	"testing"

	"downloadcenter/internal/store"
)

func TestUpdateToken(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "dc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.X(`INSERT INTO users (name, role, qts_admin, created_at) VALUES ('joe', 'user', 0, 0)`)
	s := New(db)
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
