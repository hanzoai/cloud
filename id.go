package cloud

import (
	"crypto/rand"
	"encoding/hex"
)

// ID mints a random identifier under a prefix — "run_3f7a…", sixteen bytes of
// crypto/rand rendered as thirty-two hex characters.
//
// ONE minter. Twenty-six packages each carried their own, identical down to the
// byte count, and every one of them returned an error that cannot happen: since
// Go 1.24 crypto/rand.Read is guaranteed to fill the buffer and panics rather
// than report a short read, so the system-source failure a caller was branching
// on never arrives as a value. The error went with the copies, and with it
// twenty-six branches no test could enter.
func ID(prefix string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}
