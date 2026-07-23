package research

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/hanzoai/sqlite"
)

// These tests guard the property the orm migration bought: the "no such column"
// DDL-drift class — the outage, a CREATE INDEX referencing a git_sha column an older
// CREATE TABLE lacked — is STRUCTURALLY IMPOSSIBLE. Records are typed Go values
// stored as JSON in orm's fixed _entities table, so evolving a record is a
// struct-field change with ZERO schema change and no ALTER/CREATE on open.

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
