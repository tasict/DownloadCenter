// Package api serves the UI, the REST API v1 (used by the UI with the QTS
// session cookie and by third parties with personal access tokens) and the
// event stream. Other packages (notify, importer, v4) register their routes
// through Server.Route.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/i18n"
	"downloadcenter/internal/qts"
)

// Base is the URL prefix of the package.
const Base = "/DownloadCenter"

// APIBase is the prefix of the REST API.
const APIBase = Base + "/api/v1"

// Server is the HTTP server.
type Server struct {
	M       *core.Manager
	Auth    *auth.Service
	WebDir  string
	Version string
	mux     *http.ServeMux
	Extra   map[string]any // values other packages expose to /me (e.g. import availability)
	// DevUser (dcd serve -dev-user) treats direct loopback requests that did
	// not come through Apache as this user. Development only: the service
	// script never sets it.
	DevUser string
}

// Handler is an authenticated API handler.
type Handler func(w http.ResponseWriter, r *http.Request, p *auth.Principal)

// Route options.
const (
	Session   = 1 << iota // session only (no tokens)
	AdminOnly             // effective administrators only
	Public                // no authentication
)

func New(m *core.Manager, a *auth.Service, webDir, version string) *Server {
	s := &Server{M: m, Auth: a, WebDir: webDir, Version: version, mux: http.NewServeMux(), Extra: map[string]any{}}
	s.routes()
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("api: panic on %s %s: %v", r.Method, r.URL.Path, rec)
			Error(w, 500, "internal", "Internal error")
		}
	}()
	s.mux.ServeHTTP(w, r)
}

// Mux exposes the mux for packages serving non-API paths (V4).
func (s *Server) Mux() *http.ServeMux { return s.mux }

// Route registers an API route: pattern like "GET /tasks/{id}" (relative
// to APIBase), scope required for tokens ("" = any), opts flags.
func (s *Server) Route(pattern, scope string, opts int, h Handler) {
	method, path, _ := strings.Cut(pattern, " ")
	s.mux.HandleFunc(method+" "+APIBase+path, func(w http.ResponseWriter, r *http.Request) {
		s.serveAPI(w, r, scope, opts, h)
	})
}

func (s *Server) serveAPI(w http.ResponseWriter, r *http.Request, scope string, opts int, h Handler) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Messages follow the UI language (the language chosen in QTS)
	rec := &statusRecorder{ResponseWriter: w, status: 200, lang: r.Header.Get("X-DC-Lang")}
	w = rec
	if opts&Public != 0 {
		h(w, r, nil)
		return
	}
	if r.URL.Query().Has("sid") || r.URL.Query().Has("token") || r.URL.Query().Has("access_token") {
		Error(w, 400, "credentials_in_query", "Session ids and tokens are not accepted in the query string")
		return
	}
	p, err := s.Authenticate(r)
	if err != nil {
		s.authError(w, err)
		return
	}
	if p.Via == "token" {
		defer func() { s.Auth.Audit(p.Token.ID, p.User, ClientIP(r), r.Method, r.URL.Path, rec.status) }()
		if opts&Session != 0 {
			Error(rec, 403, "session_required", "This endpoint is available to the web UI only")
			return
		}
		if scope != "" && !p.Can(scope) {
			Error(rec, 403, "insufficient_scope", "The token lacks the scope "+scope)
			return
		}
	} else if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
		// Session cookies need a CSRF guard: browsers do not send this
		// header cross-site without a CORS preflight, which we never allow
		Error(rec, 403, "csrf", "Missing X-Requested-With header")
		return
	}
	if opts&AdminOnly != 0 && !p.Admin {
		Error(rec, 403, "admin_only", "Administrators only")
		return
	}
	h(rec, r, p)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	lang   string
}

// Lang is the UI language of the request (QTS code, "" = source).
func (r *statusRecorder) Lang() string { return r.lang }

// LangOf returns the UI language a response is written in.
func LangOf(w http.ResponseWriter) string {
	if l, ok := w.(interface{ Lang() string }); ok {
		return l.Lang()
	}
	return ""
}

// Tr translates a user-facing message into the response's language.
func Tr(w http.ResponseWriter, msg string) string { return i18n.T(LangOf(w), msg) }

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Authenticate resolves the caller of a request: Bearer token, or the QTS
// session (NAS_SID cookie or X-NAS-SID header).
func (s *Server) Authenticate(r *http.Request) (*auth.Principal, error) {
	if s.DevUser != "" && r.Header.Get("X-DC-Remote") == "" && r.Header.Get("Authorization") == "" && strings.HasPrefix(r.RemoteAddr, "127.0.0.1:") {
		p, err := s.Auth.FromQTS(s.DevUser, qts.IsQTSAdmin(s.DevUser))
		if err == nil {
			p.Via = "session"
		}
		return p, err
	}
	ip := ClientIP(r)
	if h := r.Header.Get("Authorization"); h != "" {
		if tok, ok := strings.CutPrefix(h, "Bearer "); ok {
			p, err := s.Auth.FromToken(strings.TrimSpace(tok), ip)
			if err != nil && (err == auth.ErrTokenInvalid || err == auth.ErrTokenExpired) {
				s.M.Emit(core.Event{Type: "security.token_rejected", Data: map[string]any{"ip": ip, "reason": err.Error()}})
			}
			return p, err
		}
		return nil, auth.ErrTokenInvalid
	}
	sid := r.Header.Get("X-NAS-SID")
	if sid == "" {
		if c, err := r.Cookie("NAS_SID"); err == nil {
			sid = c.Value
		}
	}
	return s.Auth.FromSID(sid, ip, r.UserAgent())
}

func (s *Server) authError(w http.ResponseWriter, err error) {
	switch err {
	case auth.ErrNotOnList:
		Error(w, 403, "not_on_list", "This account does not have access to Download Center yet. Contact your administrator.")
	case auth.ErrTokenExpired:
		Error(w, 401, "token_expired", "The token has expired")
	case auth.ErrIPNotAllowed:
		Error(w, 403, "ip_not_allowed", "The token is not allowed from this address")
	case auth.ErrRateLimited:
		Error(w, 429, "rate_limited", "Too many requests")
	case auth.ErrAuthBackend:
		Error(w, 503, "auth_unavailable", "Cannot confirm the sign-in status with QTS. Try again later.")
	case auth.ErrTokenInvalid:
		Error(w, 401, "token_invalid", "Invalid token")
	default:
		Error(w, 401, "not_signed_in", "Sign in first.")
	}
}

// ClientIP is the address of the browser. Apache (the only peer allowed on
// the loopback port) sets X-DC-Remote; X-Forwarded-For's last hop is the
// fallback.
func ClientIP(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-DC-Remote")); v != "" && net.ParseIP(v) != nil {
		return v
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(ip) != nil {
			return ip
		}
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

// JSON writes v as JSON.
func JSON(w http.ResponseWriter, status int, v any) {
	localize(w, v)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	enc.Encode(v)
}

// OK writes a 200 JSON answer.
func OK(w http.ResponseWriter, v any) { JSON(w, 200, v) }

// Error writes {"error": {"code", "message"}}.
func Error(w http.ResponseWriter, status int, code, msg string) {
	JSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": Tr(w, msg)}})
}

// Fail maps a core error to an HTTP error.
func Fail(w http.ResponseWriter, err error) {
	var dup *core.DupError
	if errors.As(err, &dup) {
		JSON(w, 409, map[string]any{"error": map[string]string{"code": "duplicate", "message": Tr(w, "This task is already in the list")}, "id": dup.ID})
		return
	}
	status, code, msg := coreError(err)
	Error(w, status, code, msg)
}

// coreError is the HTTP status, error code and (untranslated) message of a
// core error, for Fail and for the per-source results of adding tasks.
func coreError(err error) (status int, code, msg string) {
	switch {
	case errors.Is(err, core.ErrDuplicate):
		return 409, "duplicate", "This task is already in the list"
	case errors.Is(err, core.ErrFolder):
		return 403, "folder_not_allowed", "This folder cannot be used"
	case errors.Is(err, core.ErrReadOnly):
		return 403, "folder_read_only", "This folder cannot be written to"
	case errors.Is(err, core.ErrNotFound):
		return 404, "not_found", "Task not found"
	case errors.Is(err, core.ErrNotOwner):
		return 403, "not_owner", "This is not your task"
	case errors.Is(err, core.ErrBadURL):
		return 400, "url_not_supported", "This type of URL is not supported"
	case errors.Is(err, core.ErrBadTorrent):
		return 400, "torrent_invalid", "Invalid torrent file format"
	case errors.Is(err, core.ErrBadMagnet):
		return 400, "magnet_invalid", "Invalid magnet link format"
	case errors.Is(err, core.ErrOtherOwner):
		return 409, "duplicate_other_owner", "Another user is already downloading this torrent"
	case errors.Is(err, core.ErrProxyGone):
		return 400, "proxy_gone", err.Error()
	case errors.Is(err, core.ErrProxyNotAllowed):
		return 403, "proxy_not_allowed", err.Error()
	case errors.Is(err, core.ErrProxyRequired):
		return 403, "proxy_required", err.Error()
	case errors.Is(err, core.ErrNoURL):
		return 400, "url_unavailable", "This NAS cannot download this kind of URL (the download component dc-dl is unavailable)"
	case errors.Is(err, core.ErrNoBT):
		return 400, "bt_unavailable", "This NAS cannot download torrents (the BT engine is missing)"
	case errors.Is(err, core.ErrMoving):
		return 409, "task_moving", "The files of this task are being moved. Try again when the move has finished."
	case errors.Is(err, core.ErrUnsupported):
		return 400, "unsupported", "The current engine does not support this operation"
	}
	return 400, "failed", err.Error()
}

// Decode reads a JSON body (max 4 MB).
func Decode(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("Invalid JSON format: %v", err)
	}
	return nil
}

// --- static files ---

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, Base)
	if rel == "" || rel == "/" {
		rel = "/index.html"
	}
	clean := filepath.Clean("/" + rel)
	p := filepath.Join(s.WebDir, clean)
	if !strings.HasPrefix(p, filepath.Clean(s.WebDir)+"/") {
		http.NotFound(w, r)
		return
	}
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		// SPA: unknown non-file paths get the app shell
		if !strings.Contains(filepath.Base(clean), ".") {
			p = filepath.Join(s.WebDir, "index.html")
		} else {
			http.NotFound(w, r)
			return
		}
	}
	ext := filepath.Ext(p)
	if ct := mime.TypeByExtension(ext); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	switch ext {
	case ".webmanifest":
		w.Header().Set("Content-Type", "application/manifest+json")
	case ".md":
		// The agent skill: shown as text in a browser, fetched as is by curl
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	case ".html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-ancestors 'self'; base-uri 'self'; form-action 'self'")
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	http.ServeFile(w, r, p)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET "+Base+"/healthz", func(w http.ResponseWriter, r *http.Request) {
		OK(w, map[string]any{"ok": true, "version": s.Version, "engines": s.M.EngineHealth(), "time": time.Now().Unix()})
	})
	s.mux.HandleFunc("GET "+Base, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, Base+"/", http.StatusMovedPermanently)
	})
	s.mux.HandleFunc("GET "+Base+"/", s.static)
	s.taskRoutes()
	s.miscRoutes()
	s.settingsRoutes()
	s.eventRoutes()
	s.previewRoutes()
}
