package audit

import (
	"testing"

	sqlitedrv "github.com/hanzoai/sqlite"
)

// requireSharedStore skips a test that needs a SECOND opener to observe the same
// store as the first.
//
// Without the live libsqlcipher codec, cek falls back to the pure-Go envelope:
// it decrypts the file into a handle-private RAM copy and seals it back on close.
// Two opens of one path therefore never see each other's writes and the last close
// wins — envelope.go states this outright ("SINGLE WRITER per file"). A test that
// tampers or reads through a second handle is asserting a property the envelope
// provably does not have, so it would fail for the wrong reason.
//
// The property IS real on the shipped build, which links the codec and keeps the
// database in place with per-commit durability and ordinary SQLite sharing. Note
// that `make test` runs CGO_ENABLED=0, so nothing in CI exercises that path today.
func requireSharedStore(t *testing.T) {
	t.Helper()
	if !sqlitedrv.CodecLinked() {
		t.Skip("pure-Go envelope: a store is single-writer (handle-private RAM copy, sealed on close), so a second opener cannot observe it; this property belongs to the codec-linked build")
	}
}
