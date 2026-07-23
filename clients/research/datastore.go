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
// version is a distinct warehouse row (retained); ReplacingMergeTree(ts) collapses only
// a re-roll-up of the SAME version (idempotent). Canonical is argMax(ts) per stable id at
// read time. Provenance (git_sha/git_branch/git_dirty/lib_versions) rides as queryable
// columns for the longitudinal board.
//
// TENANCY: org and project are the SERVER's values, bound positionally — a payload
// cannot forge them.

import (
	"context"
	"fmt"
	"sync"
	"time"

	aiobject "github.com/hanzoai/ai/object"
)

const (
	researchExperimentTable = "hanzo.research_experiment"
	researchAttemptTable    = "hanzo.research_attempt"
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
}

func (w *warehouse) ensure(ctx context.Context) error {
	if !aiobject.DatastoreEnabled() {
		return fmt.Errorf("research warehouse: datastore not connected")
	}
	w.dsMu.Lock()
	defer w.dsMu.Unlock()
	if w.dsReady {
		return nil
	}
	for _, stmt := range researchDDL {
		if err := aiobject.DatastoreExec(ctx, stmt); err != nil {
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
		if err := aiobject.DatastoreExec(ctx, researchExperimentInsert,
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
		if err := aiobject.DatastoreExec(ctx, researchAttemptInsert,
			org, project, a.Benchmark, a.Item, a.Model, hashAtt(a), revisionOf(a.Revision), runStatus(a.Status),
			a.Gold, a.Answer, boolBit(a.Correct), a.Response, sourceOf(a.Source), ats); err != nil {
			return fmt.Errorf("research attempt roll-up: %w", err)
		}
	}
	return nil
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
