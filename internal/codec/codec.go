// Package codec answers whether a test may stand down because this build links no
// SQLCipher codec.
package codec

import (
	"testing"

	sqlitedrv "github.com/hanzoai/sqlite"
)

// Require skips the test when no codec is linked, naming why in the caller's own
// words.
//
// The condition is one decision in one place because four tests make it, and it is
// the same decision each time: cek converts a plaintext store in place with
// SQLCipher's sqlcipher_export, a SQL function only the C engine has, so on a build
// without it there is no encrypted store to exercise.
//
// That is a gap in cek, not a property of the code under test — hanzoai/sqlcipher
// encrypts a file in pure Go (EncryptFile), which is what cek should convert with.
// When it does, this helper has nothing left to skip and goes away.
func Require(t *testing.T, why string) {
	t.Helper()
	if !sqlitedrv.CodecLinked() {
		t.Skip("no codec linked: " + why)
	}
}
