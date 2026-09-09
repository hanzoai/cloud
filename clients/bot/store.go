package bot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Store is one file: the org's, which holds its roster of bots and whatever
// protocol state is not one bot's own, or a single bot's, which holds that
// bot's.
//
// It holds documents rather than tables because the state left over after a
// method has composed the subsystems that already exist — a session's UI
// settings, a group's defaults, a browser's push subscription, a dismissed
// suggestion — is JSON whose shape belongs to the family that wrote it, and
// eleven families each declaring a table in one file is eleven migrations to
// keep in step. A document has none. This is the same choice clients/base
// made, applied per bot instead of per process, and the roster is kept the
// same way: a registry of a few dozen rows earns no schema of its own.
//
// Isolation is physical, as everywhere else in this cloud: the org (and the
// bot) chose the file, so neither appears as a column and no query can forget
// to filter on one.
type Store struct {
	db *sql.DB
	// run is where a statement goes: the file's connection pool, or the
	// transaction a Do is running. Every read and write below goes through it,
	// so one helper serves a plain call and the same call inside one act.
	run exec
}

// exec is the statement surface database/sql exposes on both a pool and a
// transaction, so a document operation is written once and runs in either.
type exec interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// Do runs fn as one act on this file: everything fn reads and writes through
// the *Store it is handed happens inside a single transaction, and either all
// of it lands or none of it does.
//
// It is how a read-modify-write is written here. Reading a document, changing
// it and writing it back are two statements, and two callers doing that at
// once each write over what the other had just read — and because a write
// replaces the whole document, the loser's change is gone rather than partial.
// The same holds for choosing a position from a count and then writing there,
// and for a pair of documents that must agree. Inside Do those are one act,
// and a caller that was told its work landed can be believed.
//
// fn must reach the file only through the *Store it is given. Each file is
// opened one connection at a time (cloud.OrgDB), so the transaction holds the
// only connection and a statement sent by any other route would wait for it. A
// Do inside a Do joins the outer one for the same reason: the act already in
// progress is the act.
func (s *Store) Do(ctx context.Context, fn func(*Store) error) error {
	if s.run != exec(s.db) {
		return fn(s)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := fn(&Store{db: s.db, run: tx}); err != nil {
		return err
	}
	return tx.Commit()
}

// MaxDoc bounds one document. State this large is a blob wearing a document's
// clothes and belongs in object storage.
const MaxDoc = 256 << 10

// ErrNoDoc reports a document that is not there. A method turns it into
// whatever its own contract says an absent thing means.
var ErrNoDoc = errors.New("bot: no such document")

// Doc is one stored document with its addressing and when it last changed.
type Doc struct {
	Collection string          `json:"collection"`
	ID         string          `json:"id"`
	Doc        json.RawMessage `json:"doc"`
	Created    int64           `json:"created"`
	Updated    int64           `json:"updated"`
}

// openStore is the cloud.OrgStore factory: it receives the already-open,
// already-pragma'd file and installs the schema. Idempotent, so reopening is a
// no-op.
func openStore(db *sql.DB) (*Store, error) {
	const schema = `
CREATE TABLE IF NOT EXISTS docs (
  collection TEXT    NOT NULL,
  id         TEXT    NOT NULL,
  doc        TEXT    NOT NULL,
  created    INTEGER NOT NULL,
  updated    INTEGER NOT NULL,
  PRIMARY KEY (collection, id)
)`
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("bot schema: %w", err)
	}
	return &Store{db: db, run: db}, nil
}

// Put writes a document, replacing what was there and keeping its first-write
// time.
func (s *Store) Put(ctx context.Context, collection, id string, doc any) error {
	b, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("bot: encode document: %w", err)
	}
	if len(b) > MaxDoc {
		return Invalid("document exceeds %d bytes", MaxDoc)
	}
	now := time.Now().UnixMilli()
	_, err = s.run.ExecContext(ctx,
		`INSERT INTO docs (collection, id, doc, created, updated) VALUES (?,?,?,?,?)
		 ON CONFLICT(collection, id) DO UPDATE SET doc=excluded.doc, updated=excluded.updated`,
		collection, id, string(b), now, now)
	return err
}

// Get reads one document into v. It returns ErrNoDoc when there is none.
func (s *Store) Get(ctx context.Context, collection, id string, v any) error {
	var raw string
	err := s.run.QueryRowContext(ctx,
		`SELECT doc FROM docs WHERE collection=? AND id=?`, collection, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNoDoc
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(raw), v)
}

// List returns a collection's documents, most recently written first. limit of
// zero or less means the whole collection.
func (s *Store) List(ctx context.Context, collection string, limit, offset int) ([]Doc, error) {
	q := `SELECT collection, id, doc, created, updated FROM docs
	      WHERE collection=? ORDER BY updated DESC, id ASC`
	args := []any{collection}
	if limit > 0 {
		q += ` LIMIT ? OFFSET ?`
		args = append(args, limit, max(offset, 0))
	}
	return s.read(ctx, q, args...)
}

// read runs one document query and decodes its rows.
func (s *Store) read(ctx context.Context, q string, args ...any) ([]Doc, error) {
	rows, err := s.run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	out := []Doc{}
	for rows.Next() {
		var d Doc
		var raw string
		if err := rows.Scan(&d.Collection, &d.ID, &raw, &d.Created, &d.Updated); err != nil {
			return nil, err
		}
		d.Doc = json.RawMessage(raw)
		out = append(out, d)
	}
	return out, rows.Err()
}

// Count is how many documents a collection holds.
func (s *Store) Count(ctx context.Context, collection string) (int, error) {
	var n int
	err := s.run.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM docs WHERE collection=?`, collection).Scan(&n)
	return n, err
}

// Range returns a window of a collection in id order, oldest id first. It is
// the read for a collection whose ids carry the order — a transcript, whose
// document id is the message's position — where List's recency order would put
// an edited document out of place and a whole-collection read would grow with
// the conversation. offset counts from the start; limit of zero or less means
// to the end.
func (s *Store) Range(ctx context.Context, collection string, limit, offset int) ([]Doc, error) {
	q := `SELECT collection, id, doc, created, updated FROM docs
	      WHERE collection=? ORDER BY id ASC`
	args := []any{collection}
	if limit > 0 {
		q += ` LIMIT ? OFFSET ?`
		args = append(args, limit, max(offset, 0))
	} else if offset > 0 {
		q += ` LIMIT -1 OFFSET ?`
		args = append(args, offset)
	}
	return s.read(ctx, q, args...)
}

// Delete forgets one document. Deleting what is not there is not an error:
// the caller asked for it to be gone and it is.
func (s *Store) Delete(ctx context.Context, collection, id string) error {
	_, err := s.run.ExecContext(ctx, `DELETE FROM docs WHERE collection=? AND id=?`, collection, id)
	return err
}

// Drop forgets a whole collection. It is the read-modify-write's opposite: a
// caller that wants everything under one name gone does not have to know the
// names inside it, and never learns of a document written while it was
// deleting the others.
func (s *Store) Drop(ctx context.Context, collection string) error {
	_, err := s.run.ExecContext(ctx, `DELETE FROM docs WHERE collection=?`, collection)
	return err
}

// Empty forgets every document in the file, leaving it as it was before
// anything was written to it. It is how a partition stops being one tenant's:
// the file survives the emptying, so the handle a caller already holds stays
// good and the schema does not have to be reinstalled.
func (s *Store) Empty(ctx context.Context) error {
	_, err := s.run.ExecContext(ctx, `DELETE FROM docs`)
	return err
}

// Close releases the file. cloud.OrgStore requires it.
func (s *Store) Close() error { return s.db.Close() }
