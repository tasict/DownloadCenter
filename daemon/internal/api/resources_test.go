package api

import (
	"encoding/json"
	"testing"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
)

// GET /settings reports the memory profile dcd runs with; it is not a setting.
func TestSettingsMemoryProfile(t *testing.T) {
	defer core.SetMemoryProfile(core.ProfileNormal)
	s := fixServer(t)
	s.DevUser = "admin"
	for _, p := range []string{core.ProfileLow, core.ProfileNormal} {
		core.SetMemoryProfile(p)
		_, out := call(s, "GET", "/settings", "")
		if out["memory_profile"] != p {
			t.Fatalf("profile %s: memory_profile %v", p, out["memory_profile"])
		}
		if _, in := out["settings"].(map[string]any)["memory_profile"]; in {
			t.Fatalf("memory_profile is among the settings")
		}
	}
}

// seed_waiting is only in the answer of a torrent that waits to seed.
func TestTaskSeedWaiting(t *testing.T) {
	s := fixServer(t)
	p := &auth.Principal{User: "admin", Admin: true, Via: "session", AllTasks: true}
	for _, waiting := range []bool{true, false} {
		task := &core.Task{Hash: "1111111111111111111111111111111111111111", Kind: core.KindBT, State: core.StSeeding, SeedWaiting: waiting}
		b, _ := json.Marshal(s.taskJSON(p, task, false))
		var out map[string]any
		json.Unmarshal(b, &out)
		v, in := out["seed_waiting"]
		if waiting && v != true || !waiting && in || out["state"] != "seeding" {
			t.Fatalf("waiting %v: %s", waiting, b)
		}
	}
}
