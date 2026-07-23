package research

// datastore.go is the WAREHOUSE projection of the research plane: the roll-up target
// (HIP-0512 §"Two planes, named") that mirrors each org's transactional SQLite into
// hanzoai/datastore — Hanzo's column-oriented OLAP — so the unified, cross-project
// leaderboard/observatory reads ONE aggregate surface instead of scanning per-org
// files. hanzoai/datastore is never reinvented: this reuses the SAME connection the
// account-usage warehouse uses (aiobject's package-global Datastore*), adds two
// research tables beside hanzo.account_usage, and follows the same contracts.
//
// FAIL-SOFT IS THE CONTRACT (mirrors usage/datastore.go): SQLite is the source of
// truth; this roll-up is best-effort. An absent or still-connecting datastore makes
// every function here a no-op that returns an honest error the caller SWALLOWS —
// losing a roll-up must never fail an ingest whose SQLite write already committed.
//
// TENANCY: org and project are the SERVER's values (from the validated principal),
// bound positionally — a payload cannot carry them, so it cannot forge them. Every
// row leads with (org, project).

import (
	"context"
	"encoding/json"
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
// global (the same one account-usage uses), so this owns no closable handle. dsReady
// latches on success only, so a datastore still connecting at boot is retried on the
// next call rather than permanently written off.
type warehouse struct {
	dsMu    sync.Mutex
	dsReady bool
}

// researchDDL is the ONE definition of the research warehouse schema.
//
// ENGINE — ReplacingMergeTree(ts), NOT MergeTree, for BOTH tables, because the roll-up
// re-sends rows the SQLite plane already holds (a backfill, then per-run appends that
// overlap), and those re-sends are the same fact observed twice: the table must
// COLLAPSE them, not stack them. `ts` is the version — the newest observation of a key
// wins — so the collapse rule matches the SQLite plane's own idempotency exactly.
//
// experiment — ORDER BY (org, project, id): the stable id (<kind>:<subject>:<task>) is
// the dedup key, so a re-ingest of the same experiment replaces and ReplacingMergeTree
// keeps the latest ts = the latest-run-canonical number. PARTITION BY kind keeps every
// version of one experiment in one partition (ReplacingMergeTree only collapses WITHIN
// a partition, and kind is part of the id, so a re-report always lands where it can
// collapse).
//
// attempt — ORDER BY (org, project, benchmark, item, model): the (benchmark, item,
// model) key is exactly the SQLite PRIMARY KEY, so a re-ingest of an attempt collapses
// to one row and the immutable raw response is preserved. PARTITION BY benchmark, the
// key's own low-cardinality component, keeps re-reports collapsible.
var researchDDL = []string{
	`CREATE TABLE IF NOT EXISTS ` + researchExperimentTable + ` (
  org      LowCardinality(String),
  project  LowCardinality(String),
  id       String,
  kind     LowCardinality(String),
  subject  String,
  task     String,
  metric   LowCardinality(String),
  value    Float64,
  n        UInt32,
  n_total  UInt32,
  cost_usd Float64,
  status   LowCardinality(String),
  meta     String,
  ts       DateTime64(3, 'UTC')
) ENGINE = ReplacingMergeTree(ts)
PARTITION BY kind
ORDER BY (org, project, id)`,

	`CREATE TABLE IF NOT EXISTS ` + researchAttemptTable + ` (
  org       LowCardinality(String),
  project   LowCardinality(String),
  benchmark LowCardinality(String),
  item      String,
  model     String,
  gold      String,
  answer    String,
  correct   UInt8,
  response  String,
  source    LowCardinality(String),
  ts        DateTime64(3, 'UTC')
) ENGINE = ReplacingMergeTree(ts)
PARTITION BY benchmark
ORDER BY (org, project, benchmark, item, model)`,
}

// ensure creates the research warehouse objects idempotently, latching only on
// success so a datastore that is still connecting at boot is retried on the next call.
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
  org, project, id, kind, subject, task, metric, value, n, n_total, cost_usd, status, meta, ts
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

const researchAttemptInsert = `INSERT INTO ` + researchAttemptTable + ` (
  org, project, benchmark, item, model, gold, answer, correct, response, source, ts
) VALUES (?,?,?,?,?,?,?,?,?,?,?)`

// rollUp mirrors one ingested batch into the warehouse, stamping org/project (server
// values) and a single observation clock across the batch. It is FAIL-SOFT: the caller
// treats a non-nil error as "not rolled up" (the SQLite write is already durable), so
// it stops at the FIRST failing row rather than hammering a dead datastore for every
// row of a 9k backfill. Returns nil only when the whole batch landed.
func (w *warehouse) rollUp(ctx context.Context, org, project string, exps []Experiment, atts []Attempt, now time.Time) error {
	if org == "" {
		return fmt.Errorf("research warehouse: blank org")
	}
	if err := w.ensure(ctx); err != nil {
		return err
	}
	ts := now.UTC()
	for i := range exps {
		e := exps[i]
		meta := metaBytes(e.Meta)
		// The experiment's own run ts (unix seconds) is the version if present, so the
		// warehouse's latest-run-canonical collapse matches the SQLite plane; absent, the
		// observation clock stands in.
		ets := ts
		if e.TS > 0 {
			ets = time.Unix(e.TS, 0).UTC()
		}
		if err := aiobject.DatastoreExec(ctx, researchExperimentInsert,
			org, project, e.ID, e.Kind, e.Subject, e.Task, e.Metric, e.Value,
			uint32(nonNeg(e.N)), uint32(nonNeg(e.NTotal)), e.CostUSD, runStatus(e.Status), meta, ets); err != nil {
			return fmt.Errorf("research experiment roll-up: %w", err)
		}
	}
	for i := range atts {
		a := atts[i]
		src := a.Source
		if src == "" {
			src = "hanzo-measured"
		}
		if err := aiobject.DatastoreExec(ctx, researchAttemptInsert,
			org, project, a.Benchmark, a.Item, a.Model, a.Gold, a.Answer,
			boolBit(a.Correct), a.Response, src, ts); err != nil {
			return fmt.Errorf("research attempt roll-up: %w", err)
		}
	}
	return nil
}

// metaBytes normalizes an experiment's meta to a compact JSON object string for the
// warehouse (an absent/invalid meta becomes "{}"), so the OLAP column is always valid
// JSON a downstream query can JSONExtract.
func metaBytes(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	if !json.Valid(raw) {
		return "{}"
	}
	return string(raw)
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
