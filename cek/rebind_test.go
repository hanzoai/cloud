// Copyright © 2026 Hanzo AI. MIT License.

package cek

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The migration the principal change requires: a store written before owners were in
// the derivation opens as Global and not as its org. After a rebind that reverses, and
// — the part worth proving — the DATA is still there. A rebind that produced a readable
// store with the wrong DEK would be worse than a failure.
func TestRebindMovesAStoreToItsOwner(t *testing.T) {
	EnsureDevKey()
	path := filepath.Join(t.TempDir(), "finance.db")

	// A store as it exists on a volume today.
	db, err := Open(Global, path)
	if err != nil {
		t.Fatalf("open as global: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE ledger(v TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO ledger VALUES('acme-money')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	_ = db.Close()

	if owned, err := Open(Org("acme"), path); err == nil {
		_ = owned.Close()
		t.Fatal("a global-bound store already opened as its org — the fixture proves nothing")
	}

	if err := Rebind(Global, Org("acme"), path); err != nil {
		t.Fatalf("Rebind: %v", err)
	}

	back, err := Open(Org("acme"), path)
	if err != nil {
		t.Fatalf("owner cannot open its store after rebind: %v", err)
	}
	defer back.Close()
	var got string
	if err := back.QueryRow(`SELECT v FROM ledger`).Scan(&got); err != nil {
		t.Fatalf("data lost in rebind: %v", err)
	}
	if got != "acme-money" {
		t.Fatalf("read %q, want acme-money", got)
	}

	// And the old binding must be gone, or the migration widened access instead of
	// moving it.
	if g, err := Open(Global, path); err == nil {
		_ = g.Close()
		t.Fatal("the store still opens as Global after rebind — the platform principal is still a key to a tenant's data")
	}
}

// Re-running a completed migration must be a no-op that says so, not damage and not a
// hard failure — a converging walk over a live volume will meet already-bound stores.
func TestRebindIsIdempotent(t *testing.T) {
	EnsureDevKey()
	path := filepath.Join(t.TempDir(), "tracker.db")

	db, err := Open(Global, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = db.Close()

	if err := Rebind(Global, Org("acme"), path); err != nil {
		t.Fatalf("first rebind: %v", err)
	}
	err = Rebind(Global, Org("acme"), path)
	if !errors.Is(err, ErrNotBound) {
		t.Fatalf("second rebind returned %v, want ErrNotBound so a re-run reports already-done", err)
	}
	// Still openable by its owner: the refused second pass changed nothing.
	if back, err := Open(Org("acme"), path); err != nil {
		t.Fatalf("store damaged by a repeated rebind: %v", err)
	} else {
		_ = back.Close()
	}
}

// The walk is what an operator actually runs. It must reach project-scoped stores
// (nested a level deeper), leave the reserved platform partition alone, and identify
// stores by SIDECAR — on a pure-Go build the database file may not exist at all.
func TestRebindOrgsWalksEveryTenantStore(t *testing.T) {
	EnsureDevKey()
	dir := t.TempDir()
	const platform = "_platform"

	orgScoped := filepath.Join(dir, "orgs", "acme", "finance.db")
	projScoped := filepath.Join(dir, "orgs", "acme", "projects", "default", "tracker.db")
	platformStore := filepath.Join(dir, "orgs", platform, "leases.db")
	for _, p := range []string{orgScoped, projScoped, platformStore} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		db, err := Open(Global, p)
		if err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
		_ = db.Close()
	}

	bound, skipped, errs := RebindOrgs(dir, platform)
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if bound != 2 {
		t.Fatalf("bound %d stores, want 2 (the org-scoped and the project-scoped)", bound)
	}
	if skipped != 0 {
		t.Fatalf("skipped %d on a first run", skipped)
	}

	for _, p := range []string{orgScoped, projScoped} {
		db, err := Open(Org("acme"), p)
		if err != nil {
			t.Fatalf("%s did not end up bound to acme: %v", p, err)
		}
		_ = db.Close()
	}
	// The platform partition is not a tenant and must be untouched.
	db, err := Open(Global, platformStore)
	if err != nil {
		t.Fatalf("the reserved platform store was rebound and no longer opens as Global: %v", err)
	}
	_ = db.Close()

	// Converging, not transactional: a second pass finds everything already bound.
	bound2, skipped2, errs2 := RebindOrgs(dir, platform)
	if len(errs2) != 0 || bound2 != 0 || skipped2 != 2 {
		t.Fatalf("re-run: bound=%d skipped=%d errs=%v, want 0/2/none", bound2, skipped2, errs2)
	}
}

// A store that unwraps under NEITHER principal is a real failure and must be reported,
// not silently counted as already-done.
func TestRebindReportsAStoreItCannotUnwrap(t *testing.T) {
	EnsureDevKey()
	path := filepath.Join(t.TempDir(), "corrupt.db")

	db, err := Open(Global, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = db.Close()
	// Corrupt the wrapped DEK, leaving the fileID head intact.
	sidecar, err := os.ReadFile(path + dekSuffix)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	sidecar[len(sidecar)-1] ^= 0xff
	if err := os.WriteFile(path+dekSuffix, sidecar, 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	err = Rebind(Global, Org("acme"), path)
	if err == nil {
		t.Fatal("a corrupt sidecar rebound successfully")
	}
	if errors.Is(err, ErrNotBound) {
		t.Fatal("a corrupt sidecar was reported as already-bound — an operator would read that as success")
	}
}
