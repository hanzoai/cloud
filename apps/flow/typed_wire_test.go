package flow

// typed_wire_test.go — the two ledgers that make this plane a GATE instead of
// a paragraph.
//
// SERVED: every route here is a typed op — there is no untyped route at all —
// and TestEveryRouteIsTyped holds that at zero, so a raw handler added
// tomorrow goes red rather than shipping schema-less.
//
// REFUSED: the product's authored intent (the 87-path spec hanzoai/openapi
// deleted as unserved in d86248f) named whole families this product's server
// does not answer. Those get NO route — a smaller true surface, never a bigger
// fake one — and intentRefused pins each family with the reason, measured
// against the live router (404, absent from the document), so reviving one is
// a deliberate edit.
//
// The fake upstream below is not invented: every status and body shape was
// MEASURED against a live flow v1.8.2 server (x-api-key auth) before this
// subsystem was written — the paged list envelope, the 201 create, the delete
// message, the run response, the vertex-build records, the trailing-slash
// collection paths.

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/spec"
)

// intentRefused is the CLOSED ledger of authored-intent families this plane
// deliberately does NOT serve, keyed by a representative path from the deleted
// spec (relative to /v1/flow). The reason is the same for every family — the
// product's server does not answer it — plus where each family actually lives
// when it lives anywhere.
var intentRefused = map[string]string{
	// Activepieces-shaped surface the authored spec carried and this product
	// (hanzoai/flow, the visual AI workflow builder) has never served: providers
	// are not its component model, app-connections/triggers/store-entries are
	// not its primitives. The goja provider runtime lives at /v1/automations.
	"/v1/flow/providers":       "the product has no providers registry; the provider runtime is apps/automations",
	"/v1/flow/app-connections": "the product has no app-connections primitive; connectors live at /v1/provider",
	"/v1/flow/trigger-events":  "the product has no trigger-event queue; automations owns triggers",
	"/v1/flow/store-entries":   "the product has no key-value store surface; provisioning owns /v1/kv",
	"/v1/flow/templates":       "the product's starter examples are not a template CRUD; /v1/template is the fleet's template plane",
	// Product families that exist upstream but are NOT proven against a real
	// backend yet, so they get no route until they are: the honest-slice bar.
	"/v1/flow/folders":      "upstream projects/folders exist but only as this plane's INTERNAL tenant boundary — exposing them would let a caller address another org's project",
	"/v1/flow/users":        "the product's user table is service-internal; platform identity is IAM, and mapping org members onto product users is unbuilt",
	"/v1/flow/ai-providers": "the product's model-provider config is deployment-global today; per-org provider custody needs the integrations KMS client first",
	"/v1/flow/webhooks":     "webhook trigger delivery needs a public ingress contract (signature, replay) that is not designed yet",
	"/v1/flow/mcp":          "the product's MCP server surface overlaps apps/tools' catalog; composing them is task-level work, not a relay",
}

// ── fake upstream: the measured flow wire ───────────────────────────────────

// upstreamCall records one request the fake flow service saw.
type upstreamCall struct {
	method, path, query, apikey, auth string
	body                              []byte
}

// fakeFlow is a minimal in-memory flow service speaking the measured wire:
// /v1/projects/ (list, create), /v1/flows/ (paged list, create), /v1/flows/{id}
// (get, patch, delete), /v1/run/{id}, /v1/monitor/builds.
type fakeFlow struct {
	mu       sync.Mutex
	calls    []upstreamCall
	projects map[string]string         // scope+name (or name when no bearer) → id
	flows    map[string]map[string]any // id → FlowRead-ish record
	nextID   int
	deny     bool // refuse the platform credential (401) when set
}

func (f *fakeFlow) record(r *http.Request) []byte {
	body, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, upstreamCall{
		method: r.Method, path: r.URL.Path, query: r.URL.RawQuery,
		apikey: r.Header.Get("x-api-key"), auth: r.Header.Get("Authorization"), body: body,
	})
	return body
}

func (f *fakeFlow) id(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%08x-0000-4000-8000-%012x", len(prefix), f.nextID)
}

func (f *fakeFlow) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		body := f.record(r)
		if f.deny {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"detail":"Invalid API key"}`))
			return
		}
		js := func(status int, v any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(v)
		}
		authScope := r.Header.Get("Authorization")
		projKey := func(name string) string {
			if authScope == "" {
				return name
			}
			return authScope + "|" + name
		}
		switch {
		case r.URL.Path == "/health":
			js(200, map[string]string{"status": "ok"})
		case r.URL.Path == "/v1/version":
			js(200, map[string]string{"version": "1.8.2", "package": "Flow"})
		case r.URL.Path == "/v1/projects/" && r.Method == http.MethodGet:
			rows := []map[string]any{}
			for k, id := range f.projects {
				name := k
				if authScope != "" {
					if !strings.HasPrefix(k, authScope+"|") {
						continue
					}
					name = strings.TrimPrefix(k, authScope+"|")
				} else if strings.Contains(k, "|") {
					continue
				}
				rows = append(rows, map[string]any{"id": id, "name": name, "parent_id": nil})
			}
			js(200, rows)
		case r.URL.Path == "/v1/projects/" && r.Method == http.MethodPost:
			var in struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(body, &in)
			id := f.id("proj")
			f.projects[projKey(in.Name)] = id
			js(201, map[string]any{"id": id, "name": in.Name, "parent_id": nil})
		case r.URL.Path == "/v1/flows/" && r.Method == http.MethodPost:
			var in map[string]any
			_ = json.Unmarshal(body, &in)
			id := f.id("flow")
			in["id"] = id
			f.flows[id] = in
			js(201, in)
		case r.URL.Path == "/v1/flows/" && r.Method == http.MethodGet:
			folder := r.URL.Query().Get("folder_id")
			items := []map[string]any{}
			for _, rec := range f.flows {
				if rec["folder_id"] == folder {
					items = append(items, rec)
				}
			}
			js(200, map[string]any{"items": items, "total": len(items), "page": 1, "size": 50, "pages": 1})
		case strings.HasPrefix(r.URL.Path, "/v1/flows/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/flows/")
			rec, ok := f.flows[id]
			if !ok {
				js(404, map[string]string{"detail": "Flow not found"})
				return
			}
			switch r.Method {
			case http.MethodGet:
				js(200, rec)
			case http.MethodPatch:
				var patch map[string]any
				_ = json.Unmarshal(body, &patch)
				maps.Copy(rec, patch)
				js(200, rec)
			case http.MethodDelete:
				delete(f.flows, id)
				js(200, map[string]string{"message": "Flow deleted successfully"})
			}
		case strings.HasPrefix(r.URL.Path, "/v1/run/"):
			js(200, map[string]any{
				"session_id": "sess-1",
				"outputs": []any{map[string]any{
					"inputs":  map[string]any{"input_value": "ping"},
					"outputs": []any{map[string]any{"results": map[string]any{"message": map[string]any{"text": "ping"}}}},
				}},
			})
		case r.URL.Path == "/v1/monitor/builds":
			js(200, map[string]any{"vertex_builds": map[string]any{}})
		default:
			js(404, map[string]string{"detail": "Not Found"})
		}
	})
}

// compose installs what a host installs. A subsystem never installs cloud.Bridge
// (routes() says why): the program's composer installs it once at the root, and
// in a test the test is the composer, so it owes the same install. A test that
// skips it does not test a stricter program — it tests one where every op that
// reads the principal answers a refusal production can never produce.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

// harness mounts the app against a fake upstream and returns both.
func harness(t *testing.T) (*zip.App, *fakeFlow) {
	t.Helper()
	f := &fakeFlow{projects: map[string]string{}, flows: map[string]map[string]any{}}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	t.Setenv("FLOW_UPSTREAM", srv.URL)
	t.Setenv("FLOW_API_KEY", "svc-key-test")

	app := zip.New(zip.Config{Logger: luxlog.New("flowtest"), DisableStartupMessage: true})
	compose(app)
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
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
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
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
	doc, err := spec.Of(app, spec.Info{Title: "flow", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := spec.Typed(app)
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
		t.Fatal("flow serves no operations at all — the mount did not register")
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
// which is the point — reviving a family is a decision recorded by editing the
// ledger, never a drive-by.
func TestIntentStaysRefused(t *testing.T) {
	app, _ := harness(t)
	doc, err := spec.Of(app, spec.Info{Title: "flow", Version: "v1"})
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

// ── tenancy (the bar) ───────────────────────────────────────────────────────

// A request with no validated principal is refused 401 and NEVER reaches the
// upstream — the forged-X-Org-Id path is dead.
func TestNoPrincipalIs401AndNoUpstreamByte(t *testing.T) {
	app, f := harness(t)
	for _, probe := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/flow/workflows", ""},
		{http.MethodPost, "/v1/flow/workflows", `{"name":"x"}`},
		{http.MethodGet, "/v1/flow/workflows/00000000-0000-4000-8000-000000000001", ""},
		{http.MethodPost, "/v1/flow/runs", `{"workflow":"00000000-0000-4000-8000-000000000001"}`},
		{http.MethodGet, "/v1/flow/status", ""},
	} {
		status, body := do(t, app, probe.method, probe.path, "", "victim", probe.body)
		if status != http.StatusUnauthorized {
			t.Errorf("%s %s without principal = %d, want 401; body=%s", probe.method, probe.path, status, body)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 0 {
		t.Errorf("upstream was contacted %d time(s) for unvalidated requests; must be 0", len(f.calls))
	}
}

// Each org's workflows live in that org's project: a create pins the project
// id SERVER-SIDE (there is no In field to say otherwise), the list is scoped
// to it, and another org's workflow id answers 404 — indistinguishable from a
// nonexistent one — for read, update, delete, run, and run records alike.
func TestWorkflowsAreOrgScoped(t *testing.T) {
	app, f := harness(t)

	// acme creates a workflow.
	status, body := do(t, app, http.MethodPost, "/v1/flow/workflows", "u1", "acme", `{"name":"wf-a"}`)
	if status != http.StatusOK {
		t.Fatalf("create = %d body=%s", status, body)
	}
	var created struct {
		ID     string `json:"id"`
		Folder string `json:"folder_id"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil || created.ID == "" {
		t.Fatalf("create relay unparseable: %s", body)
	}
	f.mu.Lock()
	acmeProject := f.projects["acme"]
	f.mu.Unlock()
	if acmeProject == "" || created.Folder != acmeProject {
		t.Fatalf("create pinned folder %q, want acme's project %q", created.Folder, acmeProject)
	}

	// acme sees it; the list rode acme's folder_id upstream.
	status, body = do(t, app, http.MethodGet, "/v1/flow/workflows", "u1", "acme", "")
	if status != http.StatusOK || !strings.Contains(body, created.ID) {
		t.Fatalf("acme list = %d %s", status, body)
	}

	// globex sees an empty page, and every foreign access answers 404.
	status, body = do(t, app, http.MethodGet, "/v1/flow/workflows", "u2", "globex", "")
	if status != http.StatusOK || strings.Contains(body, created.ID) {
		t.Fatalf("globex list leaked acme's workflow: %d %s", status, body)
	}
	for _, probe := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/flow/workflows/" + created.ID, ""},
		{http.MethodPatch, "/v1/flow/workflows/" + created.ID, `{"name":"stolen"}`},
		{http.MethodDelete, "/v1/flow/workflows/" + created.ID, ""},
		{http.MethodPost, "/v1/flow/runs", `{"workflow":"` + created.ID + `","input":"hi"}`},
		{http.MethodGet, "/v1/flow/runs?workflow=" + created.ID, ""},
	} {
		status, body = do(t, app, probe.method, probe.path, "u2", "globex", probe.body)
		if status != http.StatusNotFound {
			t.Errorf("globex %s %s = %d, want 404; body=%s", probe.method, probe.path, status, body)
		}
	}

	// And acme's own full loop stays green: read, update, run, records, delete.
	if status, body = do(t, app, http.MethodGet, "/v1/flow/workflows/"+created.ID, "u1", "acme", ""); status != http.StatusOK {
		t.Fatalf("acme get = %d %s", status, body)
	}
	if status, body = do(t, app, http.MethodPatch, "/v1/flow/workflows/"+created.ID, "u1", "acme", `{"description":"mine"}`); status != http.StatusOK {
		t.Fatalf("acme patch = %d %s", status, body)
	}
	if status, body = do(t, app, http.MethodPost, "/v1/flow/runs", "u1", "acme", `{"workflow":"`+created.ID+`","input":"ping"}`); status != http.StatusOK || !strings.Contains(body, "session_id") {
		t.Fatalf("acme run = %d %s", status, body)
	}
	if status, body = do(t, app, http.MethodGet, "/v1/flow/runs?workflow="+created.ID, "u1", "acme", ""); status != http.StatusOK || !strings.Contains(body, "vertex_builds") {
		t.Fatalf("acme runs = %d %s", status, body)
	}
	if status, body = do(t, app, http.MethodDelete, "/v1/flow/workflows/"+created.ID, "u1", "acme", ""); status != http.StatusOK || !strings.Contains(body, "deleted") {
		t.Fatalf("acme delete = %d %s", status, body)
	}
}

// ── the relay and the credential ────────────────────────────────────────────

// The upstream payload reaches the caller VERBATIM — including an int64 beyond
// float64, the case that fails silently if a relay ever decodes into `any` and
// re-encodes.
func TestRelayIsVerbatim(t *testing.T) {
	if got, _ := (flowResult{raw: json.RawMessage(`{"id":9007199254740993}`)}).MarshalJSON(); string(got) != `{"id":9007199254740993}` {
		t.Fatalf("relay mangled the payload: %s", got)
	}
	if got, _ := (flowResult{}).MarshalJSON(); string(got) != `{}` {
		t.Fatalf("empty relay = %s, want {}", got)
	}
}

// Every upstream call carries the platform credential on x-api-key and the
// credential never appears in a response body.
func TestCredentialRidesUpstreamOnly(t *testing.T) {
	app, f := harness(t)
	status, body := do(t, app, http.MethodGet, "/v1/flow/workflows", "u1", "acme", "")
	if status != http.StatusOK {
		t.Fatalf("list = %d %s", status, body)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("no upstream calls recorded")
	}
	for _, c := range f.calls {
		if c.apikey != "svc-key-test" {
			t.Errorf("upstream %s %s carried x-api-key %q, want the platform credential", c.method, c.path, c.apikey)
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
	status, body := do(t, app, http.MethodGet, "/v1/flow/workflows", "u1", "acme", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("credential refusal surfaced as %d (%s), want 503", status, body)
	}
	if strings.Contains(body, "Invalid API key") {
		t.Errorf("upstream credential detail leaked to the caller: %s", body)
	}
}

// The reachability lens answers honestly in both directions: version when the
// product is up, reachable:false — not an error — when nothing listens.
func TestStatusIsAnHonestLens(t *testing.T) {
	app, _ := harness(t)
	status, body := do(t, app, http.MethodGet, "/v1/flow/status", "u1", "acme", "")
	if status != http.StatusOK || !strings.Contains(body, `"reachable":true`) || !strings.Contains(body, "1.8.2") {
		t.Fatalf("status(up) = %d %s", status, body)
	}

	down := zip.New(zip.Config{Logger: luxlog.New("flowtest"), DisableStartupMessage: true})
	compose(down)
	t.Setenv("FLOW_UPSTREAM", "http://127.0.0.1:1") // nothing listens
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(down, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	status, body = do(t, down, http.MethodGet, "/v1/flow/status", "u1", "acme", "")
	if status != http.StatusOK || !strings.Contains(body, `"reachable":false`) {
		t.Fatalf("status(down) = %d %s, want an honest reachable:false", status, body)
	}
}

// A workflow id that is not a UUID never reaches the upstream: the path is the
// addressing authority and its shape is validated before any splice.
func TestIDShapeIsValidatedBeforeUpstream(t *testing.T) {
	app, f := harness(t)
	status, _ := do(t, app, http.MethodPost, "/v1/flow/runs", "u1", "acme", `{"workflow":"../../etc/passwd"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("hostile id = %d, want 400", status)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.Contains(c.path, "passwd") {
			t.Fatalf("hostile id reached the upstream: %s", c.path)
		}
	}
}

// defaultUpstream targets the in-cluster flow Service on port 80 (flowweb :8080
// -> FastAPI :7860), never the pod-internal :7860 port that Service does not expose.
func TestDefaultUpstreamUsesServicePort80(t *testing.T) {
	if defaultUpstream != "http://flow.hanzo.svc.cluster.local" {
		t.Fatalf("defaultUpstream = %q, want http://flow.hanzo.svc.cluster.local", defaultUpstream)
	}
}

// When the caller carries a validated Hanzo IAM JWT bearer, every upstream call
// relays Authorization: Bearer <jwt> instead of x-api-key, and per-user project
// caching keeps two users in the same org from colliding on project UUIDs.
func TestCallerBearerRelaysUpstreamAndScopesProjectCache(t *testing.T) {
	app, f := harness(t)
	t.Setenv("FLOW_API_KEY", "")

	doBearer := func(method, path, user, org, jwt, body string) (int, string) {
		t.Helper()
		var rdr io.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, rdr)
		req.Header.Set("X-User-Id", user)
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("Authorization", "Bearer "+jwt)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("Test(%s %s): %v", method, path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	st1, b1 := doBearer(http.MethodPost, "/v1/flow/workflows", "alice", "acme", "hdr.alice.sig", `{"name":"alice-wf"}`)
	if st1 != http.StatusOK {
		t.Fatalf("alice create = %d %s", st1, b1)
	}
	st2, b2 := doBearer(http.MethodPost, "/v1/flow/workflows", "bob", "acme", "hdr.bob.sig", `{"name":"bob-wf"}`)
	if st2 != http.StatusOK {
		t.Fatalf("bob create = %d %s", st2, b2)
	}

	var r1, r2 struct {
		ID     string `json:"id"`
		Folder string `json:"folder_id"`
	}
	if err := json.Unmarshal([]byte(b1), &r1); err != nil || r1.Folder == "" {
		t.Fatalf("alice response unparseable: %s", b1)
	}
	if err := json.Unmarshal([]byte(b2), &r2); err != nil || r2.Folder == "" {
		t.Fatalf("bob response unparseable: %s", b2)
	}
	if r1.Folder == r2.Folder {
		t.Fatalf("alice and bob collided on the same cached project UUID %q", r1.Folder)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	var sawAlice, sawBob bool
	for _, c := range f.calls {
		if c.apikey != "" {
			t.Errorf("upstream %s %s sent x-api-key %q when caller bearer was present", c.method, c.path, c.apikey)
		}
		if c.auth == "Bearer hdr.alice.sig" {
			sawAlice = true
		}
		if c.auth == "Bearer hdr.bob.sig" {
			sawBob = true
		}
	}
	if !sawAlice || !sawBob {
		t.Fatalf("expected relayed bearers for both alice (%v) and bob (%v)", sawAlice, sawBob)
	}
}

// Transient upstream 503 blips on idempotent GETs (project lookup and workflow
// listing) must retry and recover cleanly instead of breaking /flow for users.
func TestTransientGet503RetriesAndRecovers(t *testing.T) {
	prev := retryBackoffs
	retryBackoffs = []time.Duration{5 * time.Millisecond, 10 * time.Millisecond}
	t.Cleanup(func() { retryBackoffs = prev })

	var mu sync.Mutex
	projAttempts := 0
	flowAttempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/projects/":
			projAttempts++
			if projAttempts <= 2 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"detail":"restarting"}`))
				return
			}
			_, _ = w.Write([]byte(`[{"id":"proj-1","name":"acme"}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/flows/":
			flowAttempts++
			if flowAttempts == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"detail":"transient"}`))
				return
			}
			_, _ = w.Write([]byte(`{"items":[],"total":0,"page":1,"size":50,"pages":0}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	app := zip.New(zip.Config{Logger: luxlog.New("flowtest"), DisableStartupMessage: true})
	compose(app)
	t.Setenv("FLOW_UPSTREAM", srv.URL)
	t.Setenv("FLOW_API_KEY", "svc-key-test")
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use: %v", err)
	}

	status, body := do(t, app, http.MethodGet, "/v1/flow/workflows", "u1", "acme", "")
	if status != http.StatusOK {
		t.Fatalf("transient 503 did not recover: status=%d body=%s", status, body)
	}
	if projAttempts != 3 || flowAttempts != 2 {
		t.Fatalf("expected 3 project attempts and 2 flow attempts, got proj=%d flow=%d", projAttempts, flowAttempts)
	}
}

// Non-idempotent POST requests (like running a workflow) must NEVER be retried
// on upstream 503, to avoid double-execution or double-billing.
func TestPostRunNeverRetriesOn503(t *testing.T) {
	prev := retryBackoffs
	retryBackoffs = []time.Duration{5 * time.Millisecond, 10 * time.Millisecond}
	t.Cleanup(func() { retryBackoffs = prev })

	wfID := "11111111-1111-4111-8111-111111111111"
	var mu sync.Mutex
	runAttempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/projects/":
			_, _ = w.Write([]byte(`[{"id":"proj-1","name":"acme"}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/flows/"+wfID:
			_, _ = w.Write([]byte(`{"id":"` + wfID + `","folder_id":"proj-1","name":"demo"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/run/"+wfID:
			runAttempts++
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"detail":"busy"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	app := zip.New(zip.Config{Logger: luxlog.New("flowtest"), DisableStartupMessage: true})
	compose(app)
	t.Setenv("FLOW_UPSTREAM", srv.URL)
	t.Setenv("FLOW_API_KEY", "svc-key-test")
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use: %v", err)
	}

	status, _ := do(t, app, http.MethodPost, "/v1/flow/runs", "u1", "acme", `{"workflow":"`+wfID+`","input":"hi"}`)
	if status != http.StatusBadGateway {
		t.Fatalf("upstream 503 on run = %d, want 502", status)
	}
	if runAttempts != 1 {
		t.Fatalf("POST /v1/run was retried (%d attempts); non-idempotent POST must run once", runAttempts)
	}
}

// Project resolution must coalesce concurrent requests for the same org and
// never hold a global mutex across network I/O that would stall other orgs.
func TestConcurrentProjectResolutionDoesNotBlockCachedOrg(t *testing.T) {
	gate := make(chan struct{})
	slowEntered := make(chan struct{}, 1)
	var mu sync.Mutex
	creates := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/projects/":
			_, _ = w.Write([]byte(`[{"id":"proj-fast","name":"fast"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/projects/":
			mu.Lock()
			creates++
			mu.Unlock()
			select {
			case slowEntered <- struct{}{}:
			default:
			}
			<-gate
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"proj-slow","name":"slow"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/flows/":
			_, _ = w.Write([]byte(`{"items":[],"total":0,"page":1,"size":50,"pages":0}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	app := zip.New(zip.Config{Logger: luxlog.New("flowtest"), DisableStartupMessage: true})
	compose(app)
	t.Setenv("FLOW_UPSTREAM", srv.URL)
	t.Setenv("FLOW_API_KEY", "svc-key-test")
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use: %v", err)
	}

	// Warm the cache for "fast".
	if st, body := do(t, app, http.MethodGet, "/v1/flow/workflows", "u1", "fast", ""); st != http.StatusOK {
		t.Fatalf("warm fast = %d %s", st, body)
	}

	// Start two concurrent requests for uncached "slow" that block in upstream POST /v1/projects/.
	var wg sync.WaitGroup
	slowStatuses := make([]int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			slowStatuses[idx], _ = do(t, app, http.MethodGet, "/v1/flow/workflows", "u2", "slow", "")
		}(i)
	}
	<-slowEntered

	// While "slow" is blocked on upstream I/O, "fast" must complete immediately.
	doneFast := make(chan int, 1)
	go func() {
		st, _ := do(t, app, http.MethodGet, "/v1/flow/workflows", "u1", "fast", "")
		doneFast <- st
	}()
	select {
	case st := <-doneFast:
		if st != http.StatusOK {
			t.Fatalf("fast while slow blocked = %d, want 200", st)
		}
	case <-time.After(1 * time.Second):
		close(gate)
		t.Fatal("cached org 'fast' was blocked while 'slow' waited on upstream network I/O")
	}

	close(gate)
	wg.Wait()
	if slowStatuses[0] != http.StatusOK || slowStatuses[1] != http.StatusOK {
		t.Fatalf("slow statuses = %v, want [200 200]", slowStatuses)
	}
	mu.Lock()
	defer mu.Unlock()
	if creates != 1 {
		t.Fatalf("concurrent requests for same org triggered %d project creates, want 1", creates)
	}
}

// When upstream flow rejects a caller's IAM JWT bearer (e.g. sibling issuer
// https://hanzo.id vs https://hanzo.ai) with 401 and FLOW_API_KEY is set,
// cloud must fall back cleanly to x-api-key instead of returning 503 to /flow
// users, while still scoping all reads/writes to the caller's validated org.
func TestCallerBearerRefusedFallsBackToPlatformAPIKey(t *testing.T) {
	var mu sync.Mutex
	var sawBearer401, sawAPIKey200 bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "" {
			sawBearer401 = true
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"detail":"Could not validate credentials"}`))
			return
		}
		if r.Header.Get("x-api-key") != "svc-fallback-key" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"detail":"Invalid API key"}`))
			return
		}
		sawAPIKey200 = true
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/projects/":
			_, _ = w.Write([]byte(`[{"id":"proj-org","name":"ericliu612"}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/flows/":
			if got := r.URL.Query().Get("folder_id"); got != "proj-org" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"items":[],"total":0,"page":1,"size":50,"pages":0}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	app := zip.New(zip.Config{Logger: luxlog.New("flowtest"), DisableStartupMessage: true})
	compose(app)
	t.Setenv("FLOW_UPSTREAM", srv.URL)
	t.Setenv("FLOW_API_KEY", "svc-fallback-key")
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/flow/workflows", nil)
	req.Header.Set("Authorization", "Bearer hdr.hanzo-id-web-jwt.sig")
	req.Header.Set("X-User-Id", "u-eric")
	req.Header.Set("X-Org-Id", "ericliu612")
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("GET /v1/flow/workflows: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", resp.StatusCode, body)
	}
	mu.Lock()
	defer mu.Unlock()
	if !sawBearer401 || !sawAPIKey200 {
		t.Fatalf("expected bearer 401 (%v) followed by x-api-key fallback (%v)", sawBearer401, sawAPIKey200)
	}
}
