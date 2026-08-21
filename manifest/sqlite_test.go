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
// mattn/go-sqlite3 and ncruces both register "sqlite3". luxfi/zapdb is not
// SQLite at all — it is listed because it embeds an exclusive OS file lock per
// store, which is the reason apps/kms stopped using it and the reason cloud ran
// replicas=1 while it did.
// modernc.org/sqlite is NOT here, and leaving it out is the rule rather than a
// hole in it. hanzoai/sqlite's driver_nocgo.go (`//go:build !cgo ||
// sqlite_purego`) blank-imports modernc ON PURPOSE — modernc registers "sqlite"
// in its own init, and that registration IS the pure-Go build's driver, the
// "cgo-free through its vendored pure-Go engine" half of the rule go.mod states.
//
// Listing it made this gate fail on its own tree the moment it was written: the
// suite runs CGO_ENABLED=0, which is exactly the build that selects driver_nocgo,
// so `go list -deps` reports modernc for the correct configuration and the check
// called the rule a violation of itself. Measured both ways on this tree —
// CGO_ENABLED=1 links none of it, CGO_ENABLED=0 links three packages.
//
// What remains below are the engines that would be a SECOND registrar. Each
// registers "sqlite3" or "sqlite" from its own init, and database/sql.Register
// panics on a duplicate name before main — so one of these arriving beside
// hanzoai/sqlite is the failure this test exists for.
var forbidden = []string{
	"github.com/mattn/go-sqlite3",
	"github.com/ncruces/go-sqlite3",
	"github.com/glebarez/go-sqlite",
	"crawshaw.io/sqlite",
}

// registrants are the two packages that may call sql.Register("sqlite") — the
// facade's two backends. Exactly one may be linked: the facade's build tags are
// mutually exclusive (driver_cgo.go is `cgo && !sqlite_purego`, driver_nocgo.go
// is `!cgo || sqlite_purego`), so both together means something OTHER than the
// facade dragged one in, which is the duplicate registration this gate exists
// to prevent.
var registrants = []string{
	"github.com/hanzoai/csqlite",
	"modernc.org/sqlite",
}

// namedByNoApp is what a subsystem's SOURCE may never import. It is `forbidden`
// plus the two registrants: modernc in the linked graph is the facade's own
// backend and correct, but an app that NAMES it has reached past the facade to
// an engine, which is how the second registrant arrives in the first place.
var namedByNoApp = append(append([]string{}, forbidden...), registrants...)

// TestOneSQLiteEngine fails when a second engine reaches the binary.
func TestOneSQLiteEngine(t *testing.T) {
	// The whole binary, not this package: `go list -deps` over the commands is
	// what the linker sees, which is the set that decides whether two Register
	// calls run.
	// cmd/ AND plugin/: cmd/cloud is the light host and links no subsystem, so a
	// scan of it alone cannot see an engine an app dragged in — each subsystem is
	// its own binary under plugin/.
	out, err := exec.Command("go", "list", "-deps", "../cmd/...", "../plugin/...").Output()
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

	// The rule this gate is named for: ONE registrant. Which one is a property
	// of the build, so the test asks how many rather than which.
	var present []string
	for _, r := range registrants {
		if linked[r] {
			present = append(present, r)
		}
	}
	if len(present) != 1 {
		t.Errorf("cloud links %d SQLite engines (%v), want exactly 1.\n"+
			"database/sql.Register panics on a duplicate driver name at init, before main, "+
			"so two engines take the whole binary down with a stack naming neither. "+
			"The facade selects one by build tag; a second means something else imported it directly.",
			len(present), present)
	}
	t.Logf("one engine linked: %s", present)
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
		for _, bad := range namedByNoApp {
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
