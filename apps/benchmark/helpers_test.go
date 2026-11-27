package benchmark

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
)

// bg is the context a test asks the store under.
var bg = context.Background()

// openTestStore opens the arena's database under a directory of the test's own, on the
// engine the test is running on.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	st, err := openStore(dir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	dirs.Store(st, dir)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// dirs remembers where each test store was opened, so a restart can reopen it.
var dirs sync.Map

// restarted closes st and opens the same database again, as a process that starts over
// the same data does.
func restarted(t *testing.T, st *Store) *Store {
	t.Helper()
	dir, _ := dirs.Load(st)
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	again, err := openStore(dir.(string))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	dirs.Store(again, dir)
	t.Cleanup(func() { _ = again.Close() })
	return again
}

// refuseClaim makes the store fail, as a disk that fills does, on any claim stored with
// source as its source.
func refuseClaim(t *testing.T, st *Store, source string) {
	t.Helper()
	stmts := []string{`CREATE TRIGGER refuse_claim BEFORE INSERT ON benchmark_claims
	WHEN NEW.doc LIKE '%"source":"` + source + `"%' BEGIN SELECT RAISE(ABORT, 'disk full'); END`}
	if dialect.Of(st.db).Postgres() {
		stmts = []string{
			`CREATE FUNCTION refuse_claim() RETURNS trigger AS 'BEGIN IF NEW.doc LIKE ''%"source":"` + source + `"%'' THEN RAISE EXCEPTION ''disk full''; END IF; RETURN NEW; END' LANGUAGE plpgsql`,
			`CREATE TRIGGER refuse_claim BEFORE INSERT ON benchmark_claims FOR EACH ROW EXECUTE FUNCTION refuse_claim()`,
		}
	}
	for _, stmt := range stmts {
		if _, err := st.db.ExecContext(bg, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

func storedClaimsOf(t *testing.T, st *Store, bench string) []storedClaim {
	t.Helper()
	got, err := st.Claims(bg, bench)
	if err != nil {
		t.Fatalf("Claims(%q): %v", bench, err)
	}
	return got
}

func attemptsIn(t *testing.T, st *Store, bench string) []attempt {
	t.Helper()
	got, err := st.Attempts(bg, bench)
	if err != nil {
		t.Fatalf("Attempts(%q): %v", bench, err)
	}
	return got
}

func versionsIn(t *testing.T, st *Store, org string) []Preset {
	t.Helper()
	got, err := st.Versions(bg, org)
	if err != nil {
		t.Fatalf("Versions(%q): %v", org, err)
	}
	return got
}

// claimsForT is claimsFor over a reader that must answer.
func claimsForT(t *testing.T, r claimReader, bench string) map[string][]storedClaim {
	t.Helper()
	got, err := claimsFor(bg, r, bench)
	if err != nil {
		t.Fatalf("claimsFor(%q): %v", bench, err)
	}
	return got
}

func writtenTodayT(t *testing.T, st *Store, org string, now time.Time) int {
	t.Helper()
	n, err := st.writtenToday(bg, org, now)
	if err != nil {
		t.Fatalf("writtenToday(%q): %v", org, err)
	}
	return n
}
