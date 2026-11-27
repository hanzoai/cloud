package agents

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cek"

	// devmaster keys this test binary: cek opens nothing without a master and a
	// test process has no KMS.
	_ "github.com/hanzoai/cloud/sdk/kit/master"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
)

// openStoreAt opens the agents store under an explicit dir, standing in for what
// cloud.OrgDB does for a real per-org file: open the named database, then hand
// the *sql.DB to openStore for migration. Only the migration tests care WHERE the
// file is; everything else wants testStore.
func openStoreAt(dir string) (*Store, error) {
	db, err := cek.Open(namespace.System(), legacySubsystem, dir)
	if err != nil {
		return nil, err
	}
	locus.Single(db)
	st, err := openStore(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return st, nil
}

// migrate runs one step of a store's migration the way an open does, in the
// migration's one transaction under the store's lock.
func migrate(st *Store, step func(ctx context.Context, tx *sql.Tx) error) error {
	ctx := context.Background()
	return st.d.Migrate(ctx, st.db, func(tx *sql.Tx) error { return step(ctx, tx) })
}

// rawAt opens the database behind a store dir without the agents schema, for
// tests that plant a legacy table or read one back.
func rawAt(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := cek.Open(namespace.System(), legacySubsystem, dir)
	if err != nil {
		t.Fatalf("open raw %s: %v", dir, err)
	}
	locus.Single(db)
	return db
}

// storeSeq numbers the orgs openStoreOn makes, so two stores opened in one test are two
// stores on either engine, as two temp dirs were two files.
var storeSeq atomic.Int64

// openStoreOn opens a fresh store on the engine the running test is on: an encrypted
// SQLite file in a temp dir, or a schema on the throwaway PostgreSQL sqltest.Engines
// points CLOUD_SQL_DSN at. Outside sqltest.Engines that variable is unset, so it is the
// file.
func openStoreOn(t *testing.T) *Store {
	t.Helper()
	ns := namespace.MustOrg(fmt.Sprintf("test%d", storeSeq.Add(1)))
	db, err := locus.Open(ns, "agents", t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st, err := openStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// testStores is the per-org store set Mount builds, over a throwaway data dir.
// A test that reaches storage through it exercises the REAL resolution path
// (org → namespace → file → key), not a hand-placed handle, so an isolation
// assertion is a statement about the shipped code.
func testStores(t *testing.T) *cloud.OrgStore[*Store] {
	t.Helper()
	c := cloud.NewOrgStore[*Store](cloud.Base{DataDir: t.TempDir()}, "agents", openStore)
	t.Cleanup(func() { _ = c.CloseAll() })
	return c
}

// storeOf is one org's file inside a mounted state — what a test reaches for
// when it plants or reads a row directly instead of going over HTTP.
func storeOf(t *testing.T, st *state, org string) *Store {
	t.Helper()
	sto, err := st.storeFor(org)
	if err != nil {
		t.Fatalf("storeFor %q: %v", org, err)
	}
	return sto
}

// nobodys reads the runs no person asked for — a schedule's, a migrated row's —
// which is what the store-level tests insert: the scope a SuperAdmin reads them by.
var nobodys = runScope{ownerless: true}
