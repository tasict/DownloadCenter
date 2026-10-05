package update

import (
	"errors"
	"net/http"

	"downloadcenter/internal/api"
	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
)

// Register adds the update API (administrators signed in to the UI only;
// access tokens never install software) and starts the periodic check.
// It returns the stop function.
func Register(srv *api.Server, m *core.Manager, root, data string) func() {
	s := New(m, m.DB(), root, data, srv.Version)
	go s.Run()
	opts := api.AdminOnly | api.Session
	srv.Route("GET /update", "", opts, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		api.OK(w, s.Status())
	})
	srv.Route("GET /update/job", "", opts, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		api.OK(w, s.Status().Job)
	})
	srv.Route("POST /update/check", "", opts, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		if err := s.Check(); err != nil {
			api.Error(w, 502, "update_check_failed", "Could not get update information")
			return
		}
		api.OK(w, s.Status())
	})
	srv.Route("PUT /update/settings", "", opts, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		var b struct {
			AutoCheck  *bool   `json:"auto_check"`
			Prerelease *bool   `json:"prerelease"`
			Skip       *string `json:"skip"`
		}
		if err := api.Decode(r, &b); err != nil {
			api.Error(w, 400, "bad_request", err.Error())
			return
		}
		s.Settings(b.AutoCheck, b.Prerelease, b.Skip)
		api.OK(w, s.Status())
	})
	srv.Route("POST /update/install", "", opts, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		var b struct {
			Version string `json:"version"`
			Restore bool   `json:"restore"`
		}
		if err := api.Decode(r, &b); err != nil {
			api.Error(w, 400, "bad_request", err.Error())
			return
		}
		if err := s.Install(b.Version, b.Restore); err != nil {
			switch {
			case errors.Is(err, ErrBusy):
				api.Error(w, 409, "update_busy", "An update is already running")
			case errors.Is(err, ErrNoFeed), errors.Is(err, ErrNotFound):
				api.Error(w, 404, "update_not_found", "Version not found")
			case errors.Is(err, ErrSame):
				api.Error(w, 400, "update_same", "This version is already installed")
			case errors.Is(err, ErrNoPackage):
				api.Error(w, 400, "update_no_package", "This version has no package for this NAS")
			case errors.Is(err, ErrNoBackup):
				api.Error(w, 409, "update_no_backup", "Going back to this version needs a database backup from that time, and there is none")
			case errors.Is(err, ErrRestoreRequired):
				api.Error(w, 409, "update_restore_required", "Going back to this version needs the database backup from that time restored")
			default:
				api.Error(w, 500, "update_failed", "Could not start the update")
			}
			return
		}
		api.JSON(w, 202, s.Status())
	})
	return s.Stop
}
