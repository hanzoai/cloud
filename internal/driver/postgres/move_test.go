package postgres_test

// move_test.go — a store leaving its file for hanzo-sql, held to what the owner asked
// of it: nothing lost, nothing doubled, the next insert not colliding, and a failure that
// leaves the store exactly where it was.
//
// The store here is a toy with every kind of column, a foreign key, rows its own
// migration seeds and the trigram index; the stores that are really ported are held to
// the same in their own packages (apps/flags, apps/agents, apps/treasury).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/driver/postgres"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
)

type widget struct{ db *sql.DB }

// hasTrigrams reports whether the engine can make the trigram index: PostgreSQL always,
// SQLite when it was built with FTS5, which a distribution's libsqlcipher may not be.
func hasTrigrams(db *sql.DB) bool {
	if dialect.Of(db).Postgres() {
		return true
	}
	var n int
	return db.QueryRow(`SELECT COUNT(*) FROM pragma_module_list WHERE name = 'fts5'`).Scan(&n) == nil && n > 0
}

func (w *widget) Close() error { return w.db.Close() }

// openWidget is a store's open: its migration, in the dialect of whichever engine it is
// handed. fault, when set, is run on PostgreSQL only, after the migration: a rule the
// copy will trip over.
func openWidget(fault string) func(*sql.DB) (*widget, error) {
	return func(db *sql.DB) (*widget, error) {
		d := dialect.Of(db)
		ctx := context.Background()
		trigrams := hasTrigrams(db) // asked here: inside the migration the file's one connection is the transaction's
		err := d.Migrate(ctx, db, func(tx *sql.Tx) error {
			for _, ddl := range []string{
				`CREATE TABLE IF NOT EXISTS parent (
				   id ` + d.Identity() + `,
				   name TEXT NOT NULL ` + d.Bytewise() + `,
				   qty BIGINT NOT NULL,
				   ratio DOUBLE PRECISION,
				   data ` + d.Blob() + `,
				   doc ` + d.JSON() + `,
				   flag BIGINT NOT NULL DEFAULT 0,
				   note TEXT
				 )`,
				`CREATE TABLE IF NOT EXISTS child (
				   id ` + d.Identity() + `,
				   parent_id BIGINT NOT NULL REFERENCES parent(id),
				   label TEXT NOT NULL
				 )`,
				`CREATE TABLE IF NOT EXISTS settings (k TEXT PRIMARY KEY, v TEXT NOT NULL)`,
				// Rows the migration seeds, which the file also has, and the schema must not
				// have twice.
				`INSERT INTO settings (k, v) VALUES ('mode', 'seeded') ON CONFLICT (k) DO NOTHING`,
			} {
				if _, err := tx.ExecContext(ctx, ddl); err != nil {
					return err
				}
			}
			if !trigrams {
				return nil
			}
			g, err := d.Grams("snippets")
			if err != nil {
				return err
			}
			return g.Create(ctx, tx)
		})
		if err != nil {
			return nil, err
		}
		if fault != "" && d.Postgres() {
			if _, err := db.ExecContext(ctx, fault); err != nil {
				return nil, err
			}
		}
		return &widget{db: db}, nil
	}
}

// row is a parent row as the test wrote it.
type row struct {
	ID    int64
	Name  string
	Qty   int64
	Ratio sql.NullFloat64
	Data  []byte
	Doc   sql.NullString
	Flag  int64
	Note  sql.NullString
}

var fixture = []row{
	{Name: "alpha", Qty: 1, Ratio: nf(0.1), Data: []byte{0, 1, 2, 255}, Doc: sv(`{"b": 2, "a": [1, 2.50, true, null, "é"]}`), Flag: 1},
	{Name: "beta ünï", Qty: -9223372036854775808, Ratio: nf(1e300), Data: []byte{}, Doc: sv(`{}`), Note: sv("")},
	{Name: "gamma", Qty: 1 << 62, Ratio: nf(3.141592653589793), Note: sv("multi\nline 'quoted' \"double\"")},
	{Name: "delta", Qty: 7},
}

func nf(f float64) sql.NullFloat64 { return sql.NullFloat64{Float64: f, Valid: true} }
func sv(s string) sql.NullString   { return sql.NullString{String: s, Valid: true} }

func seed(t *testing.T, w *widget) {
	t.Helper()
	ctx := context.Background()
	d := dialect.Of(w.db)
	for i, r := range fixture {
		var id int64
		if err := w.db.QueryRowContext(ctx, `INSERT INTO parent (name, qty, ratio, data, doc, flag, note) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
			r.Name, r.Qty, r.Ratio, r.Data, r.Doc, r.Flag, r.Note).Scan(&id); err != nil {
			t.Fatalf("insert parent %d: %v", i, err)
		}
		if _, err := w.db.ExecContext(ctx, `INSERT INTO child (parent_id, label) VALUES ($1, $2)`, id, "of "+r.Name); err != nil {
			t.Fatal(err)
		}
	}
	if !hasTrigrams(w.db) {
		return
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	g, err := d.Grams("snippets")
	if err != nil {
		t.Fatal(err)
	}
	for id, body := range map[int64]string{1: "Hello World", 2: "Another SNIPPET of text"} {
		if err := g.Put(ctx, tx, id, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func parents(t *testing.T, db *sql.DB) []row {
	t.Helper()
	rows, err := db.Query(`SELECT id, name, qty, ratio, data, doc, flag, note FROM parent ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ID, &r.Name, &r.Qty, &r.Ratio, &r.Data, &r.Doc, &r.Flag, &r.Note); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// same compares two readings of the parent table. A JSON document is its meaning and
// not its spelling: jsonb normalises key order and spacing.
func same(t *testing.T, got, want []row) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d rows, want %d", len(got), len(want))
	}
	for i := range want {
		g, w := got[i], want[i]
		gd, wd := g.Doc, w.Doc
		g.Doc, w.Doc = sql.NullString{}, sql.NullString{}
		if gd.Valid != wd.Valid {
			t.Fatalf("row %d: doc null = %v, want %v", i, !gd.Valid, !wd.Valid)
		}
		if wd.Valid {
			var a, b any
			if err := json.Unmarshal([]byte(gd.String), &a); err != nil {
				t.Fatalf("row %d: doc %q: %v", i, gd.String, err)
			}
			_ = json.Unmarshal([]byte(wd.String), &b)
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("row %d: doc %s, want %s", i, gd.String, wd.String)
			}
		}
		// A NULL blob and an empty one are different values and stay different.
		if (g.Data == nil) != (w.Data == nil) {
			t.Fatalf("row %d: data nil = %v, want %v", i, g.Data == nil, w.Data == nil)
		}
		if g.Data == nil {
			w.Data = nil
		}
		if !reflect.DeepEqual(g, w) {
			t.Fatalf("row %d:\n got %+v\nwant %+v", i, g, w)
		}
	}
}

func wanted() []row {
	out := make([]row, len(fixture))
	for i, r := range fixture {
		r.ID = int64(i + 1)
		out[i] = r
	}
	return out
}

// onFile makes the store as production has it: a file, made with the DSN unset.
func onFile(t *testing.T, ns namespace.Namespace, dir string) {
	t.Helper()
	t.Setenv(locus.Env, "")
	w, err := locus.Use(context.Background(), nil, ns, "widget", dir, openWidget(""))
	if err != nil {
		t.Fatal(err)
	}
	seed(t, w)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// registry reads the registry's word on a store directly, as an operator would.
func registry(t *testing.T, dsn string, ns namespace.Namespace) (state string, moving, moved, failed int64, why string, found bool) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	err = db.QueryRow(`SELECT state, moving, moved, failed, error FROM public.cloud_schemas WHERE namespace = $1 AND subsystem = 'widget'`, ns.String()).
		Scan(&state, &moving, &moved, &failed, &why)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, 0, 0, "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return state, moving, moved, failed, why, true
}

func schemaExists(t *testing.T, dsn string, ns namespace.Namespace) bool {
	t.Helper()
	schema, err := postgres.Schema(ns, "widget")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = $1`, schema).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestAStoreMovesWithEveryColumnKind(t *testing.T) {
	ns := namespace.MustOrg("acme")
	dir := t.TempDir()
	onFile(t, ns, dir)
	dsn := sqltest.Server(t)

	// With the DSN set and nothing asked, the store is what it was: its file.
	if place, err := locus.Where(ns, "widget", dir); err != nil || place != locus.OnFile {
		t.Fatalf("a store nobody moved is at %v, %v; want its file", place, err)
	}
	w, err := locus.Use(context.Background(), nil, ns, "widget", dir, openWidget(""))
	if err != nil {
		t.Fatal(err)
	}
	if dialect.Of(w.db).Postgres() {
		t.Fatal("without CLOUD_SQL_MOVE the store left its file")
	}
	same(t, parents(t, w.db), wanted())
	trigrams := hasTrigrams(w.db) // a distribution's SQLite may be built without FTS5
	_ = w.Close()

	t.Setenv(locus.MoveEnv, "lazy")
	began := time.Now().Unix()
	w, err = locus.Use(context.Background(), nil, ns, "widget", dir, openWidget(""))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	if !dialect.Of(w.db).Postgres() {
		t.Fatal("the store was not moved")
	}
	same(t, parents(t, w.db), wanted())

	// The foreign key arrived whole, the seeded row once, and the index's text lowered.
	var n int
	if err := w.db.QueryRow(`SELECT COUNT(*) FROM child c JOIN parent p ON p.id = c.parent_id`).Scan(&n); err != nil || n != len(fixture) {
		t.Fatalf("%d children joined to their parents, %v; want %d", n, err, len(fixture))
	}
	var mode string
	if err := w.db.QueryRow(`SELECT v FROM settings WHERE k = 'mode'`).Scan(&mode); err != nil || mode != "seeded" {
		t.Fatalf("the seeded row is %q, %v", mode, err)
	}
	if trigrams {
		var body string
		if err := w.db.QueryRow(`SELECT body FROM snippets WHERE id = 2`).Scan(&body); err != nil || body != "another snippet of text" {
			t.Fatalf("the trigram index holds %q, %v", body, err)
		}
	}

	// The identity moved past the copied ids: the next insert does not collide.
	var next int64
	if err := w.db.QueryRow(`INSERT INTO parent (name, qty) VALUES ('after', 0) RETURNING id`).Scan(&next); err != nil || next != int64(len(fixture)+1) {
		t.Fatalf("the first id after the move is %d, %v; want %d", next, err, len(fixture)+1)
	}

	// The registry says so, with when.
	state, moving, moved, _, _, found := registry(t, dsn, ns)
	if !found || state != "moved" || moving < began-1 || moved < moving {
		t.Fatalf("the registry has state %q moving %d moved %d (found %v)", state, moving, moved, found)
	}
	if place, err := locus.Where(ns, "widget", dir); err != nil || place != locus.OnServer {
		t.Fatalf("a moved store is at %v, %v", place, err)
	}

	// The file is where it was, with what it had; the writes since went to the server.
	t.Setenv(locus.Env, "")
	f, err := locus.Use(context.Background(), nil, ns, "widget", dir, openWidget(""))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	same(t, parents(t, f.db), wanted())
}

func TestAFailedMoveLeavesTheStoreItsFileAndRetriesLater(t *testing.T) {
	ns := namespace.MustOrg("acme")
	dir := t.TempDir()
	onFile(t, ns, dir)
	dsn := sqltest.Server(t)
	t.Setenv(locus.MoveEnv, "lazy")
	defer postgres.BackOff(time.Hour)()

	// A rule on the server that one of the file's rows (qty 7) breaks halfway through.
	w, err := locus.Use(context.Background(), nil, ns, "widget", dir, openWidget(`ALTER TABLE parent ADD CONSTRAINT refuse CHECK (qty <> 7)`))
	if err != nil {
		t.Fatal(err)
	}
	if dialect.Of(w.db).Postgres() {
		t.Fatal("a store whose move failed is on the server")
	}
	same(t, parents(t, w.db), wanted()) // it opened on its file, whole, and serves
	if _, err := w.db.Exec(`INSERT INTO settings (k, v) VALUES ('kept', 'on the file')`); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	state, _, _, failed, why, found := registry(t, dsn, ns)
	if !found || state != "failed" || failed == 0 || !strings.Contains(why, "refuse") {
		t.Fatalf("the registry has state %q failed %d (found %v), because %q", state, failed, found, why)
	}
	if schemaExists(t, dsn, ns) {
		t.Fatal("the failed move left its schema behind")
	}
	if place, err := locus.Where(ns, "widget", dir); err != nil || place != locus.OnFile {
		t.Fatalf("a store whose move failed is at %v, %v", place, err)
	}

	// Within the backoff it is not tried again, even by an open that would succeed.
	w, err = locus.Use(context.Background(), nil, ns, "widget", dir, openWidget(""))
	if err != nil {
		t.Fatal(err)
	}
	if dialect.Of(w.db).Postgres() {
		t.Fatal("a move was retried inside its backoff")
	}
	_ = w.Close()

	// After it, with the obstruction gone, the move completes: the whole store, the
	// write made on the file between the attempts included.
	postgres.BackOff(0)
	w, err = locus.Use(context.Background(), nil, ns, "widget", dir, openWidget(""))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	if !dialect.Of(w.db).Postgres() {
		t.Fatal("the retry did not move the store")
	}
	same(t, parents(t, w.db), wanted())
	var v string
	if err := w.db.QueryRow(`SELECT v FROM settings WHERE k = 'kept'`).Scan(&v); err != nil || v != "on the file" {
		t.Fatalf("the write made on the file between the attempts is %q, %v", v, err)
	}
	if state, _, _, failed, why, _ := registry(t, dsn, ns); state != "moved" || failed != 0 || why != "" {
		t.Fatalf("after the retry the registry has %q, failed %d, %q", state, failed, why)
	}
}

// A copy that arrives different is not marked moved, however it came to differ.
func TestAMoveThatArrivesDifferentIsRefused(t *testing.T) {
	for name, fault := range map[string]string{
		"a value changed on the way in": `CREATE FUNCTION bump() RETURNS trigger AS $$ BEGIN NEW.name := NEW.name || 'x'; RETURN NEW; END $$ LANGUAGE plpgsql;
			CREATE TRIGGER bump BEFORE INSERT ON parent FOR EACH ROW EXECUTE FUNCTION bump()`,
		"a row dropped on the way in": `CREATE FUNCTION skip() RETURNS trigger AS $$ BEGIN IF NEW.qty = 7 THEN RETURN NULL; END IF; RETURN NEW; END $$ LANGUAGE plpgsql;
			CREATE TRIGGER skip BEFORE INSERT ON parent FOR EACH ROW EXECUTE FUNCTION skip()`,
	} {
		t.Run(name, func(t *testing.T) {
			ns := namespace.MustOrg("acme")
			dir := t.TempDir()
			onFile(t, ns, dir)
			dsn := sqltest.Server(t)
			t.Setenv(locus.MoveEnv, "lazy")

			w, err := locus.Use(context.Background(), nil, ns, "widget", dir, openWidget(fault))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = w.Close() }()
			if dialect.Of(w.db).Postgres() {
				t.Fatal("a store whose copy differed was marked moved")
			}
			same(t, parents(t, w.db), wanted())
			if state, _, _, _, _, _ := registry(t, dsn, ns); state != "failed" {
				t.Fatalf("the registry has %q", state)
			}
			if schemaExists(t, dsn, ns) {
				t.Fatal("the refused copy was left on the server")
			}
		})
	}
}

// Values a move cannot carry are named, and the store stays a file with them.
func TestAMoveRefusesWhatPostgreSQLCannotHold(t *testing.T) {
	for name, c := range map[string]struct {
		set  string
		want string
	}{
		"NUL in text":    {`UPDATE parent SET note = 'a' || char(0) || 'b' WHERE name = 'alpha'`, "NUL"},
		"bad UTF-8":      {`UPDATE parent SET note = CAST(x'ff' AS TEXT) WHERE name = 'alpha'`, "UTF-8"},
		"text in BIGINT": {`UPDATE parent SET qty = 'seven' WHERE name = 'alpha'`, "string where the column is integer"},
		"bad JSON":       {`UPDATE parent SET doc = '{not json' WHERE name = 'alpha'`, "JSON"},
	} {
		t.Run(name, func(t *testing.T) {
			ns := namespace.MustOrg("acme")
			dir := t.TempDir()
			onFile(t, ns, dir)
			t.Setenv(locus.Env, "")
			f, err := locus.Use(context.Background(), nil, ns, "widget", dir, openWidget(""))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Exec(c.set); err != nil {
				t.Fatalf("%s: %v", c.set, err)
			}
			_ = f.Close()
			dsn := sqltest.Server(t)
			t.Setenv(locus.MoveEnv, "lazy")

			w, err := locus.Use(context.Background(), nil, ns, "widget", dir, openWidget(""))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = w.Close() }()
			if dialect.Of(w.db).Postgres() {
				t.Fatal("a value PostgreSQL cannot hold was moved")
			}
			if _, _, _, _, why, _ := registry(t, dsn, ns); !strings.Contains(why, c.want) {
				t.Fatalf("the registry's reason is %q; want it to say %s", why, c.want)
			}
		})
	}
}

// A file with rows in a table the schema does not have is not moved: that would drop them.
func TestAMoveRefusesATableTheSchemaLacks(t *testing.T) {
	ns := namespace.MustOrg("acme")
	dir := t.TempDir()
	onFile(t, ns, dir)
	t.Setenv(locus.Env, "")
	f, err := locus.Use(context.Background(), nil, ns, "widget", dir, openWidget(""))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`CREATE TABLE old_events (id INTEGER PRIMARY KEY, what TEXT)`, `CREATE TABLE old_unused (id INTEGER PRIMARY KEY)`, `INSERT INTO old_events (what) VALUES ('x')`} {
		if _, err := f.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = f.Close()
	dsn := sqltest.Server(t)
	t.Setenv(locus.MoveEnv, "lazy")
	defer postgres.BackOff(0)()

	w, err := locus.Use(context.Background(), nil, ns, "widget", dir, openWidget(""))
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	if _, _, _, _, why, _ := registry(t, dsn, ns); !strings.Contains(why, "old_events") {
		t.Fatalf("the registry's reason is %q; want it to name the table", why)
	}

	// Emptied, the table goes without a word and the other empty one never mattered.
	t.Setenv(locus.Env, "")
	f, err = locus.Use(context.Background(), nil, ns, "widget", dir, openWidget(""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`DELETE FROM old_events`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	t.Setenv(locus.Env, dsn)
	w, err = locus.Use(context.Background(), nil, ns, "widget", dir, openWidget(""))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	if !dialect.Of(w.db).Postgres() {
		t.Fatal("a file whose extra tables were empty did not move")
	}
	same(t, parents(t, w.db), wanted())
}

// hanzoai/replicate's bookkeeping beside a streamed file (_replicate_seq's one row,
// _replicate_lock) is not the store's data: the move leaves it behind and goes on.
// commerce's own Base held it and both of its databases refused to move.
func TestAMoveLeavesReplicatesBookkeepingBehind(t *testing.T) {
	ns := namespace.MustOrg("acme")
	dir := t.TempDir()
	onFile(t, ns, dir)
	t.Setenv(locus.Env, "")
	f, err := locus.Use(context.Background(), nil, ns, "widget", dir, openWidget(""))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`CREATE TABLE _replicate_seq (id INTEGER PRIMARY KEY, seq INTEGER)`,
		`CREATE TABLE _replicate_lock (id INTEGER)`, `INSERT INTO _replicate_seq (id, seq) VALUES (1, 41)`} {
		if _, err := f.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = f.Close()
	sqltest.Server(t)
	t.Setenv(locus.MoveEnv, "lazy")
	defer postgres.BackOff(0)()
	w, err := locus.Use(context.Background(), nil, ns, "widget", dir, openWidget(""))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	if !dialect.Of(w.db).Postgres() {
		t.Fatal("a file with replicate's bookkeeping did not move")
	}
	same(t, parents(t, w.db), wanted())
}

// Two movers of one store cannot both move it; the second is told, and the first goes on.
func TestOnlyOneProcessMovesAStore(t *testing.T) {
	ns := namespace.MustOrg("acme")
	dir := t.TempDir()
	onFile(t, ns, dir)
	sqltest.Server(t)
	t.Setenv(locus.MoveEnv, "lazy")

	inside, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	slow := func(db *sql.DB) (*widget, error) {
		w, err := openWidget("")(db)
		if err == nil && dialect.Of(db).Postgres() {
			once.Do(func() { close(inside) })
			<-release // the first mover is on the server, holding the store
		}
		return w, err
	}
	first := make(chan error, 1)
	go func() {
		w, err := locus.Use(context.Background(), nil, ns, "widget", dir, slow)
		if err == nil {
			err = w.Close()
		}
		first <- err
	}()
	<-inside

	if place, err := locus.Where(ns, "widget", dir); !errors.Is(err, locus.ErrMoving) {
		t.Fatalf("a store being moved is at %v, %v; want ErrMoving", place, err)
	}
	if _, err := locus.Use(context.Background(), nil, ns, "widget", dir, openWidget("")); !errors.Is(err, locus.ErrMoving) {
		t.Fatalf("a second open during the move: %v; want ErrMoving", err)
	}
	if _, err := locus.Open(ns, "widget", dir); !errors.Is(err, locus.ErrMoving) {
		t.Fatalf("Org during the move: %v; want ErrMoving", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	w, err := locus.Use(context.Background(), nil, ns, "widget", dir, openWidget(""))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	same(t, parents(t, w.db), wanted())
}

// A move that is cancelled puts the store back as it found it, and it moves when asked again.
func TestACancelledMoveLeavesTheStoreItsFile(t *testing.T) {
	ns := namespace.MustOrg("acme")
	dir := t.TempDir()
	onFile(t, ns, dir)
	dsn := sqltest.Server(t)
	t.Setenv(locus.MoveEnv, "lazy")
	defer postgres.BackOff(0)()

	ctx, cancel := context.WithCancel(context.Background())
	stop := func(db *sql.DB) (*widget, error) {
		w, err := openWidget("")(db)
		if dialect.Of(db).Postgres() {
			cancel() // the process is told to stop while the store is being filled
		}
		return w, err
	}
	if _, err := locus.Use(ctx, nil, ns, "widget", dir, stop); err == nil {
		t.Fatal("a move that was cancelled reported success")
	}
	if schemaExists(t, dsn, ns) {
		t.Fatal("a cancelled move left its schema on the server")
	}
	if _, _, _, _, why, found := registry(t, dsn, ns); found {
		t.Fatalf("a move that was stopped, not defective, is on record as failed: %q", why)
	}
	if place, err := locus.Where(ns, "widget", dir); err != nil || place != locus.OnFile {
		t.Fatalf("a cancelled move left the store at %v, %v", place, err)
	}
	w, err := locus.Use(context.Background(), nil, ns, "widget", dir, openWidget(""))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	if !dialect.Of(w.db).Postgres() {
		t.Fatal("the store did not move when asked again")
	}
	same(t, parents(t, w.db), wanted())
}

// A row left at moving by a process that died is a move that failed without saying so.
func TestADeadMoversRowIsRetaken(t *testing.T) {
	ns := namespace.MustOrg("acme")
	dir := t.TempDir()
	onFile(t, ns, dir)
	dsn := sqltest.Server(t)
	t.Setenv(locus.MoveEnv, "lazy")

	schema, err := postgres.Schema(ns, "widget")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()
	// Provision the registry the way a process does, then leave what a crash leaves:
	// a schema holding half a copy and a row at moving, with no session holding the lock.
	if _, err := locus.Exists(ns, "widget"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE SCHEMA "` + schema + `"`,
		`CREATE TABLE "` + schema + `".parent (id BIGINT PRIMARY KEY, name TEXT NOT NULL, qty BIGINT NOT NULL)`,
		`INSERT INTO "` + schema + `".parent VALUES (99, 'half', 0)`,
		`INSERT INTO public.cloud_schemas (schema, namespace, subsystem, created, state, moving) VALUES ('` + schema + `', '` + ns.String() + `', 'widget', 1, 'moving', 1)`,
	} {
		if _, err := admin.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if place, err := locus.Where(ns, "widget", dir); err != nil || place != locus.OnFile {
		t.Fatalf("a store whose mover is dead is at %v, %v; want its file", place, err)
	}
	w, err := locus.Use(context.Background(), nil, ns, "widget", dir, openWidget(""))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	if !dialect.Of(w.db).Postgres() {
		t.Fatal("the dead mover's store was not moved")
	}
	same(t, parents(t, w.db), wanted()) // and the half a copy it left is not in it
}

// Where a store lives, from the facts that decide it.
func TestWhereDecidesFromTheFileAndTheRegistry(t *testing.T) {
	ns := namespace.MustOrg("acme")
	dir := t.TempDir()
	if place, err := locus.Where(ns, "widget", dir); err != nil || place != locus.OnFile {
		t.Fatalf("without a DSN: %v, %v; want the file", place, err)
	}
	sqltest.Server(t)
	if place, err := locus.Where(ns, "widget", dir); err != nil || place != locus.OnServer {
		t.Fatalf("a store with no file is %v, %v; want it born on the server", place, err)
	}
	// A store of an unported subsystem that the registry has on the server is not
	// opened as a file.
	w, err := locus.Open(ns, "widget", dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	if _, err := locus.File(ns, "widget", dir); err == nil || !strings.Contains(err.Error(), "split") {
		t.Fatalf("File opened a store the registry has on the server: %v", err)
	}
	if db, err := locus.File(namespace.MustOrg("globex"), "widget", dir); err != nil {
		t.Fatalf("File refused a store the registry has never heard of: %v", err)
	} else {
		_ = db.Close()
	}
}

func TestTheMovePolicyIsLazySweepOrNothing(t *testing.T) {
	for in, want := range map[string]locus.Mode{"": locus.Never, "lazy": locus.Lazy, " sweep ": locus.Sweep} {
		t.Setenv(locus.MoveEnv, in)
		if got, err := locus.MoveMode(); err != nil || got != want {
			t.Errorf("%q is %q, %v; want %q", in, got, err, want)
		}
	}
	t.Setenv(locus.MoveEnv, "eager")
	if _, err := locus.MoveMode(); err == nil {
		t.Error("a word that is no policy was read as one")
	}
}

var _ io.Closer = (*widget)(nil)
