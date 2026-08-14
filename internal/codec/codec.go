// Package codec answers whether a test may stand down because this build links no
// SQLCipher C codec.
package codec

import (
	"testing"

	sqlitedrv "github.com/hanzoai/sqlite"
)

// Require skips the test when the C codec is absent, naming why in the caller's
// own words.
//
// ENCRYPTION IS NOT THE REASON. A keyed store is ciphertext on every build —
// hanzoai/sqlite encrypts through the pure-Go SQLCipher codec envelope, and a
// database written by either engine reads back under the other. What the envelope
// does differently is share: it decrypts to a plaintext copy PRIVATE to the handle
// and seals it on close, so a second opener sees the last sealed state rather than
// the live writer, and durability is per-checkpoint rather than per-commit.
//
// So this is for tests whose subject is that sharing — two openers of one store,
// or a reload observing another handle's write. Those properties belong to the C
// codec, which encrypts pages in place inside the pager. So does conversion: cek
// turns a plaintext store into a keyed one with sqlcipher_export, a SQL function
// only the C engine has, so a test that needs cek to publish a store waits on the
// same build.
func Require(t *testing.T, why string) {
	t.Helper()
	if !sqlitedrv.CodecLinked() {
		t.Skip("no C codec linked: " + why)
	}
}
