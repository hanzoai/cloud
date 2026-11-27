package code

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/internal/vec"
	"github.com/zap-proto/zip"
)

// space_test.go proves the org's code index keeps ONE embedding space
// (internal/vec): a deployment whose model answers at another dimension, or
// embeds under another task prefix, indexes the org's code by its words and
// symbols with no vector written beside the index's, and its semantic queries
// are refused, never scored.

// extraGo is a file indexed after the deployment's space changed.
const extraGo = "package kitchen\n\n// Kettle says where the kettle is kept.\nfunc Kettle() string { return \"in the cupboard\" }\n"

// indexExtra indexes extraGo into repo and answers the pass.
func indexExtra(t *testing.T, app *zip.App, repo string) indexResult {
	t.Helper()
	status, b := doAuth(t, app, http.MethodPost, "/v1/code/index", "acme", indexIn{Repo: repo, Files: []fileInput{{Path: "kitchen.go", Content: extraGo}}})
	if status != http.StatusOK {
		t.Fatalf("index kitchen.go: %d %s", status, b)
	}
	var res indexResult
	mustJSON(t, b, &res)
	return res
}

// searchCode runs a search and answers its results and whether it was degraded.
func searchCode(t *testing.T, app *zip.App, query string) searchResults {
	t.Helper()
	status, b := doAuth(t, app, http.MethodGet, "/v1/code/search?"+query, "acme", nil)
	if status != http.StatusOK {
		t.Fatalf("search %s: %d %s", query, status, b)
	}
	var out searchResults
	mustJSON(t, b, &out)
	return out
}

// codeSpace is the space the org's code index records.
func codeSpace(t *testing.T, s *service) (vec.Space, bool) {
	t.Helper()
	st, err := s.storeFor("acme")
	if err != nil {
		t.Fatal(err)
	}
	sp, ok, err := codeIndex.Space(context.Background(), st.db)
	if err != nil {
		t.Fatal(err)
	}
	return sp, ok
}

// TestCodeIndexRefusesAnotherDimension: the model answering at another
// dimension is another space. The file is indexed by its words, no vector of it
// is written, a semantic search is refused rather than scored, and the index's
// space is as it was.
func TestCodeIndexRefusesAnotherDimension(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := newTestApp(t)
		before := indexFixtures(t, app, "acme", "r")
		if sp, ok := codeSpace(t, s); !ok || sp.Dimensions != 64 || sp.Model != testSpace.Model {
			t.Fatalf("the index records the space of its first write: %+v ok=%v", sp, ok)
		}

		s.embed = fakeEmbedder{dims: 32, enabled: true}
		res := indexExtra(t, app, "r")
		if res.Indexed != 1 || res.Vectors != before.Vectors {
			t.Fatalf("a file of another dimension: %+v, want indexed with no vectors (index held %d)", res, before.Vectors)
		}
		if out := searchCode(t, app, "type=text&q=cupboard"); len(out.Results) == 0 {
			t.Fatal("a file whose vectors were refused is not found by its words")
		}
		if out := searchCode(t, app, "type=semantic&q=greeting"); !out.Degraded || len(out.Results) != 0 {
			t.Fatalf("a semantic query of another dimension must be refused, not scored: %+v", out)
		}
		if out := searchCode(t, app, "q=Hello"); len(out.Results) == 0 {
			t.Fatal("a hybrid search answers by its other tiers")
		}
		if sp, _ := codeSpace(t, s); sp.Dimensions != 64 {
			t.Fatalf("a refused write changed the index's space to %+v", sp)
		}
	})
}

// TestCodeIndexRefusesAnotherPrefix: the same model and dimension under
// another task prefix is another space. The index refuses it before a single
// embedding is bought, for a write and a query alike.
func TestCodeIndexRefusesAnotherPrefix(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := newTestApp(t)
		before := indexFixtures(t, app, "acme", "r")

		var bought atomic.Int64
		s.embed = fakeEmbedder{dims: 64, enabled: true, task: vec.Task{Document: "passage: "}, calls: &bought}
		res := indexExtra(t, app, "r")
		if res.Indexed != 1 || res.Vectors != before.Vectors {
			t.Fatalf("a file under another prefix: %+v, want indexed with no vectors (index held %d)", res, before.Vectors)
		}
		if out := searchCode(t, app, "type=semantic&q=greeting"); !out.Degraded || len(out.Results) != 0 {
			t.Fatalf("a semantic query under another prefix must be refused: %+v", out)
		}
		if n := bought.Load(); n != 0 {
			t.Fatalf("%d texts embedded for an index that refuses them", n)
		}
		if sp, _ := codeSpace(t, s); sp.Task.Document != "" {
			t.Fatalf("a refused write changed the index's space to %+v", sp)
		}
	})
}

// TestCodeIndexFromBeforeSpacesIsAdopted: an org's vectors written before the
// index recorded its space keep answering, and the first write adopts them.
func TestCodeIndexFromBeforeSpacesIsAdopted(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := newTestApp(t)
		indexFixtures(t, app, "acme", "r")
		st, err := s.storeFor("acme")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(`DELETE FROM vector_spaces`); err != nil {
			t.Fatal(err)
		}
		if out := searchCode(t, app, "type=semantic&q=greeting"); out.Degraded || len(out.Results) == 0 {
			t.Fatalf("live semantic search over an index from before spaces: %+v", out)
		}
		if _, ok := codeSpace(t, s); ok {
			t.Fatal("a query recorded a space: only a write creates the index")
		}
		indexExtra(t, app, "r")
		if sp, ok := codeSpace(t, s); !ok || sp.Dimensions != 64 || sp.Model != testSpace.Model {
			t.Fatalf("the first write adopts the index: %+v ok=%v", sp, ok)
		}
	})
}
