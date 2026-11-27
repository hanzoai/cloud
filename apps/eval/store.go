package eval

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	// locus.Use is the ONE opener (it moves the store off its file first, when asked): with CLOUD_SQL_DSN set it is this subsystem's
	// schema on hanzo-sql; without, an encrypted SQLite file born under the key cek
	// derives from the process master and the system namespace.

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver: it registers
	// the "sqlite" database/sql name under both build tags. Importing modernc
	// directly instead would double-register "sqlite" under CGO and panic at
	// init. Blank import registers the driver.
	_ "github.com/hanzoai/sqlite"
)

// The eval METASTORE is the config/metadata half of the storage split (CTO
// directive): datasets, dataset items, evaluators, rubrics and dataset-run
// definitions live here in Hanzo Base (a SQLite file, or hanzo-sql), per-org. The high-volume telemetry
// half (traces, observations, scores-as-events) lives in datastore/datastore —
// see telemetry.go. These two are orthogonal: the metastore owns durable config,
// the telemetry store owns the append-only event stream; neither knows the
// other's schema. The run orchestrator (eval.go) composes both.
//
// Tenant isolation is the `org` column, present on EVERY table, NOT NULL, and a
// mandatory predicate on EVERY query. The org value is c.Org() exactly as
// SanitizeIdentity minted it (HIP-0026) — never normalized (Red HIGH-1: casing/
// trimming collapses distinct owners into one bucket). One SQLite file holds
// every org's rows; the org column is the trust boundary at the query layer. Its
// SQL is the dialect both engines share (sdk/dialect), and an upsert that
// reads then writes starts with BeginWrite.

var (
	errConflict = errors.New("eval: resource already exists")
	errNotFound = errors.New("eval: resource not found")
)

// Dataset is an org-scoped named collection of eval items. (org,name) is unique.
type Dataset struct {
	ID          string
	Org         string
	Name        string
	Description string
	Metadata    string // opaque JSON object, stored verbatim (bounded at the edge)
	CreatedAt   int64
	UpdatedAt   int64
}

// DatasetItem is one input/expected pair inside a dataset. Items are addressed
// by their own id; (org,dataset,id) scopes every lookup. Status ACTIVE|ARCHIVED
// mirrors the observation item lifecycle (a run consumes only ACTIVE items).
type DatasetItem struct {
	ID        string
	Org       string
	Dataset   string
	Input     string // opaque JSON, verbatim
	Expected  string // opaque JSON, verbatim ("expectedOutput" on the wire)
	Metadata  string // opaque JSON, verbatim
	Status    string // ACTIVE | ARCHIVED
	CreatedAt int64
	UpdatedAt int64
}

// Evaluator is an org-scoped judge definition: a model + rubric that scores a
// run's items. (org,name) is unique. It carries NO secret — the model key is the
// caller's own bearer at run time, never stored here.
type Evaluator struct {
	ID        string
	Org       string
	Name      string
	Model     string
	Criteria  string
	ScoreName string // the score name this evaluator emits (defaults to Name)
	CreatedAt int64
	UpdatedAt int64
}

// ScoreConfig is an org-scoped definition of a score's shape: NUMERIC (with
// optional min/max), CATEGORICAL (with an allowed value set), or BOOLEAN.
// Scores written for this name are validated against it (Red: score integrity).
type ScoreConfig struct {
	ID         string
	Org        string
	Name       string
	DataType   string   // NUMERIC | CATEGORICAL | BOOLEAN
	MinValue   *float64 // NUMERIC lower bound (nil = unbounded)
	MaxValue   *float64 // NUMERIC upper bound (nil = unbounded)
	Categories []string // CATEGORICAL allowed labels
	CreatedAt  int64
	UpdatedAt  int64
}

// DatasetRun is the DEFINITION of a run (its metadata): which dataset+model, the
// run name, and rollup counters. The per-item scores/traces are telemetry
// (datastore); this row is the durable, listable run record. (org,dataset,name)
// is unique so a run name is stable per dataset.
type DatasetRun struct {
	ID         string
	Org        string
	Dataset    string
	Name       string
	Model      string
	JudgeModel string
	Items      int
	Scored     int
	AvgScore   float64
	CreatedAt  int64
	UpdatedAt  int64
}

// Store is the eval metastore over one database — the deployment's own "evals", a
// SQLite file or a schema on hanzo-sql. Tenancy is the org column; on a file
// MaxOpenConns(1) serializes writes against the file lock (same discipline as
// prompts/projects).
type Store struct {
	db *sql.DB
}

func openStore(dir string) (*Store, error) {
	s, err := locus.Use(context.Background(), nil, namespace.System(), "evals", dir, func(db *sql.DB) (*Store, error) {
		s := &Store{db: db}
		if err := s.migrate(); err != nil {
			return nil, err
		}
		return s, nil
	})
	if err != nil {
		return nil, fmt.Errorf("open evals store: %w", err)
	}
	return s, nil
}

func (s *Store) migrate() error {
	d := dialect.Of(s.db)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := d.Migrate(ctx, s.db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, ddl)
		return err
	}); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// ddl is the schema, run by migrate in one transaction under the store's lock.
//
// Every table's identity is COMPOSITE with org (the model keys these `(id,
// projectId)`). The `id` is NEVER a global cross-org key: making it a global
// PRIMARY KEY would (a) stop two orgs from ever using the same item id and (b)
// leak a cross-tenant existence oracle (org A's create 409s iff org B already
// used that id). So PK = (org, id) for items; natural keys (org, name) / (org,
// dataset, name) for the rest. Tenant isolation is the org column on every row.
const ddl = `
CREATE TABLE IF NOT EXISTS datasets (
  org         TEXT NOT NULL,
  id          TEXT NOT NULL,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  metadata    TEXT NOT NULL DEFAULT '{}',
  created_at  BIGINT NOT NULL,
  updated_at  BIGINT NOT NULL,
  PRIMARY KEY (org, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_datasets_org_name ON datasets(org, name);
CREATE INDEX IF NOT EXISTS ix_datasets_org_updated ON datasets(org, updated_at);

CREATE TABLE IF NOT EXISTS dataset_items (
  org         TEXT NOT NULL,
  id          TEXT NOT NULL,
  dataset     TEXT NOT NULL,
  input       TEXT NOT NULL DEFAULT '',
  expected    TEXT NOT NULL DEFAULT '',
  metadata    TEXT NOT NULL DEFAULT '{}',
  status      TEXT NOT NULL DEFAULT 'ACTIVE',
  created_at  BIGINT NOT NULL,
  updated_at  BIGINT NOT NULL,
  PRIMARY KEY (org, id)
);
CREATE INDEX IF NOT EXISTS ix_items_org_dataset ON dataset_items(org, dataset, status);

CREATE TABLE IF NOT EXISTS evaluators (
  org         TEXT NOT NULL,
  id          TEXT NOT NULL,
  name        TEXT NOT NULL,
  model       TEXT NOT NULL DEFAULT '',
  criteria    TEXT NOT NULL DEFAULT '',
  score_name  TEXT NOT NULL DEFAULT '',
  created_at  BIGINT NOT NULL,
  updated_at  BIGINT NOT NULL,
  PRIMARY KEY (org, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_evaluators_org_name ON evaluators(org, name);

CREATE TABLE IF NOT EXISTS score_configs (
  org         TEXT NOT NULL,
  id          TEXT NOT NULL,
  name        TEXT NOT NULL,
  data_type   TEXT NOT NULL DEFAULT 'NUMERIC',
  min_value   DOUBLE PRECISION,
  max_value   DOUBLE PRECISION,
  categories  TEXT NOT NULL DEFAULT '[]',
  created_at  BIGINT NOT NULL,
  updated_at  BIGINT NOT NULL,
  PRIMARY KEY (org, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_score_configs_org_name ON score_configs(org, name);

CREATE TABLE IF NOT EXISTS dataset_runs (
  org         TEXT NOT NULL,
  id          TEXT NOT NULL,
  dataset     TEXT NOT NULL,
  name        TEXT NOT NULL,
  model       TEXT NOT NULL DEFAULT '',
  judge_model TEXT NOT NULL DEFAULT '',
  items       BIGINT NOT NULL DEFAULT 0,
  scored      BIGINT NOT NULL DEFAULT 0,
  avg_score   DOUBLE PRECISION NOT NULL DEFAULT 0,
  created_at  BIGINT NOT NULL,
  updated_at  BIGINT NOT NULL,
  PRIMARY KEY (org, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_runs_org_dataset_name ON dataset_runs(org, dataset, name);
CREATE INDEX IF NOT EXISTS ix_runs_org_updated ON dataset_runs(org, updated_at);
`

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// ── encode helpers ───────────────────────────────────────────────────────────

func encodeList(xs []string) string {
	if len(xs) == 0 {
		return "[]"
	}
	b, err := json.Marshal(xs)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func decodeList(s string) []string {
	if s == "" {
		return nil
	}
	var xs []string
	if err := json.Unmarshal([]byte(s), &xs); err != nil {
		return nil
	}
	return xs
}

// ── datasets ─────────────────────────────────────────────────────────────────

const datasetCols = `id,org,name,description,metadata,created_at,updated_at`

func scanDataset(sc interface{ Scan(...any) error }) (Dataset, error) {
	var d Dataset
	err := sc.Scan(&d.ID, &d.Org, &d.Name, &d.Description, &d.Metadata, &d.CreatedAt, &d.UpdatedAt)
	return d, err
}

// UpsertDataset creates a dataset, or updates description/metadata when
// (org,name) already exists. Idempotent create keeps the FE's "ensure dataset"
// call safe. Returns the resulting row.
func (s *Store) UpsertDataset(ctx context.Context, d Dataset) (Dataset, error) {
	if err := dialect.Text(d.ID, d.Org, d.Name, d.Description, d.Metadata); err != nil {
		return Dataset{}, fmt.Errorf("upsert dataset: %w", err)
	}
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return Dataset{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var createdAt int64
	row := tx.QueryRowContext(ctx, `SELECT created_at FROM datasets WHERE org=$1 AND name=$2`, d.Org, d.Name)
	switch err := row.Scan(&createdAt); {
	case errors.Is(err, sql.ErrNoRows):
		d.CreatedAt = d.UpdatedAt
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO datasets (`+datasetCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			d.ID, d.Org, d.Name, d.Description, d.Metadata, d.CreatedAt, d.UpdatedAt); err != nil {
			return Dataset{}, fmt.Errorf("insert dataset: %w", err)
		}
	case err != nil:
		return Dataset{}, fmt.Errorf("lookup dataset: %w", err)
	default:
		d.CreatedAt = createdAt
		if _, err := tx.ExecContext(ctx,
			`UPDATE datasets SET description=$1,metadata=$2,updated_at=$3 WHERE org=$4 AND name=$5`,
			d.Description, d.Metadata, d.UpdatedAt, d.Org, d.Name); err != nil {
			return Dataset{}, fmt.Errorf("update dataset: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Dataset{}, fmt.Errorf("commit: %w", err)
	}
	return d, nil
}

func (s *Store) GetDataset(ctx context.Context, org, name string) (Dataset, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+datasetCols+` FROM datasets WHERE org=$1 AND name=$2`, org, name)
	d, err := scanDataset(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Dataset{}, errNotFound
	}
	if err != nil {
		return Dataset{}, fmt.Errorf("get dataset: %w", err)
	}
	return d, nil
}

func (s *Store) ListDatasets(ctx context.Context, org string, limit int) ([]Dataset, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+datasetCols+` FROM datasets WHERE org=$1 ORDER BY updated_at DESC, name ASC LIMIT $2`, org, limit)
	if err != nil {
		return nil, fmt.Errorf("list datasets: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Dataset
	for rows.Next() {
		d, err := scanDataset(rows)
		if err != nil {
			return nil, fmt.Errorf("scan dataset: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeleteDataset removes a dataset and its items in one transaction. Reports
// whether the dataset existed.
func (s *Store) DeleteDataset(ctx context.Context, org, name string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `DELETE FROM datasets WHERE org=$1 AND name=$2`, org, name)
	if err != nil {
		return false, fmt.Errorf("delete dataset: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM dataset_items WHERE org=$1 AND dataset=$2`, org, name); err != nil {
		return false, fmt.Errorf("delete items: %w", err)
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return n > 0, nil
}

// ── dataset items ────────────────────────────────────────────────────────────

const itemCols = `id,org,dataset,input,expected,metadata,status,created_at,updated_at`

func scanItem(sc interface{ Scan(...any) error }) (DatasetItem, error) {
	var it DatasetItem
	err := sc.Scan(&it.ID, &it.Org, &it.Dataset, &it.Input, &it.Expected, &it.Metadata,
		&it.Status, &it.CreatedAt, &it.UpdatedAt)
	return it, err
}

// PutItem inserts a new item or updates an existing one by (org,id). The dataset
// MUST already exist for this org (enforced by the caller via GetDataset); the
// item is bound to that dataset. Cross-org item ids can never collide because
// the WHERE always carries org.
func (s *Store) PutItem(ctx context.Context, it DatasetItem) (DatasetItem, error) {
	if err := dialect.Text(it.ID, it.Org, it.Dataset, it.Input, it.Expected, it.Metadata, it.Status); err != nil {
		return DatasetItem{}, fmt.Errorf("put item: %w", err)
	}
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return DatasetItem{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var createdAt int64
	var existingDataset string
	row := tx.QueryRowContext(ctx, `SELECT created_at, dataset FROM dataset_items WHERE org=$1 AND id=$2`, it.Org, it.ID)
	switch err := row.Scan(&createdAt, &existingDataset); {
	case errors.Is(err, sql.ErrNoRows):
		it.CreatedAt = it.UpdatedAt
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO dataset_items (`+itemCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			it.ID, it.Org, it.Dataset, it.Input, it.Expected, it.Metadata, it.Status, it.CreatedAt, it.UpdatedAt); err != nil {
			return DatasetItem{}, fmt.Errorf("insert item: %w", err)
		}
	case err != nil:
		return DatasetItem{}, fmt.Errorf("lookup item: %w", err)
	default:
		// An item cannot be re-homed into a different dataset via update — that
		// would let a caller move another item under a dataset it controls. Pin it.
		if existingDataset != it.Dataset {
			return DatasetItem{}, errConflict
		}
		it.CreatedAt = createdAt
		if _, err := tx.ExecContext(ctx,
			`UPDATE dataset_items SET input=$1,expected=$2,metadata=$3,status=$4,updated_at=$5 WHERE org=$6 AND id=$7`,
			it.Input, it.Expected, it.Metadata, it.Status, it.UpdatedAt, it.Org, it.ID); err != nil {
			return DatasetItem{}, fmt.Errorf("update item: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return DatasetItem{}, fmt.Errorf("commit: %w", err)
	}
	return it, nil
}

// ListItems returns items for (org,dataset), newest first, bounded by limit. When
// activeOnly is set, ARCHIVED items are excluded (the run path uses this).
func (s *Store) ListItems(ctx context.Context, org, dataset string, activeOnly bool, limit int) ([]DatasetItem, error) {
	q := `SELECT ` + itemCols + ` FROM dataset_items WHERE org=$1 AND dataset=$2`
	args := []any{org, dataset}
	if activeOnly {
		q += ` AND status=$3`
		args = append(args, "ACTIVE")
	}
	q += fmt.Sprintf(` ORDER BY created_at ASC, id ASC LIMIT $%d`, len(args)+1)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list items: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []DatasetItem
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, fmt.Errorf("scan item: %w", err)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// CountItems returns the number of items in (org,dataset) via a COUNT(*), so the
// dataset-detail view never has to load item bodies just to size the collection
// (Red LOW: loading up to maxListLimit full rows to len() them was a ~96MB
// amplification on a large dataset).
func (s *Store) CountItems(ctx context.Context, org, dataset string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dataset_items WHERE org=$1 AND dataset=$2`, org, dataset).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count items: %w", err)
	}
	return n, nil
}

// ── evaluators ───────────────────────────────────────────────────────────────

const evaluatorCols = `id,org,name,model,criteria,score_name,created_at,updated_at`

func scanEvaluator(sc interface{ Scan(...any) error }) (Evaluator, error) {
	var e Evaluator
	err := sc.Scan(&e.ID, &e.Org, &e.Name, &e.Model, &e.Criteria, &e.ScoreName, &e.CreatedAt, &e.UpdatedAt)
	return e, err
}

func (s *Store) UpsertEvaluator(ctx context.Context, e Evaluator) (Evaluator, error) {
	if err := dialect.Text(e.ID, e.Org, e.Name, e.Model, e.Criteria, e.ScoreName); err != nil {
		return Evaluator{}, fmt.Errorf("upsert evaluator: %w", err)
	}
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return Evaluator{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var createdAt int64
	row := tx.QueryRowContext(ctx, `SELECT created_at FROM evaluators WHERE org=$1 AND name=$2`, e.Org, e.Name)
	switch err := row.Scan(&createdAt); {
	case errors.Is(err, sql.ErrNoRows):
		e.CreatedAt = e.UpdatedAt
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO evaluators (`+evaluatorCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			e.ID, e.Org, e.Name, e.Model, e.Criteria, e.ScoreName, e.CreatedAt, e.UpdatedAt); err != nil {
			return Evaluator{}, fmt.Errorf("insert evaluator: %w", err)
		}
	case err != nil:
		return Evaluator{}, fmt.Errorf("lookup evaluator: %w", err)
	default:
		e.CreatedAt = createdAt
		if _, err := tx.ExecContext(ctx,
			`UPDATE evaluators SET model=$1,criteria=$2,score_name=$3,updated_at=$4 WHERE org=$5 AND name=$6`,
			e.Model, e.Criteria, e.ScoreName, e.UpdatedAt, e.Org, e.Name); err != nil {
			return Evaluator{}, fmt.Errorf("update evaluator: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Evaluator{}, fmt.Errorf("commit: %w", err)
	}
	return e, nil
}

func (s *Store) ListEvaluators(ctx context.Context, org string, limit int) ([]Evaluator, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+evaluatorCols+` FROM evaluators WHERE org=$1 ORDER BY updated_at DESC, name ASC LIMIT $2`, org, limit)
	if err != nil {
		return nil, fmt.Errorf("list evaluators: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Evaluator
	for rows.Next() {
		e, err := scanEvaluator(rows)
		if err != nil {
			return nil, fmt.Errorf("scan evaluator: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ── score configs ────────────────────────────────────────────────────────────

const scoreConfigCols = `id,org,name,data_type,min_value,max_value,categories,created_at,updated_at`

func scanScoreConfig(sc interface{ Scan(...any) error }) (ScoreConfig, error) {
	var c ScoreConfig
	var minv, maxv sql.NullFloat64
	var cats string
	err := sc.Scan(&c.ID, &c.Org, &c.Name, &c.DataType, &minv, &maxv, &cats, &c.CreatedAt, &c.UpdatedAt)
	if minv.Valid {
		v := minv.Float64
		c.MinValue = &v
	}
	if maxv.Valid {
		v := maxv.Float64
		c.MaxValue = &v
	}
	c.Categories = decodeList(cats)
	return c, err
}

func nullFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func (s *Store) UpsertScoreConfig(ctx context.Context, c ScoreConfig) (ScoreConfig, error) {
	if err := dialect.Text(append([]string{c.ID, c.Org, c.Name, c.DataType}, c.Categories...)...); err != nil {
		return ScoreConfig{}, fmt.Errorf("upsert score config: %w", err)
	}
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return ScoreConfig{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var createdAt int64
	row := tx.QueryRowContext(ctx, `SELECT created_at FROM score_configs WHERE org=$1 AND name=$2`, c.Org, c.Name)
	switch err := row.Scan(&createdAt); {
	case errors.Is(err, sql.ErrNoRows):
		c.CreatedAt = c.UpdatedAt
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO score_configs (`+scoreConfigCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			c.ID, c.Org, c.Name, c.DataType, nullFloat(c.MinValue), nullFloat(c.MaxValue),
			encodeList(c.Categories), c.CreatedAt, c.UpdatedAt); err != nil {
			return ScoreConfig{}, fmt.Errorf("insert score config: %w", err)
		}
	case err != nil:
		return ScoreConfig{}, fmt.Errorf("lookup score config: %w", err)
	default:
		c.CreatedAt = createdAt
		if _, err := tx.ExecContext(ctx,
			`UPDATE score_configs SET data_type=$1,min_value=$2,max_value=$3,categories=$4,updated_at=$5 WHERE org=$6 AND name=$7`,
			c.DataType, nullFloat(c.MinValue), nullFloat(c.MaxValue), encodeList(c.Categories), c.UpdatedAt, c.Org, c.Name); err != nil {
			return ScoreConfig{}, fmt.Errorf("update score config: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return ScoreConfig{}, fmt.Errorf("commit: %w", err)
	}
	return c, nil
}

func (s *Store) GetScoreConfig(ctx context.Context, org, name string) (ScoreConfig, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+scoreConfigCols+` FROM score_configs WHERE org=$1 AND name=$2`, org, name)
	c, err := scanScoreConfig(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ScoreConfig{}, errNotFound
	}
	if err != nil {
		return ScoreConfig{}, fmt.Errorf("get score config: %w", err)
	}
	return c, nil
}

func (s *Store) ListScoreConfigs(ctx context.Context, org string, limit int) ([]ScoreConfig, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+scoreConfigCols+` FROM score_configs WHERE org=$1 ORDER BY updated_at DESC, name ASC LIMIT $2`, org, limit)
	if err != nil {
		return nil, fmt.Errorf("list score configs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ScoreConfig
	for rows.Next() {
		c, err := scanScoreConfig(rows)
		if err != nil {
			return nil, fmt.Errorf("scan score config: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ── dataset runs ─────────────────────────────────────────────────────────────

const runCols = `id,org,dataset,name,model,judge_model,items,scored,avg_score,created_at,updated_at`

func scanRun(sc interface{ Scan(...any) error }) (DatasetRun, error) {
	var r DatasetRun
	err := sc.Scan(&r.ID, &r.Org, &r.Dataset, &r.Name, &r.Model, &r.JudgeModel,
		&r.Items, &r.Scored, &r.AvgScore, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

// UpsertRun records or updates a run's metadata by (org,dataset,name). The run
// row is the durable, listable record; its per-item scores live in telemetry.
func (s *Store) UpsertRun(ctx context.Context, r DatasetRun) (DatasetRun, error) {
	if err := dialect.Text(r.ID, r.Org, r.Dataset, r.Name, r.Model, r.JudgeModel); err != nil {
		return DatasetRun{}, fmt.Errorf("upsert run: %w", err)
	}
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return DatasetRun{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var createdAt int64
	row := tx.QueryRowContext(ctx, `SELECT created_at FROM dataset_runs WHERE org=$1 AND dataset=$2 AND name=$3`, r.Org, r.Dataset, r.Name)
	switch err := row.Scan(&createdAt); {
	case errors.Is(err, sql.ErrNoRows):
		r.CreatedAt = r.UpdatedAt
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO dataset_runs (`+runCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			r.ID, r.Org, r.Dataset, r.Name, r.Model, r.JudgeModel, r.Items, r.Scored, r.AvgScore, r.CreatedAt, r.UpdatedAt); err != nil {
			return DatasetRun{}, fmt.Errorf("insert run: %w", err)
		}
	case err != nil:
		return DatasetRun{}, fmt.Errorf("lookup run: %w", err)
	default:
		r.CreatedAt = createdAt
		if _, err := tx.ExecContext(ctx,
			`UPDATE dataset_runs SET model=$1,judge_model=$2,items=$3,scored=$4,avg_score=$5,updated_at=$6 WHERE org=$7 AND dataset=$8 AND name=$9`,
			r.Model, r.JudgeModel, r.Items, r.Scored, r.AvgScore, r.UpdatedAt, r.Org, r.Dataset, r.Name); err != nil {
			return DatasetRun{}, fmt.Errorf("update run: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return DatasetRun{}, fmt.Errorf("commit: %w", err)
	}
	return r, nil
}

func (s *Store) ListRuns(ctx context.Context, org, dataset string, limit int) ([]DatasetRun, error) {
	q := `SELECT ` + runCols + ` FROM dataset_runs WHERE org=$1`
	args := []any{org}
	if dataset != "" {
		q += ` AND dataset=$2`
		args = append(args, dataset)
	}
	q += fmt.Sprintf(` ORDER BY updated_at DESC, name ASC LIMIT $%d`, len(args)+1)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []DatasetRun
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
