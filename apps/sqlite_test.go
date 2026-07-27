// One SQLite engine, reachable under the names that engine owns.
//
// database/sql is a global registry: two packages registering the same name
// panic the process at init ("sql: Register called twice for driver sqlite"),
// before main() runs and before a single request is served. That panic has
// already taken this binary down once — fourteen stores blank-imported
// modernc.org/sqlite while hanzoai/sqlite registered "sqlite" too (dba5d73b).
// The names collide in pairs: hanzoai/sqlite and modernc.org/sqlite both take
// "sqlite"; hanzoai/csqlite and mattn/go-sqlite3 both take "sqlite3". One blank
// import re-arms it.
//
// The guard lives here because apps is the composition root: cmd/cloud's main
// imports nothing but this package and the root cloud package, so this test
// binary's first-party graph is the shipped binary's less that one main.
// Whatever registers a driver in production registers here, and the gate
// already runs ./apps/ — so a second engine fails CI instead of a pod.
//
// The observable is the registered name -> owning package map, read back through
// reflection on the driver database/sql actually holds. It is closed by
// construction: an engine is caught whether it collides on a name, takes a fresh
// name, or hides behind a fork of a name already allowed.
//
// HIP-0106: one binary, many subsystems.
package apps

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
)

// sqliteEngines are the packages allowed to own a registered SQLite driver — one
// per build tag, both reached through the github.com/hanzoai/sqlite facade that
// every cloud store imports. Nothing else may appear.
var sqliteEngines = map[string]string{
	"github.com/hanzoai/csqlite": "cgo: the C engine behind hanzoai/sqlite, linked against libsqlcipher for at-rest encryption",

	// cgo-free: hanzoai/sqlite has no engine of its own on this path — driver_nocgo.go
	// blank-imports modernc, which registers "sqlite" in its own init, so modernc IS
	// the fork's pure-Go backend and owns the name. It is reached ONLY through the
	// fork (nothing here imports it directly), and at-rest encryption is not lost by
	// its being unkeyed: cek wraps it in the pure-Go SQLCipher envelope, which reads
	// and writes the same page format as the C codec.
	"modernc.org/sqlite": "cgo-free: the engine behind hanzoai/sqlite's !cgo backend, encrypted at rest by cek's SQLCipher envelope",
}

// sqliteNames are the driver names that engine may answer to. "sqlite" is
// required; "sqlite3" is optional because only the cgo backend registers it.
var sqliteNames = map[string]string{
	"sqlite": "registered by hanzoai/sqlite under both build tags; the name every cloud store opens",

	"sqlite3": "registered by hanzoai/csqlite's own init, same engine as \"sqlite\"; also the name mattn/go-sqlite3 takes",
}

// sqliteRegistrations maps every SQLite-family driver name registered in this
// binary to the package that owns it. A name is in the family if the name looks
// like SQLite or its owning package does, so an engine cannot slip past by
// picking a plain name.
//
// Owner resolution goes through sql.Open, which is lazy — it hands back the
// registered driver without connecting. A driver that refuses an empty DSN and is
// not SQLite-shaped by name is logged and skipped: it cannot be identified, and
// no SQLite engine in this tree behaves that way.
func sqliteRegistrations(t *testing.T) map[string]string {
	t.Helper()
	family := func(s string) bool {
		s = strings.ToLower(s)
		return strings.Contains(s, "sqlite") || strings.Contains(s, "sqlcipher")
	}
	reg := map[string]string{}
	for _, name := range sql.Drivers() {
		db, err := sql.Open(name, "")
		if err != nil {
			if family(name) {
				t.Errorf("driver %q is registered but its owner cannot be read: %v", name, err)
			} else {
				t.Logf("driver %q: owner not readable (%v); not SQLite-shaped by name", name, err)
			}
			continue
		}
		rt := reflect.TypeOf(db.Driver())
		_ = db.Close()
		for rt.Kind() == reflect.Pointer {
			rt = rt.Elem()
		}
		if pkg := rt.PkgPath(); family(name) || family(pkg) {
			reg[name] = pkg
		}
	}
	return reg
}

// TestSQLiteOneEngine fails if the binary links a second SQLite engine.
func TestSQLiteOneEngine(t *testing.T) {
	reg := sqliteRegistrations(t)
	if len(reg) == 0 {
		t.Fatal(`no SQLite driver registered: every cloud store opens "sqlite", so the composition root must carry github.com/hanzoai/sqlite`)
	}

	owners := map[string][]string{}
	for name, pkg := range reg {
		owners[pkg] = append(owners[pkg], name)
	}
	if len(owners) > 1 {
		t.Errorf("cloud links %d SQLite engines, not one: %v.\n"+
			"Two engines mean two on-disk behaviours, and the moment either takes a name the other holds, this binary panics at init.",
			len(owners), owners)
	}
	for pkg, held := range owners {
		if _, ok := sqliteEngines[pkg]; !ok {
			t.Errorf("%s owns SQLite driver name(s) %v but is not an allowed engine.\n"+
				"Open \"sqlite\" through github.com/hanzoai/sqlite instead, or add %s to sqliteEngines here with the reason it must be its own engine.",
				pkg, held, pkg)
		}
	}
}

// TestSQLiteDriverNames fails if the engine answers to a name outside the allowed
// set, which is how a collision with an upstream driver gets built.
func TestSQLiteDriverNames(t *testing.T) {
	reg := sqliteRegistrations(t)
	if _, ok := reg["sqlite"]; !ok {
		t.Errorf("driver \"sqlite\" is not registered; registered SQLite names: %v", reg)
	}
	for name := range reg {
		if _, ok := sqliteNames[name]; !ok {
			t.Errorf("SQLite driver name %q is registered but not allowed.\n"+
				"Every extra name is a name an upstream driver can no longer take without panicking this binary.\n"+
				"Drop the registration, or add %q to sqliteNames here with the reason it must exist.",
				name, name)
		}
	}
}
