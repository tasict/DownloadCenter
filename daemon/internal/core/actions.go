package core

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"downloadcenter/internal/engine"
	"downloadcenter/internal/qts"
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
	m.Log(hash, "重試")
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
		return errors.New("files_moving")
	}
	e := m.engineOf(t)
	ref := t.EngineRef
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
	var paths []string
	if t.Kind != KindBT {
		if !finished {
			removeTempDir(t.WorkDir)
		} else if deleteData && t.DataPath != "" {
			paths = append(paths, t.DataPath)
		}
	} else if t.InTemp() && ownTemp(t.WorkDir) {
		removeTempDir(t.WorkDir)
	} else {
		if deleteData {
			if t.DataPath != "" {
				paths = append(paths, t.DataPath)
			} else if t.Name != "" {
				p := filepath.Join(t.SaveDir(), t.Name)
				paths = append(paths, p, p+".aria2")
			}
		}
		// Kept data or not, the part file only served the torrent
		m.removePartFiles(hash, t.SaveDir())
	}
	for _, p := range paths {
		if taskDataOK(t, p) {
			os.RemoveAll(p)
		}
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

// realPath resolves the symlinks of p's parent folders (the last element
// is kept: RemoveAll never follows a final symlink).
func realPath(p string) (string, bool) {
	p = filepath.Clean(p)
	dir, err := filepath.EvalSymlinks(filepath.Dir(p))
	if err != nil {
		return "", false
	}
	return filepath.Join(dir, filepath.Base(p)), true
}

// taskDataOK reports whether p may be deleted as data of t: strictly inside
// the task's temporary, destination or work folder, and never a share root,
// a home folder or a user's Download folder.
func taskDataOK(t *Task, p string) bool {
	if !safeToDelete(p) {
		return false
	}
	real, ok := realPath(p)
	if !ok {
		return false
	}
	for _, sh := range qts.Shares() {
		if real == sh.Path {
			return false
		}
	}
	if hr := qts.HomesRoot(); hr != "" {
		rel := strings.TrimPrefix(real, hr+"/")
		if real == hr || (rel != real && (!strings.Contains(rel, "/") || rel == path.Join(path.Dir(rel), "Download") && !strings.Contains(path.Dir(rel), "/"))) {
			return false
		}
	}
	for _, root := range []string{t.TempDir, t.MoveDir, t.WorkDir} {
		if root == "" {
			continue
		}
		r, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		if real != r && strings.HasPrefix(real, r+"/") {
			return true
		}
	}
	return false
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
	var list []*Task
	for _, o := range m.live {
		list = append(list, o)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Position != list[j].Position {
			return list[i].Position < list[j].Position
		}
		return list[i].CreatedAt < list[j].CreatedAt
	})
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
			return errors.New("invalid position")
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
		return errors.New("至少要選一個檔案")
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
		}
		return nil
	})
	t.FilesChosen = len(sel)
	if t.State == StSeeding || t.State == StDone {
		// More files selected after completion: download them
		if t.State == StSeeding {
			t.State = StDownloading
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
