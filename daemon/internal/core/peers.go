package core

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"downloadcenter/internal/engine"
	"downloadcenter/internal/torrent"
)

// PublicTrackers is the public tracker list (internal/trackers).
type PublicTrackers interface {
	Current() []string // the list to use; a stale one is fetched again in the background
	Cached() []string  // the list as it is, never fetched
	Tick(auto bool)    // housekeeping: fetched once a day while new torrents get it
}

// ErrPrivateTrackers refuses trackers for a private torrent.
var ErrPrivateTrackers = errors.New("Private torrents only use their own trackers")

// MaxAddedTrackers is how many trackers a task may get besides its own and
// the public list (by hand or merged from other links): every one of them is
// announced to.
const MaxAddedTrackers = 200

// ErrTooManyTrackers refuses trackers beyond MaxAddedTrackers.
var ErrTooManyTrackers = errors.New("A task can have up to 200 added trackers")

// guardFor says whether a torrent's connections are guarded (see
// engine.AddRequest.Guard): a regular user's torrent, and any torrent with
// the public tracker list, whose names come from the internet.
func (m *Manager) guardFor(t *Task) bool {
	return !m.isAdminOwner(t.Owner) || len(t.Options.PublicTrackers) > 0
}

// syncGuards runs every minute: a torrent whose owner became or stopped
// being an administrator, or that lost the public tracker list, gets the
// guard that fits now. Not with m.mu held.
func (m *Manager) syncGuards() {
	type item struct {
		hash, ref, owner string
		public, had      bool
		pf               engine.PeerFinder
	}
	var list []item
	m.mu.Lock()
	for h, t := range m.live {
		had, ok := m.guards[h]
		if !ok || t.Kind != KindBT || t.EngineRef == "" {
			continue
		}
		e := m.engineOf(t)
		if pf, ok := e.(engine.PeerFinder); ok && e.Caps().PeerRefresh {
			list = append(list, item{h, t.EngineRef, t.Owner, len(t.Options.PublicTrackers) > 0, had, pf})
		}
	}
	for h := range m.guards {
		if m.live[h] == nil {
			delete(m.guards, h)
		}
	}
	m.mu.Unlock()
	for _, it := range list {
		want := !m.isAdminOwner(it.owner) || it.public
		if want == it.had || it.pf.SetGuard(it.ref, want) != nil {
			continue
		}
		m.mu.Lock()
		m.guards[it.hash] = want
		m.mu.Unlock()
	}
}

// A running torrent that has no connection and makes no progress looks for
// peers again: libtorrent never tries a peer again after three failed
// connections while the torrent runs, and DHT reporting the same peer does
// not change that. The first time after stallFirst, then after the waits
// below, the last one repeating.
var (
	stallWaits    = []time.Duration{15 * time.Minute, 30 * time.Minute, 60 * time.Minute, 120 * time.Minute}
	stallFirst    = stallWaits[0]
	stallLogEvery = 6 * time.Hour
	// sourceConnect is how long a connection must last to count as a
	// source: the NAS itself (until the handshake shows it) and the fake
	// peers some trackers and DHT nodes hand out are connected for moments
	sourceConnect = time.Minute
)

// stall is what the manager knows about a torrent without peers.
type stall struct {
	since   time.Time // running with no connection and no progress since (zero: not now)
	last    time.Time // the last time it looked for peers again
	n       int       // how often it did since `since`
	done    int64     // data the torrent had when last seen
	meta    bool      // still fetching the metadata when last seen
	looking bool      // running and looking for data when last seen
	conn    time.Time // connected since (zero: no connection when last seen)
	lastLog time.Time // kept across resets: one log line per stallLogEvery
}

// watchPeers runs for every torrent on every poll; it also keeps the
// torrent's source health. Caller holds m.mu.
func (m *Manager) watchPeers(t *Task, st *engine.Status, e engine.Engine, elapsed int64) {
	now := m.clock()
	s := m.stall[t.Hash]
	if s == nil {
		s = &stall{done: st.Completed, meta: st.IsMetadata}
		m.stall[t.Hash] = s
	}
	progress := !st.IsMetadata && st.Completed > s.done
	arrived := s.meta && !st.IsMetadata
	s.done, s.meta = st.Completed, st.IsMetadata
	looking := st.State == engine.Active && !st.Seeding && !st.Verifying && !st.Moving && t.State != StMoving
	switch {
	case st.Connections == 0:
		s.conn = time.Time{}
	case s.conn.IsZero():
		s.conn = now
	}
	connected := st.Connections > 0 && now.Sub(s.conn) >= sourceConnect
	// Data counted by a check that just ended is no progress
	m.watchHealth(t, st, looking, connected, looking && (s.looking && (progress || arrived) || st.DownRate > 0), elapsed)
	s.looking = looking
	if !looking || connected || progress || arrived {
		s.since, s.last, s.n = time.Time{}, time.Time{}, 0
		t.NoPeersSince, t.PeerSearches = 0, 0
		return
	}
	if s.since.IsZero() {
		s.since = now
	}
	t.NoPeersSince, t.PeerSearches = s.since.Unix(), s.n
	from, wait := s.since, stallWaits[min(s.n, len(stallWaits)-1)]
	if !s.last.IsZero() {
		from = s.last
	}
	if now.Sub(from) < wait {
		return
	}
	s.last = now
	pf, ok := e.(engine.PeerFinder)
	if !ok || !e.Caps().PeerRefresh {
		return
	}
	done, err := pf.RefreshPeers(t.EngineRef)
	if err != nil || !done {
		// A connection came up meanwhile (the next poll resets), or the
		// engine failed: try again after the same wait
		return
	}
	s.n++
	t.PeerSearches = s.n
	if now.Sub(s.lastLog) >= stallLogEvery {
		s.lastLog = now
		m.Log(t.Hash, fmt.Sprintf("No peers for %d minutes; looking for peers again", int(now.Sub(s.since).Minutes())))
	}
}

// yielded reports a torrent that has been without peers long enough to
// leave its download slot to the next one; it keeps running. Caller holds
// m.mu.
func (m *Manager) yielded(t *Task) bool {
	if t.Kind != KindBT {
		return false
	}
	s := m.stall[t.Hash]
	return s != nil && !s.since.IsZero() && m.clock().Sub(s.since) >= stallFirst
}

// metadataArrived runs on every poll of a torrent that has its metadata: a
// magnet is no longer waiting for it, and when it turned out to be a
// private torrent the trackers added to it go (merged, by hand, the public
// list), so it only announces to its own. Both happen once. Caller holds
// m.mu.
func (m *Manager) metadataArrived(t *Task, st *engine.Status, e engine.Engine) {
	if t.Options.MetaSince != 0 {
		t.Options.MetaSince = 0
		m.markDirty(t)
	}
	if !st.Private || len(t.Options.Trackers)+len(t.Options.PublicTrackers) == 0 {
		return
	}
	own := m.ownTrackers(t)
	// The magnet's own trackers may be listed as extra ones too (added
	// after the dialog fetched the file list): nothing to remove then
	extra := missing(union(t.Options.Trackers, t.Options.PublicTrackers), own)
	t.Options.Trackers, t.Options.PublicTrackers = nil, nil
	m.markDirty(t)
	if len(extra) == 0 {
		return
	}
	if pf, ok := e.(engine.PeerFinder); ok && e.Caps().PeerRefresh {
		pf.SetTrackers(t.EngineRef, own)
	}
	m.Log(t.Hash, "This is a private torrent; the trackers added to it were removed")
}

// ownTrackers are a torrent's own trackers: a magnet link's tr= or the
// announce list of a torrent file (a magnet's saved .torrent lists every
// tracker the engine had, so it does not count).
func (m *Manager) ownTrackers(t *Task) []string {
	link := t.Options.Magnet
	if link == "" && t.IsMagnet() {
		link = t.Source
	}
	if link != "" {
		if mg, err := torrent.ParseMagnet(link); err == nil {
			return mg.Trackers
		}
		return nil
	}
	b, err := os.ReadFile(m.torrentPath(t.Hash))
	if err != nil {
		return nil
	}
	if meta, err := torrent.Parse(b); err == nil {
		return meta.Trackers
	}
	return nil
}

// TrackerInfo is what GET /tasks/{id} reports about a torrent's trackers.
type TrackerInfo struct {
	Count         int // its own, the extra ones and the public list
	PublicMissing int // trackers of the public list it does not have
}

// TrackerInfo reports a torrent's trackers (no fetching).
func (m *Manager) TrackerInfo(t *Task) TrackerInfo {
	have := union(m.ownTrackers(t), t.Options.Trackers, t.Options.PublicTrackers)
	in := TrackerInfo{Count: len(have)}
	if m.Public != nil {
		in.PublicMissing = len(missing(m.Public.Cached(), have))
	}
	return in
}

// TrackersAdded is the outcome of AddTrackers.
type TrackersAdded struct {
	Added       int  `json:"added"`        // trackers given by hand that were new
	PublicAdded bool `json:"public_added"` // trackers of the public list were added
}

// AddTrackers adds trackers to a torrent, and the public tracker list with
// public; the engine announces to them right away. Trackers it already has
// are left out. Private torrents only use their own trackers.
func (m *Manager) AddTrackers(hash string, manual []string, public bool) (TrackersAdded, error) {
	var out TrackersAdded
	snap := m.Live(hash)
	if snap == nil {
		return out, ErrNotFound
	}
	if snap.Kind != KindBT {
		return out, ErrUnsupported
	}
	if m.TorrentPrivate(hash) {
		return out, ErrPrivateTrackers
	}
	own := m.ownTrackers(snap)
	var list []string
	if public && m.Public != nil {
		list = m.Public.Current()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.live[hash]
	if t == nil {
		return out, ErrNotFound
	}
	have := union(own, t.Options.Trackers, t.Options.PublicTrackers)
	var addManual []string
	for _, u := range manual {
		if u = strings.TrimSpace(u); u != "" && !contains(have, u) {
			have = append(have, u)
			addManual = append(addManual, u)
		}
	}
	addPublic := missing(list, have)
	if len(addManual)+len(addPublic) == 0 {
		return out, nil
	}
	if len(t.Options.Trackers)+len(addManual) > MaxAddedTrackers {
		return out, ErrTooManyTrackers
	}
	t.Options.Trackers = append(t.Options.Trackers, addManual...)
	t.Options.PublicTrackers = append(t.Options.PublicTrackers, addPublic...)
	if e := m.engineOf(t); e != nil && t.EngineRef != "" && t.State != StDone {
		// The public list's names come from the internet: guarded before
		// the first announce to them
		if pf, ok := e.(engine.PeerFinder); ok && len(addPublic) > 0 && !m.guards[t.Hash] && e.Caps().PeerRefresh {
			if pf.SetGuard(t.EngineRef, true) == nil {
				m.guards[t.Hash] = true
			}
		}
		e.AddTrackers(t.EngineRef, append(append([]string{}, addManual...), addPublic...))
	}
	m.markDirty(t)
	if len(addManual) > 0 {
		m.Log(t.Hash, fmt.Sprintf("Added %d trackers", len(addManual)))
	}
	if len(addPublic) > 0 {
		m.Log(t.Hash, fmt.Sprintf("Added the public tracker list (%d)", len(addPublic)))
	}
	out.Added, out.PublicAdded = len(addManual), len(addPublic) > 0
	return out, nil
}

// autoPublic is the public list for a new torrent when new torrents get it
// (Settings › Downloads › Torrents), else nil. Not with m.mu held: it may
// start fetching the list.
func (m *Manager) autoPublic() []string {
	if m.Public == nil || !m.Settings().Torrent.PublicTrackers {
		return nil
	}
	return m.Public.Current()
}

// engineTrackers are the trackers handed to the engine besides the
// torrent's own: the extra ones and the public list.
func engineTrackers(t *Task) []string {
	return union(t.Options.Trackers, t.Options.PublicTrackers)
}

func union(lists ...[]string) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range lists {
		for _, u := range l {
			if u = strings.TrimSpace(u); u != "" && !seen[u] {
				seen[u] = true
				out = append(out, u)
			}
		}
	}
	return out
}

func missing(list, have []string) []string {
	var out []string
	for _, u := range list {
		if !contains(have, u) && !contains(out, u) {
			out = append(out, u)
		}
	}
	return out
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
