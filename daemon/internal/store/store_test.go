package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestNewerSchemaRefusedAndBackup(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "dc.db")
	db, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMeta("probe", "kept")
	bk := filepath.Join(dir, "backup.db")
	if err := db.Backup(bk); err != nil {
		t.Fatal(err)
	}
	db.SetMeta("schema_version", "99")
	db.Close()
	if _, err := Open(p); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("a database from a newer build opened: %v", err)
	}
	b, err := Open(bk)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.Meta("probe") != "kept" {
		t.Fatal("the backup lost data")
	}
}
