package research

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/hanzoai/sqlite"
)

// TestMigrateConvergesOldSchema reproduces the production outage: an experiment table
// created by an EARLIER schema (before the provenance columns) must converge to the
// current column set on open, not fail "no such column: git_sha". CREATE TABLE IF NOT
// EXISTS never alters an existing table, so migrate() must ALTER ADD COLUMN the columns a
// pre-existing table lacks before any index/insert references them.
func TestMigrateConvergesOldSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.db")

	// Stand up the OLD schema: experiment without the provenance columns (git_sha etc.),
	// exactly the shape that broke on the newer binary.
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = old.Exec(`CREATE TABLE experiment (
	  seq INTEGER PRIMARY KEY AUTOINCREMENT, project TEXT NOT NULL DEFAULT 'default',
	  id TEXT NOT NULL, content_hash TEXT NOT NULL, kind TEXT NOT NULL, subject TEXT NOT NULL,
	  UNIQUE(project, id, content_hash));
	INSERT INTO experiment (project,id,content_hash,kind,subject)
	  VALUES ('default','kernel-perf:x:y','h1','kernel-perf','x');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	// Open through the current openStore — this used to fail with "no such column: git_sha".
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	s, err := openStore(db)
	if err != nil {
		t.Fatalf("openStore on old-schema db failed (the outage): %v", err)
	}
	defer s.Close()

	// The pre-existing row survives, and the added columns read as their defaults.
	var sha string
	if err := s.db.QueryRow(`SELECT git_sha FROM experiment WHERE id='kernel-perf:x:y'`).Scan(&sha); err != nil {
		t.Fatalf("git_sha column not queryable after migrate: %v", err)
	}
	if sha != "" {
		t.Fatalf("git_sha default should be empty, got %q", sha)
	}

	// A new insert using the full column set works (the index on git_sha exists).
	if _, err := s.db.Exec(`INSERT INTO experiment
	  (project,id,content_hash,kind,subject,git_sha,git_branch,git_dirty,lib_versions,ts)
	  VALUES ('default','kernel-perf:a:b','h2','kernel-perf','a','abc123','main',0,'{}',1)`); err != nil {
		t.Fatalf("insert with provenance columns failed: %v", err)
	}
}

// TestMigrateIdempotent confirms migrate() runs cleanly twice — a re-open (or a second
// pod) must not error on already-present columns or indexes.
func TestMigrateIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idem.db")
	for i := 0; i < 2; i++ {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		s, err := openStore(db)
		if err != nil {
			t.Fatalf("migrate pass %d failed: %v", i, err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// guard: the ensure list must not reference a column the duplicate-detection can't match
// (a typo would surface as a non-duplicate error). A trivial compile+run of migrate on a
// fresh db already exercises every ALTER against the freshly-created table (all duplicates).
func TestMigrateFreshAllDuplicates(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	s, err := openStore(db)
	if err != nil {
		// Any error here means an ensure entry produced something other than the
		// tolerated "duplicate column name" against the just-created table.
		if strings.Contains(err.Error(), "duplicate column name") {
			t.Fatalf("duplicate-column error escaped the tolerance: %v", err)
		}
		t.Fatalf("fresh migrate failed: %v", err)
	}
	_ = s.Close()
}
