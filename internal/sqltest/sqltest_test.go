package sqltest

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
)

type recorder struct {
	errors   []string
	cleanups []func()
}

func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}
func (r *recorder) Cleanup(f func()) { r.cleanups = append(r.cleanups, f) }
func (r *recorder) finish() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
}

// A skipped PostgreSQL leg reads as a pass, so CI must refuse to skip it: the leg
// decides by this, and any one of the variables a CI system sets is enough.
func TestCIIsWhatAnnouncesItself(t *testing.T) {
	names := []string{"CI", "GITHUB_ACTIONS", "HANZO_CI"}
	for value, want := range map[string]bool{"": false, "false": false, "0": false, "true": true, "1": true, "yes": true} {
		for _, name := range names {
			for _, other := range names {
				t.Setenv(other, "")
			}
			t.Setenv(name, value)
			if got := InCI(); got != want {
				t.Errorf("%s=%q: InCI() = %v, want %v", name, value, got, want)
			}
		}
	}
}

// The runtime twin reports each statement that is not in the shared dialect, once,
// and nothing a connection says after the test is over.
func TestTheHarnessReadsEveryStatementItRuns(t *testing.T) {
	dsn := os.Getenv(Env)
	if dsn == "" {
		if InCI() {
			t.Fatal(Env + " is not set in CI")
		}
		t.Skip(Env + " is not set")
	}
	t.Setenv(locus.Env, throwaway(t, dsn))
	t.Setenv("CLOUD_ENV", "test")
	var r recorder
	check(&r)
	db, err := locus.Open(namespace.MustOrg("acme"), "flags", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	for range 2 { // the same slip twice is reported once
		if _, err := db.Exec(`SELECT $2::text, $1::text`, "a", "b"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`SELECT $1::text, $2::text`, "a", "b"); err != nil { // right
		t.Fatal(err)
	}
	// A gap: the server refuses it as well, but the harness reads the statement first.
	if rows, err := db.Query(`SELECT $1::text FROM (SELECT 1) s WHERE $3::text = 'x'`, "a", "b", "c"); err == nil {
		_ = rows.Close()
	}
	stmt, err := db.Prepare(`SELECT $2::text, $1::text, 7`) // a prepared statement is a statement
	if err != nil {
		t.Fatal(err)
	}
	_ = stmt.Close()
	if len(r.errors) != 3 {
		t.Fatalf("the harness reported %d problems, want the out-of-order, the gap and the prepared one:\n%s", len(r.errors), strings.Join(r.errors, "\n"))
	}
	if !strings.Contains(r.errors[0], "first appears after") || !strings.Contains(r.errors[1], "gap") {
		t.Fatalf("reports = %v", r.errors)
	}
	r.finish()
	if _, err := db.Exec(`SELECT $2::text, $1::text, 9`, "a", "b"); err != nil {
		t.Fatal(err)
	}
	if len(r.errors) != 3 {
		t.Fatal("a statement run after the test was over was reported to it")
	}
}
