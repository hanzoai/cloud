package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/internal/vec"
	"github.com/zap-proto/zip"
)

// space_test.go proves the org's vector index keeps ONE space (internal/vec): a
// passage of another dimension, or a deployment embedding under another task
// prefix, is refused at the write and at the query; the surfaces answer by
// their words and say degraded rather than score across spaces; and
// POST /v1/knowledge/reindex is the way to another space. A store written before
// spaces were recorded keeps answering and is adopted by its first write.

// reindexAs runs POST /v1/knowledge/reindex as an admin of org.
func reindexAs(t *testing.T, app *zip.App, org string) reindexOut {
	t.Helper()
	hr := httptest.NewRequest(http.MethodPost, "/v1/knowledge/reindex", nil)
	hr.Header.Set("X-Org-Id", org)
	hr.Header.Set("X-User-Id", "u_"+org)
	hr.Header.Set("X-User-IsOrgAdmin", "true")
	resp, err := app.Test(hr, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("reindex %s: %v", org, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out reindexOut
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("reindex %s: %d %v", org, resp.StatusCode, err)
	}
	return out
}

// indexSpace is the space org's index was created in.
func indexSpace(t *testing.T, org string) (vec.Space, bool) {
	t.Helper()
	st, err := idx.storeFor(org)
	if err != nil {
		t.Fatal(err)
	}
	sp, ok, err := kbIndex.Space(context.Background(), st.db)
	if err != nil {
		t.Fatal(err)
	}
	return sp, ok
}

// wantRefused asserts err is a refusal of another space naming field.
func wantRefused(t *testing.T, err error, field string) {
	t.Helper()
	if !errors.Is(err, vec.ErrMixed) || !strings.Contains(err.Error(), field) {
		t.Fatalf("want a refusal of another space naming %s, got %v", field, err)
	}
}

// TestAPassageOfAnotherDimensionIsRefused: one model name answering at another
// dimension is another space. Its passage is not written beside the index's,
// a query of it is not scored, and the org's own passages still answer.
func TestAPassageOfAnotherDimensionIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountKB(t)
		install(t, app, "A")
		name := create(t, app, "A", "kb.memory", map[string]any{"title": "rota", "content": "the rota is on the wall", "kind": "fact"})
		search(t, app, "A", map[string]any{"query": "rota"}) // the org's first build has ended
		if sp, ok := indexSpace(t, "A"); !ok || sp.Dimensions != 1024 || sp.Model != idx.space.Model || sp.Normalization != vec.NormNone {
			t.Fatalf("the index records the space of its first write: %+v ok=%v", sp, ok)
		}
		ctx := context.Background()
		st, err := idx.storeFor("A")
		if err != nil {
			t.Fatal(err)
		}
		wantRefused(t, st.put(ctx, DTMemory.String(), "earlier", meta{}, idx.space, []span{{0, 4}}, [][]float32{{1, 0, 0}}), "dimensions")
		if n := passages(t, "A", DTMemory, "earlier"); n != 0 {
			t.Fatalf("a refused write left %d passages", n)
		}
		_, err = st.nearest(ctx, []float32{1, 0, 0}, reachOf{space: idx.space}, 10)
		wantRefused(t, err, "dimensions")
		hits, err := Semantic(ctx, SemanticReq{Org: "A", Query: "rota wall"})
		if err != nil || len(hits) != 1 || hits[0].Name != name || hits[0].Score <= 0 {
			t.Fatalf("the org's own passages still answer: %+v %v", hits, err)
		}
		if sp, _ := indexSpace(t, "A"); sp.Dimensions != 1024 {
			t.Fatalf("a refused write changed the index's space to %+v", sp)
		}
	})
}

// TestAnotherTaskPrefixIsRefusedUntilReindex: a deployment that embeds under
// another task prefix is in another space even with the same model and
// dimension. Its saves land with no passages, its queries are refused and the
// search says degraded, until a reindex rebuilds the index in the new space.
func TestAnotherTaskPrefixIsRefusedUntilReindex(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountKB(t)
		install(t, app, "A")
		create(t, app, "A", "kb.memory", map[string]any{"title": "rota", "content": "the rota is on the wall", "kind": "fact"})
		if out := search(t, app, "A", map[string]any{"query": "rota wall"}); out.Degraded || len(out.Hits) != 1 {
			t.Fatalf("before the change: %+v", out)
		}

		idx.space.Task.Document = "passage: "
		kettle := create(t, app, "A", "kb.memory", map[string]any{"title": "kettle", "content": "the kettle is in the cupboard", "kind": "fact"})
		if n := passages(t, "A", DTMemory, kettle); n != 0 {
			t.Fatalf("a document embedded under another prefix was written beside the index: %d passages", n)
		}
		if out := search(t, app, "A", map[string]any{"query": "kettle cupboard"}); !out.Degraded {
			t.Fatalf("a query of another space must say degraded, not score: %+v", out)
		}
		_, err := Semantic(context.Background(), SemanticReq{Org: "A", Query: "kettle cupboard"})
		wantRefused(t, err, "task.document")
		if sp, _ := indexSpace(t, "A"); sp.Task.Document != "" {
			t.Fatalf("the index's space changed in place: %+v", sp)
		}

		if out := reindexAs(t, app, "A"); out.Vectors != 2 || out.Failed != 0 {
			t.Fatalf("reindex: %+v", out)
		}
		if sp, ok := indexSpace(t, "A"); !ok || sp.Task.Document != "passage: " || sp.Dimensions != 1024 {
			t.Fatalf("after a reindex the index is in the new space: %+v ok=%v", sp, ok)
		}
		hits, err := Semantic(context.Background(), SemanticReq{Org: "A", Query: "kettle cupboard"})
		if err != nil || len(hits) == 0 || hits[0].Name != kettle {
			t.Fatalf("after a reindex the new space answers: %+v %v", hits, err)
		}
		if out := search(t, app, "A", map[string]any{"query": "kettle cupboard"}); out.Degraded {
			t.Fatalf("after a reindex the search is whole: %+v", out)
		}
	})
}

// TestAStoreFromBeforeSpacesKeepsAnswering: an org's passages written before
// the index recorded its space keep answering, and its first write adopts them.
// A deployment that embeds under another prefix does not adopt them: they were
// all embedded with none.
func TestAStoreFromBeforeSpacesKeepsAnswering(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountKB(t)
		install(t, app, "A")
		name := create(t, app, "A", "kb.memory", map[string]any{"title": "rota", "content": "the rota is on the wall", "kind": "fact"})
		search(t, app, "A", map[string]any{"query": "rota"})
		ctx := context.Background()
		st, err := idx.storeFor("A")
		if err != nil {
			t.Fatal(err)
		}
		forget := func() {
			t.Helper()
			if _, err := st.db.ExecContext(ctx, `DELETE FROM vector_spaces`); err != nil {
				t.Fatal(err)
			}
		}
		forget() // the store as a build before spaces left it

		hits, err := Semantic(ctx, SemanticReq{Org: "A", Query: "rota wall"})
		if err != nil || len(hits) != 1 || hits[0].Name != name {
			t.Fatalf("live search over a store from before spaces: %+v %v", hits, err)
		}
		if _, ok := indexSpace(t, "A"); ok {
			t.Fatal("a query recorded a space: only a write creates the index")
		}
		next := create(t, app, "A", "kb.memory", map[string]any{"title": "kettle", "content": "the kettle is in the cupboard", "kind": "fact"})
		if n := passages(t, "A", DTMemory, next); n != 1 {
			t.Fatalf("the first write into a store from before spaces: %d passages", n)
		}
		if sp, ok := indexSpace(t, "A"); !ok || sp.Dimensions != 1024 || sp.Model != idx.space.Model {
			t.Fatalf("the first write adopts the store: %+v ok=%v", sp, ok)
		}

		forget()
		idx.space.Task.Query = "query: "
		_, err = Semantic(ctx, SemanticReq{Org: "A", Query: "rota wall"})
		wantRefused(t, err, "task.query")
		third := create(t, app, "A", "kb.memory", map[string]any{"title": "keys", "content": "the keys are on the hook", "kind": "fact"})
		if n := passages(t, "A", DTMemory, third); n != 0 {
			t.Fatalf("a store from before spaces was adopted by another prefix: %d passages", n)
		}
	})
}

// TestAFileOfAnotherSpaceIsSearchedByItsWords: a file whose vectors the org's
// index refuses is ready, found by its words, and says why, with no retry
// buying embeddings no write would keep; a reindex embeds it in the new space.
func TestAFileOfAnotherSpaceIsSearchedByItsWords(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, mem := mountFiles(t, readerAI{fakeAI: fakeAI{dims: 1024}})
		install(t, app, "A")
		first := ingestOne(t, app, mem, "first.md", "text/markdown", []byte("# First\n\nThe phrase is zebra crossing."))
		if first.Status != fileReady || first.Embedded == 0 || first.Error != "" {
			t.Fatalf("the first file: %+v", first)
		}

		idx.space.Task.Document = "passage: "
		second := ingestOne(t, app, mem, "second.md", "text/markdown", []byte("# Second\n\nThe launch window opens on a Tuesday."))
		if second.Status != fileReady || second.Embedded != 0 || !strings.Contains(second.Error, "task.document") {
			t.Fatalf("a file the index refuses = %+v, want ready, unembedded, saying why", second)
		}
		out := searchFilesIn(t, app, "A", map[string]any{"query": "launch window"})
		if len(out.Passages) == 0 || !out.Degraded {
			t.Fatalf("a file of another space is found by its words, said as degraded: %+v", out)
		}

		reindexAs(t, app, "A")
		for _, id := range []string{first.ID, second.ID} {
			if f := settle(t, app, "A", id); f.Embedded == 0 || f.Embedded != f.Passages || f.Error != "" {
				t.Fatalf("after a reindex file %s = %+v, want every passage embedded", id, f)
			}
		}
		if sp, ok := indexSpace(t, "A"); !ok || sp.Task.Document != "passage: " {
			t.Fatalf("after a reindex the index is in the new space: %+v ok=%v", sp, ok)
		}
		if out := searchFilesIn(t, app, "A", map[string]any{"query": "launch window"}); out.Degraded || len(out.Passages) == 0 {
			t.Fatalf("after a reindex the file search is whole: %+v", out)
		}
	})
}
