package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud/apps/framework"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/zap-proto/zip"
)

// passage_test.go proves the vector leg over the org's own store, through the
// surface a client hits: a long page is found by a fact in its tail, a re-save
// leaves no passage of the old text, a delete leaves none at all, no org reads
// another's passages, and an ai plane outage is a degraded answer rather than a
// failed write or a silent empty.

// flipAI is fakeAI that a test can take down mid-run.
type flipAI struct {
	fakeAI
	down atomic.Bool
}

func (f *flipAI) Embed(ctx context.Context, req *infer.EmbedRequest) ([][]float32, error) {
	if f.down.Load() {
		return nil, errAIDown
	}
	return f.fakeAI.Embed(ctx, req)
}

// tailFact is the one sentence the long page says about the vault, at its end.
const (
	tailFact  = "The vault combination is zebra quartz."
	tailQuery = "vault combination zebra quartz"
)

// longPage is a page of more than 20000 bytes whose only mention of the vault is
// its last sentence. The filler shares no word with tailQuery, so a similarity
// above zero can only come from the passage that holds the fact.
func longPage() string {
	var b strings.Builder
	for i := 0; b.Len() < 22000; i++ {
		fmt.Fprintf(&b, "Section %d reviews the backup rotation schedule for cluster %d. ", i, i*7)
		if i%6 == 5 {
			b.WriteString("\n\n")
		}
	}
	b.WriteString("\n\n## Vault\n\n" + tailFact)
	return b.String()
}

func install(t *testing.T, app *zip.App, orgs ...string) {
	t.Helper()
	for _, org := range orgs {
		if code, b := req(t, app, http.MethodPost, "/v1/framework/modules/kb/install", org, nil); code != http.StatusOK {
			t.Fatalf("install kb for %s: %d %s", org, code, b)
		}
	}
}

// create writes a document through /v1/framework and answers its name.
func create(t *testing.T, app *zip.App, org, doctype string, data map[string]any) string {
	t.Helper()
	code, b := req(t, app, http.MethodPost, "/v1/framework/"+doctype, org, data)
	if code != http.StatusCreated {
		t.Fatalf("create %s in %s: %d %s", doctype, org, code, b)
	}
	var doc struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(b, &doc); err != nil || doc.Name == "" {
		t.Fatalf("create %s: no name in %s", doctype, b)
	}
	return doc.Name
}

// search asks /v1/knowledge/search as org.
func search(t *testing.T, app *zip.App, org string, in map[string]any) searchOut {
	t.Helper()
	code, b := req(t, app, http.MethodPost, "/v1/knowledge/search", org, in)
	if code != http.StatusOK {
		t.Fatalf("search as %s: %d %s", org, code, b)
	}
	var out searchOut
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode search: %v (%s)", err, b)
	}
	return out
}

// spans reads a document's stored passage spans, in order.
func spans(t *testing.T, org, doctype, name string) []span {
	t.Helper()
	st, err := idx.storeFor(org)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := st.db.Query(`SELECT lo, hi FROM chunks WHERE doctype=$1 AND name=$2 ORDER BY seq`, doctype, name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []span
	for rows.Next() {
		var s span
		if err := rows.Scan(&s.lo, &s.hi); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// TestTailFactIsFound: a fact in the last sentence of a 22 KB page is the top
// answer, with a real similarity, over a dozen documents that share nothing with
// the question — and the passage it is cited by is the tail, not the head.
//
// It ends on the mutant: the same page stored as ONE passage, which is what one
// vector per page was. The model reads a bounded window (fakeWindow, as a real
// one does), so that vector never holds the fact and the page is not found — the
// passages, not the fake, are what find it.
func TestTailFactIsFound(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountKB(t)
		install(t, app, "A")
		page := longPage()
		name := create(t, app, "A", "kb.page", map[string]any{"title": "Operations handbook", "slug": "handbook", "body": page})
		for i := range 12 {
			create(t, app, "A", "kb.memory", map[string]any{"title": fmt.Sprintf("note %d", i), "content": "lunch menu for the offsite", "kind": "note"})
		}
		sp := spans(t, "A", DTPage.String(), name)
		if len(sp) < 10 {
			t.Fatalf("a %d-byte page has %d passages", len(page), len(sp))
		}

		out := search(t, app, "A", map[string]any{"query": tailQuery, "limit": 1})
		if out.Degraded || len(out.Hits) != 1 || out.Hits[0].Name != name || out.Hits[0].Score <= 0 {
			t.Fatalf("the page must be the top hit with a positive similarity: %+v", out)
		}

		// The /v1/search leg answers the same document, cited by the passage that
		// holds the fact — far past the first 20000 bytes of the page.
		hits, err := Semantic(context.Background(), SemanticReq{Org: "A", Query: tailQuery, Limit: 1})
		if err != nil {
			t.Fatalf("Semantic: %v", err)
		}
		if len(hits) != 1 || hits[0].Name != name {
			t.Fatalf("Semantic top hit: %+v", hits)
		}
		if hits[0].lo < 20000 || !strings.Contains(hits[0].Text, tailFact) {
			t.Fatalf("cited by passage %d..%d: %q", hits[0].lo, hits[0].hi, hits[0].Text)
		}

		ctx := context.Background()
		text := docText(DTPage, "Operations handbook", map[string]any{"body": page})
		whole, err := idx.embed(ctx, "A", "", []string{text})
		if err != nil {
			t.Fatal(err)
		}
		st, err := idx.storeFor("A")
		if err != nil {
			t.Fatal(err)
		}
		if err := st.put(ctx, DTPage.String(), name, meta{}, idx.space, []span{{0, len(text)}}, whole); err != nil {
			t.Fatal(err)
		}
		if hits, err = Semantic(ctx, SemanticReq{Org: "A", Query: tailQuery, Limit: 20}); err != nil {
			t.Fatalf("Semantic: %v", err)
		}
		for _, h := range hits {
			if h.Score > 0 {
				t.Fatalf("one vector for the whole page still found its tail: %+v", h)
			}
		}
	})
}

// TestResaveLeavesNoStalePassages: replacing a long page with a short one leaves
// exactly the short one's passages — none of the old text, whose tail fact is no
// longer found.
func TestResaveLeavesNoStalePassages(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountKB(t)
		install(t, app, "A")
		name := create(t, app, "A", "kb.page", map[string]any{"title": "Operations handbook", "slug": "handbook", "body": longPage()})
		if n := passages(t, "A", DTPage, name); n < 10 {
			t.Fatalf("long page: %d passages", n)
		}
		short := map[string]any{"title": "Operations handbook", "slug": "handbook", "body": "The handbook moved to the new wiki."}
		if code, b := req(t, app, http.MethodPut, "/v1/framework/kb.page/"+name, "A", short); code != http.StatusOK {
			t.Fatalf("re-save: %d %s", code, b)
		}
		text := docText(DTPage, "Operations handbook", short)
		if got, want := spans(t, "A", DTPage.String(), name), split(text); len(got) != len(want) || got[0] != want[0] {
			t.Fatalf("after re-save the page has passages %v, want exactly %v", got, want)
		}
		if n := passages(t, "A", framework.ID{}, ""); n != 1 {
			t.Fatalf("the org's store holds %d passages, want the one", n)
		}
		out := search(t, app, "A", map[string]any{"query": tailQuery})
		if len(out.Hits) != 1 || out.Hits[0].Score != 0 {
			t.Fatalf("the old tail must be gone: %+v", out.Hits)
		}
	})
}

// TestDeleteRemovesPassages: deleting a page removes its passages and no one
// else's.
func TestDeleteRemovesPassages(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountKB(t)
		install(t, app, "A")
		name := create(t, app, "A", "kb.page", map[string]any{"title": "Operations handbook", "slug": "handbook", "body": longPage()})
		mem := create(t, app, "A", "kb.memory", map[string]any{"title": "keep", "content": "the rota is on the wall", "kind": "fact"})
		if code, b := req(t, app, http.MethodDelete, "/v1/framework/kb.page/"+name, "A", nil); code/100 != 2 {
			t.Fatalf("delete: %d %s", code, b)
		}
		if n := passages(t, "A", DTPage, name); n != 0 {
			t.Fatalf("a deleted page still has %d passages", n)
		}
		if n := passages(t, "A", DTMemory, mem); n != 1 {
			t.Fatalf("the delete took another document's passages: %d left", n)
		}
		for _, h := range search(t, app, "A", map[string]any{"query": tailQuery}).Hits {
			if h.Name == name {
				t.Fatalf("a deleted page is still found: %+v", h)
			}
		}
	})
}

// TestTenantIsolation: two orgs holding a page under the SAME name keep their
// passages in two files, and neither's search or vector leg ever answers with
// the other's document.
func TestTenantIsolation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountKB(t)
		install(t, app, "A", "B")
		create(t, app, "A", "kb.page", map[string]any{"title": "Alpha plan", "slug": "plan", "body": "alpha secret launch codes"})
		create(t, app, "B", "kb.page", map[string]any{"title": "Beta plan", "slug": "plan", "body": "beta weekly notes"})

		a, err := idx.storeFor("A")
		if err != nil {
			t.Fatal(err)
		}
		b, err := idx.storeFor("B")
		if err != nil {
			t.Fatal(err)
		}
		if a == b {
			t.Fatal("two orgs share one passage store")
		}
		beta := split(docText(DTPage, "Beta plan", map[string]any{"body": "beta weekly notes"}))
		if got := spans(t, "B", DTPage.String(), "plan"); len(got) != len(beta) || got[0] != beta[0] {
			t.Fatalf("B's store holds passages %v, want B's page's own %v", got, beta)
		}

		q := "alpha secret launch codes"
		for _, h := range search(t, app, "B", map[string]any{"query": q}).Hits {
			if h.Title != "Beta plan" || h.Score != 0 {
				t.Fatalf("CROSS-ORG LEAK through /v1/knowledge/search: %+v", h)
			}
		}
		hits, err := Semantic(context.Background(), SemanticReq{Org: "B", Query: q})
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range hits {
			if h.Title != "Beta plan" || strings.Contains(h.Text, "alpha") {
				t.Fatalf("CROSS-ORG LEAK through Semantic: %+v", h)
			}
		}
		if out := search(t, app, "A", map[string]any{"query": q}); len(out.Hits) != 1 || out.Hits[0].Title != "Alpha plan" || out.Hits[0].Score <= 0 {
			t.Fatalf("A must find its own plan: %+v", out.Hits)
		}
	})
}

// TestAIPlaneDown is the failure contract (index.go). With the ai plane gone, a
// write still lands and leaves its document with NO passages rather than stale
// ones; /v1/knowledge/search answers 200 with degraded:true rather than a 5xx or
// an unqualified empty; and the vector leg returns the error for /v1/search to
// report. When the plane is back, the next save restores the passages.
func TestAIPlaneDown(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ai := &flipAI{fakeAI: fakeAI{dims: 1024}}
		app := mountKBWith(t, ai)
		install(t, app, "A")
		name := create(t, app, "A", "kb.page", map[string]any{"title": "Operations handbook", "slug": "handbook", "body": longPage()})
		if n := passages(t, "A", DTPage, name); n == 0 {
			t.Fatal("no passages while the plane was up")
		}

		ai.down.Store(true)
		edited := map[string]any{"title": "Operations handbook", "slug": "handbook", "body": "Rewritten while the plane was down."}
		if code, b := req(t, app, http.MethodPut, "/v1/framework/kb.page/"+name, "A", edited); code != http.StatusOK {
			t.Fatalf("a write must land with the ai plane down: %d %s", code, b)
		}
		if n := passages(t, "A", DTPage, name); n != 0 {
			t.Fatalf("%d passages of text the page no longer has", n)
		}
		fresh := create(t, app, "A", "kb.memory", map[string]any{"title": "new", "content": "written during the outage", "kind": "note"})
		if n := passages(t, "A", DTMemory, fresh); n != 0 {
			t.Fatalf("a document embedded with no ai plane: %d passages", n)
		}
		out := search(t, app, "A", map[string]any{"query": tailQuery})
		if !out.Degraded || out.Hits == nil || len(out.Hits) != 0 {
			t.Fatalf("an outage is degraded:true with the other leg's hits (none here): %+v", out)
		}
		if _, err := Semantic(context.Background(), SemanticReq{Org: "A", Query: tailQuery}); !errors.Is(err, errAIDown) {
			t.Fatalf("the vector leg must return the outage, got %v", err)
		}

		ai.down.Store(false)
		if code, b := req(t, app, http.MethodPut, "/v1/framework/kb.page/"+name, "A", edited); code != http.StatusOK {
			t.Fatalf("re-save: %d %s", code, b)
		}
		if n := passages(t, "A", DTPage, name); n != 1 {
			t.Fatalf("after the outage the re-save has %d passages, want 1", n)
		}
	})
}
