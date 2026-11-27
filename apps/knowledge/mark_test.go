package knowledge

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
)

// A file an earlier build marked built — as its PRAGMA user_version, which is not a
// table and does not travel — opens built, and the mark is carried into the table the
// store keeps it in now, so the build (which embeds every document) is not run again.
func TestAnEarlierBuildsMarkIsCarried(t *testing.T) {
	t.Setenv(locus.Env, "")
	db, err := locus.Open(namespace.MustOrg("acme"), "knowledge", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	st, err := openStore(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if !st.built.Load() {
		t.Fatal("a file marked built by an earlier build opened unbuilt")
	}
	var v int
	if err := st.db.QueryRow(`SELECT value FROM marks WHERE name = 'built'`).Scan(&v); err != nil || v != 1 {
		t.Fatalf("the mark was not carried into marks: %d %v", v, err)
	}
}

// A store is unbuilt until its build says so, and says so once, on either engine; a store
// opened again reads the mark it left.
func TestTheBuildMarkSurvivesAReopenOnEitherEngine(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ns := namespace.MustOrg("acme")
		st, err := openStore(open(ns, "knowledge"))
		if err != nil {
			t.Fatal(err)
		}
		if st.built.Load() {
			t.Fatal("a store nobody built opened built")
		}
		for range 2 { // marking twice is the same as once
			if err := st.markBuilt(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.Close(); err != nil { // a file is reopened after its handle is closed
			t.Fatal(err)
		}
		again, err := openStore(open(ns, "knowledge"))
		if err != nil {
			t.Fatal(err)
		}
		if !again.built.Load() {
			t.Fatal("the mark did not survive a reopen")
		}
		if n := countRows(t, again, `SELECT COUNT(*) FROM marks`); n != 1 {
			t.Fatalf("%d marks, want 1", n)
		}
	})
}

func countRows(t *testing.T, st *store, query string) int {
	t.Helper()
	var n int
	if err := st.db.QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
