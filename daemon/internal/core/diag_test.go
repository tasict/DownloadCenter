package core

import (
	"fmt"
	"regexp"
	"testing"
	"time"
)

func TestTaskLogBounded(t *testing.T) {
	m := testManager(t)
	h := "0123456789abcdef0123456789abcdef01234567"
	// The same message again within 10 minutes adds nothing
	m.Log(h, "retrying")
	m.Log(h, "retrying")
	m.Log(h, "other")
	m.Log(h, "retrying")
	if n := len(m.TaskLog(h, 1000)); n != 3 {
		t.Fatalf("%d lines, want 3", n)
	}
	for i := 0; i < TaskLogMax+50; i++ {
		m.Log(h, fmt.Sprintf("line %d", i))
	}
	m.Log("other-task", "kept")
	m.trimTaskLogs()
	lines := m.TaskLog(h, 1000)
	if len(lines) != TaskLogMax || lines[0].Msg != fmt.Sprintf("line %d", TaskLogMax+49) {
		t.Fatalf("%d lines, newest %q", len(lines), lines[0].Msg)
	}
	if len(m.TaskLog("other-task", 10)) != 1 {
		t.Fatal("another task's log was cut")
	}
}

func TestDiagTermsAndCode(t *testing.T) {
	m := testManager(t)
	h := "fedcba9876543210fedcba9876543210fedcba98"
	if _, err := m.db.X(`INSERT INTO tasks (hash, owner, kind, source, name, state, caller, created_at, removed_at) VALUES (?, 'dave', 'bt', 'magnet:?xt=urn:btih:`+h+`&dn=Some+Show&tr=udp%3A%2F%2Ftracker.example.com%3A1337', 'Some Show', 'done', 'API:my laptop', ?, ?)`,
		h, time.Now().Unix(), time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	m.db.X(`INSERT INTO task_files (hash, idx, path) VALUES (?, 0, 'Season 1/Episode 01.mkv')`, h)
	want := map[string]string{h: "task", "Some Show": "name", "dave": "account", "my laptop": "account",
		"tracker.example.com": "host", "Season 1": "name", "Episode 01.mkv": "name"}
	got := map[string]string{}
	for _, d := range m.DiagTerms() {
		got[d.Text] = d.Kind
	}
	for text, kind := range want {
		if got[text] != kind {
			t.Errorf("%q: kind %q, want %q", text, got[text], kind)
		}
	}
	c := m.DiagCode(h)
	if !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(c) || m.DiagCode(h) != c || m.DiagCode(h[:39]+"0") == c {
		t.Fatalf("code %q", c)
	}
	// Removed tasks still in the history are found too
	if got, removed, ok := m.TaskByCode(c); !ok || got != h || removed == 0 {
		t.Fatalf("TaskByCode: %q %d %v", got, removed, ok)
	}
	if _, _, ok := m.TaskByCode("zzzzzzzz"); ok {
		t.Fatal("found a task for a code that is not one")
	}
}
