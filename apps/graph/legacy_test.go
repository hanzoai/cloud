package graph

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/hanzoai/sqlite" // registers "sqlite" under both build tags
)

// oldShape is a graph file as it stood before statements could end and before the
// index followed an update: no `until`, INTEGER columns, the index and its two
// triggers. A file in production is this, and SQLite is where it is, so the
// upgrade is SQLite's alone and the test builds the file by hand.
const oldShape = `
CREATE TABLE assertion (
	seq        INTEGER PRIMARY KEY AUTOINCREMENT,
	id         TEXT NOT NULL UNIQUE,
	entity     TEXT NOT NULL,
	relation   TEXT NOT NULL,
	value      TEXT NOT NULL,
	names      INTEGER NOT NULL DEFAULT 0,
	at         INTEGER NOT NULL,
	seen       INTEGER NOT NULL,
	knowable   INTEGER NOT NULL,
	source     TEXT NOT NULL,
	evidence   TEXT NOT NULL DEFAULT '',
	"by"       TEXT NOT NULL DEFAULT '',
	confidence REAL NOT NULL DEFAULT 0,
	hold       INTEGER NOT NULL DEFAULT 0,
	wrote      INTEGER NOT NULL
);
CREATE INDEX assertion_out ON assertion(entity, relation);
CREATE INDEX assertion_in ON assertion(value, relation);
CREATE INDEX assertion_rel ON assertion(relation);
CREATE INDEX assertion_wrote ON assertion(wrote);
CREATE INDEX assertion_knowable ON assertion(knowable);
CREATE VIRTUAL TABLE assertion_fts USING fts5(
	entity, relation, value, source, evidence,
	content='assertion', content_rowid='seq', tokenize='unicode61'
);
CREATE TRIGGER assertion_ai AFTER INSERT ON assertion BEGIN
	INSERT INTO assertion_fts(rowid, entity, relation, value, source, evidence)
	VALUES (new.seq, new.entity, new.relation, new.value, new.source, new.evidence);
END;
CREATE TRIGGER assertion_ad AFTER DELETE ON assertion BEGIN
	INSERT INTO assertion_fts(assertion_fts, rowid, entity, relation, value, source, evidence)
	VALUES ('delete', old.seq, old.entity, old.relation, old.value, old.source, old.evidence);
END;
`

// TestALegacyFileIsUpgradedInPlace: a file in the old shape opens, keeps every row,
// gains `until` (open, which NULL says), keeps its index and gains the update
// trigger, and a second open changes nothing.
func TestALegacyFileIsUpgradedInPlace(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(oldShape); err != nil {
		t.Fatalf("old shape: %v", err)
	}
	for _, f := range []Fact{
		mk(t, "acme/svc/api", "owner", "acme/team/core", true, "registry", t0),
		mk(t, "acme/svc/worker", "runtime", "rust", false, "scanner", t0),
	} {
		if _, err := db.Exec(`INSERT INTO assertion
			(id, entity, relation, value, names, at, seen, knowable, source, evidence, "by", confidence, wrote)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			f.ID, f.Entity, f.Relation, f.Value, flag(f.Names), f.At.Unix(), f.Seen.Unix(), f.Knowable.Unix(),
			f.Source, f.Evidence, f.By, f.Confidence, f.Wrote.Unix()); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	for range 2 {
		s, err := openStore(db)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		var open, rows int
		if err := db.QueryRow(`SELECT count(*), count(*) FILTER (WHERE until IS NULL) FROM assertion`).Scan(&rows, &open); err != nil || rows != 2 || open != 2 {
			t.Fatalf("after the upgrade: %d rows, %d open, %v; want 2 and 2", rows, open, err)
		}
		if got := searched(t, s, "registry", filter{}); len(got) != 1 || got[0] != "acme/team/core" {
			t.Fatalf("the index lost the rows it had: %v", got)
		}
		var trigger int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'trigger' AND name = 'assertion_au'`).Scan(&trigger); err != nil || trigger != 1 {
			t.Fatalf("the update trigger: %d, %v", trigger, err)
		}
	}

	s, err := openStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.record(context.Background(), []Fact{mk(t, "acme/svc/api", "runsOn", "kubernetes", false, "deploy", t0)}); err != nil {
		t.Fatal(err)
	}
	if got := searched(t, s, "kubernetes", filter{}); len(got) != 1 {
		t.Fatalf("a row written after the upgrade is not found: %v", got)
	}
}
