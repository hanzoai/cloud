package kms

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
)

// TestDBFor_TenantCannotSpellReservedPartition pins the defense-in-depth the red
// team flagged: a tenant path literally spelling "/orgs/_platform/…" must never
// be routed to the deployment's own partition. The facade is reachable ONLY by
// the empty/non-"/orgs" route, and this holds whatever the org was called. It is
// true by KIND — the facade is the system namespace and a
// tenant is an org namespace — rather than by an argument about which runes a
// slugger emits.
func TestDBFor_TenantCannotSpellReservedPartition(t *testing.T) {
	s := newSecretStore(cloud.Base{DataDir: t.TempDir()}, false)
	// The facade partition is chosen by the boolean, never by a tenant org string.
	_, facade := fileOrg("/orgs/_platform/secrets/x")
	if facade {
		t.Fatal("a tenant path /orgs/_platform routed to the facade partition — must not")
	}
	if _, f := fileOrg("/facade-secret"); !f {
		t.Fatal("a non-/orgs path must route to the facade partition")
	}
	// The tenant _platform path opens a DISTINCT store (not PlatformDB) and its DB
	// pointer differs from the real facade store — proof they never alias.
	tenantDB, err := s.dbFor("/orgs/_platform/secrets/x", true)
	if err != nil || tenantDB == nil {
		t.Fatalf("tenant _platform path must open its own store: db=%v err=%v", tenantDB, err)
	}
	facadeDB, err := s.dbFor("/facade-secret", true)
	if err != nil || facadeDB == nil {
		t.Fatalf("facade route must open PlatformDB: db=%v err=%v", facadeDB, err)
	}
	if tenantDB == facadeDB {
		t.Fatal("tenant _platform store and the facade store are the SAME handle — they must never alias")
	}
}

// TestAStatementDoesNotWaitForeverOnTheSoleConnection pins the bound that turns
// a busy store into an error instead of a hang.
//
// cloud.OrgDB pins each org file to ONE connection, so a second statement waits
// for the first to finish — and database/sql waits until its CONTEXT is done.
// These methods used db.QueryRow/Exec/Query, which carry no context, so the wait
// had no ceiling of its own. The only one left was the caller's: cmd/cloud gives
// up on a plugin at fifteen minutes, and live reads were measured completing at
// duration_ms 900002 — that cap, not the work. Nothing waits that long, so the
// answer arrived for nobody.
//
// The facade partition is deliberately the subject: a path outside orgs/<slug>/
// is namespace.System(), so every platform credential CI reads shares this one
// file and this one connection.
func TestAStatementDoesNotWaitForeverOnTheSoleConnection(t *testing.T) {
	defer func(d time.Duration) { storeOpTimeout = d }(storeOpTimeout)
	storeOpTimeout = 150 * time.Millisecond

	s := newSecretStore(cloud.Base{DataDir: t.TempDir()}, false)
	db, err := s.dbFor("/facade-secret", true)
	if err != nil || db == nil {
		t.Fatalf("open facade store: db=%v err=%v", db, err)
	}

	// Hold the sole connection, exactly as a slow write or a checkpoint would.
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	done := make(chan error, 1)
	go func() { _, e := s.get("/facade-secret", "NAME", "prod"); done <- e }()

	select {
	case e := <-done:
		if !errors.Is(e, context.DeadlineExceeded) {
			t.Fatalf("get returned %v, want a deadline — the wait must end as an error, not as a late answer", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("get is still waiting for the sole connection after 5s — unbounded, so the only ceiling is the caller's fifteen-minute plugin cap")
	}
}
