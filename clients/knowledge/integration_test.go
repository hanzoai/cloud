package knowledge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/framework"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// integration_test.go proves the KB spine end-to-end against a REAL framework store
// (SQLite) and a MOCK Qdrant + embeddings, driving the ACTUAL HTTP surface a client
// hits. It verifies the two properties that matter:
//
//  1. RAG: creating a kb-page fires the after_save hook, which embeds the page and
//     upserts it into the org's OWN collection with an org-pinned payload; a search
//     over the org's namespace retrieves it.
//
//  2. Per-org isolation: org A's knowledge is upserted into collection "kb_A" with
//     payload.org=A, and a search as org B is scoped to "kb_B" with payload.org=B —
//     org B never sees org A's point. This is the cross-tenant boundary RED verifies.

// fakeVector is an in-memory stand-in for Qdrant + the embeddings gateway. It records
// every upsert per collection and answers a search by returning that collection's
// points whose payload.org matches the filter (mirroring the real filter semantics),
// so the test asserts on the SAME scoping the production code relies on.
type fakeVector struct {
	mu        sync.Mutex
	upserts   map[string][]map[string]any // collection → payloads upserted
	server    *httptest.Server
	embedDims int
}

// fakeAI is a deterministic in-test AIClient: Embed returns one embedDims-vector
// per input, so the KB index path is exercised offline through the SAME deps.AI
// seam production uses — no HTTP embed endpoint.
type fakeAI struct{ dims int }

func (fakeAI) ChatCompletion(context.Context, *cloud.ChatRequest) (*cloud.ChatResponse, error) {
	return &cloud.ChatResponse{Content: "x"}, nil
}

func (f fakeAI) Embed(_ context.Context, req *cloud.EmbedRequest) ([][]float32, error) {
	out := make([][]float32, len(req.Inputs))
	for i := range req.Inputs {
		v := make([]float32, f.dims)
		for j := range v {
			v[j] = 0.1
		}
		out[i] = v
	}
	return out, nil
}

func newFakeVector(t *testing.T) *fakeVector {
	t.Helper()
	fv := &fakeVector{upserts: map[string][]map[string]any{}, embedDims: 8}
	mux := http.NewServeMux()

	// Embeddings (OpenAI-compatible): return a deterministic small vector.
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		vec := make([]float32, fv.embedDims)
		for i := range vec {
			vec[i] = 0.1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": vec}},
		})
	})

	// Qdrant REST subset: collection get/create, index, points upsert, points search.
	mux.HandleFunc("/collections/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/collections/")
		// e.g. "kb_A", "kb_A/points", "kb_A/points/search", "kb_A/index"
		col := path
		if i := strings.IndexByte(path, '/'); i >= 0 {
			col = path[:i]
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/points/search"):
			fv.handleSearch(w, r, col)
		case strings.HasSuffix(r.URL.Path, "/points/delete"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":{}}`))
		case strings.HasSuffix(r.URL.Path, "/points"):
			fv.handleUpsert(w, r, col)
		case strings.HasSuffix(r.URL.Path, "/index"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":{}}`))
		case r.Method == http.MethodGet:
			// Collection info: report "not found" so ensureCollection creates it.
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPut:
			// Create collection.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":true}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":{}}`))
		}
	})

	fv.server = httptest.NewServer(mux)
	t.Cleanup(fv.server.Close)
	return fv
}

func (fv *fakeVector) handleUpsert(w http.ResponseWriter, r *http.Request, col string) {
	var body struct {
		Points []struct {
			Payload map[string]any `json:"payload"`
		} `json:"points"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	fv.mu.Lock()
	for _, p := range body.Points {
		fv.upserts[col] = append(fv.upserts[col], p.Payload)
	}
	fv.mu.Unlock()
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"result":{"status":"completed"}}`))
}

// handleSearch returns this collection's points whose payload.org matches the
// request's org filter — the same scoping the real Qdrant applies — so the test
// exercises the production filter path, not a bypass.
func (fv *fakeVector) handleSearch(w http.ResponseWriter, r *http.Request, col string) {
	var body struct {
		Filter struct {
			Must []struct {
				Key   string `json:"key"`
				Match struct {
					Value any `json:"value"`
				} `json:"match"`
			} `json:"must"`
		} `json:"filter"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	wantOrg := ""
	for _, m := range body.Filter.Must {
		if m.Key == "org" {
			wantOrg, _ = m.Match.Value.(string)
		}
	}
	fv.mu.Lock()
	defer fv.mu.Unlock()
	result := []map[string]any{}
	for _, pl := range fv.upserts[col] {
		if str(pl["org"]) == wantOrg {
			result = append(result, map[string]any{"score": 0.99, "payload": pl})
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"result": result})
}

// resetIndexer forces the process-global indexer to re-read env for this test (it is
// a sync.Once singleton in prod; the test points it at the fake server).
func resetIndexer(t *testing.T, base string) {
	t.Helper()
	t.Setenv("vectorEndpoint", base)
	t.Setenv("KB_EMBED_DIMS", "8")
	// Embeddings run through the shared AI client (deps.AI), not an HTTP endpoint —
	// inject a deterministic fake so the index path is exercised offline.
	kbAI = fakeAI{dims: 8}
	// Rebuild the singleton against this env (tests share the process).
	idxOnce = sync.Once{}
	idx = nil
	_ = index()
}

func mountKB(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := framework.Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("framework.Mount: %v", err)
	}
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), Domain: "api.test"}); err != nil {
		t.Fatalf("kb.Mount: %v", err)
	}
	t.Cleanup(func() { _ = framework.Shutdown() })
	return app
}

// req issues an HTTP request with a validated principal (X-User-Id set) scoped to org.
func req(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = strings.NewReader(string(b))
	}
	hr := httptest.NewRequest(method, path, r)
	if body != nil {
		hr.Header.Set("Content-Type", "application/json")
	}
	hr.Header.Set("X-Org-Id", org)
	hr.Header.Set("X-User-Id", "u_"+org) // the validated-principal signal
	resp, err := app.Fiber().Test(hr)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestKBSpinePerOrgRAG is the end-to-end proof: install → create page (org A) →
// hook indexes into kb_A with org pin → search as A retrieves it → search as B (and
// the kb_B collection) sees nothing. Cross-org knowledge never leaks.
func TestKBSpinePerOrgRAG(t *testing.T) {
	fv := newFakeVector(t)
	resetIndexer(t, fv.server.URL)
	app := mountKB(t)

	// Install the kb module into BOTH orgs (each is its own tenant; the first
	// principal to administer an unowned org is auto-seeded System Manager).
	for _, org := range []string{"A", "B"} {
		if code, b := req(t, app, http.MethodPost, "/v1/framework/modules/kb/install", org, nil); code != http.StatusOK {
			t.Fatalf("install kb for %s: %d %s", org, code, b)
		}
	}

	// Org A creates a wiki page (Lexical body). This fires after_save → indexer.
	pageBody := map[string]any{
		"title": "Incident Runbook",
		"slug":  "incident-runbook",
		"body":  `{"root":{"type":"root","children":[{"type":"paragraph","children":[{"type":"text","text":"Page the on-call and restart the pod."}]}]}}`,
	}
	if code, b := req(t, app, http.MethodPost, "/v1/framework/kb-page", "A", pageBody); code != http.StatusCreated {
		t.Fatalf("create kb-page in A: %d %s", code, b)
	}

	// The hook must have upserted exactly one point into A's collection, org-pinned.
	// The collection name is derived through the SAME collection() helper the indexer
	// uses (which runs the org through provisioning.SanitizeOrg for an injective
	// physical namespace), so the test asserts the REAL collection, not a hardcoded
	// one — and the payload.org pin stays the raw org ("A").
	colA := (&indexer{}).collection("A")
	colB := (&indexer{}).collection("B")
	fv.mu.Lock()
	aPoints := fv.upserts[colA]
	bPoints := fv.upserts[colB]
	fv.mu.Unlock()
	if len(aPoints) != 1 {
		t.Fatalf("expected 1 upsert into %s, got %d", colA, len(aPoints))
	}
	if str(aPoints[0]["org"]) != "A" || str(aPoints[0]["doctype"]) != DTPage {
		t.Errorf("%s payload not org-pinned/typed: %+v", colA, aPoints[0])
	}
	if len(bPoints) != 0 {
		t.Errorf("org A's write leaked into %s: %+v", colB, bPoints)
	}

	// Org A retrieves its knowledge via the RAG entry point.
	code, b := req(t, app, http.MethodPost, "/v1/kb/search", "A", map[string]any{"query": "how do I handle an incident"})
	if code != http.StatusOK {
		t.Fatalf("search as A: %d %s", code, b)
	}
	var aRes struct {
		Hits []hit `json:"hits"`
	}
	_ = json.Unmarshal(b, &aRes)
	if len(aRes.Hits) != 1 || aRes.Hits[0].Title != "Incident Runbook" {
		t.Fatalf("org A should retrieve its page, got %s", b)
	}

	// Org B searches the SAME query — its namespace is empty. No cross-org leak.
	code, b = req(t, app, http.MethodPost, "/v1/kb/search", "B", map[string]any{"query": "how do I handle an incident"})
	if code != http.StatusOK {
		t.Fatalf("search as B: %d %s", code, b)
	}
	var bRes struct {
		Hits []hit `json:"hits"`
	}
	_ = json.Unmarshal(b, &bRes)
	if len(bRes.Hits) != 0 {
		t.Fatalf("CROSS-ORG LEAK: org B retrieved org A's knowledge: %s", b)
	}
}

// TestSearchRefusesWithoutPrincipal proves the retrieval surface refuses a request
// with an org header but NO validated principal (the anonymous-forge path).
func TestSearchRefusesWithoutPrincipal(t *testing.T) {
	fv := newFakeVector(t)
	resetIndexer(t, fv.server.URL)
	app := mountKB(t)

	// No X-User-Id → not a validated principal → 403 before any store/index access.
	hr := httptest.NewRequest(http.MethodPost, "/v1/kb/search", strings.NewReader(`{"query":"x"}`))
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("X-Org-Id", "victim") // forged org, no principal
	resp, err := app.Fiber().Test(hr)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("forged-org search must be 403, got %d", resp.StatusCode)
	}
}

// TestMemoryIndexedSameStore proves an AI memory (kb-memory) is indexed into the
// SAME per-org namespace as a wiki page — human wiki + AI memory are one store.
func TestMemoryIndexedSameStore(t *testing.T) {
	fv := newFakeVector(t)
	resetIndexer(t, fv.server.URL)
	app := mountKB(t)
	if code, b := req(t, app, http.MethodPost, "/v1/framework/modules/kb/install", "A", nil); code != http.StatusOK {
		t.Fatalf("install: %d %s", code, b)
	}
	mem := map[string]any{"title": "api rotation", "content": "the prod API key rotates on the first of each month", "kind": "fact"}
	if code, b := req(t, app, http.MethodPost, "/v1/framework/kb-memory", "A", mem); code != http.StatusCreated {
		t.Fatalf("create memory: %d %s", code, b)
	}
	colA := (&indexer{}).collection("A")
	fv.mu.Lock()
	pts := fv.upserts[colA]
	fv.mu.Unlock()
	if len(pts) != 1 || str(pts[0]["doctype"]) != DTMemory {
		t.Fatalf("memory not indexed into %s: %+v", colA, pts)
	}
}
