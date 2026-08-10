// Package devmaster keys a test binary.
//
// cek needs a master key before the first database opens, and it does not go
// looking for one — a process that could not resolve a key opens nothing rather
// than writing plaintext. A production process resolves it through BootMaster,
// from KMS. A test process has no KMS, so it mints its own.
//
// Importing this package for its side effect is how a test binary says that:
//
//	import _ "github.com/hanzoai/cloud/internal/devmaster"
//
// It is stated once here rather than in a TestMain per package, because it is
// one fact about test binaries and not seventy independent decisions.
//
// It does nothing outside a test binary. An import that reached production
// could otherwise key a real deployment with a master that dies with the
// process — every database written under it unreadable on the next boot, and
// nothing on fire until then.
package devmaster

import (
	"testing"

	"github.com/hanzoai/cek"
)

func init() {
	if !testing.Testing() || cek.HasMaster() {
		return
	}
	if _, err := cek.SetDevMaster(); err != nil {
		panic("devmaster: " + err.Error())
	}
}
