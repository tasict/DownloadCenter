package core

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"downloadcenter/internal/engine"
)

// probeEngine counts the metadata-only tasks it holds.
type probeEngine struct {
	fakeBT
	held map[string]bool
}

func (p *probeEngine) Add(hash string, r engine.AddRequest) (string, error) {
	ref := fmt.Sprintf("ref%d", len(p.held))
	for p.held[ref] {
		ref += "x"
	}
	p.held[ref] = true
	return ref, nil
}

func (p *probeEngine) Remove(ref string) error { delete(p.held, ref); return nil }

// Probes are capped per account, end by themselves when nobody asks again,
// and only their starter (or an administrator) cancels them.
func TestProbesBounded(t *testing.T) {
	m := testManager(t)
	e := &probeEngine{held: map[string]bool{}}
	m.Engines["libtorrent"] = e
	probeMu.Lock()
	probes = map[string]*probe{}
	probeMu.Unlock()
	magnet := func(i int) string { return fmt.Sprintf("magnet:?xt=urn:btih:%040x", i+1) }
	for i := 0; i < 10; i++ {
		_, _, err := m.ProbeMagnet(magnet(i), "bob", false, true)
		if i < maxProbes && err != nil || i >= maxProbes && !errors.Is(err, ErrProbeLimit) {
			t.Fatalf("probe %d: %v", i, err)
		}
	}
	if len(e.held) != maxProbes {
		t.Fatalf("engine holds %d probes", len(e.held))
	}
	// Another account has its own allowance
	if _, _, err := m.ProbeMagnet(magnet(20), "carol", false, true); err != nil {
		t.Fatal(err)
	}
	// Only the starter or an administrator cancels
	hash := fmt.Sprintf("%040x", 1)
	m.CancelProbeOf(hash, "carol", false)
	if len(e.held) != maxProbes+1 {
		t.Error("someone else cancelled bob's probe")
	}
	m.CancelProbeOf(hash, "bob", false)
	m.CancelProbeOf(fmt.Sprintf("%040x", 21), "admin", true)
	if len(e.held) != maxProbes-1 {
		t.Errorf("cancel: engine holds %d", len(e.held))
	}
	// Nobody asks again: the per-minute pass ends them after the timeout
	m.expireProbes(time.Now())
	if len(e.held) != maxProbes-1 {
		t.Error("ended too early")
	}
	m.expireProbes(time.Now().Add(probeTimeout + time.Second))
	probeMu.Lock()
	left := len(probes)
	probeMu.Unlock()
	if len(e.held) != 0 || left != 0 {
		t.Errorf("after the timeout: engine %d, probes %d", len(e.held), left)
	}
}
