package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/hanzoai/cloud/apps/framework"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// integration_test.go proves the KB spine end-to-end against a REAL framework store
// and a REAL passage store (both SQLite, one file per org) with an offline
// embedder, driving the ACTUAL HTTP surface a client hits. It verifies the two
// properties that matter:
//
//  1. RAG: creating a kb.page fires the after_save hook, which cuts the page into
//     passages, embeds them and writes them into the org's OWN store; a search
//     over the org's knowledge retrieves it.
//
//  2. Per-org isolation: org A's passages are written into A's file, and a search
//     as org B reads B's file — org B never sees org A's knowledge. This is the
//     cross-tenant boundary RED verifies.

// fakeAI is a deterministic, offline embedder on the SAME seam production embeds
// through (cloud.Deps.Embed): the hashing trick over lowercased words, so text
// sharing words with a query scores a higher cosine and text sharing none scores
// 0. Like a real model it reads a bounded window — the first fakeWindow bytes of
// each input — which is exactly why one vector per whole page lost the page's
// tail.
type fakeAI struct{ dims int }

const fakeWindow = 4096

func (fakeAI) ChatCompletion(context.Context, *infer.ChatRequest) (*infer.ChatResponse, error) {
	return &infer.ChatResponse{Content: "x"}, nil
}

func (f fakeAI) Embed(_ context.Context, req *infer.EmbedRequest) ([][]float32, error) {
	out := make([][]float32, len(req.Inputs))
	for i, in := range req.Inputs {
		if len(in) > fakeWindow {
			in = in[:fakeWindow]
		}
		v := make([]float32, f.dims)
		for _, w := range words(in) {
			h := fnv.New32a()
			_, _ = h.Write([]byte(w))
			v[h.Sum32()%uint32(f.dims)]++
		}
		out[i] = v
	}
	return out, nil
}

func (fakeAI) Rerank(_ context.Context, req *infer.RerankRequest) ([]float64, error) {
	return make([]float64, len(req.Documents)), nil
}

// words is the fake's tokenizer: lowercased runs of letters and digits.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// downAI is the ai plane unreachable: every embedding fails.
type downAI struct{ fakeAI }

var errAIDown = errors.New("ai plane unreachable")

func (downAI) Embed(context.Context, *infer.EmbedRequest) ([][]float32, error) { return nil, errAIDown }

// compose installs what a host installs. A subsystem never installs cloud.Bridge
// (routes says why): the program's composer owns it — serve.go in production — so
// a test app owes the same install, else every org-scoped op answers 403 for a
// reason no composed program has.
func compose(app *zip.App) { app.Use(cloud.Bridge(), zip.Handler(asSubject)) }

// asSubject is the test composer's stand-in for the identity middleware's one
// job the header harness cannot do: mint a principal WITH a subject. In the
// program a person's is a verified token's `sub`, which is also their User;
// here it is X-Test-Subject, so a test can be two different people. X-Test-Kind
// makes it a key's or an application's instead, whose User X-Test-User states.
func asSubject(c *zip.Ctx) error {
	if sub := c.Header("X-Test-Subject"); sub != "" {
		kind, user := principal.Kind(c.Header("X-Test-Kind")), c.Header("X-Test-User")
		if kind == "" {
			kind, user = principal.Person, sub
		}
		principal.Mint(c, principal.Principal{Org: c.Org(), User: user, Subject: sub, Stable: true, Kind: kind})
	}
	return c.Next()
}

// mountKB mounts framework and knowledge together, embedding through fakeAI.
func mountKB(t *testing.T) *zip.App { return mountKBWith(t, fakeAI{dims: 1024}) }

// mountKBWith mounts them with ai as the ai plane client, exactly as a host hands
// knowledge deps.Embed.
func mountKBWith(t *testing.T, ai infer.Client) *zip.App { return mountKBAt(t, t.TempDir(), ai) }

// mountKBAt mounts them over the data in dir, as a restarted host finds it.
func mountKBAt(t *testing.T, dir string, ai infer.Client) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	// The lane is enabled for every org here; the gate is tested in apps/framework.
	planetest.Manager(t)
	planetest.Entitled(t, func(_, product string) bool { return product == Module })
	t.Setenv("CLOUD_DATA_DIR", dir)
	if err := framework.Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("framework.Use:  %v", err)
	}
	t.Setenv("CLOUD_DOMAIN", "api.test")
	if err := Use(app, cloud.Deps{Embed: ai}); err != nil {
		t.Fatalf("kb.Use:  %v", err)
	}
	t.Cleanup(func() {
		_ = Shutdown()
		_ = framework.Shutdown()
	})
	return app
}

// passages counts the rows a document has in the org's passage store; "" for
// the doctype and name counts the org's whole store.
func passages(t *testing.T, org string, dt framework.ID, name string) int {
	t.Helper()
	st, err := idx.storeFor(org)
	if err != nil {
		t.Fatalf("store for %s: %v", org, err)
	}
	q, args := `SELECT COUNT(*) FROM chunks`, []any{}
	if name != "" {
		q, args = q+` WHERE doctype=$1 AND name=$2`, []any{dt.String(), name}
	}
	var n int
	if err := st.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("count passages: %v", err)
	}
	return n
}

// req issues an HTTP request with a validated principal (X-User-Id set) scoped to org.
func req(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	if strings.HasSuffix(path, "/modules/kb/install") {
		// Installing the kb module opens the org's framework store, which is ported to
		// hanzo-sql by its own commit and opens as a file until then. The knowledge store,
		// which these tests are for, opens on the first document, after this.
		dsn := os.Getenv(locus.Env)
		t.Setenv(locus.Env, "")
		defer t.Setenv(locus.Env, dsn)
	}
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
	// The router bounds a test request at 1s unless told otherwise, and this one does
	// real work, so on a busy runner it timed out with nothing wrong.
	resp, err := app.Test(hr, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestKBSpinePerOrgRAG is the end-to-end proof: install → create page (org A) →
// hook writes its passages into A's own store → search as A retrieves it →
// search as B (and B's store) sees nothing. Cross-org knowledge never leaks.
func TestKBSpinePerOrgRAG(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
		if code, b := req(t, app, http.MethodPost, "/v1/framework/kb.page", "A", pageBody); code != http.StatusCreated {
			t.Fatalf("create kb.page in A: %d %s", code, b)
		}

		// The hook wrote the page's passage into A's store and nothing into B's.
		if n := passages(t, "A", DTPage, "incident-runbook"); n != 1 {
			t.Fatalf("A's store holds %d passages of the page, want 1", n)
		}
		if n := passages(t, "B", framework.ID{}, ""); n != 0 {
			t.Fatalf("org A's write leaked %d passages into B's store", n)
		}

		// Org A retrieves its knowledge via the RAG entry point.
		code, b := req(t, app, http.MethodPost, "/v1/knowledge/search", "A", map[string]any{"query": "how do I handle an incident"})
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
		code, b = req(t, app, http.MethodPost, "/v1/knowledge/search", "B", map[string]any{"query": "how do I handle an incident"})
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
	})
}

// TestSearchRefusesWithoutPrincipal proves the retrieval surface refuses a request
// with an org header but NO validated principal (the anonymous-forge path).
func TestSearchRefusesWithoutPrincipal(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountKB(t)

		// No X-User-Id → not a validated principal → 401 before any store/index access.
		hr := httptest.NewRequest(http.MethodPost, "/v1/knowledge/search", strings.NewReader(`{"query":"x"}`))
		hr.Header.Set("Content-Type", "application/json")
		hr.Header.Set("X-Org-Id", "victim") // forged org, no principal
		resp, err := app.Test(hr, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("test: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("forged-org search must be 401, got %d", resp.StatusCode)
		}
	})
}

// TestMemoryIndexedSameStore proves an AI memory (kb.memory) is indexed into the
// SAME per-org namespace as a wiki page — human wiki + AI memory are one store.
func TestMemoryIndexedSameStore(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountKB(t)
		if code, b := req(t, app, http.MethodPost, "/v1/framework/modules/kb/install", "A", nil); code != http.StatusOK {
			t.Fatalf("install: %d %s", code, b)
		}
		mem := map[string]any{"title": "api rotation", "content": "the prod API key rotates on the first of each month", "kind": "fact"}
		code, b := req(t, app, http.MethodPost, "/v1/framework/kb.memory", "A", mem)
		if code != http.StatusCreated {
			t.Fatalf("create memory: %d %s", code, b)
		}
		var created struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(b, &created)
		if n := passages(t, "A", DTMemory, created.Name); n != 1 {
			t.Fatalf("memory %q has %d passages in A's store, want 1 (%s)", created.Name, n, b)
		}
	})
}

// as issues a request as one PERSON in org: the header principal plus a subject.
func as(t *testing.T, app *zip.App, method, path, org, subject string, body any) (int, []byte) {
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
	hr.Header.Set("X-User-Id", "u_"+org)
	hr.Header.Set("X-Test-Subject", subject)
	resp, err := app.Test(hr, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// TestOwnedDocumentReachesItsOwnerAlone: a memory one person claims is found by
// that person and by nobody else in the org — not the org's own search, not
// another member's — while the org's documents stay in everyone's reach. It
// also proves the claim itself is bounded: naming another person is refused.
func TestOwnedDocumentReachesItsOwnerAlone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountKB(t)
		if code, b := req(t, app, http.MethodPost, "/v1/framework/modules/kb/install", "A", nil); code != http.StatusOK {
			t.Fatalf("install: %d %s", code, b)
		}
		shared := map[string]any{"title": "runbook", "content": "rotate the key monthly", "kind": "fact"}
		if code, b := req(t, app, http.MethodPost, "/v1/framework/kb.memory", "A", shared); code != http.StatusCreated {
			t.Fatalf("org memory: %d %s", code, b)
		}
		mine := map[string]any{"title": "my note", "content": "my private draft", "kind": "note", "owner": "alice"}
		if code, b := as(t, app, http.MethodPost, "/v1/framework/kb.memory", "A", "alice", mine); code != http.StatusCreated {
			t.Fatalf("alice's memory: %d %s", code, b)
		}
		forged := map[string]any{"title": "planted", "content": "x", "kind": "note", "owner": "alice"}
		if code, _ := as(t, app, http.MethodPost, "/v1/framework/kb.memory", "A", "bob", forged); code/100 == 2 {
			t.Fatal("bob must not be able to write a document into alice's reach")
		}
		titles := func(raw []byte) map[string]bool {
			var out struct {
				Hits []struct {
					Title string `json:"title"`
				} `json:"hits"`
			}
			_ = json.Unmarshal(raw, &out)
			m := map[string]bool{}
			for _, h := range out.Hits {
				m[h.Title] = true
			}
			return m
		}
		q := map[string]any{"query": "draft"}
		_, raw := as(t, app, http.MethodPost, "/v1/knowledge/search", "A", "alice", q)
		if got := titles(raw); !got["my note"] || !got["runbook"] {
			t.Fatalf("alice sees her own and the org's: %v", got)
		}
		_, raw = as(t, app, http.MethodPost, "/v1/knowledge/search", "A", "bob", q)
		if got := titles(raw); got["my note"] || !got["runbook"] {
			t.Fatalf("bob sees the org's only: %v", got)
		}
		_, raw = req(t, app, http.MethodPost, "/v1/knowledge/search", "A", q)
		if got := titles(raw); got["my note"] || !got["runbook"] {
			t.Fatalf("the org itself sees the org's only: %v", got)
		}
	})
}

// asKind is as for a principal of another kind: its Subject and its User as the
// boundary mints them for an API key or an application.
func asKind(t *testing.T, app *zip.App, method, path, org, subject string, kind principal.Kind, user string, body any) (int, []byte) {
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
	hr.Header.Set("X-User-Id", user)
	hr.Header.Set("X-Test-Subject", subject)
	hr.Header.Set("X-Test-Kind", string(kind))
	hr.Header.Set("X-Test-User", user)
	resp, err := app.Test(hr, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// An application an org admin names after a member presents that member's key's
// subject, `A/bob`. A document is owned by the caller's User, so what bob's key
// owns is found by bob's key alone — the application named bob neither finds it
// nor claims into its reach — and what the application owns is its own.
func TestAnApplicationNamedAfterAMemberDoesNotReachTheirDocuments(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountKB(t)
		if code, b := req(t, app, http.MethodPost, "/v1/framework/modules/kb/install", "A", nil); code != http.StatusOK {
			t.Fatalf("install: %d %s", code, b)
		}
		key := func(method, path string, body any) (int, []byte) {
			return asKind(t, app, method, path, "A", "A/bob", principal.Key, "A/bob", body)
		}
		named := func(method, path string, body any) (int, []byte) {
			return asKind(t, app, method, path, "A", "A/bob", principal.App, "app:A/bob", body)
		}
		if code, b := key(http.MethodPost, "/v1/framework/kb.memory",
			map[string]any{"title": "the key's note", "content": "the private draft", "kind": "note", "owner": "A/bob"}); code != http.StatusCreated {
			t.Fatalf("bob's key's memory: %d %s", code, b)
		}
		if code, _ := named(http.MethodPost, "/v1/framework/kb.memory",
			map[string]any{"title": "planted", "content": "the private draft", "kind": "note", "owner": "A/bob"}); code/100 == 2 {
			t.Fatal("the application named bob wrote a document into bob's key's reach")
		}
		if code, b := named(http.MethodPost, "/v1/framework/kb.memory",
			map[string]any{"title": "the app's note", "content": "the private draft of the app", "kind": "note", "owner": "app:A/bob"}); code != http.StatusCreated {
			t.Fatalf("the application's own memory: %d %s", code, b)
		}
		titles := func(raw []byte) map[string]bool {
			var out struct {
				Hits []struct {
					Title string `json:"title"`
				} `json:"hits"`
			}
			_ = json.Unmarshal(raw, &out)
			m := map[string]bool{}
			for _, h := range out.Hits {
				m[h.Title] = true
			}
			return m
		}
		q := map[string]any{"query": "private draft"}
		_, raw := key(http.MethodPost, "/v1/knowledge/search", q)
		if got := titles(raw); !got["the key's note"] || got["the app's note"] {
			t.Fatalf("bob's key finds its own and not the application's: %v", got)
		}
		_, raw = named(http.MethodPost, "/v1/knowledge/search", q)
		if got := titles(raw); got["the key's note"] || !got["the app's note"] {
			t.Fatalf("the application named bob finds its own and not bob's key's: %v", got)
		}
	})
}
