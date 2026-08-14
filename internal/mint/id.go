// Package mint makes opaque identifiers.
//
// It is a LEAF on purpose. The function is pure, so every layer may use it —
// including the packages the root cloud package itself imports, which cannot
// import cloud back. A minter parked in the framework package would be reachable
// only from above it, and the money packages that sit below would need their own.
package mint

import (
	"crypto/rand"
	"encoding/hex"
)

// ID returns a random identifier under a prefix — "run_3f7a…", sixteen bytes of
// crypto/rand rendered as thirty-two hex characters.
//
// No error. Twenty-six packages each carried their own minter and every one
// returned one, but since Go 1.24 crypto/rand.Read is guaranteed to fill the
// buffer and panics rather than report a short read, so the system-source failure
// they branched on never arrives as a value.
func ID(prefix string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}
