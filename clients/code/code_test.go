package code

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/cek"

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

// TestTreeAndFile covers the repo-inspection primitives (the zread contract):
// /v1/code/tree = get_repo_structure, /v1/code/file = read_file — both over the
// org's own index, with per-org isolation and a 404 for an unindexed file.
func TestTreeAndFile(t *testing.T) {
	app, _ := newTestApp(t)
	indexFixtures(t, app, "acme", "svc")

	// tree: the repo's files with symbol counts.
	status, b := doAuth(t, app, http.MethodGet, "/v1/code/tree?repo=svc", "acme", nil)
	if status != http.StatusOK {
		t.Fatalf("tree status=%d body=%s", status, b)
	}
	var tree struct {
		Repo  string      `json:"repo"`
		Files []TreeEntry `json:"files"`
	}
	mustJSON(t, b, &tree)
	if len(tree.Files) == 0 {
		t.Fatal("tree returned no files")
	}
	var greeter *TreeEntry
	for i := range tree.Files {
		if tree.Files[i].Path == "greeter.go" {
			greeter = &tree.Files[i]
		}
	}
	if greeter == nil {
		t.Fatalf("tree missing greeter.go: %+v", tree.Files)
	}
	if greeter.Symbols == 0 || greeter.Lang != "go" {
		t.Errorf("greeter.go entry wrong: %+v", *greeter)
	}

	// file: the full reconstructed content.
	status, b = doAuth(t, app, http.MethodGet, "/v1/code/file?repo=svc&path=greeter.go", "acme", nil)
	if status != http.StatusOK {
		t.Fatalf("file status=%d body=%s", status, b)
	}
	var file struct {
		Repo, Path, Lang, Content string
	}
	mustJSON(t, b, &file)
	// Indexed content = the symbol chunks (not byte-verbatim; the git plane holds
	// exact bytes). Assert the symbol bodies the index knows are present.
	if file.Path != "greeter.go" || file.Lang != "go" ||
		!strings.Contains(file.Content, "Hello() string") || !strings.Contains(file.Content, "func greet") {
		t.Errorf("file content missing indexed symbols: path=%s lang=%s len=%d", file.Path, file.Lang, len(file.Content))
	}

	// an unindexed file 404s (distinct from an empty file).
	status, _ = doAuth(t, app, http.MethodGet, "/v1/code/file?repo=svc&path=nope.go", "acme", nil)
	if status != http.StatusNotFound {
		t.Errorf("unindexed file: got %d want 404", status)
	}

	// per-org isolation: orgB cannot read orgA's tree (empty, never a leak).
	status, b = doAuth(t, app, http.MethodGet, "/v1/code/tree?repo=svc", "otherorg", nil)
	if status != http.StatusOK {
		t.Fatalf("cross-org tree status=%d", status)
	}
	mustJSON(t, b, &tree)
	if len(tree.Files) != 0 {
		t.Errorf("cross-org tree leaked %d files", len(tree.Files))
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
		// cek.Exists, not os.Stat: a store still OPEN has not materialized its
		// database file on the pure-Go codec — only its sidecar is on disk.
		if !cek.Exists(p) {
			t.Fatalf("expected per-org store at %s", p)
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
