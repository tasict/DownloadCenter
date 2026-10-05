package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"downloadcenter/internal/api"
	"downloadcenter/internal/auth"
)

// Adapter is a declarative notification service (INTEGRATION.md §7): one
// HTTP request per event, described by a JSON manifest. It cannot run code.
type Adapter struct {
	ID      string         `json:"adapter"`
	Title   string         `json:"title"`
	Fields  []Field        `json:"-"`
	Request AdapterRequest `json:"request"`
}

// AdapterRequest is the request template of an adapter.
type AdapterRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    any               `json:"body"`
}

var adapterIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
var fieldKeyRe = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// ParseManifest validates an adapter manifest.
func ParseManifest(b []byte) (*Adapter, error) {
	var raw struct {
		Adapter string `json:"adapter"`
		Title   string `json:"title"`
		Fields  []struct {
			Key      string `json:"key"`
			Label    string `json:"label"`
			Type     string `json:"type"`
			Secret   bool   `json:"secret"`
			Required *bool  `json:"required"`
			Help     string `json:"help"`
		} `json:"fields"`
		Request AdapterRequest `json:"request"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, errors.New("The manifest is not valid JSON")
	}
	if !adapterIDRe.MatchString(raw.Adapter) {
		return nil, errors.New("Adapter names can only use lowercase letters, digits, - and _")
	}
	if serviceDef(raw.Adapter) != nil {
		return nil, errors.New("The adapter name duplicates a built-in service")
	}
	if strings.TrimSpace(raw.Title) == "" {
		return nil, errors.New("title is missing")
	}
	a := &Adapter{ID: raw.Adapter, Title: strings.TrimSpace(raw.Title), Request: raw.Request}
	seen := map[string]bool{}
	for _, f := range raw.Fields {
		if !fieldKeyRe.MatchString(f.Key) || seen[f.Key] {
			return nil, errors.New("Invalid field name: " + f.Key)
		}
		seen[f.Key] = true
		typ := "text"
		switch {
		case f.Secret:
			typ = "secret"
		case f.Type == "url" || f.Type == "number":
			typ = f.Type
		}
		req := true
		if f.Required != nil {
			req = *f.Required
		}
		label := f.Label
		if label == "" {
			label = f.Key
		}
		a.Fields = append(a.Fields, Field{Key: f.Key, Label: label, Type: typ, Required: req, Help: f.Help})
	}
	a.Request.Method = strings.ToUpper(strings.TrimSpace(a.Request.Method))
	if a.Request.Method == "" {
		a.Request.Method = "POST"
	}
	switch a.Request.Method {
	case "GET", "POST", "PUT", "PATCH":
	default:
		return nil, errors.New("request.method must be GET, POST, PUT or PATCH")
	}
	if a.Request.URL == "" {
		return nil, errors.New("request.url is missing")
	}
	if !strings.HasPrefix(a.Request.URL, "{{fields.") && !strings.HasPrefix(a.Request.URL, "https://") && !strings.HasPrefix(a.Request.URL, "http://") {
		return nil, errors.New("request.url must start with http(s):// or {{fields.…}}")
	}
	for k := range a.Request.Headers {
		lk := strings.ToLower(k)
		if lk == "host" || lk == "content-length" || strings.ContainsAny(k, "\r\n:") {
			return nil, errors.New("This header cannot be set: " + k)
		}
	}
	return a, nil
}

type storedAdapter struct {
	Adapter
	FieldsJSON []Field `json:"fields"`
}

func (s *Service) adapters() []*Adapter {
	rows, err := s.db.Query(`SELECT manifest FROM adapters ORDER BY title`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*Adapter
	for rows.Next() {
		var m string
		rows.Scan(&m)
		var st storedAdapter
		if json.Unmarshal([]byte(m), &st) == nil {
			a := st.Adapter
			a.Fields = st.FieldsJSON
			out = append(out, &a)
		}
	}
	return out
}

func (s *Service) adapter(id string) *Adapter {
	for _, a := range s.adapters() {
		if a.ID == id {
			return a
		}
	}
	return nil
}

func (s *Service) adapterViews() []map[string]any {
	out := []map[string]any{}
	for _, a := range s.adapters() {
		out = append(out, map[string]any{"id": a.ID, "title": a.Title, "fields": a.Fields, "method": a.Request.Method})
	}
	return out
}

func (s *Service) sendAdapter(cl *http.Client, ch *Channel, cfg map[string]string, msg *Message) (int, error) {
	id := strings.TrimPrefix(ch.Service, "adapter:")
	a := s.adapter(id)
	if a == nil {
		return 0, errors.New("This adapter has been removed")
	}
	v := Vars{}
	for k, x := range msg.Vars {
		v[k] = x
	}
	if ch.Template == "" {
		v["event.body"] = msg.Body
	}
	for k, x := range cfg {
		v["fields."+k] = x
	}
	target := RenderURL(a.Request.URL, v)
	if err := checkURL(target); err != nil {
		return 0, err
	}
	var body []byte
	if a.Request.Body != nil && a.Request.Method != "GET" {
		if str, ok := a.Request.Body.(string); ok {
			body = []byte(Render(str, v, nil))
		} else {
			body, _ = json.Marshal(RenderJSON(a.Request.Body, v))
		}
	}
	req, err := http.NewRequest(a.Request.Method, target, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	for k, x := range a.Request.Headers {
		req.Header.Set(k, RenderHeader(x, v))
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return do(cl, req)
}

func (s *Service) adapterRoutes(srv *api.Server) {
	srv.Route("GET /adapters", "notify:manage", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		api.OK(w, map[string]any{"adapters": s.adapterViews()})
	})
	srv.Route("POST /adapters", "settings:write", api.AdminOnly, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		var b struct {
			Manifest json.RawMessage `json:"manifest"`
		}
		if err := api.Decode(r, &b); err != nil {
			api.Error(w, 400, "bad_request", err.Error())
			return
		}
		raw := []byte(b.Manifest)
		// The manifest may be sent as a JSON string as well
		var str string
		if json.Unmarshal(raw, &str) == nil {
			raw = []byte(str)
		}
		a, err := ParseManifest(raw)
		if err != nil {
			api.Error(w, 400, "invalid", err.Error())
			return
		}
		st := storedAdapter{Adapter: *a, FieldsJSON: a.Fields}
		m, _ := json.Marshal(st)
		if _, err := s.db.X(`INSERT INTO adapters (id, title, manifest, created_at) VALUES (?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET title = excluded.title, manifest = excluded.manifest`, a.ID, a.Title, string(m), s.now().Unix()); err != nil {
			api.Error(w, 500, "failed", err.Error())
			return
		}
		api.OK(w, map[string]any{"adapter": map[string]any{"id": a.ID, "title": a.Title, "fields": a.Fields}})
	})
	srv.Route("DELETE /adapters/{id}", "settings:write", api.AdminOnly, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		id := r.PathValue("id")
		s.db.X(`DELETE FROM adapters WHERE id = ?`, id)
		s.db.X(`UPDATE channels SET enabled = 0 WHERE service = ?`, "adapter:"+id)
		api.OK(w, map[string]any{"ok": true})
	})
}
