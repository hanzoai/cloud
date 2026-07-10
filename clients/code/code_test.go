package code

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/clients/provisioning"
	"github.com/zap-proto/zip"
)

// The org gate fails closed: a forged X-Org-Id with NO validated principal
// (no X-User-Id, as an off-gateway attacker would send) is refused on EVERY
// route. This is the cross-org boundary.
func TestPrincipalGateForbidden(t *testing.T) {
	app, _ := newTestApp(t)
	forged := func(method, path string, body any) int {
		var r *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		} else {
			r = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, r)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Org-Id", "victim") // forged, but no validated X-User-Id
		status, _ := runReq(t, app, req)
		return status
	}
	cases := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/code/search?q=x", nil},
		{http.MethodPost, "/v1/code/context", contextReq{Query: "x"}},
		{http.MethodGet, "/v1/code/ask?q=x", nil},
		{http.MethodPost, "/v1/code/index", indexReq{Repo: "r", Files: []fileInput{{Path: "a.go", Content: "package a"}}}},
	}
	for _, c := range cases {
		if got := forged(c.method, c.path, c.body); got != http.StatusForbidden {
			t.Errorf("%s %s: forged principal got %d, want 403", c.method, c.path, got)
		}
	}
}

func TestIndexSearchContextAsk(t *testing.T) {
	app, _ := newTestApp(t)
	res := indexFixtures(t, app, "acme", "svc")
	if res.Indexed != 3 {
		t.Fatalf("indexed=%d want 3", res.Indexed)
	}
	if res.Symbols == 0 || res.Chunks == 0 {
		t.Fatalf("no symbols/chunks: %+v", res)
	}
	if !res.Semantic || res.Vectors == 0 {
		t.Fatalf("semantic tier not populated: %+v", res)
	}

	// text tier
	assertResults(t, app, "acme", "/v1/code/search?type=text&q=greet", func(sp []Span) {
		if len(sp) == 0 {
			t.Error("text search for greet: no results")
		}
	})
	// symbol tier
	assertResults(t, app, "acme", "/v1/code/search?type=symbol&q=Hello", func(sp []Span) {
		found := false
		for _, s := range sp {
			if s.Symbol == "Hello" {
				found = true
			}
		}
		if !found {
			t.Errorf("symbol search for Hello: %+v", sp)
		}
	})
	// regex tier (Zoekt model: trigram prefilter + regexp verify)
	assertResults(t, app, "acme", "/v1/code/search?type=regex&q=func.*Hello", func(sp []Span) {
		if len(sp) == 0 {
			t.Error("regex search: no results")
		}
	})
	// semantic tier (fake embedder)
	assertResults(t, app, "acme", "/v1/code/search?type=semantic&q=greeting", func(sp []Span) {
		if len(sp) == 0 {
			t.Error("semantic search: no results")
		}
	})
	// hybrid (default)
	assertResults(t, app, "acme", "/v1/code/search?q=greeting+for+name", func(sp []Span) {
		if len(sp) == 0 {
			t.Error("hybrid search: no results")
		}
	})

	// context bundle
	status, b := doAuth(t, app, http.MethodPost, "/v1/code/context", "acme",
		contextReq{Query: "how does Hello greet", BudgetTokens: 2000, Repo: "svc"})
	if status != http.StatusOK {
		t.Fatalf("context status=%d body=%s", status, b)
	}
	var bundle ContextBundle
	mustJSON(t, b, &bundle)
	if len(bundle.Spans) == 0 || bundle.UsedTokens == 0 || bundle.UsedTokens > 2000 {
		t.Fatalf("bad bundle: spans=%d used=%d", len(bundle.Spans), bundle.UsedTokens)
	}

	// ask (cited RAG; fake synth)
	status, b = doAuth(t, app, http.MethodGet, "/v1/code/ask?q=how+does+hello+work&repo=svc", "acme", nil)
	if status != http.StatusOK {
		t.Fatalf("ask status=%d body=%s", status, b)
	}
	var ans AskAnswer
	mustJSON(t, b, &ans)
	if ans.Answer != "GROUNDED_ANSWER" {
		t.Errorf("ask answer=%q want GROUNDED_ANSWER", ans.Answer)
	}
	if len(ans.Citations) == 0 {
		t.Error("ask returned no citations")
	}
}

// A search issued as org B must never see org A's indexed code — the per-org
// file boundary, proven end to end. And the two org files exist separately.
func TestPerOrgIsolationHTTP(t *testing.T) {
	app, s := newTestApp(t)
	indexFixtures(t, app, "orgA", "svc")

	assertResults(t, app, "orgB", "/v1/code/search?type=symbol&q=Hello", func(sp []Span) {
		if len(sp) != 0 {
			t.Fatalf("org B saw org A's symbols: %+v", sp)
		}
	})
	assertResults(t, app, "orgA", "/v1/code/search?type=symbol&q=Hello", func(sp []Span) {
		if len(sp) == 0 {
			t.Fatal("org A cannot find its own symbol")
		}
	})

	for _, org := range []string{"orgA", "orgB"} {
		if _, err := s.storeFor(org); err != nil {
			t.Fatalf("storeFor %s: %v", org, err)
		}
	}
	for _, org := range []string{"orgA", "orgB"} {
		p := filepath.Join(s.dataDir, "orgs", provisioning.SanitizeOrg(org), "code.db")
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected per-org db at %s: %v", p, err)
		}
	}
}

func TestIncrementalAndPrune(t *testing.T) {
	app, _ := newTestApp(t)
	indexFixtures(t, app, "acme", "svc")

	// Re-indexing the identical tree skips every unchanged file.
	res := indexFixtures(t, app, "acme", "svc")
	if res.Indexed != 0 || res.Skipped != 3 {
		t.Fatalf("incremental: indexed=%d skipped=%d want 0/3", res.Indexed, res.Skipped)
	}

	// A pruning re-index with only one file removes the two absent files.
	body := indexReq{Repo: "svc", Prune: true, Files: []fileInput{{Path: "greeter.go", Content: goFixture}}}
	status, b := doAuth(t, app, http.MethodPost, "/v1/code/index", "acme", body)
	if status != http.StatusOK {
		t.Fatalf("prune status=%d body=%s", status, b)
	}
	var res2 indexResult
	mustJSON(t, b, &res2)
	if res2.Pruned != 2 || res2.Files != 1 {
		t.Fatalf("prune: pruned=%d files=%d want 2/1", res2.Pruned, res2.Files)
	}
}

func TestSearchRequiresQuery(t *testing.T) {
	app, _ := newTestApp(t)
	if status, _ := doAuth(t, app, http.MethodGet, "/v1/code/search", "acme", nil); status != http.StatusBadRequest {
		t.Errorf("empty q: status=%d want 400", status)
	}
}

// assertResults GETs a search path as org and runs check over the parsed spans.
func assertResults(t *testing.T, app *zip.App, org, path string, check func([]Span)) {
	t.Helper()
	status, b := doAuth(t, app, http.MethodGet, path, org, nil)
	if status != http.StatusOK {
		t.Fatalf("GET %s: status=%d body=%s", path, status, b)
	}
	var resp struct {
		Results []Span `json:"results"`
	}
	mustJSON(t, b, &resp)
	check(resp.Results)
}
