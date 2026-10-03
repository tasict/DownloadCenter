package qts

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// AppName is the name Download Center is registered under in QTS's
// application privileges (Control Panel › Privilege › Users › Edit
// Application Privilege). It must equal the qpkg.conf section, so the dialog
// shows the package's display name.
const AppName = "DownloadCenter"

const apprivPath = "/sbin/appriv"

// Account types of application privilege rows.
const (
	PrivLocalUser   = 1
	PrivLocalGroup  = 2
	PrivDomainUser  = 4
	PrivDomainGroup = 8
)

// PrivEntry is a row of an application's privilege list.
type PrivEntry struct {
	Name string `json:"name"`
	Type int    `json:"type"`
}

// PrivTargetExists reports whether a row names an existing local account or
// group (domain names cannot be checked here and count as existing).
func PrivTargetExists(e PrivEntry) bool {
	switch e.Type {
	case PrivLocalUser:
		_, _, ok := Lookup(e.Name)
		return ok
	case PrivLocalGroup:
		return GroupExists(e.Name)
	}
	return true
}

// Group reports a group row.
func (e PrivEntry) Group() bool { return e.Type == PrivLocalGroup || e.Type == PrivDomainGroup }

// appriv runs /sbin/appriv and returns its output and exit status; tests
// replace it.
var appriv = func(args ...string) (string, int, error) {
	out, err := exec.Command(apprivPath, args...).CombinedOutput()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return string(out), ee.ExitCode(), nil
		}
		return string(out), -1, err
	}
	return string(out), 0, nil
}

// StubAppriv replaces /sbin/appriv (tests); the returned function restores it.
func StubAppriv(fn func(args ...string) (string, int, error)) (restore func()) {
	old := appriv
	appriv = fn
	return func() { appriv = old }
}

// AppPrivAvailable reports whether this firmware has application privileges.
func AppPrivAvailable() bool {
	_, err := os.Stat(apprivPath)
	return err == nil
}

// AppRegistered reports whether Download Center is registered. QTS lets
// everyone use an application that is not registered, so callers must not
// trust AppAllowed without it.
func AppRegistered() bool { return registered(AppName) }

func registered(app string) bool {
	_, code, err := appriv("--get_app", app)
	return err == nil && code == 0
}

// AppAllowed asks QTS whether an account may use Download Center (appriv
// answers 0 for allowed, 254 for denied).
func AppAllowed(user string) bool {
	if user == "" {
		return false
	}
	_, code, err := appriv("-C", "-n", user, "--app_name", AppName)
	return err == nil && code == 0
}

// RegisterApp registers Download Center with white lists for local and
// domain accounts: nobody but the accounts granted in QTS may use it.
func RegisterApp() error {
	return run("--register_app", AppName, "--local", "1", "--domain", "1")
}

// AppGrant grants an account or a group the use of Download Center.
func AppGrant(e PrivEntry) error {
	args := []string{"-A", "-n", e.Name, "--app_name", AppName}
	if e.Group() {
		args = append(args, "--is_group", "1")
	}
	return run(args...)
}

// AppGrants lists the rows stored for Download Center of one account type.
func AppGrants(typ int) ([]PrivEntry, error) { return list(AppName, 0, typ) }

// OfficialGrants lists the accounts (PrivLocalUser) or groups
// (PrivLocalGroup) QTS lets use the official Download Station, whatever its
// policy; nil when it is not registered (QTS would list everyone).
func OfficialGrants(typ int) []PrivEntry {
	if !registered("DownloadStation") {
		return nil
	}
	l, _ := list("DownloadStation", 2, typ)
	return l
}

// list runs appriv -l: mode 0 the stored rows, mode 2 who is granted.
func list(app string, mode, typ int) ([]PrivEntry, error) {
	out, code, err := appriv("-l", "--app_name", app, "--list_mode", strconv.Itoa(mode), "--list_type", strconv.Itoa(typ))
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("appriv -l: exit %d", code)
	}
	return parsePrivList(out, typ), nil
}

func run(args ...string) error {
	out, code, err := appriv(args...)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("appriv %s: exit %d: %s", args[0], code, strings.TrimSpace(out))
	}
	return nil
}

// parsePrivList reads the granted rows of "name:id:type:privilege" lines
// (after a "count = n/m" line and a header); a name may itself hold colons.
func parsePrivList(out string, typ int) []PrivEntry {
	var list []PrivEntry
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		f := strings.Split(line, ":")
		if len(f) < 4 || line == "name:id:type:privilege" {
			continue
		}
		t, err := strconv.Atoi(f[len(f)-2])
		if err != nil || f[len(f)-1] != "1" {
			continue
		}
		if t == 0 {
			t = typ
		}
		list = append(list, PrivEntry{Name: strings.Join(f[:len(f)-3], ":"), Type: t})
	}
	return list
}
