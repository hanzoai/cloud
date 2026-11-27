package research

// datastore.go is the WAREHOUSE projection of the research plane: the roll-up target
// (HIP-0512 §"Two planes, named") that mirrors each org's transactional SQLite into
// hanzoai/datastore — Hanzo's column-oriented OLAP — so the unified, cross-project
// leaderboard/observatory reads ONE aggregate surface. hanzoai/datastore is never
// reinvented: this reuses the SAME connection the account-usage warehouse uses
// (aiobject's package-global Datastore*), adds two research tables beside
// hanzo.account_usage, and follows the same contracts.
//
// FAIL-SOFT IS THE CONTRACT (mirrors usage/datastore.go): the SQLite plane is the local
// source of truth; this roll-up is best-effort. An absent or still-connecting datastore
// makes every function a no-op returning an error the caller SWALLOWS — losing a roll-up
// must never fail an ingest whose SQLite write already committed. (Retry/reconciliation
// of missed roll-ups — including grant updates — is the reconciliation increment still on
// the critical path; today the roll-up mirrors the ingest-time state.)
//
// VERSIONED, RETAINED: content_hash is part of the ORDER BY key, so every distinct
// version is a distinct warehouse row (retained); ReplacingMergeTree(ts) collapses ONLY a
// re-roll-up of the SAME version (same content_hash) — it is idempotent dedup of an
// observation, NOT cross-version supersession. Provenance
// (git_sha/git_branch/git_dirty/lib_versions) rides as queryable columns.
//
// CANONICAL OLAP READ IS DEFERRED — and when it lands it MUST be SEQ-authoritative, not
// argMax(ts) (N2). A client ts is not a valid supersession clock (a backfill stamps a big
// wall-clock ts, an SDK live record stamps none), exactly as in store.go's canonical
// predicates. So the roll-up will carry the SQLite plane's monotonic append seq and the
// canonical read will argMax by THAT, never by ts. Safe today: the OLAP plane serves no
// authorization-relevant or canonical read yet, and the retained rows are complete.
//
// TENANCY: org and project are the SERVER's values, bound positionally — a payload
// cannot forge them.

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hanzoai/cloud/datastore"
)

const (
	researchExperimentTable = "hanzo.research_experiment"
	researchAttemptTable    = "hanzo.research_attempt"
	researchArtifactTable   = "hanzo.research_artifact"
	researchRunTable        = "hanzo.research_run"
)

// warehouse holds ONLY the idempotent-DDL latch; the connection is aiobject's package
// global. dsReady latches on success only, so a datastore still connecting at boot is
// retried on the next call rather than permanently written off.
type warehouse struct {
	dsMu    sync.Mutex
	dsReady bool
}

// researchDDL is the ONE definition of the research warehouse schema. ReplacingMergeTree
// keyed WITH content_hash retains every version; ts is the collapse version so a
// re-roll-up of one version is idempotent. PARTITION BY the key's own low-cardinality
// component (kind / benchmark) keeps re-reports collapsible (ReplacingMergeTree collapses
// only within a partition, and the partition column is part of the key).
var researchDDL = []string{
	`CREATE TABLE IF NOT EXISTS ` + researchExperimentTable + ` (
  org          LowCardinality(String),
  project      LowCardinality(String),
  id           String,
  content_hash String,
  revision     LowCardinality(String),
  status       LowCardinality(String),
  visibility   LowCardinality(String),
  trainable    UInt8,
  publishable  UInt8,
  kind         LowCardinality(String),
  subject      String,
  task         String,
  metric       LowCardinality(String),
  value        Float64,
  n            UInt32,
  n_total      UInt32,
  cost_usd     Float64,
  meta         String,
  git_sha      String,
  git_branch   LowCardinality(String),
  git_dirty    UInt8,
  lib_versions String,
  ts           DateTime64(3, 'UTC')
) ENGINE = ReplacingMergeTree(ts)
PARTITION BY kind
ORDER BY (org, project, id, content_hash)`,

	`CREATE TABLE IF NOT EXISTS ` + researchAttemptTable + ` (
  org          LowCardinality(String),
  project      LowCardinality(String),
  benchmark    LowCardinality(String),
  item         String,
  model        String,
  content_hash String,
  revision     LowCardinality(String),
  status       LowCardinality(String),
  gold         String,
  answer       String,
  correct      UInt8,
  response     String,
  source       LowCardinality(String),
  ts           DateTime64(3, 'UTC')
) ENGINE = ReplacingMergeTree(ts)
PARTITION BY benchmark
ORDER BY (org, project, benchmark, item, model, content_hash)`,

	`CREATE TABLE IF NOT EXISTS ` + researchArtifactTable + ` (
  org             LowCardinality(String),
  project         LowCardinality(String),
  sha256          String,
  kind            LowCardinality(String),
  ref             String,
  run_id          String,
  visibility      LowCardinality(String),
  retention_class LowCardinality(String),
  git_sha         String,
  git_branch      LowCardinality(String),
  git_dirty       UInt8,
  lib_versions    String,
  ts              DateTime64(3, 'UTC')
) ENGINE = ReplacingMergeTree(ts)
PARTITION BY kind
ORDER BY (org, sha256)`,

	// The EXECUTION, with completion as columns. questions/answered are Int32 and
	// NEGATIVE means the record does not say — the one encoding that keeps "not
	// stated" out of the same bucket as zero in a numeric column, which a board
	// summing answered/questions would otherwise read as a finished run of nothing.
	// `completion` is the derived value carried as a column so the cross-project
	// question — which published runs never finished — is one WHERE clause.
	`CREATE TABLE IF NOT EXISTS ` + researchRunTable + ` (
  org            LowCardinality(String),
  project        LowCardinality(String),
  id             String,
  content_hash   String,
  revision       LowCardinality(String),
  benchmark      LowCardinality(String),
  split          LowCardinality(String),
  ` + "`system`" + `  String,
  version        String,
  baseline       UInt8,
  study          String,
  embedder       String,
  reader         String,
  k              Int32,
  temperature    Float64,
  max_tokens     Int32,
  prompt         String,
  prompt_digest  String,
  dataset_digest String,
  store_digest   String,
  facts_digest   String,
  ` + "`commit`" + `  String,
  questions      Int32,
  answered       Int32,
  completion     LowCardinality(String),
  started        String,
  ended          String,
  ` + "`by`" + `      String,
  visibility     LowCardinality(String),
  ts             DateTime64(3, 'UTC')
) ENGINE = ReplacingMergeTree(ts)
PARTITION BY benchmark
ORDER BY (org, project, id, content_hash)`,
}

func (w *warehouse) ensure(ctx context.Context) error {
	if !datastore.Ready() {
		return fmt.Errorf("research warehouse: datastore not connected")
	}
	w.dsMu.Lock()
	defer w.dsMu.Unlock()
	if w.dsReady {
		return nil
	}
	for _, stmt := range researchDDL {
		if err := datastore.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("research ddl: %w", err)
		}
	}
	w.dsReady = true
	return nil
}

const researchExperimentInsert = `INSERT INTO ` + researchExperimentTable + ` (
  org, project, id, content_hash, revision, status, visibility, trainable, publishable,
  kind, subject, task, metric, value, n, n_total, cost_usd, meta,
  git_sha, git_branch, git_dirty, lib_versions, ts
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

const researchAttemptInsert = `INSERT INTO ` + researchAttemptTable + ` (
  org, project, benchmark, item, model, content_hash, revision, status,
  gold, answer, correct, response, source, ts
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

// rollUp mirrors one ingested batch into the warehouse, stamping org/project (server
// values) and the same private/withheld visibility the SQLite ingest records. It is
// FAIL-SOFT: the caller treats a non-nil error as "not rolled up" (the SQLite write is
// durable), so it stops at the FIRST failing row rather than hammering a dead datastore
// for every row of a large backfill.
func (w *warehouse) rollUp(ctx context.Context, org, project string, exps []Experiment, atts []Attempt, now time.Time) error {
	if org == "" {
		return fmt.Errorf("research warehouse: blank org")
	}
	if err := w.ensure(ctx); err != nil {
		return err
	}
	obs := now.UTC()
	for i := range exps {
		e := exps[i]
		ets := obs
		if e.TS > 0 {
			ets = time.Unix(e.TS, 0).UTC()
		}
		if err := datastore.Exec(ctx, researchExperimentInsert,
			org, project, e.ID, hashExp(e), revisionOf(e.Revision), runStatus(e.Status), "private", uint8(0), uint8(0),
			e.Kind, e.Subject, e.Task, e.Metric, e.Value, uint32(nonNeg(e.N)), uint32(nonNeg(e.NTotal)),
			e.CostUSD, jsonObj(e.Meta), e.GitSHA, e.GitBranch, boolBit(e.GitDirty), jsonObj(e.LibVersions), ets); err != nil {
			return fmt.Errorf("research experiment roll-up: %w", err)
		}
	}
	for i := range atts {
		a := atts[i]
		ats := obs
		if a.TS > 0 {
			ats = time.Unix(a.TS, 0).UTC()
		}
		if err := datastore.Exec(ctx, researchAttemptInsert,
			org, project, a.Benchmark, a.Item, a.Model, hashAtt(a), revisionOf(a.Revision), runStatus(a.Status),
			a.Gold, a.Answer, boolBit(a.Correct), a.Response, sourceOf(a.Source), ats); err != nil {
			return fmt.Errorf("research attempt roll-up: %w", err)
		}
	}
	return nil
}

const researchArtifactInsert = `INSERT INTO ` + researchArtifactTable + ` (
  org, project, sha256, kind, ref, run_id, visibility, retention_class,
  git_sha, git_branch, git_dirty, lib_versions, ts
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`

// rollUpArtifact mirrors one diary artifact into the warehouse, keyed by its sha256
// content hash (idempotent). Fail-soft like the run roll-up: the SQLite manifest is the
// source of truth.
func (w *warehouse) rollUpArtifact(ctx context.Context, org, project string, a ResearchArtifact, now time.Time) error {
	if org == "" {
		return fmt.Errorf("research warehouse: blank org")
	}
	if err := w.ensure(ctx); err != nil {
		return err
	}
	ts := now.UTC()
	if a.TS > 0 {
		ts = time.Unix(a.TS, 0).UTC()
	}
	return datastore.Exec(ctx, researchArtifactInsert,
		org, project, a.SHA256, a.Kind, a.Ref, a.RunID, "private", "raw-artifact",
		a.GitSHA, a.GitBranch, boolBit(a.GitDirty), jsonObj(a.LibVersions), ts)
}

func boolBit(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

func nonNeg(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

const researchRunInsert = `INSERT INTO ` + researchRunTable + ` (
  org, project, id, content_hash, revision, benchmark, split, ` + "`system`" + `, version, baseline,
  study, embedder, reader, k, temperature, max_tokens, prompt, prompt_digest,
  dataset_digest, store_digest, facts_digest, ` + "`commit`" + `, questions, answered, completion,
  started, ended, ` + "`by`" + `, visibility, ts
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

// rollUpRuns mirrors recorded executions into the warehouse. Fail-soft on the same
// terms as every other roll-up here: the per-org SQLite is the source of truth and a
// datastore outage degrades the cross-project view rather than failing a recorded run.
//
// The per-category measures are deliberately NOT projected. They are a list per run
// and flattening them into a second fact table would put the same numbers in two
// places with no reconciliation between them; a board that needs a run's numbers
// reads the run. What this table carries is the identity, the configuration and the
// completion — the columns a fleet-wide question is actually asked in.
func (w *warehouse) rollUpRuns(ctx context.Context, org, project string, runs []Run, now time.Time) error {
	if org == "" {
		return fmt.Errorf("research warehouse: blank org")
	}
	if len(runs) == 0 {
		return nil
	}
	if err := w.ensure(ctx); err != nil {
		return err
	}
	obs := now.UTC()
	for i := range runs {
		r := runs[i]
		if err := datastore.Exec(ctx, researchRunInsert,
			org, project, r.ID, hashRun(r), revisionOf(r.Revision), r.Benchmark, r.Split,
			r.System, r.Version, boolBit(r.Baseline), r.Study, r.Embedder, r.Reader,
			int32(orUnknown(r.K)), orZero(r.Temperature), int32(orUnknown(r.MaxTokens)),
			r.Prompt, r.PromptDigest, r.DatasetDigest, r.StoreDigest, r.FactsDigest, r.Commit,
			int32(orUnknown(r.Questions)), int32(orUnknown(r.Answered)),
			completionOf(r.Questions, r.Answered, r.Ended),
			r.When, r.Ended, r.By, "private", obs); err != nil {
			return fmt.Errorf("research run roll-up: %w", err)
		}
	}
	return nil
}

// orUnknown renders an optional count for a numeric column: -1 for "the record does
// not say", which is outside every real count and so can never be mistaken for one.
// The completion column is derived from the POINTERS before this runs, so a query
// that wants completion never has to decode the sentinel at all.
func orUnknown(v *int) int {
	if v == nil {
		return -1
	}
	return *v
}

// orZero renders an optional temperature. A float column has no free sentinel — 0 and
// -1 are both real temperatures — so the honest projection is 0 with the caveat that
// this column cannot express absence; the transactional record can, and does.
func orZero(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}
