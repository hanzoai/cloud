package tenure

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
	_ "github.com/hanzoai/sqlite"
)

// legacyFile opens a SQLite file the way a store does, which is the only kind of database
// Upgrade works on.
func legacyFile(t *testing.T) *sql.DB {
	t.Helper()
	t.Setenv(locus.Env, "")
	db, err := locus.Open(namespace.System(), "sealtest", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// Sealing a legacy table costs time in proportion to its rows: the walk reads each row once,
// so 20 times the rows is about 20 times the time. A walk that began again at the top of the
// table for every batch would cost 400 times as much, and a book of half a million entries
// would not open.
func TestUpgradeTimeGrowsWithRows(t *testing.T) {
	sizes := []int{25000, 100000, 500000}
	took := make([]time.Duration, len(sizes))
	for i, n := range sizes {
		db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "x.db"))
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		ctx := context.Background()
		if _, err := db.ExecContext(ctx, `CREATE TABLE treasury_entries (id TEXT PRIMARY KEY, memo TEXT NOT NULL DEFAULT '')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, fmt.Sprintf(`WITH RECURSIVE c(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM c WHERE i < %d)
			INSERT INTO treasury_entries (id, memo) SELECT printf('e%%09d', i), 'zen-1/chat' FROM c`, n)); err != nil {
			t.Fatal(err)
		}
		f := newFields(t, orgNS(t, "acme"), "finance", cipherOf(t, 1))
		began := time.Now()
		got, err := f.Upgrade(ctx, db, "treasury_entries", []string{"id"}, []string{"memo"})
		took[i] = time.Since(began)
		if err != nil || got != n {
			t.Fatalf("rows=%d sealed=%d err=%v", n, got, err)
		}
		t.Logf("rows=%d upgrade took %s", n, took[i].Round(time.Millisecond))
		_ = db.Close()
	}
	// 20x the rows: linear is 20x the time, a walk that restarts is 400x. 60x is the line.
	if took[2] > 60*took[0] {
		t.Fatalf("%d rows took %s and %d rows took %s: more than linear", sizes[2], took[2], sizes[0], took[0])
	}
}

// The next batch is a seek past the last rowid, never a scan from the top: that is what the
// plan of its read says.
func TestUpgradeBatchSeeksPastTheLastRow(t *testing.T) {
	db := legacyFile(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE two (org TEXT NOT NULL, id TEXT NOT NULL, memo TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, `EXPLAIN QUERY PLAN `+upgradeQuery("two", []string{"org", "id"}, []string{"memo"}, true), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var plan string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if !strings.Contains(plan, "SEARCH two USING INTEGER PRIMARY KEY (rowid>?)") || strings.Contains(plan, "SCAN") || strings.Contains(plan, "TEMP B-TREE") {
		t.Errorf("the next batch is not a seek in rowid order:\n%s", plan)
	}
}

// A batch that fails leaves the batches before it sealed, and the next pass takes up where it
// stopped: the upgrade is resumable across boots, not all or nothing.
func TestUpgradeResumesWhereItStopped(t *testing.T) {
	db := legacyFile(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, peopleDDL); err != nil {
		t.Fatal(err)
	}
	f := newFields(t, namespace.System(), "sealtest", cipherOf(t, 1))
	const rows = upgradeRows*2 + 500
	tx, _ := db.BeginTx(ctx, nil)
	for i := 0; i < rows; i++ {
		if _, err := tx.ExecContext(ctx, `INSERT INTO people (org, id, email) VALUES ($1, $2, $3)`, "acme", fmt.Sprintf("p%06d", i), "p@acme.example"); err != nil {
			t.Fatal(err)
		}
	}
	// The last row refuses to be written, so the third batch fails after the first two commit.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`CREATE TRIGGER people_stuck BEFORE UPDATE ON people WHEN OLD.id = 'p%06d' BEGIN SELECT RAISE(ABORT, 'stuck'); END`, rows-1)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	sealed, err := f.Upgrade(ctx, db, "people", []string{"org", "id"}, []string{"email"})
	if err == nil || sealed != upgradeRows*2 {
		t.Fatalf("sealed %d, %v; want %d and a refusal", sealed, err, upgradeRows*2)
	}
	var plain int
	if err := db.QueryRow(`SELECT COUNT(*) FROM people WHERE substr(email, 1, 5) <> 'seal:'`).Scan(&plain); err != nil || plain != 500 {
		t.Fatalf("%d rows left in plaintext after the failed pass, want 500: %v", plain, err)
	}

	if _, err := db.ExecContext(ctx, `DROP TRIGGER people_stuck`); err != nil {
		t.Fatal(err)
	}
	sealed, err = f.Upgrade(ctx, db, "people", []string{"org", "id"}, []string{"email"})
	if err != nil || sealed != 500 {
		t.Fatalf("the next pass sealed %d, %v; want the 500 that were left", sealed, err)
	}
}

// A key that names no one row, and has no index, still seals every row to its own value: a
// ledger line is keyed by its voucher and account, and a voucher may touch an account twice.
// The rows share one key across several batches here, so a walk by key would skip most of
// them and a write by key would give every one the last row's text.
func TestUpgradeSealsRowsThatShareAKey(t *testing.T) {
	db := legacyFile(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE gl_entry (source_kind TEXT NOT NULL, source_id TEXT NOT NULL, account TEXT NOT NULL, remarks TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	const rows = upgradeRows*2 + 300
	tx, _ := db.BeginTx(ctx, nil)
	for i := 0; i < rows; i++ {
		if _, err := tx.ExecContext(ctx, `INSERT INTO gl_entry (source_kind, source_id, account, remarks) VALUES ('invoice', 'inv-1', '4000', $1)`, fmt.Sprintf("line %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	f := newFields(t, namespace.System(), "sealtest", cipherOf(t, 1))
	n, err := f.Upgrade(ctx, db, "gl_entry", []string{"source_kind", "source_id", "account"}, []string{"remarks"})
	if err != nil || n != rows {
		t.Fatalf("sealed %d of %d: %v", n, rows, err)
	}
	rs, err := db.QueryContext(ctx, `SELECT remarks FROM gl_entry ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rs.Close() }()
	for i := 0; rs.Next(); i++ {
		var stored string
		if err := rs.Scan(&stored); err != nil {
			t.Fatal(err)
		}
		got, err := openField(f, "gl_entry", "remarks", []string{"invoice", "inv-1", "4000"}, stored)
		if err != nil || got != fmt.Sprintf("line %d", i) {
			t.Fatalf("row %d reads %q, %v", i, got, err)
		}
	}
}

// hanzo-sql tables are born sealed, so there is nothing to repair there and Upgrade leaves
// them alone: a plaintext value found on the server is a defect, and Row.Open says so.
func TestUpgradeLeavesHanzoSQLAlone(t *testing.T) {
	sqltest.Server(t)
	db, err := locus.Open(namespace.System(), "sealtest", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, peopleDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO people (org, id, email) VALUES ('acme', '1', 'ada@acme.example')`); err != nil {
		t.Fatal(err)
	}
	f := newFields(t, namespace.System(), "sealtest", cipherOf(t, 1))
	n, err := f.Upgrade(ctx, db, "people", []string{"org", "id"}, []string{"email"})
	if err != nil || n != 0 {
		t.Fatalf("sealed %d, %v; want nothing", n, err)
	}
	if got := rawPeople(t, db, "acme", "1", "email"); got != "ada@acme.example" {
		t.Fatalf("the value was rewritten: %q", got)
	}
	if _, err := openField(f, "people", "email", []string{"acme", "1"}, rawPeople(t, db, "acme", "1", "email")); !errors.Is(err, ErrFieldPlain) {
		t.Fatalf("a plaintext value on the server opened: %v", err)
	}
}

func TestUpgradeRefusesToSealItsOwnKey(t *testing.T) {
	f := one(t)
	if _, err := f.Upgrade(context.Background(), nil, "people", []string{"org", "email"}, []string{"email"}); err == nil {
		t.Fatal("a column that is part of the key was accepted for sealing")
	}
}
