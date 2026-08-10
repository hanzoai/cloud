// Copyright © 2026 Hanzo AI. MIT License.

package planetest

// dir.go — a directory a plane socket can actually bind in.

import (
	"os"
	"testing"
)

// Dir is a runtime directory for ZIP_RUNTIME_DIR, short enough to bind a socket in.
//
// t.TempDir CANNOT be used for this and the reason is not obvious: a unix socket
// address is 104 bytes on Darwin (108 on Linux), t.TempDir spends most of them on
// the TEST'S OWN NAME, and the remainder has to hold "/<app>.sock". So a test whose
// name is a sentence — which the good ones here are — fails to bind with EINVAL,
// reported as "bind: invalid argument", and a test that passes today breaks when a
// test is RENAMED. It fails only on a Mac, so CI is green while every developer's
// machine reports a dozen packages red.
//
// This is one function rather than a copy per package because it was three copies,
// each spelled a little differently, and the packages WITHOUT one are exactly the
// ones that were failing.
func Dir(t *testing.T) string {
	t.Helper()
	// "" is TMPDIR, which is itself long on Darwin — the short prefix is what buys
	// the headroom, so keep it short.
	dir, err := os.MkdirTemp("", "pl")
	if err != nil {
		t.Fatalf("plane runtime dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
