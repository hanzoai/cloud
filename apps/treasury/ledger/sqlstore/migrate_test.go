package sqlstore

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/treasury/ledger"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cek"
	"github.com/hanzoai/namespace"
)

// legacyBook makes the file a version before the seq column left behind: entries and
// postings, no seq, no trigger.
func legacyBook(db *sql.DB, entries int) error {
	for _, stmt := range []string{
		`CREATE TABLE treasury_accounts (id TEXT PRIMARY KEY, kind TEXT NOT NULL, balance TEXT NOT NULL DEFAULT '0', created_at INTEGER NOT NULL)`,
		`CREATE TABLE treasury_entries (id TEXT PRIMARY KEY, kind TEXT NOT NULL, program TEXT NOT NULL DEFAULT '',
		   ref TEXT NOT NULL, memo TEXT NOT NULL DEFAULT '', project TEXT NOT NULL DEFAULT '', service TEXT NOT NULL DEFAULT '',
		   amount TEXT NOT NULL DEFAULT '0', created_at INTEGER NOT NULL, UNIQUE(kind, program, ref))`,
		`CREATE TABLE treasury_postings (id INTEGER PRIMARY KEY AUTOINCREMENT, entry_id TEXT NOT NULL,
		   account TEXT NOT NULL, amount TEXT NOT NULL DEFAULT '0', created_at INTEGER NOT NULL)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	for i := range entries {
		// Ids run backwards against the write order, so a numbering by id would be wrong.
		if _, err := tx.Exec(`INSERT INTO treasury_entries (id,kind,ref,created_at) VALUES ($1,'k',$2,7)`,
			fmt.Sprintf("e%07d", entries-i), fmt.Sprint("r", i)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// bookKey is the master the three processes of the kill test share. cek's dev master
// is random per process, and a file keyed under one cannot be opened by another.
var bookKey = []byte("kill-mid-migration-test-master!!")

// TestMigrationChild is the other half of TestKilledMigrationLeavesABookThatOpens. It
// runs only when the parent starts the test binary with arguments after the flags
// (the standard library's helper-process idiom, with the mode on the command line
// where the environment would otherwise carry it): `seed DIR`, `migrate DIR`, or
// `verify DIR`. Run on its own, it returns at once.
func TestMigrationChild(t *testing.T) {
	if flag.NArg() < 2 {
		return
	}
	mode, dir := flag.Arg(0), flag.Arg(1)
	if err := cek.SetMaster(bookKey); err != nil {
		t.Fatal(err)
	}
	ns := namespace.System()
	switch mode {
	case "seed":
		db, err := cek.Open(ns, "treasury", dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := legacyBook(db, killedEntries); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	case "migrate", "verify":
		s, err := Open(ns, "treasury", dir, "", testMaster)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if mode == "verify" {
			verifySeq(t, s.db, killedEntries)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown mode %q", mode)
	}
}

const killedEntries = 20000

// verifySeq holds a migrated book to its invariants: every entry numbered, the
// numbers distinct and in write order, and the next entry takes the next number.
func verifySeq(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	var rows, zeros, distinct, outOfOrder int
	if err := db.QueryRow(`SELECT COUNT(*), SUM(CASE WHEN seq = 0 THEN 1 ELSE 0 END), COUNT(DISTINCT seq) FROM treasury_entries`).Scan(&rows, &zeros, &distinct); err != nil {
		t.Fatalf("count: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM treasury_entries WHERE seq <> rowid`).Scan(&outOfOrder); err != nil {
		t.Fatalf("order: %v", err)
	}
	if rows != n || zeros != 0 || distinct != n || outOfOrder != 0 {
		t.Fatalf("after migration: %d rows (want %d), %d unnumbered, %d distinct, %d out of write order", rows, n, zeros, distinct, outOfOrder)
	}
}

// A migration that dies at any point leaves a book that opens, and finishes.
//
// SQLite's DDL is transactional, so the column, its numbering, its index and its
// trigger are one transaction: killed before the commit the file is what it was, and
// killed after it the file is migrated. The failure this guards is the one that was
// written first, where the column was added and committed before the numbering ran: a
// kill between them left every row at seq 0, and the unique index then refused to
// build on every open after. The parent seeds a legacy book of 20,000 entries in a
// child process, copies it once per kill point, kills a child opening each copy with
// SIGKILL after a different delay, and then opens the survivor in a third process.
func TestKilledMigrationLeavesABookThatOpens(t *testing.T) {
	t.Setenv(locus.Env, "")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := func(mode, dir string) *exec.Cmd {
		return exec.Command(self, "-test.run=^TestMigrationChild$", "-test.count=1", mode, dir)
	}
	pristine := t.TempDir()
	if out, err := child("seed", pristine).CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}

	killed := 0
	for _, delay := range []time.Duration{0, time.Millisecond, 3 * time.Millisecond, 8 * time.Millisecond, 15 * time.Millisecond,
		30 * time.Millisecond, 60 * time.Millisecond, 120 * time.Millisecond, 250 * time.Millisecond} {
		dir := t.TempDir()
		if err := copyTree(pristine, dir); err != nil {
			t.Fatal(err)
		}
		m := child("migrate", dir)
		if err := m.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(delay)
		if m.Process.Kill() == nil {
			killed++
		}
		_ = m.Wait()

		if out, err := child("verify", dir).CombinedOutput(); err != nil {
			t.Fatalf("kill after %v: the book does not open after it: %v\n%s", delay, err, out)
		}
	}
	if killed == 0 {
		t.Fatal("no child was killed, so no kill point was exercised")
	}
}

// The version that predates the column inserts entries without a seq. After the
// upgrade it must keep working, and what it writes must be numbered, so rolling the
// binary back needs no data step.
func TestAnOlderBinaryStillWritesAMigratedBook(t *testing.T) {
	t.Setenv(locus.Env, "")
	db, err := cek.Open(namespace.System(), "oldwriter", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := legacyBook(db, 3); err != nil {
		t.Fatal(err)
	}
	s := &Store{db: db, fields: testFields(t)}
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	// The old binary's INSERT: no seq column named.
	for i := range 3 {
		if _, err := db.Exec(`INSERT INTO treasury_entries (id,kind,ref,created_at) VALUES ($1,'k',$2,8)`, fmt.Sprint("old", i), fmt.Sprint("o", i)); err != nil {
			t.Fatalf("an older binary's insert %d after the upgrade: %v", i, err)
		}
	}
	verifySeq(t, db, 6)
	// And the new binary carries on from there.
	if err := s.Tx(context.Background(), func(tx ledger.Tx) error {
		return tx.Insert(ledger.JournalEntry{ID: "new", Kind: "k", Ref: "n", Amount: money.FromCents(1), CreatedAt: 9},
			[]ledger.Posting{{Account: "a", Amount: money.FromCents(1)}, {Account: "b", Amount: money.FromCents(-1)}})
	}); err != nil {
		t.Fatal(err)
	}
	verifySeq(t, db, 7)
	es, _ := s.Entries(context.Background(), 1)
	if len(es) != 1 || es[0].ID != "new" {
		t.Fatalf("newest entry = %v", ids(es))
	}
}

// A migration that stopped between the column and its numbering (the state a version
// that committed them apart could leave) is finished by the next open.
func TestAHalfUpgradedBookIsFinished(t *testing.T) {
	t.Setenv(locus.Env, "")
	db, err := cek.Open(namespace.System(), "halfway", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := legacyBook(db, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE treasury_entries ADD COLUMN seq BIGINT NOT NULL DEFAULT 0`); err != nil {
		t.Fatal(err)
	}
	s := &Store{db: db, fields: testFields(t)}
	if err := s.migrate(); err != nil {
		t.Fatalf("a half-upgraded book does not open: %v", err)
	}
	verifySeq(t, db, 5)
}

// A panic inside a transaction rolls it back and frees the store for the next one.
func TestAPanicInATxRollsBack(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s := openOn(t, open, "")
		func() {
			defer func() {
				if recover() == nil {
					t.Error("the panic did not propagate")
				}
			}()
			_ = s.Tx(context.Background(), func(tx ledger.Tx) error {
				_ = tx.Insert(ledger.JournalEntry{ID: "p", Kind: "k", Ref: "p", Amount: money.FromCents(1), CreatedAt: 1},
					[]ledger.Posting{{Account: "a", Amount: money.FromCents(1)}, {Account: "b", Amount: money.FromCents(-1)}})
				panic("boom")
			})
		}()
		done := make(chan error, 1)
		go func() {
			done <- s.Tx(context.Background(), func(tx ledger.Tx) error { return nil })
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the store is still held after a panic")
		}
		if es, _ := s.Entries(context.Background(), 0); len(es) != 0 {
			t.Fatalf("the panicked transaction left %d entries", len(es))
		}
	})
}

// copyTree copies the files of one directory tree into another.
func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o700)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o600)
	})
}
