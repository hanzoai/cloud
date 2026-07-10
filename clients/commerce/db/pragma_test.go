package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestPlaintextPragmasApplied is the self-contained gate proving the modernc
// (pure-Go / CGO_ENABLED=0) DSN pragma translation actually takes effect through
// the ONE hanzoai/sqlite driver. The mattn-style `_busy_timeout=N&_journal_mode=WAL`
// params modernc SILENTLY IGNORES, which would leave a money store on the rollback
// journal (no WAL) with no busy_timeout — lock contention + corruption risk. This
// opens a fresh tenant store on the plaintext path (the production CGO-off boot
// mode) and asserts WAL + a positive busy_timeout on BOTH the read and write pools.
func TestPlaintextPragmasApplied(t *testing.T) {
	dir := t.TempDir()
	sdb, err := NewSQLiteDB(&SQLiteDBConfig{
		Path:       filepath.Join(dir, "money.db"),
		Config:     DefaultConfig().SQLite,
		TenantID:   "acme",
		TenantType: "org",
		MasterKey:  nil, // plaintext -> pure-Go modernc driver under CGO_ENABLED=0
	})
	if err != nil {
		t.Fatalf("NewSQLiteDB (plaintext): %v", err)
	}
	defer sdb.Close()

	// Both pools open the same DSN; both must honor the pragmas. WAL enabled on
	// only one connection would still leave the other on the default journal.
	check := func(name string, pool *sql.DB) {
		var jm string
		if err := pool.QueryRow("PRAGMA journal_mode").Scan(&jm); err != nil {
			t.Fatalf("%s: pragma journal_mode: %v", name, err)
		}
		if jm != "wal" {
			t.Fatalf("%s: journal_mode = %q, want wal — modernc DSN pragmas not honored (mattn-style params were silently ignored)", name, jm)
		}
		var busy int
		if err := pool.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
			t.Fatalf("%s: pragma busy_timeout: %v", name, err)
		}
		if busy <= 0 {
			t.Fatalf("%s: busy_timeout = %d, want > 0 — a money store with no busy_timeout fails a concurrent writer immediately instead of waiting", name, busy)
		}
		t.Logf("%s: journal_mode=%s busy_timeout=%d", name, jm, busy)
	}

	check("readDB", sdb.readDB)
	check("writeDB", sdb.writeDB)
}
