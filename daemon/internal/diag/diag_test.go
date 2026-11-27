package diag

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"downloadcenter/internal/core"
	"downloadcenter/internal/store"
)

const (
	hashA = "0123456789abcdef0123456789abcdef01234567"
	hashB = "fedcba9876543210fedcba9876543210fedcba98"
)

func terms() []core.DiagTerm {
	return []core.DiagTerm{
		{Text: hashA, Kind: "task", Hash: hashA},
		{Text: "My Holiday Video 2026.mkv", Kind: "name", Hash: hashA},
		{Text: "https://files.example.org/x/y?token=abc", Kind: "link", Hash: hashA},
		{Text: "files.example.org", Kind: "host", Hash: hashA},
		{Text: "photos", Kind: "name", Hash: hashA},
		{Text: "alice", Kind: "account", Hash: ""},
		{Text: "Download", Kind: "name", Hash: hashA}, // one of our own words: never replaced
		{Text: "42", Kind: "name", Hash: hashA},       // too short and only digits
	}
}

func code(h string) string { return h[:8] }

func TestRedactor(t *testing.T) {
	r := NewRedactor(terms(), code, time.Date(2026, 11, 1, 0, 0, 0, 0, time.Local))
	cases := []struct{ in, want string }{
		{"2026/11/27 10:00:00 core: seeding targets of " + hashA + ": error",
			"2026/11/27 10:00:00 core: seeding targets of <task 01234567>: error"},
		{"2026/11/27 10:00:01 dc-bt: x: data in /share/CACHEDEV1_DATA/Download/My Holiday Video 2026.mkv not taken over",
			"2026/11/27 10:00:01 dc-bt: x: data in /share/CACHEDEV1_DATA/Download/<name in task 01234567> not taken over"},
		{"2026/11/27 10:00:02 fetch https://files.example.org/x/y?token=abc failed", "2026/11/27 10:00:02 fetch <link of task 01234567> failed"},
		{"2026/11/27 10:00:03 tracker http://other.example.com/announce?passkey=1 timed out", "2026/11/27 10:00:03 tracker http://<host>/… timed out"},
		{"2026/11/27 10:00:04 added magnet:?xt=urn:btih:" + hashB + "&dn=x", "2026/11/27 10:00:04 added <magnet>"},
		{"2026/11/27 10:00:05 peer 192.168.1.5:6881 and 127.0.0.1:18780 (libtorrent 2.0.15.0)", "2026/11/27 10:00:05 peer <IP>:6881 and 127.0.0.1:18780 (libtorrent 2.0.15.0)"},
		{"2026/11/27 10:00:05 libtorrent: 10.1.2.3 refused, version: 10.1.2.4, v10.1.2.5", "2026/11/27 10:00:05 libtorrent: <IP> refused, version: <IP>, v<IP>"},
		{"2026/11/27 10:00:05 dc-bt ready (libtorrent/2.0.15.0, version 1.8.0.1)", "2026/11/27 10:00:05 dc-bt ready (libtorrent/2.0.15.0, version 1.8.0.1)"},
		{"2026/11/27 10:00:06 listening on [fe80::265e:beff:fe90:b9a9%qvs0]:6881", "2026/11/27 10:00:06 listening on [<IP>]:6881"},
		{"2026/11/27 10:00:07 auth: grant alice: failed", "2026/11/27 10:00:07 auth: grant <account 1>: failed"},
		{"2026/11/27 10:00:08 photoshop.log and photos", "2026/11/27 10:00:08 photoshop.log and <name in task 01234567>"},
		{`2026/11/27 10:00:09 api: make folder "Family" in "/share/CACHEDEV1_DATA/Public"`, `2026/11/27 10:00:09 api: make folder "…" in "…"`},
		{"2026/11/27 10:00:10 tls: CA list /share/CACHEDEV2_DATA/.qpkg/DownloadCenter/certs/cacert.pem", "2026/11/27 10:00:10 tls: CA list /share/CACHEDEV2_DATA/.qpkg/DownloadCenter/certs/cacert.pem"},
		{"2026/11/27 10:00:11 home /share/homes/bob/Download/x and /share/CACHEDEV1_DATA/@DownloadCenterTemp/" + hashB + "/a", "2026/11/27 10:00:11 home /share/homes/… and /share/CACHEDEV1_DATA/@DownloadCenterTemp/…"},
		{"2026/11/27 10:00:12 Download Center 42 ready", "2026/11/27 10:00:12 Download Center 42 ready"},
		{"2026/11/27 10:00:13 staged in /share/CACHEDEV1_DATA/@DownloadCenterTemp/" + hashA + ".seed", "2026/11/27 10:00:13 staged in /share/CACHEDEV1_DATA/@DownloadCenterTemp/<task 01234567>.seed"},
	}
	for _, c := range cases {
		got, ok := r.Line(c.in)
		if !ok || got != c.want {
			t.Errorf("\n in: %s\ngot: %s\nwant %s", c.in, got, c.want)
		}
	}
	// Older than the history: left out, and the lines without a time after it
	if _, ok := r.Line("2026/10/01 10:00:00 old " + hashA); ok {
		t.Error("an old line was kept")
	}
	if _, ok := r.Line("goroutine 1 [running]:"); ok {
		t.Error("the line after an old one was kept")
	}
	if got, ok := r.Line("2026/11/27 11:00:00 new"); !ok || got != "2026/11/27 11:00:00 new" {
		t.Errorf("got %q", got)
	}
	if r.Dropped != 2 || r.Dict == 0 || r.Rules == 0 {
		t.Errorf("counts: dropped %d, dict %d, rules %d", r.Dropped, r.Dict, r.Rules)
	}
}

func TestMatcherManyNames(t *testing.T) {
	var ts []core.DiagTerm
	for i := 0; i < 20000; i++ {
		ts = append(ts, core.DiagTerm{Text: "episode-" + strings.Repeat("x", i%7) + "-" + string(rune('a'+i%26)) + "-" + time.Duration(i).String(), Kind: "name", Hash: hashA})
	}
	ts = append(ts, core.DiagTerm{Text: "The Secret Film", Kind: "name", Hash: hashA})
	r := NewRedactor(ts, code, time.Time{})
	start := time.Now()
	var got string
	for i := 0; i < 20000; i++ {
		got = r.Text("2026/11/27 10:00:00 core: moving the secret film into place")
	}
	if got != "2026/11/27 10:00:00 core: moving <name in task 01234567> into place" {
		t.Fatalf("got %q", got)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("too slow: %v", d)
	}
}

func TestExport(t *testing.T) {
	qtsEventLog, kernelLog, dmesgCmd = "/nonexistent/event.log", "/nonexistent/kmsg", "/nonexistent/dmesg"
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	os.MkdirAll(filepath.Join(data, "logs"), 0700)
	os.MkdirAll(filepath.Join(data, "run"), 0700)
	db, err := store.Open(filepath.Join(data, "dc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	if _, err := db.X(`INSERT INTO tasks (hash, owner, kind, source, name, state, created_at) VALUES (?, 'carol', 'http', 'https://media.example.net/private/Wedding%20Day.mp4', 'Wedding Day.mp4', 'downloading', ?)`, hashB, now.Unix()); err != nil {
		t.Fatal(err)
	}
	db.X(`INSERT INTO task_files (hash, idx, path) VALUES (?, 0, 'Album/Wedding Day.mp4')`, hashB)
	m := core.New(db, data)
	stamp := now.Format("2006/01/02 15:04:05")
	os.WriteFile(filepath.Join(data, "logs", "dcd.log"), []byte(
		stamp+" core: task "+hashB+" from https://media.example.net/private/Wedding%20Day.mp4\n"+
			stamp+" core: moving Wedding Day.mp4 for carol into Album\n"+
			now.AddDate(0, 0, -200).Format("2006/01/02 15:04:05")+" old line about Wedding Day.mp4\n"), 0600)
	os.WriteFile(filepath.Join(data, "logs", "dc-bt.log"), []byte(stamp+" dc-bt: "+hashB+": listening on 10.1.2.3:6881\n"), 0600)
	var buf bytes.Buffer
	if err := Export(&buf, Source{M: m, Root: dir, Data: data, Version: "1.5.0", Started: now}); err != nil {
		t.Fatal(err)
	}
	files := unzip(t, buf.Bytes())
	for _, n := range []string{"dcd.log.txt", "dc-bt.log.txt", "system-log.txt", "kernel-oom.txt", "summary.txt"} {
		if _, ok := files[n]; !ok {
			t.Errorf("missing %s", n)
		}
	}
	text := strings.Join(values(files), "")
	for _, secret := range []string{"Wedding", "media.example.net", "carol", hashB, "10.1.2.3", "Album"} {
		if strings.Contains(text, secret) {
			t.Errorf("the export shows %q:\n%s", secret, text)
		}
	}
	c := m.DiagCode(hashB)
	if !strings.Contains(text, "<task "+c+">") || !strings.Contains(text, "Download Center 1.5.0") {
		t.Errorf("missing the task code or the version:\n%s", text)
	}
	if h, _, ok := m.TaskByCode(c); !ok || h != hashB {
		t.Errorf("TaskByCode(%s) = %s %v", c, h, ok)
	}
	if m.DiagCode(hashB) != c {
		t.Error("the code changed")
	}
}

// unzip returns the files of an export by name (without the folder).
func unzip(t *testing.T, b []byte) map[string]string {
	t.Helper()
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range z.File {
		rc, _ := f.Open()
		body, _ := io.ReadAll(rc)
		rc.Close()
		out[strings.TrimPrefix(f.Name, "DownloadCenter-logs/")] = string(body)
	}
	return out
}

func values(m map[string]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
