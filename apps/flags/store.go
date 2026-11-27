package flags

// The definitions store — one database per (org, project) via cloud.OrgDB (HIP-0302
// physical isolation: a SQLite file {DataDir}/orgs/{org}/projects/{project}/flags.db,
// or, on hanzo-sql, a schema of that name). Flag definitions are the
// PostHog-compatible JSON the native evaluator consumes; the store keeps them small,
// versioned and audited. No KV anywhere.
//
// Its SQL is the dialect both engines share (sdk/dialect), and its tests run
// on both (internal/sqltest). A definition is TEXT and not a JSON column on purpose:
// it is returned byte for byte as it was written, and jsonb would renormalise it.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
)

type Store struct {
	db *sql.DB
}

func openStore(db *sql.DB) (*Store, error) {
	d := dialect.Of(db)
	schema := `
CREATE TABLE IF NOT EXISTS flag_defs (
    key        TEXT PRIMARY KEY,
    definition TEXT NOT NULL,             -- full FlagDef JSON (key, active, filters, ...)
    version    BIGINT NOT NULL DEFAULT 1,
    updated_at TEXT NOT NULL,
    updated_by TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS flag_activity (
    id     ` + d.Identity() + `,
    key    TEXT NOT NULL,
    action TEXT NOT NULL,                 -- created | updated | deleted
    actor  TEXT NOT NULL DEFAULT '',
    at     TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_flag_activity_key ON flag_activity(key, id);
-- The opt-in to public betas (optin.go). Rows live in the reserved platform
-- store alone, reached through Client.platform; a tenant's store holds none.
CREATE TABLE IF NOT EXISTS flag_optin (
    subject TEXT PRIMARY KEY,             -- the person's IAM id
    opted   BIGINT NOT NULL,
    at      TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS flag_optin_org (
    org     TEXT PRIMARY KEY,
    blocked BIGINT NOT NULL,
    at      TEXT NOT NULL,
    by      TEXT NOT NULL DEFAULT ''
);
`
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := d.Migrate(ctx, db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, schema)
		return err
	}); err != nil {
		return nil, fmt.Errorf("flags: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// DefsJSON assembles every definition into the JSON array the evaluator takes.
// The stored definition's "key" wins; a row whose JSON is corrupt is skipped
// rather than poisoning the whole project.
func (s *Store) DefsJSON() ([]byte, int, error) {
	rows, err := s.db.Query(`SELECT definition FROM flag_defs ORDER BY key`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var def string
		if err := rows.Scan(&def); err != nil {
			return nil, 0, err
		}
		if json.Valid([]byte(def)) {
			parts = append(parts, def)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return []byte("[" + strings.Join(parts, ",") + "]"), len(parts), nil
}

// DefRow is one stored flag definition and its provenance.
type DefRow struct {
	// Key is the flag's primary key in the caller's (org, project) store, and the
	// name evaluation looks it up by. On a write it is taken from the URL, never
	// from the body: the stored document's own "key" is forced to match.
	Key string `json:"key"`
	// Definition is the flag-definition document the evaluator consumes, kept
	// BYTE-FOR-BYTE as it was written. It is the engine's format rather than this
	// package's, so it carries fields no Go type here names — targeting groups,
	// rollout percentages, variants, payloads — and a caller must round-trip it
	// whole rather than rebuilding it from the parts it recognizes.
	Definition json.RawMessage `json:"definition"`
	// Version is 1 when the key was created and rises by one on every overwrite.
	// It counts writes, not content changes: re-storing an identical document
	// bumps it.
	Version int `json:"version"`
	// UpdatedAt is when the definition was last written, RFC 3339 UTC.
	UpdatedAt string `json:"updated_at"`
	// UpdatedBy is the email of the principal who last wrote it. Empty when the
	// write came from an in-process composer (an experiment registering its own
	// assignment flag) rather than from a signed-in person.
	UpdatedBy string `json:"updated_by"`
}

func (s *Store) List() ([]DefRow, error) {
	rows, err := s.db.Query(`SELECT key, definition, version, updated_at, updated_by FROM flag_defs ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DefRow{}
	for rows.Next() {
		var r DefRow
		var def string
		if err := rows.Scan(&r.Key, &def, &r.Version, &r.UpdatedAt, &r.UpdatedBy); err != nil {
			return nil, err
		}
		r.Definition = json.RawMessage(def)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Get(key string) (DefRow, bool, error) {
	return get(context.Background(), s.db, key)
}

// runner is what a statement runs on: the store's pool, or a transaction of it.
type runner interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func get(ctx context.Context, q runner, key string) (DefRow, bool, error) {
	var r DefRow
	var def string
	err := q.QueryRowContext(ctx, `SELECT key, definition, version, updated_at, updated_by FROM flag_defs WHERE key = $1`, key).
		Scan(&r.Key, &def, &r.Version, &r.UpdatedAt, &r.UpdatedBy)
	if err == sql.ErrNoRows {
		return DefRow{}, false, nil
	}
	if err != nil {
		return DefRow{}, false, err
	}
	r.Definition = json.RawMessage(def)
	return r, true, nil
}

// Upsert stores a definition under key (the definition's own "key" field is
// forced to match) and logs the change.
//
// A JSON `null` body unmarshals into a NIL map without error, and the next line
// assigns into it — which panicked, on a request any caller could send. `null` is
// not an object, so it is refused exactly like `[1,2]` or `"x"`: one predicate,
// not two, and no reachable crash.
func (s *Store) Upsert(key string, definition json.RawMessage, actor string) error {
	ctx, cancel := context.WithTimeout(context.Background(), dialect.LockWait)
	defer cancel()
	return s.Edit(ctx, key, actor, func(json.RawMessage) (json.RawMessage, error) { return definition, nil })
}

// Edit makes a read, an edit and a write of one definition the only one on key, across
// every replica: edit is handed the definition as stored (nil when there is none) and
// answers the one to store, and the change and its log entry land together or not at
// all. Two edits of one key, from the same replica or two, run one after the other,
// each on what the last stored; other keys do not wait. The lock is the transaction
// (Dialect.Serialize), so a replica that dies holding it frees the key.
//
// edit runs with the lock held and may do what is not this store's (put the change on
// the audit trail), but nothing of this store: the connection the lock holds is the
// only one it may use, and a file has no other.
func (s *Store) Edit(ctx context.Context, key, actor string, edit func(stored json.RawMessage) (json.RawMessage, error)) error {
	tx, err := dialect.Of(s.db).Serialize(ctx, s.db, key)
	if err != nil {
		return fmt.Errorf("flags: lock %s: %w", key, err)
	}
	defer func() { _ = tx.Rollback() }()
	row, found, err := get(ctx, tx, key)
	if err != nil {
		return err
	}
	var stored json.RawMessage
	if found {
		stored = row.Definition
	}
	definition, err := edit(stored)
	if err != nil {
		return err
	}
	if err := put(ctx, tx, key, definition, actor); err != nil {
		return err
	}
	return tx.Commit()
}

func put(ctx context.Context, q runner, key string, definition json.RawMessage, actor string) error {
	var def map[string]any
	if err := json.Unmarshal(definition, &def); err != nil {
		return fmt.Errorf("flags: definition not an object: %w", err)
	}
	if def == nil {
		return fmt.Errorf("flags: definition not an object: null")
	}
	def["key"] = key
	norm, err := json.Marshal(def)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	// The row's own version says which path ran: 1 is a create, anything else an
	// overwrite. Neither engine reports that through RowsAffected (both say 1 for
	// both), so the statement hands it back.
	var version int
	if err := q.QueryRowContext(ctx, `
INSERT INTO flag_defs (key, definition, version, updated_at, updated_by)
VALUES ($1, $2, 1, $3, $4)
ON CONFLICT(key) DO UPDATE SET
    definition = excluded.definition,
    version    = flag_defs.version + 1,
    updated_at = excluded.updated_at,
    updated_by = excluded.updated_by
RETURNING version`,
		key, string(norm), now, actor).Scan(&version); err != nil {
		return err
	}
	action := "updated"
	if version == 1 {
		action = "created"
	}
	_, err = q.ExecContext(ctx, `INSERT INTO flag_activity (key, action, actor, at) VALUES ($1, $2, $3, $4)`,
		key, action, actor, now)
	return err
}

// Delete removes a definition and logs it, as the only change on key: a grant that
// read the definition before it went does not write it back.
func (s *Store) Delete(key, actor string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dialect.LockWait)
	defer cancel()
	tx, err := dialect.Of(s.db).Serialize(ctx, s.db, key)
	if err != nil {
		return false, fmt.Errorf("flags: lock %s: %w", key, err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `DELETE FROM flag_defs WHERE key = $1`, key)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return false, nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx, `INSERT INTO flag_activity (key, action, actor, at) VALUES ($1, 'deleted', $2, $3)`,
		key, actor, now); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// ActivityRow is one entry of a project's flag change log.
type ActivityRow struct {
	// ID is the log's own sequence number, rising with each entry. The log is
	// served newest-first, which is this descending.
	ID int64 `json:"id"`
	// Key is the flag that changed. It survives a delete, so the log still names
	// flags the definition store no longer holds.
	Key string `json:"key"`
	// Action is one of created, updated, deleted.
	Action string `json:"action"`
	// Actor is the email of the principal who made the change. Empty for a write
	// by an in-process composer; a project key can never appear here, because
	// evaluating flags is all a key may do.
	Actor string `json:"actor"`
	// At is when the change was made, RFC 3339 UTC.
	At string `json:"at"`
	// Detail is free-form context about the change. Nothing writes it today, so
	// it is absent from every row the store serves.
	Detail string `json:"detail,omitempty"`
}

func (s *Store) Activity(limit int) ([]ActivityRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, key, action, actor, at, detail FROM flag_activity ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ActivityRow{}
	for rows.Next() {
		var r ActivityRow
		if err := rows.Scan(&r.ID, &r.Key, &r.Action, &r.Actor, &r.At, &r.Detail); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
