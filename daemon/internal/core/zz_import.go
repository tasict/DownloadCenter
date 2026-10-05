package core

import (
	"os"
	"path/filepath"
	"time"

	"downloadcenter/internal/torrent"
)

// ImportSpec describes a task imported from the official Download Station.
// Imported data is never moved by the import itself: unfinished tasks keep
// downloading where the official package left them.
type ImportSpec struct {
	Hash       string
	Owner      string
	Kind       string // http | ftp | bt
	Source     string // URL, magnet or torrent name
	Name       string
	TempDir    string // real path of the temporary location
	MoveDir    string // real path, "" = do not move
	WorkDir    string // real path of the partial data in the official temporary folder ("" = a new temp folder)
	OutName    string // URL task: name of the partial file to continue
	Torrent    []byte
	Select     []int // nil = all files
	Done       bool  // completed: history record only
	DataPath   string
	Size       int64
	Created    int64
	Started    int64
	Finished   int64
	Caller     string
	ResumeFile string // official libtorrent 1.2 .fastresume (read only)
}

// HasTask reports whether a hash is already known (live or in the history).
func (m *Manager) HasTask(hash string) bool {
	var n int
	m.db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE hash = ?`, hash).Scan(&n)
	return n > 0
}

// ImportTask adds an imported task. Existing hashes are skipped (ErrDuplicate).
func (m *Manager) ImportTask(s ImportSpec) error {
	if m.HasTask(s.Hash) {
		return &DupError{ID: s.Hash}
	}
	now := time.Now().Unix()
	if s.Created == 0 {
		s.Created = now
	}
	caller := s.Caller
	if caller == "" {
		caller = "Download Station"
	}
	t := &Task{
		Hash: s.Hash, Owner: s.Owner, Kind: s.Kind, Source: s.Source, Name: s.Name,
		State: StQueued, TempDir: s.TempDir, MoveDir: s.MoveDir, WorkDir: s.WorkDir,
		Size: s.Size, CreatedAt: s.Created, StartedAt: s.Started, FinishedAt: s.Finished,
		Caller: caller, Imported: true,
	}
	t.Options.Official = true
	t.Options.OutName = s.OutName
	t.Options.Select = s.Select
	t.Options.ResumeFile = s.ResumeFile
	var files []FileRow
	if s.Kind == KindBT {
		if len(s.Torrent) > 0 {
			meta, err := torrent.Parse(s.Torrent)
			if err != nil {
				return ErrBadTorrent
			}
			if t.Name == "" {
				t.Name = meta.Name
			}
			t.Comment = meta.Comment
			t.IsFolder = meta.IsFolder
			if t.Size == 0 {
				t.Size = selectedSize(meta, s.Select)
			}
			for _, f := range meta.Files {
				prio := 1
				if s.Select != nil && !containsInt(s.Select, f.Index) {
					prio = 0
				}
				files = append(files, FileRow{Index: f.Index, Path: f.Path, Size: f.Size, Priority: prio})
			}
			t.FilesTotal = len(files)
			t.FilesChosen = len(files)
			if s.Select != nil {
				t.FilesChosen = len(s.Select)
			}
			if !s.Done {
				if err := os.WriteFile(m.torrentPath(t.Hash), s.Torrent, 0600); err != nil {
					return err
				}
			}
		} else {
			t.Options.Magnet = s.Source
			t.State = StMetadata
		}
		t.Engine = m.BTEngine().Name()
		t.Options.Check = true
	}
	if s.Done {
		t.State = StDone
		t.RemovedAt = now
		t.DoneBytes = t.Size
		t.DataPath = s.DataPath
		if t.FinishedAt == 0 {
			t.FinishedAt = now
		}
		if err := m.saveTask(t); err != nil {
			return err
		}
		if len(files) > 0 {
			m.db.Tx(func(tx *sqlTx) error { return m.storeFiles(tx, t.Hash, files) })
		}
		return nil
	}
	if t.WorkDir == "" {
		if t.Kind == KindBT {
			m.stageTorrent(t, t.Name)
		} else {
			t.WorkDir = m.workDirFor(t.TempDir, t.Hash)
		}
	}
	m.mu.Lock()
	t.Position = m.nextPosition()
	if err := m.saveTask(t); err != nil {
		m.mu.Unlock()
		return err
	}
	if len(files) > 0 {
		m.db.Tx(func(tx *sqlTx) error { return m.storeFiles(tx, t.Hash, files) })
	}
	m.live[t.Hash] = t
	m.mu.Unlock()
	m.Log(t.Hash, "Imported from the official Download Station")
	m.TaskEvent("task.added", t, map[string]any{"source": "import"})
	m.Kick()
	return nil
}

// FinalPath is where a finished task's data is expected (move folder or
// temporary location plus the task name).
func FinalPath(dir, name string) string {
	if dir == "" || name == "" {
		return ""
	}
	return filepath.Join(dir, name)
}
