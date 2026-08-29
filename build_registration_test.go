package cloud_test

import (
	"testing"

	"github.com/hanzoai/cloud"
)

// TestMountFunc_IsTheSubsystemSignature pins the registry's mount contract: the
// signature every subsystem exports IS a cloud.UseFunc, checked by the compiler.
//
// This file used to test cloud.Typed, the adapter that took a UseFunc's `any`
// app and asserted it back to *zip.App. Both of its tests went with it, and
// neither is a loss:
//
//   - "Typed recovers the *zip.App" only ever proved the adapter handed through
//     the value it was given. UseFunc now names *zip.App, so there is no
//     recovery step left to get wrong.
//   - "Typed fails closed on a wrong type" can no longer be written: passing
//     "not-a-zip-app" to a UseFunc is a compile error, so the runtime branch it
//     exercised does not exist. A test asserting a wrong type is rejected is
//     precisely what a type already is.
//
// What remains is the only claim worth making, and the build enforces it.
func TestMountFunc_IsTheSubsystemSignature(t *testing.T) {
	var _ cloud.UseFunc = func(cloud.Router, cloud.Deps) error { return nil }
}
