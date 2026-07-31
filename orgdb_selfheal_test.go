package cloud

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/cek"
)

// A STORE MIGRATES ITSELF ON OPEN. This is the production failure, reproduced.
//
// "a store's key names its owner" changed the sidecar derivation from Global to
// owner-bound. The code that WRITES the new form shipped; the migration for stores
// already written in the old form did not — it existed only as cmd/cek-rewrap, and
// nothing invoked it. So two long-lived stores stopped opening in production:
//
//	OrgDB open "/var/lib/cloud/orgs/hanzo/git.db":  cek: unwrap DEK … message authentication failed
//	OrgDB open "/var/lib/cloud/orgs/hanzo/sync.db": cek: unwrap DEK … message authentication failed
//
// That took the native git plane and the mirror engine down, and with them every
// deploy — including the one that would have carried the migration.
//
// A migration that must be run by hand is a migration that has not shipped. The
// need is discovered at open, so it is answered at open.
func TestOrgStoreMigratesItselfOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "git.db")

	// A store as the OLD world wrote it: created under the legacy Global identity,
	// with real data in it, using only the public API.
	legacy, err := cek.Open(cek.Global, path)
	if err != nil {
		t.Fatalf("create legacy store: %v", err)
	}
	if _, err := legacy.Exec(`CREATE TABLE repo (name TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := legacy.Exec(`INSERT INTO repo VALUES ('universe')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	legacy.Close()

	// Opening under the OWNER is what production does, and what failed.
	db, err := openOrgDB(cek.Org("hanzo"), path)
	if err != nil {
		t.Fatalf("a legacy store did not migrate itself: %v", err)
	}
	defer db.Close()

	// The DATA survived, which is the whole safety argument — a new DEK would leave
	// every page unreadable.
	var name string
	if err := db.QueryRow(`SELECT name FROM repo`).Scan(&name); err != nil {
		t.Fatalf("read after migration: %v", err)
	}
	if name != "universe" {
		t.Fatalf("row = %q, want universe", name)
	}

	// Idempotent: opening again is an ordinary open, not a second migration.
	again, err := openOrgDB(cek.Org("hanzo"), path)
	if err != nil {
		t.Fatalf("second open failed: %v", err)
	}
	again.Close()
}

// A store that opens under NEITHER identity stays an error. A genuinely wrong
// master key or a corrupt sidecar must not be papered over by rewriting a sidecar
// we could not read — the migration is for a legacy wrapping, not a repair for
// anything unreadable.
func TestUnreadableStoreStaysAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.db")

	db, err := cek.Open(cek.Global, path)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	db.Close()

	// Corrupt the sidecar: now neither the owner nor Global can unwrap it.
	sidecar := path + ".dek"
	blob, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	for i := range blob {
		blob[i] ^= 0xff
	}
	if err := os.WriteFile(sidecar, blob, 0o600); err != nil {
		t.Fatalf("corrupt sidecar: %v", err)
	}

	if _, err := openOrgDB(cek.Org("hanzo"), path); err == nil {
		t.Fatal("an unreadable store opened — the failure was papered over")
	}
}
