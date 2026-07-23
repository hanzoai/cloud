package research

// store.go is the DURABLE WRITE PLANE of Hanzo Research (HIP-0512 §"Two planes,
// named"): one per-org (per HIP-0302, physical-file-isolated) transactional SQLite
// holding the two research tables — experiment + attempt — that mirror the enso-bench
// project's hanzo_research.db (enso-bench/datastore.py). This is the SOURCE OF TRUTH;
// the ClickHouse projection (datastore.go) is a best-effort roll-up over it.
//
// The ORG is the physical tenant boundary — a distinct org resolves to a distinct
// {DataDir}/orgs/{slug}/research.db file, so a query in one org's store can never
// reach another's rows. `project` is a first-class COLUMN, not a second file: the
// producing research projects of one org (enso-bench first; hanzo-engine kernel-perf,
// hanzo-ml training next) share the org's store so the org's ops board aggregates
// across them in ONE cheap query. The stable keys are therefore project-qualified —
// experiment (project, id), attempt (project, benchmark, item, model) — so two
// projects can never collide on a shared id.
//
// IDEMPOTENCY IS THE NO-DATA-LOSS GUARANTEE (HIP-0512 §5):
//   - attempt is APPEND-ONLY + immutable: a re-ingest of the same key is a no-op
//     (INSERT OR IGNORE), so re-running the backfill leaves the counts identical and
//     the raw response is never overwritten.
//   - experiment is LATEST-RUN-CANONICAL: a re-ingest replaces the row only when its
//     run ts is at least as recent, so the stored number is deterministically the
//     latest complete run, never cherry-picked, and an out-of-order replay of an older
//     run cannot regress it.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// store is one org's research plane over a single SQLite file. MaxOpenConns(1) — set
// by cloud.OrgDB — serializes writes against the file lock, so a batch ingest is one
// transaction with no reader/writer race.
type store struct {
	db *sql.DB
}

// openStore wraps an org DB — already opened + pragma'd by cloud.OrgDB — into the
// research store, running its migration. It is the open func cloud.OrgStore calls once
// per org file.
func openStore(db *sql.DB) (*store, error) {
	s := &store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// migrate creates the two research tables. They mirror enso-bench/datastore.py plus
// the columns the plane needs cloud-side: `project` (the org's sub-dimension), and on
// experiment `ts` (the latest-run-canonical version), `status` (the run's state — see
// runStatus), and `n_total` (the run's item target, so the ops board can show
// done=n vs remaining=n_total−n once the durable-execution phase populates in-flight
// runs).
func (s *store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS experiment (
  project   TEXT NOT NULL DEFAULT 'default',
  id        TEXT NOT NULL,
  kind      TEXT NOT NULL,
  subject   TEXT NOT NULL,
  task      TEXT,
  metric    TEXT,
  value     REAL,
  n         INTEGER,
  n_total   INTEGER NOT NULL DEFAULT 0,
  cost_usd  REAL,
  status    TEXT NOT NULL DEFAULT 'complete',
  meta      TEXT,
  ts        INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (project, id)
);
CREATE TABLE IF NOT EXISTS attempt (
  project   TEXT NOT NULL DEFAULT 'default',
  benchmark TEXT NOT NULL,
  item      TEXT NOT NULL,
  model     TEXT NOT NULL,
  gold      TEXT,
  answer    TEXT,
  correct   INTEGER NOT NULL,
  response  TEXT,
  source    TEXT NOT NULL DEFAULT 'hanzo-measured',
  PRIMARY KEY (project, benchmark, item, model)
);
CREATE INDEX IF NOT EXISTS ix_attempt_proj_bench ON attempt(project, benchmark);
CREATE INDEX IF NOT EXISTS ix_exp_proj_kind ON experiment(project, kind);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("research migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *store) Close() error { return s.db.Close() }

// ingest writes one batch — experiments and attempts, all under project — in a single
// transaction, so a crash mid-batch leaves the store either fully-before or
// fully-after, never partway. project is the SERVER's value (principal.Project),
// stamped on every row here; a payload cannot carry it. Returns how many rows each
// table gained (a re-ingest of already-present keys gains 0 — the idempotency the
// double-backfill test asserts).
func (s *store) ingest(ctx context.Context, project string, exps []Experiment, atts []Attempt) (expAdded, attAdded int, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("research ingest begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for i := range exps {
		added, e := putExperimentTx(ctx, tx, project, exps[i])
		if e != nil {
			return 0, 0, e
		}
		expAdded += added
	}
	for i := range atts {
		added, e := putAttemptTx(ctx, tx, project, atts[i])
		if e != nil {
			return 0, 0, e
		}
		attAdded += added
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("research ingest commit: %w", err)
	}
	return expAdded, attAdded, nil
}

// putExperimentTx upserts one experiment latest-run-canonical: the row is written on
// first sight and REPLACED on a later re-ingest only when the incoming run ts is at
// least as recent (excluded.ts >= experiment.ts). An older replayed run therefore
// cannot regress a newer stored number, and a same-run re-ingest is a no-op — the
// deterministic "latest complete run" the HIP requires. Returns 1 only when a new
// (project,id) was inserted.
func putExperimentTx(ctx context.Context, tx *sql.Tx, project string, e Experiment) (int, error) {
	meta := string(e.Meta)
	if meta == "" {
		meta = "{}"
	}
	status := runStatus(e.Status)
	var existed int
	// COUNT before the upsert distinguishes an insert (added=1) from a replace/no-op
	// (added=0) without depending on RowsAffected, whose meaning varies for ON
	// CONFLICT across drivers.
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM experiment WHERE project=? AND id=?`, project, e.ID).Scan(&existed); err != nil {
		return 0, fmt.Errorf("research experiment probe: %w", err)
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO experiment (project, id, kind, subject, task, metric, value, n, n_total, cost_usd, status, meta, ts)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(project, id) DO UPDATE SET
  kind=excluded.kind, subject=excluded.subject, task=excluded.task,
  metric=excluded.metric, value=excluded.value, n=excluded.n, n_total=excluded.n_total,
  cost_usd=excluded.cost_usd, status=excluded.status, meta=excluded.meta, ts=excluded.ts
WHERE excluded.ts >= experiment.ts`,
		project, e.ID, e.Kind, e.Subject, e.Task, e.Metric, e.Value, e.N, e.NTotal, e.CostUSD, status, meta, e.TS)
	if err != nil {
		return 0, fmt.Errorf("research experiment upsert: %w", err)
	}
	if existed == 0 {
		return 1, nil
	}
	return 0, nil
}

// putAttemptTx appends one attempt idempotently by its stable (project, benchmark,
// item, model) key: INSERT OR IGNORE, so the raw response is immutable (first write
// wins) and re-running the backfill never duplicates or loses a row. Returns rows
// added (0 when the key was already present).
func putAttemptTx(ctx context.Context, tx *sql.Tx, project string, a Attempt) (int, error) {
	src := a.Source
	if src == "" {
		src = "hanzo-measured"
	}
	res, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO attempt (project, benchmark, item, model, gold, answer, correct, response, source)
VALUES (?,?,?,?,?,?,?,?,?)`,
		project, a.Benchmark, a.Item, a.Model, a.Gold, a.Answer, boolInt(a.Correct), a.Response, src)
	if err != nil {
		return 0, fmt.Errorf("research attempt insert: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil // driver without RowsAffected: count is best-effort, not load-bearing
	}
	return int(n), nil
}

// listExperiments returns experiments latest-run-canonical (one row per stable id,
// read mechanically), filtered by project ("" = every project in the org, the ops
// board's cross-project view) and optionally kind, ordered deterministically.
func (s *store) listExperiments(ctx context.Context, project, kind string) ([]Experiment, error) {
	q := `SELECT project, id, kind, subject, task, metric, value, n, n_total, cost_usd, status, meta, ts FROM experiment`
	var conds []string
	var args []any
	if project != "" {
		conds = append(conds, "project=?")
		args = append(args, project)
	}
	if kind != "" {
		conds = append(conds, "kind=?")
		args = append(args, kind)
	}
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += ` ORDER BY project, id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("research list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Experiment
	for rows.Next() {
		var e Experiment
		var meta string
		if err := rows.Scan(&e.Project, &e.ID, &e.Kind, &e.Subject, &e.Task, &e.Metric, &e.Value, &e.N, &e.NTotal, &e.CostUSD, &e.Status, &meta, &e.TS); err != nil {
			return nil, fmt.Errorf("research scan: %w", err)
		}
		e.Meta = json.RawMessage(meta)
		out = append(out, e)
	}
	return out, rows.Err()
}

// counts reports the store's whole-org totals — the figures the ingest response
// returns so the uploader can print before/after and prove idempotency.
func (s *store) counts(ctx context.Context) (experiments, attempts int, err error) {
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM experiment`).Scan(&experiments); err != nil {
		return 0, 0, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM attempt`).Scan(&attempts); err != nil {
		return 0, 0, err
	}
	return experiments, attempts, nil
}

// ── ops-board aggregates (HIP-0512 §"Hanzo Research"; the observatory reads these) ──

// ProjectSummary is one research project's real totals — the ops board's "every
// project + real totals" row. Deterministic, computed from the store (never
// estimated). Tokens are intentionally absent: the current corpus does not carry
// per-attempt token usage (enso-bench's datastore.py drops usage at db-build time), so
// reporting a token total would be a fabricated zero; cost_usd (from the experiment
// row) is the real spend figure the corpus does carry.
type ProjectSummary struct {
	Project     string   `json:"project"`
	Experiments int      `json:"experiments"`
	Attempts    int      `json:"attempts"`
	Models      int      `json:"models"`
	Benchmarks  int      `json:"benchmarks"`
	CostUSD     float64  `json:"cost_usd"`
	Kinds       []string `json:"kinds"`
}

// projectSummaries folds the two tables into one per-project summary each. Two grouped
// index scans (experiment by project, attempt by project) merged in Go — cheap enough
// to poll every few seconds. Projects with only experiments (kernel-perf, training)
// and projects with only attempts both surface.
func (s *store) projectSummaries(ctx context.Context) ([]ProjectSummary, error) {
	byProj := map[string]*ProjectSummary{}
	get := func(p string) *ProjectSummary {
		if byProj[p] == nil {
			byProj[p] = &ProjectSummary{Project: p}
		}
		return byProj[p]
	}
	erows, err := s.db.QueryContext(ctx,
		`SELECT project, COUNT(*), COALESCE(SUM(cost_usd),0), GROUP_CONCAT(DISTINCT kind) FROM experiment GROUP BY project`)
	if err != nil {
		return nil, fmt.Errorf("research project experiments: %w", err)
	}
	for erows.Next() {
		var p, kinds string
		var n int
		var cost float64
		if err := erows.Scan(&p, &n, &cost, &kinds); err != nil {
			_ = erows.Close()
			return nil, err
		}
		ps := get(p)
		ps.Experiments, ps.CostUSD, ps.Kinds = n, cost, splitKinds(kinds)
	}
	_ = erows.Close()

	arows, err := s.db.QueryContext(ctx,
		`SELECT project, COUNT(*), COUNT(DISTINCT model), COUNT(DISTINCT benchmark) FROM attempt GROUP BY project`)
	if err != nil {
		return nil, fmt.Errorf("research project attempts: %w", err)
	}
	for arows.Next() {
		var p string
		var atts, models, benches int
		if err := arows.Scan(&p, &atts, &models, &benches); err != nil {
			_ = arows.Close()
			return nil, err
		}
		ps := get(p)
		ps.Attempts, ps.Models, ps.Benchmarks = atts, models, benches
	}
	_ = arows.Close()

	out := make([]ProjectSummary, 0, len(byProj))
	for _, ps := range byProj {
		out = append(out, *ps)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Project < out[j].Project })
	return out, nil
}

// KindTotal is the per-kind slice of a totals aggregate.
type KindTotal struct {
	Kind        string  `json:"kind"`
	Experiments int     `json:"experiments"`
	CostUSD     float64 `json:"cost_usd"`
}

// Totals is the observatory's headline aggregate over the org (or one project when
// scoped): the real totals plus a per-kind breakdown, deterministic from the store.
type Totals struct {
	Project     string      `json:"project,omitempty"`
	Projects    int         `json:"projects"`
	Experiments int         `json:"experiments"`
	Attempts    int         `json:"attempts"`
	Models      int         `json:"models"`
	Benchmarks  int         `json:"benchmarks"`
	CostUSD     float64     `json:"cost_usd"`
	ByKind      []KindTotal `json:"by_kind"`
}

// totals computes the headline aggregate for the org, or for one project when project
// != "". Cheap grouped scans; the same shape the OLAP roll-up serves for the
// real-time cross-org observatory once the datastore is live.
func (s *store) totals(ctx context.Context, project string) (Totals, error) {
	where := ""
	var args []any
	if project != "" {
		where = " WHERE project=?"
		args = append(args, project)
	}
	var t Totals
	t.Project = project
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COUNT(DISTINCT project), COALESCE(SUM(cost_usd),0) FROM experiment`+where, args...).
		Scan(&t.Experiments, &t.Projects, &t.CostUSD); err != nil {
		return Totals{}, fmt.Errorf("research totals experiments: %w", err)
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COUNT(DISTINCT model), COUNT(DISTINCT benchmark) FROM attempt`+where, args...).
		Scan(&t.Attempts, &t.Models, &t.Benchmarks); err != nil {
		return Totals{}, fmt.Errorf("research totals attempts: %w", err)
	}
	krows, err := s.db.QueryContext(ctx,
		`SELECT kind, COUNT(*), COALESCE(SUM(cost_usd),0) FROM experiment`+where+` GROUP BY kind ORDER BY kind`, args...)
	if err != nil {
		return Totals{}, fmt.Errorf("research totals by kind: %w", err)
	}
	defer func() { _ = krows.Close() }()
	for krows.Next() {
		var kt KindTotal
		if err := krows.Scan(&kt.Kind, &kt.Experiments, &kt.CostUSD); err != nil {
			return Totals{}, err
		}
		t.ByKind = append(t.ByKind, kt)
	}
	return t, krows.Err()
}

// splitKinds turns SQLite's GROUP_CONCAT(DISTINCT kind) CSV into a sorted slice.
func splitKinds(csv string) []string {
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	sort.Strings(parts)
	return parts
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
