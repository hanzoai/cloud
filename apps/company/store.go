package company

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/tenure"
	// locus.Open is the ONE opener: with CLOUD_SQL_DSN set the database is a schema
	// on hanzo-sql, and otherwise it is born encrypted under the key cek derives from
	// the process master and the system namespace, with the single-connection cap
	// already applied.
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
)

// errNotFound is returned when an org has no formation yet. Handlers map it to 404.
var errNotFound = errors.New("company: formation not found")

// Store persists formations. ONE database — the system namespace's "company", a
// SQLite file or a schema on hanzo-sql — holds every org's formation; tenant isolation
// is the `org` primary key, enforced on EVERY query. There is at most one formation
// per org (an org forms one company through this flow), so the aggregate is stored as a
// single row: the machine-relevant projection (stage/structure/name) in columns for
// cheap listing, and the full Formation as a JSON document in `data`, TEXT because it
// is only ever read back whole. Its SQL is the dialect both engines share
// (sdk/dialect), and its tests run on both (internal/sqltest).
//
// The people of a formation (founders, officers, their addresses and KYC status) are in
// `data`, and the name the entity is being formed under is `name`: both are SEALED
// columns (internal/org, field.go) on both engines, bound to the row's org. The register
// shows the name and filters by stage and structure, so those two and the times stay
// plain; no WHERE or ORDER BY reads a sealed column.
type Store struct {
	db     *sql.DB
	d      dialect.Dialect
	fields *tenure.Fields
}

// subsystem names the store: the system namespace's "company". The database and the
// sealer are both made from it, so a column's key and its schema cannot name two things.
const subsystem = "company"

var formationKey, formationSealed = []string{"org"}, []string{"name", "data"}

// openStore opens the database and the sealed columns' keys. master is
// cloud.Deps.MasterKey; with none the store does not open, so no formation is ever
// written in the clear because a key was missing.
func openStore(dir string, master []byte) (*Store, error) {
	c, err := tenure.NewCipher(master)
	if err != nil {
		return nil, fmt.Errorf("company: %w", err)
	}
	fields, err := c.Fields(namespace.System(), subsystem)
	if err != nil {
		return nil, fmt.Errorf("company: %w", err)
	}
	return locus.Use(context.Background(), nil, namespace.System(), subsystem, dir, func(db *sql.DB) (*Store, error) {
		s := &Store{db: db, d: dialect.Of(db), fields: fields}
		if err := s.migrate(); err != nil {
			return nil, err
		}
		return s, nil
	})
}

// migrate creates the formations table, in one transaction under the store's migration
// lock. Idempotent (IF NOT EXISTS). `org` is the primary key, so tenant isolation is a
// physical property.
func (s *Store) migrate() error {
	// org is ordered by its bytes, as a file does, so ties in the register fall the same way.
	ddl := `
CREATE TABLE IF NOT EXISTS company_formations (
  org         TEXT ` + s.d.Bytewise() + ` PRIMARY KEY,
  stage       TEXT NOT NULL,
  structure   TEXT NOT NULL DEFAULT '',
  name        TEXT NOT NULL DEFAULT '',
  data        TEXT NOT NULL,
  created_at  BIGINT NOT NULL,
  updated_at  BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_company_formations_stage ON company_formations(stage);
`
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.d.Migrate(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return fmt.Errorf("company migrate: %w", err)
	}
	// A SQLite file from before the columns were sealed holds them in plaintext. Seal them
	// in place, before any read, so the store never reads a value it did not write and
	// cmd/sqlmove copies ciphertext. hanzo-sql tables are born sealed, and Upgrade leaves
	// them alone. It is a walk in small transactions of its own, after the schema has
	// committed.
	if _, err := s.fields.Upgrade(context.Background(), s.db, "company_formations", formationKey, formationSealed); err != nil {
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
		`SELECT data FROM company_formations WHERE org=$1`, org).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get formation: %w", err)
	}
	r := s.fields.Row("company_formations", org)
	data = r.Open("data", data)
	if err := r.Err(); err != nil {
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
	if err := dialect.Text(f.Org, string(f.Stage), string(f.Structure), f.Name, string(data)); err != nil {
		return fmt.Errorf("put formation: %w", err)
	}
	r := s.fields.Row("company_formations", f.Org)
	name, sealed := r.Seal("name", f.Name), r.Seal("data", string(data))
	if err := r.Err(); err != nil {
		return fmt.Errorf("put formation: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO company_formations (org, stage, structure, name, data, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)
		 ON CONFLICT(org) DO UPDATE SET
		   stage=excluded.stage, structure=excluded.structure, name=excluded.name,
		   data=excluded.data, updated_at=excluded.updated_at`,
		f.Org, string(f.Stage), string(f.Structure), name, sealed, f.CreatedAt, f.UpdatedAt)
	if err != nil {
		return fmt.Errorf("put formation: %w", err)
	}
	return nil
}

// Registration is one row of the register: the projection Put already writes, with
// no JSON decode. Hanzo forms the entity and carries the formation KYC/AML
// obligation, so the platform needs to read its own book — how many entities it
// formed, which are stalled, and where. Get answers a question about ONE org and
// cannot answer any of those.
//
// It is NOT called Summary. Schema names are flat across the fleet document, and
// apps/marketing already publishes a Summary that means campaign counters — one
// name, two shapes, which every generated SDK would bind to whichever it read
// last. openapi.Compose refuses that composition, and it refused this one the
// moment the register became a typed op.
type Registration struct {
	// Org is the org whose formation this row projects.
	Org string `json:"org"`
	// Stage is the formation's current state — what the platform reads to see which
	// formations are stalled and where.
	Stage Stage `json:"stage"`
	// Structure is the legal entity being formed: c-corp, llc or dao-llc.
	Structure Structure `json:"structure"`
	// Name is the company name the entity is being formed under.
	Name string `json:"name"`
	// CreatedAt is the unix second the formation was opened.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is the unix second of the most recent write to the formation, and
	// the key the register sorts on (newest activity first).
	UpdatedAt int64 `json:"updatedAt"`
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
func (s *Store) List(ctx context.Context, f Filter) ([]Registration, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultRegisterLimit
	}
	offset := max(f.Offset, 0)
	// Both predicates are bound parameters; the empty string means "any" via the
	// ($n='' OR col=$n) form, so the SQL text is fixed regardless of the filter.
	rows, err := s.db.QueryContext(ctx,
		`SELECT org, stage, structure, name, created_at, updated_at
		   FROM company_formations
		  WHERE ($1='' OR stage=$1) AND ($2='' OR structure=$2)
		  ORDER BY updated_at DESC, org ASC
		  LIMIT $3 OFFSET $4`,
		string(f.Stage), string(f.Structure), limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list formations: %w", err)
	}
	defer rows.Close()

	out := []Registration{}
	for rows.Next() {
		var r Registration
		if err := rows.Scan(&r.Org, &r.Stage, &r.Structure, &r.Name, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan formation: %w", err)
		}
		row := s.fields.Row("company_formations", r.Org)
		r.Name = row.Open("name", r.Name)
		if err := row.Err(); err != nil {
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
		  WHERE stage=$1 ORDER BY updated_at ASC LIMIT $2`,
		string(StageFounders), limit)
	if err != nil {
		return nil, fmt.Errorf("pending formations: %w", err)
	}
	defer rows.Close()

	out := []*Formation{}
	for rows.Next() {
		var owner, data string
		if err := rows.Scan(&owner, &data); err != nil {
			return nil, fmt.Errorf("scan pending: %w", err)
		}
		r := s.fields.Row("company_formations", owner)
		data = r.Open("data", data)
		if err := r.Err(); err != nil {
			return nil, fmt.Errorf("scan pending: %w", err)
		}
		var f Formation
		if err := json.Unmarshal([]byte(data), &f); err != nil {
			return nil, fmt.Errorf("decode pending formation %q: %w", owner, err)
		}
		f.Org = owner // the column is authoritative, as in Get.
		out = append(out, &f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pending formations: %w", err)
	}
	return out, nil
}
