// Package qts wraps the parts of QTS the package depends on: shared folders,
// home folders, local accounts and the firmware's configuration files.
package qts

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Share is a QTS shared folder.
type Share struct {
	Name string `json:"name"`
	Path string `json:"path"` // real path, e.g. /share/CACHEDEV1_DATA/Download
}

var (
	shareMu    sync.Mutex
	shareCache []Share
	shareAt    time.Time
)

// Shares lists the shared folders from smb.conf, without system and hidden
// entries. The list is cached for 30 seconds.
func Shares() []Share {
	shareMu.Lock()
	defer shareMu.Unlock()
	if shareCache != nil && time.Since(shareAt) < 30*time.Second {
		return shareCache
	}
	var out []Share
	f, err := os.Open("/etc/config/smb.conf")
	if err == nil {
		defer f.Close()
		var name string
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
				name = line[1 : len(line)-1]
				continue
			}
			if name == "" {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok || strings.TrimSpace(strings.ToLower(k)) != "path" {
				continue
			}
			p := strings.TrimSpace(v)
			lname := strings.ToLower(name)
			if lname == "global" || lname == "homes" || lname == "home" || lname == "printers" ||
				strings.HasPrefix(name, ".") || strings.HasPrefix(name, "@") ||
				strings.Contains(p, "%") || strings.Contains(p, ".@msdfs_root") || !strings.HasPrefix(p, "/share/") {
				continue
			}
			if st, err := os.Stat(p); err != nil || !st.IsDir() {
				continue
			}
			out = append(out, Share{Name: name, Path: p})
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	shareCache, shareAt = out, time.Now()
	return out
}

// HomesRoot is the real path of the homes share ("" when the home folder
// service is off).
func HomesRoot() string {
	p, err := filepath.EvalSymlinks("/share/homes")
	if err != nil {
		return ""
	}
	if st, err := os.Stat(p); err != nil || !st.IsDir() {
		return ""
	}
	return p
}

// HomeDir returns the real path of a user's home folder, or "".
func HomeDir(user string) string {
	root := HomesRoot()
	if root == "" || user == "" || strings.ContainsAny(user, "/\x00") || user == "." || user == ".." {
		return ""
	}
	p := filepath.Join(root, user)
	if st, err := os.Stat(p); err == nil && st.IsDir() {
		return p
	}
	return ""
}

// PublicDir is the real path of the Public share.
func PublicDir() string {
	for _, s := range Shares() {
		if strings.EqualFold(s.Name, "Public") {
			return s.Path
		}
	}
	if p, err := filepath.EvalSymlinks("/share/Public"); err == nil {
		return p
	}
	return "/share/Public"
}

// ShareOf returns the share containing the real path p ("homes" counts as a
// share for home folders).
func ShareOf(p string) (Share, bool) {
	p = filepath.Clean(p)
	best := Share{}
	for _, s := range Shares() {
		if p == s.Path || strings.HasPrefix(p, s.Path+"/") {
			if len(s.Path) > len(best.Path) {
				best = s
			}
		}
	}
	if hr := HomesRoot(); hr != "" && (p == hr || strings.HasPrefix(p, hr+"/")) {
		if len(hr) > len(best.Path) {
			best = Share{Name: "homes", Path: hr}
		}
	}
	return best, best.Path != ""
}

// Account is a local QTS account.
type Account struct {
	Name  string `json:"name"`
	UID   int    `json:"uid"`
	GID   int    `json:"gid"`
	Admin bool   `json:"admin"`
}

// Accounts lists local accounts that can sign in (uid 0 admin and uid >= 500,
// without guest).
func Accounts() []Account {
	admins := groupMembers("administrators")
	var out []Account
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Split(sc.Text(), ":")
		if len(parts) < 4 {
			continue
		}
		uid, _ := strconv.Atoi(parts[2])
		gid, _ := strconv.Atoi(parts[3])
		name := parts[0]
		if name == "guest" || (uid < 500 && name != "admin") || uid >= 65534 {
			continue
		}
		out = append(out, Account{Name: name, UID: uid, GID: gid, Admin: admins[name] || uid == 0})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Lookup returns the uid and gid of a local account.
func Lookup(name string) (uid, gid int, ok bool) {
	for _, a := range Accounts() {
		if a.Name == name {
			return a.UID, a.GID, true
		}
	}
	return 0, 0, false
}

// IsQTSAdmin reports membership of the administrators group.
func IsQTSAdmin(name string) bool {
	return name == "admin" || groupMembers("administrators")[name]
}

func groupMembers(group string) map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile("/etc/group")
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.Split(line, ":")
		if len(parts) >= 4 && parts[0] == group {
			for _, m := range strings.Split(parts[3], ",") {
				if m = strings.TrimSpace(m); m != "" {
					out[m] = true
				}
			}
		}
	}
	return out
}

// GetCfg reads a value with /sbin/getcfg (QTS INI files).
func GetCfg(section, key, file, def string) string {
	args := []string{section, key, "-d", def}
	if file != "" {
		args = append(args, "-f", file)
	}
	out, err := exec.Command("/sbin/getcfg", args...).Output()
	if err != nil {
		return def
	}
	return strings.TrimSpace(string(out))
}

// Firmware is the QTS version, e.g. "5.2.10".
func Firmware() string { return GetCfg("System", "Version", "", "0.0.0") }

// Hostname is the NAS name.
func Hostname() string {
	h, _ := os.Hostname()
	return h
}

// QPKGInstalled reports whether a QPKG is installed and enabled.
func QPKGInstalled(name string) (installed, enabled bool) {
	path := GetCfg(name, "Install_Path", "/etc/config/qpkg.conf", "")
	if path == "" {
		return false, false
	}
	return true, strings.EqualFold(GetCfg(name, "Enable", "/etc/config/qpkg.conf", "FALSE"), "TRUE")
}
