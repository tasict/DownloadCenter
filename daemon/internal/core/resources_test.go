package core

import (
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"

	"downloadcenter/internal/store"
)

func TestDetectMemoryProfile(t *testing.T) {
	dir := t.TempDir()
	meminfo := filepath.Join(dir, "meminfo")
	data := filepath.Join(dir, "data")
	os.MkdirAll(data, 0700)
	write := func(kb string) {
		os.WriteFile(meminfo, []byte("MemTotal:       "+kb+" kB\nMemFree:          12345 kB\n"), 0600)
	}
	cases := []struct {
		kb      string
		file    string // "" = no data/memory_profile
		want    string
		mib     int64
		forced  bool
		missing bool
	}{
		{kb: "515072", want: ProfileLow, mib: 503},      // 512 MB model
		{kb: "996147", want: ProfileLow, mib: 972},      // 1 GB model (0.95 GiB)
		{kb: "1572864", want: ProfileLow, mib: 1536},    // the threshold itself
		{kb: "1945600", want: ProfileNormal, mib: 1900}, // 2 GB model
		{kb: "3879731", want: ProfileNormal, mib: 3788}, // 3.7 GiB
		{missing: true, want: ProfileNormal},            // no meminfo
		{kb: "junk", want: ProfileNormal},               // unreadable value
		{kb: "8000000", file: "low\n", want: ProfileLow, mib: 7812, forced: true},
		{kb: "515072", file: "normal", want: ProfileNormal, mib: 503, forced: true},
		{kb: "515072", file: "  NORMAL  \nlow\n", want: ProfileNormal, mib: 503, forced: true},
		{kb: "8000000", file: "Low", want: ProfileLow, mib: 7812, forced: true},
		{kb: "515072", file: "tiny", want: ProfileLow, mib: 503},       // other content: ignored
		{kb: "8000000", file: "", want: ProfileNormal, mib: 7812},      // no file
		{kb: "8000000", file: "\nlow", want: ProfileNormal, mib: 7812}, // only the first line counts
	}
	for i, c := range cases {
		os.Remove(meminfo)
		os.Remove(filepath.Join(data, "memory_profile"))
		if !c.missing {
			write(c.kb)
		}
		if c.file != "" {
			os.WriteFile(filepath.Join(data, "memory_profile"), []byte(c.file), 0600)
		}
		p, mib, forced := DetectMemoryProfile(meminfo, data)
		if p != c.want || mib != c.mib || forced != c.forced {
			t.Errorf("case %d (%q, %q): got %s %d %v, want %s %d %v", i, c.kb, c.file, p, mib, forced, c.want, c.mib, c.forced)
		}
	}
}

func TestApplyMemoryLimit(t *testing.T) {
	old := debug.SetMemoryLimit(-1)
	defer debug.SetMemoryLimit(old)
	debug.SetMemoryLimit(math.MaxInt64)
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "GOMEMLIMIT" {
				return v
			}
			return ""
		}
	}
	if got := ApplyMemoryLimit(ProfileNormal, env("")); got != 0 || debug.SetMemoryLimit(-1) != math.MaxInt64 {
		t.Fatalf("normal profile set a limit: %d", got)
	}
	// GOMEMLIMIT wins: the runtime applied it already, dcd leaves it alone
	debug.SetMemoryLimit(128 << 20)
	if got := ApplyMemoryLimit(ProfileLow, env("128MiB")); got != 0 || debug.SetMemoryLimit(-1) != 128<<20 {
		t.Fatalf("GOMEMLIMIT overridden: %d, limit %d", got, debug.SetMemoryLimit(-1))
	}
	debug.SetMemoryLimit(math.MaxInt64)
	if got := ApplyMemoryLimit(ProfileLow, env("")); got != 64<<20 || debug.SetMemoryLimit(-1) != 64<<20 {
		t.Fatalf("low profile: got %d, limit %d", got, debug.SetMemoryLimit(-1))
	}
}

func TestSetMemoryProfile(t *testing.T) {
	defer SetMemoryProfile(ProfileNormal)
	SetMemoryProfile("low")
	if MemoryProfile() != ProfileLow {
		t.Fatal("low not set")
	}
	SetMemoryProfile("weird")
	if MemoryProfile() != ProfileNormal {
		t.Fatal("unknown profile is not normal")
	}
}

// openAt opens (or creates) the database at path the way dcd does at start.
func openAt(t *testing.T, path string) *Manager {
	t.Helper()
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db, filepath.Dir(path))
}

func TestProfileDefaults(t *testing.T) {
	defer SetMemoryProfile(ProfileNormal)
	SetMemoryProfile(ProfileNormal)
	d := DefaultSettings()
	if d.BT.MaxNum != 5 || d.Torrent.MaxConn != 300 || d.Torrent.MaxSeeding != 0 {
		t.Fatalf("normal defaults %d %d %d", d.BT.MaxNum, d.Torrent.MaxConn, d.Torrent.MaxSeeding)
	}

	// Never saved: the low profile's defaults
	SetMemoryProfile(ProfileLow)
	path := filepath.Join(t.TempDir(), "dc.db")
	m := openAt(t, path)
	s := m.Settings()
	if s.BT.MaxNum != 2 || s.Torrent.MaxConn != 100 || s.Torrent.MaxSeeding != 3 {
		t.Fatalf("low profile, never saved: %d %d %d", s.BT.MaxNum, s.Torrent.MaxConn, s.Torrent.MaxSeeding)
	}

	// Saved values stay what they are, whatever the profile
	dir := t.TempDir()
	SetMemoryProfile(ProfileNormal)
	m = openAt(t, filepath.Join(dir, "dc.db"))
	s = m.Settings()
	if s.BT.MaxNum != 5 || s.Torrent.MaxConn != 300 {
		t.Fatalf("normal: %d %d", s.BT.MaxNum, s.Torrent.MaxConn)
	}
	if err := m.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	m.DB().Close()
	SetMemoryProfile(ProfileLow)
	m = openAt(t, filepath.Join(dir, "dc.db"))
	if s = m.Settings(); s.BT.MaxNum != 5 || s.Torrent.MaxConn != 300 {
		t.Fatalf("saved 5/300 changed with the low profile: %d %d", s.BT.MaxNum, s.Torrent.MaxConn)
	}

	// Out of range
	s.BT.MaxNum, s.Torrent.MaxSeeding = 0, -4
	m.SaveSettings(s)
	if s = m.Settings(); s.BT.MaxNum != 2 || s.Torrent.MaxSeeding != 0 {
		t.Fatalf("low profile, below range: %d %d", s.BT.MaxNum, s.Torrent.MaxSeeding)
	}
	s.Torrent.MaxSeeding = 99
	m.SaveSettings(s)
	if s = m.Settings(); s.Torrent.MaxSeeding != 50 {
		t.Fatalf("max_seeding above range: %d", s.Torrent.MaxSeeding)
	}
}

// torrent.max_seeding defaults to 3 with the low profile on new
// installations only; upgraded ones keep seeding everything.
func TestSeedingDefault(t *testing.T) {
	defer SetMemoryProfile(ProfileNormal)
	SetMemoryProfile(ProfileLow)
	seeding := func(m *Manager) int { return m.Settings().Torrent.MaxSeeding }

	// A new installation, and the same one after a restart
	dir := t.TempDir()
	m := openAt(t, filepath.Join(dir, "dc.db"))
	if seeding(m) != 3 || m.DB().Meta("seeding_default") != "profile" {
		t.Fatalf("new installation: %d %q", seeding(m), m.DB().Meta("seeding_default"))
	}
	m.DB().Close()
	if m = openAt(t, filepath.Join(dir, "dc.db")); seeding(m) != 3 {
		t.Fatalf("new installation after a restart: %d", seeding(m))
	}

	// 1.4.0 saved its settings (no max_seeding among them), then the upgrade
	dir = t.TempDir()
	db, err := store.Open(filepath.Join(dir, "dc.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.PutJSON("config", map[string]any{"bt": map[string]any{"max_num": 5}, "torrent": map[string]any{"max_conn": 300, "seed_ratio": 1.5}})
	db.Close()
	m = openAt(t, filepath.Join(dir, "dc.db"))
	if seeding(m) != 0 || m.DB().Meta("seeding_default") != "unlimited" {
		t.Fatalf("upgrade with saved settings: %d %q", seeding(m), m.DB().Meta("seeding_default"))
	}
	// The administrator sets a limit afterwards
	s := m.Settings()
	s.Torrent.MaxSeeding = 3
	m.SaveSettings(s)
	m.DB().Close()
	if m = openAt(t, filepath.Join(dir, "dc.db")); seeding(m) != 3 {
		t.Fatalf("limit saved after the upgrade: %d", seeding(m))
	}

	// 1.4.0 never saved its settings, then the upgrade
	dir = t.TempDir()
	if db, err = store.Open(filepath.Join(dir, "dc.db")); err != nil {
		t.Fatal(err)
	}
	db.Close()
	m = openAt(t, filepath.Join(dir, "dc.db"))
	if s = m.Settings(); s.Torrent.MaxSeeding != 0 || s.BT.MaxNum != 2 || s.Torrent.MaxConn != 100 {
		t.Fatalf("upgrade without saved settings: %d %d %d", s.Torrent.MaxSeeding, s.BT.MaxNum, s.Torrent.MaxConn)
	}
}
