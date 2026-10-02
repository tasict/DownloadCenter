// Package store opens the package database (SQLite, WAL) and owns its schema.
// The database is the source of truth for settings, users, tasks and their
// mapping to engine references; engines can always be rebuilt from it.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps *sql.DB. SQLite allows one writer at a time; writes are
// serialised through mu so that busy errors do not surface to callers.
type DB struct {
	*sql.DB
	mu sync.Mutex
}

// SchemaVersion is the database layout this build writes. Raise it with any
// change an older build could not read; a build refuses a database with a
// higher number (see Open), and every release records it in release.json so
// the updater knows when going back to an older version needs a backup.
const SchemaVersion = 1

// ErrNewerSchema is returned by Open for a database written by a newer build.
var ErrNewerSchema = errors.New("the database was written by a newer version of Download Center")

var schema = []string{
	`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	// Secrets (passwords, API keys, bot tokens, cookies) never leave the
	// daemon through an API. The database file is 0600 inside data/.
	`CREATE TABLE IF NOT EXISTS secrets (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS users (
		name TEXT PRIMARY KEY,
		role TEXT NOT NULL DEFAULT 'user',
		qts_admin INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL,
		last_login_at INTEGER NOT NULL DEFAULT 0,
		prefs TEXT NOT NULL DEFAULT '{}'
	)`,
	`CREATE TABLE IF NOT EXISTS tasks (
		hash TEXT PRIMARY KEY,
		owner TEXT NOT NULL,
		kind TEXT NOT NULL,
		source TEXT NOT NULL,
		name TEXT NOT NULL DEFAULT '',
		engine TEXT NOT NULL DEFAULT '',
		engine_ref TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL,
		user_paused INTEGER NOT NULL DEFAULT 0,
		wake_time INTEGER NOT NULL DEFAULT 0,
		position INTEGER NOT NULL DEFAULT 0,
		temp_dir TEXT NOT NULL DEFAULT '',
		move_dir TEXT NOT NULL DEFAULT '',
		work_dir TEXT NOT NULL DEFAULT '',
		data_path TEXT NOT NULL DEFAULT '',
		size INTEGER NOT NULL DEFAULT 0,
		done_bytes INTEGER NOT NULL DEFAULT 0,
		down_total INTEGER NOT NULL DEFAULT 0,
		up_total INTEGER NOT NULL DEFAULT 0,
		up_base INTEGER NOT NULL DEFAULT 0,
		down_base INTEGER NOT NULL DEFAULT 0,
		files_total INTEGER NOT NULL DEFAULT 0,
		files_chosen INTEGER NOT NULL DEFAULT 0,
		is_folder INTEGER NOT NULL DEFAULT 0,
		error_code TEXT NOT NULL DEFAULT '',
		error_msg TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		started_at INTEGER NOT NULL DEFAULT 0,
		finished_at INTEGER NOT NULL DEFAULT 0,
		seeded_at INTEGER NOT NULL DEFAULT 0,
		active_secs INTEGER NOT NULL DEFAULT 0,
		auto_remove TEXT NOT NULL DEFAULT '',
		account TEXT NOT NULL DEFAULT '',
		options TEXT NOT NULL DEFAULT '{}',
		comment TEXT NOT NULL DEFAULT '',
		caller TEXT NOT NULL DEFAULT '',
		imported INTEGER NOT NULL DEFAULT 0,
		removed_at INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX IF NOT EXISTS tasks_owner ON tasks (owner, removed_at)`,
	`CREATE INDEX IF NOT EXISTS tasks_ref ON tasks (engine, engine_ref)`,
	`CREATE TABLE IF NOT EXISTS task_files (
		hash TEXT NOT NULL,
		idx INTEGER NOT NULL,
		path TEXT NOT NULL,
		size INTEGER NOT NULL DEFAULT 0,
		done INTEGER NOT NULL DEFAULT 0,
		priority INTEGER NOT NULL DEFAULT 1,
		PRIMARY KEY (hash, idx)
	)`,
	// Other torrents of the same content (different infohash) folded into a
	// task; one is active at a time.
	`CREATE TABLE IF NOT EXISTS task_sources (
		task_hash TEXT NOT NULL,
		source_hash TEXT NOT NULL,
		kind TEXT NOT NULL,
		source TEXT NOT NULL,
		name TEXT NOT NULL DEFAULT '',
		active INTEGER NOT NULL DEFAULT 0,
		added_at INTEGER NOT NULL,
		PRIMARY KEY (task_hash, source_hash)
	)`,
	`CREATE TABLE IF NOT EXISTS task_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		hash TEXT NOT NULL,
		time INTEGER NOT NULL,
		msg TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS task_log_hash ON task_log (hash, id)`,
	// Site credentials and file-hosting accounts, per owner.
	`CREATE TABLE IF NOT EXISTS accounts (
		id TEXT PRIMARY KEY,
		owner TEXT NOT NULL,
		kind TEXT NOT NULL,
		host TEXT NOT NULL DEFAULT '',
		username TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1,
		info TEXT NOT NULL DEFAULT '{}',
		created_at INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS tokens (
		id TEXT PRIMARY KEY,
		owner TEXT NOT NULL,
		name TEXT NOT NULL,
		secret_hash TEXT NOT NULL,
		scopes TEXT NOT NULL,
		tasks TEXT NOT NULL DEFAULT 'own',
		folders TEXT NOT NULL DEFAULT '[]',
		sources TEXT NOT NULL DEFAULT '[]',
		ip_allow TEXT NOT NULL DEFAULT '[]',
		expires_at INTEGER NOT NULL DEFAULT 0,
		rate_limit INTEGER NOT NULL DEFAULT 120,
		created_at INTEGER NOT NULL,
		last_used_at INTEGER NOT NULL DEFAULT 0,
		last_used_ip TEXT NOT NULL DEFAULT '',
		use_count INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS audit (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		time INTEGER NOT NULL,
		token_id TEXT NOT NULL,
		owner TEXT NOT NULL,
		ip TEXT NOT NULL,
		method TEXT NOT NULL,
		path TEXT NOT NULL,
		status INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		type TEXT NOT NULL,
		time INTEGER NOT NULL,
		owner TEXT NOT NULL DEFAULT '',
		task_hash TEXT NOT NULL DEFAULT '',
		data TEXT NOT NULL DEFAULT '{}'
	)`,
	`CREATE INDEX IF NOT EXISTS events_time ON events (time)`,
	// Notification channels: built-in services, generic webhooks and
	// declarative adapters. Secret fields live in secrets ("channel:<id>").
	`CREATE TABLE IF NOT EXISTS channels (
		id TEXT PRIMARY KEY,
		owner TEXT NOT NULL,
		service TEXT NOT NULL,
		name TEXT NOT NULL,
		config TEXT NOT NULL DEFAULT '{}',
		events TEXT NOT NULL DEFAULT '[]',
		operate INTEGER NOT NULL DEFAULT 0,
		scope TEXT NOT NULL DEFAULT 'own',
		quiet TEXT NOT NULL DEFAULT '',
		digest INTEGER NOT NULL DEFAULT 0,
		template TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1,
		fail_count INTEGER NOT NULL DEFAULT 0,
		last_event_id INTEGER NOT NULL DEFAULT 0,
		state TEXT NOT NULL DEFAULT '{}',
		created_at INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS deliveries (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		channel_id TEXT NOT NULL,
		event_id INTEGER NOT NULL,
		delivery TEXT NOT NULL,
		attempt INTEGER NOT NULL DEFAULT 0,
		next_at INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'pending',
		http_status INTEGER NOT NULL DEFAULT 0,
		duration_ms INTEGER NOT NULL DEFAULT 0,
		error TEXT NOT NULL DEFAULT '',
		time INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS deliveries_channel ON deliveries (channel_id, id)`,
	`CREATE INDEX IF NOT EXISTS deliveries_pending ON deliveries (status, next_at)`,
	`CREATE TABLE IF NOT EXISTS chat_links (
		channel_id TEXT NOT NULL,
		chat_user TEXT NOT NULL,
		qts_user TEXT NOT NULL,
		scopes TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		PRIMARY KEY (channel_id, chat_user)
	)`,
	`CREATE TABLE IF NOT EXISTS pair_codes (
		code TEXT PRIMARY KEY,
		channel_id TEXT NOT NULL,
		qts_user TEXT NOT NULL,
		scopes TEXT NOT NULL,
		expires_at INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS adapters (
		id TEXT PRIMARY KEY,
		title TEXT NOT NULL,
		manifest TEXT NOT NULL,
		created_at INTEGER NOT NULL
	)`,
}

// Open opens (and creates or migrates) the database at path.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sdb.SetMaxOpenConns(4)
	db := &DB{DB: sdb}
	for _, s := range schema {
		if _, err := db.Exec(s); err != nil {
			return nil, fmt.Errorf("schema: %w", err)
		}
	}
	if v, _ := strconv.Atoi(db.Meta("schema_version")); v > SchemaVersion {
		db.Close()
		return nil, fmt.Errorf("%w (schema %d, this build reads up to %d): install that version again or restore a backup from data/backups", ErrNewerSchema, v, SchemaVersion)
	}
	db.SetMeta("schema_version", fmt.Sprint(SchemaVersion))
	for _, f := range []string{path, path + "-wal", path + "-shm"} {
		os.Chmod(f, 0600)
	}
	return db, nil
}

// Write runs fn while holding the write lock.
func (db *DB) Write(fn func() error) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return fn()
}

// X executes a write statement under the write lock.
func (db *DB) X(query string, args ...any) (sql.Result, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.Exec(query, args...)
}

// Tx runs fn in a transaction under the write lock.
func (db *DB) Tx(fn func(tx *sql.Tx) error) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Backup writes a consistent copy of the database to path (VACUUM INTO).
func (db *DB) Backup(path string) error {
	os.Remove(path)
	db.mu.Lock()
	defer db.mu.Unlock()
	if _, err := db.Exec(`VACUUM INTO ?`, path); err != nil {
		return err
	}
	return os.Chmod(path, 0600)
}

func (db *DB) Meta(key string) string {
	var v string
	db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	return v
}

func (db *DB) SetMeta(key, value string) {
	db.X(`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
}

// GetJSON reads a settings row into v; missing rows leave v untouched.
func (db *DB) GetJSON(key string, v any) error {
	var s string
	err := db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&s)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(s), v)
}

func (db *DB) PutJSON(key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = db.X(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, string(b))
	return err
}

func (db *DB) Secret(key string) string {
	var v string
	db.QueryRow(`SELECT value FROM secrets WHERE key = ?`, key).Scan(&v)
	return v
}

func (db *DB) SetSecret(key, value string) error {
	if value == "" {
		_, err := db.X(`DELETE FROM secrets WHERE key = ?`, key)
		return err
	}
	_, err := db.X(`INSERT INTO secrets (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// DeleteSecrets removes every secret whose key starts with prefix.
func (db *DB) DeleteSecrets(prefix string) {
	db.X(`DELETE FROM secrets WHERE substr(key, 1, ?) = ?`, len(prefix), prefix)
}

// Now is the unix time used for every timestamp column.
func Now() int64 { return time.Now().Unix() }

// Placeholders returns "?, ?, ?" for n arguments.
func Placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}
