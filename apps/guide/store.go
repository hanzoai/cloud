package guide

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Store is one org's guide database. Isolation is PHYSICAL: cloud.OrgStore opens a
// distinct file per org ({DataDir}/orgs/{org}/guide.db), so there is no org column
// and no cross-org query is expressible. It holds three tables:
//
//   - progress:   one row per step whose state has diverged from the todo default.
//   - actions:    the Business AI action ledger — every "do it for me" tool call,
//     its args, and its outcome. It is both the audit-visible record and
//     the backing state for the "acted" auto-detect signal.
//   - curriculum: at most one row — the org's custom curriculum override (raw doc).
//
// The caller (guide.Mount) opens it through cloud.NewOrgStore; openStore below is
// the per-org open func. cloud.OrgDB has already applied the WAL/busy pragmas and
// single-writer bound, so openStore only migrates.
type Store struct {
	db *sql.DB
}

func openStore(db *sql.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS guide_progress (
  step_id    TEXT PRIMARY KEY,
  state      TEXT NOT NULL,
  source     TEXT NOT NULL DEFAULT '',
  note       TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS guide_actions (
  id         TEXT PRIMARY KEY,
  step_id    TEXT NOT NULL,
  tool       TEXT NOT NULL,
  args       TEXT NOT NULL DEFAULT '',
  result     TEXT NOT NULL DEFAULT '',
  ok         INTEGER NOT NULL DEFAULT 0,
  err        TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_guide_actions_tool_ok ON guide_actions(tool, ok);
CREATE INDEX IF NOT EXISTS ix_guide_actions_created ON guide_actions(created_at);
CREATE TABLE IF NOT EXISTS guide_curriculum (
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  doc        TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("guide migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database. Idempotent-safe via sql.DB.
func (s *Store) Close() error { return s.db.Close() }

// StateRow is a step's persisted progress.
type StateRow struct {
	State     State  `json:"state"`
	Source    string `json:"source,omitempty"`
	Note      string `json:"note,omitempty"`
	UpdatedAt int64  `json:"updatedAt,omitempty"`
}

// States returns every recorded progress row keyed by step id. Steps with no row
// are implicitly todo (the engine's stateOf default) — absence is the todo state,
// so a fresh org needs no seeding.
func (s *Store) States(ctx context.Context) (map[string]StateRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT step_id, state, source, note, updated_at FROM guide_progress`)
	if err != nil {
		return nil, fmt.Errorf("load progress: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]StateRow, 16)
	for rows.Next() {
		var id string
		var r StateRow
		if err := rows.Scan(&id, &r.State, &r.Source, &r.Note, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan progress: %w", err)
		}
		out[id] = r
	}
	return out, rows.Err()
}

// SetState upserts a step's state with its source ("manual"|"agent"|"auto") and an
// optional note.
func (s *Store) SetState(ctx context.Context, stepID string, state State, source, note string, now int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO guide_progress (step_id, state, source, note, updated_at) VALUES (?,?,?,?,?)
		 ON CONFLICT(step_id) DO UPDATE SET state=excluded.state, source=excluded.source, note=excluded.note, updated_at=excluded.updated_at`,
		stepID, string(state), source, note, now)
	if err != nil {
		return fmt.Errorf("set state: %w", err)
	}
	return nil
}

// ResetState removes a step's row, returning it to the implicit todo default.
func (s *Store) ResetState(ctx context.Context, stepID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM guide_progress WHERE step_id=?`, stepID); err != nil {
		return fmt.Errorf("reset state: %w", err)
	}
	return nil
}

// ActionRecord is one Business AI tool execution — the audit-visible ledger row.
type ActionRecord struct {
	ID        string `json:"id"`
	StepID    string `json:"stepId"`
	Tool      string `json:"tool"`
	Args      string `json:"args,omitempty"`
	Result    string `json:"result,omitempty"`
	OK        bool   `json:"ok"`
	Err       string `json:"err,omitempty"`
	CreatedAt int64  `json:"createdAt"`
}

// AddAction appends an action-ledger row.
func (s *Store) AddAction(ctx context.Context, a ActionRecord) error {
	ok := 0
	if a.OK {
		ok = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO guide_actions (id, step_id, tool, args, result, ok, err, created_at) VALUES (?,?,?,?,?,?,?,?)`,
		a.ID, a.StepID, a.Tool, a.Args, a.Result, ok, a.Err, a.CreatedAt)
	if err != nil {
		return fmt.Errorf("add action: %w", err)
	}
	return nil
}

// ListActions returns the most-recent actions first, bounded by limit.
func (s *Store) ListActions(ctx context.Context, limit int) ([]ActionRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, step_id, tool, args, result, ok, err, created_at FROM guide_actions ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list actions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]ActionRecord, 0, 16)
	for rows.Next() {
		var a ActionRecord
		var ok int
		if err := rows.Scan(&a.ID, &a.StepID, &a.Tool, &a.Args, &a.Result, &ok, &a.Err, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan action: %w", err)
		}
		a.OK = ok != 0
		out = append(out, a)
	}
	return out, rows.Err()
}

// HasSuccessfulAction reports whether a prior "do it for me" call on tool succeeded
// — the backing predicate for the "acted" auto-detect signal. A successful call
// means the real, audited effect (e.g. a Content draft) landed in the sibling
// subsystem, so the step's done-criterion is met.
func (s *Store) HasSuccessfulAction(ctx context.Context, tool string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM guide_actions WHERE tool=? AND ok=1 LIMIT 1`, tool).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("has action: %w", err)
	}
	return true, nil
}

// GetCurriculum returns the org's custom curriculum doc, or (nil,false) when the
// org uses the built-in default.
func (s *Store) GetCurriculum(ctx context.Context) ([]byte, bool, error) {
	var doc string
	err := s.db.QueryRowContext(ctx, `SELECT doc FROM guide_curriculum WHERE id=1`).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get curriculum: %w", err)
	}
	return []byte(doc), true, nil
}

// SetCurriculum stores the org's custom curriculum doc (the raw validated source).
func (s *Store) SetCurriculum(ctx context.Context, doc []byte, now int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO guide_curriculum (id, doc, updated_at) VALUES (1,?,?)
		 ON CONFLICT(id) DO UPDATE SET doc=excluded.doc, updated_at=excluded.updated_at`,
		string(doc), now)
	if err != nil {
		return fmt.Errorf("set curriculum: %w", err)
	}
	return nil
}

// ClearCurriculum removes the org override, reverting to the built-in default.
func (s *Store) ClearCurriculum(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM guide_curriculum WHERE id=1`); err != nil {
		return fmt.Errorf("clear curriculum: %w", err)
	}
	return nil
}
