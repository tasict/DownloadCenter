package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
)

type ticket struct {
	p       *auth.Principal
	expires time.Time
}

var (
	ticketMu sync.Mutex
	tickets  = map[string]ticket{}
)

func (s *Server) eventRoutes() {
	s.Route("GET /events", "events:read", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		var out []core.Event
		for _, e := range s.M.Events(after, limit) {
			if core.Visible(e, p.User, p.Admin, p.AllTasks) {
				out = append(out, e)
			}
		}
		if out == nil {
			out = []core.Event{}
		}
		OK(w, map[string]any{"events": out, "last_id": s.M.LastEventID()})
	})
	s.Route("POST /events/ticket", "events:read", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		id := auth.RandomID("tk_", 32)
		ticketMu.Lock()
		now := time.Now()
		for k, t := range tickets {
			if now.After(t.expires) {
				delete(tickets, k)
			}
		}
		tickets[id] = ticket{p: p, expires: now.Add(30 * time.Second)}
		ticketMu.Unlock()
		OK(w, map[string]any{"ticket": id, "expires_in": 30})
	})
	s.Route("GET /events/stream", "", Public, s.stream)
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request, _ *auth.Principal) {
	var p *auth.Principal
	if tk := r.URL.Query().Get("ticket"); tk != "" {
		ticketMu.Lock()
		t, ok := tickets[tk]
		delete(tickets, tk)
		ticketMu.Unlock()
		if !ok || time.Now().After(t.expires) {
			Error(w, 401, "ticket_invalid", "The ticket is invalid or expired")
			return
		}
		p = t.p
	} else {
		var err error
		if r.Header.Get("Authorization") != "" {
			Error(w, 400, "use_ticket", "Use POST /events/ticket and pass ?ticket=")
			return
		}
		if p, err = s.Authenticate(r); err != nil {
			s.authError(w, err)
			return
		}
	}
	if !p.Can("events:read") {
		Error(w, 403, "insufficient_scope", "The token lacks the scope events:read")
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		Error(w, 500, "no_flush", "Streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(200)
	ch, cancel := s.M.Subscribe()
	defer cancel()
	send := func(e core.Event) {
		if !core.Visible(e, p.User, p.Admin, p.AllTasks) {
			return
		}
		b, _ := json.Marshal(e)
		fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Type, b)
	}
	// Resume after a reconnect
	if last, err := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64); err == nil && last > 0 {
		for _, e := range s.M.Events(last, 500) {
			send(e)
		}
	}
	fmt.Fprintf(w, "retry: 5000\n: connected\n\n")
	fl.Flush()
	hb := time.NewTicker(25 * time.Second)
	defer hb.Stop()
	// Sessions are re-checked periodically: logging out of QTS ends the stream
	recheck := time.NewTicker(2 * time.Minute)
	defer recheck.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			send(e)
			fl.Flush()
		case <-hb.C:
			fmt.Fprintf(w, ": ping %d\n\n", time.Now().Unix())
			fl.Flush()
		case <-recheck.C:
			if p.Via == "token" && p.Token != nil && !s.Auth.TokenExists(p.Token.ID) {
				return
			}
			if p.Via == "session" {
				if _, err := s.Auth.FromSID(p.SID, ClientIP(r), r.UserAgent()); err != nil {
					return
				}
			}
		}
	}
}
