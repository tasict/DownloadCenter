package core

import (
	"sync"
	"time"
)

// The download speed of the last 15 minutes, for the sparkline of the
// Deskpane widget: every owner's download rate per 10-second slot of the
// wall clock, in memory only. A slot keeps the last rate the loop saw in
// it; slots the loop missed (a stall, a restart) read as 0, so the history
// always covers the same span.
const (
	rateSlot  = 10 // seconds per sample
	rateSlots = 90 // samples kept
)

type rateSample struct {
	slot  int64            // Unix time / rateSlot
	rates map[string]int64 // owner -> bytes per second
}

type rateHistory struct {
	mu    sync.Mutex
	slots [rateSlots]rateSample
}

// record stores the rates seen at now.
func (h *rateHistory) record(now time.Time, rates map[string]int64) {
	slot := now.Unix() / rateSlot
	h.mu.Lock()
	h.slots[slot%rateSlots] = rateSample{slot: slot, rates: rates}
	h.mu.Unlock()
}

// sum returns the rates of the rateSlots slots up to now, oldest first,
// added up over the owners sees accepts.
func (h *rateHistory) sum(now time.Time, sees func(owner string) bool) []int64 {
	cur := now.Unix() / rateSlot
	out := make([]int64, rateSlots)
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range out {
		slot := cur - int64(rateSlots-1-i)
		s := h.slots[slot%rateSlots]
		if s.slot != slot {
			continue
		}
		for owner, r := range s.rates {
			if sees(owner) {
				out[i] += r
			}
		}
	}
	return out
}

// RateHistory is the download rate (bytes per second) of the tasks of the
// owners sees accepts, one value per 10 seconds for the last 15 minutes,
// oldest first.
func (m *Manager) RateHistory(sees func(owner string) bool) []int64 {
	return m.rates.sum(time.Now(), sees)
}

// recordRates samples every owner's download rate. Caller holds m.mu.
func (m *Manager) recordRates(now time.Time) {
	rates := map[string]int64{}
	for _, t := range m.live {
		if t.DownRate > 0 {
			rates[t.Owner] += t.DownRate
		}
	}
	m.rates.record(now, rates)
}
