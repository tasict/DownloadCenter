package core

import (
	"testing"
	"time"
)

func TestRateHistory(t *testing.T) {
	var h rateHistory
	all := func(string) bool { return true }
	base := time.Unix(1_700_000_000, 0) // a slot boundary
	// Two owners, one sample per slot for 100 slots (wraps the 90 kept)
	for i := 0; i < 100; i++ {
		h.record(base.Add(time.Duration(i*rateSlot)*time.Second), map[string]int64{"alice": int64(i), "bob": 1000})
	}
	now := base.Add(99 * rateSlot * time.Second)
	got := h.sum(now, all)
	if len(got) != rateSlots {
		t.Fatalf("len %d", len(got))
	}
	if got[0] != 10+1000 || got[rateSlots-1] != 99+1000 {
		t.Fatalf("oldest %d, newest %d", got[0], got[rateSlots-1])
	}
	alice := h.sum(now, func(o string) bool { return o == "alice" })
	if alice[rateSlots-1] != 99 {
		t.Fatalf("alice only: %d", alice[rateSlots-1])
	}
	// The last value in a slot wins
	h.record(now.Add(3*time.Second), map[string]int64{"alice": 5})
	if got := h.sum(now, all); got[rateSlots-1] != 5 {
		t.Fatalf("last in slot: %d", got[rateSlots-1])
	}
	// Slots the loop missed read as 0, and the span stays the same
	later := now.Add(30 * rateSlot * time.Second)
	got = h.sum(later, all)
	if len(got) != rateSlots || got[rateSlots-1] != 0 || got[rateSlots-31] != 5 {
		t.Fatalf("gap: newest %d, before the gap %d", got[rateSlots-1], got[rateSlots-31])
	}
	// Nothing recorded at all
	var empty rateHistory
	for _, v := range empty.sum(now, all) {
		if v != 0 {
			t.Fatal("empty history has values")
		}
	}
}
