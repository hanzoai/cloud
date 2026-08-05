// Package sqlpool states, once, how cloud pools connections to a SQLite file.
//
// It is one sentence — one connection — and it is a package so that the sentence
// has somewhere to be true. It used to be written at every store that opened a
// database, together with a block of PRAGMAs restating the driver's own defaults;
// fifty-odd copies of a fact is fifty-odd chances for one of them to drift.
//
// The PRAGMAs are gone because hanzoai/sqlite already applies them, on every
// connection, on every backend — see sqlpool_test.go, which asserts exactly that
// and fails if it ever stops being true. What is NOT already applied everywhere
// is the pool cap, so that is what remains here.
package sqlpool

import (
	"database/sql"
	"fmt"

	"github.com/hanzoai/cek"
	"github.com/hanzoai/namespace"
)

// Single caps db at one connection.
//
// These databases are single-writer by construction, and two facts depend on the
// cap rather than merely benefiting from it:
//
//   - A read-modify-write spanning two statements (tracker's per-project issue
//     number, agents' MAX(seq)+1 event allocation) is atomic ONLY because no
//     second connection can interleave. Widen the pool and those become races
//     that a UNIQUE index turns into errors instead of corruption — on a good day.
//   - On the pure-Go codec the file is an envelope decrypted to one plaintext
//     copy; a checkpoint quiesces writers by taking that single connection.
//
// hanzoai/sqlite already caps the pool on the envelope path (envelope.go, at the
// sql.OpenDB) but NOT on the live-libsqlcipher path, which is the one the shipped
// image builds. Until that asymmetry is fixed upstream the cap has to be stated
// by the caller, and this is where cloud states it.
func Single(db *sql.DB) { db.SetMaxOpenConns(1) }

// Open opens the named SYSTEM-namespace database under dir, with the cap already
// applied. It is the one call an app makes to get its store's handle.
//
// It exists because the two lines it replaces were a PAIR that nothing paired.
// Thirty-three stores opened themselves with byte-identical code —
//
//	db, err := cek.Open(namespace.System(), "<app>", dir)
//	if err != nil { return nil, fmt.Errorf("open <app> store: %w", err) }
//	sqlpool.Single(db)
//
// — and the correctness argument above (a two-statement read-modify-write is
// atomic ONLY because no second connection can interleave) rests entirely on the
// third line, which is a separate call the caller has to remember. Thirty-three
// remembered. apps/framework did not: it opens a cek database through an engine
// callback and never capped it, so its DocType store ran uncapped in production.
//
// A rule that lives in thirty-three copies of a prologue holds until someone
// writes a thirty-fourth store; a rule INSIDE the opener is a property of every
// handle that exists. Same argument as cloud.App being the only way to get an app.
//
// The name is the FILE key in the system namespace — the app's own name — never a
// path: these are the deployment's databases, not a tenant's. A per-org store
// comes through OrgDB, which names its owner.
func Open(name, dir string) (*sql.DB, error) {
	db, err := cek.Open(namespace.System(), name, dir)
	if err != nil {
		return nil, fmt.Errorf("open %s store: %w", name, err)
	}
	Single(db)
	return db, nil
}
