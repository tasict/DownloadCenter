package core

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"downloadcenter/internal/engine"
	"downloadcenter/internal/qts"
	"downloadcenter/internal/safefs"
)

// Pause pauses a task; minutes > 0 resumes it automatically (wake_time).
func (m *Manager) Pause(hash string, minutes int, by string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.live[hash]
	if !ok {
		return ErrNotFound
	}
	if t.State == StDone || t.State == StMoving {
		return nil
	}
	t.UserPaused = true
	t.WakeTime = 0
	if minutes > 0 {
		t.WakeTime = time.Now().Add(time.Duration(minutes) * time.Minute).Unix()
	}
	if e := m.engineOf(t); e != nil && t.EngineRef != "" {
		if e.Pause(t.EngineRef) == nil {
			m.running[hash] = false
		}
	}
	t.State = StPaused
	t.DownRate, t.UpRate = 0, 0
	m.markDirty(t)
	m.saveTask(t)
	m.TaskEvent("task.paused", t, map[string]any{"by": by, "minutes": minutes})
	return nil
}

// Resume clears a user pause (and retries a failed task).
func (m *Manager) Resume(hash, by string) error {
	m.mu.Lock()
	t, ok := m.live[hash]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	if t.State == StError {
		m.mu.Unlock()
		return m.Retry(hash)
	}
	if t.State == StDone {
		m.mu.Unlock()
		return nil
	}
	was := t.UserPaused
	t.UserPaused = false
	t.WakeTime = 0
	if t.State == StPaused {
		t.State = StQueued
		if t.Kind == KindBT && t.FinishedAt > 0 && t.Size > 0 && t.DoneBytes >= t.Size {
			t.State = StSeeding
		}
	}
	m.markDirty(t)
	m.saveTask(t)
	if was {
		m.TaskEvent("task.resumed", t, map[string]any{"by": by})
	}
	m.mu.Unlock()
	m.Kick()
	return nil
}

// Retry restarts a failed task from where it stopped.
func (m *Manager) Retry(hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.live[hash]
	if !ok {
		return ErrNotFound
	}
	if t.State != StError {
		return nil
	}
	if e := m.engineOf(t); e != nil && t.EngineRef != "" {
		e.Remove(t.EngineRef)
	}
	t.EngineRef = ""
	t.ErrorCode, t.ErrorMsg = "", ""
	t.Options.AutoRetries, t.Options.RetryAt, t.Options.Retries = 0, 0, 0
	t.State = StQueued
	t.UserPaused = false
	if t.Kind == KindBT && t.DoneBytes > 0 {
		t.Options.Check = true
	}
	if t.Kind != KindBT && t.WorkDir != "" {
		// The data of a failed move may still be in the temp folder: if the
		// engine finished, finish the move instead of downloading again
		if t.DoneBytes > 0 && t.DoneBytes == t.Size {
			if _, err := os.Stat(t.WorkDir); err == nil {
				m.startMove(t, t.WorkDir, t.finalDir(), true)
				return nil
			}
		}
	}
	m.markDirty(t)
	m.saveTask(t)
	m.Log(hash, "Retry")
	go m.Kick()
	return nil
}

// Remove removes a task from the list (kept in the history). deleteData
// also deletes the downloaded data; a task whose data is still in its
// @DownloadCenterTemp folder always loses that folder.
func (m *Manager) Remove(hash string, deleteData bool, auto bool) error {
	m.mu.Lock()
	t, ok := m.live[hash]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	if m.moving[hash] || t.State == StMoving {
		m.mu.Unlock()
		return ErrMoving
	}
	e := m.engineOf(t)
	ref := t.EngineRef
	if ref == "" && m.adopting[hash] {
		// Not added yet: the engine still checks the data in the
		// destination, which it knows by the torrent's hash; that stops
		ref = hash
	}
	delete(m.adopting, hash)
	delete(m.live, hash)
	delete(m.running, hash)
	delete(m.applied, hash)
	t.RemovedAt = time.Now().Unix()
	t.Options.DataDeleted = deleteData
	t.EngineRef = ""
	m.saveTask(t)
	m.mu.Unlock()

	if e != nil && ref != "" {
		e.Remove(ref)
	}
	finished := t.State == StDone
	if t.Kind != KindBT {
		if !finished {
			removeTempDir(t.WorkDir)
		} else if deleteData && t.DataPath != "" && taskDataOK(t, t.DataPath) {
			m.removeDownloadedFile(t)
		}
	} else if t.InTemp() && ownTemp(t.WorkDir) {
		removeTempDir(t.WorkDir)
	} else {
		if deleteData {
			m.removeTorrentData(t)
		}
		// Kept data or not, the part file only served the torrent
		m.removePartFiles(hash, t.SaveDir())
	}
	if !finished || deleteData {
		os.Remove(m.torrentPath(hash))
		m.db.X(`DELETE FROM task_sources WHERE task_hash = ?`, hash)
	}
	m.db.DeleteSecrets("task:" + hash)
	m.TaskEvent("task.removed", t, map[string]any{"data_deleted": deleteData, "auto": auto})
	m.Kick()
	return nil
}

// safeToDelete refuses share roots, home roots and anything outside /share.
func safeToDelete(p string) bool {
	p = filepath.Clean(p)
	if !strings.HasPrefix(p, "/share/") || strings.Count(p, "/") < 4 {
		return false
	}
	_, err := os.Lstat(p)
	return err == nil
}

// taskDataOK reports whether p may be deleted as data of t: strictly inside
// the task's temporary, destination or work folder as the task recorded
// them, and never a share root, a home folder or a user's Download folder.
// Paths are compared as they are, links not resolved: the deletion opens
// them one component at a time without following links, so a path that
// leads through a link deletes nothing.
func taskDataOK(t *Task, p string) bool {
	if !safeToDelete(p) {
		return false
	}
	p = filepath.Clean(p)
	for _, sh := range qts.Shares() {
		if p == sh.Path {
			return false
		}
	}
	if hr := homesRoot(); hr != "" {
		rel := strings.TrimPrefix(p, hr+"/")
		if p == hr || (rel != p && (!strings.Contains(rel, "/") || rel == path.Join(path.Dir(rel), "Download") && !strings.Contains(path.Dir(rel), "/"))) {
			return false
		}
	}
	for _, root := range []string{t.TempDir, t.MoveDir, t.WorkDir} {
		if root != "" && p != filepath.Clean(root) && inside(p, root) {
			return true
		}
	}
	return false
}

// removeDownloadedFile deletes the file a finished URL download left at
// its destination. Only a regular file is deleted: anything else under that
// name now (a folder someone put in its place) is someone else's, and
// stays.
func (m *Manager) removeDownloadedFile(t *Task) {
	d, name, err := safefs.OpenParent(t.DataPath)
	if err != nil {
		return
	}
	defer d.Close()
	st, err := d.Lstat(name)
	if err != nil {
		return
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		m.Log(t.Hash, "The downloaded file was not deleted: something else has its name now")
		return
	}
	d.Remove(name)
}

// removeTorrentData deletes a torrent's own files, those in its file list,
// and then the folders that this left empty. Anything else in its folder
// stays, and nothing is followed if it is a link.
func (m *Manager) removeTorrentData(t *Task) {
	base := t.SaveDir()
	top := ""
	if t.DataPath != "" {
		// The data may carry another name at its destination ("Name (1)")
		top = filepath.Base(t.DataPath)
	}
	dirs := map[string]bool{}
	for _, f := range m.Files(t.Hash) {
		rel := filepath.Clean(f.Path)
		if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
			continue
		}
		if top != "" {
			if _, rest, ok := strings.Cut(rel, "/"); ok {
				rel = top + "/" + rest
			} else {
				rel = top
			}
		}
		p := filepath.Join(base, rel)
		if !taskDataOK(t, p) {
			continue
		}
		if d, name, err := safefs.OpenParent(p); err == nil {
			if st, err := d.Lstat(name); err == nil && st.Mode&syscall.S_IFMT != syscall.S_IFDIR {
				d.Remove(name)
			}
			d.Close()
		}
		for dir := filepath.Dir(rel); dir != "." && dir != "/"; dir = filepath.Dir(dir) {
			dirs[dir] = true
		}
	}
	if t.DataPath == "" && t.Name != "" {
		// What aria2 kept beside the data of a 0.9.0 torrent
		if p := filepath.Join(base, t.Name+".aria2"); taskDataOK(t, p) {
			if d, name, err := safefs.OpenParent(p); err == nil {
				if st, err := d.Lstat(name); err == nil && st.Mode&syscall.S_IFMT == syscall.S_IFREG {
					d.Remove(name)
				}
				d.Close()
			}
		}
	}
	list := make([]string, 0, len(dirs))
	for d := range dirs {
		list = append(list, d)
	}
	// Deepest first, so a parent is only tried once its children are gone
	sort.Slice(list, func(i, j int) bool { return strings.Count(list[i], "/") > strings.Count(list[j], "/") })
	for _, rel := range list {
		p := filepath.Join(base, rel)
		if !taskDataOK(t, p) {
			continue
		}
		if d, name, err := safefs.OpenParent(p); err == nil {
			d.RemoveEmpty(name)
			d.Close()
		}
	}
}

// Undo restores a task removed without data deletion (UI undo).
func (m *Manager) Undo(hash string) error {
	t, err := m.loadTask(hash)
	if err != nil || t.RemovedAt == 0 {
		return ErrNotFound
	}
	if t.Options.DataDeleted || time.Now().Unix()-t.RemovedAt > 60 {
		return ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.live[hash]; ok {
		return nil
	}
	t.RemovedAt = 0
	t.EngineRef = ""
	if t.State != StDone && (t.Kind != KindBT || t.InTemp() && ownTemp(t.WorkDir)) {
		// The temp folder was deleted: start over
		t.DoneBytes = 0
	}
	if t.State != StDone && t.Kind == KindBT && t.DoneBytes > 0 {
		t.Options.Check = true
	}
	m.saveTask(t)
	m.live[hash] = t
	go m.Kick()
	return nil
}

// Move changes the queue position: "top", "up", "down", "bottom" or a
// 1-based position. visible limits up/down to the tasks the caller sees.
func (m *Manager) Move(hash, where string, visible func(*Task) bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.live[hash]
	if !ok {
		return ErrNotFound
	}
	list := m.queueOrder()
	idx := -1
	for i, o := range list {
		if o == t {
			idx = i
		}
	}
	switch where {
	case "top":
		list = append(list[:idx], list[idx+1:]...)
		list = append([]*Task{t}, list...)
	case "bottom":
		list = append(list[:idx], list[idx+1:]...)
		list = append(list, t)
	case "up", "down":
		step := -1
		if where == "down" {
			step = 1
		}
		for j := idx + step; j >= 0 && j < len(list); j += step {
			if visible == nil || visible(list[j]) {
				list[idx], list[j] = list[j], list[idx]
				break
			}
		}
	default:
		var n int
		if _, err := fmt.Sscanf(where, "%d", &n); err != nil || n < 1 {
			return ErrBadPosition
		}
		if n > len(list) {
			n = len(list)
		}
		list = append(list[:idx], list[idx+1:]...)
		list = append(list[:n-1], append([]*Task{t}, list[n-1:]...)...)
	}
	for i, o := range list {
		if o.Position != i+1 {
			o.Position = i + 1
			m.markDirty(o)
		}
	}
	go m.Kick()
	return nil
}

// MoveNear puts the tasks ids (as one block, in their queue order) right
// before or after the anchor task. Tasks the caller cannot see are left out;
// an anchor it cannot see is not found. It reports the tasks that get a
// download slot by the move and those that lose theirs, as the next tick
// will do it.
func (m *Manager) MoveNear(ids []string, anchor string, after bool, visible func(*Task) bool) (started, stopped []*Task, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.live[anchor]; !ok || (visible != nil && !visible(a)) {
		return nil, nil, ErrNotFound
	}
	moving := map[string]bool{}
	for _, id := range ids {
		if t, ok := m.live[id]; ok && (visible == nil || visible(t)) {
			moving[id] = true
		}
	}
	if len(moving) == 0 {
		return nil, nil, ErrNotFound
	}
	if moving[anchor] {
		return nil, nil, ErrBadPosition
	}
	mode := m.scheduleMode()
	list := m.queueOrder()
	before, _ := m.slotHolders(list, mode)
	var block, rest []*Task
	for _, t := range list {
		if moving[t.Hash] {
			block = append(block, t)
		} else {
			rest = append(rest, t)
		}
	}
	at := 0
	for i, t := range rest {
		if t.Hash == anchor {
			at = i
			if after {
				at++
			}
		}
	}
	list = append(append(append([]*Task{}, rest[:at]...), block...), rest[at:]...)
	for i, o := range list {
		if o.Position != i+1 {
			o.Position = i + 1
			m.markDirty(o)
		}
	}
	now, _ := m.slotHolders(list, mode)
	for _, t := range list {
		if visible != nil && !visible(t) {
			continue
		}
		if now[t.Hash] && !before[t.Hash] {
			started = append(started, t)
		} else if before[t.Hash] && !now[t.Hash] {
			stopped = append(stopped, t)
		}
	}
	go m.Kick()
	return started, stopped, nil
}

// SetFiles selects the files of a torrent task. prio maps file index to
// 0 (skip) .. 7; without priority levels any non-zero value means "download".
func (m *Manager) SetFiles(hash string, prio map[int]int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.live[hash]
	if !ok {
		return ErrNotFound
	}
	if t.Kind != KindBT {
		return ErrUnsupported
	}
	files := m.Files(hash)
	var sel []int
	pr := map[string]int{}
	for _, f := range files {
		p, given := prio[f.Index]
		if !given {
			p = f.Priority
		}
		if p > 0 {
			sel = append(sel, f.Index)
			if p > 7 {
				p = 7
			}
			pr[fmt.Sprint(f.Index)] = p
		}
	}
	if len(sel) == 0 {
		return errors.New("Select at least one file")
	}
	if len(sel) == len(files) {
		t.Options.Select = nil
	} else {
		t.Options.Select = sel
	}
	t.Options.Priorities = pr
	if e := m.engineOf(t); e != nil && t.EngineRef != "" && t.State != StDone && t.State != StMetadata {
		ints := map[int]int{}
		for k, v := range pr {
			var i int
			fmt.Sscanf(k, "%d", &i)
			ints[i] = v
		}
		if err := e.SetFiles(t.EngineRef, sel, ints); err != nil {
			return err
		}
	}
	m.db.Write(func() error {
		for _, f := range files {
			p := 0
			if v, ok := pr[fmt.Sprint(f.Index)]; ok {
				p = v
			}
			m.db.Exec(`UPDATE task_files SET priority = ? WHERE hash = ? AND idx = ?`, p, hash, f.Index)
			m.forgetFiles(hash)
		}
		return nil
	})
	t.FilesChosen = len(sel)
	if t.State == StSeeding || t.State == StDone {
		// More files selected after completion: download them
		if t.State == StSeeding {
			t.State = StDownloading
			t.SeedWaiting = false
		}
	}
	m.markDirty(t)
	m.saveTask(t)
	return nil
}

// SetTaskLimits sets per-task speed caps (bytes/s, 0 = none).
func (m *Manager) SetTaskLimits(hash string, down, up int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.live[hash]
	if !ok {
		return ErrNotFound
	}
	t.Options.MaxDown, t.Options.MaxUp = down, up
	delete(m.applied, hash)
	m.markDirty(t)
	return nil
}

// SetSequential switches sequential ("stream while downloading") mode.
func (m *Manager) SetSequential(hash string, on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.live[hash]
	if !ok {
		return ErrNotFound
	}
	if t.Kind != KindBT {
		return ErrUnsupported
	}
	e := m.engineOf(t)
	if e == nil || !e.Caps().Sequential {
		return ErrUnsupported
	}
	if t.EngineRef != "" {
		if err := e.SetSequential(t.EngineRef, on); err != nil {
			return err
		}
	}
	t.Options.Sequential = on
	m.markDirty(t)
	return nil
}

// SetAutoRemove changes a task's auto-removal option.
func (m *Manager) SetAutoRemove(hash, v string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.live[hash]
	if !ok {
		return ErrNotFound
	}
	switch v {
	case "", "completed", "seeded":
	default:
		return errors.New("invalid auto_remove")
	}
	t.AutoRemove = v
	m.markDirty(t)
	return nil
}

// Peers lists the connected peers of a torrent task.
func (m *Manager) Peers(hash string) ([]engine.Peer, error) {
	m.mu.Lock()
	t, ok := m.live[hash]
	var e engine.Engine
	ref := ""
	if ok {
		e, ref = m.engineOf(t), t.EngineRef
	}
	m.mu.Unlock()
	if !ok || e == nil || ref == "" || t.Kind != KindBT {
		return []engine.Peer{}, nil
	}
	return e.Peers(ref)
}
