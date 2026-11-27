package diag

import (
	"log"
	"net/http"
	"sync"
	"time"

	"downloadcenter/internal/api"
	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
)

// Register adds the export to the API: administrators signed in to the
// interface only (no tokens, no V4, no chat, and not the page's tools for
// an AI agent), one export at a time.
func Register(srv *api.Server, m *core.Manager, root, data string) {
	h := &handlers{src: Source{M: m, Root: root, Data: data, Version: srv.Version, Started: time.Now()}}
	opts := api.AdminOnly | api.Session
	srv.Route("GET /diagnostics/logs", "", opts, h.logs)
	srv.Route("GET /diagnostics/code/{code}", "", opts, h.code)
}

type handlers struct {
	src  Source
	busy sync.Mutex
}

// person: the route already wants an administrator's session; a request
// sent by the page's tools for an AI agent is refused as well.
func person(w http.ResponseWriter, p *auth.Principal) bool {
	if p == nil || !p.Admin || p.Via != "session" || p.Agent {
		api.Error(w, 403, "session_required", "This endpoint is available to the web UI only")
		return false
	}
	return true
}

func (h *handlers) logs(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	if !person(w, p) {
		return
	}
	if !h.busy.TryLock() {
		api.Error(w, 429, "rate_limited", "Too many requests")
		return
	}
	defer h.busy.Unlock()
	name := "DownloadCenter-logs-" + h.src.Version + "-" + time.Now().Format("20060102-1504") + ".zip"
	hd := w.Header()
	hd.Set("Content-Type", "application/zip")
	hd.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Content-Type-Options", "nosniff")
	if err := Export(w, h.src); err != nil {
		log.Printf("diag: export: %v", err)
	}
}

// code finds the task behind a diagnostic code (Settings › About and updates).
func (h *handlers) code(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	if !person(w, p) {
		return
	}
	hash, removed, ok := h.src.M.TaskByCode(r.PathValue("code"))
	if !ok {
		api.Error(w, 404, "not_found", "Task not found")
		return
	}
	api.OK(w, map[string]any{"id": hash, "removed_at": removed})
}
