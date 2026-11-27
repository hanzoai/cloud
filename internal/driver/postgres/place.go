package postgres

// place.go — where a store lives is a fact in the registry, not a property of the
// deployment.
//
// With CLOUD_SQL_DSN set a store is a schema on hanzo-sql OR a SQLite file, and which
// one is read, in this order, from facts that cannot disagree:
//
//	no file under dir          the store is born on hanzo-sql (there is nothing to move)
//	registry says moved        the store is on hanzo-sql; its file, if still there, is
//	                           a dead copy that nothing opens
//	anything else              the store is its file: not yet moved, being moved by a
//	                           process that has died, or a move that failed and rolled back
//
// That is what lets the DSN be set before every store has been moved: a store that
// has not is exactly what it was. [Move] is the only thing that flips a store from the
// file to hanzo-sql, in one place and one direction, and flipping it back is deleting
// its registry row (see LLM.md, "Prod state lives in hanzo-sql").

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	"github.com/hanzoai/namespace"
)

// Where says where the store (ns, subsystem) lives now. Without a DSN every store is a
// file.
func Where(ns namespace.Namespace, subsystem, dir string) (Place, error) {
	if DSN() == "" {
		return OnFile, nil
	}
	if dir == "" {
		return OnServer, nil // a caller with no data directory has no files to have
	}
	path, err := namespace.Path(dir, ns, subsystem)
	if err != nil {
		return OnFile, err
	}
	return WhereAt(ns, subsystem, path)
}

// WhereAt is [Where] for a store whose file is at a path the caller names: an engine
// that keeps its own files (a Base's data.db) and so is not under the namespace's
// rendering of a path.
func WhereAt(ns namespace.Namespace, subsystem, path string) (Place, error) {
	dsn := DSN()
	if dsn == "" {
		return OnFile, nil
	}
	if !fileExists(path) {
		return OnServer, nil // born here
	}
	schema, err := Schema(ns, subsystem)
	if err != nil {
		return OnFile, err
	}
	s, err := serverOf(dsn)
	if err != nil {
		return OnFile, err
	}
	if _, ok := s.moved.Load(schema); ok {
		return OnServer, nil
	}
	state, _, err := s.state(context.Background(), schema)
	if err != nil {
		return OnFile, fmt.Errorf("postgres: where is %s/%s: %w", ns, subsystem, err)
	}
	switch state {
	case stateMoved:
		s.moved.Store(schema, struct{}{})
		return OnServer, nil
	case stateMoving:
		// A row that says moving is a move in progress or the remains of one whose
		// process died. The mover's session lock tells which: the server drops it when
		// the connection that holds it goes.
		held, err := s.moveHeld(context.Background(), schema)
		if err != nil {
			return OnFile, err
		}
		if held {
			return OnFile, ErrMoving
		}
	}
	return OnFile, nil
}

// Guard refuses a file for a store the registry says is (or is becoming) a schema,
// which a file would split in two.
func Guard(ns namespace.Namespace, subsystem string) error {
	dsn := DSN()
	if dsn == "" {
		return nil
	}
	schema, err := Schema(ns, subsystem)
	if err != nil {
		return err
	}
	s, err := serverOf(dsn)
	if err != nil {
		return err
	}
	state, _, err := s.state(context.Background(), schema)
	if err != nil {
		return err
	}
	if state == stateMoved || state == stateMoving {
		return fmt.Errorf("postgres: %s/%s is %s on hanzo-sql but its subsystem is not ported, so opening its file would split it in two", ns, subsystem, state)
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// state is the registry's word on a store: "" when it has no row.
func (s *server) state(ctx context.Context, schema string) (state string, failed int64, err error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	err = s.admin.QueryRowContext(ctx, `SELECT state, failed FROM `+registry+` WHERE schema = $1`, schema).Scan(&state, &failed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil
	}
	return state, failed, err
}
