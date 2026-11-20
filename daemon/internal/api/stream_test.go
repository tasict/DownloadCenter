package api

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"downloadcenter/internal/auth"
)

// An open event stream ends once its token no longer may read events,
// even though the token still exists.
func TestStreamEndsWhenRightsChange(t *testing.T) {
	s := fixServer(t)
	old := streamRecheck
	streamRecheck = 50 * time.Millisecond
	defer func() { streamRecheck = old }()
	tok := &auth.Token{Owner: "admin", Name: "events", Scopes: []string{"events:read", "tasks:read"}, Tasks: "all"}
	value, err := s.Auth.CreateToken(tok, true)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Auth.FromToken(value, "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	ticketMu.Lock()
	tickets["tk_test"] = ticket{p: p, expires: time.Now().Add(time.Minute)}
	ticketMu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("GET", "/x?ticket=tk_test", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		s.stream(httptest.NewRecorder(), req, nil)
		close(done)
	}()
	time.Sleep(150 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("stream ended while its rights were unchanged")
	default:
	}
	p.Token.Scopes = []string{"tasks:read"}
	if err := s.Auth.UpdateToken(p.Token, true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("stream kept going without events:read")
	}
}
