// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"bytes"
	"os"
	"testing"

	"github.com/hanzoai/namespace"
)

// sqliteMagic is the 16-byte header every UNENCRYPTED SQLite file starts with. Its
// presence is the plaintext test: an encrypted store's first page is ciphertext.
var sqliteMagic = []byte("SQLite format 3\x00")

// The identity store must not be readable off the volume. This asserts the property
// on the BYTES, not on which function was called — the previous code opened through
// orm directly and left `SQLite format 3` at offset 0, so a test that only checked
// "did we call the right opener" would have passed before the bug and after it alike.
func TestStoreIsEncryptedAtRest(t *testing.T) {
	dir := t.TempDir()

	_, conn, err := openStore(dir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	// CLOSE before reading: on the pure-Go codec the database is written back at
	// close, so an open store has not yet landed its bytes.
	if err := conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	path, err := namespace.Path(dir, namespace.System(), storeSubsystem)
	if err != nil {
		t.Fatalf("namespace.Path: %v", err)
	}
	head, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the iam store does not exist at %s: %v", path, err)
	}
	if len(head) >= len(sqliteMagic) && bytes.Equal(head[:len(sqliteMagic)], sqliteMagic) {
		t.Fatalf("iam store is PLAINTEXT at rest: %q begins with the unencrypted SQLite magic — every identity, org membership and credential record is readable from a lifted volume", path)
	}
}

// The ORM must actually work over the keyed handle: an encrypted store that cannot
// hold a record would trade a security bug for an outage. initSchema running inside
// AdaptSQLDB is what this proves.
func TestStoreIsUsableAfterEncryptedOpen(t *testing.T) {
	db, conn, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if db == nil {
		t.Fatal("openStore returned a nil orm.DB")
	}
}
