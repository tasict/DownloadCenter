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
			api.Error(w, 502, "update_check_failed", "無法取得更新資訊")
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
				api.Error(w, 409, "update_busy", "已經在更新中")
			case errors.Is(err, ErrNoFeed), errors.Is(err, ErrNotFound):
				api.Error(w, 404, "update_not_found", "找不到這個版本")
			case errors.Is(err, ErrSame):
				api.Error(w, 400, "update_same", "已經是這個版本")
			case errors.Is(err, ErrNoPackage):
				api.Error(w, 400, "update_no_package", "這個版本沒有適合這台 NAS 的套件")
			case errors.Is(err, ErrNoBackup):
				api.Error(w, 409, "update_no_backup", "降回這一版需要當時的資料庫備份，但找不到備份")
			case errors.Is(err, ErrRestoreRequired):
				api.Error(w, 409, "update_restore_required", "降回這一版需要還原當時的資料庫備份")
			default:
				api.Error(w, 500, "update_failed", "無法開始更新")
			}
			return
		}
		api.JSON(w, 202, s.Status())
	})
	return s.Stop
}
