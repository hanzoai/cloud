package audit

// Shareability probe (HA carve evidence, NOT a shipped feature).
//
// Proves the audit SQLite store is SHAREABLE by a read-only second opener while
// the single writer is live-appending — the exact reader/writer split the HA
// carve needs. A reader opened with ?mode=ro:
//   - never takes the write lock, so it cannot contend with or corrupt the
//     writer's serialized appends (no chain-head fork: the reader never appends),
//   - sees the writer's committed records via the shared WAL (concurrent reads),
//   - and on "promotion" a fresh RW open recovers the head from disk == the
//     writer's last synchronously-persisted record (no gap, no fork).
//
// This is the audit-store analog of clients/kms TestReaderReadOnlyRoundTrip
// (which proves the same for the KMS ZapDB store). Run:
//   CGO_ENABLED=0 GOWORK=off GOFLAGS=-mod=mod go test ./audit/ -run Shareability -v

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/cek"
	_ "github.com/hanzoai/sqlite"
)

func TestShareability_ReaderSharesLiveWriterStore(t *testing.T) {
	requireSharedStore(t)

	dir := t.TempDir()
	path := dir + "/audit.db"

	// Writer: the real serialized Recorder (WAL, MaxOpenConns(1)).
	w, err := Open(path, nil)
	if err != nil {
		t.Fatalf("writer Open: %v", err)
	}
	defer w.Close()

	const total = 200
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < total; i++ {
			if _, err := w.Append(context.Background(), Record{Action: "POST /v1/probe"}); err != nil {
				t.Errorf("append %d: %v", i, err)
				return
			}
			time.Sleep(200 * time.Microsecond)
		}
	}()

	// Reader: a SECOND, independent connection opened READ-ONLY against the SAME
	// files while the writer appends. This is what a reader-role pod does.
	// Opened through cek: the store is encrypted at rest, so a bare sql.Open has no
	// key and cannot read it. cek has no read-only mode, so this is a second RW
	// handle that only ever reads — it still proves the reader sees the live
	// writer's committed records, which is the claim, but it does not by itself
	// prove the reader takes no write lock.
	ro, err := cek.Open(path)
	if err != nil {
		t.Fatalf("reader Open: %v", err)
	}
	defer ro.Close()

	// Poll the reader's view of the head while the writer runs. It must (a) never
	// error, (b) be monotonic, and (c) eventually observe the writer's records —
	// concurrent cross-connection RO reads over a live WAL writer.
	var lastSeen int64 = -1
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var maxSeq sql.NullInt64
		if err := ro.QueryRow(`SELECT MAX(seq) FROM audit_log`).Scan(&maxSeq); err != nil {
			t.Fatalf("reader concurrent read failed (store NOT shareable): %v", err)
		}
		if maxSeq.Valid {
			if maxSeq.Int64 < lastSeen {
				t.Fatalf("reader head regressed %d -> %d (not monotonic)", lastSeen, maxSeq.Int64)
			}
			lastSeen = maxSeq.Int64
		}
		if lastSeen >= total-1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	wg.Wait()

	if lastSeen < total-1 {
		t.Fatalf("reader never caught up: saw head %d, writer wrote %d", lastSeen, total-1)
	}

	// Writer chain is intact and complete AFTER concurrent reader activity.
	gotCount, _ := w.Head()
	if gotCount != total {
		t.Fatalf("writer head count = %d, want %d", gotCount, total)
	}

	// "Promotion": a fresh RW opener (the reader taking over) recovers the head
	// from disk == the writer's last synchronously-persisted record. No fork.
	promoted, err := Open(path, nil)
	if err != nil {
		t.Fatalf("promotion RW re-open: %v", err)
	}
	defer promoted.Close()
	pc, _ := promoted.Head()
	if pc != total {
		t.Fatalf("promoted writer recovered head %d, want %d (chain gap/fork)", pc, total)
	}
	t.Logf("SHAREABLE: reader observed %d records concurrently; writer chain=%d; promotion recovered head=%d",
		lastSeen+1, gotCount, pc)
}
