package analytics

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"downloadcenter/internal/api"
	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
)

// state is what /me tells administrators (read when /me is answered).
type state struct{ s *Service }

func (v state) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]bool{"enabled": v.s.Enabled(), "asked": v.s.db.Meta(kAck) == "1"})
}

// Register adds the statistics API and starts the daily report. It returns
// the stop function.
func Register(srv *api.Server, m *core.Manager, root, data string) func() {
	arch := ""
	if b, err := os.ReadFile(filepath.Join(root, "bin", "arch")); err == nil {
		arch = strings.TrimSpace(string(b))
	}
	s := New(m, m.DB(), data, srv.Version, arch)
	go s.Run()
	srv.Extra["analytics"] = state{s}
	api.Counter = s.count
	opts := api.AdminOnly | api.Session
	srv.Route("GET /analytics", "", opts, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		api.OK(w, state{s})
	})
	srv.Route("PUT /analytics", "", opts, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		var b struct {
			Enabled *bool `json:"enabled"`
		}
		if err := api.Decode(r, &b); err != nil || b.Enabled == nil {
			api.Error(w, 400, "bad_request", "enabled required")
			return
		}
		s.SetEnabled(*b.Enabled)
		api.OK(w, state{s})
	})
	// Counters from the web UI of any signed-in user (fixed names, capped)
	srv.Route("POST /analytics/ui", "", api.Session, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		var b struct {
			Counts map[string]int64 `json:"counts"`
		}
		if err := api.Decode(r, &b); err != nil {
			api.Error(w, 400, "bad_request", err.Error())
			return
		}
		s.UI(p.User, b.Counts)
		w.WriteHeader(http.StatusNoContent)
	})
	return s.Stop
}
