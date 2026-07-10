package db

import (
	"path/filepath"
	"testing"
)

// TestDualPoolConcurrentWriteSurvives reproduces the exact BeforeSuite panic
// end-to-end against the REAL NewSQLiteDB: a held read cursor on the concurrent
// read pool must not make a write on the serialized write pool fail with
// SQLITE_LOCKED ("database table is locked"). Before the fix the plaintext path
// opened with cache=shared and this write returned that un-retryable error.
func TestDualPoolConcurrentWriteSurvives(t *testing.T) {
	sdb, err := NewSQLiteDB(&SQLiteDBConfig{
		Path:       filepath.Join(t.TempDir(), "data.db"),
		Config:     DefaultConfig().SQLite,
		TenantID:   "system",
		TenantType: "org",
		MasterKey:  nil, // plaintext -> encDriverDSN plaintext branch (PragmaDSN)
	})
	if err != nil {
		t.Fatalf("NewSQLiteDB (plaintext dual-pool open) failed: %v", err)
	}
	defer sdb.Close()

	if _, err := sdb.writeDB.Exec(`CREATE TABLE tt(id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := sdb.writeDB.Exec(`INSERT INTO tt(v) VALUES('seed')`); err != nil {
		t.Fatalf("seed insert: %v", err)
	}

	// Hold an open read cursor (a table read lock) on the concurrent read pool.
	rows, err := sdb.readDB.Query(`SELECT id, v FROM tt`)
	if err != nil {
		t.Fatalf("read query: %v", err)
	}
	rows.Next() // pin the cursor open (lock held)
	defer rows.Close()

	// Write from the serialized write pool while the read lock is held — this is
	// what tripped SQLITE_LOCKED under shared cache. It must now succeed.
	if _, err := sdb.writeDB.Exec(`UPDATE tt SET v='changed' WHERE id=1`); err != nil {
		t.Fatalf("concurrent write under held read cursor failed (shared-cache LOCKED regression): %v", err)
	}
}
