package diag

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"downloadcenter/internal/core"
	"downloadcenter/internal/logfiles"

	_ "modernc.org/sqlite"
)

// Export limits: the last 2 MiB of each log, 16 MiB read in all.
const (
	fileTail = 2 << 20
	totalMax = 16 << 20
)

// The files of data/logs an export takes, in this order; nothing else is
// ever read, and no part of the request names a file.
var logNames = []string{"dcd.log", "dcd.log.1", "dcd.log.crash", "watchdog.log", "dc-dl.log", "dc-dl.log.1", "dc-bt.log", "dc-bt.log.1", "update.log"}

// Where QTS keeps its system log (SQLite) and a copy of the kernel log.
var (
	qtsEventLog = "/etc/logs/event.log"
	kernelLog   = "/etc/logs/kmsg"
	dmesgCmd    = "dmesg"
)

// Source is what an export reads.
type Source struct {
	M       *core.Manager
	Root    string // the package folder
	Data    string // its data folder
	Version string
	Started time.Time
}

// Export writes the zip file of an export to w.
func Export(w io.Writer, src Source) error {
	now := time.Now()
	days := src.M.Settings().HistoryDays
	r := NewRedactor(src.M.DiagTerms(), src.M.DiagCode, now.AddDate(0, 0, -days))
	z := zip.NewWriter(w)
	add := func(name string, body []byte) error {
		f, err := z.CreateHeader(&zip.FileHeader{Name: "DownloadCenter-logs/" + name, Method: zip.Deflate, Modified: now})
		if err != nil {
			return err
		}
		_, err = f.Write(body)
		return err
	}
	var sizes []string
	read := 0
	for _, n := range logNames {
		p := filepath.Join(src.Data, "logs", n)
		st, err := os.Lstat(p)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		sizes = append(sizes, fmt.Sprintf("  %s: %d bytes", n, st.Size()))
		if read >= totalMax {
			continue
		}
		b, err := logfiles.Tail(p, fileTail)
		if err != nil {
			continue
		}
		read += len(b)
		r.keep = false // every file starts unknown until its first dated line
		if err := add(n+".txt", redactLines(r, b)); err != nil {
			return err
		}
	}
	r.keep = false
	if err := add("system-log.txt", redactLines(r, qtsEvents())); err != nil {
		return err
	}
	r.keep = false
	if err := add("kernel-oom.txt", redactLines(r, kernelOOM())); err != nil {
		return err
	}
	sum := summary(src, now, days, sizes)
	sum += fmt.Sprintf("\nExport: %d lines older than %d days left out; %d names from the database and %d other links, hashes, addresses or paths replaced.\n", r.Dropped, days, r.Dict, r.Rules)
	if err := add("summary.txt", []byte(r.Text(sum))); err != nil {
		return err
	}
	return z.Close()
}

func redactLines(r *Redactor, b []byte) []byte {
	var out bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if s, ok := r.Line(sc.Text()); ok {
			out.WriteString(s)
			out.WriteByte('\n')
		}
	}
	return out.Bytes()
}

// qtsEvents is the service script's entries in the QTS system log
// ([DownloadCenter] …; the notification channel's [Download Center] …
// entries name tasks and are not taken), newest 200, oldest first.
func qtsEvents() []byte {
	if _, err := os.Stat(qtsEventLog); err != nil {
		return []byte("(no QTS system log at " + qtsEventLog + ")\n")
	}
	db, err := sql.Open("sqlite", "file:"+qtsEventLog+"?mode=ro")
	if err != nil {
		return []byte("(the QTS system log cannot be read)\n")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `SELECT event_timet, event_type, event_desc FROM NASLOG_EVENT WHERE event_desc LIKE '[DownloadCenter]%' ORDER BY event_id DESC LIMIT 200`)
	if err != nil {
		return []byte("(the QTS system log cannot be read)\n")
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var at, typ int64
		var desc string
		if rows.Scan(&at, &typ, &desc) != nil {
			continue
		}
		level := map[int64]string{0: "info", 1: "warning", 2: "error"}[typ]
		lines = append(lines, time.Unix(at, 0).Format("2006-01-02 15:04:05")+" "+level+" "+desc)
	}
	sort.Strings(lines)
	if len(lines) == 0 {
		return []byte("(no [DownloadCenter] entries)\n")
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

var oomLine = regexp.MustCompile(`(?i)(out of memory|oom-kill|oom_reaper|killed process|invoked oom)`)
var ourProc = regexp.MustCompile(`\b(dcd|dc-dl|dc-bt)\b`)

// kernelOOM is the kernel's out-of-memory lines about dcd, dc-dl or
// dc-bt, from QTS's copy of the kernel log and from dmesg (only these
// lines, never the whole log).
func kernelOOM() []byte {
	var out []string
	pick := func(b []byte, prefix string) {
		for _, l := range strings.Split(string(b), "\n") {
			if oomLine.MatchString(l) && ourProc.MatchString(l) {
				out = append(out, prefix+strings.TrimSpace(l))
			}
		}
	}
	if b, err := logfiles.Tail(kernelLog, 4<<20); err == nil {
		pick(b, "")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if b, err := exec.CommandContext(ctx, dmesgCmd).Output(); err == nil {
		// dmesg has no wall-clock time: these lines are shown as of now
		pick(b, time.Now().Format("2006-01-02 15:04:05")+" dmesg: ")
	}
	if len(out) > 100 {
		out = out[len(out)-100:]
	}
	if len(out) == 0 {
		return []byte(time.Now().Format("2006-01-02 15:04:05") + " (no out-of-memory lines about Download Center)\n")
	}
	return []byte(strings.Join(out, "\n") + "\n")
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func cfg(file, section, key string) string {
	b, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	in := section == ""
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "[") {
			// No section asked: the key wherever it is
			in = section == "" || strings.EqualFold(strings.Trim(l, "[]"), section)
			continue
		}
		if k, v, ok := strings.Cut(l, "="); in && ok && strings.EqualFold(strings.TrimSpace(k), key) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func meminfo() (total, avail int64) {
	b, _ := os.ReadFile("/proc/meminfo")
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		n, _ := strconv.ParseInt(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			total = n >> 10
		case "MemAvailable:":
			avail = n >> 10
		}
	}
	return
}

func version(bin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		if _, serr := os.Stat(bin); serr != nil {
			return "not installed"
		}
		return "no answer"
	}
	return firstLine(b)
}

type healther interface{ Health() error }

func health(e any) string {
	if e == nil {
		return "not installed"
	}
	if h, ok := e.(healther); ok {
		if err := h.Health(); err != nil {
			return "not running"
		}
	}
	return "running"
}

func readTrim(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return "(none)"
	}
	return strings.TrimSpace(string(b))
}

// summary describes the NAS and the package in numbers and versions only.
func summary(src Source, now time.Time, days int, sizes []string) string {
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	p("Download Center %s, diagnostic export of %s", src.Version, now.Format("2006-01-02 15:04:05 -0700"))
	p("Architecture: %s (package %s, kernel %s)", runtime.GOARCH, readTrim(filepath.Join(src.Root, "bin", "arch")), readTrim("/proc/sys/kernel/osrelease"))
	system := "QTS"
	if mounts, _ := os.ReadFile("/proc/mounts"); bytes.Contains(mounts, []byte(" zfs ")) {
		system = "QuTS hero (ZFS volumes)"
	}
	p("System: %s %s build %s", system, cfg("/etc/config/uLinux.conf", "System", "Version"), cfg("/etc/config/uLinux.conf", "System", "Build Number"))
	p("Model: %s (%s)", cfg("/etc/platform.conf", "", "DISPLAY_NAME"), cfg("/etc/config/uLinux.conf", "System", "Model"))
	total, avail := meminfo()
	p("Memory: %d MiB, %d MiB available; memory profile %s", total, avail, core.MemoryProfile())
	p("dcd: pid %d, running since %s; pid file %s, ready %s", os.Getpid(), src.Started.Format("2006-01-02 15:04:05"),
		readTrim(filepath.Join(src.Data, "run", "dcd.pid")), readTrim(filepath.Join(src.Data, "run", "ready")))
	p("Watchdog: %s", readTrim(filepath.Join(src.Data, "run", "watchdog")))
	if st, err := os.Stat(filepath.Join(src.Data, "logs", "dcd.log.crash")); err == nil {
		p("Last time dcd did not stop cleanly: before %s", st.ModTime().Format("2006-01-02 15:04:05"))
	} else {
		p("Last time dcd did not stop cleanly: none recorded")
	}
	p("dc-dl: %s, %s", version(filepath.Join(src.Root, "bin", "dc-dl")), health(src.M.URL))
	var bt any
	if e := src.M.Engines["libtorrent"]; e != nil {
		bt = e
	}
	p("dc-bt: %s, %s", version(filepath.Join(src.Root, "bin", "dc-bt")), health(bt))
	counts := src.M.DiagCounts()
	var states []string
	for s := range counts {
		states = append(states, s)
	}
	sort.Strings(states)
	var parts []string
	for _, s := range states {
		parts = append(parts, fmt.Sprintf("%s %d", s, counts[s]))
	}
	if len(parts) == 0 {
		parts = []string{"none"}
	}
	p("Tasks: %s", strings.Join(parts, ", "))
	p("Task history: %d days (older log lines are left out of this export)", days)
	p("Files in data/logs:")
	for _, s := range sizes {
		p("%s", s)
	}
	return b.String()
}
