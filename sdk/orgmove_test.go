package cloud

// orgmove_test.go — stores coming off their files while the OrgStore that owns them goes
// on serving: writers that arrive during a move wait and then write to hanzo-sql and lose
// nothing, a sweep moves the rest smallest first and stops when it is told, and a store
// that has not moved is still seen by the sweeps that meter and settle.

import (
	"database/sql"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/driver/postgres"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/namespace"
)

// padded is a subsystem's open func whose rows weigh something, so the files of stores
// with more rows are bigger.
func padded(db *sql.DB) (*sql.DB, error) {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS rows (v BIGINT NOT NULL, pad TEXT NOT NULL DEFAULT '')`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// fill writes n rows of about a kilobyte into the store the OrgStore holds for ns.
func fill(t *testing.T, stores *OrgStore[*sql.DB], ns namespace.Namespace, n int) {
	t.Helper()
	st, err := stores.For(ns)
	if err != nil {
		t.Fatal(err)
	}
	pad := strings.Repeat("x", 1000)
	for i := range n {
		if _, err := st.Exec(`INSERT INTO rows (v, pad) VALUES ($1, $2)`, i, pad); err != nil {
			t.Fatal(err)
		}
	}
}

func count(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM rows`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The files of a deployment that is about to move: one store per org, made as they are
// made today, with the DSN unset.
func productionFiles(t *testing.T, dir string, sizes map[string]int) {
	t.Helper()
	t.Setenv(locus.Env, "")
	old := NewOrgStore(Base{DataDir: dir}, "flags", padded)
	for org, n := range sizes {
		fill(t, old, principal.MustOrgNamespace(org, ""), n)
	}
	if err := old.CloseAll(); err != nil {
		t.Fatal(err)
	}
}

func TestAWriterThatArrivesDuringAMoveWaitsAndWritesToTheServer(t *testing.T) {
	dir := t.TempDir()
	acme := principal.MustOrgNamespace("acme", "")
	productionFiles(t, dir, map[string]int{"acme": 30})
	dsn := sqltest.Server(t)
	t.Setenv(locus.MoveEnv, "lazy")

	// The store is on the server and being filled when this open is let go.
	entered, gate := make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	open := func(db *sql.DB) (*sql.DB, error) {
		db, err := padded(db)
		if err == nil && dialect.Of(db).Postgres() && once.CompareAndSwap(false, true) {
			close(entered)
			<-gate
		}
		return db, err
	}
	stores := NewOrgStore(Base{DataDir: dir}, "flags", open)
	t.Cleanup(func() { _ = stores.CloseAll() })

	const writers = 24
	var done atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, writers+1)
	write := func(i int) {
		defer wg.Done()
		st, err := stores.For(acme)
		if err != nil {
			errs <- err
			return
		}
		if _, err := st.Exec(`INSERT INTO rows (v) VALUES ($1)`, 1000+i); err != nil {
			errs <- err
			return
		}
		done.Add(1)
	}
	wg.Add(1)
	go write(0) // the open that moves the store
	<-entered
	for i := 1; i < writers; i++ {
		wg.Add(1)
		go write(i)
	}
	time.Sleep(200 * time.Millisecond)
	if n := done.Load(); n != 0 {
		t.Fatalf("%d writers wrote while the store was being moved", n)
	}
	close(gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("a writer failed: %v", err)
	}

	st, err := stores.For(acme)
	if err != nil {
		t.Fatal(err)
	}
	if !dialect.Of(st).Postgres() {
		t.Fatal("the store is not on the server")
	}
	if n := count(t, st); n != 30+writers {
		t.Fatalf("the server holds %d rows; want the 30 the file had and the %d writes", n, writers)
	}
	var bytes int
	if err := st.QueryRow(`SELECT COUNT(*) FROM rows WHERE length(pad) = 1000`).Scan(&bytes); err != nil || bytes != 30 {
		t.Fatalf("%d of the file's rows arrived whole, %v", bytes, err)
	}

	// Not one of the writes reached the file.
	_ = stores.CloseAll()
	t.Setenv(locus.Env, "")
	file := NewOrgStore(Base{DataDir: dir}, "flags", padded)
	t.Cleanup(func() { _ = file.CloseAll() })
	f, err := file.For(acme)
	if err != nil {
		t.Fatal(err)
	}
	if n := count(t, f); n != 30 {
		t.Fatalf("the file holds %d rows after the move; want the 30 it had", n)
	}
	t.Setenv(locus.Env, dsn)
}

// A caller does not wait for ever for a move that takes long: it is told so.
func TestAWaiterIsToldTheStoreIsBeingMoved(t *testing.T) {
	dir := t.TempDir()
	acme := principal.MustOrgNamespace("acme", "")
	productionFiles(t, dir, map[string]int{"acme": 3})
	sqltest.Server(t)
	t.Setenv(locus.MoveEnv, "lazy")
	was := openWait
	openWait = 50 * time.Millisecond
	t.Cleanup(func() { openWait = was })

	entered, gate := make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	open := func(db *sql.DB) (*sql.DB, error) {
		db, err := padded(db)
		if err == nil && dialect.Of(db).Postgres() && once.CompareAndSwap(false, true) {
			close(entered)
			<-gate
		}
		return db, err
	}
	stores := NewOrgStore(Base{DataDir: dir}, "flags", open)
	t.Cleanup(func() { _ = stores.CloseAll() })
	first := make(chan error, 1)
	go func() { _, err := stores.For(acme); first <- err }()
	<-entered
	began := time.Now()
	if _, err := stores.For(acme); !errors.Is(err, locus.ErrMoving) {
		t.Fatalf("a waiter was answered %v; want ErrMoving", err)
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("the waiter waited %v", took)
	}
	close(gate)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	st, err := stores.For(acme)
	if err != nil || count(t, st) != 3 {
		t.Fatalf("the store after the move: %v", err)
	}
}

// The sweep moves the stores nobody asks for, smallest first, and then it is done.
func TestTheSweepMovesTheRestSmallestFirst(t *testing.T) {
	dir := t.TempDir()
	sizes := map[string]int{"big": 400, "small": 4, "middle": 60}
	productionFiles(t, dir, sizes)
	sqltest.Server(t)
	t.Setenv(locus.MoveEnv, "sweep")

	schemas := map[string]string{}
	for org := range sizes {
		s, err := postgres.Schema(principal.MustOrgNamespace(org, ""), "flags")
		if err != nil {
			t.Fatal(err)
		}
		schemas[s] = org
	}
	var mu sync.Mutex
	var order []string
	open := func(db *sql.DB) (*sql.DB, error) {
		db, err := padded(db)
		if err == nil && dialect.Of(db).Postgres() {
			var schema string
			if err := db.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
				return nil, err
			}
			mu.Lock()
			if org := schemas[schema]; len(order) == 0 || order[len(order)-1] != org {
				order = append(order, org)
			}
			mu.Unlock()
		}
		return db, err
	}
	stores := NewOrgStore(Base{DataDir: dir}, "flags", open)
	t.Cleanup(func() { _ = stores.CloseAll() })

	deadline := time.Now().Add(60 * time.Second)
	for {
		moved := 0
		for org := range sizes {
			if ok, err := locus.Exists(principal.MustOrgNamespace(org, ""), "flags"); err == nil && ok {
				moved++
			}
		}
		if moved == len(sizes) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the sweep moved %d of %d stores in a minute", moved, len(sizes))
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	moved := strings.Join(dedupe(order), ",")
	mu.Unlock()
	if got := moved; got != "small,middle,big" {
		t.Fatalf("the sweep moved %s; want smallest first: small,middle,big", got)
	}
	for org, n := range sizes {
		st, err := stores.For(principal.MustOrgNamespace(org, ""))
		if err != nil {
			t.Fatal(err)
		}
		if got := count(t, st); got != n {
			t.Errorf("%s holds %d rows after its move; want %d", org, got, n)
		}
	}
}

func dedupe(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// A sweep told to stop stops: no store is left half moved and no move goes on after
// CloseAll has returned.
func TestTheSweepStopsWhenTheStoreCloses(t *testing.T) {
	dir := t.TempDir()
	sizes := map[string]int{}
	for i := range 60 {
		sizes[fmt.Sprintf("org%02d", i)] = 20
	}
	productionFiles(t, dir, sizes)
	dsn := sqltest.Server(t)
	t.Setenv(locus.MoveEnv, "sweep")

	started := make(chan struct{})
	var once sync.Once
	open := func(db *sql.DB) (*sql.DB, error) {
		if dialect.Of(db).Postgres() {
			once.Do(func() { close(started) })
		}
		return padded(db)
	}
	stores := NewOrgStore(Base{DataDir: dir}, "flags", open)
	<-started
	if err := stores.CloseAll(); err != nil {
		t.Fatal(err)
	}

	reg, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reg.Close() }()
	var moving, moved int
	query := func() {
		if err := reg.QueryRow(`SELECT COUNT(*) FILTER (WHERE state = 'moving'), COUNT(*) FILTER (WHERE state = 'moved') FROM public.cloud_schemas WHERE subsystem = 'flags'`).Scan(&moving, &moved); err != nil {
			t.Fatal(err)
		}
	}
	query()
	before := moved
	if moving != 0 {
		t.Fatalf("%d stores are left half moved after CloseAll", moving)
	}
	time.Sleep(300 * time.Millisecond)
	query()
	if moved != before || moving != 0 {
		t.Fatalf("the sweep went on after CloseAll: %d moved, then %d, %d moving", before, moved, moving)
	}
	if moved >= len(sizes) {
		t.Skip("the sweep finished before it could be stopped")
	}
}

// While stores are still files, the sweeps that meter and settle see them, and nothing
// is made on the server for them.
func TestStoresThatAreStillFilesAreSwept(t *testing.T) {
	dir := t.TempDir()
	productionFiles(t, dir, map[string]int{"acme": 2, "globex": 3})
	dsn := sqltest.Server(t)

	stores := NewOrgStore(Base{DataDir: dir}, "flags", padded)
	t.Cleanup(func() { _ = stores.CloseAll() })
	acme := principal.MustOrgNamespace("acme", "")
	if !stores.Has(acme) || !stores.Stored() || stores.Has(principal.MustOrgNamespace("initech", "")) {
		t.Fatal("Has / Stored do not see the stores that are still files")
	}
	seen := map[string]int{}
	if err := stores.Each(func(ns namespace.Namespace, st *sql.DB, err error) {
		if err != nil {
			t.Errorf("Each(%s): %v", ns, err)
			return
		}
		seen[ns.String()] = count(t, st)
		if dialect.Of(st).Postgres() {
			t.Errorf("%s was opened on the server with nothing asking to move it", ns)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen["org/acme"] != 2 || seen["org/globex"] != 3 {
		t.Fatalf("the sweep saw %v", seen)
	}
	if !stores.Owned(acme) {
		t.Fatal("a store that is still a file is not this replica's to act on")
	}
	reg, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reg.Close() }()
	var n int
	if err := reg.QueryRow(`SELECT COUNT(*) FROM public.cloud_schemas WHERE subsystem = 'flags'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d stores were registered on the server for files that were not moved, %v", n, err)
	}
}

// A store of a ported subsystem opens through locus.Use or an OrgStore, never through
// locus.Open: Org opens where the store is and never moves it, so a store that opened
// that way would stay a file for as long as the deployment ran.
func TestNoAppOpensAStoreWithoutTheMove(t *testing.T) {
	fset := token.NewFileSet()
	err := filepath.WalkDir("apps", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "locus" && sel.Sel.Name == "Org" {
					t.Errorf("%s: locus.Open opens a store where it is and never moves it; open through locus.Use", fset.Position(call.Pos()))
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
