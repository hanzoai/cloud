package kms

// Concurrent-open invariant (HORIZONTAL-SCALE evidence — a REGRESSION GUARD).
//
// This test replaces the former ZapDB probe, which ASSERTED that a second opener
// of the single embedded ZapDB (Badger fork) store FAILED — the exclusive OS lock
// that pinned cloud to replicas=1 and forced the reader tier to reverse-proxy KMS.
// The KMS store is now PER-ORG SQLite ({DataDir}/orgs/{org}/kms.db via cloud.OrgDB
// → cek), which has NO single-opener lock. This test pins the property that makes
// cloud horizontally scalable OUT OF THE BOX:
//
//  1. Two independent Clients open the SAME data dir CONCURRENTLY and both work —
//     there is no exclusive lock, so a second pod can open the store (impossible
//     with the old ZapDB store).
//  2. Distinct orgs land in distinct files, so two "pods" writing DIFFERENT
//     tenants never contend — the file IS the tenant boundary.
//  3. The SAME org's file is openable by a second handle (SQLite is WAL-shareable
//     for reads), which is what lets a reader serve KMS locally instead of
//     proxying. Concurrent WRITERS to one org still require the consistent-hash
//     org→pod routing documented in the package report; this guard is about the
//     absence of the hard OS lock, not a license for uncoordinated writes.
//
// If a future change reintroduces a single global, OS-locked KMS store, THIS TEST
// WILL FAIL — the signal that the replicas=1 constraint has crept back in.
//
// Run:
//   CGO_ENABLED=0 GOWORK=off GOFLAGS=-mod=mod go test ./clients/kms/ -run ConcurrentOpen -v

import (
	"fmt"
	"sync"
	"testing"

	luxlog "github.com/luxfi/log"
)

func TestConcurrentOpen_PerOrgSQLiteHasNoExclusiveLock(t *testing.T) {
	dir := t.TempDir()
	log := luxlog.NewNoOpLogger()
	key := b64key(t, 0x5A)

	// Two independent Clients over the SAME data dir — the two-pods-one-PVC shape.
	// With the old ZapDB store the second open would fail on the exclusive lock;
	// per-org SQLite has none, so both open.
	a, err := New(Config{DataDir: dir, MasterKeyB64: key}, log)
	if err != nil {
		t.Fatalf("client A New: %v", err)
	}
	defer a.Close()
	b, err := New(Config{DataDir: dir, MasterKeyB64: key}, log)
	if err != nil {
		t.Fatalf("client B New (second opener must succeed — no exclusive lock): %v", err)
	}
	defer b.Close()

	// Distinct orgs → distinct files → no cross-tenant contention. Each client
	// writes a different org concurrently; both succeed.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := a.Put("/orgs/acme", "K", "prod", []byte("acme-secret")); err != nil {
			errs <- fmt.Errorf("A Put acme: %w", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := b.Put("/orgs/globex", "K", "prod", []byte("globex-secret")); err != nil {
			errs <- fmt.Errorf("B Put globex: %w", err)
		}
	}()
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent per-org write failed: %v", e)
	}

	// Cross-read: A reads globex (written by B, same shared dir) — the files are
	// shared on disk, only the WRITER should be one pod per org.
	if got, err := a.Get("/orgs/globex", "K", "prod"); err != nil || string(got) != "globex-secret" {
		t.Fatalf("A cross-read globex = %q err=%v, want globex-secret", got, err)
	}
	if got, err := b.Get("/orgs/acme", "K", "prod"); err != nil || string(got) != "acme-secret" {
		t.Fatalf("B cross-read acme = %q err=%v, want acme-secret", got, err)
	}
	t.Log("INVARIANT HELD: per-org SQLite has no exclusive-opener lock — two clients " +
		"open the same data dir concurrently; distinct orgs never contend. replicas=1 lifted.")
}
