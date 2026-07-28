// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/cek"
)

// sqliteMagic is the 16-byte header every UNENCRYPTED SQLite file starts with. Its
// presence is the plaintext test: an encrypted store's first page is ciphertext.
var sqliteMagic = []byte("SQLite format 3\x00")

// The identity store must not be readable off the volume. This asserts the property
// on the BYTES, not on which function was called — the previous code opened through
// orm directly and left `SQLite format 3` at offset 0, so a test that only checked
// "did we call cek" would have passed before the bug and after it alike.
func TestStoreIsEncryptedAtRest(t *testing.T) {
	cek.EnsureDevKey() // pure-Go test build: key the same way a dev binary does

	dir := t.TempDir()
	path := filepath.Join(dir, storeFile)

	db, err := openStore(path)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	head, err := os.ReadFile(path)
	if err != nil {
		// The pure-Go codec envelope keys the file out of band and may not
		// materialize it at this path; the sidecar is then the proof cek owns it.
		if _, serr := os.Stat(path + ".dek"); serr == nil {
			return
		}
		t.Fatalf("neither %s nor its .dek sidecar exists — cek never took ownership of the store: %v", path, err)
	}
	if len(head) >= len(sqliteMagic) && bytes.Equal(head[:len(sqliteMagic)], sqliteMagic) {
		t.Fatalf("iam store is PLAINTEXT at rest: %q begins with the unencrypted SQLite magic — every identity, org membership and credential record is readable from a lifted volume", path)
	}
}

// cek owning the file is what makes the master key load-bearing, so the sidecar must
// exist. Without it the store is not in the envelope at all, whatever its header says.
func TestStoreHasKeySidecar(t *testing.T) {
	cek.EnsureDevKey()

	dir := t.TempDir()
	path := filepath.Join(dir, storeFile)

	db, err := openStore(path)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := os.Stat(path + ".dek"); err != nil {
		t.Fatalf("no wrapped-DEK sidecar beside the iam store — it was not opened through cek: %v", err)
	}
}

// The ORM must actually work over the keyed handle: an encrypted store that cannot
// hold a record would trade a security bug for an outage. initSchema running inside
// AdaptSQLDB is what this proves.
func TestStoreIsUsableAfterEncryptedOpen(t *testing.T) {
	cek.EnsureDevKey()

	path := filepath.Join(t.TempDir(), storeFile)
	db, err := openStore(path)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if db == nil {
		t.Fatal("openStore returned a nil orm.DB")
	}
}
