package host

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain runs this package's tests from the module root. Many of them are
// gates over the whole tree (apps/, plugin/, client/, the source of this
// package itself) and name what they read by its path from the root, the
// one place those paths mean the same thing to a reader and to the test.
func TestMain(m *testing.M) {
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			fmt.Fprintln(os.Stderr, "no go.mod above the package")
			os.Exit(1)
		}
		dir = parent
	}
	if err := os.Chdir(dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// pkgDir is this package's directory, from the module root the tests run in.
const pkgDir = "internal/host"
