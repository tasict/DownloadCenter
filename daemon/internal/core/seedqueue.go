package core

import "time"

// seedIdleAfter is how long a seeding torrent runs without uploading before
// it stops taking a seeding slot (it keeps seeding).
const seedIdleAfter = 30 * time.Minute

// seedSlot decides whether a seeding torrent runs: torrent.max_seeding of
// them in queue order (0 = all). One that has not uploaded anything for
// seedIdleAfter keeps seeding without taking a slot, so the next waiting one
// starts; when it uploads again it takes its slot back, and the last one to
// hold a slot waits again. n counts the slots taken so far in this pass.
// Caller holds m.mu.
func (m *Manager) seedSlot(t *Task, n *int) bool {
	idle := m.seedIdleNow(t)
	limit := m.Settings().Torrent.MaxSeeding
	if limit <= 0 || idle {
		return true
	}
	if *n < limit {
		*n++
		return true
	}
	t.SeedWaiting = true
	return false
}

// seedIdleNow reports whether a seeding torrent has run without uploading
// for seedIdleAfter. The time is kept in memory only: after a restart it
// counts again. Caller holds m.mu.
func (m *Manager) seedIdleNow(t *Task) bool {
	if !m.running[t.Hash] || t.UpRate > 0 {
		delete(m.seedIdle, t.Hash)
		return false
	}
	since, ok := m.seedIdle[t.Hash]
	if !ok {
		m.seedIdle[t.Hash] = m.clock()
		return false
	}
	return m.clock().Sub(since) >= seedIdleAfter
}

// pruneSeedIdle forgets the times of tasks that no longer seed. Caller holds
// m.mu.
func (m *Manager) pruneSeedIdle() {
	for h := range m.seedIdle {
		if t := m.live[h]; t == nil || t.State != StSeeding {
			delete(m.seedIdle, h)
		}
	}
}
