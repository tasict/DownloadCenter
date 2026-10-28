package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/trackers"
)

// MaxTrackers is how many trackers one request may add.
const MaxTrackers = 200

// trackerError answers a tracker address that Check refused.
func trackerError(w http.ResponseWriter, bad string, err error) {
	if errors.Is(err, trackers.ErrNotAllowed) {
		Error(w, 400, "tracker_not_allowed", TrackerMessage(bad, err))
		return
	}
	Error(w, 400, "tracker_invalid", TrackerMessage(bad, err))
}

// TrackerMessage is the (untranslated) message for a refused tracker.
func TrackerMessage(bad string, err error) string {
	return err.Error() + ": " + bad
}

func (s *Server) trackerRoutes() {
	s.Route("POST /tasks/{id}/trackers", "tasks:control", 0, s.addTrackers)
	s.Route("POST /tasks/{id}/find-peers", "tasks:control", 0, s.findPeers)
	s.Route("GET /trackers/public", "tasks:read", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		if s.Public == nil {
			OK(w, trackers.Info{Trackers: trackers.Builtin(), Count: len(trackers.Builtin()), Source: "builtin"})
			return
		}
		OK(w, s.Public.Info())
	})
	s.Route("POST /trackers/public/refresh", "settings:write", AdminOnly, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		if s.Public == nil {
			Error(w, 503, "unavailable", "The public tracker list is not available")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		// A failure keeps the list; the answer says why in error
		in, _ := s.Public.Refresh(ctx)
		OK(w, in)
	})
}

// findPeers makes a torrent without a connection look for peers right away.
func (s *Server) findPeers(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	t := s.visibleTask(w, p, r.PathValue("id"))
	if t == nil {
		return
	}
	done, reason, wait, err := s.M.FindPeers(t.Hash)
	if err != nil {
		Fail(w, err)
		return
	}
	out := map[string]any{"refreshed": done}
	if reason != "" {
		out["reason"] = reason
	}
	if wait > 0 {
		out["retry_after"] = wait
	}
	OK(w, out)
}

// addTrackers adds trackers, and with public the public tracker list, to a
// torrent.
func (s *Server) addTrackers(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	t := s.visibleTask(w, p, r.PathValue("id"))
	if t == nil {
		return
	}
	var b struct {
		Trackers []string `json:"trackers"`
		Public   bool     `json:"public"`
	}
	if err := Decode(r, &b); err != nil {
		Error(w, 400, "bad_request", err.Error())
		return
	}
	if len(b.Trackers) > MaxTrackers {
		Error(w, 400, "bad_request", "Up to 200 trackers at a time")
		return
	}
	list, bad, err := trackers.Check(b.Trackers, p.Admin)
	if err != nil {
		trackerError(w, bad, err)
		return
	}
	if len(list) == 0 && !b.Public {
		Error(w, 400, "bad_request", "No trackers to add")
		return
	}
	res, err := s.M.AddTrackers(t.Hash, list, b.Public)
	if err != nil {
		Fail(w, err)
		return
	}
	OK(w, map[string]any{"ok": true, "added": res.Added, "public_added": res.PublicAdded})
}
