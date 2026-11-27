// Package logfiles keeps the log files in data/logs bounded: each one has a
// size limit and at most one older copy (.1), so that logs never grow into
// wasted disk space. Only sizes are looked at (stat) until a file is over
// its limit, and nothing is written while nothing happens, so the disks can
// sleep.
package logfiles

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"
)

// Limits of the files in data/logs. dcd.log, dc-dl.log and dc-bt.log keep
// one older copy each; data/logs holds about 15 MiB at most.
const (
	Max         = 2 << 20   // dcd.log, dc-dl.log, dc-bt.log (each, plus .1)
	UpdateMax   = 1 << 20   // update.log
	WatchdogMax = 256 << 10 // watchdog.log (the service script cuts it too)
)

// Rotate moves path to path.1 when it is larger than max. For a file no
// process writes to (a sidecar's log before it starts).
func Rotate(path string, max int64) {
	if st, err := os.Lstat(path); err == nil && st.Mode().IsRegular() && st.Size() > max {
		os.Rename(path, path+".1")
	}
}

// CopyTruncate keeps a file that a running process appends to (opened with
// O_APPEND) under max: the file is copied to path.1, then cut to nothing.
// The writer goes on at the new end; lines written between the copy and the
// cut are lost, which is accepted.
func CopyTruncate(path string, max int64) {
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() <= max {
		return
	}
	src, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return
	}
	defer src.Close()
	tmp := path + ".1.tmp"
	dst, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return
	}
	_, err = io.Copy(dst, src)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil || os.Rename(tmp, path+".1") != nil {
		os.Remove(tmp)
		return
	}
	src.Truncate(0)
}

// KeepTail cuts a file no process keeps open (update.log, watchdog.log) to
// its last half when it is larger than max, at a line start, and makes it
// private.
func KeepTail(path string, max int64) {
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() {
		return
	}
	if st.Mode().Perm() != 0600 {
		os.Chmod(path, 0600)
	}
	if st.Size() <= max {
		return
	}
	b, err := Tail(path, max/2)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0600) != nil || os.Rename(tmp, path) != nil {
		os.Remove(tmp)
	}
}

// Tail reads at most the last n bytes of a regular file (not following a
// link), starting at a line: a partial first line is dropped.
func Tail(path string, n int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", filepath.Base(path))
	}
	off := int64(0)
	if st.Size() > n {
		off = st.Size() - n
	}
	b, err := io.ReadAll(io.NewSectionReader(f, off, st.Size()-off))
	if err != nil {
		return nil, err
	}
	if off > 0 {
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		} else {
			b = nil
		}
	}
	return b, nil
}

// StdioIs reports whether stdout is the file at path (the service script
// started dcd with >> data/logs/dcd.log).
func StdioIs(path string) bool {
	var out, st syscall.Stat_t
	return syscall.Fstat(1, &out) == nil && syscall.Stat(path, &st) == nil && out.Dev == st.Dev && out.Ino == st.Ino
}

// RotateStdio moves path (which stdout and stderr are) to path.1 and points
// stdout and stderr at a new, empty path. Nothing written is lost.
func RotateStdio(path string) error {
	if err := os.Rename(path, path+".1"); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Dup3(int(f.Fd()), 1, 0); err != nil {
		return err
	}
	return syscall.Dup3(int(f.Fd()), 2, 0)
}

// Maintain is the per-minute pass over data/logs: dcd's own log is rotated
// (only when stdout is that file), the sidecars' logs are copied and cut,
// update.log and watchdog.log keep their last half.
func Maintain(logs string) {
	dcd := filepath.Join(logs, "dcd.log")
	if st, err := os.Stat(dcd); err == nil && st.Size() > Max && StdioIs(dcd) {
		RotateStdio(dcd)
	}
	CopyTruncate(filepath.Join(logs, "dc-dl.log"), Max)
	CopyTruncate(filepath.Join(logs, "dc-bt.log"), Max)
	KeepTail(filepath.Join(logs, "update.log"), UpdateMax)
	KeepTail(filepath.Join(logs, "watchdog.log"), WatchdogMax)
}

// stamp is the time the log package puts in front of each line.
var stamp = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} `)

// Dedup is the writer of dcd's log: the same message again within a minute
// is only counted, and the count is written before the next different
// message (or by Flush once the minute is over), so that one error that
// keeps happening cannot push everything else out of the 2 MiB.
type Dedup struct {
	mu     sync.Mutex
	w      io.Writer
	last   string
	lastAt time.Time
	n      int
	now    func() time.Time
}

// NewDedup writes through to w.
func NewDedup(w io.Writer) *Dedup { return &Dedup{w: w, now: time.Now} }

func (d *Dedup) Write(p []byte) (int, error) {
	msg := stamp.ReplaceAllString(string(p), "")
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	if msg == d.last && now.Sub(d.lastAt) < time.Minute {
		d.n++
		return len(p), nil
	}
	d.flush(now)
	d.last, d.lastAt = msg, now
	return d.w.Write(p)
}

func (d *Dedup) flush(now time.Time) {
	if d.n == 0 {
		return
	}
	fmt.Fprintf(d.w, "%s (the previous message was repeated %d more times)\n", now.Format("2006/01/02 15:04:05"), d.n)
	d.n = 0
}

// Flush writes a pending count once its minute is over.
func (d *Dedup) Flush() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if now := d.now(); d.n > 0 && now.Sub(d.lastAt) >= time.Minute {
		d.flush(now)
		d.last = ""
	}
}
