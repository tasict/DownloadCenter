package dl

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"downloadcenter/internal/engine"
)

const (
	basePiece   = 1 << 20 // progress granularity
	maxPieces   = 16384   // larger files get larger pieces
	minSplit    = 4 << 20 // a connection takes over at least this much
	idleTimeout = 60 * time.Second
	maxFails    = 5 // consecutive connection failures without progress
	maxRestarts = 3 // re-downloads from zero in one run
)

// task is one URL download.
type task struct {
	e   *Engine
	ref string

	mu    sync.Mutex
	req   engine.AddRequest
	uris  []string
	state engine.State
	code  string
	msg   string

	// What is known about the remote file
	known    bool // probed, or loaded from a control file
	name     string
	size     int64 // -1 = unknown (no Content-Length)
	ranges   bool  // byte ranges work: segmented and resumable
	noRanges bool  // a range request was ignored: do not trust Accept-Ranges again
	pieceLen int64
	done     []bool
	etag     string
	modified string
	adopted  string // aria2 control file whose progress was taken over
	contSize int64  // size of a data file without progress record (continue after it)

	// Current run
	file       *os.File
	workers    []*worker
	streamDone bool  // single stream of unknown size reached its end
	recv       int64 // bytes received since the engine started (speed)
	lastRecv   int64
	lastAt     time.Time
	rate       int64
	cancel     context.CancelFunc
	runDone    chan struct{}
	saved      time.Time

	lim limiter
}

// worker is one connection downloading [pos, end) of the file.
type worker struct {
	start  int64
	pos    int64
	end    int64 // exclusive; -1 = until the stream ends
	marked int   // next piece to mark complete
}

func newTask(e *Engine, ref string, r engine.AddRequest) *task {
	t := &task{e: e, ref: ref, req: r, uris: append([]string(nil), r.URIs...), state: engine.Paused, size: -1}
	t.lim.set(r.MaxDown)
	return t
}

func (t *task) uri() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.uris[0]
}

func (t *task) dataPath() string { return filepath.Join(t.req.Dir, t.name) }

// resume starts the download unless it runs or has ended.
func (t *task) resume() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.runDone != nil || t.state == engine.Complete || t.state == engine.Error {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.cancel, t.runDone = cancel, done
	t.state = engine.Active
	go t.run(ctx, done)
}

// stop ends a running download and waits until its writers are gone.
func (t *task) stop(st engine.State) {
	t.mu.Lock()
	cancel, done := t.cancel, t.runDone
	t.mu.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
		}
	}
	t.mu.Lock()
	if t.state == engine.Active {
		t.state = st
	}
	t.mu.Unlock()
}

func (t *task) run(ctx context.Context, done chan struct{}) {
	err := t.download(ctx)
	t.mu.Lock()
	if t.file != nil {
		t.file.Close()
		t.file = nil
	}
	t.workers = nil
	t.cancel, t.runDone = nil, nil
	switch {
	case ctx.Err() != nil:
		// Paused or removed: the caller sets the state
	case err != nil:
		var de *dlError
		if !errors.As(err, &de) {
			de = &dlError{code: engine.ErrUnknown, msg: err.Error()}
		}
		t.state, t.code, t.msg = engine.Error, de.code, de.msg
	default:
		t.state = engine.Complete
	}
	t.mu.Unlock()
	if ctx.Err() != nil || err != nil {
		t.saveControl()
	}
	close(done)
}

// download runs one attempt after another until the file is complete, the
// context ends or an error is final. Restarts from zero happen when the
// remote file changed or the server stopped honouring ranges.
func (t *task) download(ctx context.Context) error {
	if err := os.MkdirAll(t.req.Dir, 0775); err != nil {
		return &dlError{code: engine.ErrCreateDir, msg: err.Error()}
	}
	for restarts := 0; ; restarts++ {
		err := t.attempt(ctx)
		var de *dlError
		if ctx.Err() == nil && errors.As(err, &de) && de.restart && restarts < maxRestarts {
			t.mu.Lock()
			t.known = false
			if de.noRanges {
				t.noRanges = true
			}
			t.mu.Unlock()
			t.e.logf(t.ref, "伺服器上的檔案已變更或不支援續傳，重新下載")
			continue
		}
		return err
	}
}

func (t *task) attempt(ctx context.Context) error {
	t.mu.Lock()
	t.streamDone = false
	known := t.known
	t.mu.Unlock()
	var first *stream
	if !known {
		s, inf, err := t.probe(ctx)
		if err != nil {
			return err
		}
		t.mu.Lock()
		t.applyInfo(inf)
		// A data file without progress record (an imported task, like
		// aria2's continue=true): keep the bytes before its end
		keep := t.contSize > 0 && t.ranges && t.contSize <= t.size
		flags := os.O_RDWR | os.O_CREATE | os.O_TRUNC
		if keep {
			flags = os.O_RDWR | os.O_CREATE
			for i := int64(0); i < t.contSize/t.pieceLen; i++ {
				t.done[i] = true
			}
		}
		t.contSize = 0
		t.mu.Unlock()
		if keep && s != nil {
			// The probe started at byte 0: let the connections start after
			// what is already there
			s.Close()
			s = nil
		}
		f, err := os.OpenFile(t.dataPath(), flags, 0664)
		if err != nil {
			if s != nil {
				s.Close()
			}
			return fileError(engine.ErrCreateFile, err)
		}
		t.mu.Lock()
		t.file = f
		t.mu.Unlock()
		first = s
	} else {
		f, err := os.OpenFile(t.dataPath(), os.O_RDWR|os.O_CREATE, 0664)
		if err != nil {
			return fileError(engine.ErrCreateFile, err)
		}
		t.mu.Lock()
		t.file = f
		t.mu.Unlock()
	}
	t.saveControl()
	t.removeAdopted()
	err := t.supervise(ctx, first)
	if err == nil {
		return t.finalize()
	}
	return err
}

// applyInfo takes over what the first response said. Caller holds t.mu.
func (t *task) applyInfo(inf *info) {
	t.known = true
	t.name = inf.name
	if t.req.Out != "" {
		t.name = t.req.Out
	}
	t.size = inf.size
	t.ranges = inf.ranges && inf.size > 0 && !t.noRanges
	t.etag, t.modified = inf.etag, inf.modified
	t.pieceLen, t.done = 0, nil
	if t.size > 0 {
		t.pieceLen = basePiece
		for t.size/t.pieceLen > maxPieces {
			t.pieceLen *= 2
		}
		t.done = make([]bool, int((t.size+t.pieceLen-1)/t.pieceLen))
	}
}

type result struct {
	w   *worker
	err error
}

// supervise runs the connections until every piece is present.
func (t *task) supervise(ctx context.Context, first *stream) error {
	wctx, wcancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() {
		wcancel()
		wg.Wait()
	}()
	results := make(chan result, 16)
	launch := func(w *worker, s *stream) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- result{w, t.work(wctx, w, s)}
		}()
	}
	t.mu.Lock()
	maxConns := t.e.MaxConns
	// FTP and SFTP servers often limit the connections per account
	if !isHTTP(t.uris[0]) || !t.ranges || maxConns < 1 {
		maxConns = 1
	}
	if first != nil {
		w := &worker{end: t.size}
		t.workers = append(t.workers, w)
		launch(w, first)
	}
	t.mu.Unlock()

	fails := 0
	var lastErr error
	nextSpawn := time.Now()
	progress := t.received()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		t.mu.Lock()
		if time.Now().After(nextSpawn) && fails <= maxFails {
			for len(t.workers) < maxConns {
				w := t.assign()
				if w == nil {
					break
				}
				t.workers = append(t.workers, w)
				launch(w, nil)
			}
		}
		active, finished := len(t.workers), t.finished()
		t.mu.Unlock()
		if active == 0 {
			if finished {
				return nil
			}
			if fails > maxFails && lastErr != nil {
				return lastErr
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case r := <-results:
			t.mu.Lock()
			t.dropWorker(r.w)
			others := len(t.workers)
			t.mu.Unlock()
			if r.err == nil || wctx.Err() != nil {
				continue
			}
			var de *dlError
			if !errors.As(r.err, &de) {
				de = &dlError{code: engine.ErrUnknown, msg: r.err.Error()}
			}
			switch {
			case de.restart:
				return de
			case de.perConn && others > 0:
				// The server refuses more connections: live with fewer
				maxConns = others
			case !de.transient:
				return de
			default:
				fails++
				lastErr = de
				nextSpawn = time.Now().Add(time.Duration(fails*3) * time.Second)
			}
		case <-tick.C:
			if n := t.received(); n != progress {
				progress, fails = n, 0
			}
			t.mu.Lock()
			due := time.Since(t.saved) >= 5*time.Second
			t.mu.Unlock()
			if due {
				t.saveControl()
			}
		}
	}
}

func (t *task) received() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.recv
}

func (t *task) dropWorker(w *worker) {
	for i, o := range t.workers {
		if o == w {
			t.workers = append(t.workers[:i], t.workers[i+1:]...)
			return
		}
	}
}

// finished reports whether the whole file is present. Caller holds t.mu.
func (t *task) finished() bool {
	if t.size < 0 {
		return t.streamDone
	}
	for _, d := range t.done {
		if !d {
			return false
		}
	}
	return true
}

// covered reports whether a running connection will fetch piece i.
func (t *task) covered(i int) bool {
	for _, w := range t.workers {
		if w.end < 0 {
			return true
		}
		last := int((w.end + t.pieceLen - 1) / t.pieceLen)
		if i >= w.marked && i < last {
			return true
		}
	}
	return false
}

// assign returns the next range for a new connection, or nil. A missing
// stretch no connection covers comes first; otherwise the largest remaining
// range of a running connection is split in two. Caller holds t.mu.
func (t *task) assign() *worker {
	if t.size < 0 {
		if len(t.workers) == 0 && !t.streamDone {
			return &worker{end: -1}
		}
		return nil
	}
	if !t.ranges {
		if len(t.workers) > 0 || t.finished() {
			return nil
		}
		// Without ranges a broken stream starts over
		for i := range t.done {
			t.done[i] = false
		}
		return &worker{end: t.size}
	}
	n := len(t.done)
	for i := 0; i < n; i++ {
		if t.done[i] || t.covered(i) {
			continue
		}
		j := i
		for j < n && !t.done[j] && !t.covered(j) {
			j++
		}
		start := int64(i) * t.pieceLen
		return &worker{start: start, pos: start, end: min(int64(j)*t.pieceLen, t.size), marked: i}
	}
	var best *worker
	var rem int64
	for _, w := range t.workers {
		if r := w.end - w.pos; r > rem {
			best, rem = w, r
		}
	}
	if best == nil || rem < 2*minSplit {
		return nil
	}
	mid := best.pos + rem/2
	mid = (mid + t.pieceLen - 1) / t.pieceLen * t.pieceLen
	if mid <= best.pos || mid >= best.end {
		return nil
	}
	w := &worker{start: mid, pos: mid, end: best.end, marked: int(mid / t.pieceLen)}
	best.end = mid
	return w
}

// work downloads the range of w over s (opened here when nil).
func (t *task) work(ctx context.Context, w *worker, s *stream) error {
	if s == nil {
		t.mu.Lock()
		from, to := w.pos, w.end
		t.mu.Unlock()
		var err error
		if s, err = t.open(ctx, from, to); err != nil {
			return err
		}
	}
	defer s.Close()
	var stalled atomic.Bool
	idle := time.AfterFunc(idleTimeout, func() { stalled.Store(true); s.cancel() })
	defer idle.Stop()
	buf := make([]byte, 64<<10)
	for {
		t.mu.Lock()
		pos, end, f := w.pos, w.end, t.file
		t.mu.Unlock()
		if end >= 0 && pos >= end {
			return nil
		}
		want := t.lim.chunk(len(buf))
		if end >= 0 && end-pos < int64(want) {
			want = int(end - pos)
		}
		n, rerr := s.body.Read(buf[:want])
		if n > 0 {
			idle.Reset(idleTimeout)
			if _, err := f.WriteAt(buf[:n], pos); err != nil {
				return fileError(engine.ErrFileIO, err)
			}
			t.mu.Lock()
			w.pos += int64(n)
			t.recv += int64(n)
			t.mark(w)
			t.mu.Unlock()
			if err := t.lim.wait(ctx, n); err != nil {
				return err
			}
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if stalled.Load() {
				return &dlError{code: engine.ErrTimeout, msg: "no data received for 60 seconds", transient: true}
			}
			if errors.Is(rerr, io.EOF) {
				t.mu.Lock()
				defer t.mu.Unlock()
				if w.end < 0 {
					t.streamDone = true
					t.size = w.pos
					return nil
				}
				if w.pos >= w.end {
					return nil
				}
				return &dlError{code: engine.ErrNetwork, msg: "connection closed early", transient: true}
			}
			var de *dlError
			if errors.As(rerr, &de) {
				return de
			}
			return netError(rerr, t.req.Proxy != "")
		}
	}
}

// mark records the pieces w has completed. Caller holds t.mu.
func (t *task) mark(w *worker) {
	if t.pieceLen == 0 {
		return
	}
	for w.marked < len(t.done) {
		end := min(int64(w.marked+1)*t.pieceLen, t.size)
		if w.pos < end {
			break
		}
		t.done[w.marked] = true
		w.marked++
	}
}

// finalize trims the file, drops the control file and dates the file like
// the server did.
func (t *task) finalize() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.file != nil {
		if t.size >= 0 {
			if err := t.file.Truncate(t.size); err != nil {
				return fileError(engine.ErrFileIO, err)
			}
		}
		if err := t.file.Close(); err != nil {
			return fileError(engine.ErrFileIO, err)
		}
		t.file = nil
	}
	os.Remove(controlPath(t.req.Dir, t.name))
	if tm, err := http.ParseTime(t.modified); err == nil {
		os.Chtimes(t.dataPath(), tm, tm)
	}
	for i := range t.done {
		t.done[i] = true
	}
	return nil
}

// completed counts the bytes present. Caller holds t.mu.
func (t *task) completed() int64 {
	if t.size < 0 || t.pieceLen == 0 {
		var n int64
		for _, w := range t.workers {
			n += w.pos
		}
		if t.state == engine.Complete && t.size > 0 {
			return t.size
		}
		return n
	}
	var n int64
	for i, d := range t.done {
		if d {
			n += min(t.pieceLen, t.size-int64(i)*t.pieceLen)
		}
	}
	for _, w := range t.workers {
		if p := w.pos - int64(w.marked)*t.pieceLen; p > 0 {
			n += p
		}
	}
	return min(n, t.size)
}

func (t *task) bitfield() string {
	if len(t.done) == 0 {
		return ""
	}
	b := make([]byte, (len(t.done)+7)/8)
	for i, d := range t.done {
		if d {
			b[i/8] |= 0x80 >> uint(i%8)
		}
	}
	return hex.EncodeToString(b)
}

func (t *task) status() *engine.Status {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	if dt := now.Sub(t.lastAt).Seconds(); dt >= 0.5 {
		inst := int64(float64(t.recv-t.lastRecv) / dt)
		if t.lastAt.IsZero() {
			inst = 0
		}
		t.rate = (t.rate + inst) / 2
		t.lastRecv, t.lastAt = t.recv, now
	}
	st := &engine.Status{
		Ref: t.ref, State: t.state, Name: t.name, Dir: t.req.Dir,
		Completed: t.completed(), Connections: len(t.workers),
		ErrorCode: t.code, ErrorMsg: t.msg,
		NumPieces: len(t.done), PieceLength: t.pieceLen, Bitfield: t.bitfield(),
	}
	if t.size > 0 {
		st.Total = t.size
	}
	if t.state == engine.Active {
		st.DownRate = t.rate
	} else {
		t.rate = 0
	}
	if t.name != "" {
		st.Files = []engine.File{{Index: 0, Path: t.name, Size: st.Total, Completed: st.Completed, Selected: true, Priority: 1}}
	}
	return st
}
