package core

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
)

// Task states (package level; the V4 layer maps them to its integers).
const (
	StQueued      = "queued" // waiting for a slot (or schedule)
	StDownloading = "downloading"
	StMetadata    = "metadata" // magnet: fetching the file list
	StChecking    = "checking"
	StPaused      = "paused"
	StSeeding     = "seeding"
	StMoving      = "moving"
	StDone        = "done"
	StError       = "error"
)

// Kinds.
const (
	KindHTTP = "http"
	KindFTP  = "ftp"
	KindBT   = "bt"
)

// TaskOptions are per-task options stored as JSON.
type TaskOptions struct {
	Select        []int          `json:"select,omitempty"`     // selected file indices (nil = all)
	Priorities    map[string]int `json:"priorities,omitempty"` // file index -> 0..7
	Trackers      []string       `json:"trackers,omitempty"`
	Headers       []string       `json:"headers,omitempty"`
	Sequential    bool           `json:"sequential,omitempty"`
	MaxDown       int64          `json:"max_down,omitempty"` // bytes/s, user-set per task
	MaxUp         int64          `json:"max_up,omitempty"`
	AccountMode   string         `json:"account_mode,omitempty"` // auto | none | id | manual
	AccountID     string         `json:"account_id,omitempty"`
	Hoster        string         `json:"hoster,omitempty"` // file-hosting service used to resolve
	HosterAcct    string         `json:"hoster_account,omitempty"`
	OrigURL       string         `json:"orig_url,omitempty"` // share link before resolving
	ExpiresAt     int64          `json:"expires_at,omitempty"`
	Direct        string         `json:"direct,omitempty"`         // resolved direct URL of a file-hosting link
	DirectHeaders []string       `json:"direct_headers,omitempty"` // headers/cookies the direct URL needs
	NoPages       bool           `json:"no_pages,omitempty"`       // a web page from the direct URL is an error
	Retries       int            `json:"retries,omitempty"`
	AutoRetries   int            `json:"auto_retries,omitempty"`
	RetryAt       int64          `json:"retry_at,omitempty"`
	MoveSrc       string         `json:"move_src,omitempty"`
	MoveDst       string         `json:"move_dst,omitempty"`
	MoveAt        int64          `json:"move_at,omitempty"` // torrents: when the engine was asked to move the data
	Root          string         `json:"root,omitempty"`    // torrents: name of the data at its destination when not the torrent's
	DataDeleted   bool           `json:"data_deleted,omitempty"`
	Check         bool           `json:"check,omitempty"`    // verify data on the next engine add
	OutName       string         `json:"out,omitempty"`      // file name for URL tasks
	Official      bool           `json:"official,omitempty"` // imported, data in @DownloadStationTempFiles
	UpBaseline    int64          `json:"-"`
	Magnet        string         `json:"magnet,omitempty"`
	ResumeFile    string         `json:"resume_file,omitempty"` // official .fastresume to try (libtorrent)
	LastActive    int64          `json:"last_active,omitempty"` // last time data moved (stall detection)
	Proxy         string         `json:"proxy,omitempty"`       // URL tasks: "" automatic, "none" or a proxy profile id
}

// Task is a row of the tasks table plus live engine figures.
type Task struct {
	Hash        string      `json:"id"`
	Owner       string      `json:"owner"`
	Kind        string      `json:"kind"`
	Source      string      `json:"source"`
	Name        string      `json:"name"`
	Engine      string      `json:"engine"`
	EngineRef   string      `json:"-"`
	State       string      `json:"state"`
	UserPaused  bool        `json:"user_paused"`
	WakeTime    int64       `json:"wake_time"`
	Position    int         `json:"position"`
	TempDir     string      `json:"-"`
	MoveDir     string      `json:"-"`
	WorkDir     string      `json:"-"`
	DataPath    string      `json:"-"`
	Size        int64       `json:"size"`
	DoneBytes   int64       `json:"done"`
	DownTotal   int64       `json:"down_total"`
	UpTotal     int64       `json:"up_total"`
	UpBase      int64       `json:"-"`
	DownBase    int64       `json:"-"`
	FilesTotal  int         `json:"files_total"`
	FilesChosen int         `json:"files_chosen"`
	IsFolder    bool        `json:"is_folder"`
	ErrorCode   string      `json:"error_code"`
	ErrorMsg    string      `json:"error_message"`
	CreatedAt   int64       `json:"created_at"`
	StartedAt   int64       `json:"started_at"`
	FinishedAt  int64       `json:"finished_at"`
	SeededAt    int64       `json:"seeded_at"`
	ActiveSecs  int64       `json:"active_secs"`
	AutoRemove  string      `json:"auto_remove"`
	Account     string      `json:"-"`
	Options     TaskOptions `json:"-"`
	Comment     string      `json:"comment"`
	Caller      string      `json:"caller"`
	Imported    bool        `json:"imported"`
	RemovedAt   int64       `json:"removed_at"`

	// Live figures (not persisted)
	DownRate    int64  `json:"down_rate"`
	UpRate      int64  `json:"up_rate"`
	Peers       int    `json:"peers"`
	Seeds       int    `json:"seeds"`
	Verifying   bool   `json:"-"`
	SchedPaused bool   `json:"sched_paused"`
	Bitfield    string `json:"-"`
	NumPieces   int    `json:"-"`
	ProxyUsed   string `json:"-"` // URL tasks: proxy URL the engine task was added with
	PieceLen    int64  `json:"-"`
}

const taskCols = `hash, owner, kind, source, name, engine, engine_ref, state, user_paused, wake_time, position,
	temp_dir, move_dir, work_dir, data_path, size, done_bytes, down_total, up_total, up_base, down_base,
	files_total, files_chosen, is_folder, error_code, error_msg, created_at, started_at, finished_at, seeded_at,
	active_secs, auto_remove, account, options, comment, caller, imported, removed_at`

type scanner interface{ Scan(dest ...any) error }

type sqlTx = sql.Tx

func scanTask(s scanner) (*Task, error) {
	t := &Task{}
	var opts string
	var up, folder, imported int
	err := s.Scan(&t.Hash, &t.Owner, &t.Kind, &t.Source, &t.Name, &t.Engine, &t.EngineRef, &t.State, &up, &t.WakeTime, &t.Position,
		&t.TempDir, &t.MoveDir, &t.WorkDir, &t.DataPath, &t.Size, &t.DoneBytes, &t.DownTotal, &t.UpTotal, &t.UpBase, &t.DownBase,
		&t.FilesTotal, &t.FilesChosen, &folder, &t.ErrorCode, &t.ErrorMsg, &t.CreatedAt, &t.StartedAt, &t.FinishedAt, &t.SeededAt,
		&t.ActiveSecs, &t.AutoRemove, &t.Account, &opts, &t.Comment, &t.Caller, &imported, &t.RemovedAt)
	if err != nil {
		return nil, err
	}
	t.UserPaused, t.IsFolder, t.Imported = up != 0, folder != 0, imported != 0
	json.Unmarshal([]byte(opts), &t.Options)
	return t, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// saveTask writes every persisted column of t.
func (m *Manager) saveTask(t *Task) error {
	opts, _ := json.Marshal(t.Options)
	_, err := m.db.X(`INSERT INTO tasks (`+taskCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(hash) DO UPDATE SET owner=excluded.owner, kind=excluded.kind, source=excluded.source, name=excluded.name,
		engine=excluded.engine, engine_ref=excluded.engine_ref, state=excluded.state, user_paused=excluded.user_paused,
		wake_time=excluded.wake_time, position=excluded.position, temp_dir=excluded.temp_dir, move_dir=excluded.move_dir,
		work_dir=excluded.work_dir, data_path=excluded.data_path, size=excluded.size, done_bytes=excluded.done_bytes,
		down_total=excluded.down_total, up_total=excluded.up_total, up_base=excluded.up_base, down_base=excluded.down_base,
		files_total=excluded.files_total, files_chosen=excluded.files_chosen, is_folder=excluded.is_folder,
		error_code=excluded.error_code, error_msg=excluded.error_msg, created_at=excluded.created_at, started_at=excluded.started_at,
		finished_at=excluded.finished_at, seeded_at=excluded.seeded_at, active_secs=excluded.active_secs,
		auto_remove=excluded.auto_remove, account=excluded.account, options=excluded.options, comment=excluded.comment,
		caller=excluded.caller, imported=excluded.imported, removed_at=excluded.removed_at`,
		t.Hash, t.Owner, t.Kind, t.Source, t.Name, t.Engine, t.EngineRef, t.State, b2i(t.UserPaused), t.WakeTime, t.Position,
		t.TempDir, t.MoveDir, t.WorkDir, t.DataPath, t.Size, t.DoneBytes, t.DownTotal, t.UpTotal, t.UpBase, t.DownBase,
		t.FilesTotal, t.FilesChosen, b2i(t.IsFolder), t.ErrorCode, t.ErrorMsg, t.CreatedAt, t.StartedAt, t.FinishedAt, t.SeededAt,
		t.ActiveSecs, t.AutoRemove, t.Account, string(opts), t.Comment, t.Caller, b2i(t.Imported), t.RemovedAt)
	return err
}

func (m *Manager) loadTask(hash string) (*Task, error) {
	return scanTask(m.db.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE hash = ?`, hash))
}

func (m *Manager) loadTasks(where string, args ...any) ([]*Task, error) {
	rows, err := m.db.Query(`SELECT `+taskCols+` FROM tasks WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// IsMagnet reports a magnet task that has not received its metadata yet.
func (t *Task) IsMagnet() bool { return strings.HasPrefix(t.Source, "magnet:") }

// staged reports a torrent that, like a URL task, downloads in a temporary
// folder (@DownloadCenterTemp/<hash>, or the official one when imported) and
// moves to its destination once its data is complete. Torrents added by
// 0.9.x have no WorkDir: they download in place and move when seeding ends.
func (t *Task) staged() bool { return t.Kind == KindBT && t.WorkDir != "" }

// InTemp reports a task whose data is still in its temporary folder.
func (t *Task) InTemp() bool {
	if t.Kind == KindBT {
		return t.staged() && t.DataPath == ""
	}
	return t.State != StDone && t.DataPath == ""
}

// SaveDir is the folder the file paths of a torrent are relative to: its
// temporary folder until the data has moved, then the folder the data is in.
func (t *Task) SaveDir() string {
	switch {
	case t.DataPath != "":
		return filepath.Dir(t.DataPath)
	case t.WorkDir != "":
		return t.WorkDir
	}
	return t.TempDir
}

// dataPath is the top folder (or single file) of a torrent's data.
func (t *Task) dataPath() string {
	if t.DataPath != "" {
		return t.DataPath
	}
	if t.Name == "" {
		return ""
	}
	return filepath.Join(t.SaveDir(), t.Name)
}

// Final reports a task that no longer needs an engine.
func (t *Task) Final() bool { return t.State == StDone }

// Progress in percent.
func (t *Task) Progress() float64 {
	if t.State == StDone {
		return 100
	}
	if t.Size <= 0 {
		return 0
	}
	p := float64(t.DoneBytes) * 100 / float64(t.Size)
	if p > 100 {
		p = 100
	}
	return p
}

// ETA in seconds (-1 unknown).
func (t *Task) ETA() int64 {
	if t.State != StDownloading || t.DownRate <= 0 || t.Size <= 0 {
		return -1
	}
	return (t.Size - t.DoneBytes) / t.DownRate
}

// Ratio of uploaded to downloaded data.
func (t *Task) Ratio() float64 {
	base := t.DownTotal
	if base <= 0 {
		base = t.DoneBytes
	}
	if base <= 0 {
		return 0
	}
	return float64(t.UpTotal) / float64(base)
}

// FileRow is a row of task_files.
type FileRow struct {
	Index    int    `json:"index"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Done     int64  `json:"done"`
	Priority int    `json:"priority"`
}

// Files returns the stored file list of a task.
func (m *Manager) Files(hash string) []FileRow {
	rows, err := m.db.Query(`SELECT idx, path, size, done, priority FROM task_files WHERE hash = ? ORDER BY idx`, hash)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []FileRow
	for rows.Next() {
		var f FileRow
		rows.Scan(&f.Index, &f.Path, &f.Size, &f.Done, &f.Priority)
		out = append(out, f)
	}
	return out
}

func (m *Manager) storeFiles(tx *sql.Tx, hash string, files []FileRow) error {
	if _, err := tx.Exec(`DELETE FROM task_files WHERE hash = ?`, hash); err != nil {
		return err
	}
	for _, f := range files {
		if _, err := tx.Exec(`INSERT INTO task_files (hash, idx, path, size, done, priority) VALUES (?,?,?,?,?,?)`,
			hash, f.Index, f.Path, f.Size, f.Done, f.Priority); err != nil {
			return err
		}
	}
	return nil
}

// Log appends a line to a task's log.
func (m *Manager) Log(hash, msg string) {
	m.db.X(`INSERT INTO task_log (hash, time, msg) VALUES (?, strftime('%s','now'), ?)`, hash, msg)
}

// LogLine is one task log entry.
type LogLine struct {
	Time int64  `json:"time"`
	Msg  string `json:"msg"`
}

func (m *Manager) TaskLog(hash string, limit int) []LogLine {
	rows, err := m.db.Query(`SELECT time, msg FROM task_log WHERE hash = ? ORDER BY id DESC LIMIT ?`, hash, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []LogLine
	for rows.Next() {
		var l LogLine
		rows.Scan(&l.Time, &l.Msg)
		out = append(out, l)
	}
	return out
}
