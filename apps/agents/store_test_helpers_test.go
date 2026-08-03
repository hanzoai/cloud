package agents

import (
	"database/sql"
	"testing"

	"github.com/hanzoai/cek"
	"github.com/hanzoai/cloud"

	// devmaster keys this test binary: cek opens nothing without a master and a
	// test process has no KMS.
	_ "github.com/hanzoai/cloud/internal/devmaster"
	"github.com/hanzoai/cloud/sqlpool"
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
	sqlpool.Single(db)
	st, err := openStore(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return st, nil
}

// rawAt opens the database behind a store dir without the agents schema, for
// tests that plant a legacy table or read one back.
func rawAt(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := cek.Open(namespace.System(), legacySubsystem, dir)
	if err != nil {
		t.Fatalf("open raw %s: %v", dir, err)
	}
	sqlpool.Single(db)
	return db
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
