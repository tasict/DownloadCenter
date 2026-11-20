package core

import (
	"errors"
	"strings"
	"testing"
)

// An administrator's token limited to its owner's tasks does not reach
// other owners' tasks through an add (no tracker merge, no id), and a task
// added through a token is guarded even for an administrator.
func TestAddCallerRights(t *testing.T) {
	m := testManager(t)
	m.Engines["libtorrent"] = &fakeBT{}
	m.URL = supportsAll{}
	st := m.Settings()
	st.TempDir = t.TempDir()
	m.SaveSettings(st)
	link := "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"
	if _, err := m.AddMagnet(link, AddOptions{Owner: "alice", Admin: true, AllTasks: true}); err != nil {
		t.Fatal(err)
	}
	res, err := m.AddMagnet(link+"&tr=udp://t.example.org:1", AddOptions{Owner: "admin", Admin: true})
	if !errors.Is(err, ErrOtherOwner) || res != nil {
		t.Errorf("own-tasks caller: %v %v", res, err)
	}
	if tr := m.Live("0123456789abcdef0123456789abcdef01234567").Options.Trackers; len(tr) != 0 {
		t.Errorf("trackers merged into someone else's task: %v", tr)
	}
	if _, err := m.AddMagnet(link+"&tr=udp://t.example.org:1", AddOptions{Owner: "admin", Admin: true, AllTasks: true}); err != nil {
		t.Errorf("all-tasks caller: %v", err)
	}

	r, err := m.AddURL("https://example.org/a.bin", AddOptions{Owner: "admin", Admin: true, AllTasks: true, Guard: true})
	if err != nil {
		t.Fatal(err)
	}
	task := m.live[r.ID]
	req, err := m.buildAdd(task)
	if err != nil || !req.Guard {
		t.Errorf("token-added task of an administrator: guard %v (%v)", req.Guard, err)
	}
	r, _ = m.AddURL("https://example.org/b.bin", AddOptions{Owner: "admin", Admin: true, AllTasks: true})
	if req, err := m.buildAdd(m.live[r.ID]); err != nil || req.Guard {
		t.Errorf("session-added task of an administrator: guard %v (%v)", req.Guard, err)
	}
}

// A password in a task's address is never shown.
func TestSourceForDisplay(t *testing.T) {
	if got := SourceForDisplay("ftp://joe:s3cret@ftp.example.org/a.iso"); strings.Contains(got, "s3cret") || !strings.HasPrefix(got, "ftp://joe:") {
		t.Errorf("password: %s", got)
	}
	for _, same := range []string{"ftp://joe@ftp.example.org/a.iso", "https://example.org/a.iso?x=1", "magnet:?xt=urn:btih:abc"} {
		if got := SourceForDisplay(same); got != same {
			t.Errorf("%s shown as %s", same, got)
		}
	}
}

// The duplicate check names another owner's task only to a caller that
// acts on every owner's tasks.
func TestCheckOtherOwners(t *testing.T) {
	m := testManager(t)
	m.Engines["libtorrent"] = &fakeBT{}
	st := m.Settings()
	st.TempDir = t.TempDir()
	m.SaveSettings(st)
	link := "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"
	if _, err := m.AddMagnet(link, AddOptions{Owner: "alice", Admin: true, AllTasks: true}); err != nil {
		t.Fatal(err)
	}
	if r := m.Check("admin", false, []CheckItem{{Source: link}}, ""); r[0].TaskID != "" || r[0].TaskName != "" {
		t.Errorf("own-tasks caller sees %+v", r[0])
	}
	if r := m.Check("admin", true, []CheckItem{{Source: link}}, ""); r[0].TaskID == "" {
		t.Errorf("all-tasks caller: %+v", r[0])
	}
}
