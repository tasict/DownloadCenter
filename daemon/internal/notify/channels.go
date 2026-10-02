package notify

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"downloadcenter/internal/api"
	"downloadcenter/internal/auth"
)

// Channel is a notification channel.
type Channel struct {
	ID         string            `json:"id"`
	Owner      string            `json:"owner"`
	Service    string            `json:"service"`
	Name       string            `json:"name"`
	Config     map[string]string `json:"config"`
	SecretSet  []string          `json:"secret_fields_set"`
	Events     []string          `json:"events"`
	Operate    bool              `json:"operate"`
	Scope      string            `json:"scope"`
	Quiet      string            `json:"quiet"`
	Digest     int               `json:"digest"`
	Template   string            `json:"template"`
	Enabled    bool              `json:"enabled"`
	FailCount  int               `json:"fail_count"`
	Linked     int               `json:"linked"`
	CreatedAt  int64             `json:"created_at"`
	InboundURL string            `json:"inbound_url,omitempty"`
	State      map[string]any    `json:"-"`
}

const chanCols = `id, owner, service, name, config, events, operate, scope, quiet, digest, template, enabled, fail_count, state, created_at`

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Service) loadChannels(where string, args ...any) []*Channel {
	rows, err := s.db.Query(`SELECT `+chanCols+` FROM channels WHERE `+where+` ORDER BY created_at`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*Channel
	for rows.Next() {
		c := &Channel{}
		var cfg, events, state string
		var op, en int
		if rows.Scan(&c.ID, &c.Owner, &c.Service, &c.Name, &cfg, &events, &op, &c.Scope, &c.Quiet, &c.Digest, &c.Template, &en, &c.FailCount, &state, &c.CreatedAt) != nil {
			continue
		}
		c.Operate, c.Enabled = op != 0, en != 0
		json.Unmarshal([]byte(cfg), &c.Config)
		json.Unmarshal([]byte(events), &c.Events)
		json.Unmarshal([]byte(state), &c.State)
		if c.Config == nil {
			c.Config = map[string]string{}
		}
		if c.State == nil {
			c.State = map[string]any{}
		}
		if c.Events == nil {
			c.Events = []string{}
		}
		out = append(out, c)
	}
	return out
}

func (s *Service) channel(id string) *Channel {
	l := s.loadChannels(`id = ?`, id)
	if len(l) == 1 {
		return l[0]
	}
	return nil
}

func (s *Service) saveChannel(c *Channel) error {
	cfg, _ := json.Marshal(c.Config)
	ev, _ := json.Marshal(c.Events)
	st, _ := json.Marshal(c.State)
	_, err := s.db.X(`INSERT INTO channels (`+chanCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, config=excluded.config, events=excluded.events, operate=excluded.operate,
		scope=excluded.scope, quiet=excluded.quiet, digest=excluded.digest, template=excluded.template, enabled=excluded.enabled,
		fail_count=excluded.fail_count, state=excluded.state`,
		c.ID, c.Owner, c.Service, c.Name, string(cfg), string(ev), b2i(c.Operate), c.Scope, c.Quiet, c.Digest, c.Template,
		b2i(c.Enabled), c.FailCount, string(st), c.CreatedAt)
	return err
}

func (s *Service) setState(c *Channel, key string, v any) {
	c.State[key] = v
	st, _ := json.Marshal(c.State)
	s.db.X(`UPDATE channels SET state = ? WHERE id = ?`, string(st), c.ID)
}

// view fills the derived fields for the API.
func (s *Service) view(c *Channel) *Channel {
	v := *c
	v.SecretSet = []string{}
	for _, f := range s.fields(c.Service) {
		if f.Type == "secret" && s.db.Secret("channel:"+c.ID+":"+f.Key) != "" {
			v.SecretSet = append(v.SecretSet, f.Key)
		}
	}
	s.db.QueryRow(`SELECT COUNT(*) FROM chat_links WHERE channel_id = ?`, c.ID).Scan(&v.Linked)
	if c.Service == "line" {
		if base := strings.TrimRight(s.m.Settings().ExternalURL, "/"); strings.HasPrefix(base, "https://") {
			v.InboundURL = base + api.APIBase + "/line/" + c.ID
		}
	}
	return &v
}

// channelInput is the body of POST/PATCH /channels.
type channelInput struct {
	Service  *string           `json:"service"`
	Name     *string           `json:"name"`
	Config   map[string]any    `json:"config"`
	Events   *[]string         `json:"events"`
	Operate  *bool             `json:"operate"`
	Scope    *string           `json:"scope"`
	Quiet    *string           `json:"quiet"`
	Digest   *int              `json:"digest"`
	Template *string           `json:"template"`
	Enabled  *bool             `json:"enabled"`
	URL      *string           `json:"url"` // webhook alias
	extra    map[string]string // secrets to store
}

func randomSecret() string {
	b := make([]byte, 24)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// apply validates in against c (a new or existing channel) for principal p.
// It returns the secrets to store (empty values keep the stored one).
func (s *Service) apply(c *Channel, in *channelInput, p *auth.Principal, isNew bool) (map[string]string, error) {
	if in.Name != nil {
		c.Name = strings.TrimSpace(*in.Name)
	}
	fields := s.fields(c.Service)
	if fields == nil && serviceDef(c.Service) == nil {
		return nil, errors.New("不支援這個服務")
	}
	if d := serviceDef(c.Service); d != nil && d.AdminOnly && !p.Admin {
		return nil, errors.New("只有系統管理者可以使用這個服務")
	}
	if in.URL != nil {
		if in.Config == nil {
			in.Config = map[string]any{}
		}
		in.Config["url"] = *in.URL
	}
	secrets := map[string]string{}
	for _, f := range fields {
		raw, given := in.Config[f.Key]
		val := ""
		if given && raw != nil {
			switch x := raw.(type) {
			case string:
				val = strings.TrimSpace(x)
			case float64:
				val = strconv.FormatFloat(x, 'f', -1, 64)
			case bool:
				if x {
					val = "1"
				}
			}
		}
		if f.Type == "secret" {
			if val != "" {
				secrets[f.Key] = val
			}
			if f.Required && val == "" && (isNew || s.db.Secret("channel:"+c.ID+":"+f.Key) == "") {
				if !(c.Service == "webhook" && f.Key == "secret") {
					return nil, errors.New("請填「" + f.Label + "」")
				}
			}
			continue
		}
		if given {
			c.Config[f.Key] = val
		}
		if f.Required && c.Config[f.Key] == "" {
			return nil, errors.New("請填「" + f.Label + "」")
		}
		if f.Type == "url" && c.Config[f.Key] != "" {
			if err := checkURL(c.Config[f.Key]); err != nil {
				return nil, errors.New("「" + f.Label + "」" + err.Error())
			}
		}
	}
	for _, k := range []string{"webhook_url"} {
		if v := secrets[k]; v != "" {
			if err := checkURL(v); err != nil {
				return nil, err
			}
		}
	}
	if in.Events != nil {
		valid := map[string]bool{"*": true, "notify.disabled": true}
		for _, t := range EventTypes {
			valid[t] = true
		}
		c.Events = []string{}
		for _, t := range *in.Events {
			if valid[t] {
				c.Events = append(c.Events, t)
			}
		}
	} else if isNew {
		c.Events = append([]string{}, DefaultEvents...)
	}
	if in.Scope != nil {
		c.Scope = *in.Scope
	}
	if c.Scope != "all" || !p.Admin {
		c.Scope = "own"
	}
	if in.Quiet != nil {
		q := strings.TrimSpace(*in.Quiet)
		if q != "" {
			if a, b, ok := strings.Cut(q, "-"); !ok || !validHM(a) || !validHM(b) {
				return nil, errors.New("勿擾時段的格式是 22:00-07:00")
			}
		}
		c.Quiet = q
	}
	if in.Digest != nil {
		c.Digest = *in.Digest
		if c.Digest < 0 {
			c.Digest = 0
		}
		if c.Digest > 1440 {
			c.Digest = 1440
		}
	}
	if in.Template != nil {
		c.Template = *in.Template
		if len(c.Template) > 4000 {
			return nil, errors.New("訊息範本太長")
		}
	}
	if in.Enabled != nil {
		if *in.Enabled && !c.Enabled {
			c.FailCount = 0
		}
		c.Enabled = *in.Enabled
	}
	if in.Operate != nil {
		c.Operate = *in.Operate
	}
	if c.Operate {
		switch c.Service {
		case "telegram":
		case "line":
			if !strings.HasPrefix(s.m.Settings().ExternalURL, "https://") {
				return nil, errors.New("LINE 要在頻道裡操作下載，必須先在設定裡填寫 NAS 的對外 HTTPS 網址")
			}
		default:
			c.Operate = false
		}
	}
	if c.Name == "" {
		if d := serviceDef(c.Service); d != nil {
			c.Name = d.Title
		} else {
			c.Name = c.Service
		}
	}
	return secrets, nil
}

func validHM(s string) bool {
	_, ok := parseHM(s)
	return ok
}

func (s *Service) storeSecrets(id string, secrets map[string]string) {
	for k, v := range secrets {
		s.db.SetSecret("channel:"+id+":"+k, v)
	}
}

// ownChannel loads a channel the caller may manage.
func (s *Service) ownChannel(w http.ResponseWriter, p *auth.Principal, id string, service string) *Channel {
	c := s.channel(id)
	if c == nil || (c.Owner != p.User && !(p.Admin && p.Via == "session")) || (service != "" && c.Service != service) {
		api.Error(w, 404, "not_found", "找不到這個頻道")
		return nil
	}
	return c
}

func (s *Service) servicesView(w http.ResponseWriter, p *auth.Principal) []map[string]any {
	var out []map[string]any
	for _, d := range Services {
		if d.AdminOnly && !p.Admin {
			continue
		}
		ops := d.Ops
		if d.ID == "line" && !strings.HasPrefix(s.m.Settings().ExternalURL, "https://") {
			ops = "https"
		}
		// Titles, field labels and help in the UI's language
		fields := make([]Field, len(d.Fields))
		for i, f := range d.Fields {
			f.Label, f.Help = api.Tr(w, f.Label), api.Tr(w, f.Help)
			fields[i] = f
		}
		out = append(out, map[string]any{"id": d.ID, "title": api.Tr(w, d.Title), "fields": fields, "ops": ops, "help": api.Tr(w, d.Help)})
	}
	for _, a := range s.adapters() {
		out = append(out, map[string]any{"id": "adapter:" + a.ID, "title": a.Title, "fields": a.Fields, "ops": "no", "adapter": true})
	}
	return out
}

func (s *Service) listChannels(w http.ResponseWriter, r *http.Request, p *auth.Principal, service string) {
	var list []*Channel
	if p.Admin && r.URL.Query().Get("all") == "1" {
		list = s.loadChannels(`1 = 1`)
	} else {
		list = s.loadChannels(`owner = ?`, p.User)
	}
	out := []*Channel{}
	for _, c := range list {
		if service != "" && c.Service != service {
			continue
		}
		out = append(out, s.view(c))
	}
	if service == "webhook" {
		api.OK(w, map[string]any{"webhooks": out, "channels": out})
		return
	}
	api.OK(w, map[string]any{"channels": out, "services": s.servicesView(w, p), "events": EventTypes, "adapters": s.adapterViews()})
}

func (s *Service) createChannel(w http.ResponseWriter, r *http.Request, p *auth.Principal, service string) {
	var in channelInput
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, 400, "bad_request", err.Error())
		return
	}
	if service == "" {
		if in.Service == nil {
			api.Error(w, 400, "bad_request", "請選擇服務")
			return
		}
		service = *in.Service
	}
	if service == "webhook" && in.Name == nil {
		n := "Webhook"
		in.Name = &n
	}
	c := &Channel{ID: auth.RandomID("ch_", 10), Owner: p.User, Service: service, Config: map[string]string{}, Enabled: true,
		Scope: "own", CreatedAt: s.now().Unix(), State: map[string]any{}}
	secrets, err := s.apply(c, &in, p, true)
	if err != nil {
		api.Error(w, 400, "invalid", err.Error())
		return
	}
	generated := ""
	if service == "webhook" && secrets["secret"] == "" {
		generated = randomSecret()
		secrets["secret"] = generated
	}
	if err := s.saveChannel(c); err != nil {
		api.Error(w, 500, "failed", err.Error())
		return
	}
	s.storeSecrets(c.ID, secrets)
	resp := map[string]any{"channel": s.view(c)}
	if service == "webhook" {
		resp["webhook"] = resp["channel"]
		resp["secret"] = firstNonEmpty(generated, secrets["secret"])
	}
	if c.Operate {
		if pair, err := s.newPair(c, p, nil); err == nil {
			resp["pair"] = pair
		}
	}
	s.reconcilePollers()
	api.OK(w, resp)
}

func (s *Service) patchChannel(w http.ResponseWriter, r *http.Request, p *auth.Principal, service string) {
	c := s.ownChannel(w, p, r.PathValue("id"), service)
	if c == nil {
		return
	}
	var in channelInput
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, 400, "bad_request", err.Error())
		return
	}
	in.Service = nil
	owner := p
	if c.Owner != p.User {
		if op, err := s.au.FromQTS(c.Owner, s.isAdmin(c.Owner)); err == nil {
			owner = op
		}
	}
	wasOperate := c.Operate
	secrets, err := s.apply(c, &in, owner, false)
	if err != nil {
		api.Error(w, 400, "invalid", err.Error())
		return
	}
	if err := s.saveChannel(c); err != nil {
		api.Error(w, 500, "failed", err.Error())
		return
	}
	s.storeSecrets(c.ID, secrets)
	if wasOperate && !c.Operate {
		s.db.X(`DELETE FROM pair_codes WHERE channel_id = ?`, c.ID)
	}
	resp := map[string]any{"channel": s.view(c)}
	if !wasOperate && c.Operate {
		if pair, err := s.newPair(c, owner, nil); err == nil {
			resp["pair"] = pair
		}
	}
	s.reconcilePollers()
	api.OK(w, resp)
}

func (s *Service) deleteChannel(w http.ResponseWriter, r *http.Request, p *auth.Principal, service string) {
	c := s.ownChannel(w, p, r.PathValue("id"), service)
	if c == nil {
		return
	}
	s.db.X(`DELETE FROM channels WHERE id = ?`, c.ID)
	s.db.X(`DELETE FROM deliveries WHERE channel_id = ?`, c.ID)
	s.db.X(`DELETE FROM chat_links WHERE channel_id = ?`, c.ID)
	s.db.X(`DELETE FROM pair_codes WHERE channel_id = ?`, c.ID)
	s.db.X(`DELETE FROM notify_digest WHERE channel_id = ?`, c.ID)
	s.db.DeleteSecrets("channel:" + c.ID + ":")
	s.reconcilePollers()
	api.OK(w, map[string]any{"ok": true})
}

// Pair is a pairing code for linking a chat account.
type Pair struct {
	Code      string `json:"code"`
	ExpiresAt int64  `json:"expires_at"`
	Command   string `json:"command"`
}

// chatScopes are granted to a linked chat account by default: full task
// control without deleting data.
var chatScopes = []string{"tasks:read", "tasks:add", "tasks:control", "tasks:remove", "stats:read", "events:read"}

func (s *Service) newPair(c *Channel, p *auth.Principal, scopes []string) (*Pair, error) {
	if !c.Operate {
		return nil, errors.New("這個頻道沒有開啟「在頻道裡操作下載」")
	}
	if len(scopes) == 0 {
		scopes = chatScopes
	}
	var granted []string
	valid := map[string]bool{}
	for _, sc := range auth.AllScopes {
		valid[sc] = true
	}
	for _, sc := range scopes {
		if valid[sc] && p.Can(sc) {
			granted = append(granted, sc)
		}
	}
	if len(granted) == 0 {
		return nil, errors.New("沒有可以授權的權限")
	}
	b, _ := json.Marshal(granted)
	exp := s.now().Add(10 * time.Minute).Unix()
	for i := 0; i < 5; i++ {
		code := auth.RandomDigits(6)
		if _, err := s.db.X(`INSERT INTO pair_codes (code, channel_id, qts_user, scopes, expires_at) VALUES (?,?,?,?,?)`, code, c.ID, p.User, string(b), exp); err == nil {
			return &Pair{Code: code, ExpiresAt: exp, Command: "/link " + code}, nil
		}
	}
	return nil, errors.New("無法產生配對碼")
}

// Link binds a chat user with a pairing code. It returns the QTS user.
func (s *Service) Link(channelID, chatUser, code string) (string, error) {
	code = strings.TrimSpace(code)
	var user, scopes string
	var exp int64
	err := s.db.QueryRow(`SELECT qts_user, scopes, expires_at FROM pair_codes WHERE code = ? AND channel_id = ?`, code, channelID).Scan(&user, &scopes, &exp)
	if err != nil || exp < s.now().Unix() {
		// Brute-force guard: after 5 wrong codes in 10 minutes every pending
		// code of the channel is dropped (6 digits would otherwise fall to
		// guessing within a code's lifetime)
		linkFailMu.Lock()
		f := linkFails[channelID]
		if f == nil || s.now().Sub(f.start) > 10*time.Minute {
			f = &linkFail{start: s.now()}
			linkFails[channelID] = f
		}
		f.n++
		drop := f.n >= 5
		linkFailMu.Unlock()
		if drop {
			s.db.X(`DELETE FROM pair_codes WHERE channel_id = ?`, channelID)
		}
		return "", errors.New("配對碼不正確或已過期。請在 Download Center 重新產生。")
	}
	s.db.X(`DELETE FROM pair_codes WHERE code = ?`, code)
	_, err = s.db.X(`INSERT INTO chat_links (channel_id, chat_user, qts_user, scopes, created_at) VALUES (?,?,?,?,?)
		ON CONFLICT(channel_id, chat_user) DO UPDATE SET qts_user = excluded.qts_user, scopes = excluded.scopes, created_at = excluded.created_at`,
		channelID, chatUser, user, scopes, s.now().Unix())
	return user, err
}

// linked returns the principal of a linked chat user (nil when unlinked).
func (s *Service) linked(channelID, chatUser string) *auth.Principal {
	var user, scopes string
	if s.db.QueryRow(`SELECT qts_user, scopes FROM chat_links WHERE channel_id = ? AND chat_user = ?`, channelID, chatUser).Scan(&user, &scopes) != nil {
		return nil
	}
	var sc []string
	json.Unmarshal([]byte(scopes), &sc)
	p, err := s.au.ForChat(user, sc)
	if err != nil {
		return nil
	}
	return p
}

type linkRow struct {
	ChatUser  string   `json:"chat_user"`
	QTSUser   string   `json:"qts_user"`
	Scopes    []string `json:"scopes"`
	CreatedAt int64    `json:"created_at"`
}

func (s *Service) links(channelID string) []linkRow {
	rows, err := s.db.Query(`SELECT chat_user, qts_user, scopes, created_at FROM chat_links WHERE channel_id = ? ORDER BY created_at`, channelID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []linkRow{}
	for rows.Next() {
		var l linkRow
		var sc string
		rows.Scan(&l.ChatUser, &l.QTSUser, &sc, &l.CreatedAt)
		json.Unmarshal([]byte(sc), &l.Scopes)
		out = append(out, l)
	}
	return out
}

type deliveryRow struct {
	ID         int64  `json:"id"`
	EventID    int64  `json:"event_id"`
	EventType  string `json:"event_type"`
	Delivery   string `json:"delivery"`
	Status     string `json:"status"`
	HTTPStatus int    `json:"http_status"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error"`
	Time       int64  `json:"time"`
	Attempt    int    `json:"attempt"`
	NextAt     int64  `json:"next_at,omitempty"`
}

func (s *Service) deliveries(channelID string) []deliveryRow {
	rows, err := s.db.Query(`SELECT d.id, d.event_id, COALESCE(e.type, ''), d.delivery, d.status, d.http_status, d.duration_ms, d.error, d.time, d.attempt, d.next_at
		FROM deliveries d LEFT JOIN events e ON e.id = d.event_id WHERE d.channel_id = ? ORDER BY d.id DESC LIMIT 50`, channelID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []deliveryRow{}
	for rows.Next() {
		var d deliveryRow
		rows.Scan(&d.ID, &d.EventID, &d.EventType, &d.Delivery, &d.Status, &d.HTTPStatus, &d.DurationMS, &d.Error, &d.Time, &d.Attempt, &d.NextAt)
		switch {
		case d.EventID == 0:
			d.EventType = "test"
		case d.EventID == -1:
			d.EventType = "digest"
		}
		if d.Status != "pending" {
			d.NextAt = 0
		}
		out = append(out, d)
	}
	return out
}

func (s *Service) channelRoutes(srv *api.Server) {
	for _, prefix := range []string{"/channels", "/webhooks"} {
		service := ""
		if prefix == "/webhooks" {
			service = "webhook"
		}
		srv.Route("GET "+prefix, "notify:manage", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			s.listChannels(w, r, p, service)
		})
		srv.Route("POST "+prefix, "notify:manage", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			s.createChannel(w, r, p, service)
		})
		srv.Route("PATCH "+prefix+"/{id}", "notify:manage", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			s.patchChannel(w, r, p, service)
		})
		srv.Route("DELETE "+prefix+"/{id}", "notify:manage", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			s.deleteChannel(w, r, p, service)
		})
		srv.Route("POST "+prefix+"/{id}/test", "notify:manage", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			c := s.ownChannel(w, p, r.PathValue("id"), service)
			if c == nil {
				return
			}
			ok, msg := s.TestChannel(c)
			api.OK(w, map[string]any{"ok": ok, "error": api.Tr(w, msg)})
		})
		srv.Route("GET "+prefix+"/{id}/deliveries", "notify:manage", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			c := s.ownChannel(w, p, r.PathValue("id"), service)
			if c == nil {
				return
			}
			rows := s.deliveries(c.ID)
			for i := range rows {
				rows[i].Error = api.Tr(w, rows[i].Error)
			}
			api.OK(w, map[string]any{"deliveries": rows})
		})
	}
	srv.Route("POST /channels/{id}/pair", "notify:manage", api.Session, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		c := s.ownChannel(w, p, r.PathValue("id"), "")
		if c == nil {
			return
		}
		if c.Owner != p.User {
			api.Error(w, 403, "not_owner", "只有頻道的擁有者可以產生配對碼")
			return
		}
		var b struct {
			Scopes []string `json:"scopes"`
		}
		api.Decode(r, &b)
		pair, err := s.newPair(c, p, b.Scopes)
		if err != nil {
			api.Error(w, 400, "invalid", err.Error())
			return
		}
		api.OK(w, pair)
	})
	srv.Route("GET /channels/{id}/links", "notify:manage", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		c := s.ownChannel(w, p, r.PathValue("id"), "")
		if c == nil {
			return
		}
		api.OK(w, map[string]any{"links": s.links(c.ID)})
	})
	srv.Route("DELETE /channels/{id}/links/{chat_user}", "notify:manage", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		c := s.ownChannel(w, p, r.PathValue("id"), "")
		if c == nil {
			return
		}
		s.db.X(`DELETE FROM chat_links WHERE channel_id = ? AND chat_user = ?`, c.ID, r.PathValue("chat_user"))
		api.OK(w, map[string]any{"ok": true})
	})
}

type linkFail struct {
	start time.Time
	n     int
}

var (
	linkFailMu sync.Mutex
	linkFails  = map[string]*linkFail{}
)
