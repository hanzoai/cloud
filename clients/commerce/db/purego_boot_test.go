package db

import (
	"os"
	"testing"
)

// TestPureGoOpensMigratedLedger proves the plaintext systemDB path opens on the
// pure-Go (modernc) backend under CGO_ENABLED=0 — the production build mode — and
// can read the migrated central ledger. Guards the fix for the CGO-off
// "go-sqlite3 requires cgo ... This is a stub" boot failure. The test DB is the
// real pg->SQLite ETL output; point COMMERCE_TEST_LEDGER at it.
func TestPureGoOpensMigratedLedger(t *testing.T) {
	path := os.Getenv("COMMERCE_TEST_LEDGER")
	if path == "" {
		t.Skip("COMMERCE_TEST_LEDGER not set")
	}
	cfg := DefaultConfig()
	sdb, err := NewSQLiteDB(&SQLiteDBConfig{
		Path:       path,
		Config:     cfg.SQLite,
		TenantID:   "system",
		TenantType: "org",
		MasterKey:  nil, // plaintext (no COMMERCE_KMS_MASTER_KEY) -> pure-Go driver
	})
	if err != nil {
		t.Fatalf("NewSQLiteDB (pure-Go plaintext open) failed: %v", err)
	}
	defer sdb.Close()

	var n int
	if err := sdb.readDB.QueryRow("SELECT count(*) FROM _entities").Scan(&n); err != nil {
		t.Fatalf("query _entities: %v", err)
	}
	t.Logf("_entities rows = %d", n)
	if n == 0 {
		t.Fatalf("expected migrated rows, got 0")
	}

	var txns int
	var sumAmount int64
	if err := sdb.readDB.QueryRow(
		"SELECT count(*), COALESCE(SUM(CAST(json_extract(data,'$.amount') AS INTEGER)),0) FROM _entities WHERE kind='transaction'",
	).Scan(&txns, &sumAmount); err != nil {
		t.Fatalf("query transactions: %v", err)
	}
	t.Logf("transactions = %d  sum(amount) = %d", txns, sumAmount)

	// Prove a busy_timeout is actually set (the modernc DSN form applied) — the
	// whole point of switching off the mattn-style params modernc ignores.
	var busy int
	if err := sdb.readDB.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		t.Fatalf("pragma busy_timeout: %v", err)
	}
	t.Logf("busy_timeout = %d", busy)
	if busy < 1000 {
		t.Fatalf("busy_timeout not applied (got %d) — modernc DSN pragmas not honored", busy)
	}
}
