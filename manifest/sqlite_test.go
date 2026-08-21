package manifest_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ONE SQLITE ENGINE, AND THE GATE THAT KEEPS IT ONE.
//
// `github.com/hanzoai/sqlite` is the only engine cloud links. It registers the
// driver name "sqlite" under both build tags — cgo through hanzoai/csqlite
// against libsqlcipher, cgo-free through its vendored pure-Go engine and the
// hanzoai/sqlcipher codec VFS — so every store imports that facade and never an
// engine.
//
// The rule is not a style preference. `database/sql.Register` PANICS on a
// duplicate driver name, and it runs at init, before main: a second package
// registering "sqlite3" takes the process down at startup, everywhere at once,
// with a stack that names neither offender's import path. A dependency added for
// one subsystem stops the whole binary.
//
// go.mod's comment has named this file as the gate for some time; the file is
// what makes the claim true. It reads the module graph rather than the import
// statements, because the panic is caused by a package being LINKED, and a
// transitive dependency links just as hard as a direct one.

// forbidden are the engines that register a driver name cloud already owns, or
// that would put a second SQLite implementation in the binary.
//
// mattn/go-sqlite3 and modernc.org/sqlite both register "sqlite3"; ncruces
// registers "sqlite3" as well. luxfi/zapdb is not SQLite at all — it is listed
// because it embeds an exclusive OS file lock per store, which is the reason
// apps/kms stopped using it and the reason cloud ran replicas=1 while it did.
var forbidden = []string{
	"github.com/mattn/go-sqlite3",
	"modernc.org/sqlite",
	"github.com/ncruces/go-sqlite3",
	"github.com/glebarez/go-sqlite",
	"crawshaw.io/sqlite",
}

// TestOneSQLiteEngine fails when a second engine reaches the binary.
func TestOneSQLiteEngine(t *testing.T) {
	// The whole binary, not this package: `go list -deps` over the commands is
	// what the linker sees, which is the set that decides whether two Register
	// calls run.
	out, err := exec.Command("go", "list", "-deps", "../cmd/...").Output()
	if err != nil {
		t.Skipf("go list unavailable in this environment: %v", err)
	}
	linked := make(map[string]bool)
	for _, p := range strings.Split(string(out), "\n") {
		linked[strings.TrimSpace(p)] = true
	}
	if len(linked) < 100 {
		t.Fatalf("go list returned %d packages, which is too few to be the real graph", len(linked))
	}

	for _, bad := range forbidden {
		for p := range linked {
			if p == bad || strings.HasPrefix(p, bad+"/") {
				t.Errorf("%s is linked into cloud.\n"+
					"It registers a driver name hanzoai/sqlite already owns, and "+
					"database/sql.Register panics on a duplicate at init — before main, "+
					"taking the whole binary down rather than the one subsystem that "+
					"added it. Use github.com/hanzoai/sqlite.", bad)
			}
		}
	}

	if !linked["github.com/hanzoai/sqlite"] {
		t.Error("github.com/hanzoai/sqlite is not linked; it is the facade every store is supposed to open through")
	}
}

// TestNoAppImportsAnEngineDirectly is the same rule read at the source, so a
// violation names the file that introduced it rather than only the module.
func TestNoAppImportsAnEngineDirectly(t *testing.T) {
	// apps/ is the sibling this rule is about. The gate lives in manifest/
	// because apps/ holds no Go file of its own — the composition root folded
	// into the generated manifest — and a directory of only _test.go does not
	// build.
	root, err := filepath.Abs("../apps")
	if err != nil {
		t.Fatal(err)
	}
	var found int
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		// This file names every engine in order to refuse it, so scanning itself
		// reports the list as five violations.
		if filepath.Base(path) == "sqlite_test.go" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(b)
		for _, bad := range forbidden {
			if strings.Contains(src, `"`+bad+`"`) {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("apps/%s imports %s directly; open through github.com/hanzoai/sqlite", rel, bad)
			}
		}
		found++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Fatal("walked apps/ and read no Go files, so this gate proved nothing")
	}
}
