// Package locus is the one way to open a per-org store.
//
// A store is a namespace (github.com/hanzoai/namespace) times a subsystem, kept
// under a data directory. By default it is an encrypted SQLite file there, one
// connection wide, on the SDK's own engine (hanzoai/cek over hanzoai/sqlite): a
// laptop needs no configuration. A deployment that names a server in the
// environment (Env) links that server's driver, which registers an [Engine], and
// the same calls then answer a handle on the server, and move a file there once.
// Nothing here links a server driver.
package locus

import (
	"context"
	"database/sql"
	"fmt"
	"io"

	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cek"
	"github.com/hanzoai/namespace"
	luxlog "github.com/luxfi/log"
)

// Env names the server the stores live on, a URL its driver reads. Unset, every
// store is an embedded SQLite file.
const Env = "CLOUD_SQL_DSN"

// DSN is the server this process keeps its stores on, "" when it keeps files.
func DSN() string { return env.Or(Env, "") }

// Single caps db at one connection.
//
// These databases are single-writer by construction, and two facts depend on the
// cap rather than merely benefiting from it:
//
//   - A read-modify-write spanning two statements is atomic ONLY because no second
//     connection can interleave. Widen the pool and those become races.
//   - On the pure-Go codec the file is an envelope decrypted to one plaintext copy;
//     a checkpoint quiesces writers by taking that single connection.
//
// hanzoai/sqlite caps the pool on the envelope path but NOT on the live-libsqlcipher
// path, so the cap has to be stated by the caller, and this is where it is.
func Single(db *sql.DB) { db.SetMaxOpenConns(1) }

// System opens the named SYSTEM-namespace database under dir, with the cap already
// applied: the deployment's own database, not a tenant's. The name is the file key
// in the system namespace, never a path. It is always a file.
func System(name, dir string) (*sql.DB, error) {
	db, err := cek.Open(namespace.System(), name, dir)
	if err != nil {
		return nil, fmt.Errorf("open %s locus: %w", name, err)
	}
	Single(db)
	return db, nil
}

// Local opens the namespace's encrypted SQLite file for subsystem under dir, with
// the cap applied, whatever a server holds. It is the file as it is, which a driver
// reads to move it; an app opens through [Open] or [Use], which know where the store is.
func Local(ns namespace.Namespace, subsystem, dir string) (*sql.DB, error) {
	db, err := cek.Open(ns, subsystem, dir)
	if err != nil {
		return nil, err
	}
	Single(db)
	return db, nil
}

// Open opens the database the namespace x subsystem names, where it lives now: its
// file under dir, or the server's store when a server is named and the store is
// there. It never moves a store; [Use] does. The handle is the caller's to migrate
// and close.
func Open(ns namespace.Namespace, subsystem, dir string) (*sql.DB, error) {
	e, err := server()
	if err != nil {
		return nil, err
	}
	if e == nil {
		return Local(ns, subsystem, dir)
	}
	place, err := e.Where(ns, subsystem, dir)
	if err != nil {
		return nil, err
	}
	if place == OnFile {
		return Local(ns, subsystem, dir)
	}
	return e.Open(ns, subsystem)
}

// File opens the SQLite file of a store whose subsystem is not kept on the server.
// It refuses one case: a store the server holds, which a file would split in two.
func File(ns namespace.Namespace, subsystem, dir string) (*sql.DB, error) {
	e, err := server()
	if err != nil {
		return nil, err
	}
	if e != nil {
		if err := e.Guard(ns, subsystem); err != nil {
			return nil, err
		}
	}
	return Local(ns, subsystem, dir)
}

// Use opens the store (ns, subsystem) under dir where it lives now and returns what
// open makes of it, moving it from its file first when the server's policy says to.
// It is the one open of a store that may move.
//
// open is the store's own open over a database handle, migration included; the
// handle is the store's to close, as with [Open], and is closed here when open
// fails. A move that fails does not fail the open: the store opens on its file, as
// it was. log is the process's default when nil.
func Use[T io.Closer](ctx context.Context, log luxlog.Logger, ns namespace.Namespace, subsystem, dir string, open func(*sql.DB) (T, error)) (T, error) {
	var zero T
	e, err := server()
	if err != nil {
		return zero, err
	}
	if e != nil {
		if log == nil {
			log = luxlog.Default()
		}
		if err := e.Settle(ctx, log, ns, subsystem, dir, func(db *sql.DB) (io.Closer, error) {
			st, err := open(db)
			if err != nil {
				return nil, err
			}
			return st, nil
		}); err != nil {
			return zero, err
		}
	}
	db, err := Open(ns, subsystem, dir)
	if err != nil {
		return zero, err
	}
	st, err := open(db)
	if err != nil {
		_ = db.Close()
		return zero, err
	}
	return st, nil
}
