// Package migratetest is the shared regression harness for one production bug
// class: a store's migrate() creates an index over a column that a pre-existing
// (legacy) table lacks — because the column is ALTER-added later — so
// CREATE INDEX fails "no such column", migrate() fails, mount fails, and the pod
// crashloops on deploy.
//
// A Case seeds one store's REAL pre-migration schema into a fresh database, then
// runs that store's REAL open+migrate over it and asserts it succeeds and is
// idempotent — proving migration-correctness as a pure test over schema epochs
// instead of discovering it at production boot. Because each clients/* store's
// migrate() is unexported, the store's own package wires the Open closure; the
// assertion lives here, once, so every store shares ONE correctness check.
package migratetest

import (
	"io"
	"testing"

	"github.com/hanzoai/cloud/cek"
)

// Case is one store's legacy-schema migration regression.
//
//   - Name      names the store (temp-file + failure label).
//   - LegacyDDL is the store's table(s) EXACTLY as a pre-migration production DB
//     carries them — i.e. WITHOUT the columns the store later ALTER-adds. This is
//     the shape over which an index-before-column ordering bug fails.
//   - Open opens a store at path and runs its migrate() (the caller's
//     package-local openStore), returning the store as an io.Closer. It is called
//     AFTER the harness seeds LegacyDDL at that same path, so it exercises the
//     exact boot path that crashloops on a legacy DB.
//   - Probe, if set, runs one write after a successful migrate to prove the
//     forward-added columns (and their now-safe indexes) are usable.
type Case struct {
	Name      string
	LegacyDDL string
	Open      func(path string) (io.Closer, error)
	Probe     func(t *testing.T, store io.Closer)
}

// Run seeds LegacyDDL, migrates over it, optionally probes, then re-opens to
// prove migrate() is idempotent (a warm restart must not fail).
func (c Case) Run(t *testing.T) {
	t.Helper()
	path := t.TempDir() + "/" + c.Name + ".db"

	// Stand up the legacy schema exactly as a pre-migration prod DB has it, using
	// the SAME driver the store opens with.
	raw, err := cek.Open(path)
	if err != nil {
		t.Fatalf("%s: open legacy db: %v", c.Name, err)
	}
	if _, err := raw.Exec(c.LegacyDDL); err != nil {
		_ = raw.Close()
		t.Fatalf("%s: seed legacy schema: %v", c.Name, err)
	}
	_ = raw.Close()

	// The boot path that crashloops when an index precedes its ALTER-added column.
	store, err := c.Open(path)
	if err != nil {
		t.Fatalf("%s: migrate over legacy schema: %v", c.Name, err)
	}
	if c.Probe != nil {
		c.Probe(t, store)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("%s: close: %v", c.Name, err)
	}

	// Re-open the SAME db: migrate() must be idempotent (ALTERs no-op via
	// duplicate-column, indexes via IF NOT EXISTS) with no error on a warm restart.
	store2, err := c.Open(path)
	if err != nil {
		t.Fatalf("%s: re-migrate (idempotency): %v", c.Name, err)
	}
	_ = store2.Close()
}
