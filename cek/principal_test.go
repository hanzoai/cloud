// Copyright © 2026 Hanzo AI. MIT License.

package cek

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	sqlitedrv "github.com/hanzoai/sqlite"
)

// legacyGlobalDerivation is the derivation every store on disk today was written
// under, spelled out independently of the code being tested: type "global", id =
// hex(fileID). It is the reference the platform partition must still match, so that
// adding the principal cannot silently orphan the existing fleet.
func legacyGlobalDerivation(master, fileID []byte) (kek, aad []byte, err error) {
	id := hex.EncodeToString(fileID)
	kek, err = sqlitedrv.DeriveKey(master, sqlitedrv.PrincipalGlobal, id)
	if err != nil {
		return nil, nil, err
	}
	return kek, sqlitedrv.PrincipalAAD(sqlitedrv.PrincipalGlobal, id), nil
}

// The property the principal exists for: a store belongs to its owner, and carrying it
// into another tenant's directory must not open it.
//
// This is what the old derivation could not do. Every store keyed under the single tag
// "global", so the key knew nothing about the owner and a {db,.dek} pair moved between
// orgs opened perfectly — confidentiality held, but nothing bound a file to its tenant.
func TestStoreDoesNotOpenUnderAnotherOrg(t *testing.T) {
	EnsureDevKey()
	dir := t.TempDir()
	path := filepath.Join(dir, "finance.db")

	db, err := Open(Org("acme"), path)
	if err != nil {
		t.Fatalf("open as acme: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE ledger(v TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO ledger VALUES('acme-money')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	_ = db.Close()

	// The same bytes, claimed by a different tenant. The wrapped DEK is bound to acme
	// through both the KEK and the GCM AAD, so this must fail rather than open.
	if other, err := Open(Org("evil"), path); err == nil {
		_ = other.Close()
		t.Fatal("acme's store opened under org 'evil' — the key is not bound to its owner, so a {db,.dek} pair is portable across tenants")
	}

	// And the owner must still be able to open it: binding that also locks out the
	// rightful tenant is not isolation, it is data loss.
	back, err := Open(Org("acme"), path)
	if err != nil {
		t.Fatalf("acme can no longer open its own store: %v", err)
	}
	defer back.Close()
	var got string
	if err := back.QueryRow(`SELECT v FROM ledger`).Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got != "acme-money" {
		t.Fatalf("read %q", got)
	}
}

// A tenant store must not open under the platform principal either — otherwise any
// platform-scoped opener would be a skeleton key over every org.
func TestOrgStoreDoesNotOpenAsGlobal(t *testing.T) {
	EnsureDevKey()
	path := filepath.Join(t.TempDir(), "tracker.db")

	db, err := Open(Org("acme"), path)
	if err != nil {
		t.Fatalf("open as acme: %v", err)
	}
	_ = db.Close()

	if g, err := Open(Global, path); err == nil {
		_ = g.Close()
		t.Fatal("a tenant store opened under the platform principal — Global would be a skeleton key")
	}
}

// The platform partition keeps deriving from the file id alone. This is not cosmetic:
// every platform store already on disk was written that way, and a Global open must
// still be the same derivation or the whole fleet's stores stop opening.
func TestGlobalDerivationIsUnchanged(t *testing.T) {
	EnsureDevKey()
	master, err := resolveMaster()
	if err != nil {
		t.Fatalf("master: %v", err)
	}
	fileID := make([]byte, fileIDLen)
	for i := range fileID {
		fileID[i] = byte(i)
	}

	kek, aad, err := deriveFor(Global, master, fileID)
	if err != nil {
		t.Fatalf("deriveFor: %v", err)
	}
	// The pre-principal derivation: type "global", id = hex(fileID), nothing else.
	wantKEK, wantAAD, err := legacyGlobalDerivation(master, fileID)
	if err != nil {
		t.Fatalf("reference derivation: %v", err)
	}
	if string(kek) != string(wantKEK) {
		t.Fatal("Global no longer derives the KEK every existing platform store was written under — those stores would stop opening")
	}
	if string(aad) != string(wantAAD) {
		t.Fatal("Global's wrap AAD changed — existing platform sidecars would fail their GCM tag check")
	}
}

// Two orgs must never share a key for the same file id.
func TestDistinctOrgsDeriveDistinctKeys(t *testing.T) {
	EnsureDevKey()
	master, err := resolveMaster()
	if err != nil {
		t.Fatalf("master: %v", err)
	}
	fileID := make([]byte, fileIDLen)

	a, _, err := deriveFor(Org("acme"), master, fileID)
	if err != nil {
		t.Fatalf("acme: %v", err)
	}
	b, _, err := deriveFor(Org("beta"), master, fileID)
	if err != nil {
		t.Fatalf("beta: %v", err)
	}
	if string(a) == string(b) {
		t.Fatal("two orgs derived the same KEK for the same file id")
	}
}

// A store must survive a move, which is only true while the path stays out of the
// derivation. The owner is in the key; the location is not.
func TestOwnerBoundStoreStillSurvivesRename(t *testing.T) {
	EnsureDevKey()
	dir := t.TempDir()
	from := filepath.Join(dir, "a.db")

	db, err := Open(Org("acme"), from)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE t(v TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = db.Close()

	to := filepath.Join(dir, "b.db")
	for _, sfx := range []string{"", ".dek"} {
		if err := os.Rename(from+sfx, to+sfx); err != nil && !os.IsNotExist(err) {
			t.Fatalf("rename: %v", err)
		}
	}

	moved, err := Open(Org("acme"), to)
	if err != nil {
		t.Fatalf("owner-bound store did not survive a rename — the path leaked into the derivation: %v", err)
	}
	_ = moved.Close()
}
