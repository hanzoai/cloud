// Copyright © 2026 Hanzo AI. MIT License.

package planetest

// dir.go — a directory a plane socket can actually bind in.

import (
	"os"
	"testing"
)

// Dir is a runtime directory for ZIP_RUNTIME_DIR, short enough to bind a socket
// in. t.TempDir cannot be used: a unix socket address is 104 bytes on Darwin and
// t.TempDir spends most of them on the TEST'S OWN NAME, so a descriptive name
// fails to bind with EINVAL, reported as "bind: invalid argument" — green on
// Linux, red on every Mac, and broken by RENAMING a test. One helper, because it
// was three copies spelled differently and the packages without one were the
// ones failing.
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
