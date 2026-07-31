package engine

// typed_wire_test.go — the two ledgers that make this plane a GATE instead of
// a paragraph.
//
// SERVED: every route here is a typed op — there is no untyped route at all —
// and TestEveryRouteIsTyped holds that at zero, so a raw handler added
// tomorrow goes red rather than shipping schema-less.
//
// REFUSED: the product's authored intent (the 22-path spec hanzoai/openapi
// deleted as unserved in d86248f) named whole families this product's server
// does not answer — GPU clusters, jobs, Ray, pipelines, fleet GPU inventory,
// serve-endpoint CRUD. Those get NO route — a smaller true surface, never a
// bigger fake one — and intentRefused pins each family with the reason,
// measured against the live router (404, absent from the document), so
// reviving one is a deliberate edit.
//
// The fake upstream below is not invented: every status and body shape was
// MEASURED against a live hanzo-server (the hanzoai/engine binary serving a
// real model on this box) before this subsystem was written — the plain-text
// /health, the OpenAI-style model list with its load-status extension, the
// POST-read /v1/models/status, the /v1/system/info device inventory with the
// build's git revision.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
)

// intentRefused is the CLOSED ledger of authored-intent families this plane
// deliberately does NOT serve, keyed by a representative path from the
// deleted spec (relative to /v1/engine), plus the product's own admin surface
// that a shared runtime cannot expose per-org.
var intentRefused = map[string]string{
	// Cluster-manager surface the authored spec carried and this product (the
	// hanzoai/engine serving runtime) has never served — each family lives on
	// the cluster plane where it is real.
	"/v1/engine/clusters":        "GPU cluster registration is the cluster plane: /v1/clusters (apps/visor) merges managed clusters with the BYO fleet registry; the engine is the serving runtime, not a cluster manager",
	"/v1/engine/jobs":            "training job orchestration is /v1/train/jobs (apps/ml, TrainJob CRD); the engine runs no job queue",
	"/v1/engine/ray/clusters":    "no Ray operator backs the fleet; the engine is a single-process runtime, not a Ray head",
	"/v1/engine/pipelines":       "ML pipeline orchestration has no backend behind this product; the engine executes inference, not DAGs",
	"/v1/engine/gpus":            "fleet-wide GPU inventory needs the cluster plane; the engine reports only its own host's devices, served at /v1/engine/system",
	"/v1/engine/serve/endpoints": "serving-endpoint CRUD with autoscaling is /v1/ml/models (kserve InferenceService); the engine's own serve table is the /v1/engine/models lens",
	// Product surface that exists upstream but is refused HERE: the engine
	// deployment is ONE shared runtime with no per-org primitive, so its
	// mutations are platform operations, not tenant ops.
	"/v1/engine/models/unload":  "load/unload/reload/tune/requantize mutate the one shared runtime — an org-scoped route would hand each tenant every other tenant's availability; mutations wait for per-org engine instances",
	"/v1/engine/system/doctor":  "the doctor runs load diagnostics on shared serving capacity; an org-triggered benchmark is a denial lever, not a read",
	"/v1/engine/chat":           "inference is the fleet's ONE metered door — the OpenAI-compatible /v1 surface (apps/ai + the zen claim); a second completion door here would split billing",
}

// ── fake upstream: the measured engine wire ─────────────────────────────────

// upstreamCall records one request the fake engine saw.
type upstreamCall struct {
	method, path, query, auth string
	body                      []byte
}

// fakeEngine is a minimal in-memory hanzo-server speaking the measured wire:
// /health (plain text), /v1/models (list envelope with load status),
// /v1/models/status (POST read), /v1/system/info (host inventory).
type fakeEngine struct {
	mu    sync.Mutex
	calls []upstreamCall
	deny  bool // refuse the platform credential (401) when set
}

const (
	fakeModel    = "Qwen/Qwen3-4B"
	fakeRevision = "4a14d174d8474d9c2d3f3e69b90830c6bb2f20fc"
)

func (f *fakeEngine) record(r *http.Request) []byte {
	body, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, upstreamCall{
		method: r.Method, path: r.URL.Path, query: r.URL.RawQuery,
		auth: r.Header.Get("Authorization"), body: body,
	})
	return body
}

func (f *fakeEngine) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		body := f.record(r)
		if f.deny {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid api key"}`))
			return
		}
		js := func(status int, v any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(v)
		}
		switch {
		case r.URL.Path == "/health" && r.Method == http.MethodGet:
			// The live server answers plain text, not JSON.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("OK"))
		case r.URL.Path == "/v1/models" && r.Method == http.MethodGet:
			js(200, map[string]any{"object": "list", "data": []any{
				map[string]any{"id": "default", "object": "model", "created": 1785366657, "owned_by": "local"},
				map[string]any{"id": fakeModel, "object": "model", "created": 1785366657, "owned_by": "local", "status": "loaded"},
			}})
		case r.URL.Path == "/v1/models/status" && r.Method == http.MethodPost:
			var in struct {
				Model string `json:"model_id"`
			}
			if json.Unmarshal(body, &in) != nil || in.Model == "" {
				// The live server's extractor rejection is plain text.
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte("Failed to deserialize the JSON body into the target type: missing field `model_id`"))
				return
			}
			status := "not_found"
			if in.Model == fakeModel {
				status = "loaded"
			}
			js(200, map[string]any{"model_id": in.Model, "status": status})
		case r.URL.Path == "/v1/system/info" && r.Method == http.MethodGet:
			js(200, map[string]any{
				"os": "Ubuntu", "kernel": "6.17.0-1029-nvidia",
				"cpu":    map[string]any{"brand": "Cortex-A725", "logical_cores": 20, "physical_cores": 20},
				"memory": map[string]any{"total_bytes": 130662936576, "available_bytes": 98503725056},
				"devices": []any{
					map[string]any{"kind": "cpu", "ordinal": nil, "name": "Cortex-A725", "total_memory_bytes": 130662936576},
					map[string]any{"kind": "cuda", "ordinal": 0, "total_memory_bytes": 111063496089, "available_memory_bytes": 83725830144, "compute_capability": []int{12, 1}, "flash_attn_compatible": true, "unified_memory": true},
				},
				"build":         map[string]any{"cuda": true, "metal": false, "flash_attn": true, "git_revision": fakeRevision},
				"hf_cache_path": "/home/e2e/.cache/huggingface/hub",
			})
		default:
			js(404, map[string]any{"error": "not found"})
		}
	})
}

// harness mounts the app against a fake upstream and returns both.
func harness(t *testing.T) (*zip.App, *fakeEngine) {
	t.Helper()
	f := &fakeEngine{}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	t.Setenv("ENGINE_UPSTREAM", srv.URL)
	t.Setenv("ENGINE_API_KEY", "svc-key-test")

	app := zip.New(zip.Config{Logger: luxlog.New("enginetest"), DisableStartupMessage: true})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("enginetest"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app, f
}

// do drives one request with minted identity headers (as SanitizeIdentity
// would set them): user!="" makes a validated principal.
func do(t *testing.T, app *zip.App, method, path, user, org, body string) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test(%s %s): %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// ── the served ledger ───────────────────────────────────────────────────────

// TestEveryRouteIsTyped holds the untyped count at ZERO: every operation this
// plane serves is a typed op, so each carries schema, prose, an MCP tool, a
// CLI command and an SDK method. A raw route added tomorrow fails here.
func TestEveryRouteIsTyped(t *testing.T) {
	app, _ := harness(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "engine", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	served := map[string]bool{}
	for path, item := range doc.Paths {
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	if len(served) == 0 {
		t.Fatal("engine serves no operations at all — the mount did not register")
	}
	var untyped []string
	for key := range served {
		if _, ok := reg.Ops[key]; !ok {
			untyped = append(untyped, key)
		}
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("untyped operation(s) on a fully-typed plane: %s", strings.Join(untyped, ", "))
	}
	if len(reg.Ops) != len(served) {
		t.Errorf("%d typed ops but %d served operations — the two projections disagree", len(reg.Ops), len(served))
	}
}

// TestIntentStaysRefused measures the refusal ledger: every named family
// answers a route-level 404 on the live router and appears nowhere in the
// published document. A route added at one of these paths makes this fail,
// which is the point — reviving a family is a decision recorded by editing
// the ledger, never a drive-by.
func TestIntentStaysRefused(t *testing.T) {
	app, _ := harness(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "engine", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	for path, reason := range intentRefused {
		if reason == "" {
			t.Errorf("refused path %q carries no reason", path)
		}
		status, _ := do(t, app, http.MethodGet, path, "u1", "acme", "")
		if status != http.StatusNotFound {
			t.Errorf("GET %s = %d, want the router's 404 — the refused family answered", path, status)
		}
		for docPath := range doc.Paths {
			if docPath == path || strings.HasPrefix(docPath, path+"/") {
				t.Errorf("document publishes %s, which the ledger refuses", docPath)
			}
		}
	}
}

// ── the gate (the bar) ──────────────────────────────────────────────────────

// A request with no validated principal is refused 403 and NEVER reaches the
// upstream — the forged-X-Org-Id path is dead.
func TestNoPrincipalIs403AndNoUpstreamByte(t *testing.T) {
	app, f := harness(t)
	for _, path := range []string{
		"/v1/engine/status",
		"/v1/engine/models",
		"/v1/engine/model?model=" + fakeModel,
		"/v1/engine/system",
	} {
		status, body := do(t, app, http.MethodGet, path, "", "victim", "")
		if status != http.StatusForbidden {
			t.Errorf("GET %s without principal = %d, want 403; body=%s", path, status, body)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 0 {
		t.Errorf("upstream was contacted %d time(s) for unvalidated requests; must be 0", len(f.calls))
	}
}

// ── the lens over the measured wire ─────────────────────────────────────────

// The model table relays verbatim — the server's own list envelope with its
// load-status extension — and one model's state reads through the POST seam
// while staying a GET on this plane.
func TestModelsLensRelaysTheMeasuredWire(t *testing.T) {
	app, f := harness(t)

	status, body := do(t, app, http.MethodGet, "/v1/engine/models", "u1", "acme", "")
	if status != http.StatusOK || !strings.Contains(body, `"object":"list"`) || !strings.Contains(body, `"status":"loaded"`) {
		t.Fatalf("models = %d %s", status, body)
	}

	status, body = do(t, app, http.MethodGet, "/v1/engine/model?model="+fakeModel, "u1", "acme", "")
	if status != http.StatusOK || !strings.Contains(body, `"status":"loaded"`) {
		t.Fatalf("model(loaded) = %d %s", status, body)
	}
	status, body = do(t, app, http.MethodGet, "/v1/engine/model?model=ghost", "u1", "acme", "")
	if status != http.StatusOK || !strings.Contains(body, `"status":"not_found"`) {
		t.Fatalf("model(unknown) = %d %s — the product's own not_found must relay", status, body)
	}

	// The id moved from our query string into the upstream body: the seam is
	// POST /v1/models/status carrying model_id.
	f.mu.Lock()
	defer f.mu.Unlock()
	var seam bool
	for _, c := range f.calls {
		if c.method == http.MethodPost && c.path == "/v1/models/status" && strings.Contains(string(c.body), `"model_id":"`+fakeModel+`"`) {
			seam = true
		}
	}
	if !seam {
		t.Error("no upstream POST /v1/models/status carried the model id")
	}
}

// An empty model id is refused before any upstream byte.
func TestModelRequiresAnId(t *testing.T) {
	app, f := harness(t)
	status, _ := do(t, app, http.MethodGet, "/v1/engine/model", "u1", "acme", "")
	if status != http.StatusBadRequest {
		t.Fatalf("model without id = %d, want 400", status)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 0 {
		t.Errorf("upstream was contacted %d time(s) for an invalid request; must be 0", len(f.calls))
	}
}

// The host inventory relays verbatim: the accelerator devices and the build's
// capability flags reach the caller exactly as the engine reports them.
func TestSystemRelaysDeviceInventory(t *testing.T) {
	app, _ := harness(t)
	status, body := do(t, app, http.MethodGet, "/v1/engine/system", "u1", "acme", "")
	if status != http.StatusOK {
		t.Fatalf("system = %d %s", status, body)
	}
	for _, want := range []string{`"kind":"cuda"`, `"compute_capability":[12,1]`, `"git_revision":"` + fakeRevision + `"`} {
		if !strings.Contains(body, want) {
			t.Errorf("system relay lost %s: %s", want, body)
		}
	}
}

// ── the relay and the credential ────────────────────────────────────────────

// The upstream payload reaches the caller VERBATIM — including an int64
// beyond float64, the case that fails silently if a relay ever decodes into
// `any` and re-encodes.
func TestRelayIsVerbatim(t *testing.T) {
	if got, _ := (engineResult{raw: json.RawMessage(`{"total_memory_bytes":9007199254740993}`)}).MarshalJSON(); string(got) != `{"total_memory_bytes":9007199254740993}` {
		t.Fatalf("relay mangled the payload: %s", got)
	}
	if got, _ := (engineResult{}).MarshalJSON(); string(got) != `{}` {
		t.Fatalf("empty relay = %s, want {}", got)
	}
}

// Every upstream call carries the platform credential as a bearer token and
// the credential never appears in a response body.
func TestCredentialRidesUpstreamOnly(t *testing.T) {
	app, f := harness(t)
	status, body := do(t, app, http.MethodGet, "/v1/engine/models", "u1", "acme", "")
	if status != http.StatusOK {
		t.Fatalf("models = %d %s", status, body)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("no upstream calls recorded")
	}
	for _, c := range f.calls {
		if c.auth != "Bearer svc-key-test" {
			t.Errorf("upstream %s %s carried Authorization %q, want the platform bearer", c.method, c.path, c.auth)
		}
	}
	if strings.Contains(body, "svc-key-test") {
		t.Errorf("platform credential leaked into a response: %s", body)
	}
}

// An upstream that refuses the platform credential is a deployment fault: the
// caller sees 503, never a 401/403 that would read as their own auth failing.
func TestUpstreamCredentialRefusalIs503(t *testing.T) {
	app, f := harness(t)
	f.mu.Lock()
	f.deny = true
	f.mu.Unlock()
	status, body := do(t, app, http.MethodGet, "/v1/engine/models", "u1", "acme", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("credential refusal surfaced as %d (%s), want 503", status, body)
	}
	if strings.Contains(body, "invalid api key") {
		t.Errorf("upstream credential detail leaked to the caller: %s", body)
	}
}

// The reachability lens answers honestly in both directions: the build
// revision when the runtime is up, reachable:false — not an error — when
// nothing listens.
func TestStatusIsAnHonestLens(t *testing.T) {
	app, _ := harness(t)
	status, body := do(t, app, http.MethodGet, "/v1/engine/status", "u1", "acme", "")
	if status != http.StatusOK || !strings.Contains(body, `"reachable":true`) || !strings.Contains(body, fakeRevision) {
		t.Fatalf("status(up) = %d %s", status, body)
	}

	down := zip.New(zip.Config{Logger: luxlog.New("enginetest"), DisableStartupMessage: true})
	t.Setenv("ENGINE_UPSTREAM", "http://127.0.0.1:1") // nothing listens
	if err := Mount(down, cloud.Deps{Logger: luxlog.New("enginetest"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	status, body = do(t, down, http.MethodGet, "/v1/engine/status", "u1", "acme", "")
	if status != http.StatusOK || !strings.Contains(body, `"reachable":false`) {
		t.Fatalf("status(down) = %d %s, want an honest reachable:false", status, body)
	}
}
