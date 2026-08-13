// Package codec answers whether a test may stand down because this build links no
// SQLCipher codec.
package codec

import (
	"os"
	"testing"

	sqlitedrv "github.com/hanzoai/sqlite"
)

// Require skips the test when the codec is absent, naming why in the caller's own
// words — unless the build asked to be held to the shipped engine, in which case an
// absent codec is a FAILURE.
//
// SQLITE_REQUIRE_CODEC=1 is that request. `make test-codec` and the Dockerfile both
// set it, and the Makefile says the target "either exercises the shipped engine or
// says it cannot". It did not say so: the variable is read by hanzoai/sqlite's own
// suite and by nothing here, so on a machine whose -lsqlite3 is plain SQLite every
// storage test skipped and the target reported success — the one shape that is
// indistinguishable from having run.
func Require(t *testing.T, why string) {
	t.Helper()
	if sqlitedrv.CodecLinked() {
		return
	}
	if os.Getenv("SQLITE_REQUIRE_CODEC") == "1" {
		t.Fatalf("SQLITE_REQUIRE_CODEC=1 but no codec is linked, so this cannot be exercised: %s", why)
	}
	t.Skip("no codec linked: " + why)
}
