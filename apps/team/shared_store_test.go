package team

import (
	"testing"

	sqlitedrv "github.com/hanzoai/sqlite"
)

// requireSharedStore skips a test that opens a SECOND handle on a workspace store
// and expects to see writes the first handle committed.
//
// Without the live libsqlcipher codec, cek falls back to the pure-Go envelope: each
// handle decrypts the file into its own RAM copy and seals it back on close, so a
// second handle observes the last sealed state and not the live writer. envelope.go
// documents this as a design constraint ("SINGLE WRITER per file").
//
// The shipped image links the codec, where the database is written in place with
// per-commit durability and an ordinary second opener sees committed rows — so the
// reconnect/reload behaviour under test is real there. `make test-codec` is the
// target that exercises it.
func requireSharedStore(t *testing.T) {
	t.Helper()
	if !sqlitedrv.CodecLinked() {
		t.Skip("pure-Go envelope: a second handle sees the last sealed state, not the live writer; reconnect/reload is a property of the codec-linked build")
	}
}
