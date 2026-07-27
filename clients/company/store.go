package company

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	// cek opens the store encrypted at rest (migrate-on-open + shred).
	"github.com/hanzoai/cloud/cek"
	// The ONE "sqlite" driver, kept registered for cek's no-key plaintext fallback.
	_ "github.com/hanzoai/sqlite"
)

// errNotFound is returned when an org has no formation yet. Handlers map it to 404.
var errNotFound = errors.New("company: formation not found")

// Store persists formations. ONE SQLite file ({DataDir}/company.db) holds every
// org's formation; tenant isolation is the `org` primary key, enforced on EVERY
// query. There is at most one formation per org (an org forms one company through
// this flow), so the aggregate is stored as a single row: the machine-relevant
// projection (stage/structure/name) in columns for cheap listing, and the full
// Formation as a JSON document in `data`. MaxOpenConns(1) serializes writes.
type Store struct {
	db *sql.DB
}

func openStore(path string) (*Store, error) {
	db, err := cek.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("pragma %q: %w", pragma, err)
		}
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// migrate creates the formations table. Idempotent (IF NOT EXISTS). `org` is the
// primary key, so tenant isolation is a physical property.
func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS company_formations (
  org         TEXT PRIMARY KEY,
  stage       TEXT NOT NULL,
  structure   TEXT NOT NULL DEFAULT '',
  name        TEXT NOT NULL DEFAULT '',
  data        TEXT NOT NULL,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_company_formations_stage ON company_formations(stage);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("company migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database. Idempotent-safe via sql.DB.
func (s *Store) Close() error { return s.db.Close() }

// Get loads the org's formation, or errNotFound.
func (s *Store) Get(ctx context.Context, org string) (*Formation, error) {
	var data string
	err := s.db.QueryRowContext(ctx,
		`SELECT data FROM company_formations WHERE org=?`, org).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get formation: %w", err)
	}
	var f Formation
	if err := json.Unmarshal([]byte(data), &f); err != nil {
		return nil, fmt.Errorf("decode formation: %w", err)
	}
	// The org column is authoritative — never trust the JSON's own copy.
	f.Org = org
	return &f, nil
}

// Put upserts the formation. The org, stage, structure, and name projections are
// written alongside the JSON so a list/summary never has to decode every row.
func (s *Store) Put(ctx context.Context, f *Formation) error {
	if f == nil || f.Org == "" {
		return fmt.Errorf("company: put requires a formation with an org")
	}
	data, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("encode formation: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO company_formations (org, stage, structure, name, data, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?)
		 ON CONFLICT(org) DO UPDATE SET
		   stage=excluded.stage, structure=excluded.structure, name=excluded.name,
		   data=excluded.data, updated_at=excluded.updated_at`,
		f.Org, string(f.Stage), string(f.Structure), f.Name, string(data), f.CreatedAt, f.UpdatedAt)
	if err != nil {
		return fmt.Errorf("put formation: %w", err)
	}
	return nil
}

// Summary is one row of the register: the projection Put already writes, with no
// JSON decode. Hanzo forms the entity and carries the formation KYC/AML obligation,
// so the platform needs to read its own book — how many entities it formed, which
// are stalled, and where. Get answers a question about ONE org and cannot answer
// any of those.
type Summary struct {
	Org       string    `json:"org"`
	Stage     Stage     `json:"stage"`
	Structure Structure `json:"structure"`
	Name      string    `json:"name"`
	CreatedAt int64     `json:"createdAt"`
	UpdatedAt int64     `json:"updatedAt"`
}

// Filter narrows the register. A zero Filter lists every formation.
type Filter struct {
	Stage     Stage     // "" = any
	Structure Structure // "" = any
	Limit     int       // <=0 = defaultRegisterLimit
	Offset    int
}

// defaultRegisterLimit bounds an unfiltered register read. The page is explicit so
// a growing book cannot silently turn one request into an unbounded scan.
const defaultRegisterLimit = 200

// List returns formations across every org, newest activity first.
//
// This is the ONE cross-tenant read in this package, and it is deliberate: it
// serves a SuperAdmin operation (the platform reading its own register), never a
// tenant request. Every other query is keyed by org. Callers MUST establish the
// platform scope before calling — the store enforces shape, not authority.
//
// Only the projection columns are read. Put maintains them precisely so a listing
// never decodes a document it is not going to show.
func (s *Store) List(ctx context.Context, f Filter) ([]Summary, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultRegisterLimit
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	// Both predicates are bound parameters; the empty string means "any" via the
	// (?='' OR col=?) form, so the SQL text is fixed regardless of the filter.
	rows, err := s.db.QueryContext(ctx,
		`SELECT org, stage, structure, name, created_at, updated_at
		   FROM company_formations
		  WHERE (?='' OR stage=?) AND (?='' OR structure=?)
		  ORDER BY updated_at DESC, org ASC
		  LIMIT ? OFFSET ?`,
		string(f.Stage), string(f.Stage),
		string(f.Structure), string(f.Structure),
		limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list formations: %w", err)
	}
	defer rows.Close()

	out := []Summary{}
	for rows.Next() {
		var r Summary
		if err := rows.Scan(&r.Org, &r.Stage, &r.Structure, &r.Name, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan formation: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list formations: %w", err)
	}
	return out, nil
}

// Count returns how many formations sit at each stage — the register's shape in
// one query, for a back office that needs to see a queue growing.
func (s *Store) Count(ctx context.Context) (map[Stage]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT stage, COUNT(*) FROM company_formations GROUP BY stage`)
	if err != nil {
		return nil, fmt.Errorf("count formations: %w", err)
	}
	defer rows.Close()

	out := map[Stage]int{}
	for rows.Next() {
		var stage Stage
		var n int
		if err := rows.Scan(&stage, &n); err != nil {
			return nil, fmt.Errorf("scan count: %w", err)
		}
		out[stage] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count formations: %w", err)
	}
	return out, nil
}

// Pending decodes the formations that can hold a founder awaiting a KYC decision.
//
// A founder's KYC status lives in the JSON document, not in a column, so this is
// the one listing that decodes. It stays cheap by decoding ONLY StageFounders rows
// — by construction the sole stage where KYC is in flight, since guardKYCVerified
// gates the edge out of it. Rows at any other stage cannot contain pending KYC and
// are never read.
func (s *Store) Pending(ctx context.Context, limit int) ([]*Formation, error) {
	if limit <= 0 {
		limit = defaultRegisterLimit
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT org, data FROM company_formations
		  WHERE stage=? ORDER BY updated_at ASC LIMIT ?`,
		string(StageFounders), limit)
	if err != nil {
		return nil, fmt.Errorf("pending formations: %w", err)
	}
	defer rows.Close()

	out := []*Formation{}
	for rows.Next() {
		var org, data string
		if err := rows.Scan(&org, &data); err != nil {
			return nil, fmt.Errorf("scan pending: %w", err)
		}
		var f Formation
		if err := json.Unmarshal([]byte(data), &f); err != nil {
			return nil, fmt.Errorf("decode pending formation %q: %w", org, err)
		}
		f.Org = org // the column is authoritative, as in Get.
		out = append(out, &f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pending formations: %w", err)
	}
	return out, nil
}

// Delete removes the org's formation (used only in tests / a hard reset).
func (s *Store) Delete(ctx context.Context, org string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM company_formations WHERE org=?`, org)
	if err != nil {
		return false, fmt.Errorf("delete formation: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
