package code

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/internal/vec"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// reindex_test.go proves POST /v1/code/reindex is the way an org's code index
// reaches another embedding space: it re-embeds every chunk the index holds from
// its own text, in the space this deployment embeds in, whatever repository the
// chunk came from. In the space the index is already in it embeds one chunk to
// learn the dimension the model answers now, then only the chunks with no vector,
// so a reindex that stopped part way is finished by calling it again.

// reindexAs runs POST /v1/code/reindex for org acme, as an org admin when
// admin is set, and answers the status and body.
func reindexAs(t *testing.T, app *zip.App, admin bool) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/code/reindex", nil)
	req.Header.Set("X-Org-Id", "acme")
	req.Header.Set("X-User-Id", "u_acme")
	if admin {
		req.Header.Set("X-User-IsOrgAdmin", "true")
	}
	return runReq(t, app, req)
}

// reindexed runs a reindex that must succeed and answers what it did.
func reindexed(t *testing.T, app *zip.App) reindexResult {
	t.Helper()
	status, b := reindexAs(t, app, true)
	if status != http.StatusOK {
		t.Fatalf("reindex: %d %s", status, b)
	}
	var out reindexResult
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode reindex: %v (%s)", err, b)
	}
	return out
}

// twoRepos indexes the fixtures into repository one and kitchen.go into
// repository two, and answers how many chunks the org's index holds.
func twoRepos(t *testing.T, app *zip.App, s *service) int {
	t.Helper()
	if res := indexFixtures(t, app, "acme", "one"); res.Vectors == 0 {
		t.Fatalf("fixtures have no vectors: %+v", res)
	}
	if res := indexExtra(t, app, "two"); res.Vectors == 0 {
		t.Fatalf("kitchen.go has no vectors: %+v", res)
	}
	return count(t, s, `SELECT COUNT(*) FROM chunks`)
}

// count answers a COUNT(*) over the org's store.
func count(t *testing.T, s *service, q string, args ...any) int {
	t.Helper()
	st, err := s.storeFor("acme")
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestReindexMovesTheIndexToAnotherDimension: once the model answers at another
// dimension the index refuses its vectors; a reindex re-embeds every chunk of
// every repository in the new space, semantic search answers again, and the
// next index pass writes vectors.
func TestReindexMovesTheIndexToAnotherDimension(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := newTestApp(t)
		chunks := twoRepos(t, app, s)

		s.embed = fakeEmbedder{dims: 32, enabled: true}
		if out := searchCode(t, app, "type=semantic&q=greeting"); !out.Degraded {
			t.Fatalf("before the reindex the index refuses the new dimension: %+v", out)
		}
		out := reindexed(t, app)
		if out.Chunks != chunks || out.Embedded != chunks || out.Vectors != chunks || out.Dimensions != 32 || out.ModelRevision != testSpace.Model {
			t.Fatalf("reindex = %+v, want all %d chunks embedded at 32 dimensions", out, chunks)
		}
		if n := count(t, s, `SELECT COUNT(*) FROM vectors WHERE dim<>32`); n != 0 {
			t.Fatalf("%d vectors of the old dimension survived the reindex", n)
		}
		if sp, ok := codeSpace(t, s); !ok || sp.Dimensions != 32 {
			t.Fatalf("after a reindex the index is in the new space: %+v ok=%v", sp, ok)
		}
		if out := searchCode(t, app, "type=semantic&q=greeting"); out.Degraded || len(out.Results) == 0 {
			t.Fatalf("after a reindex semantic search answers: %+v", out)
		}
		if out := searchCode(t, app, "type=semantic&q=kettle+cupboard&repo=two"); out.Degraded || len(out.Results) == 0 {
			t.Fatalf("every repository is embedded again, not only one: %+v", out)
		}
		status, b := doAuth(t, app, http.MethodPost, "/v1/code/index", "acme",
			indexIn{Repo: "three", Files: []fileInput{{Path: "teapot.go", Content: "package tea\n\n// Teapot pours.\nfunc Teapot() string { return \"pour\" }\n"}}})
		var res indexResult
		mustJSON(t, b, &res)
		if status != http.StatusOK || res.Vectors == 0 {
			t.Fatalf("an index pass after the reindex writes vectors: %d %+v", status, res)
		}
	})
}

// TestReindexMovesTheIndexToAnotherPrefix: a task prefix is part of the space;
// a reindex embeds every chunk under the new one, once.
func TestReindexMovesTheIndexToAnotherPrefix(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := newTestApp(t)
		chunks := twoRepos(t, app, s)

		var bought atomic.Int64
		s.embed = fakeEmbedder{dims: 64, enabled: true, task: vec.Task{Document: "passage: "}, calls: &bought}
		out := reindexed(t, app)
		if out.Embedded != chunks || out.Vectors != chunks || bought.Load() != int64(chunks) {
			t.Fatalf("reindex = %+v after %d texts, want all %d chunks embedded once", out, bought.Load(), chunks)
		}
		if sp, _ := codeSpace(t, s); sp.Task.Document != "passage: " {
			t.Fatalf("after a reindex the index records the new prefix: %+v", sp)
		}
		if out := searchCode(t, app, "type=semantic&q=greeting"); out.Degraded {
			t.Fatalf("after a reindex semantic search answers: %+v", out)
		}
	})
}

// TestReindexInItsOwnSpaceEmbedsOnlyWhatIsMissing: an index already in this
// deployment's space keeps its vectors. A reindex embeds one chunk to learn the
// dimension, then only the chunks with none, and a second one only that chunk.
func TestReindexInItsOwnSpaceEmbedsOnlyWhatIsMissing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := newTestApp(t)
		chunks := twoRepos(t, app, s)
		st, err := s.storeFor("acme")
		if err != nil {
			t.Fatal(err)
		}
		res, err := st.db.Exec(`DELETE FROM vectors WHERE chunk_id IN (SELECT id FROM chunks WHERE repo=$1)`, "two")
		if err != nil {
			t.Fatal(err)
		}
		lost, _ := res.RowsAffected()
		if lost == 0 {
			t.Fatal("no vectors to lose")
		}

		var bought atomic.Int64
		s.embed = fakeEmbedder{dims: 64, enabled: true, calls: &bought}
		out := reindexed(t, app)
		if out.Embedded != int(lost)+1 || out.Vectors != chunks || bought.Load() != lost+1 {
			t.Fatalf("reindex = %+v after %d texts, want the first chunk and the %d missing ones", out, bought.Load(), lost)
		}
		bought.Store(0)
		if out := reindexed(t, app); out.Embedded != 1 || out.Vectors != chunks || bought.Load() != 1 {
			t.Fatalf("a reindex of a whole index = %+v after %d texts, want only the first chunk", out, bought.Load())
		}
	})
}

// flakyEmbedder answers until it has embedded budget texts, then fails.
type flakyEmbedder struct {
	fakeEmbedder
	budget *atomic.Int64
}

var errEmbedDown = errors.New("embedding service unreachable")

func (f flakyEmbedder) Embed(ctx context.Context, org string, payer account.Account, project string, texts []string) ([][]float32, error) {
	if f.budget.Add(-int64(len(texts))) < 0 {
		return nil, errEmbedDown
	}
	return f.fakeEmbedder.Embed(ctx, org, payer, project, texts)
}

// TestAReindexThatStopsIsFinishedByTheNext: an embedding outage part way
// through a move leaves the index in the new space with some chunks embedded and
// says how far it got; the next reindex embeds the rest.
func TestAReindexThatStopsIsFinishedByTheNext(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := newTestApp(t)
		reindexPage = 2 // pages small enough that the fixtures take several
		t.Cleanup(func() { reindexPage = defaultReindexPage })
		chunks := twoRepos(t, app, s)
		if chunks < 6 {
			t.Fatalf("fixtures hold %d chunks, want at least 6", chunks)
		}

		// The first chunk (1 text) finds the new dimension and moves the index,
		// one page of 2 lands, and the next page finds the service down.
		var budget atomic.Int64
		budget.Store(3)
		s.embed = flakyEmbedder{fakeEmbedder: fakeEmbedder{dims: 32, enabled: true}, budget: &budget}
		status, b := reindexAs(t, app, true)
		if status != http.StatusBadGateway || !strings.Contains(string(b), fmt.Sprintf("embedded 3 of %d", chunks)) || !strings.Contains(string(b), "again") {
			t.Fatalf("a reindex that stops: %d %s, want 502 saying how far it got", status, b)
		}
		if sp, _ := codeSpace(t, s); sp.Dimensions != 32 {
			t.Fatalf("a stopped reindex leaves the index in the new space: %+v", sp)
		}
		if n := count(t, s, `SELECT COUNT(*) FROM vectors`); n != 3 {
			t.Fatalf("a stopped reindex left %d vectors, want the 3 it embedded", n)
		}

		var bought atomic.Int64
		s.embed = fakeEmbedder{dims: 32, enabled: true, calls: &bought}
		out := reindexed(t, app)
		if out.Embedded != chunks-3+1 || out.Vectors != chunks || bought.Load() != int64(chunks-3+1) {
			t.Fatalf("the next reindex = %+v after %d texts, want the first chunk and the other %d", out, bought.Load(), chunks-3)
		}
	})
}

// TestReindexIsAnOrgAdminsAndNeedsTheSemanticTier: a member who is not an org
// admin is refused, and so is a deployment with no embedder.
func TestReindexIsAnOrgAdminsAndNeedsTheSemanticTier(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := newTestApp(t)
		indexFixtures(t, app, "acme", "one")
		if status, b := reindexAs(t, app, false); status != http.StatusForbidden {
			t.Fatalf("a member's reindex: %d %s, want 403", status, b)
		}
		s.embed = fakeEmbedder{enabled: false}
		if status, b := reindexAs(t, app, true); status != http.StatusServiceUnavailable {
			t.Fatalf("a reindex with no semantic tier: %d %s, want 503", status, b)
		}
	})
}

// TestReindexOfAnIndexFromBeforeSpaces: an index whose vectors carry no record
// is rebuilt whole, since which model wrote them cannot be told.
func TestReindexOfAnIndexFromBeforeSpaces(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := newTestApp(t)
		chunks := twoRepos(t, app, s)
		st, err := s.storeFor("acme")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(`DELETE FROM vector_spaces`); err != nil {
			t.Fatal(err)
		}
		var bought atomic.Int64
		s.embed = fakeEmbedder{dims: 64, enabled: true, calls: &bought}
		if out := reindexed(t, app); out.Embedded != chunks || out.Vectors != chunks || bought.Load() != int64(chunks) {
			t.Fatalf("reindex of an index from before spaces = %+v after %d texts, want all %d", out, bought.Load(), chunks)
		}
		if _, ok := codeSpace(t, s); !ok {
			t.Fatal("after a reindex the index records its space")
		}
	})
}

// TestReindexOfAnEmptyIndex: an org with no code answers a count of nothing,
// and a record of another space goes, so the next write creates the index anew.
func TestReindexOfAnEmptyIndex(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := newTestApp(t)
		if out := reindexed(t, app); out.Chunks != 0 || out.Embedded != 0 || out.Vectors != 0 || out.Dimensions != 0 {
			t.Fatalf("reindex of an empty index = %+v", out)
		}
		st, err := s.storeFor("acme")
		if err != nil {
			t.Fatal(err)
		}
		tx, err := st.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := codeIndex.Claim(context.Background(), tx, vec.Space{Model: "older", Dimensions: 8, Normalization: vec.NormNone}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		reindexed(t, app)
		if _, ok := codeSpace(t, s); ok {
			t.Fatal("a reindex of an index with no chunks keeps a record of another space")
		}
		if res := indexExtra(t, app, "two"); res.Vectors == 0 {
			t.Fatalf("the next write creates the index in this deployment's space: %+v", res)
		}
	})
}
