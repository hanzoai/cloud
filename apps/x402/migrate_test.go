package x402

import (
	"database/sql"
	"testing"
)

// The migration must reach a database that already has the table.
//
// `CREATE TABLE IF NOT EXISTS` is a no-op once the table exists, so a column
// added to that DDL never lands on a live database — and the DDL is the only
// place anyone thinks to add one. `settled` went in that way, the pending index
// is partial on `settled`, and so the migration did not merely skip the column:
// it failed outright with `no such column: settled`. Mount returns that error,
// and a lazy plugin that cannot mount answers 503 `no instance running` to every
// /v1/x402 request until someone reads the host's log. It did exactly that in
// production, and nothing in this package would have caught it: every other test
// opens a FRESH database, where the CREATE TABLE writes all the columns and the
// migration has nothing to migrate.
//
// So this test starts where production started — the table as it shipped BEFORE
// the four retrofits — and then migrates.
const shipped = `
CREATE TABLE settlements (
  id          TEXT PRIMARY KEY,
  payer_org   TEXT NOT NULL,
  from_addr   TEXT NOT NULL,
  nonce       TEXT NOT NULL,
  resource    TEXT NOT NULL,
  payee       TEXT NOT NULL,
  payee_org   TEXT NOT NULL,
  amount      TEXT NOT NULL,
  settled_via TEXT NOT NULL,
  created_at  INTEGER NOT NULL
);`

func columns(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info('settlements')`)
	if err != nil {
		t.Fatalf("read columns: %v", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[n] = true
	}
	return out
}

func TestMigrateAddsColumnsToATableThatAlreadyExists(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Skipf("no in-memory sqlite driver here: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(shipped); err != nil {
		t.Fatalf("seed the shipped schema: %v", err)
	}

	s := &store{db: db}
	if err := s.migrate(); err != nil {
		t.Fatalf("migrate over an existing table: %v", err)
	}

	have := columns(t, db)
	for _, c := range []string{"payee_subj", "network", "tx_hash", "settled"} {
		if !have[c] {
			t.Errorf("column %q missing after migrate — CREATE TABLE IF NOT EXISTS "+
				"cannot add it, so it needs an ALTER", c)
		}
	}

	// The statement the missing column actually killed. Re-running migrate must
	// also be clean, because it runs on every open.
	if err := s.migrate(); err != nil {
		t.Fatalf("migrate is not idempotent: %v", err)
	}
}

func TestMigrateOnAFreshDatabaseAgrees(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Skipf("no in-memory sqlite driver here: %v", err)
	}
	defer db.Close()

	s := &store{db: db}
	if err := s.migrate(); err != nil {
		t.Fatalf("migrate on a fresh database: %v", err)
	}
	have := columns(t, db)
	for _, c := range []string{"payee_subj", "network", "tx_hash", "settled"} {
		if !have[c] {
			t.Errorf("fresh database is missing %q", c)
		}
	}
}
