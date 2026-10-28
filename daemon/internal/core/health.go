package core

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"downloadcenter/internal/engine"
)

// Source health of a torrent that is not complete: whether anybody still
// shares it, judged from what the engine saw while it had no sources.
const (
	HealthOK         = "ok"          // connected, or data came in lately
	HealthSearching  = "searching"   // no sources yet, not long enough to say more
	HealthNoSharing  = "no_sharing"  // nobody seems to share it
	HealthLikelyDead = "likely_dead" // it has probably stopped being shared
)

const (
	// healthAfter is how long a torrent goes without sources before it
	// counts as searching (and its trackers are asked about every minute)
	healthAfter = 10 * 60
	// dhtRound is one DHT announce round of libtorrent: DHT usable this
	// long without a reply means it was asked and found nobody
	dhtRound = 15 * 60
	// findPeersEvery limits looking for peers again by hand
	findPeersEvery = time.Minute
)

// Health is what a torrent showed of its sources, kept in its options so
// that it survives restarts. The times only grow while the torrent runs.
type Health struct {
	StalledSecs     int64           `json:"stalled_secs,omitempty"`    // running with no connection and no progress
	IncompleteSecs  int64           `json:"incomplete_secs,omitempty"` // connected, but the peers lack pieces and nothing comes
	EverConnected   bool            `json:"ever_connected,omitempty"`
	LastConnectedAt int64           `json:"last_connected_at,omitempty"`
	LastDataAt      int64           `json:"last_data_at,omitempty"`
	DHTSecs         int64           `json:"dht_secs,omitempty"`     // of StalledSecs, DHT was usable
	DHTFound        int             `json:"dht_found,omitempty"`    // peers of the latest DHT reply in this period
	DHTReplyAt      int64           `json:"dht_reply_at,omitempty"` // latest DHT reply seen (the engine's clock)
	Trackers        *TrackerSummary `json:"trackers,omitempty"`     // the latest look at the trackers in this period
	LikelyNotified  bool            `json:"likely_notified,omitempty"`
}

// TrackerSummary sums up a torrent's trackers for the health level. Some
// trackers of the public list report a seeder for any torrent: seeders
// count when the torrent's own trackers (or those added by hand) report
// them, or two trackers of the public list do.
type TrackerSummary struct {
	Total        int   `json:"total"`
	Answered     int   `json:"answered"`     // answered the last announce
	SeedsKnown   int   `json:"seeds_known"`  // of them, reported how many seeders there are
	AnySeeds     bool  `json:"any_seeds"`    // seeders are reported (see above)
	PublicSeeds  int   `json:"public_seeds"` // trackers of the public list reporting seeders
	Unregistered bool  `json:"unregistered"` // one says the torrent does not exist
	CheckedAt    int64 `json:"checked_at"`
}

// Tracker states in the sources of a task.
const (
	TrackerWorking      = "working"
	TrackerFailing      = "failing"
	TrackerUnregistered = "unregistered"
	TrackerNotContacted = "not_contacted"
)

// notRegistered are what trackers say about a torrent they do not know.
// Public trackers register any torrent announced to them, so this is
// mostly private trackers.
var notRegistered = []string{
	"unregistered", "not registered", "torrent not found", "unknown torrent",
	"info_hash not found", "infohash not found", "torrent does not exist",
}

func saysNotRegistered(msg string) bool {
	msg = strings.ToLower(msg)
	for _, s := range notRegistered {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// trackerState names the state of one tracker.
func trackerState(tr engine.TrackerState) string {
	switch {
	case !tr.Contacted:
		return TrackerNotContacted
	case tr.Fails == 0:
		return TrackerWorking
	case saysNotRegistered(tr.Message):
		return TrackerUnregistered
	}
	return TrackerFailing
}

// summarize sums up trackers; the counts of a failing tracker are old and
// do not count. public: the trackers of the public list the task has.
func summarize(trs []engine.TrackerState, public []string, now int64) *TrackerSummary {
	s := &TrackerSummary{Total: len(trs), CheckedAt: now}
	own := false
	for _, tr := range trs {
		switch trackerState(tr) {
		case TrackerWorking:
			s.Answered++
			if tr.Seeds >= 0 {
				s.SeedsKnown++
			}
			if tr.Seeds > 0 {
				if contains(public, tr.URL) {
					s.PublicSeeds++
				} else {
					own = true
				}
			}
		case TrackerUnregistered:
			s.Unregistered = true
		}
	}
	s.AnySeeds = own || s.PublicSeeds >= 2
	return s
}

// healthLimits are the thresholds in seconds: not shared, probably dead
// (never shorter than the first).
func healthLimits(s BTSettings) (noSharing, likelyDead int64) {
	noSharing = int64(s.NoSharingHours) * 3600
	likelyDead = int64(s.LikelyDeadDays) * 86400
	if likelyDead < noSharing {
		likelyDead = noSharing
	}
	return
}

// sourceHealth judges a torrent from its health record: the level and,
// for not shared and probably dead, why (unregistered, no_seeders,
// incomplete). Private torrents do not use DHT.
func sourceHealth(h *Health, noSharing, likelyDead int64, private bool, connections int) (level, reason string) {
	if h == nil {
		return HealthOK, ""
	}
	tr := h.Trackers
	if tr == nil {
		tr = &TrackerSummary{}
	}
	switch {
	case connections == 0 && tr.Unregistered && !tr.AnySeeds:
		return HealthLikelyDead, "unregistered"
	case h.StalledSecs >= likelyDead && !tr.AnySeeds:
		return HealthLikelyDead, "no_seeders"
	case h.IncompleteSecs >= likelyDead:
		return HealthLikelyDead, "incomplete"
	}
	// Nobody found, and somebody looked: DHT for a round, or a tracker
	// that counts seeders; without either the evidence is too thin
	dhtLooked := !private && h.DHTSecs >= dhtRound
	nobody := !tr.AnySeeds && (private || h.DHTFound == 0)
	switch {
	case h.StalledSecs >= noSharing && nobody && (dhtLooked || tr.SeedsKnown > 0):
		return HealthNoSharing, "no_seeders"
	case h.IncompleteSecs >= noSharing:
		return HealthNoSharing, "incomplete"
	case h.StalledSecs >= healthAfter || h.IncompleteSecs >= healthAfter:
		return HealthSearching, ""
	}
	return HealthOK, ""
}

// watchHealth folds one poll of a torrent into its health record and
// judges it. looking: it runs and should be finding data (not paused,
// checking, moving or seeding); connected: a connection lasted long enough
// to be a source (see sourceConnect); progress: data came in since the
// last poll it was looking. Caller holds m.mu.
func (m *Manager) watchHealth(t *Task, st *engine.Status, looking, connected, progress bool, elapsed int64) {
	if st.Seeding || t.State == StSeeding || t.State == StDone {
		if t.Options.Health != nil {
			t.Options.Health = nil
			m.markDirty(t)
		}
		t.SourceHealth = ""
		return
	}
	h := t.Options.Health
	if h == nil {
		h = &Health{}
		t.Options.Health = h
		m.markDirty(t)
	}
	now := m.clock().Unix()
	conns := 0
	if connected {
		conns = st.Connections
	}
	if connected {
		if !h.EverConnected {
			h.EverConnected = true
			m.markDirty(t)
		}
		h.LastConnectedAt = now
	}
	if progress {
		h.LastDataAt = now
	}
	reply := st.DHTReplyAt > h.DHTReplyAt
	if reply {
		h.DHTReplyAt = st.DHTReplyAt
	}
	if looking {
		if !connected && !progress {
			h.StalledSecs += elapsed
			if st.DHTUp {
				h.DHTSecs += elapsed
			}
			// A newer reply counts; the same engine may also take the NAS
			// itself out of its count once it learns its address
			if reply || st.DHTReplyAt == h.DHTReplyAt && st.DHTPeers < h.DHTFound {
				h.DHTFound = st.DHTPeers
			}
		} else if h.StalledSecs > 0 || h.Trackers != nil {
			// A new period: what the trackers and DHT said is old now
			h.StalledSecs, h.DHTSecs, h.DHTFound, h.Trackers = 0, 0, 0, nil
			m.markDirty(t)
		}
		if connected && !st.IsMetadata && st.Availability >= 0 && st.Availability < 1 && !progress {
			h.IncompleteSecs += elapsed
		} else if h.IncompleteSecs > 0 {
			h.IncompleteSecs = 0
			m.markDirty(t)
		}
	}
	noSharing, likelyDead := healthLimits(m.Settings().Torrent)
	level, reason := sourceHealth(h, noSharing, likelyDead, st.Private, conns)
	t.SourceHealth = level
	switch {
	case level == HealthOK && h.LikelyNotified:
		h.LikelyNotified = false
		m.markDirty(t)
	case level == HealthLikelyDead && !h.LikelyNotified:
		h.LikelyNotified = true
		m.markDirty(t)
		waited := h.StalledSecs
		if reason == "incomplete" {
			waited = h.IncompleteSecs
		}
		m.TaskEvent("task.likely_dead", t, map[string]any{
			"waiting_secs": waited, "ever_connected": h.EverConnected, "reason": reason,
		})
		m.Log(t.Hash, fmt.Sprintf("No sources for %d hours; this download has probably stopped being shared", waited/3600))
	}
}

// askTrackers runs every minute: torrents that have gone without sources
// for a while, or whose peers lack pieces, get their trackers summed up.
// Torrents that download normally are left alone. Not with m.mu held.
func (m *Manager) askTrackers() {
	type item struct {
		hash, ref string
		pf        engine.PeerFinder
	}
	var list []item
	m.mu.Lock()
	for h, t := range m.live {
		hl := t.Options.Health
		if t.Kind != KindBT || t.EngineRef == "" || hl == nil || hl.StalledSecs < healthAfter && hl.IncompleteSecs == 0 {
			continue
		}
		e := m.engineOf(t)
		if pf, ok := e.(engine.PeerFinder); ok && e.Caps().PeerRefresh {
			list = append(list, item{h, t.EngineRef, pf})
		}
	}
	m.mu.Unlock()
	for _, it := range list {
		trs, err := it.pf.Sources(it.ref)
		if err != nil {
			continue
		}
		m.mu.Lock()
		if t := m.live[it.hash]; t != nil && t.EngineRef == it.ref {
			m.noteTrackers(t, trs)
		}
		m.mu.Unlock()
	}
}

// noteTrackers keeps the summary of a torrent's trackers while it has no
// sources (a torrent that has them needs none). Caller holds m.mu.
func (m *Manager) noteTrackers(t *Task, trs []engine.TrackerState) {
	h := t.Options.Health
	if h == nil || h.StalledSecs == 0 && h.IncompleteSecs == 0 {
		return
	}
	h.Trackers = summarize(trs, t.Options.PublicTrackers, m.clock().Unix())
	m.markDirty(t)
}

// HealthReport is what the source health of a task is based on, for the API.
type HealthReport struct {
	Level            string          `json:"level,omitempty"` // empty for a failed task
	Reason           string          `json:"reason,omitempty"`
	WaitingSecs      int64           `json:"waiting_secs"`
	IncompleteSecs   int64           `json:"incomplete_secs"`
	EverConnected    bool            `json:"ever_connected"`
	LastConnectedAt  int64           `json:"last_connected_at"`
	LastDataAt       int64           `json:"last_data_at"`
	Availability     float64         `json:"availability"`
	LastSeenComplete int64           `json:"last_seen_complete"`
	Running          bool            `json:"running"` // the times grow now
	NoSharingSecs    int64           `json:"no_sharing_secs"`
	LikelyDeadSecs   int64           `json:"likely_dead_secs"`
	DHT              *HealthDHT      `json:"dht,omitempty"` // not for private torrents
	Trackers         []HealthTracker `json:"trackers"`
}

// HealthDHT is what DHT found in this period.
type HealthDHT struct {
	Available    bool  `json:"available"`
	SearchedSecs int64 `json:"searched_secs"`
	Found        int   `json:"found"`
}

// HealthTracker is one tracker of a task.
type HealthTracker struct {
	URL      string `json:"url"`
	Host     string `json:"host"`
	State    string `json:"state"`
	Seeds    int    `json:"seeds"`
	Leechers int    `json:"leechers"`
	Fails    int    `json:"fails"` // failed announces in a row
	Message  string `json:"message,omitempty"`
	Public   bool   `json:"public"` // from the public tracker list
}

// HealthOf reports what the source health of a torrent is based on; it
// asks the engine about the trackers (outside m.mu) and keeps their
// summary. A torrent without a record yet reports nothing found so far.
// ErrUnsupported for URL tasks and complete torrents.
func (m *Manager) HealthOf(hash string) (*HealthReport, error) {
	m.mu.Lock()
	t := m.live[hash]
	if t == nil {
		m.mu.Unlock()
		return nil, ErrNotFound
	}
	if t.Kind != KindBT || t.State == StSeeding || t.State == StDone {
		m.mu.Unlock()
		return nil, ErrUnsupported
	}
	ref := t.EngineRef
	e := m.engineOf(t)
	m.mu.Unlock()
	var trs []engine.TrackerState
	var st *engine.Status
	if ref != "" && e != nil {
		if pf, ok := e.(engine.PeerFinder); ok && e.Caps().PeerRefresh {
			trs, _ = pf.Sources(ref)
		}
		st, _ = e.Status(ref)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if t = m.live[hash]; t == nil {
		return nil, ErrNotFound
	}
	if trs != nil && t.EngineRef == ref {
		m.noteTrackers(t, trs)
	}
	h := t.Options.Health
	if h == nil {
		h = &Health{}
	}
	noSharing, likelyDead := healthLimits(m.Settings().Torrent)
	out := &HealthReport{
		WaitingSecs: h.StalledSecs, IncompleteSecs: h.IncompleteSecs,
		EverConnected: h.EverConnected, LastConnectedAt: h.LastConnectedAt, LastDataAt: h.LastDataAt,
		Availability: -1, Running: m.running[hash] && t.State != StError,
		NoSharingSecs: noSharing, LikelyDeadSecs: likelyDead,
		Trackers: []HealthTracker{},
	}
	private := false
	if st != nil {
		out.Availability, out.LastSeenComplete, private = st.Availability, st.LastSeenComplete, st.Private
	}
	if t.State != StError {
		out.Level, out.Reason = sourceHealth(h, noSharing, likelyDead, private, t.Peers)
	}
	if !private {
		out.DHT = &HealthDHT{Available: st != nil && st.DHTUp, SearchedSecs: h.DHTSecs, Found: h.DHTFound}
	}
	public := map[string]bool{}
	for _, u := range t.Options.PublicTrackers {
		public[u] = true
	}
	for _, tr := range trs {
		host := tr.URL
		if u, err := url.Parse(tr.URL); err == nil && u.Hostname() != "" {
			host = u.Hostname()
		}
		out.Trackers = append(out.Trackers, HealthTracker{
			URL: tr.URL, Host: host, State: trackerState(tr), Seeds: tr.Seeds, Leechers: tr.Leechers,
			Fails: tr.Fails, Message: tr.Message, Public: public[tr.URL],
		})
	}
	return out, nil
}

// FindPeers makes a torrent without a connection look for peers right
// away instead of at its next automatic search. Not done: "connected",
// "not_running" or "too_soon" (with the seconds to wait); a search counts
// like an automatic one and moves the next one on.
func (m *Manager) FindPeers(hash string) (done bool, reason string, wait int, err error) {
	m.mu.Lock()
	t := m.live[hash]
	if t == nil {
		m.mu.Unlock()
		return false, "", 0, ErrNotFound
	}
	if t.Kind != KindBT {
		m.mu.Unlock()
		return false, "", 0, ErrUnsupported
	}
	e := m.engineOf(t)
	pf, ok := e.(engine.PeerFinder)
	if !ok || !e.Caps().PeerRefresh {
		m.mu.Unlock()
		return false, "", 0, ErrUnsupported
	}
	ref, now := t.EngineRef, m.clock()
	s := m.stall[hash]
	switch {
	// A moment's connection (see sourceConnect) does not stop it; the
	// engine still refuses while one is open
	case t.Peers > 0 && s != nil && !s.conn.IsZero() && now.Sub(s.conn) >= sourceConnect:
		reason = "connected"
	case ref == "" || !m.running[hash] || t.State == StError:
		reason = "not_running"
	case s != nil && !s.last.IsZero() && now.Sub(s.last) < findPeersEvery:
		reason, wait = "too_soon", int((findPeersEvery-now.Sub(s.last)+time.Second-1)/time.Second)
	}
	if reason != "" {
		m.mu.Unlock()
		return false, reason, wait, nil
	}
	if s == nil {
		s = &stall{done: t.DoneBytes}
		m.stall[hash] = s
	}
	// Reserved before the call: a second click meanwhile is too soon
	prev := s.last
	s.last = now
	m.mu.Unlock()
	done, err = pf.RefreshPeers(ref)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil || !done {
		s.last = prev
		if err != nil {
			return false, "", 0, err
		}
		return false, "connected", 0, nil
	}
	s.n++
	if t = m.live[hash]; t != nil {
		t.PeerSearches = s.n
		m.Log(hash, "Looked for peers again")
	}
	return true, "", 0, nil
}
