package research

// store.go is the durable WRITE PLANE of Hanzo Research (HIP-0512 §"Two planes,
// named"): one per-org (per HIP-0302, physical-file-isolated) transactional SQLite
// holding the two research tables — experiment + attempt. This is the local source of
// truth; the ClickHouse projection (datastore.go) is a best-effort roll-up over it.
//
// HONEST DURABILITY LABEL — the plane is VERSIONED · APPEND-ONLY; cloud mirroring is
// ROLLING OUT. It is NOT yet "immutable · hash-addressed · replicated ·
// recovery-tested" — that stronger claim is earned only after the retry +
// reconciliation + replication + backup/restore path (the uploader's roll-up, still on
// the critical path) is built and tested. Comments here say what is true TODAY.
//
// VERSIONED, NOT WRITE-ONCE. Every completed run is a versioned record. A correction
// does not mutate the prior row — it APPENDS a new version under the SAME stable id; the
// prior version is RETAINED (superseded), never deleted. Two orthogonal axes travel on
// every row:
//   - revision ∈ {original, corrected, retracted} — the producer-declared kind of this
//     version. `superseded` and `canonical` are DERIVED (a version is canonical iff it
//     is the latest non-retracted version of its stable id; any earlier non-retracted
//     version is superseded).
//   - status   ∈ {planning, running, complete, faulted} — the run's execution state.
//     faulted/failed runs are RETAINED — negative results are evidence.
//
// Version identity is content_hash (server-computed over the measurement AND its
// provenance — git sha/branch/dirty, lib versions): re-ingesting byte-identical content
// is a no-op (idempotent backfill), while a corrected number OR a run on a new commit /
// lib version is new content → a new RETAINED version. RETAINED is the full history (the
// truth); CANONICAL is the deterministic deduped view over it, so dedup never reads as
// loss.
//
// PROVENANCE is first-class + queryable (not buried in meta): project (which project
// produced the run), git_sha / git_branch / git_dirty (indexed), and lib_versions (a
// structured JSON object) — so the board can answer "which lib version regressed
// LiveCodeBench" over the longitudinal record.
//
// RETENTION CLASSES are separable, not one opaque blob: run metadata + provenance
// (experiment.* incl. git/lib_versions, meta) is nonpersonal and permanent; raw
// artifacts (attempt.response / answer / gold) carry their own retention and may hold
// personal or licensed material; SECRETS are NEVER stored here (credentials are KMS
// references, per HIP-0512 §5).
//
// VISIBILITY + CONSENT are private-by-default and SEPARATE from upload: ingest always
// records visibility='private', trainable=0, publishable=0 — public board visibility,
// training rights, and commons publication are each a distinct authorized grant
// (setGrant), never implied by uploading a run.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// store is one org's research plane over a single SQLite file. MaxOpenConns(1) — set by
// cloud.OrgDB — serializes writes against the file lock, so a batch ingest is one
// transaction with no reader/writer race.
type store struct {
	db *sql.DB
}

func openStore(db *sql.DB) (*store, error) {
	s := &store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// migrate creates the two versioned tables. seq is the append clock (canonical picks the
// latest version); UNIQUE(stable key, content_hash) makes a re-ingest of identical
// content idempotent while a correction (new content) appends a new version. Provenance
// columns (git_sha, git_branch, git_dirty, lib_versions) are indexed/queryable.
func (s *store) migrate() error {
	const tables = `
CREATE TABLE IF NOT EXISTS experiment (
  seq          INTEGER PRIMARY KEY AUTOINCREMENT,
  project      TEXT NOT NULL DEFAULT 'default',
  id           TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  revision     TEXT NOT NULL DEFAULT 'original',
  status       TEXT NOT NULL DEFAULT 'complete',
  visibility   TEXT NOT NULL DEFAULT 'private',
  trainable    INTEGER NOT NULL DEFAULT 0,
  publishable  INTEGER NOT NULL DEFAULT 0,
  kind         TEXT NOT NULL,
  subject      TEXT NOT NULL,
  task         TEXT,
  metric       TEXT,
  value        REAL,
  n            INTEGER,
  n_total      INTEGER NOT NULL DEFAULT 0,
  cost_usd     REAL,
  meta         TEXT,
  git_sha      TEXT NOT NULL DEFAULT '',
  git_branch   TEXT NOT NULL DEFAULT '',
  git_dirty    INTEGER NOT NULL DEFAULT 0,
  lib_versions TEXT NOT NULL DEFAULT '{}',
  ts           INTEGER NOT NULL DEFAULT 0,
  UNIQUE (project, id, content_hash)
);
CREATE TABLE IF NOT EXISTS attempt (
  seq          INTEGER PRIMARY KEY AUTOINCREMENT,
  project      TEXT NOT NULL DEFAULT 'default',
  benchmark    TEXT NOT NULL,
  item         TEXT NOT NULL,
  model        TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  revision     TEXT NOT NULL DEFAULT 'original',
  status       TEXT NOT NULL DEFAULT 'complete',
  gold         TEXT,
  answer       TEXT,
  correct      INTEGER NOT NULL,
  response     TEXT,
  source       TEXT NOT NULL DEFAULT 'hanzo-measured',
  ts           INTEGER NOT NULL DEFAULT 0,
  UNIQUE (project, benchmark, item, model, content_hash)
);
CREATE TABLE IF NOT EXISTS artifact (
  sha256          TEXT PRIMARY KEY,
  kind            TEXT NOT NULL,
  ref             TEXT NOT NULL,
  content         BLOB,
  run_id          TEXT NOT NULL DEFAULT '',
  project         TEXT NOT NULL DEFAULT 'default',
  visibility      TEXT NOT NULL DEFAULT 'private',
  retention_class TEXT NOT NULL DEFAULT 'raw-artifact',
  git_sha         TEXT NOT NULL DEFAULT '',
  git_branch      TEXT NOT NULL DEFAULT '',
  git_dirty       INTEGER NOT NULL DEFAULT 0,
  lib_versions    TEXT NOT NULL DEFAULT '{}',
  ts              INTEGER NOT NULL DEFAULT 0
);
`
	if _, err := s.db.Exec(tables); err != nil {
		return fmt.Errorf("research migrate tables: %w", err)
	}
	// Converge a table created by an OLDER schema to the current column set. CREATE TABLE
	// IF NOT EXISTS never alters an existing table, so a column added after the table was
	// first created must be added by ALTER — otherwise a later statement (an index or an
	// insert) referencing it fails "no such column". A duplicate-column error means the
	// column is already present (the desired state) and is ignored. Every current non-key
	// column carries a DEFAULT, so ADD COLUMN is always valid. This makes migrate()
	// idempotent across schema evolution: adding a column = adding it to the CREATE above
	// AND this list.
	ensure := []struct{ table, col string }{
		{"experiment", "content_hash TEXT NOT NULL DEFAULT ''"},
		{"experiment", "revision TEXT NOT NULL DEFAULT 'original'"},
		{"experiment", "status TEXT NOT NULL DEFAULT 'complete'"},
		{"experiment", "visibility TEXT NOT NULL DEFAULT 'private'"},
		{"experiment", "trainable INTEGER NOT NULL DEFAULT 0"},
		{"experiment", "publishable INTEGER NOT NULL DEFAULT 0"},
		{"experiment", "metric TEXT"},
		{"experiment", "value REAL"},
		{"experiment", "n INTEGER"},
		{"experiment", "n_total INTEGER NOT NULL DEFAULT 0"},
		{"experiment", "cost_usd REAL"},
		{"experiment", "meta TEXT"},
		{"experiment", "git_sha TEXT NOT NULL DEFAULT ''"},
		{"experiment", "git_branch TEXT NOT NULL DEFAULT ''"},
		{"experiment", "git_dirty INTEGER NOT NULL DEFAULT 0"},
		{"experiment", "lib_versions TEXT NOT NULL DEFAULT '{}'"},
		{"experiment", "ts INTEGER NOT NULL DEFAULT 0"},
		{"attempt", "content_hash TEXT NOT NULL DEFAULT ''"},
		{"attempt", "revision TEXT NOT NULL DEFAULT 'original'"},
		{"attempt", "status TEXT NOT NULL DEFAULT 'complete'"},
		{"attempt", "source TEXT NOT NULL DEFAULT 'hanzo-measured'"},
		{"attempt", "ts INTEGER NOT NULL DEFAULT 0"},
		{"artifact", "run_id TEXT NOT NULL DEFAULT ''"},
		{"artifact", "visibility TEXT NOT NULL DEFAULT 'private'"},
		{"artifact", "retention_class TEXT NOT NULL DEFAULT 'raw-artifact'"},
		{"artifact", "git_sha TEXT NOT NULL DEFAULT ''"},
		{"artifact", "git_branch TEXT NOT NULL DEFAULT ''"},
		{"artifact", "git_dirty INTEGER NOT NULL DEFAULT 0"},
		{"artifact", "lib_versions TEXT NOT NULL DEFAULT '{}'"},
		{"artifact", "ts INTEGER NOT NULL DEFAULT 0"},
	}
	for _, e := range ensure {
		if _, err := s.db.Exec("ALTER TABLE " + e.table + " ADD COLUMN " + e.col); err != nil &&
			!strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("research migrate ensure %s.%s: %w", e.table, e.col, err)
		}
	}
	const indexes = `
CREATE INDEX IF NOT EXISTS ix_exp_key ON experiment(project, id);
CREATE INDEX IF NOT EXISTS ix_att_key ON attempt(project, benchmark, item, model);
CREATE INDEX IF NOT EXISTS ix_exp_proj_kind ON experiment(project, kind);
CREATE INDEX IF NOT EXISTS ix_exp_git ON experiment(git_sha);
CREATE INDEX IF NOT EXISTS ix_exp_branch ON experiment(git_branch);
CREATE INDEX IF NOT EXISTS ix_art_run ON artifact(project, run_id);
CREATE INDEX IF NOT EXISTS ix_art_ts ON artifact(project, ts);
`
	if _, err := s.db.Exec(indexes); err != nil {
		return fmt.Errorf("research migrate indexes: %w", err)
	}
	return nil
}

func (s *store) Close() error { return s.db.Close() }

// ── content hashes (version identity) ────────────────────────────────────────

// hashExp is an experiment version's content identity: the REVISION (original /
// corrected / retracted), the substantive measurement, AND its PROVENANCE (git
// sha/branch/dirty, lib versions). revision is part of the identity so a RETRACTION of an
// otherwise-identical run is a DISTINCT version (not an INSERT-OR-IGNORE no-op) — without
// it a retraction silently vanishes. Provenance is part of the identity so the SAME
// number on a different commit or lib version is a distinct RETAINED version. Meta and
// lib_versions are canonicalized (sorted-key, whitespace-free) so a re-serialization does
// not mint a spurious version. visibility/ts are NOT identity (a grant / a re-observation
// is not a new version).
func hashExp(e Experiment) string {
	return hashJoin(revisionOf(e.Revision), e.Kind, e.Subject, e.Task, e.Metric,
		strconv.FormatFloat(e.Value, 'g', -1, 64), strconv.Itoa(e.N), strconv.Itoa(e.NTotal),
		strconv.FormatFloat(e.CostUSD, 'g', -1, 64), runStatus(e.Status), canonJSON(e.Meta),
		e.GitSHA, e.GitBranch, strconv.Itoa(boolInt(e.GitDirty)), canonJSON(e.LibVersions))
}

// hashAtt is an attempt version's content identity: the revision + the measured artifact
// (gold, answer, correctness, raw response, source, status). A re-scored/re-run/retracted
// attempt is a new version; a byte-identical re-ingest is idempotent.
func hashAtt(a Attempt) string {
	return hashJoin(revisionOf(a.Revision), a.Gold, a.Answer, strconv.Itoa(boolInt(a.Correct)),
		a.Response, sourceOf(a.Source), runStatus(a.Status))
}

func hashJoin(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ── ingest (append versions, idempotent by content) ──────────────────────────

// ingest appends one batch — experiments and attempts under project — in a single
// transaction. project is the SERVER's value (principal.Project), stamped here; a
// payload cannot carry it. Visibility/consent are FORCED to private/withheld — upload
// never grants them. Returns how many NEW versions each table gained (a re-ingest of
// identical content gains 0 — the idempotency the double-backfill asserts).
func (s *store) ingest(ctx context.Context, project string, exps []Experiment, atts []Attempt) (expAdded, attAdded int, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("research ingest begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for i := range exps {
		e := exps[i]
		res, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO experiment
  (project, id, content_hash, revision, status, visibility, trainable, publishable,
   kind, subject, task, metric, value, n, n_total, cost_usd, meta,
   git_sha, git_branch, git_dirty, lib_versions, ts)
VALUES (?,?,?,?,?, 'private', 0, 0, ?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			project, e.ID, hashExp(e), revisionOf(e.Revision), runStatus(e.Status),
			e.Kind, e.Subject, e.Task, e.Metric, e.Value, e.N, e.NTotal, e.CostUSD, jsonObj(e.Meta),
			e.GitSHA, e.GitBranch, boolInt(e.GitDirty), jsonObj(e.LibVersions), e.TS)
		if err != nil {
			return 0, 0, fmt.Errorf("research experiment insert: %w", err)
		}
		expAdded += affected(res)
	}
	for i := range atts {
		a := atts[i]
		res, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO attempt
  (project, benchmark, item, model, content_hash, revision, status, gold, answer, correct, response, source, ts)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			project, a.Benchmark, a.Item, a.Model, hashAtt(a), revisionOf(a.Revision), runStatus(a.Status),
			a.Gold, a.Answer, boolInt(a.Correct), a.Response, sourceOf(a.Source), a.TS)
		if err != nil {
			return 0, 0, fmt.Errorf("research attempt insert: %w", err)
		}
		attAdded += affected(res)
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("research ingest commit: %w", err)
	}
	return expAdded, attAdded, nil
}

func affected(res sql.Result) int {
	n, err := res.RowsAffected()
	if err != nil {
		return 0
	}
	return int(n)
}

// ── canonical predicates (the ONE definition of "canonical") ─────────────────
//
// A version is canonical iff it is THE single latest APPENDED version (max seq) of its
// stable id AND that latest version is not retracted. If the latest is a retraction, the
// id has NO canonical — WITHDRAWN (a retraction is honored, not ignored).
//
// SEQ, NOT CLIENT TS, IS THE SUPERSESSION AUTHORITY (N2). A client ts is untrustworthy as
// an ordering key: the backfill importer stamps a wall-clock ts (~1.7e9) while an SDK live
// record sends none (ts=0), so ordering by ts would sort an SDK correction/retraction
// (ts=0) BEFORE a backfilled experiment (ts=big) and it would NEVER supersede — the stale
// value would stay canonical while the producer sees added=1 (looks like success). seq is
// assigned by the store on APPEND, so latest-APPEND-wins is correct across every producer
// and un-forgeable by a client. ts is retained as display-only (measured-at). "latest" is
// over ALL versions (the NOT EXISTS does not filter revision), so a retraction that is the
// newest append wins and no earlier version resurfaces. Every version is retained.

const canonicalExpWhere = `e.revision != 'retracted' AND NOT EXISTS (
  SELECT 1 FROM experiment e2 WHERE e2.project=e.project AND e2.id=e.id AND e2.seq > e.seq)`

const canonicalAttWhere = `a.revision != 'retracted' AND NOT EXISTS (
  SELECT 1 FROM attempt a2 WHERE a2.project=a.project AND a2.benchmark=a.benchmark
    AND a2.item=a.item AND a2.model=a.model AND a2.seq > a.seq)`

// A canonical COUNT is the ANSWERED latest-run view: the canonical version whose status
// is a completed result. faulted/failed runs are RETAINED (counted in *_retained) but
// are NOT the answered headline — this is exactly the retained-minus-canonical gap (a
// faulted-only key is retained history, not an answered result). listExperiments still
// RETURNS the canonical version whatever its status, so the board can render faults.
const answeredExp = ` AND e.status NOT IN ('faulted','failed')`
const answeredAtt = ` AND a.status NOT IN ('faulted','failed')`

// canonAnsExp / canonAnsAtt select the ANSWERED canonical version — the latest-version
// dedup AND a completed status. Ops-board headline counts use these; *_retained use raw
// COUNT(*), so the retained-minus-canonical gap is exactly the superseded + faulted rows.
const canonAnsExp = canonicalExpWhere + answeredExp
const canonAnsAtt = canonicalAttWhere + answeredAtt

// ── counts (retained = truth; canonical = deduped view) ──────────────────────

// Counts carries both axes for both tables. retained is the full versioned history (the
// truth); canonical is the distinct-stable-id deduped view. retained ≥ canonical always,
// and the gap is superseded/faulted versions — never lost data.
type Counts struct {
	ExperimentsRetained  int `json:"experiments_retained"`
	ExperimentsCanonical int `json:"canonical_experiments"`
	AttemptsRetained     int `json:"attempts_retained"`
	AttemptsCanonical    int `json:"canonical_attempts"`
}

func (s *store) counts(ctx context.Context) (Counts, error) {
	var c Counts
	q := func(dst *int, sql string) error { return s.db.QueryRowContext(ctx, sql).Scan(dst) }
	if err := q(&c.ExperimentsRetained, `SELECT COUNT(*) FROM experiment`); err != nil {
		return c, err
	}
	if err := q(&c.ExperimentsCanonical, `SELECT COUNT(*) FROM experiment e WHERE `+canonicalExpWhere+answeredExp); err != nil {
		return c, err
	}
	if err := q(&c.AttemptsRetained, `SELECT COUNT(*) FROM attempt`); err != nil {
		return c, err
	}
	if err := q(&c.AttemptsCanonical, `SELECT COUNT(*) FROM attempt a WHERE `+canonicalAttWhere+answeredAtt); err != nil {
		return c, err
	}
	return c, nil
}

// listExperiments returns the CANONICAL experiments (latest non-retracted version per
// stable id), with provenance, filtered by project ("" = every project in the org, the
// ops board's cross-project view) and optionally kind, ordered deterministically.
func (s *store) listExperiments(ctx context.Context, project, kind string) ([]Experiment, error) {
	q := `SELECT e.project, e.id, e.revision, e.status, e.visibility, e.trainable, e.publishable,
  e.kind, e.subject, e.task, e.metric, e.value, e.n, e.n_total, e.cost_usd, e.meta,
  e.git_sha, e.git_branch, e.git_dirty, e.lib_versions, e.ts
FROM experiment e WHERE ` + canonicalExpWhere
	var args []any
	if project != "" {
		q += ` AND e.project=?`
		args = append(args, project)
	}
	if kind != "" {
		q += ` AND e.kind=?`
		args = append(args, kind)
	}
	q += ` ORDER BY e.project, e.id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("research list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Experiment
	for rows.Next() {
		var e Experiment
		var meta, libs string
		var trainable, publishable, gitDirty int
		if err := rows.Scan(&e.Project, &e.ID, &e.Revision, &e.Status, &e.Visibility, &trainable, &publishable,
			&e.Kind, &e.Subject, &e.Task, &e.Metric, &e.Value, &e.N, &e.NTotal, &e.CostUSD, &meta,
			&e.GitSHA, &e.GitBranch, &gitDirty, &libs, &e.TS); err != nil {
			return nil, fmt.Errorf("research scan: %w", err)
		}
		e.Meta = json.RawMessage(meta)
		e.LibVersions = json.RawMessage(libs)
		e.Canonical = true
		e.Trainable = trainable != 0
		e.Publishable = publishable != 0
		e.GitDirty = gitDirty != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

// ── grants (visibility + consent — SEPARATE from upload) ──────────────────────

// setGrant records an authorized visibility/consent decision for a stable id's records —
// the SEPARATE authorization upload never implies. It updates every retained version of
// the id (the decision is about the run, not one version). A nil pointer leaves that
// field unchanged. Returns rows touched.
func (s *store) setGrant(ctx context.Context, project, id string, visibility *string, trainable, publishable *bool) (int, error) {
	var sets []string
	var args []any
	if visibility != nil {
		sets = append(sets, "visibility=?")
		args = append(args, *visibility)
	}
	if trainable != nil {
		sets = append(sets, "trainable=?")
		args = append(args, boolInt(*trainable))
	}
	if publishable != nil {
		sets = append(sets, "publishable=?")
		args = append(args, boolInt(*publishable))
	}
	if len(sets) == 0 {
		return 0, nil
	}
	args = append(args, project, id)
	res, err := s.db.ExecContext(ctx, `UPDATE experiment SET `+strings.Join(sets, ", ")+` WHERE project=? AND id=?`, args...)
	if err != nil {
		return 0, fmt.Errorf("research grant: %w", err)
	}
	return affected(res), nil
}

// ── artifacts (the research diary — raw-artifact retention class) ─────────────

// putArtifact records one artifact idempotently by its sha256, storing the BYTES the
// server hashed. The sha256 is SERVER-DERIVED (see postArtifact) — never client-asserted —
// so the artifact is genuinely hash-addressed and a poisoned first-write is impossible (a
// distinct hash would need a preimage). project is the SERVER's value; visibility is
// FORCED private (a grant is separate). Returns 1 when a new artifact was recorded, 0 on a
// dup (the same bytes → the same hash → a no-op).
func (s *store) putArtifact(ctx context.Context, project string, a Artifact, content []byte) (int, error) {
	res, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO artifact
  (sha256, kind, ref, content, run_id, project, visibility, retention_class, git_sha, git_branch, git_dirty, lib_versions, ts)
VALUES (?,?,?,?,?,?, 'private', 'raw-artifact', ?,?,?,?,?)`,
		a.SHA256, a.Kind, a.Ref, content, a.RunID, project,
		a.GitSHA, a.GitBranch, boolInt(a.GitDirty), jsonObj(a.LibVersions), a.TS)
	if err != nil {
		return 0, fmt.Errorf("research artifact insert: %w", err)
	}
	return affected(res), nil
}

// artifactContent returns one artifact's stored bytes + kind, hash-addressed by sha256 and
// org-scoped (the file IS the org). ok is false for an unknown hash.
func (s *store) artifactContent(ctx context.Context, project, sha256 string) ([]byte, string, bool) {
	var content []byte
	var kind string
	q := `SELECT content, kind FROM artifact WHERE sha256=?`
	args := []any{sha256}
	if project != "" {
		q += ` AND project=?`
		args = append(args, project)
	}
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&content, &kind); err != nil {
		return nil, "", false
	}
	return content, kind, true
}

// listArtifacts returns the chronological diary feed newest-first, filtered by project,
// optional run_id, and an optional `since` unix-seconds lower bound. Bounded by limit.
func (s *store) listArtifacts(ctx context.Context, project, runID string, since int64, limit int) ([]Artifact, error) {
	q := `SELECT sha256, kind, ref, run_id, project, visibility, retention_class,
  git_sha, git_branch, git_dirty, lib_versions, ts FROM artifact WHERE 1=1`
	var args []any
	if project != "" {
		q += ` AND project=?`
		args = append(args, project)
	}
	if runID != "" {
		q += ` AND run_id=?`
		args = append(args, runID)
	}
	if since > 0 {
		q += ` AND ts >= ?`
		args = append(args, since)
	}
	q += ` ORDER BY ts DESC, sha256 LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("research artifacts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Artifact
	for rows.Next() {
		var a Artifact
		var libs string
		var gitDirty int
		if err := rows.Scan(&a.SHA256, &a.Kind, &a.Ref, &a.RunID, &a.Project, &a.Visibility, &a.RetentionClass,
			&a.GitSHA, &a.GitBranch, &gitDirty, &libs, &a.TS); err != nil {
			return nil, fmt.Errorf("research artifact scan: %w", err)
		}
		a.LibVersions = json.RawMessage(libs)
		a.GitDirty = gitDirty != 0
		out = append(out, a)
	}
	return out, rows.Err()
}

// setArtifactVisibility records the SEPARATE visibility grant for one artifact by sha256
// — the same private-by-default rule as runs. Returns rows touched.
func (s *store) setArtifactVisibility(ctx context.Context, project, sha256, visibility string) (int, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE artifact SET visibility=? WHERE project=? AND sha256=?`, visibility, project, sha256)
	if err != nil {
		return 0, fmt.Errorf("research artifact grant: %w", err)
	}
	return affected(res), nil
}

// ── ops-board aggregates ─────────────────────────────────────────────────────

// ProjectSummary is one project's real totals — the ops board's "every project + real
// totals" row. Canonical counts are the deduped view; *_retained expose the full
// versioned history so dedup never reads as loss. Tokens are intentionally absent (the
// corpus carries no per-attempt token usage); cost_usd is the real spend the corpus has.
type ProjectSummary struct {
	Project             string   `json:"project"`
	Experiments         int      `json:"experiments"` // canonical
	ExperimentsRetained int      `json:"experiments_retained"`
	Attempts            int      `json:"attempts"` // canonical
	AttemptsRetained    int      `json:"attempts_retained"`
	Models              int      `json:"models"`
	Benchmarks          int      `json:"benchmarks"`
	CostUSD             float64  `json:"cost_usd"`
	Kinds               []string `json:"kinds"`
}

func (s *store) projectSummaries(ctx context.Context) ([]ProjectSummary, error) {
	byProj := map[string]*ProjectSummary{}
	get := func(p string) *ProjectSummary {
		if byProj[p] == nil {
			byProj[p] = &ProjectSummary{Project: p}
		}
		return byProj[p]
	}
	erows, err := s.db.QueryContext(ctx, `SELECT e.project,
  SUM(CASE WHEN `+canonAnsExp+` THEN 1 ELSE 0 END),
  COUNT(*),
  COALESCE(SUM(CASE WHEN `+canonAnsExp+` THEN e.cost_usd ELSE 0 END),0),
  GROUP_CONCAT(DISTINCT e.kind)
FROM experiment e GROUP BY e.project`)
	if err != nil {
		return nil, fmt.Errorf("research project experiments: %w", err)
	}
	for erows.Next() {
		var p, kinds string
		var canon, retained int
		var cost float64
		if err := erows.Scan(&p, &canon, &retained, &cost, &kinds); err != nil {
			_ = erows.Close()
			return nil, err
		}
		ps := get(p)
		ps.Experiments, ps.ExperimentsRetained, ps.CostUSD, ps.Kinds = canon, retained, cost, splitKinds(kinds)
	}
	_ = erows.Close()
	arows, err := s.db.QueryContext(ctx, `SELECT a.project,
  SUM(CASE WHEN `+canonAnsAtt+` THEN 1 ELSE 0 END),
  COUNT(*),
  COUNT(DISTINCT CASE WHEN `+canonAnsAtt+` THEN a.model END),
  COUNT(DISTINCT CASE WHEN `+canonAnsAtt+` THEN a.benchmark END)
FROM attempt a GROUP BY a.project`)
	if err != nil {
		return nil, fmt.Errorf("research project attempts: %w", err)
	}
	for arows.Next() {
		var p string
		var canon, retained, models, benches int
		if err := arows.Scan(&p, &canon, &retained, &models, &benches); err != nil {
			_ = arows.Close()
			return nil, err
		}
		ps := get(p)
		ps.Attempts, ps.AttemptsRetained, ps.Models, ps.Benchmarks = canon, retained, models, benches
	}
	_ = arows.Close()

	out := make([]ProjectSummary, 0, len(byProj))
	for _, ps := range byProj {
		out = append(out, *ps)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Project < out[j].Project })
	return out, nil
}

// KindTotal is the per-kind slice of a totals aggregate (canonical experiments + cost).
type KindTotal struct {
	Kind        string  `json:"kind"`
	Experiments int     `json:"experiments"`
	CostUSD     float64 `json:"cost_usd"`
}

// Totals is the observatory's headline aggregate: canonical counts as the headline plus
// *_retained so the retained-vs-canonical truth is always visible, and a per-kind
// breakdown. Deterministic from the store.
type Totals struct {
	Project             string      `json:"project,omitempty"`
	Projects            int         `json:"projects"`
	Experiments         int         `json:"experiments"` // canonical
	ExperimentsRetained int         `json:"experiments_retained"`
	Attempts            int         `json:"attempts"` // canonical
	AttemptsRetained    int         `json:"attempts_retained"`
	Models              int         `json:"models"`
	Benchmarks          int         `json:"benchmarks"`
	CostUSD             float64     `json:"cost_usd"`
	ByKind              []KindTotal `json:"by_kind"`
}

func (s *store) totals(ctx context.Context, project string) (Totals, error) {
	expWhere, attWhere := "", ""
	var eArgs, aArgs []any
	if project != "" {
		expWhere = " AND e.project=?"
		attWhere = " AND a.project=?"
		eArgs, aArgs = []any{project}, []any{project}
	}
	var t Totals
	t.Project = project
	if err := s.db.QueryRowContext(ctx, `SELECT
  COALESCE(SUM(CASE WHEN `+canonAnsExp+` THEN 1 ELSE 0 END),0),
  COUNT(*),
  COUNT(DISTINCT e.project),
  COALESCE(SUM(CASE WHEN `+canonAnsExp+` THEN e.cost_usd ELSE 0 END),0)
FROM experiment e WHERE 1=1`+expWhere, eArgs...).
		Scan(&t.Experiments, &t.ExperimentsRetained, &t.Projects, &t.CostUSD); err != nil {
		return Totals{}, fmt.Errorf("research totals experiments: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT
  COALESCE(SUM(CASE WHEN `+canonAnsAtt+` THEN 1 ELSE 0 END),0),
  COUNT(*),
  COUNT(DISTINCT CASE WHEN `+canonAnsAtt+` THEN a.model END),
  COUNT(DISTINCT CASE WHEN `+canonAnsAtt+` THEN a.benchmark END)
FROM attempt a WHERE 1=1`+attWhere, aArgs...).
		Scan(&t.Attempts, &t.AttemptsRetained, &t.Models, &t.Benchmarks); err != nil {
		return Totals{}, fmt.Errorf("research totals attempts: %w", err)
	}
	krows, err := s.db.QueryContext(ctx, `SELECT e.kind,
  SUM(CASE WHEN `+canonAnsExp+` THEN 1 ELSE 0 END),
  COALESCE(SUM(CASE WHEN `+canonAnsExp+` THEN e.cost_usd ELSE 0 END),0)
FROM experiment e WHERE 1=1`+expWhere+` GROUP BY e.kind ORDER BY e.kind`, eArgs...)
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

// ── small helpers ─────────────────────────────────────────────────────────────

func splitKinds(csv string) []string {
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	sort.Strings(parts)
	return parts
}

// jsonObj normalizes a JSON field to a compact object string; an absent or invalid value
// becomes "{}", so meta / lib_versions are always valid JSON a downstream query
// (SQLite json_extract, ClickHouse JSONExtract) can read.
func jsonObj(raw json.RawMessage) string {
	if len(raw) == 0 || !json.Valid(raw) {
		return "{}"
	}
	return string(raw)
}

// canonJSON renders a JSON value in a CANONICAL form (sorted keys, no whitespace) for
// content hashing, so a caller's re-serialization of the same object does not mint a
// spurious retained version. An unparseable value is hashed as its raw bytes (still
// deterministic); an absent value is empty.
func canonJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, err := json.Marshal(v) // encoding/json sorts map keys and strips whitespace
	if err != nil {
		return string(raw)
	}
	return string(b)
}

// revisionOf normalizes a producer-declared version kind; empty is `original`. superseded
// and canonical are DERIVED, never stored, so they are not accepted here.
func revisionOf(r string) string {
	switch strings.TrimSpace(strings.ToLower(r)) {
	case "corrected":
		return "corrected"
	case "retracted":
		return "retracted"
	default:
		return "original"
	}
}

func sourceOf(s string) string {
	if s == "" {
		return "hanzo-measured"
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
