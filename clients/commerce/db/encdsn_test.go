package db

import (
	"strings"
	"testing"
)

// TestPlaintextDSNNoSharedCache guards the fix for the BeforeSuite panic
// "database table is locked: _entities". Commerce opens a concurrent read pool
// and a serialized write pool against the SAME file; under SQLite shared-cache
// mode a held read cursor takes a table read lock and the writer gets
// SQLITE_LOCKED — which busy_timeout does NOT retry (it only retries
// SQLITE_BUSY). The plaintext DSN therefore MUST NOT set cache=shared. (Routing
// the plaintext path through sqlitedrv.DSN(path, nil), which appends
// cache=shared, is what panicked the test suite.)
func TestPlaintextDSNNoSharedCache(t *testing.T) {
	driver, dsn := encDriverDSN("/tmp/tenant/data.db", nil, DefaultConfig().SQLite)

	if driver != "sqlite" {
		t.Fatalf("plaintext driver = %q, want the canonical hanzoai/sqlite %q", driver, "sqlite")
	}
	if strings.Contains(dsn, "cache=shared") {
		t.Fatalf("plaintext DSN must NOT use shared cache (dual read/write pools deadlock on SQLITE_LOCKED): %s", dsn)
	}
	// The correctness-critical pragmas must still be present (in whichever
	// backend form the active build emits — mattn `_busy_timeout=`, modernc
	// `_pragma=busy_timeout(`).
	if !strings.Contains(dsn, "busy_timeout") {
		t.Fatalf("plaintext DSN missing busy_timeout: %s", dsn)
	}
	if !strings.Contains(dsn, "WAL") {
		t.Fatalf("plaintext DSN missing journal_mode=WAL: %s", dsn)
	}
}
