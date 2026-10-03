package auth

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"downloadcenter/internal/qts"
	"downloadcenter/internal/store"
)

// fakeQTS stands in for /sbin/appriv: which applications are registered,
// the accounts QTS allows, and the rows "appriv -l" lists per application and
// account type ("DownloadCenter/1").
type fakeQTS struct {
	registered map[string]bool
	allow      map[string]bool
	rows       map[string]string
	calls      []string
}

func (f *fakeQTS) run(args ...string) (string, int, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	switch args[0] {
	case "--get_app":
		if f.registered[args[1]] {
			return "", 0, nil
		}
		return "", 255, nil
	case "--register_app":
		f.registered[args[1]] = true
	case "-C":
		if f.allow[args[2]] {
			return "Permission Allow", 0, nil
		}
		return "Permission Deny", 254, nil
	case "-l":
		return "count = 1/1\nname:id:type:privilege\n" + f.rows[args[2]+"/"+args[len(args)-1]], 0, nil
	}
	return "", 0, nil
}

func (f *fakeQTS) granted() map[string]bool {
	out := map[string]bool{}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "-A -n ") {
			out[strings.Fields(c)[2]] = true
		}
	}
	return out
}

func newTestService(t *testing.T, f *fakeQTS) (*Service, *store.DB) {
	t.Helper()
	if f.registered == nil {
		f.registered = map[string]bool{}
	}
	t.Cleanup(qts.StubAppriv(f.run))
	old := privTargetExists
	privTargetExists = func(e qts.PrivEntry) bool { return e.Name != "ghost" }
	t.Cleanup(func() { privTargetExists = old })
	db, err := store.Open(filepath.Join(t.TempDir(), "dc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db), db
}

func TestAllowed(t *testing.T) {
	f := &fakeQTS{allow: map[string]bool{"tasict": true}}
	s, db := newTestService(t, f)
	if !s.Allowed("someadmin", true) {
		t.Error("an administrator was refused")
	}
	if len(f.calls) != 0 {
		t.Errorf("administrators need no QTS check: %q", f.calls)
	}
	// Not registered: QTS would let everyone in, so nobody else gets in
	if s.Allowed("tasict", false) {
		t.Error("allowed while Download Center is not registered")
	}
	f.registered["DownloadCenter"] = true
	s.forget()
	if !s.Allowed("tasict", false) || s.Allowed("joe", false) {
		t.Error("QTS's answers were not followed")
	}
	// The first sign-in makes the account's row; the role follows QTS
	p, err := s.principal("tasict", false, true)
	if err != nil || p.Admin || p.Role != "user" {
		t.Fatalf("principal %+v, %v", p, err)
	}
	if u, err := s.GetUser("tasict"); err != nil || u.LastLogin == 0 {
		t.Fatalf("no row after the first sign-in: %v", err)
	}
	if _, err := s.principal("joe", false, true); !errors.Is(err, ErrNotOnList) {
		t.Errorf("joe: %v", err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM users WHERE name = 'joe'`).Scan(&n)
	if n != 0 {
		t.Error("a refused account got a row")
	}
}

func TestKeepAppPrivilege(t *testing.T) {
	f := &fakeQTS{registered: map[string]bool{"DownloadStation": true}, rows: map[string]string{
		"DownloadStation/1": "bingje:0:1:1\ntasict:0:1:1\n",
		"DownloadCenter/1":  "bingje:0:1:1\njoe:0:1:1\ntasict:0:1:1\n", "DownloadCenter/2": "administrators:0:2:1\n"}}
	s, db := newTestService(t, f)
	db.X(`INSERT INTO users (name, role, qts_admin, created_at) VALUES ('joe', 'user', 0, 0), ('tasict', 'admin', 1, 0), ('ghost', 'user', 0, 0)`)
	// First registration: the administrators group, the old user list and
	// whoever could use the official Download Station; deleted accounts are
	// skipped
	s.keepAppPrivilege()
	if f.calls[0] != "--get_app DownloadCenter" || f.calls[1] != "--register_app DownloadCenter --local 1 --domain 1" {
		t.Fatalf("registration: %q", f.calls)
	}
	if g := f.granted(); !reflect.DeepEqual(g, map[string]bool{"administrators": true, "joe": true, "tasict": true, "bingje": true}) {
		t.Fatalf("first grants: %v", g)
	}
	if !strings.Contains(strings.Join(f.calls, "\n"), "-A -n administrators --app_name DownloadCenter --is_group 1") {
		t.Error("the administrators group was not granted as a group")
	}
	if db.Meta(metaPrivGrants) == "" || db.Meta(metaPrivPending) != "" {
		t.Fatal("grants not remembered, or still pending")
	}
	// Registration gone: the grants QTS had come back, not everyone who ever
	// signed in
	db.X(`INSERT INTO users (name, role, qts_admin, created_at) VALUES ('mallory', 'user', 0, 0)`)
	f.registered["DownloadCenter"], f.calls = false, nil
	s.keepAppPrivilege()
	if g := f.granted(); g["mallory"] || !g["joe"] || !g["bingje"] || !g["administrators"] {
		t.Errorf("restore: %v", g)
	}
	// Registered: nothing to do but remember the grants
	f.calls = nil
	s.keepAppPrivilege()
	for _, c := range f.calls {
		if strings.HasPrefix(c, "--register_app") || strings.HasPrefix(c, "-A ") {
			t.Errorf("changed QTS while registered: %q", c)
		}
	}
	// QTS suddenly lists nothing: the last known grants are kept
	f.rows = map[string]string{}
	s.keepAppPrivilege()
	if !strings.Contains(db.Meta(metaPrivGrants), "bingje") {
		t.Error("an empty list replaced the last known grants")
	}
}

// A token never does more than its owner can do now.
func TestTokenSubset(t *testing.T) {
	f := &fakeQTS{registered: map[string]bool{"DownloadCenter": true}, allow: map[string]bool{"joe": true, "exadmin": true}}
	s, _ := newTestService(t, f)
	if _, err := s.CreateToken(&Token{Owner: "joe", Name: "x", Scopes: []string{"tasks:read", "settings:read"}}, false); err == nil {
		t.Error("a regular user made a token with a settings scope")
	}
	tok := &Token{Owner: "joe", Name: "x", Scopes: []string{"tasks:read"}, Tasks: "all", Folders: []string{"Download"}}
	if _, err := s.CreateToken(tok, false); err != nil || tok.Tasks != "own" || tok.Folders != nil {
		t.Errorf("a regular user's token: %+v, %v", tok, err)
	}
	// Made while its owner was an administrator; the owner is not one now
	adm := &Token{Owner: "exadmin", Name: "x", Scopes: []string{"tasks:read", "settings:write"}, Tasks: "all", Folders: []string{"Download"}}
	value, err := s.CreateToken(adm, true)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.FromToken(value, "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Admin || p.Can("settings:write") || p.AllTasks || p.Folders != nil || !p.Can("tasks:read") {
		t.Errorf("token of a former administrator: %+v", p)
	}
	// Made by a regular user who has become an administrator since: still
	// only what was granted then
	up := &Token{Owner: "admin", Name: "x", Scopes: []string{"tasks:read"}, Tasks: "all"}
	uv, err := s.CreateToken(up, false)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := s.FromToken(uv, "192.0.2.1"); err != nil || !p.Admin || p.AllTasks || p.Can("settings:read") || p.Can("tasks:add") {
		t.Errorf("token of a new administrator: %+v, %v", p, err)
	}
	// The owner lost the use of Download Center in QTS
	f.allow["exadmin"] = false
	s.forget()
	if _, err := s.FromToken(value, "192.0.2.1"); err == nil {
		t.Error("the token outlived its owner's access")
	}
}
