// Package basedb is the ONE way cloud opens a database.
//
// It is cek.Open plus the one thing a file needs that a key does not: the
// directory it lives in. cek renders the path from the namespace and opens it
// keyed, but nothing creates the parent — and on the pure-Go codec the database
// is only written back at CLOSE, so a missing directory does not fail the open,
// it loses the data at the end. That is the kind of thing that must be stated
// once, beside the open it belongs to, rather than remembered by ~50 stores.
//
// Nothing else lives here. Which key, where the file goes and how it is sealed
// are cek's, namespace's and the driver's respectively; cloud holds none of it.
package basedb

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hanzoai/cek"
	"github.com/hanzoai/namespace"
)

// Open opens the database holding subsystem for ns, under dir, creating both the
// directory and the database if they do not exist.
//
// The path is rendered from the SAME namespace.Path cek opens through, so the
// directory created here is the directory the file lands in — the two cannot
// name different places.
func Open(ns namespace.Namespace, subsystem, dir string) (*sql.DB, error) {
	path, err := namespace.Path(dir, ns, subsystem)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("basedb: create directory for %s/%s: %w", ns, subsystem, err)
	}
	return cek.Open(ns, subsystem, dir)
}
