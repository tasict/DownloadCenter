package dl

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"downloadcenter/internal/safefs"
)

// The largest file the engine takes a server's word for (also the largest
// file ext4 can hold): anything above is a broken or hostile answer.
const maxFileSize = 1 << 44

// errWorkDir: the task's folder is missing, not a folder, or reached
// through a link someone other than root could have made.
var errWorkDir = errors.New("the download folder cannot be used")

// openWorkDir opens the folder a task downloads into. The core makes it;
// here it is only opened, without following a link anyone but root could
// have put on the way (see realDir), and every file of the task is then
// opened relative to it without following links either.
func openWorkDir(p string) (*safefs.Dir, error) {
	real, err := realDir(p)
	if err != nil {
		return nil, err
	}
	d, err := safefs.OpenDir(real)
	if err != nil {
		return nil, err
	}
	return d, nil
}

// realDir resolves the links on the way to the folder p that only root can
// have made: a link owned by root in a folder of root's that nobody else can
// write to, or a sticky one (such as /share/homes on QTS). Any other link is
// refused.
func realDir(p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", safefs.ErrName
	}
	todo := strings.Split(strings.TrimPrefix(filepath.Clean(p), "/"), "/")
	cur := "/"
	links := 0
	for len(todo) > 0 {
		c := todo[0]
		todo = todo[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur) // cur holds no link: its parent is real
			continue
		}
		next := filepath.Join(cur, c)
		var st syscall.Stat_t
		if err := syscall.Lstat(next, &st); err != nil {
			return "", err
		}
		if st.Mode&syscall.S_IFMT != syscall.S_IFLNK {
			cur = next
			continue
		}
		var ds syscall.Stat_t
		if err := syscall.Lstat(cur, &ds); err != nil {
			return "", err
		}
		shared := ds.Mode&0022 != 0 && ds.Mode&syscall.S_ISVTX == 0
		if st.Uid != 0 || ds.Uid != 0 || shared || links >= 16 {
			return "", safefs.ErrLink
		}
		links++
		target, err := os.Readlink(next)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(target) {
			cur = "/"
		}
		todo = append(strings.Split(target, "/"), todo...)
	}
	return cur, nil
}
