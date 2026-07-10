package kms

// Concurrent-open invariant (HA carve evidence — a REGRESSION GUARD, not a feature).
//
// blue_readonly_test.go proves the SEQUENTIAL reader path (write, CLOSE the
// writer, THEN reopen RO). That is a red herring for HA: in a shared-PVC
// same-node topology the writer pod holds the ZapDB store open for WRITE (an
// actively-growing memtable WAL) while a reader pod would open the SAME on-disk
// files. This test proves that scenario is NOT supported and, deliberately,
// asserts the FAILURE so the constraint is enforced in CI:
//
//	Opening a live ZapDB (Badger fork) store READ-ONLY while the writer is
//	mid-write fails with "Log truncate required to run DB" — Badger's RO open
//	replays the current memtable WAL, finds it partially written
//	(end offset < preallocated size), and REFUSES to truncate it (truncation
//	is a write, forbidden in RO mode). There is no torn read; there is no open.
//
// CONSEQUENCE (the design this guards): a reader-role pod must NOT open the KMS
// ZapDB store off the live writer's PVC. The KMS store is the ONE cloud store
// that is NOT concurrently shareable (unlike the audit SQLite store — see
// audit/shareability_probe_test.go — which shares cleanly over WAL). Therefore a
// reader serves KMS by REVERSE-PROXYING /v1/kms/* (and every mutation +
// /v1/admin/*) to the writer, never by opening the store locally. If a future
// zapdb release makes live concurrent RO-open work, THIS TEST WILL FAIL — that is
// the signal to revisit the reader-serves-KMS-locally option.
//
// Run:
//   CGO_ENABLED=0 GOWORK=off GOFLAGS=-mod=mod go test ./clients/kms/ -run ConcurrentOpen -v

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
)

func TestConcurrentOpen_LiveWriterStoreIsNotROShareable(t *testing.T) {
	dir := t.TempDir()
	log := luxlog.NewNoOpLogger()
	key := b64key(t, 0x5A)

	// Writer: create the encrypted store and seed baseline secrets, then keep
	// writing so the current memtable WAL is genuinely mid-flight (not flushed,
	// not closed) when the reader attempts to open.
	w, err := New(Config{DataDir: dir, MasterKeyB64: key}, log)
	if err != nil {
		t.Fatalf("writer New: %v", err)
	}
	defer w.Close()
	for i := 0; i < 200; i++ {
		if err := w.Put("/orgs/acme", fmt.Sprintf("K%d", i), "default", []byte(fmt.Sprintf("v%d", i))); err != nil {
			t.Fatalf("writer seed Put %d: %v", i, err)
		}
	}

	var stop atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 200; !stop.Load(); i++ {
			if err := w.Put("/orgs/acme", fmt.Sprintf("K%d", i), "default", []byte(fmt.Sprintf("v%d", i))); err != nil {
				return // writer wound down; not the subject of this assertion
			}
			time.Sleep(200 * time.Microsecond)
		}
	}()
	time.Sleep(50 * time.Millisecond) // ensure the WAL is actively mid-write

	// Reader: attempt to open the SAME files READ-ONLY (BypassLockGuard) while the
	// writer is live. INVARIANT: this must fail (no torn read, no silent success).
	r, err := New(Config{DataDir: dir, MasterKeyB64: key, ReadOnly: true}, log)
	stop.Store(true)
	wg.Wait()

	if err == nil {
		if r != nil {
			_ = r.Close()
		}
		t.Fatal("EXPECTED concurrent RO-open of a live ZapDB writer to FAIL, but it " +
			"succeeded. If zapdb now supports live concurrent RO-open, the reader " +
			"tier may serve KMS locally instead of proxying — revisit the design.")
	}
	// The failure is the WAL-truncation refusal, confirming Badger's RO open cannot
	// coexist with a live writer's unflushed memtable.
	if !strings.Contains(err.Error(), "truncate") && !strings.Contains(err.Error(), "Log truncate") {
		t.Logf("concurrent RO-open failed (as required) with a different error: %v", err)
	}
	t.Logf("INVARIANT HELD: live ZapDB store is NOT RO-shareable (%v) — reader must proxy /v1/kms/*", err)
}
