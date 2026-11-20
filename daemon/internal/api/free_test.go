package api

import (
	"testing"

	"downloadcenter/internal/auth"
)

// The add dialog's free space is that of the volume the data is written
// to: the destination's (the move folder, the default one included).
func TestFreeForDestination(t *testing.T) {
	s := fixServer(t)
	temp, move, other := t.TempDir(), t.TempDir(), t.TempDir()
	st := s.M.Settings()
	st.TempDir, st.MoveDir = temp, move
	s.M.SaveSettings(st)
	old := freeSpace
	defer func() { freeSpace = old }()
	sizes := map[string]int64{temp: 1, move: 2, other: 3}
	freeSpace = func(p string) int64 { return sizes[p] }
	admin := &auth.Principal{User: "admin", Admin: true, AllTasks: true, Via: "session"}
	if got := s.freeFor(admin, nil, nil); got != 2 {
		t.Errorf("default move folder: %d", got)
	}
	if got := s.freeFor(admin, nil, []string{""}); got != 1 {
		t.Errorf("not moved: %d", got)
	}
	st.MoveDir = ""
	s.M.SaveSettings(st)
	if got := s.freeFor(admin, nil, nil); got != 1 {
		t.Errorf("no move folder: %d", got)
	}
}
