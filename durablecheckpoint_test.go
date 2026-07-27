package cloud

import (
	"context"
	"path/filepath"
	"testing"

	sqlitedrv "github.com/hanzoai/sqlite"
)

// TestDurableCheckpointReEncryptsEnvelope proves durableCheckpoint makes the real
// on-disk path reflect every committed write on the pure-Go codec ENVELOPE backend —
// where encryption is deferred to Checkpoint/Close, so a raw wal_checkpoint alone
// leaves the real path stale and a fenced ship would restore that stale snapshot,
// silently losing an acked write on takeover (the HIGH-1 defect, PoC-confirmed).
//
// The store handle is kept OPEN across the checkpoint and the read: closing it would
// itself re-encrypt (Close seals), masking whether durableCheckpoint did so. On the
// write-time backends (cgo libsqlcipher, plaintext) the store persists per-commit and
// the re-encrypt step is a no-op, so this passes on both lanes.
func TestDurableCheckpointReEncryptsEnvelope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "org.db")
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}

	// Seed the encrypted store with row A and seal it (Close re-encrypts the real path).
	seed, err := sqlitedrv.OpenDB(path, key)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := seed.Exec(`CREATE TABLE t(v TEXT)`); err != nil {
		t.Fatalf("ddl: %v", err)
	}
	if _, err := seed.Exec(`INSERT INTO t VALUES('A')`); err != nil {
		t.Fatalf("insert A: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("seal: %v", err)
	}

	// The live store handle Durable.Bind lends to Sync. Commit row B — an ACKED write.
	handle, err := sqlitedrv.OpenDB(path, key)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer handle.Close()
	if _, err := handle.Exec(`INSERT INTO t VALUES('B')`); err != nil {
		t.Fatalf("insert B: %v", err)
	}

	// The ship checkpoint. After it, the real-path bytes readFramed ships MUST include B.
	if err := durableCheckpoint(context.Background(), handle); err != nil {
		t.Fatalf("durableCheckpoint: %v", err)
	}

	// A fresh open reads the CURRENT real-path ciphertext — exactly what a successor
	// CarryForward-restores on takeover. It must see both acked writes, not a stale {A}.
	if got := realPathRows(t, path, key); got != 2 {
		t.Fatalf("SILENT LOST WRITE: real path has %d row(s) after durableCheckpoint, want 2 "+
			"(acked write 'B' dropped → lost on takeover)", got)
	}
}

// realPathRows opens the store fresh (decrypting the current real-path ciphertext) and
// returns the row count — the view a successor sees after CarryForward.
func realPathRows(t *testing.T, path string, key []byte) int {
	t.Helper()
	db, err := sqlitedrv.OpenDB(path, key)
	if err != nil {
		t.Fatalf("open real path: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}
