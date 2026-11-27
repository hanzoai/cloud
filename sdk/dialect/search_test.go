package dialect_test

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
)

// notes is a table with rows in it before it has an index, which is how a file that
// predates one arrives; Create then has to build the index from them.
func notes(t *testing.T, open sqltest.Open) (*sql.DB, dialect.Words) {
	t.Helper()
	db := store(t, open)
	d := dialect.Of(db)
	if _, err := db.Exec(`CREATE TABLE note (
  id    ` + d.Identity() + `,
  title TEXT NOT NULL,
  body  TEXT NOT NULL,
  hold  BIGINT NOT NULL DEFAULT 0
)`); err != nil {
		t.Fatal(err)
	}
	for _, n := range [][2]string{
		{"acme/svc/api", "owner acme/team/core registry"},
		{"acme/svc/worker", "owner acme/team/platform registry"},
		{"go1.25", "the runtime of the api"},
		{"Kubernetes", "a long passage that mentions kubernetes once among a great many other words on unrelated matters"},
		{"kubernetes", "kubernetes kubernetes"},
		{"core api", "adjacent words"},
		{"api core", "words in the other order"},
	} {
		if _, err := db.Exec(`INSERT INTO note (title, body) VALUES ($1, $2)`, n[0], n[1]); err != nil {
			t.Fatal(err)
		}
	}
	w, err := d.Words("note", "id", "title", "body")
	if err != nil {
		t.Fatal(err)
	}
	create(t, db, w)
	return db, w
}

func create(t *testing.T, db *sql.DB, w dialect.Words) {
	t.Helper()
	ctx := context.Background()
	if err := dialect.Of(db).Migrate(ctx, db, func(tx *sql.Tx) error { return w.Create(ctx, tx) }); err != nil {
		t.Fatal(err)
	}
}

// found is the titles of the rows the terms find, best first.
func found(t *testing.T, db *sql.DB, w dialect.Words, all bool, terms ...string) []string {
	t.Helper()
	hits, args := w.Hits(1, terms, all)
	rows, err := db.Query(`SELECT title FROM note JOIN (`+hits+`) ON hit = id ORDER BY score, id`, args...)
	if err != nil {
		t.Fatalf("%v: %v", terms, err)
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

func sorted(xs []string) []string {
	out := slices.Clone(xs)
	slices.Sort(out)
	return out
}

// The same rows come back for the same query on both engines: a word of a key,
// the beginning of a word, every term or any, an adjacent phrase, case ignored.
func TestWordsFindRowsByTheirWords(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db, w := notes(t, open)
		for _, c := range []struct {
			name  string
			all   bool
			terms []string
			want  []string
		}{
			{"a word", true, []string{"platform"}, []string{"acme/svc/worker"}},
			{"a word of a key", true, []string{"svc"}, []string{"acme/svc/api", "acme/svc/worker"}},
			{"a whole key, as the phrase it is", true, []string{"acme/svc/api"}, []string{"acme/svc/api"}},
			{"the beginning of a word", true, []string{"regist"}, []string{"acme/svc/api", "acme/svc/worker"}},
			{"not the middle of one", true, []string{"egistry"}, nil},
			{"case is ignored", true, []string{"KUBERNETES"}, []string{"Kubernetes", "kubernetes"}},
			{"every term narrows", true, []string{"registry", "core"}, []string{"acme/svc/api"}},
			{"any term widens", false, []string{"platform", "core"}, []string{"acme/svc/api", "acme/svc/worker", "api core", "core api"}},
			{"a term is a phrase: in order", true, []string{"core api"}, []string{"core api"}},
			{"a term is a phrase: not the other order", true, []string{"api core"}, []string{"api core"}},
			{"a version is its parts", true, []string{"go1.25"}, []string{"go1.25"}},
			{"a part of a version", true, []string{"25"}, []string{"go1.25"}},
			{"nothing matches", true, []string{"zebra"}, nil},
			{"a term with no word matches nothing, under all", true, []string{"registry", `"`}, nil},
			{"and is left out under any", false, []string{"platform", `"`}, []string{"acme/svc/worker"}},
		} {
			if got := found(t, db, w, c.all, c.terms...); !slices.Equal(sorted(got), c.want) {
				t.Errorf("%s: %v found %q, want %q", c.name, c.terms, got, c.want)
			}
		}
	})
}

// A caller's text is text. Every character an engine reads as syntax is searched
// for, and none of it is an error.
func TestWordsTreatPunctuationAsText(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db, w := notes(t, open)
		for _, q := range []string{
			`"`, `a"b`, `'`, `a'b`, `\`, `a\b`, `*`, `^`, `-core`, `foo:bar`, `AND`, `OR`, `NOT registry`,
			`((`, `))`, `a & b`, `a | b`, `!a`, `a <-> b`, `a:*`, `'a':*`, `;DROP TABLE note`, "a\x00b", "a\xffb", "é", "",
		} {
			for _, all := range []bool{true, false} {
				found(t, db, w, all, q)
				found(t, db, w, all, q, "registry")
			}
		}
		// Nothing was dropped.
		if got := found(t, db, w, true, "acme"); len(got) != 2 {
			t.Fatalf("acme found %q after the hostile queries", got)
		}
	})
}

// The best match comes first: the row about the word, not the row that mentions it
// once in a long passage.
func TestWordsAnswerTheBestMatchFirst(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db, w := notes(t, open)
		got := found(t, db, w, true, "kubernetes")
		if len(got) != 2 || got[0] != "kubernetes" || got[1] != "Kubernetes" {
			t.Fatalf("kubernetes found %q, want the row about it first", got)
		}
	})
}

// The index follows the table: a row inserted, changed or deleted after it exists
// is found, found by its new words, or gone.
func TestWordsFollowTheTable(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db, w := notes(t, open)
		if _, err := db.Exec(`INSERT INTO note (title, body) VALUES ($1, $2)`, "acme/svc/new", "owner zebra"); err != nil {
			t.Fatal(err)
		}
		if got := found(t, db, w, true, "zebra"); !slices.Equal(got, []string{"acme/svc/new"}) {
			t.Fatalf("an inserted row was not indexed: %q", got)
		}
		if _, err := db.Exec(`UPDATE note SET body = $1 WHERE title = $2`, "owner giraffe", "acme/svc/new"); err != nil {
			t.Fatal(err)
		}
		if got := found(t, db, w, true, "zebra"); len(got) != 0 {
			t.Fatalf("an updated row is still found by its old words: %q", got)
		}
		if got := found(t, db, w, true, "giraffe"); !slices.Equal(got, []string{"acme/svc/new"}) {
			t.Fatalf("an updated row is not found by its new words: %q", got)
		}
		// A column that is not indexed changes nothing.
		if _, err := db.Exec(`UPDATE note SET hold = 1 WHERE title = $1`, "acme/svc/new"); err != nil {
			t.Fatal(err)
		}
		if got := found(t, db, w, true, "giraffe"); !slices.Equal(got, []string{"acme/svc/new"}) {
			t.Fatalf("an update of another column lost the row: %q", got)
		}
		if _, err := db.Exec(`DELETE FROM note WHERE title = $1`, "acme/svc/new"); err != nil {
			t.Fatal(err)
		}
		if got := found(t, db, w, true, "giraffe"); len(got) != 0 {
			t.Fatalf("a deleted row is still found: %q", got)
		}
	})
}

// Creating the index again changes nothing, and does not build it a second time.
func TestWordsCreateIsIdempotent(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db, w := notes(t, open)
		for range 3 {
			create(t, db, w)
		}
		if got := found(t, db, w, true, "registry"); len(got) != 2 {
			t.Fatalf("recreating the index changed what it holds: %q", got)
		}
	})
}

func TestWordsNamesAreChecked(t *testing.T) {
	var d dialect.Dialect
	for _, c := range []struct {
		table, key string
		cols       []string
	}{
		{"note; DROP TABLE x", "id", []string{"title"}},
		{"note", "id", nil},
		{"note", "id", []string{"title", "fts"}},
		{"note", "id", []string{`ti"tle`}},
		{"Note", "id", []string{"title"}},
	} {
		if _, err := d.Words(c.table, c.key, c.cols...); err == nil {
			t.Errorf("Words(%q, %q, %q) was accepted", c.table, c.key, c.cols)
		}
	}
	if _, err := d.Grams("snippet; x"); err == nil {
		t.Error("Grams accepted a name that is not an identifier")
	}
}

func gramsOf(t *testing.T, open sqltest.Open) (*sql.DB, dialect.Grams) {
	t.Helper()
	db := store(t, open)
	g, err := dialect.Of(db).Grams("snippet")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := dialect.Of(db).Migrate(ctx, db, func(tx *sql.Tx) error { return g.Create(ctx, tx) }); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for id, body := range map[int64]string{
		1: "getusername get user name",
		2: "load history -> handler",
		3: "x := y parseerror parse error",
		4: "get user",
		5: "100% sure_thing",
		6: "surexthing",
		7: "100 sure",
	} {
		if err := g.Put(ctx, tx, id, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return db, g
}

func ids(t *testing.T, db *sql.DB, g dialect.Grams, all bool, terms ...string) []int64 {
	t.Helper()
	hits, args := g.Hits(1, terms, all)
	rows, err := db.Query(`SELECT hit FROM (`+hits+`) h ORDER BY score, hit`, args...)
	if err != nil {
		t.Fatalf("%v: %v", terms, err)
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// A substring is found wherever it falls, ignoring case, however it is spelled.
func TestGramsFindASubstring(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db, g := gramsOf(t, open)
		for _, c := range []struct {
			name  string
			all   bool
			terms []string
			want  []int64
		}{
			{"a whole word", true, []string{"user"}, []int64{1, 4}},
			{"the middle of a word", true, []string{"seerr"}, []int64{3}},
			{"case is ignored", true, []string{"GETUSER"}, []int64{1}},
			{"case is ignored across a space", true, []string{"GET USER"}, []int64{1, 4}},
			{"a space is part of it", true, []string{"er na"}, []int64{1}},
			{"punctuation is part of it", true, []string{"ory ->"}, []int64{2}},
			{"an assignment", true, []string{":= "}, []int64{3}},
			{"a percent is a percent", true, []string{"0% s"}, []int64{5}},
			{"an underscore is an underscore", true, []string{"e_t"}, []int64{5}},
			{"a backslash is a backslash", true, []string{`\\\`}, nil},
			{"every term", true, []string{"get", "history"}, nil},
			{"every term, found", true, []string{"load", "history"}, []int64{2}},
			{"any term", false, []string{"history", "parseerror"}, []int64{2, 3}},
			{"shorter than a trigram finds nothing", true, []string{"us"}, nil},
			{"shorter, under all, spoils the query", true, []string{"get", "us"}, nil},
			{"shorter, under any, is left out", false, []string{"history", "us"}, []int64{2}},
		} {
			got := ids(t, db, g, c.all, c.terms...)
			slices.Sort(got) // which of equal matches comes first is each engine's own
			if !slices.Equal(got, c.want) && (len(got) != 0 || len(c.want) != 0) {
				t.Errorf("%s: %q found %v, want %v", c.name, c.terms, got, c.want)
			}
		}
	})
}

// A text with more of the terms comes first.
func TestGramsAnswerTheBestMatchFirst(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db, g := gramsOf(t, open)
		got := ids(t, db, g, false, "get", "user", "name")
		if len(got) != 2 || got[0] != 1 || got[1] != 4 {
			t.Fatalf("found %v, want row 1 (all three terms) before row 4 (two)", got)
		}
	})
}

func TestGramsForgetADeletedText(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db, g := gramsOf(t, open)
		ctx := context.Background()
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := g.Delete(ctx, tx, 2); err != nil {
			t.Fatal(err)
		}
		if err := g.Delete(ctx, tx, 99); err != nil {
			t.Fatalf("an id that is not indexed is an error: %v", err)
		}
		if err := g.Put(ctx, tx, 2, "reindexed handler"); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if got := ids(t, db, g, true, "history"); len(got) != 0 {
			t.Fatalf("a replaced text is still found by its old words: %v", got)
		}
		if got := ids(t, db, g, true, "reindexed"); !slices.Equal(got, []int64{2}) {
			t.Fatalf("a replaced text is not found by its new words: %v", got)
		}
	})
}

// The terms are text: a hostile one is searched for, never run.
func TestGramsTreatTermsAsText(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db, g := gramsOf(t, open)
		for _, q := range []string{`"`, `a"b"c`, `'`, `a'b'c`, `\`, `%%%`, `___`, `" OR "`, strings.Repeat("x", 5000), "a\x00bcd", "a\xffbcd", "ééé"} {
			for _, all := range []bool{true, false} {
				ids(t, db, g, all, q)
			}
		}
		if got := ids(t, db, g, true, "user"); len(got) != 2 {
			t.Fatalf("user found %v after the hostile queries", got)
		}
	})
}

func TestGramsCreateIsIdempotent(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db, g := gramsOf(t, open)
		ctx := context.Background()
		for range 3 {
			if err := dialect.Of(db).Migrate(ctx, db, func(tx *sql.Tx) error { return g.Create(ctx, tx) }); err != nil {
				t.Fatal(err)
			}
		}
		if got := ids(t, db, g, true, "user"); len(got) != 2 {
			t.Fatalf("recreating changed what the index holds: %v", got)
		}
	})
}

func TestHasTable(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db := store(t, open)
		d := dialect.Of(db)
		ctx := context.Background()
		if ok, err := d.HasTable(ctx, db, "widget"); err != nil || ok {
			t.Fatalf("HasTable before = %v, %v", ok, err)
		}
		if _, err := db.Exec(`CREATE TABLE widget (id BIGINT PRIMARY KEY)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE INDEX widget_id ON widget (id)`); err != nil {
			t.Fatal(err)
		}
		if ok, err := d.HasTable(ctx, db, "widget"); err != nil || !ok {
			t.Fatalf("HasTable after = %v, %v", ok, err)
		}
		if ok, err := d.HasTable(ctx, db, "widget_id"); err != nil || ok {
			t.Fatalf("an index is not a table: %v, %v", ok, err)
		}
	})
}

// A column declared Bytewise orders and ranges by its bytes on both engines.
func TestBytewiseOrdersByBytes(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db := store(t, open)
		d := dialect.Of(db)
		if _, err := db.Exec(`CREATE TABLE k (key TEXT PRIMARY KEY ` + d.Bytewise() + `, v BIGINT NOT NULL)`); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"acme/b", "acme-b", "Acme/b", "acme0", "acme/a", "acmé"} {
			if _, err := db.Exec(`INSERT INTO k (key, v) VALUES ($1, 1)`, k); err != nil {
				t.Fatal(err)
			}
		}
		rows, err := db.Query(`SELECT key FROM k WHERE key >= $1 AND key < $2 ORDER BY key`, "acme/", "acme0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var got []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			got = append(got, s)
		}
		if want := []string{"acme/a", "acme/b"}; !slices.Equal(got, want) {
			t.Fatalf("a prefix range read %q, want %q", got, want)
		}
	})
}

func TestClusteredIsATableTail(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db := store(t, open)
		d := dialect.Of(db)
		if _, err := db.Exec(`CREATE TABLE run (a TEXT NOT NULL, b TEXT NOT NULL, PRIMARY KEY (a, b)) ` + d.Clustered()); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO run (a, b) VALUES ($1, $2)`, "x", "y"); err != nil {
			t.Fatal(err)
		}
	})
}

// An erasure deletes under Shred and flushes after the commit. Whatever the engine
// does about its file, the rows and their terms are gone from every read.
func TestShredAndFlushLeaveNothingFound(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db, w := notes(t, open)
		d := dialect.Of(db)
		ctx := context.Background()
		tx, err := d.BeginWrite(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Shred(ctx, tx, w); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`DELETE FROM note WHERE title = $1`, "kubernetes"); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := d.Flush(ctx, db); err != nil {
			t.Fatal(err)
		}
		if got := found(t, db, w, true, "kubernetes"); !slices.Equal(got, []string{"Kubernetes"}) {
			t.Fatalf("kubernetes found %q after one of its two rows was erased", got)
		}
	})
}

// The clause that pins an index is part of a statement both engines run.
func TestIndexClauseRunsOnBothEngines(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db := store(t, open)
		d := dialect.Of(db)
		for _, stmt := range []string{
			`CREATE TABLE edge (a TEXT NOT NULL, b TEXT NOT NULL)`,
			`CREATE INDEX edge_a ON edge (a)`,
			`INSERT INTO edge (a, b) VALUES ('x', 'y')`,
		} {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatal(err)
			}
		}
		var b string
		if err := db.QueryRow(`SELECT b FROM edge`+d.Index("edge_a")+` WHERE a >= $1 AND a < $2`, "x", "y").Scan(&b); err != nil || b != "y" {
			t.Fatalf("b = %q, %v", b, err)
		}
	})
}

// The join in the order it is written runs on both engines, with an ON.
func TestJoinRunsOnBothEngines(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db := store(t, open)
		d := dialect.Of(db)
		for _, stmt := range []string{
			`CREATE TABLE hit (k TEXT NOT NULL)`,
			`CREATE TABLE doc (k TEXT NOT NULL, body TEXT NOT NULL)`,
			`INSERT INTO hit (k) VALUES ('a')`,
			`INSERT INTO doc (k, body) VALUES ('a', 'x'), ('b', 'y')`,
		} {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatal(err)
			}
		}
		var body string
		if err := db.QueryRow(`SELECT d.body FROM hit h ` + d.Join() + ` doc d ON d.k = h.k WHERE TRUE`).Scan(&body); err != nil || body != "x" {
			t.Fatalf("body = %q, %v", body, err)
		}
	})
}

func TestAnalyzeRunsInTheTransactionThatWrote(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db := store(t, open)
		d := dialect.Of(db)
		ctx := context.Background()
		if _, err := db.Exec(`CREATE TABLE bulk (a BIGINT PRIMARY KEY)`); err != nil {
			t.Fatal(err)
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO bulk (a) VALUES (1), (2), (3)`); err != nil {
			t.Fatal(err)
		}
		if err := d.Analyze(ctx, tx, "bulk"); err != nil {
			t.Fatal(err)
		}
		if err := d.Analyze(ctx, tx, "bulk; DROP TABLE bulk"); d.Postgres() && err == nil {
			t.Fatal("a table that is not an identifier was analyzed")
		}
		_ = tx.Rollback()
	})
}
