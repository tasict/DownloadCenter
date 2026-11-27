package core

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The switch is stored for URL tasks and reaches the engine; tasks saved
// before it existed check certificates.
func TestAddSkipCertCheck(t *testing.T) {
	m := testManager(t)
	m.URL = supportsAll{}
	s := m.Settings()
	s.TempDir = t.TempDir()
	m.SaveSettings(s)
	for _, skip := range []bool{true, false} {
		res, err := m.AddURL("https://dl.example.com/"+map[bool]string{true: "skip", false: "check"}[skip]+".bin",
			AddOptions{Owner: "admin", Admin: true, Paused: true, SkipCertCheck: skip})
		if err != nil {
			t.Fatal(err)
		}
		task := m.Live(res.ID)
		if task.Options.SkipCertCheck != skip {
			t.Fatalf("stored %v, want %v", task.Options.SkipCertCheck, skip)
		}
		m.mu.Lock()
		req, err := m.buildAdd(task)
		m.mu.Unlock()
		if err != nil || req.SkipCertCheck != skip {
			t.Fatalf("engine request: %v %v", req.SkipCertCheck, err)
		}
	}
	var o TaskOptions
	json.Unmarshal([]byte(`{"proxy":"none","headers":["A: b"]}`), &o)
	if o.SkipCertCheck {
		t.Fatal("an old task skips the check")
	}
	if b, _ := json.Marshal(TaskOptions{}); strings.Contains(string(b), "skip_cert_check") {
		t.Fatalf("stored for a checking task: %s", b)
	}
}

// Changing the switch queues a failed task again and logs it; the same
// value changes nothing; torrents have no such switch.
func TestSetSkipCertCheck(t *testing.T) {
	m := testManager(t)
	m.URL = supportsAll{}
	s := m.Settings()
	s.TempDir = t.TempDir()
	m.SaveSettings(s)
	res, err := m.AddURL("https://dl.example.com/f.bin", AddOptions{Owner: "admin", Admin: true, Paused: true})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	task := m.live[res.ID]
	task.State, task.ErrorCode, task.ErrorMsg = StError, "dl_1", "The certificate of dl.example.com cannot be verified (x)"
	task.Options.AutoRetries = 3
	m.mu.Unlock()
	if err := m.SetSkipCertCheck(res.ID, true); err != nil {
		t.Fatal(err)
	}
	got := m.Live(res.ID)
	if !got.Options.SkipCertCheck || got.State != StQueued || got.ErrorCode != "" || got.Options.AutoRetries != 0 {
		t.Fatalf("after: skip=%v state=%s code=%q retries=%d", got.Options.SkipCertCheck, got.State, got.ErrorCode, got.Options.AutoRetries)
	}
	logs := m.TaskLog(res.ID, 100) // newest first
	if len(logs) == 0 || logs[0].Msg != "Certificate check turned off for this task" {
		t.Fatalf("log: %+v", logs)
	}
	n := len(logs)
	if err := m.SetSkipCertCheck(res.ID, true); err != nil || len(m.TaskLog(res.ID, 100)) != n {
		t.Fatalf("same value: %v, %d log lines", err, len(m.TaskLog(res.ID, 100)))
	}
	m.SetSkipCertCheck(res.ID, false)
	if logs := m.TaskLog(res.ID, 1); logs[0].Msg != "Certificate check turned on for this task" || m.Live(res.ID).Options.SkipCertCheck {
		t.Fatal("not turned on again")
	}
	m.mu.Lock()
	m.live["bt1"] = &Task{Hash: "bt1", Kind: KindBT, Owner: "admin", State: StQueued}
	m.mu.Unlock()
	if err := m.SetSkipCertCheck("bt1", true); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("torrent: %v", err)
	}
	if err := m.SetSkipCertCheck("nope", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

// A certificate message names the server; a long host name does not cut
// it (a cut message would no longer be translated).
func TestEngineErrorKeepsCertificateMessage(t *testing.T) {
	host := strings.Repeat("a", 60) + ".download-cdn.example.com"
	msg := "The certificate of " + host + " cannot be verified: the server does not send its complete certificate chain, or its certificate authority is not trusted"
	code, text := engineError("1", msg)
	if code != "dl_1" || text != "Download failed (unknown error) ("+msg+")" {
		t.Fatalf("%s %q", code, text)
	}
}
