package vec_test

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/internal/vec"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/namespace"
)

// space_test.go holds the one rule every vector store here keeps: an index is
// created in one space and never holds, or answers a query with, a vector of
// another. Its stores (apps/knowledge, apps/code) prove they ask it; these prove
// what it answers.

// one is the space a test index is created in.
var one = vec.Space{Model: "zen-embedding", Dimensions: 3, Normalization: vec.NormNone}

// probe is the index over a table standing in for a store's vectors.
var probe = vec.Index{Name: "probe", Held: `SELECT 1 FROM probe LIMIT 1`}

// openProbe is a database holding the space record and the probe table.
func openProbe(t *testing.T, open sqltest.Open) *sql.DB {
	t.Helper()
	db := open(namespace.MustOrg("acme"), "vec")
	ddl := `CREATE TABLE IF NOT EXISTS probe (id BIGINT PRIMARY KEY, vec ` + dialect.Of(db).Blob() + `)`
	for _, s := range []string{vec.SpaceDDL, ddl} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}
	return db
}

// claim runs Claim in a transaction of its own, as a store's write does.
func claim(t *testing.T, db *sql.DB, s vec.Space) error {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := probe.Claim(context.Background(), tx, s); err != nil {
		return err
	}
	return tx.Commit()
}

// mixed asserts err is a refusal naming exactly the fields that differ.
func mixed(t *testing.T, err error, fields ...string) {
	t.Helper()
	var m *vec.MixedError
	if !errors.Is(err, vec.ErrMixed) || !errors.As(err, &m) {
		t.Fatalf("want a refusal of mixed vectors, got %v", err)
	}
	if got := m.Fields(); !slices.Equal(got, fields) {
		t.Fatalf("refusal names %v, want %v (%v)", got, fields, err)
	}
	for _, f := range fields {
		if !strings.Contains(err.Error(), f) {
			t.Fatalf("the refusal does not say which field differs (%s): %v", f, err)
		}
	}
	if !strings.Contains(err.Error(), "rebuild") {
		t.Fatalf("the refusal does not say what changes a space: %v", err)
	}
}

// TestAnIndexIsCreatedInTheSpaceOfItsFirstWrite: the first write records the
// space, every later write and query in it is admitted, and the record reads back
// exactly as written.
func TestAnIndexIsCreatedInTheSpaceOfItsFirstWrite(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		db := openProbe(t, open)
		if _, ok, err := probe.Space(ctx, db); err != nil || ok {
			t.Fatalf("a new index has no space yet: ok=%v err=%v", ok, err)
		}
		if err := probe.Admit(ctx, db, one); err != nil {
			t.Fatalf("an empty index answers any query (with nothing): %v", err)
		}
		if err := claim(t, db, one); err != nil {
			t.Fatal(err)
		}
		got, ok, err := probe.Space(ctx, db)
		if err != nil || !ok || got != one {
			t.Fatalf("recorded %+v ok=%v err=%v, want %+v", got, ok, err, one)
		}
		if err := claim(t, db, one); err != nil {
			t.Fatalf("a second write in the index's space: %v", err)
		}
		if err := probe.Admit(ctx, db, one); err != nil {
			t.Fatalf("a query in the index's space: %v", err)
		}
	})
}

// TestAMismatchedDimensionIsRefused: one model name answering at another
// dimension is another space. Its write and its query are both refused, and the
// refusal leaves the record as it was.
func TestAMismatchedDimensionIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		db := openProbe(t, open)
		if err := claim(t, db, one); err != nil {
			t.Fatal(err)
		}
		short := one
		short.Dimensions = 2
		mixed(t, claim(t, db, short), "dimensions")
		mixed(t, probe.Admit(ctx, db, short), "dimensions")
		if got, _, _ := probe.Space(ctx, db); got != one {
			t.Fatalf("a refused write changed the record to %+v", got)
		}
		// Before anything is embedded the dimension is the model's to answer, so
		// a space that has none yet is checked on every other field.
		unknown := one
		unknown.Dimensions = 0
		if err := probe.Admit(ctx, db, unknown); err != nil {
			t.Fatalf("a space with no dimension yet: %v", err)
		}
	})
}

// TestAMismatchedPrefixIsRefused: the same model and dimension under another
// task prefix is another space, for a document's prefix and a query's alike.
func TestAMismatchedPrefixIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		db := openProbe(t, open)
		if err := claim(t, db, one); err != nil {
			t.Fatal(err)
		}
		doc := one
		doc.Task.Document = "title: none | text: "
		mixed(t, claim(t, db, doc), "task.document")
		query := one
		query.Task.Query = "task: classification | query: "
		mixed(t, probe.Admit(ctx, db, query), "task.query")
		if got, _, _ := probe.Space(ctx, db); got != one {
			t.Fatalf("a refused write changed the record to %+v", got)
		}
	})
}

// TestEveryFieldIsPartOfTheSpace: model revision and normalization refuse as
// dimension and prefix do, and a refusal names every field that differs.
func TestEveryFieldIsPartOfTheSpace(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		db := openProbe(t, open)
		if err := claim(t, db, one); err != nil {
			t.Fatal(err)
		}
		rev := one
		rev.Model = "zen-embedding@2"
		mixed(t, probe.Admit(ctx, db, rev), "model_revision")
		l2 := one
		l2.Normalization = vec.NormL2
		mixed(t, claim(t, db, l2), "normalization")
		all := vec.Space{Model: "other", Dimensions: 9, Normalization: vec.NormL2, Task: vec.Task{Query: "q: ", Document: "d: "}}
		mixed(t, probe.Admit(ctx, db, all), "model_revision", "dimensions", "normalization", "task.query", "task.document")
	})
}

// TestARebuildIsTheOnlyWayToAnotherSpace: Forget, in the transaction that
// removes the index's vectors, is what lets the next write create it in another
// space.
func TestARebuildIsTheOnlyWayToAnotherSpace(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		db := openProbe(t, open)
		if err := claim(t, db, one); err != nil {
			t.Fatal(err)
		}
		wide := one
		wide.Dimensions = 768
		mixed(t, claim(t, db, wide), "dimensions")
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := probe.Forget(ctx, tx); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := claim(t, db, wide); err != nil {
			t.Fatalf("after a rebuild the index is created anew: %v", err)
		}
		if got, _, _ := probe.Space(ctx, db); got != wide {
			t.Fatalf("rebuilt in %+v, want %+v", got, wide)
		}
	})
}

// TestAnIndexFromBeforeSpacesIsAdopted: vectors written before an index
// recorded its space carry no record. They were all embedded as the model
// answered and with no prefix, so a space that shares those adopts the index
// with its first write, and a space that does not is refused rather than
// scored beside them.
func TestAnIndexFromBeforeSpacesIsAdopted(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		db := openProbe(t, open)
		if _, err := db.Exec(`INSERT INTO probe (id, vec) VALUES (1, $1)`, vec.Encode([]float32{1, 0, 0})); err != nil {
			t.Fatal(err)
		}
		prefixed := one
		prefixed.Task.Document = "passage: "
		mixed(t, claim(t, db, prefixed), "task.document")
		l2 := one
		l2.Normalization = vec.NormL2
		mixed(t, probe.Admit(ctx, db, l2), "normalization")
		if err := probe.Admit(ctx, db, one); err != nil {
			t.Fatalf("a query in the space the old vectors share: %v", err)
		}
		if _, ok, _ := probe.Space(ctx, db); ok {
			t.Fatal("a query recorded a space: only a write creates an index")
		}
		if err := claim(t, db, one); err != nil {
			t.Fatalf("the first write adopts the index: %v", err)
		}
		if got, ok, _ := probe.Space(ctx, db); !ok || got != one {
			t.Fatalf("adopted as %+v ok=%v", got, ok)
		}
	})
}

// TestOfRefusesABatchNoSpaceHolds: vectors of two dimensions, or an empty one,
// are in no one space.
func TestOfRefusesABatchNoSpaceHolds(t *testing.T) {
	p := vec.Space{Model: "m", Normalization: vec.NormNone}
	s, err := p.Of([]float32{1, 2}, []float32{3, 4})
	if err != nil || s.Dimensions != 2 || s.Model != "m" {
		t.Fatalf("one dimension: %+v %v", s, err)
	}
	if _, err := p.Of([]float32{1, 2}, []float32{3, 4, 5}); err == nil {
		t.Fatal("a batch of two dimensions must be refused")
	}
	if _, err := p.Of([]float32{}); err == nil {
		t.Fatal("an empty vector must be refused")
	}
}

// TestEmbedCarriesTheSpace: a document is embedded after its document prefix
// and a query after its query prefix, every vector is shaped as the space
// says, and the answer names the space it is in.
func TestEmbedCarriesTheSpace(t *testing.T) {
	var sent []string
	embed := func(in []string) ([][]float32, error) {
		sent = append(sent, in...)
		out := make([][]float32, len(in))
		for i := range in {
			out[i] = []float32{3, 4}
		}
		return out, nil
	}
	p := vec.Space{Model: "m", Normalization: vec.NormL2, Task: vec.Task{Query: "q: ", Document: "d: "}}
	docs, s, err := p.Documents([]string{"one", "two"}, embed)
	if err != nil || len(docs) != 2 || s.Dimensions != 2 {
		t.Fatalf("documents: %v %+v %v", docs, s, err)
	}
	q, qs, err := p.Query("three", embed)
	if err != nil || qs != s {
		t.Fatalf("query: %v %+v %v", q, qs, err)
	}
	if want := []string{"d: one", "d: two", "q: three"}; !slices.Equal(sent, want) {
		t.Fatalf("sent %q, want %q", sent, want)
	}
	for _, v := range append(docs, q) {
		if n := math.Hypot(float64(v[0]), float64(v[1])); math.Abs(n-1) > 1e-6 {
			t.Fatalf("an l2 space keeps unit vectors, got length %v (%v)", n, v)
		}
	}
	none := vec.Space{Model: "m", Normalization: vec.NormNone}
	raw, _, _ := none.Documents([]string{"x"}, embed)
	if raw[0][0] != 3 || raw[0][1] != 4 {
		t.Fatalf("a space with no normalization keeps the model's answer, got %v", raw[0])
	}
	if v, s, err := none.Documents([]string{"x"}, func([]string) ([][]float32, error) { return nil, nil }); err != nil || v != nil || s.Dimensions != 0 {
		t.Fatalf("an embedder with nothing to answer (disabled) is no vectors, not an error: %v %+v %v", v, s, err)
	}
	if _, _, err := none.Documents([]string{"x", "y"}, func([]string) ([][]float32, error) { return [][]float32{{1}}, nil }); err == nil {
		t.Fatal("one vector for two texts must be refused")
	}
}

// TestPolicyNamesTheRevision: the deployment's revision is part of the model
// it embeds with.
func TestPolicyNamesTheRevision(t *testing.T) {
	t.Setenv("CLOUD_EMBED_REVISION", "")
	if p := vec.Policy("zen-embedding"); p.Model != "zen-embedding" || p.Normalization != vec.NormNone || p.Task != (vec.Task{}) || p.Dimensions != 0 {
		t.Fatalf("policy %+v", p)
	}
	t.Setenv("CLOUD_EMBED_REVISION", " 2026-11 ")
	if p := vec.Policy("zen-embedding"); p.Model != "zen-embedding@2026-11" {
		t.Fatalf("policy with a revision %+v", p)
	}
}
