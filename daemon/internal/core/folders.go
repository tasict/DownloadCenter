package core

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"

	"downloadcenter/internal/qts"
)

// Folder is one entry of the folder pickers (the web UI's and V4 clients').
type Folder struct {
	Name      string `json:"name"`
	Path      string `json:"path"`           // display path, see DisplayPath
	Writable  bool   `json:"writable"`       // its volume takes writes
	Choosable bool   `json:"choosable"`      // it can hold downloads
	Free      int64  `json:"free,omitempty"` // shared folders only: free space of the volume
}

var (
	ErrNoFolder = errors.New("folder_not_found")
	ErrReadOnly = errors.New("folder_read_only")
	ErrBadName  = errors.New("bad_name")
	ErrExists   = errors.New("folder_exists")
)

// Writable reports whether dir takes writes. dcd runs as admin, so this only
// fails on read-only volumes (ISO shares, write-protected disks) and the like.
func Writable(dir string) bool {
	return syscall.Access(dir, 2) == nil // W_OK, which syscall does not export
}

// Choosable reports whether dir can hold downloads: a writable folder, but
// not the root of the homes share, whose folders are the users' homes.
func Choosable(dir string) bool {
	return usable(dir) == nil
}

func usable(dir string) error {
	if !Writable(dir) {
		return ErrReadOnly
	}
	if hr := qts.HomesRoot(); hr != "" && filepath.Clean(dir) == hr {
		return ErrFolder
	}
	return nil
}

// hiddenFolder is a system or hidden folder the pickers leave out.
func hiddenFolder(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "@") || strings.HasPrefix(name, "#") ||
		strings.HasPrefix(name, "Network Recycle Bin")
}

// SharedFolders is the top level of the pickers: the shared folders, then
// the homes share, each with the free space of its volume.
func (m *Manager) SharedFolders() []Folder {
	out := []Folder{}
	add := func(name, real string) {
		out = append(out, Folder{Name: name, Path: name, Writable: Writable(real), Choosable: Choosable(real), Free: FreeSpace(real)})
	}
	shares := append([]qts.Share(nil), qts.Shares()...) // the list is cached: sort a copy
	sort.SliceStable(shares, func(i, j int) bool { return NaturalLess(shares[i].Name, shares[j].Name) })
	for _, sh := range shares {
		add(sh.Name, sh.Path)
	}
	if hr := qts.HomesRoot(); hr != "" {
		add("homes", hr)
	}
	return out
}

// ListFolders lists the sub-folders of real, whose display path is display.
// Hidden and system folders are left out, and so is a link that leads out of
// the shared folders.
func ListFolders(real, display string) ([]Folder, error) {
	ents, err := os.ReadDir(real)
	if err != nil {
		return nil, ErrNoFolder
	}
	display = strings.Trim(display, "/")
	out := []Folder{}
	for _, e := range ents {
		n := e.Name()
		if hiddenFolder(n) {
			continue
		}
		p := filepath.Join(real, n)
		if e.Type()&os.ModeSymlink != 0 {
			ev, err := filepath.EvalSymlinks(p)
			if err != nil {
				continue
			}
			if _, ok := qts.ShareOf(ev); !ok {
				continue
			}
			if st, err := os.Stat(ev); err != nil || !st.IsDir() {
				continue
			}
			p = ev
		} else if !e.IsDir() {
			continue
		}
		// A sub-folder is never the homes root: writable is all it takes
		w := Writable(p)
		out = append(out, Folder{Name: n, Path: display + "/" + n, Writable: w, Choosable: w})
	}
	sort.Slice(out, func(i, j int) bool { return NaturalLess(out[i].Name, out[j].Name) })
	return out, nil
}

// ValidFolderName checks the name of a new folder: not empty, at most 255
// bytes, no character SMB clients cannot use, not one the pickers hide (".",
// "@", "#") and not ending in a space or a dot, which Windows drops.
func ValidFolderName(n string) bool {
	if n == "" || len(n) > 255 || !utf8.ValidString(n) || hiddenFolder(n) ||
		strings.HasSuffix(n, " ") || strings.HasSuffix(n, ".") || strings.ContainsAny(n, `/\|:?<>*"`) {
		return false
	}
	for _, r := range n {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// MakeFolder creates the folder name in the display path parent and returns
// its display path. A folder made in someone's home belongs to them.
func (m *Manager) MakeFolder(viewer, parent, name string) (string, error) {
	if !ValidFolderName(name) {
		return "", ErrBadName
	}
	parent = strings.Trim(parent, "/")
	real, err := m.ResolvePath(viewer, parent)
	if err != nil {
		return "", ErrNoFolder
	}
	if err := usable(real); err != nil {
		return "", err
	}
	p := filepath.Join(real, name)
	if err := os.Mkdir(p, 0777); err != nil {
		if os.IsExist(err) {
			return "", ErrExists
		}
		return "", err
	}
	if u := homeOwner(p); u != "" {
		chownPath(p, u, false)
	}
	return parent + "/" + name, nil
}

// SameDir reports whether two paths name the same folder once links are
// followed (a home's Download folder may be a link within the home).
func SameDir(a, b string) bool {
	if ev, err := filepath.EvalSymlinks(a); err == nil {
		a = ev
	}
	if ev, err := filepath.EvalSymlinks(b); err == nil {
		b = ev
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// homeOwner is the account whose home folder holds p ("" outside the homes).
func homeOwner(p string) string {
	hr := qts.HomesRoot()
	if hr == "" || !inside(p, hr) {
		return ""
	}
	user, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(filepath.Clean(p), hr), "/"), "/")
	if user == "" {
		return ""
	}
	if _, _, ok := qts.Lookup(user); !ok {
		return ""
	}
	return user
}

// ownerFor is the account completed data at p is handed to: a regular
// user's own tasks are theirs, an administrator's go to whoever owns the home
// folder they landed in, and otherwise stay with admin ("").
func ownerFor(owner string, admin bool, p string) string {
	if !admin {
		return owner
	}
	return homeOwner(p)
}

// NaturalLess orders names the way people expect: case does not matter and
// runs of digits compare by value ("Season 2" before "Season 10"). Names that
// are equal that way fall back to a plain comparison, so the order is total.
func NaturalLess(a, b string) bool {
	if c := naturalCompare(a, b); c != 0 {
		return c < 0
	}
	return a < b
}

func naturalCompare(a, b string) int {
	ra, rb := []rune(strings.ToLower(a)), []rune(strings.ToLower(b))
	i, j := 0, 0
	for i < len(ra) && j < len(rb) {
		if isDigit(ra[i]) && isDigit(rb[j]) {
			si, sj := i, j
			for i < len(ra) && isDigit(ra[i]) {
				i++
			}
			for j < len(rb) && isDigit(rb[j]) {
				j++
			}
			na, nb := strings.TrimLeft(string(ra[si:i]), "0"), strings.TrimLeft(string(rb[sj:j]), "0")
			if len(na) != len(nb) {
				return cmpInt(len(na), len(nb))
			}
			if na != nb {
				return strings.Compare(na, nb)
			}
			continue
		}
		if ra[i] != rb[j] {
			return cmpInt(int(ra[i]), int(rb[j]))
		}
		i++
		j++
	}
	return cmpInt(len(ra)-i, len(rb)-j)
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
