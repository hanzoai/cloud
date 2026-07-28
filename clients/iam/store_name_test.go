// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/cek"
)

// A store written under the old name must come forward with its CONTENTS, not merely
// with its filename. The rename moves an encrypted file away from the sidecar that
// holds its wrapped DEK unless the sidecar travels too, and that failure would look
// like an empty identity store rather than an error — so the row written before the
// move is the assertion.
func TestLegacyStoreIsAdoptedWithItsData(t *testing.T) {
	cek.EnsureDevKey()
	dir := t.TempDir()

	legacy := filepath.Join(dir, legacyStoreFile)
	db, err := cek.Open(legacy)
	if err != nil {
		t.Fatalf("seed legacy store: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE identities(name TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO identities VALUES('ada')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	_ = db.Close()

	moved, err := adoptLegacyStore(dir)
	if err != nil {
		t.Fatalf("adoptLegacyStore: %v", err)
	}
	if moved != legacyStoreFile {
		t.Fatalf("adopted %q, want %q", moved, legacyStoreFile)
	}

	got, err := openStore(filepath.Join(dir, storeFile))
	if err != nil {
		t.Fatalf("open adopted store: %v", err)
	}
	t.Cleanup(func() { _ = got.Close() })

	// Read through cek at the new name — the identity written before the move must
	// still be there, which is only true if the wrapped DEK came with it.
	conn, err := cek.Open(filepath.Join(dir, storeFile))
	if err != nil {
		t.Fatalf("reopen adopted store: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	var name string
	if err := conn.QueryRow(`SELECT name FROM identities`).Scan(&name); err != nil {
		t.Fatalf("the adopted store lost its data (the sidecar did not travel): %v", err)
	}
	if name != "ada" {
		t.Fatalf("read %q, want ada", name)
	}
}

// A legacy store can be present as its SIDECAR ALONE — the pure-Go codec envelope keys
// the file out of band, so `iam2.db` need not exist while `iam2.db.dek` does. A live
// boot proved that statting the database alone reports "nothing to adopt", abandons the
// real store, and creates an empty one beside it. Presence must be answered the way cek
// answers it.
func TestLegacyStorePresentAsSidecarOnlyIsAdopted(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, legacyStoreFile+".dek"), []byte("wrapped"), 0o600); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}

	moved, err := adoptLegacyStore(dir)
	if err != nil {
		t.Fatalf("adoptLegacyStore: %v", err)
	}
	if moved != legacyStoreFile {
		t.Fatalf("a sidecar-only legacy store was not adopted (it would be silently abandoned and replaced by an empty store); moved=%q", moved)
	}
	if _, err := os.Stat(filepath.Join(dir, storeFile+".dek")); err != nil {
		t.Fatalf("the wrapped DEK did not arrive at the current name: %v", err)
	}
}

// Nothing to adopt is the normal path and must be silent, not an error.
func TestAdoptIsANoOpWithoutALegacyStore(t *testing.T) {
	moved, err := adoptLegacyStore(t.TempDir())
	if err != nil {
		t.Fatalf("adopt on a fresh dir: %v", err)
	}
	if moved != "" {
		t.Fatalf("adopted %q from an empty dir", moved)
	}
}

// NEGATIVE, and the one that matters: two identity stores side by side must stop the
// boot. Choosing either silently serves a set of identities nobody selected, and
// overwriting destroys the other.
func TestAdoptRefusesWhenBothNamesExist(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{legacyStoreFile, storeFile} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	if _, err := adoptLegacyStore(dir); err == nil {
		t.Fatal("adopt silently picked one of two identity stores")
	}

	// And it must not have destroyed either one while refusing.
	for _, name := range []string{legacyStoreFile, storeFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s was removed by a refused adopt: %v", name, err)
		}
	}
}

// The name carries no version number. This is the rule being enforced, so it is worth
// stating as a test rather than leaving to review.
func TestStoreNameCarriesNoVersion(t *testing.T) {
	for _, r := range storeFile {
		if r >= '0' && r <= '9' {
			t.Fatalf("store filename %q contains a digit — a version in a filename is a migration waiting to be mistaken for an identity", storeFile)
		}
	}
}
