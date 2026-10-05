// Package auth resolves who is calling: a QTS session (NAS_SID, validated
// with authLogin.cgi), a personal access token, or a linked chat account.
// Who may use Download Center is QTS's application privilege (Control Panel ›
// Privilege › Users › Edit Application Privilege); QTS administrators always
// may, and they are Download Center's administrators.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"downloadcenter/internal/qts"
	"downloadcenter/internal/store"
)

var (
	ErrNotSignedIn  = errors.New("not_signed_in")
	ErrNotOnList    = errors.New("not_on_list")
	ErrTokenInvalid = errors.New("token_invalid")
	ErrTokenExpired = errors.New("token_expired")
	ErrIPNotAllowed = errors.New("ip_not_allowed")
	ErrRateLimited  = errors.New("rate_limited")
	ErrAuthBackend  = errors.New("auth_unavailable")
)

// Scopes of personal access tokens.
var AllScopes = []string{"tasks:read", "tasks:add", "tasks:control", "tasks:remove", "files:delete",
	"events:read", "notify:manage", "stats:read", "settings:read", "settings:write"}

// AdminScopes need an administrator owner.
var AdminScopes = map[string]bool{"settings:read": true, "settings:write": true}

// Principal is the authenticated caller of a request.
type Principal struct {
	User     string
	Role     string // admin | user
	QTSAdmin bool
	Admin    bool   // administrator: a QTS administrator
	Via      string // session | token | v4 | chat
	SID      string
	Token    *Token
	scopes   map[string]bool
	AllTasks bool
	Folders  []string
	Sources  []string
}

// Can reports whether the caller has a scope (sessions have every scope).
func (p *Principal) Can(scope string) bool {
	if p.scopes == nil {
		if AdminScopes[scope] {
			return p.Admin
		}
		return true
	}
	if AdminScopes[scope] && !p.Admin {
		return false
	}
	return p.scopes[scope]
}

// Scopes returns the effective scopes.
func (p *Principal) Scopes() []string {
	var out []string
	for _, s := range AllScopes {
		if p.Can(s) {
			out = append(out, s)
		}
	}
	return out
}

// SeesOwner reports whether the caller may see tasks of owner.
func (p *Principal) SeesOwner(owner string) bool {
	return (p.Admin && p.AllTasks) || owner == p.User
}

// SourceAllowed applies the token's source restriction.
func (p *Principal) SourceAllowed(kind string) bool {
	if len(p.Sources) == 0 {
		return true
	}
	for _, s := range p.Sources {
		if s == kind {
			return true
		}
	}
	return false
}

// User is what the package keeps per account (preferences, last sign-in);
// the row is made at the first sign-in.
type User struct {
	Name      string         `json:"name"`
	Role      string         `json:"role"`
	QTSAdmin  bool           `json:"qts_admin"`
	Created   int64          `json:"created_at"`
	LastLogin int64          `json:"last_login_at"`
	Prefs     map[string]any `json:"prefs"`
}

// Service is the auth service.
type Service struct {
	db      *store.DB
	mu      sync.Mutex
	cache   map[string]sidEntry
	rate    map[string]*bucket
	fails   map[string]*bucket
	allowed map[string]checked
	reg     checked
}

// checked is a cached answer from QTS.
type checked struct {
	ok      bool
	expires time.Time
}

type sidEntry struct {
	user    string
	admin   bool
	expires time.Time
}

type bucket struct {
	start time.Time
	n     int
}

func New(db *store.DB) *Service {
	return &Service{db: db, cache: map[string]sidEntry{}, rate: map[string]*bucket{}, fails: map[string]*bucket{}, allowed: map[string]checked{}}
}

// FromSID authenticates a QTS session id. The answer is cached for 30 s.
func (s *Service) FromSID(sid, ip, agent string) (*Principal, error) {
	if sid == "" {
		return nil, ErrNotSignedIn
	}
	key := sha(sid)
	s.mu.Lock()
	e, ok := s.cache[key]
	s.mu.Unlock()
	if !ok || time.Now().After(e.expires) {
		a, err := qts.ValidateSID(sid, ip, agent)
		if err != nil {
			return nil, ErrAuthBackend
		}
		if !a.Passed() || a.User() == "" {
			return nil, ErrNotSignedIn
		}
		e = sidEntry{user: a.User(), admin: a.IsAdmin(), expires: time.Now().Add(30 * time.Second)}
		s.mu.Lock()
		if len(s.cache) > 5000 {
			s.cache = map[string]sidEntry{}
		}
		s.cache[key] = e
		s.mu.Unlock()
	}
	p, err := s.principal(e.user, e.admin, true)
	if err != nil {
		return nil, err
	}
	p.Via, p.SID, p.AllTasks = "session", sid, true
	return p, nil
}

// ForgetSID drops a cached session (logout).
func (s *Service) ForgetSID(sid string) {
	s.mu.Lock()
	delete(s.cache, sha(sid))
	s.mu.Unlock()
}

// FromQTS builds a principal for a user QTS already authenticated (V4
// Misc/Login answer).
func (s *Service) FromQTS(user string, qtsAdmin bool) (*Principal, error) {
	p, err := s.principal(user, qtsAdmin, true)
	if err != nil {
		return nil, err
	}
	p.AllTasks = true
	return p, nil
}

// principal applies QTS's application privilege. login records the sign-in
// (and makes the account's row on the first one).
func (s *Service) principal(user string, qtsAdmin bool, login bool) (*Principal, error) {
	if !s.Allowed(user, qtsAdmin) {
		return nil, ErrNotOnList
	}
	role := "user"
	if qtsAdmin {
		role = "admin"
	}
	if login {
		u, err := s.GetUser(user)
		now := time.Now().Unix()
		if err != nil {
			s.db.X(`INSERT INTO users (name, role, qts_admin, created_at, last_login_at) VALUES (?, ?, ?, ?, ?)
				ON CONFLICT(name) DO NOTHING`, user, role, b2i(qtsAdmin), now, now)
		} else if u.QTSAdmin != qtsAdmin || u.Role != role || now-u.LastLogin > 300 {
			s.db.X(`UPDATE users SET role = ?, qts_admin = ?, last_login_at = ? WHERE name = ?`, role, b2i(qtsAdmin), now, user)
		}
	}
	return &Principal{User: user, Role: role, QTSAdmin: qtsAdmin, Admin: qtsAdmin}, nil
}

// IsAdmin reports whether an account is an administrator (a QTS
// administrator), for callers without a session.
func IsAdmin(user string) bool { return qts.IsQTSAdmin(user) }

// Allowed reports whether an account may use Download Center: QTS
// administrators always, everyone else when QTS's application privilege
// grants it. QTS lets everyone use an application that is not registered, so
// without the registration nobody else is let in. Answers are cached for 30 s.
func (s *Service) Allowed(user string, qtsAdmin bool) bool {
	if qtsAdmin {
		return true
	}
	if user == "" || !s.registered() {
		return false
	}
	now := time.Now()
	s.mu.Lock()
	c, ok := s.allowed[user]
	s.mu.Unlock()
	if ok && now.Before(c.expires) {
		return c.ok
	}
	c = checked{ok: qts.AppAllowed(user), expires: now.Add(30 * time.Second)}
	s.mu.Lock()
	if len(s.allowed) > 1000 {
		s.allowed = map[string]checked{}
	}
	s.allowed[user] = c
	s.mu.Unlock()
	return c.ok
}

func (s *Service) registered() bool {
	now := time.Now()
	s.mu.Lock()
	c := s.reg
	s.mu.Unlock()
	if now.Before(c.expires) {
		return c.ok
	}
	c = checked{ok: qts.AppRegistered(), expires: now.Add(30 * time.Second)}
	s.mu.Lock()
	s.reg = c
	s.mu.Unlock()
	return c.ok
}

// forget drops the cached answers (after the grants changed).
func (s *Service) forget() {
	s.mu.Lock()
	s.allowed = map[string]checked{}
	s.reg = checked{}
	s.mu.Unlock()
}

// Member is an account that may use Download Center.
type Member struct {
	Name      string `json:"name"`
	Admin     bool   `json:"admin"`
	LastLogin int64  `json:"last_login_at"`
}

// Access describes who may use Download Center, for the users page.
type Access struct {
	Available  bool     `json:"available"`  // the firmware has application privileges
	Registered bool     `json:"registered"` // Download Center is registered with them
	Members    []Member `json:"members"`
	Groups     []string `json:"groups"` // groups granted in QTS
}

// Members lists the local accounts that may use Download Center now. It asks
// QTS once per account (fresh answers for the users page), so its cost grows
// with the number of accounts.
func (s *Service) Members() Access {
	a := Access{Available: qts.AppPrivAvailable(), Members: []Member{}, Groups: []string{}}
	s.forget()
	a.Registered = a.Available && s.registered()
	last := map[string]int64{}
	if rows, err := s.db.Query(`SELECT name, last_login_at FROM users`); err == nil {
		for rows.Next() {
			var n string
			var t int64
			rows.Scan(&n, &t)
			last[n] = t
		}
		rows.Close()
	}
	for _, acc := range qts.Accounts() {
		if s.Allowed(acc.Name, acc.Admin) {
			a.Members = append(a.Members, Member{Name: acc.Name, Admin: acc.Admin, LastLogin: last[acc.Name]})
		}
	}
	if a.Registered {
		for _, typ := range []int{qts.PrivLocalGroup, qts.PrivDomainGroup} {
			gs, _ := qts.AppGrants(typ)
			for _, g := range gs {
				a.Groups = append(a.Groups, g.Name)
			}
		}
	}
	return a
}

// Grant lets an account use Download Center (QTS application privilege).
func (s *Service) Grant(user string) error {
	if _, _, ok := qts.Lookup(user); !ok {
		return errors.New("QTS account not found")
	}
	err := qts.AppGrant(qts.PrivEntry{Name: user, Type: qts.PrivLocalUser})
	s.forget()
	return err
}

// KeepAppPrivilege keeps Download Center registered with QTS's application
// privileges, once at start and then every minute. The first time it grants
// who could use Download Center or the official Download Station before (see
// initialGrants); when the registration has disappeared it registers again
// and restores the grants it last saw.
func (s *Service) KeepAppPrivilege() {
	if !qts.AppPrivAvailable() {
		return
	}
	go func() {
		for {
			s.keepAppPrivilege()
			time.Sleep(time.Minute)
		}
	}()
}

const (
	metaPrivGrants   = "app_priv_grants"   // the grants last seen in QTS
	metaPrivMigrated = "app_priv_migrated" // the first grants were decided
	metaPrivPending  = "app_priv_pending"  // grants still to apply
	metaPrivTries    = "app_priv_tries"    // attempts at the pending grants
)

var privTypes = []int{qts.PrivLocalUser, qts.PrivLocalGroup, qts.PrivDomainUser, qts.PrivDomainGroup}

// privTargetExists skips grants for accounts and groups deleted since (tests
// replace it).
var privTargetExists = qts.PrivTargetExists

func (s *Service) keepAppPrivilege() {
	fresh := false
	if !qts.AppRegistered() {
		if err := qts.RegisterApp(); err != nil {
			log.Printf("auth: register with QTS application privileges: %v", err)
			return
		}
		fresh = true
	}
	// Decide what to grant once; applying it is retried until it succeeds
	pending := s.db.Meta(metaPrivPending)
	if pending == "" {
		var grants []qts.PrivEntry
		if s.db.Meta(metaPrivMigrated) == "" {
			grants = s.initialGrants()
			log.Printf("auth: registered with QTS application privileges, granting %d accounts and groups", len(grants))
		} else if fresh {
			json.Unmarshal([]byte(s.db.Meta(metaPrivGrants)), &grants)
			log.Printf("auth: registered with QTS application privileges again, restoring %d grants", len(grants))
		}
		if len(grants) > 0 {
			b, _ := json.Marshal(grants)
			pending = string(b)
			s.db.SetMeta(metaPrivPending, pending)
		}
		s.db.SetMeta(metaPrivMigrated, "1")
	}
	if pending != "" {
		var grants, left []qts.PrivEntry
		json.Unmarshal([]byte(pending), &grants)
		for _, g := range grants {
			if !privTargetExists(g) {
				continue
			}
			if err := qts.AppGrant(g); err != nil {
				log.Printf("auth: grant %s: %v", g.Name, err)
				left = append(left, g)
			}
		}
		s.forget()
		if tries, _ := strconv.Atoi(s.db.Meta(metaPrivTries)); len(left) > 0 && tries < 10 {
			b, _ := json.Marshal(left)
			s.db.SetMeta(metaPrivPending, string(b))
			s.db.SetMeta(metaPrivTries, strconv.Itoa(tries+1))
			return
		} else if len(left) > 0 {
			log.Printf("auth: giving up granting %d accounts and groups", len(left))
		}
		s.db.SetMeta(metaPrivPending, "")
		s.db.SetMeta(metaPrivTries, "")
	}
	s.saveGrants()
}

// initialGrants is who may use Download Center when it first registers: the
// administrators group, the accounts on the package's own user list of
// earlier versions, and the accounts and groups QTS let use the official
// Download Station.
func (s *Service) initialGrants() []qts.PrivEntry {
	var grants []qts.PrivEntry
	seen := map[qts.PrivEntry]bool{}
	add := func(e qts.PrivEntry) {
		if e.Name != "" && !seen[e] {
			seen[e] = true
			grants = append(grants, e)
		}
	}
	add(qts.PrivEntry{Name: "administrators", Type: qts.PrivLocalGroup})
	if rows, err := s.db.Query(`SELECT name FROM users ORDER BY name`); err == nil {
		for rows.Next() {
			var n string
			rows.Scan(&n)
			add(qts.PrivEntry{Name: n, Type: qts.PrivLocalUser})
		}
		rows.Close()
	}
	for _, typ := range []int{qts.PrivLocalUser, qts.PrivLocalGroup} {
		for _, e := range qts.OfficialGrants(typ) {
			add(e)
		}
	}
	return grants
}

// saveGrants remembers the grants, to restore them should the registration
// disappear.
func (s *Service) saveGrants() {
	all := []qts.PrivEntry{}
	for _, typ := range privTypes {
		gs, err := qts.AppGrants(typ)
		if err != nil {
			return
		}
		all = append(all, gs...)
	}
	b, _ := json.Marshal(all)
	old := s.db.Meta(metaPrivGrants)
	if string(b) == old {
		return
	}
	if len(all) == 0 && old != "" && old != "[]" {
		// Everything gone at once looks like QTS resetting its list rather
		// than an administrator's choice: keep what can be restored
		log.Printf("auth: QTS lists no grants for Download Center; keeping the last known ones")
		return
	}
	s.db.SetMeta(metaPrivGrants, string(b))
}

// --- users ---

func (s *Service) GetUser(name string) (*User, error) {
	u := &User{}
	var qa int
	var prefs string
	err := s.db.QueryRow(`SELECT name, role, qts_admin, created_at, last_login_at, prefs FROM users WHERE name = ?`, name).
		Scan(&u.Name, &u.Role, &qa, &u.Created, &u.LastLogin, &prefs)
	if err != nil {
		return nil, err
	}
	u.QTSAdmin = qa != 0
	json.Unmarshal([]byte(prefs), &u.Prefs)
	if u.Prefs == nil {
		u.Prefs = map[string]any{}
	}
	return u, nil
}

// SetPrefs merges per-user preferences (theme, sort, last folder).
func (s *Service) SetPrefs(name string, prefs map[string]any) error {
	u, err := s.GetUser(name)
	if err != nil {
		return err
	}
	for k, v := range prefs {
		if v == nil {
			delete(u.Prefs, k)
		} else {
			u.Prefs[k] = v
		}
	}
	b, _ := json.Marshal(u.Prefs)
	if len(b) > 16<<10 {
		return errors.New("prefs too large")
	}
	_, err = s.db.X(`UPDATE users SET prefs = ? WHERE name = ?`, string(b), name)
	return err
}

// --- tokens ---

// Token is a personal access token (the secret is only stored hashed).
type Token struct {
	ID        string   `json:"id"`
	Owner     string   `json:"owner"`
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	Tasks     string   `json:"tasks"`
	Folders   []string `json:"folders"`
	Sources   []string `json:"sources"`
	IPAllow   []string `json:"ip_allow"`
	ExpiresAt int64    `json:"expires_at"`
	RateLimit int      `json:"rate_limit"`
	Created   int64    `json:"created_at"`
	LastUsed  int64    `json:"last_used_at"`
	LastIP    string   `json:"last_used_ip"`
	UseCount  int64    `json:"use_count"`
	hash      string
}

const tokCols = `id, owner, name, secret_hash, scopes, tasks, folders, sources, ip_allow, expires_at, rate_limit, created_at, last_used_at, last_used_ip, use_count`

func scanToken(sc interface{ Scan(...any) error }) (*Token, error) {
	t := &Token{}
	var scopes, folders, sources, ips string
	if err := sc.Scan(&t.ID, &t.Owner, &t.Name, &t.hash, &scopes, &t.Tasks, &folders, &sources, &ips, &t.ExpiresAt, &t.RateLimit, &t.Created, &t.LastUsed, &t.LastIP, &t.UseCount); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(scopes), &t.Scopes)
	json.Unmarshal([]byte(folders), &t.Folders)
	json.Unmarshal([]byte(sources), &t.Sources)
	json.Unmarshal([]byte(ips), &t.IPAllow)
	return t, nil
}

const b62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func randString(n int, alphabet string) string {
	out := make([]byte, n)
	max := big.NewInt(int64(len(alphabet)))
	for i := range out {
		v, _ := rand.Int(rand.Reader, max)
		out[i] = alphabet[v.Int64()]
	}
	return string(out)
}

// CreateToken stores a token and returns it with its one-time full value.
// checkToken validates and normalises what a token may do (new or edited).
func checkToken(t *Token, ownerAdmin bool) error {
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" {
		return errors.New("Enter a name")
	}
	valid := map[string]bool{}
	for _, sc := range AllScopes {
		valid[sc] = true
	}
	var scopes []string
	for _, sc := range t.Scopes {
		if !valid[sc] {
			return errors.New("unknown scope " + sc)
		}
		if AdminScopes[sc] && !ownerAdmin {
			return errors.New("Only administrators can create tokens with settings permissions")
		}
		scopes = append(scopes, sc)
	}
	if len(scopes) == 0 {
		return errors.New("Choose at least one permission")
	}
	t.Scopes = scopes
	if t.Tasks != "all" || !ownerAdmin {
		t.Tasks = "own"
	}
	if !ownerAdmin {
		t.Folders = nil
	}
	for _, ip := range t.IPAllow {
		if _, _, err := net.ParseCIDR(ip); err != nil && net.ParseIP(ip) == nil {
			return errors.New("Invalid IP format: " + ip)
		}
	}
	if t.RateLimit <= 0 {
		t.RateLimit = 120
	}
	return nil
}

func (s *Service) CreateToken(t *Token, ownerAdmin bool) (string, error) {
	if err := checkToken(t, ownerAdmin); err != nil {
		return "", err
	}
	t.ID = "tok_" + strings.ToLower(randString(8, "abcdefghijklmnopqrstuvwxyz0123456789"))
	secret := randString(43, b62)
	t.Created = time.Now().Unix()
	scJ, _ := json.Marshal(t.Scopes)
	fJ, _ := json.Marshal(nonNil(t.Folders))
	sJ, _ := json.Marshal(nonNil(t.Sources))
	iJ, _ := json.Marshal(nonNil(t.IPAllow))
	_, err := s.db.X(`INSERT INTO tokens (id, owner, name, secret_hash, scopes, tasks, folders, sources, ip_allow, expires_at, rate_limit, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, t.ID, t.Owner, t.Name, sha(secret), string(scJ), t.Tasks, string(fJ), string(sJ), string(iJ), t.ExpiresAt, t.RateLimit, t.Created)
	if err != nil {
		return "", err
	}
	return "dct_" + strings.TrimPrefix(t.ID, "tok_") + "_" + secret, nil
}

// UpdateToken changes what an existing token may do; its secret, owner and
// usage stay. The same rules as for a new token apply.
func (s *Service) UpdateToken(t *Token, ownerAdmin bool) error {
	if err := checkToken(t, ownerAdmin); err != nil {
		return err
	}
	scJ, _ := json.Marshal(t.Scopes)
	fJ, _ := json.Marshal(nonNil(t.Folders))
	sJ, _ := json.Marshal(nonNil(t.Sources))
	iJ, _ := json.Marshal(nonNil(t.IPAllow))
	res, err := s.db.X(`UPDATE tokens SET name = ?, scopes = ?, tasks = ?, folders = ?, sources = ?, ip_allow = ?, expires_at = ?, rate_limit = ? WHERE id = ? AND owner = ?`,
		t.Name, string(scJ), t.Tasks, string(fJ), string(sJ), string(iJ), t.ExpiresAt, t.RateLimit, t.ID, t.Owner)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoToken
	}
	return nil
}

// ErrNoToken: no token with this id belongs to the user.
var ErrNoToken = errors.New("Token not found")

// TokenOf returns a token of the user (nil if there is none).
func (s *Service) TokenOf(id, owner string) *Token {
	for _, t := range s.Tokens(owner) {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// RegenerateToken issues a new secret for a token, keeping its settings.
func (s *Service) RegenerateToken(id, owner string) (string, error) {
	secret := randString(43, b62)
	res, err := s.db.X(`UPDATE tokens SET secret_hash = ? WHERE id = ? AND owner = ?`, sha(secret), id, owner)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", errors.New("not found")
	}
	return "dct_" + strings.TrimPrefix(id, "tok_") + "_" + secret, nil
}

func (s *Service) Tokens(owner string) []*Token {
	q := `SELECT ` + tokCols + ` FROM tokens`
	var args []any
	if owner != "" {
		q += ` WHERE owner = ?`
		args = append(args, owner)
	}
	rows, err := s.db.Query(q+` ORDER BY created_at DESC`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*Token
	for rows.Next() {
		if t, err := scanToken(rows); err == nil {
			out = append(out, t)
		}
	}
	return out
}

func (s *Service) RevokeToken(id, owner string) error {
	_, err := s.db.X(`DELETE FROM tokens WHERE id = ? AND owner = ?`, id, owner)
	return err
}

// FromToken authenticates "dct_<id>_<secret>".
func (s *Service) FromToken(raw, ip string) (*Principal, error) {
	if s.failed(ip) {
		return nil, ErrRateLimited
	}
	parts := strings.SplitN(raw, "_", 3)
	if len(parts) != 3 || parts[0] != "dct" {
		s.fail(ip)
		return nil, ErrTokenInvalid
	}
	t, err := scanToken(s.db.QueryRow(`SELECT `+tokCols+` FROM tokens WHERE id = ?`, "tok_"+parts[1]))
	if err != nil || subtle.ConstantTimeCompare([]byte(t.hash), []byte(sha(parts[2]))) != 1 {
		s.fail(ip)
		return nil, ErrTokenInvalid
	}
	if t.ExpiresAt > 0 && time.Now().Unix() > t.ExpiresAt {
		return nil, ErrTokenExpired
	}
	if len(t.IPAllow) > 0 && !ipAllowed(ip, t.IPAllow) {
		return nil, ErrIPNotAllowed
	}
	if !s.allow("tok:"+t.ID, t.RateLimit) {
		return nil, ErrRateLimited
	}
	// The owner's current rights: on the list, QTS admin flag from the
	// account database (no session to ask authLogin.cgi with)
	p, err := s.principal(t.Owner, qts.IsQTSAdmin(t.Owner), false)
	if err != nil {
		return nil, ErrTokenInvalid
	}
	p.Via, p.Token = "token", t
	p.scopes = map[string]bool{}
	for _, sc := range t.Scopes {
		p.scopes[sc] = true
	}
	p.AllTasks = t.Tasks == "all" && p.Admin
	if p.Admin {
		p.Folders = t.Folders
	}
	p.Sources = t.Sources
	s.db.X(`UPDATE tokens SET last_used_at = ?, last_used_ip = ?, use_count = use_count + 1 WHERE id = ?`, time.Now().Unix(), ip, t.ID)
	return p, nil
}

// ForChat builds a principal for a linked chat account with given scopes.
func (s *Service) ForChat(user string, scopes []string) (*Principal, error) {
	p, err := s.principal(user, qts.IsQTSAdmin(user), false)
	if err != nil {
		return nil, err
	}
	p.Via = "chat"
	p.scopes = map[string]bool{}
	for _, sc := range scopes {
		p.scopes[sc] = true
	}
	p.AllTasks = p.Admin
	return p, nil
}

func ipAllowed(ip string, allow []string) bool {
	pip := net.ParseIP(ip)
	if pip == nil {
		return false
	}
	for _, a := range allow {
		if _, n, err := net.ParseCIDR(a); err == nil && n.Contains(pip) {
			return true
		}
		if x := net.ParseIP(a); x != nil && x.Equal(pip) {
			return true
		}
	}
	return false
}

func (s *Service) allow(key string, perMinute int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.rate[key]
	if b == nil || time.Since(b.start) > time.Minute {
		b = &bucket{start: time.Now()}
		s.rate[key] = b
	}
	b.n++
	return b.n <= perMinute
}

func (s *Service) fail(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.fails[ip]
	if b == nil || time.Since(b.start) > 10*time.Minute {
		b = &bucket{start: time.Now()}
		s.fails[ip] = b
	}
	b.n++
}

func (s *Service) failed(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.fails[ip]
	return b != nil && time.Since(b.start) < 10*time.Minute && b.n >= 20
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Audit records a token request.
func (s *Service) Audit(tokenID, owner, ip, method, path string, status int) {
	s.db.X(`INSERT INTO audit (time, token_id, owner, ip, method, path, status) VALUES (?,?,?,?,?,?,?)`,
		time.Now().Unix(), tokenID, owner, ip, method, path, status)
}

// AuditEntry is one audit row.
type AuditEntry struct {
	Time    int64  `json:"time"`
	TokenID string `json:"token_id"`
	Owner   string `json:"owner"`
	IP      string `json:"ip"`
	Method  string `json:"method"`
	Path    string `json:"path"`
	Status  int    `json:"status"`
}

func (s *Service) AuditLog(owner string, limit int) []AuditEntry {
	q := `SELECT time, token_id, owner, ip, method, path, status FROM audit`
	var args []any
	if owner != "" {
		q += ` WHERE owner = ?`
		args = append(args, owner)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var a AuditEntry
		rows.Scan(&a.Time, &a.TokenID, &a.Owner, &a.IP, &a.Method, &a.Path, &a.Status)
		out = append(out, a)
	}
	return out
}

// RandomDigits returns n random decimal digits (pairing codes).
func RandomDigits(n int) string { return randString(n, "0123456789") }

// RandomID returns a random id with a prefix.
func RandomID(prefix string, n int) string {
	return prefix + strings.ToLower(randString(n, "abcdefghijklmnopqrstuvwxyz0123456789"))
}

// TokenExists reports whether a token has not been revoked or expired.
func (s *Service) TokenExists(id string) bool {
	var exp int64
	if err := s.db.QueryRow(`SELECT expires_at FROM tokens WHERE id = ?`, id).Scan(&exp); err != nil {
		return false
	}
	return exp == 0 || time.Now().Unix() < exp
}
