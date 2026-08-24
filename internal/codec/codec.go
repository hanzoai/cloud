// Package codec answers whether a test may stand down because this build links no
// SQLCipher C codec.
package codec

import (
	"os"
	"testing"

	sqlitedrv "github.com/hanzoai/sqlite"
)

// RequireEnv is how a RUN declares that it is the shipped engine's lane. Set, standing
// down is the failure rather than the answer.
//
// It has to come from the run and not from a build tag, and that is the same reason the
// Makefile gives for the target that sets it: the tag selects the C engine, but the codec
// is a RUNTIME probe of the libsqlcipher that engine links. A cgo build against plain
// SQLite compiles under the same tag and leaves the probe false, so every test whose
// subject is the shipped storage would skip and the lane would report green having
// exercised nothing.
//
// The name is the one the Makefile and the Dockerfile already say. Both described it as
// turning a stand-down into a failure and no Go code here read it, so the assertion those
// comments promised was never made. This is where it is made.
const RequireEnv = "SQLITE_REQUIRE_CODEC"

// Require skips the test when the C codec is absent, naming why in the caller's
// own words — unless the run declares the shipped engine, where absence is a failure.
//
// ENCRYPTION IS NOT THE REASON. A keyed store is ciphertext on every build —
// hanzoai/sqlite encrypts through the pure-Go SQLCipher codec envelope, and a
// database written by either engine reads back under the other. What the envelope
// does differently is share: it decrypts to a plaintext copy PRIVATE to the handle
// and seals it on close, so a second opener sees the last sealed state rather than
// the live writer, and durability is per-checkpoint rather than per-commit.
//
// So this is for tests whose subject is that sharing — two openers of one store,
// or a reload observing another handle's write, or a ship holding its file still
// with a second handle while it copies it. Those properties belong to the C codec,
// which encrypts pages in place inside the pager. So does conversion: cek turns a
// plaintext store into a keyed one with sqlcipher_export, a SQL function only the C
// engine has, so a test that needs cek to publish a store waits on the same build.
func Require(t *testing.T, why string) {
	t.Helper()
	if sqlitedrv.CodecLinked() {
		return
	}
	if os.Getenv(RequireEnv) == "1" {
		t.Fatalf("%s=1 declares this run the shipped engine's, and no C codec is linked: %s", RequireEnv, why)
	}
	t.Skip("no C codec linked: " + why)
}
