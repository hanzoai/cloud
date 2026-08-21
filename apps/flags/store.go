package flags

// The definitions store — SQLite per (org, project) via cloud.OrgDB (HIP-0302
// physical isolation: {DataDir}/orgs/{org}/projects/{project}/flags.db). Flag
// definitions are the PostHog-compatible JSON the native evaluator consumes;
// the store keeps them small, versioned and audited. No KV anywhere.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Store struct {
	db *sql.DB
}

func openStore(db *sql.DB) (*Store, error) {
	const schema = `
CREATE TABLE IF NOT EXISTS flag_defs (
    key        TEXT PRIMARY KEY,
    definition TEXT NOT NULL,             -- full FlagDef JSON (key, active, filters, ...)
    version    INTEGER NOT NULL DEFAULT 1,
    updated_at TEXT NOT NULL,
    updated_by TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS flag_activity (
    id     INTEGER PRIMARY KEY AUTOINCREMENT,
    key    TEXT NOT NULL,
    action TEXT NOT NULL,                 -- created | updated | deleted
    actor  TEXT NOT NULL DEFAULT '',
    at     TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_flag_activity_key ON flag_activity(key, id);
`
	if _, err := db.Exec(schema); err != nil {
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
	var r DefRow
	var def string
	err := s.db.QueryRow(`SELECT key, definition, version, updated_at, updated_by FROM flag_defs WHERE key = ?`, key).
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
	res, err := s.db.Exec(`
INSERT INTO flag_defs (key, definition, version, updated_at, updated_by)
VALUES (?, ?, 1, ?, ?)
ON CONFLICT(key) DO UPDATE SET
    definition = excluded.definition,
    version    = flag_defs.version + 1,
    updated_at = excluded.updated_at,
    updated_by = excluded.updated_by`,
		key, string(norm), now, actor)
	if err != nil {
		return err
	}
	action := "updated"
	if n, _ := res.RowsAffected(); n == 1 {
		// SQLite reports 1 for both paths; disambiguate via version
		var v int
		if err := s.db.QueryRow(`SELECT version FROM flag_defs WHERE key = ?`, key).Scan(&v); err == nil && v == 1 {
			action = "created"
		}
	}
	_, err = s.db.Exec(`INSERT INTO flag_activity (key, action, actor, at) VALUES (?, ?, ?, ?)`,
		key, action, actor, now)
	return err
}

func (s *Store) Delete(key, actor string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM flag_defs WHERE key = ?`, key)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return false, nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.Exec(`INSERT INTO flag_activity (key, action, actor, at) VALUES (?, 'deleted', ?, ?)`,
		key, actor, now)
	return true, err
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
	rows, err := s.db.Query(`SELECT id, key, action, actor, at, detail FROM flag_activity ORDER BY id DESC LIMIT ?`, limit)
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
