package research

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/hanzoai/sqlite"
)

// These tests cover BOTH migration axes of the orm store:
//   - SCHEMA EVOLUTION: the "no such column" DDL-drift class — the outage, a CREATE
//     INDEX referencing a git_sha column an older CREATE TABLE lacked — is
//     STRUCTURALLY IMPOSSIBLE, because records are typed Go values stored as JSON in
//     orm's fixed _entities table (evolving a record is a struct-field change, no DDL).
//   - LEGACY DATA MIGRATION: an existing store's pre-orm raw-SQL evidence
//     (experiment/attempt/artifact tables) is carried forward into _entities on open,
//     preserving canonical/supersession, provenance, and artifact blobs — so a store
//     that predates the orm cutover is never silently read as empty.

// expRecordEvolved is a FUTURE revision of expRecord with an added provenance field.
// It stands in for the next code version that needs one more field — the exact kind
// of change that broke the raw-SQL store.
type expRecordEvolved struct {
	Project     string `json:"project"`
	ID          string `json:"id"`
	ContentHash string `json:"content_hash"`
	Seq         int64  `json:"seq"`
	Revision    string `json:"revision"`
	Status      string `json:"status"`
	Kind        string `json:"kind"`
	GitSHA      string `json:"git_sha"`
	GitTag      string `json:"git_tag"` // NEW provenance field — needs NO migration
}

// TestNoMigrationNeededForNewProvenanceField proves the regression the raw-SQL store
// could not have: adding a provenance field to a record needs NO DDL. A record
// carrying a brand-new field is written and read back through the SAME _entities
// table the current records use, and an old-shape record coexists — no ALTER TABLE,
// no CREATE INDEX, no "no such column".
func TestNoMigrationNeededForNewProvenanceField(t *testing.T) {
	st, ctx := newStore(t)

	// A current-shape experiment, ingested normally.
	if _, _, err := st.ingest(ctx, proj, []Experiment{{
		ID: "kernel-perf:engine:prefill", Kind: "kernel-perf", Subject: "engine", Task: "prefill",
		Metric: "tok/s", Value: 94.4, GitSHA: "oldsha",
	}}, nil); err != nil {
		t.Fatalf("ingest current-shape experiment: %v", err)
	}

	// A FUTURE-shape record with an added git_tag field writes through the same orm
	// store with ZERO schema change — the write that, under the raw-SQL store, would
	// have needed a migration (and broken on a drifted table).
	ev := expRecordEvolved{
		Project: proj, ID: "kernel-perf:engine:decode", ContentHash: "h-evolved", Seq: 999,
		Revision: "original", Status: "complete", Kind: "kernel-perf",
		GitSHA: "newsha", GitTag: "v1.2.3",
	}
	key := st.db.NewKey(kindExp, "e:evolved-regression", 0, nil)
	if _, err := st.db.Put(ctx, key, &ev); err != nil {
		t.Fatalf("writing a record with a NEW field must need no migration, but failed: %v", err)
	}

	// The new field round-trips.
	var back expRecordEvolved
	if err := st.db.Get(ctx, key, &back); err != nil {
		t.Fatalf("read evolved record: %v", err)
	}
	if back.GitTag != "v1.2.3" || back.GitSHA != "newsha" {
		t.Fatalf("evolved record round-trip = %+v, want git_tag v1.2.3 git_sha newsha", back)
	}

	// The old-shape record still reads fine under the CURRENT struct (which has no
	// git_tag field — it is simply ignored), and the evolved record coexists in the
	// same table. No column drift, no error: the outage cannot recur.
	exps, err := st.allExp(ctx)
	if err != nil {
		t.Fatalf("load all experiments across the mixed-shape table: %v", err)
	}
	var sawOld, sawEvolved bool
	for _, r := range exps {
		switch r.GitSHA {
		case "oldsha":
			sawOld = true
		case "newsha":
			sawEvolved = true
		}
	}
	if !sawOld || !sawEvolved {
		t.Fatalf("mixed-shape coexistence: sawOld=%v sawEvolved=%v (both must read with no migration)", sawOld, sawEvolved)
	}
}

// openRawStore opens the orm-backed store directly over a plain SQLite *sql.DB (the
// same seam cloud.OrgStore uses, minus cek encryption) so a reopen test exercises
// the RECORD layer's schema idempotency without the file/encryption layer, which is
// owned and covered elsewhere.
func openRawStore(t *testing.T, path string) *store {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite %q: %v", path, err)
	}
	db.SetMaxOpenConns(1) // the single-writer contract OrgDB applies
	st, err := openStore(db)
	if err != nil {
		t.Fatalf("openStore %q: %v", path, err)
	}
	return st
}

// TestStoreReopenIsIdempotent proves opening the store is field-agnostic and
// idempotent: a second open of the same file (a restart, or a second replica) does
// not error, and the records the first open persisted are intact — the orm analogue
// of "migrate runs cleanly twice", now with nothing to migrate.
func TestStoreReopenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.db")
	ctx := context.Background()

	st := openRawStore(t, path)
	if _, _, err := st.ingest(ctx, proj, []Experiment{{ID: "benchmark:m:hle", Kind: "benchmark", Subject: "m", Task: "hle", Value: 70}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Re-open the SAME file — orm's initSchema must run cleanly again, and the data
	// persists.
	st2 := openRawStore(t, path)
	t.Cleanup(func() { _ = st2.Close() })
	c, err := st2.counts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.ExperimentsRetained != 1 {
		t.Fatalf("re-open lost data: retained=%d, want 1", c.ExperimentsRetained)
	}
}

// seedLegacy writes the pre-orm raw-SQL schema (the original store's experiment /
// attempt / artifact tables) into path and inserts real rows, including a CORRECTION
// PAIR (same stable id, original seq 1 then corrected seq 2) — the shape that must
// keep the corrected version canonical across the migration.
func seedLegacy(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()

	if _, err := db.Exec(`
CREATE TABLE experiment (
  seq INTEGER PRIMARY KEY AUTOINCREMENT, project TEXT NOT NULL DEFAULT 'default',
  id TEXT NOT NULL, content_hash TEXT NOT NULL, revision TEXT NOT NULL DEFAULT 'original',
  status TEXT NOT NULL DEFAULT 'complete', visibility TEXT NOT NULL DEFAULT 'private',
  trainable INTEGER NOT NULL DEFAULT 0, publishable INTEGER NOT NULL DEFAULT 0,
  kind TEXT NOT NULL, subject TEXT NOT NULL, task TEXT, metric TEXT, value REAL, n INTEGER,
  n_total INTEGER NOT NULL DEFAULT 0, cost_usd REAL, meta TEXT,
  git_sha TEXT NOT NULL DEFAULT '', git_branch TEXT NOT NULL DEFAULT '',
  git_dirty INTEGER NOT NULL DEFAULT 0, lib_versions TEXT NOT NULL DEFAULT '{}',
  ts INTEGER NOT NULL DEFAULT 0, UNIQUE(project, id, content_hash));
CREATE TABLE attempt (
  seq INTEGER PRIMARY KEY AUTOINCREMENT, project TEXT NOT NULL DEFAULT 'default',
  benchmark TEXT NOT NULL, item TEXT NOT NULL, model TEXT NOT NULL, content_hash TEXT NOT NULL,
  revision TEXT NOT NULL DEFAULT 'original', status TEXT NOT NULL DEFAULT 'complete',
  gold TEXT, answer TEXT, correct INTEGER NOT NULL, response TEXT,
  source TEXT NOT NULL DEFAULT 'hanzo-measured', ts INTEGER NOT NULL DEFAULT 0,
  UNIQUE(project, benchmark, item, model, content_hash));
CREATE TABLE artifact (
  sha256 TEXT PRIMARY KEY, kind TEXT NOT NULL, ref TEXT NOT NULL, content BLOB,
  run_id TEXT NOT NULL DEFAULT '', project TEXT NOT NULL DEFAULT 'default',
  visibility TEXT NOT NULL DEFAULT 'private', retention_class TEXT NOT NULL DEFAULT 'raw-artifact',
  git_sha TEXT NOT NULL DEFAULT '', git_branch TEXT NOT NULL DEFAULT '',
  git_dirty INTEGER NOT NULL DEFAULT 0, lib_versions TEXT NOT NULL DEFAULT '{}',
  ts INTEGER NOT NULL DEFAULT 0);`); err != nil {
		t.Fatalf("seed schema: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO experiment
	  (project,id,content_hash,revision,status,kind,subject,task,metric,value,n,git_sha,git_branch,lib_versions,ts) VALUES
	  ('enso-bench','benchmark:enso:livecodebench','h1','original','complete','benchmark','enso','livecodebench','accuracy',91.4,200,'oldsha','main','{"harness":"0.1.0"}',100),
	  ('enso-bench','benchmark:enso:livecodebench','h2','corrected','complete','benchmark','enso','livecodebench','accuracy',69.7,200,'newsha','main','{"harness":"0.2.0"}',200),
	  ('enso-bench','benchmark:grok:gpqa','h3','original','complete','benchmark','grok','gpqa','accuracy',88.0,198,'x','main','{}',50)`); err != nil {
		t.Fatalf("seed experiments: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO attempt (project,benchmark,item,model,content_hash,answer,correct,ts) VALUES
	  ('enso-bench','livecodebench','q1','enso','a1','A',1,10)`); err != nil {
		t.Fatalf("seed attempts: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO artifact (sha256,kind,ref,content,run_id,project,ts) VALUES
	  ('abc123','snapshot','sha256:abc123',?,'benchmark:enso:livecodebench','enso-bench',300)`, []byte("legacy-board-png")); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
}

// TestLegacyDataMigration is the regression the CRITICAL demands: an existing store's
// pre-orm raw-SQL evidence must be carried forward on open — NOT silently read as
// empty — with canonical/supersession, provenance, and artifact bytes intact, and the
// append clock continued so later ingests still supersede the migrated versions.
func TestLegacyDataMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.db")
	ctx := context.Background()
	seedLegacy(t, path)

	// Open through the orm store — migrateLegacy carries every legacy row into _entities.
	st := openRawStore(t, path)

	// Retained + canonical == the pre-migration truth (3 exp versions, 2 canonical ids;
	// 1 attempt). Without the migration these were all 0 (the CRITICAL).
	c, err := st.counts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.ExperimentsRetained != 3 || c.ExperimentsCanonical != 2 {
		t.Fatalf("post-migration exp counts=%+v, want retained=3 canonical=2 (evidence carried forward)", c)
	}
	if c.AttemptsRetained != 1 || c.AttemptsCanonical != 1 {
		t.Fatalf("post-migration attempt counts=%+v, want retained=1 canonical=1", c)
	}

	// The corrected version (higher legacy seq) stays canonical, with its provenance.
	find := func() Experiment {
		exps, err := st.listExperiments(ctx, "", "")
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range exps {
			if e.ID == "benchmark:enso:livecodebench" {
				return e
			}
		}
		t.Fatal("migrated livecodebench experiment missing")
		return Experiment{}
	}
	lcb := find()
	if lcb.Value != 69.7 || lcb.Revision != "corrected" || !lcb.Canonical {
		t.Fatalf("migrated canonical wrong: %+v (want value 69.7, corrected, canonical)", lcb)
	}
	if lcb.GitSHA != "newsha" || string(lcb.LibVersions) != `{"harness":"0.2.0"}` {
		t.Fatalf("migrated provenance lost: sha=%q libs=%s", lcb.GitSHA, lcb.LibVersions)
	}

	// The artifact + its bytes survived, hash-addressed.
	content, kind, ok := st.artifactContent(ctx, "enso-bench", "abc123")
	if !ok || kind != "snapshot" || string(content) != "legacy-board-png" {
		t.Fatalf("migrated artifact wrong: ok=%v kind=%q content=%q", ok, kind, content)
	}

	// A later correction to the MIGRATED lineage must supersede it even with ts=0 —
	// proving the append clock continued PAST the migrated seqs (seq, not ts, wins
	// across the migration boundary).
	if _, _, err := st.ingest(ctx, "enso-bench", []Experiment{{
		ID: "benchmark:enso:livecodebench", Kind: "benchmark", Subject: "enso", Task: "livecodebench",
		Metric: "accuracy", Value: 72.1, N: 200, Revision: "corrected", TS: 0,
	}}, nil); err != nil {
		t.Fatal(err)
	}
	if v := find().Value; v != 72.1 {
		t.Fatalf("post-migration ingest did not supersede migrated canonical: got %v, want 72.1", v)
	}

	// An UNRELATED ingest must NOT flip the migrated grok canonical.
	if _, _, err := st.ingest(ctx, "enso-bench", []Experiment{{
		ID: "benchmark:other:mmlu", Kind: "benchmark", Subject: "other", Task: "mmlu", Metric: "accuracy", Value: 50,
	}}, nil); err != nil {
		t.Fatal(err)
	}
	exps, _ := st.listExperiments(ctx, "", "")
	for _, e := range exps {
		if e.ID == "benchmark:grok:gpqa" && (e.Value != 88.0 || !e.Canonical) {
			t.Fatalf("unrelated ingest disturbed migrated grok canonical: %+v", e)
		}
	}

	// Idempotent: re-open the same file — migration re-runs to a no-op (CreateIfAbsent),
	// no duplication. retained = 3 migrated + 2 ingested = 5; canonical = livecodebench
	// (72.1) + grok (88) + mmlu (50) = 3.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st2 := openRawStore(t, path)
	t.Cleanup(func() { _ = st2.Close() })
	c2, err := st2.counts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c2.ExperimentsRetained != 5 || c2.ExperimentsCanonical != 3 {
		t.Fatalf("post-reopen counts=%+v, want retained=5 canonical=3 (idempotent, no dup)", c2)
	}
}
