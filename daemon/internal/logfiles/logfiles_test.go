package logfiles

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, path string, n int) {
	t.Helper()
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		b.WriteString("2026/11/27 10:00:00 line\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0600); err != nil {
		t.Fatal(err)
	}
}

func size(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return st.Size()
}

func TestRotate(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "dc-bt.log")
	write(t, p, 100)
	Rotate(p, 1000)
	if size(p) <= 0 || size(p+".1") != -1 {
		t.Fatal("a small file was rotated")
	}
	write(t, p, 2000)
	Rotate(p, 1000)
	if size(p) != -1 || size(p+".1") < 2000 {
		t.Fatalf("not rotated: %d %d", size(p), size(p+".1"))
	}
}

func TestCopyTruncate(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "dc-dl.log")
	write(t, p, 3000)
	w, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	CopyTruncate(p, 1000)
	if size(p) != 0 || size(p+".1") < 3000 {
		t.Fatalf("sizes %d %d", size(p), size(p+".1"))
	}
	// The writer (O_APPEND) goes on at the start of the cut file
	w.WriteString("after\n")
	if b, _ := os.ReadFile(p); string(b) != "after\n" {
		t.Fatalf("got %q", b)
	}
}

func TestKeepTail(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "update.log")
	write(t, p, 5000)
	os.Chmod(p, 0644)
	KeepTail(p, 2000)
	st, _ := os.Stat(p)
	if st.Size() > 1000 || st.Size() == 0 || st.Mode().Perm() != 0600 {
		t.Fatalf("size %d mode %v", st.Size(), st.Mode().Perm())
	}
	if b, _ := os.ReadFile(p); !strings.HasPrefix(string(b), "2026/") {
		t.Fatalf("does not start at a line: %q", b[:20])
	}
}

func TestTailSkipsLinks(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "a.log")
	os.WriteFile(p, []byte("x\n"), 0600)
	os.Symlink(p, filepath.Join(d, "b.log"))
	if _, err := Tail(filepath.Join(d, "b.log"), 100); err == nil {
		t.Fatal("followed a link")
	}
}

func TestDedup(t *testing.T) {
	var out bytes.Buffer
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.Local)
	d := NewDedup(&out)
	d.now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		d.Write([]byte(now.Format("2006/01/02 15:04:05") + " core: engine down\n"))
		now = now.Add(time.Second)
	}
	d.Write([]byte(now.Format("2006/01/02 15:04:05") + " core: engine restarted\n"))
	got := out.String()
	if strings.Count(got, "engine down") != 1 || !strings.Contains(got, "repeated 4 more times") || !strings.Contains(got, "engine restarted") {
		t.Fatalf("got:\n%s", got)
	}
	// A count with nothing after it is written once its minute is over
	out.Reset()
	d.Write([]byte("2026/11/27 10:00:10 core: engine restarted\n"))
	d.Flush()
	if out.Len() != 0 {
		t.Fatalf("flushed early: %q", out.String())
	}
	now = now.Add(2 * time.Minute)
	d.Flush()
	if !strings.Contains(out.String(), "repeated 1 more times") {
		t.Fatalf("got %q", out.String())
	}
}
