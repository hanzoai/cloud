package sqltest

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/kit"

	// Keys the test binary: cek opens no SQLite file without a master, and a test
	// process has no KMS.
	"github.com/hanzoai/cloud/internal/driver/postgres"
	"github.com/hanzoai/cloud/internal/sqlcheck"
	_ "github.com/hanzoai/cloud/sdk/kit/master"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// Env names the PostgreSQL server the postgres leg runs on: a postgres:// URL whose
// role may create databases, and, like production's, is not a superuser.
const Env = "CLOUD_TEST_SQL_DSN"

// Open opens the database the namespace x subsystem names, on this leg's engine,
// the way cloud.OrgDB does. It closes the handle when the test ends. Opening the
// same pair again, after closing the first handle, reaches the same data: that is
// how a test reopens a store.
type Open func(ns namespace.Namespace, subsystem string) *sql.DB

// Engines runs body once per engine, each as a subtest, in a database of its own:
// a temp dir on SQLite, a database created for the test and dropped after it on
// PostgreSQL. A test that uses t.Parallel cannot be wrapped; the body sets
// environment.
func Engines(t *testing.T, body func(t *testing.T, open Open)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		t.Setenv(locus.Env, "")
		body(t, opener(t))
	})
	t.Run("postgres", func(t *testing.T) {
		Server(t)
		body(t, opener(t))
	})
}

// Server points CLOUD_SQL_DSN at a database made for this test on the PostgreSQL server,
// and drops it when the test ends. It is the postgres leg of Engines for a test that is
// about both engines at once, a store moving from its file to the server, which has no
// SQLite leg to run beside it: the test makes the file first, with the DSN unset, then
// calls this. Without a server it skips, and fails in CI, as the leg does.
func Server(t testing.TB) string {
	t.Helper()
	dsn := os.Getenv(Env)
	if dsn == "" {
		// A leg that skips reads as a pass. On a workstation that is a convenience;
		// in CI it would let a store go unproven on PostgreSQL with every gate green.
		if InCI() {
			t.Fatal(Env + " is not set in CI, so this store was not run on PostgreSQL; run the gate under scripts/postgres.sh")
		}
		t.Skip(Env + " is not set; scripts/postgres.sh starts a server for a command")
	}
	check(t)
	db := throwaway(t, dsn)
	t.Setenv(locus.Env, db)
	if os.Getenv("CLOUD_ENV") == "" {
		// The seam refuses a database it cannot bind to a named environment.
		t.Setenv("CLOUD_ENV", "test")
	}
	if os.Getenv(postgres.DialTimeoutEnv) == "" {
		// The seam gives a server 3s to answer, which is right for a production server
		// that may be gone. This one is local and shares its box with every package
		// under test, and under that load it stalls for seconds: the 3s bound failed
		// whole packages at once in CI. 30s is the bound the HTTP tests here use.
		t.Setenv(postgres.DialTimeoutEnv, "30s")
	}
	return db
}

// reporter is the part of a *testing.T that check needs, so a test can hand it a
// recorder.
type reporter interface {
	Errorf(format string, args ...any)
	Cleanup(func())
}

// check holds every statement the postgres leg executes to the dialect both engines
// read alike (internal/sqlcheck), as the connection receives it. It is the twin of the
// source gate (sqlgate_test.go in the root package): that one reads string literals
// and the text they are joined into, and cannot read a statement composed across a
// closure, a method value, a helper's return, a struct field or another package's
// constant; this one reads nothing but what was run. The text is the same on the
// SQLite leg, so one leg reads it for both. A statement is reported once.
func check(t reporter) {
	var mu sync.Mutex
	seen := map[string]bool{}
	over := false
	restore := postgres.Observe(func(query string) {
		mu.Lock()
		defer mu.Unlock()
		if over || seen[query] {
			return
		}
		seen[query] = true
		for _, problem := range sqlcheck.Problems(query, true) {
			t.Errorf("a statement this test executed is not in the shared dialect: %s\n\t%s", problem, query)
		}
	})
	t.Cleanup(func() {
		mu.Lock()
		over = true // a connection still closing when the test ends is not the test's
		mu.Unlock()
		restore()
	})
}

// InCI reports whether the process runs under a CI system. Most announce it with CI,
// and the ones this estate runs also say GITHUB_ACTIONS (the forge's runner copies
// it) and HANZO_CI. A test that would skip for want of a PostgreSQL asks, and fails
// instead.
func InCI() bool { return kit.CI() }

func opener(t *testing.T) Open {
	dir := t.TempDir()
	return func(ns namespace.Namespace, subsystem string) *sql.DB {
		t.Helper()
		db, err := locus.Open(ns, subsystem, dir)
		if err != nil {
			t.Fatalf("open %s/%s: %v", ns, subsystem, err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
}

// throwaway makes a database on the server dsn names and drops it when the test
// ends, whatever still holds a connection to it. It answers the DSN of that
// database. A database and not a schema, because the seam names its own schemas
// from the store and a body may open any number of them: only a database holds
// all of them and the registry beside them.
func throwaway(t testing.TB, dsn string) string {
	t.Helper()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("%s: %v", Env, err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	var token [6]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	name := "t_" + hex.EncodeToString(token[:])
	if _, err := admin.Exec(`CREATE DATABASE ` + name); err != nil {
		t.Fatalf("create the test database on %s: %v", Env, err)
	}
	t.Cleanup(func() {
		if err := drop(admin, name); err != nil {
			t.Errorf("drop the test database: %v", err)
		}
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("%s: %v", Env, err)
	}
	u.Path = "/" + name
	return u.String()
}

// dropWait is how long drop waits out sessions that are still ending.
const dropWait = 30 * time.Second

// drop drops a test database, waiting for the server. WITH (FORCE) tells every session
// on it to end and then gives them five seconds to go. On a server the whole test run is
// loading they have taken longer: CI run 17680 failed a test that had passed with "is
// being accessed by other users" (55006) at this drop. Only that refusal is waited out,
// for up to dropWait; any other error, or a session still there after that (a pool that
// keeps reconnecting), is the caller's to report.
//
// A session of another role that the test closed and that has not finished exiting is
// listed by the server but is not the harness's to end: FORCE answers permission denied
// (42501) for it, as TestOrgRefusesASuperuser's refused superuser connection does. It is
// waited out the same way, because closing it is all the test can do and it is gone in
// moments.
func drop(admin *sql.DB, name string) error {
	give := time.Now().Add(dropWait)
	for {
		_, err := admin.Exec(`DROP DATABASE ` + name + ` WITH (FORCE)`)
		var pg *pgconn.PgError
		if err == nil || !errors.As(err, &pg) || (pg.Code != "55006" && pg.Code != "42501") || time.Now().After(give) {
			return err
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// Peak watches the number of connections the server holds for the database dsn
// names, from a connection of its own that is outside any pool under test, and the
// func it returns stops the watch and answers the highest count it saw. Sampled every
// millisecond, so it is a floor on the true peak and the budget a test holds a
// process to is a ceiling on it.
func Peak(t *testing.T, dsn string) (stop func() int) {
	t.Helper()
	probe, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	probe.SetMaxOpenConns(1)
	var high atomic.Int64
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		for {
			var n int64
			if probe.QueryRow(`SELECT COUNT(*) FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid()`).Scan(&n) == nil && n > high.Load() {
				high.Store(n)
			}
			select {
			case <-done:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	return func() int {
		close(done)
		<-finished
		_ = probe.Close()
		return int(high.Load())
	}
}

// Dump reads every row of every table a store keeps, each row as its columns by name,
// sorted, so a store on one engine can be held against the same store on the other: the
// oracle of a move, independent of the checksum the move computes itself. A virtual table
// (SQLite's full-text indexes) and the tables FTS5 keeps beside one are not the store's
// data and are left out, and so is a column PostgreSQL computes (`fts`); a column or
// table only one side has is a difference the caller reads.
func Dump(t testing.TB, db *sql.DB) map[string][]string {
	t.Helper()
	_, pg := db.Driver().(*stdlib.Driver)
	var tables []string
	if pg {
		tables = names(t, db, `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'`)
	} else {
		virtual := names(t, db, `SELECT name FROM sqlite_master WHERE type = 'table' AND sql LIKE 'CREATE VIRTUAL TABLE%'`)
		skip := map[string]bool{}
		for _, v := range virtual {
			skip[v] = true
			for _, suffix := range []string{"_data", "_idx", "_content", "_docsize", "_config"} {
				skip[v+suffix] = true
			}
		}
		for _, name := range names(t, db, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`) {
			if !skip[name] {
				tables = append(tables, name)
			}
		}
	}
	out := map[string][]string{}
	for _, table := range tables {
		rows, err := db.Query(`SELECT * FROM "` + table + `"`)
		if err != nil {
			t.Fatalf("dump %s: %v", table, err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		lines := []string{}
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatalf("dump %s: %v", table, err)
			}
			cells := make([]string, 0, len(cols))
			for i, c := range cols {
				if c == "fts" {
					continue
				}
				cells = append(cells, c+"="+render(vals[i]))
			}
			sort.Strings(cells)
			lines = append(lines, strings.Join(cells, "|"))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
		sort.Strings(lines)
		out[table] = lines
	}
	return out
}

func names(t testing.TB, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func render(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		if x {
			return "1"
		}
		return "0"
	case []byte:
		return fmt.Sprintf("x'%x'", x)
	case string:
		return strconv.Quote(x)
	}
	return fmt.Sprintf("%v", v)
}

// Same fails the test unless every table that held rows in want holds exactly those rows
// in got. A table only got has (a table the new code made) is not a difference, and the
// rows of a table only want has are one.
func Same(t testing.TB, want, got map[string][]string) {
	t.Helper()
	rows := 0
	for table, lines := range want {
		if len(lines) == 0 {
			continue
		}
		rows += len(lines)
		have := got[table]
		if len(have) != len(lines) {
			t.Errorf("%s: %d rows, want %d", table, len(have), len(lines))
			continue
		}
		for i := range lines {
			if have[i] != lines[i] {
				t.Errorf("%s: row differs\n got %s\nwant %s", table, have[i], lines[i])
				break
			}
		}
	}
	if rows == 0 {
		t.Error("the store held no rows, so there is nothing to compare")
	}
}
