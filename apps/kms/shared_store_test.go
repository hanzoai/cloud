package kms

import (
	"testing"

	sqlitedrv "github.com/hanzoai/sqlite"
)

// requireSharedStore skips a test that needs two handles on ONE store file to see
// each other — the property that makes a per-org SQLite store horizontally scalable.
//
// Without the live libsqlcipher codec, cek falls back to the pure-Go envelope, which
// decrypts the file into a handle-private RAM copy and seals it on close. Two opens
// of one path never observe each other and the last close wins; envelope.go states
// this as a design constraint ("SINGLE WRITER per file"). So on this build the store
// really is not shareable, and the guard below would fail for that reason rather
// than because a global OS-locked store crept back in — the regression it exists to
// catch.
//
// The shipped image links the codec and keeps the database in place, with per-commit
// durability and ordinary SQLite multi-opener semantics, so the property holds there.
// `make test` runs CGO_ENABLED=0, so no CI job exercises that build today.
func requireSharedStore(t *testing.T) {
	t.Helper()
	if !sqlitedrv.CodecLinked() {
		t.Skip("pure-Go envelope: one store is single-writer (handle-private RAM copy, sealed on close); the no-exclusive-lock property belongs to the codec-linked build")
	}
}
