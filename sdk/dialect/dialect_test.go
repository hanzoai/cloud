package dialect_test

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
)

func store(t *testing.T, open sqltest.Open) *sql.DB {
	t.Helper()
	return open(namespace.MustOrg("acme"), "widget")
}

func TestOfReadsTheEngineOffTheHandle(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		if got, want := dialect.Of(store(t, open)).Postgres(), locus.DSN() != ""; got != want {
			t.Fatalf("Postgres() = %v, want %v", got, want)
		}
	})
}

// The rules a store's SQL is written to, run on the engine under test: numbered
// placeholders, a reused number, ON CONFLICT, ->> on a bare key and on a chain,
// RETURNING, and a time that is an integer set from Go.
func TestTheSharedDialectRunsOnBothEngines(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db := store(t, open)
		d := dialect.Of(db)
		ddl := `CREATE TABLE doc (
  id   ` + d.Identity() + `,
  k    TEXT NOT NULL UNIQUE,
  body ` + d.JSON() + ` NOT NULL,
  raw  ` + d.Blob() + `,
  at   BIGINT NOT NULL,
  n    BIGINT NOT NULL DEFAULT 0
)`
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
		now := time.Now().Unix()
		upsert := `INSERT INTO doc (k, body, raw, at) VALUES ($1, $2, $3, $4)
		           ON CONFLICT (k) DO UPDATE SET body = excluded.body, n = doc.n + 1
		           RETURNING id, n`
		var id1, n int64
		if err := db.QueryRow(upsert, "a", `{"x":"1","m":{"y":"deep"}}`, []byte{0, 1, 2}, now).Scan(&id1, &n); err != nil || n != 0 {
			t.Fatalf("insert: id %d n %d, %v", id1, n, err)
		}
		var id2 int64
		if err := db.QueryRow(upsert, "b", `{"x":"2"}`, nil, now).Scan(&id2, &n); err != nil {
			t.Fatal(err)
		}
		if id2 <= id1 {
			t.Fatalf("identity not ascending: %d then %d", id1, id2)
		}
		var again int64
		if err := db.QueryRow(upsert, "a", `{"x":"9","m":{"y":"deeper"}}`, nil, now).Scan(&again, &n); err != nil || again != id1 || n != 1 {
			t.Fatalf("upsert of a: id %d (want %d) n %d (want 1), %v", again, id1, n, err)
		}

		var x, y string
		if err := db.QueryRow(`SELECT body ->> 'x', body -> 'm' ->> 'y' FROM doc WHERE k = $1`, "a").Scan(&x, &y); err != nil || x != "9" || y != "deeper" {
			t.Fatalf("->> gave %q %q, %v", x, y, err)
		}
		var raw []byte
		var at int64
		if err := db.QueryRow(`SELECT raw, at FROM doc WHERE k = $1`, "a").Scan(&raw, &at); err != nil || at != now {
			t.Fatalf("time/blob: %v %d, %v", raw, at, err)
		}
		// The bytes stored first survive an upsert that did not set them.
		if !slices.Equal(raw, []byte{0, 1, 2}) {
			t.Fatalf("blob = %v", raw)
		}
		// One number used twice.
		var n2 int
		if err := db.QueryRow(`SELECT COUNT(*) FROM doc WHERE k = $1 OR body ->> 'x' = $1`, "2").Scan(&n2); err != nil || n2 != 1 {
			t.Fatalf("reused placeholder matched %d rows, %v", n2, err)
		}
		// An IN list built with Marks, after one scalar.
		rows, err := db.Query(`SELECT k FROM doc WHERE at = $1 AND k IN (`+dialect.Marks(2, 2)+`) ORDER BY id`, now, "a", "b")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var ks []string
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				t.Fatal(err)
			}
			ks = append(ks, k)
		}
		if !slices.Equal(ks, []string{"a", "b"}) {
			t.Fatalf("IN list gave %v", ks)
		}
	})
}

func TestArgsNumberMarksInTheOrderTheyAreWritten(t *testing.T) {
	var a dialect.Args
	text := "a = " + a.Mark("x") + " AND b IN (" + a.Mark(7) + "," + a.Mark(8) + ") LIMIT " + a.Mark(3)
	if text != "a = $1 AND b IN ($2,$3) LIMIT $4" {
		t.Fatalf("text = %q", text)
	}
	if got := a.Values(); len(got) != 4 || got[0] != "x" || got[3] != 3 {
		t.Fatalf("values = %v", got)
	}
}

// A table of records refuses the changes it was told to, with the message it was given,
// on either engine; a change it was not told to refuse goes through; and telling it twice
// is the same as once.
func TestRefuseKeepsRecordsOnBothEngines(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db := store(t, open)
		d := dialect.Of(db)
		ctx := context.Background()
		if err := d.Migrate(ctx, db, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `CREATE TABLE record (id TEXT PRIMARY KEY, status TEXT NOT NULL, body TEXT NOT NULL)`); err != nil {
				return err
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := d.Migrate(ctx, db, func(tx *sql.Tx) error {
				if err := d.Refuse(ctx, tx, "record_final", "record", "UPDATE",
					"OLD.status = 'final' OR NEW.body IS DISTINCT FROM OLD.body", "a record is final"); err != nil {
					return err
				}
				return d.Refuse(ctx, tx, "record_kept", "record", "DELETE", "", "a record is kept")
			}); err != nil {
				t.Fatal(err)
			}
		}
		for _, row := range [][]any{{"a", "open", "x"}, {"b", "final", "y"}} {
			if _, err := db.Exec(`INSERT INTO record (id, status, body) VALUES ($1, $2, $3)`, row...); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec(`UPDATE record SET status = 'closed' WHERE id = 'a'`); err != nil {
			t.Fatalf("a change the trigger does not name was refused: %v", err)
		}
		for what, stmt := range map[string]string{
			"a rewrite of the body":     `UPDATE record SET body = 'z' WHERE id = 'a'`,
			"any change to a final one": `UPDATE record SET status = 'open' WHERE id = 'b'`,
		} {
			if _, err := db.Exec(stmt); err == nil || !strings.Contains(err.Error(), "a record is final") {
				t.Errorf("%s: %v, want the message", what, err)
			}
		}
		if _, err := db.Exec(`DELETE FROM record WHERE id = 'a'`); err == nil || !strings.Contains(err.Error(), "a record is kept") {
			t.Errorf("a delete: %v, want the message", err)
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM record`).Scan(&n); err != nil || n != 2 {
			t.Errorf("%d records remain (%v), want 2", n, err)
		}
		if err := d.Migrate(ctx, db, func(tx *sql.Tx) error {
			return d.Refuse(ctx, tx, "record_final", "record", "INSERT", "", "no")
		}); err == nil {
			t.Error("an event that is not UPDATE or DELETE was accepted")
		}
	})
}

// Rows written before a table had a seq column are numbered by when they were written,
// once, and a row written after is numbered one past the last.
func TestSequenceNumbersRowsInTheOrderTheyWereWritten(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db := store(t, open)
		d := dialect.Of(db)
		ctx := context.Background()
		if _, err := db.Exec(`CREATE TABLE ledger (id TEXT PRIMARY KEY)`); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"c", "a", "b"} {
			if _, err := db.Exec(`INSERT INTO ledger (id) VALUES ($1)`, id); err != nil {
				t.Fatal(err)
			}
		}
		for range 2 {
			if err := d.Migrate(ctx, db, func(tx *sql.Tx) error { return d.Sequence(ctx, tx, "ledger") }); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec(`INSERT INTO ledger (id, seq) VALUES ($1, `+dialect.NextSeq("ledger")+`)`, "d"); err != nil {
			t.Fatal(err)
		}
		rows, err := db.Query(`SELECT id FROM ledger ORDER BY seq, id`)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var got []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			got = append(got, id)
		}
		// A server has no rowid, so the rows it already held are all at 0 and tie, in id
		// order; a file numbers them in the order they were written.
		want := []string{"c", "a", "b", "d"}
		if d.Postgres() {
			want = []string{"a", "b", "c", "d"}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("order = %v, want %v", got, want)
		}
		if err := d.Sequence(ctx, db2tx(t, db), "no table"); err == nil {
			t.Error("a table name that is not an identifier was accepted")
		}
	})
}

func db2tx(t *testing.T, db *sql.DB) *sql.Tx {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

func TestMarks(t *testing.T) {
	if got := dialect.Marks(2, 3); got != "$2,$3,$4" {
		t.Fatalf("Marks(2,3) = %q", got)
	}
	if got := dialect.Marks(1, 1); got != "$1" {
		t.Fatalf("Marks(1,1) = %q", got)
	}
}

func TestHasColumn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db := store(t, open)
		d := dialect.Of(db)
		ctx := context.Background()
		if _, err := db.Exec(`CREATE TABLE thing (id BIGINT PRIMARY KEY)`); err != nil {
			t.Fatal(err)
		}
		other := open(namespace.MustOrg("globex"), "widget")
		if _, err := other.Exec(`CREATE TABLE thing (id BIGINT PRIMARY KEY, extra TEXT)`); err != nil {
			t.Fatal(err)
		}
		has := func(table, col string) bool {
			ok, err := d.HasColumn(ctx, db, table, col)
			if err != nil {
				t.Fatal(err)
			}
			return ok
		}
		if !has("thing", "id") || has("thing", "extra") || has("nothing", "id") {
			t.Fatal("HasColumn disagrees with the table (and must read only its own store's schema)")
		}
		if _, err := db.Exec(`ALTER TABLE thing ADD COLUMN extra TEXT NOT NULL DEFAULT ''`); err != nil {
			t.Fatal(err)
		}
		if !has("thing", "extra") {
			t.Fatal("HasColumn missed a column that was added")
		}
	})
}

// On PostgreSQL a second writer waits for the first's transaction; on SQLite the
// pinned connection already did that, and BeginWrite asks for nothing more.
func TestLockSerialisesWritersOnAStore(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db := store(t, open)
		d := dialect.Of(db)
		ctx := context.Background()
		if _, err := db.Exec(`CREATE TABLE bal (n BIGINT NOT NULL)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO bal VALUES (0)`); err != nil {
			t.Fatal(err)
		}
		// 16 read-modify-writes that would lose updates at READ COMMITTED.
		done := make(chan error, 16)
		for range 16 {
			go func() {
				tx, err := d.BeginWrite(ctx, db)
				if err != nil {
					done <- err
					return
				}
				defer func() { _ = tx.Rollback() }()
				var n int64
				if err := tx.QueryRowContext(ctx, `SELECT n FROM bal`).Scan(&n); err != nil {
					done <- err
					return
				}
				time.Sleep(2 * time.Millisecond)
				if _, err := tx.ExecContext(ctx, `UPDATE bal SET n = $1`, n+1); err != nil {
					done <- err
					return
				}
				done <- tx.Commit()
			}()
		}
		for range 16 {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		var n int64
		if err := db.QueryRow(`SELECT n FROM bal`).Scan(&n); err != nil || n != 16 {
			t.Fatalf("16 serialised increments gave %d, %v", n, err)
		}
	})
}

// Two migrations of one store never run inside one another, one migration needs one
// connection however many are asked for at once, and a failed one leaves nothing.
func TestMigrateIsOneTransactionUnderTheStoresLock(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db := store(t, open)
		d := dialect.Of(db)
		ctx := context.Background()
		var inside, overlap atomic.Int32
		done := make(chan error, 6)
		for range 6 {
			go func() {
				done <- d.Migrate(ctx, db, func(tx *sql.Tx) error {
					if inside.Add(1) > 1 {
						overlap.Add(1)
					}
					defer inside.Add(-1)
					time.Sleep(10 * time.Millisecond)
					_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS thing (id BIGINT PRIMARY KEY)`)
					return err
				})
			}()
		}
		for range 6 {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		if locus.DSN() != "" && overlap.Load() != 0 {
			t.Fatalf("%d migrators ran inside one another's critical section", overlap.Load())
		}

		// A migration that fails is rolled back whole, on either engine.
		boom := errors.New("boom")
		err := d.Migrate(ctx, db, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `CREATE TABLE half (id BIGINT PRIMARY KEY)`); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("Migrate answered %v, want the migration's own error", err)
		}
		if _, err := db.Exec(`SELECT id FROM half`); err == nil {
			t.Fatal("a failed migration left its table behind")
		}

		// Holding it does not hold another store's.
		if locus.DSN() == "" {
			return
		}
		hold := make(chan struct{})
		started := make(chan struct{})
		held := make(chan error, 1)
		go func() {
			held <- d.Migrate(ctx, db, func(*sql.Tx) error {
				close(started)
				<-hold
				return nil
			})
		}()
		<-started
		other := open(namespace.MustOrg("globex"), "widget")
		freed := make(chan error, 1)
		go func() {
			freed <- dialect.Of(other).Migrate(ctx, other, func(*sql.Tx) error { return nil })
		}()
		select {
		case err := <-freed:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a migration in one store waited on another store's")
		}
		close(hold)
		if err := <-held; err != nil {
			t.Fatal(err)
		}
	})
}

// A copy of rows with explicit ids leaves the identity behind them; Resync moves it
// past the largest, and an empty table starts at 1.
func TestResyncMovesIdentityPastCopiedIds(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db := store(t, open)
		d := dialect.Of(db)
		ctx := context.Background()
		if _, err := db.Exec(`CREATE TABLE log (id ` + d.Identity() + `, v TEXT NOT NULL)`); err != nil {
			t.Fatal(err)
		}
		if err := d.Resync(ctx, db, "log", "id"); err != nil {
			t.Fatal(err)
		}
		next := func() int64 {
			var id int64
			if err := db.QueryRow(`INSERT INTO log (v) VALUES ('x') RETURNING id`).Scan(&id); err != nil {
				t.Fatal(err)
			}
			return id
		}
		if got := next(); got != 1 {
			t.Fatalf("first id of an empty table after Resync = %d, want 1", got)
		}
		for _, id := range []int64{100, 200} {
			if _, err := db.Exec(`INSERT INTO log (id, v) VALUES ($1, 'copied')`, id); err != nil {
				t.Fatal(err)
			}
		}
		if err := d.Resync(ctx, db, "log", "id"); err != nil {
			t.Fatal(err)
		}
		if got := next(); got != 201 {
			t.Fatalf("id after copying 100 and 200 and Resync = %d, want 201", got)
		}
		if err := d.Resync(ctx, db, "log; DROP TABLE log", "id"); err == nil {
			t.Fatal("Resync interpolated something that is not an identifier")
		}
		if err := d.Resync(ctx, db, "nothere", "id"); err == nil {
			t.Fatal("Resync of a table that is not there answered nil")
		}
		if err := d.Resync(ctx, db, "log", "nothere"); err == nil {
			t.Fatal("Resync of a column that is not there answered nil")
		}
		if _, err := db.Exec(`CREATE TABLE plain (id BIGINT PRIMARY KEY)`); err != nil {
			t.Fatal(err)
		}
		if err := d.Resync(ctx, db, "plain", "id"); d.Postgres() && err == nil {
			t.Fatal("Resync of a column with no sequence answered nil")
		}
	})
}

// PostgreSQL's text refuses what SQLite's stores: a NUL byte and bytes that are not
// UTF-8. A port rejects both at its boundary, so a value that fits on one engine is
// not a different error on the other.
func TestTextTheEnginesDisagreeAbout(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db := store(t, open)
		if _, err := db.Exec(`CREATE TABLE t (v TEXT)`); err != nil {
			t.Fatal(err)
		}
		pg := dialect.Of(db).Postgres()
		for name, v := range map[string]string{"NUL": "a\x00b", "invalid UTF-8": "a\xffb"} {
			_, err := db.Exec(`INSERT INTO t (v) VALUES ($1)`, v)
			if pg && err == nil {
				t.Errorf("PostgreSQL stored %s; the rule in LLM.md assumes it refuses", name)
			}
			if !pg && err != nil {
				t.Errorf("SQLite refused %s: %v", name, err)
			}
		}
	})
}

// LIKE is the one comparison whose case rule differs; lower() on both sides is the
// shared spelling.
func TestFoldedLikeAgrees(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db := store(t, open)
		if _, err := db.Exec(`CREATE TABLE t (v TEXT)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO t (v) VALUES ('Wallet:A'), ('wallet:b'), ('other')`); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM t WHERE lower(v) LIKE lower($1)`, "WALLET:%").Scan(&n); err != nil || n != 2 {
			t.Fatalf("folded LIKE matched %d, want 2 (%v)", n, err)
		}
	})
}

func TestUniqueNamesAConstraintOnBothEngines(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db := store(t, open)
		if _, err := db.Exec(`CREATE TABLE once (k TEXT PRIMARY KEY, v TEXT NOT NULL UNIQUE)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO once (k, v) VALUES ($1, $2)`, "a", "x"); err != nil {
			t.Fatal(err)
		}
		for name, args := range map[string][]any{"a primary key": {"a", "y"}, "a unique column": {"b", "x"}} {
			_, err := db.Exec(`INSERT INTO once (k, v) VALUES ($1, $2)`, args...)
			if !dialect.Unique(err) {
				t.Errorf("%s: Unique(%v) = false", name, err)
			}
		}
		_, err := db.Exec(`INSERT INTO once (k, v) VALUES ($1, NULL)`, "c")
		if err == nil || dialect.Unique(err) {
			t.Errorf("a NOT NULL violation is not a unique one: %v", err)
		}
		if dialect.Unique(nil) || dialect.Unique(errors.New("the network went away")) {
			t.Error("Unique is true of an error that is not a constraint")
		}
	})
}

func TestTextRefusesWhatPostgresRefuses(t *testing.T) {
	for name, v := range map[string]string{"a NUL": "a\x00b", "invalid UTF-8": "a\xffb", "a truncated rune": "\xe2\x82"} {
		if !errors.Is(dialect.Text("fine", v), dialect.ErrText) {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := dialect.Text("", "plain", "naïve 日本語 \u2028 \x01\t\n"); err != nil {
		t.Errorf("text both engines store was refused: %v", err)
	}
	if err := dialect.Text(); err != nil {
		t.Errorf("no text was refused: %v", err)
	}
	// The claim the check rests on: the engine under test stores what Text accepts,
	// and on PostgreSQL refuses what Text refuses.
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db := store(t, open)
		if _, err := db.Exec(`CREATE TABLE note (body TEXT NOT NULL)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO note (body) VALUES ($1)`, "naïve 日本語 \u2028 \x01\t\n"); err != nil {
			t.Fatalf("accepted text was refused by the engine: %v", err)
		}
		if dialect.Of(db).Postgres() {
			for _, v := range []string{"a\x00b", "a\xffb"} {
				if _, err := db.Exec(`INSERT INTO note (body) VALUES ($1)`, v); err == nil {
					t.Errorf("postgres stored %q, so Text refuses what it would take", v)
				}
			}
		}
	})
}

// Document is the refusal a JSON column shares between the engines: a NUL escaped
// inside a string, bytes that are not UTF-8 and text that is not JSON.
func TestDocumentRefusesWhatAJSONColumnRefuses(t *testing.T) {
	for name, v := range map[string]string{
		"an escaped NUL":       `{"line":"a\u0000b"}`,
		"an escaped NUL key":   `{"a\u0000":1}`,
		"invalid UTF-8":        "{\"line\":\"a\xffb\"}",
		"text that is no JSON": `{"line":`,
	} {
		if err := dialect.Document([]byte(v)); !errors.Is(err, dialect.ErrText) {
			t.Errorf("%s: got %v, want ErrText", name, err)
		}
	}
	for name, v := range map[string]string{
		"a plain document":            `{"line":"héllo 😀","n":[1,2.5,null,true]}`,
		"a backslash then the digits": `{"line":"\\u0000"}`,
	} {
		if err := dialect.Document([]byte(v)); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
}

// Serialize orders the transactions that name one key and, on PostgreSQL, leaves
// the ones that name another alone. On SQLite the store's one connection orders them
// all, which is stricter than the key asks.
func TestSerializeOrdersOneKeyAndOnlyThatKey(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db := store(t, open)
		d := dialect.Of(db)
		ctx := context.Background()

		held, err := d.Serialize(ctx, db, "payer-a")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = held.Rollback() }()

		short := func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(ctx, 300*time.Millisecond)
		}

		c, cancel := short()
		defer cancel()
		if tx, err := d.Serialize(c, db, "payer-a"); err == nil {
			_ = tx.Rollback()
			t.Fatal("a second transaction for the same key began while the first was open")
		}

		c, cancel = short()
		defer cancel()
		other, err := d.Serialize(c, db, "payer-b")
		if d.Postgres() {
			if err != nil {
				t.Fatalf("a transaction for another key waited: %v", err)
			}
			_ = other.Rollback()
		} else if err == nil {
			_ = other.Rollback()
			t.Fatal("the store's one connection let a second transaction begin")
		}

		if err := held.Rollback(); err != nil {
			t.Fatal(err)
		}
		c, cancel = short()
		defer cancel()
		again, err := d.Serialize(c, db, "payer-a")
		if err != nil {
			t.Fatalf("the key was not released at the end of the transaction: %v", err)
		}
		_ = again.Rollback()
	})
}

// A lock taken through Held belongs to the work and not to the request that asked
// for it: the caller's context ending does not end it, a wait is abandoned when its
// context ends or its bound runs out, and release gives it up and lands what was
// written under it.
func TestHeldOutlivesTheContextThatAskedForIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db := store(t, open)
		d := dialect.Of(db)
		begin := func(ctx context.Context) (*sql.Tx, error) { return d.BeginWrite(ctx, db) }
		if _, err := db.Exec(`CREATE TABLE note (body TEXT NOT NULL)`); err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		tx, release, err := dialect.Held(ctx, 0, begin)
		if err != nil {
			t.Fatal(err)
		}
		cancel() // the request that asked is gone
		time.Sleep(100 * time.Millisecond)
		if _, err := tx.Exec(`INSERT INTO note (body) VALUES ($1)`, "held"); err != nil {
			t.Fatalf("the lock ended with the context that asked for it: %v", err)
		}

		// A second asker waits, and gives up when its context ends...
		short, stop := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer stop()
		if _, again, err := dialect.Held(short, 0, begin); err == nil {
			_ = again()
			t.Fatal("a second transaction took the lock while it was held")
		}
		// ...or when its bound runs out, whatever its context says.
		start := time.Now()
		if _, again, err := dialect.Held(context.Background(), 200*time.Millisecond, begin); err == nil {
			_ = again()
			t.Fatal("a second transaction took the lock while it was held")
		} else if time.Since(start) > 5*time.Second {
			t.Fatalf("a bounded wait took %v", time.Since(start))
		}

		if err := release(); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM note`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("release did not land what was written under the lock: %d rows, %v", n, err)
		}
		_, free, err := dialect.Held(context.Background(), time.Second, begin)
		if err != nil {
			t.Fatalf("the lock was not given up by release: %v", err)
		}
		_ = free()
	})
}
