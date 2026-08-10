package auto

// typed_wire_test.go — the two ledgers that make this plane a GATE instead of
// a paragraph.
//
// SERVED: every route here is a typed op — there is no untyped route at all —
// and TestEveryRouteIsTyped holds that at zero, so a raw handler added
// tomorrow goes red rather than shipping schema-less.
//
// REFUSED: the product's authored intent (the 50-path Activepieces-shaped
// spec hanzoai/openapi deleted as unserved in d86248f) named whole families
// the v2 product's server does not answer. Those get NO route — a smaller
// true surface, never a bigger fake one — and intentRefused pins each family
// with the reason, measured against the live router (404, absent from the
// document), so reviving one is a deliberate edit.
//
// The fake upstream below is not invented: every status and body shape was
// MEASURED against a live hanzoai/auto v2 server (base + tasksd, the real
// engine executing real runs) before this subsystem was written — the
// {"items"} envelopes, the 201 create, the 202 dispatch, the 204 delete, the
// 401 gateway-auth refusal, base's capitalized {"message"} error field.

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

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
)

// intentRefused is the CLOSED ledger of authored-intent families this plane
// deliberately does NOT serve, keyed by a representative path from the
// deleted spec (relative to /v1/auto), plus this plane's own capability
// refusals — each measured as a router-level 404.
var intentRefused = map[string]string{
	// Custody: the v2 product stores connection configs base64-at-rest (its
	// recorded pre-KMS seam). A tenant-facing secret-custody surface ships
	// only when the product's KMS DEK derivation lands — never before.
	"/v1/auto/app-connections":    "connection configs are base64-at-rest in the product today; the KMS DEK seam must land before cloud offers secret custody",
	"/v1/auto/connections":        "same custody bar as app-connections — the product's route exists but stores configs base64-at-rest, so no cloud door until KMS-backed",
	"/v1/auto/global-connections": "platform-shared credentials are an EE concept the v2 product does not have",
	// Triggers: the product serves list+fire, but its v1 API has NO route
	// that creates a trigger row — a door onto rows nothing can make is not
	// a working loop. Runs are started at /v1/auto/runs.
	"/v1/auto/triggers":       "the product's served API cannot create a trigger row yet; a fire endpoint over unmakeable rows is not a loop — start runs at /v1/auto/runs",
	"/v1/auto/trigger-events": "no trigger-event queue in the v2 product",
	"/v1/auto/trigger-runs":   "no trigger-run ledger in the v2 product; run records live at /v1/auto/runs",
	"/v1/auto/test-trigger":   "trigger testing needs the trigger family first",
	"/v1/auto/webhooks":       "public webhook ingress needs a delivery contract (signature, replay) that is not designed yet",
	// Identity is IAM. The product's auth contract is gateway-minted headers;
	// there are no product users to sign in, invite, or enumerate.
	"/v1/auto/authentication/sign-in": "identity is Hanzo IAM; the product has no credential surface — its auth contract is the gateway-minted org header",
	"/v1/auto/users":                  "platform identity is IAM; the product keeps no user table to expose",
	"/v1/auto/user-invitations":       "membership is IAM's team plane, not a product surface",
	"/v1/auto/project-members":        "membership is IAM's team plane, not a product surface",
	// The product's project row IS the tenant boundary (one row per org,
	// auto-provisioned). Exposing it would hand callers the isolation
	// primitive itself.
	"/v1/auto/projects": "the product's project row is this plane's tenant boundary — one per org, provisioned on first use — and is not addressable by callers",
	"/v1/auto/folders":  "not a v2 primitive; flows are a flat per-org list",
	// Activepieces-shaped families the v2 rewrite does not carry.
	"/v1/auto/pieces/categories": "the v2 piece catalog is one flat compiled-in list at /v1/auto/pieces; there is no registry with categories/versions/options",
	"/v1/auto/store-entries":     "no key-value store surface in the v2 product; provisioning owns /v1/kv",
	"/v1/auto/templates":         "no template CRUD in the v2 product; /v1/templates is the fleet's template plane",
	"/v1/auto/tables":            "no table/field/record plane in the v2 product",
	"/v1/auto/todos":             "no todo plane in the v2 product",
	"/v1/auto/git-repos":         "no git-sync in the v2 product; /v1/git is the fleet's git plane",
	"/v1/auto/ai-providers":      "model-provider custody belongs to the ai plane, not the automation product",
	"/v1/auto/api-keys":          "credentials are IAM's plane; the product accepts identity only from its gateway",
	"/v1/auto/flags":             "feature flags are the fleet's /v1/flags plane",
	"/v1/auto/audit-events":      "audit is the fleet's /v1/auditlog plane",
	"/v1/auto/sample-data":       "step sample-data is builder-internal in the v2 product, not an API",
	"/v1/auto/flow-runs":         "the authored name for run records; the v2 product serves them at /v1/auto/runs, which this plane mounts",
}

// ── fake upstream: the measured auto wire ───────────────────────────────────

// upstreamCall records one request the fake auto service saw.
type upstreamCall struct {
	method, path, query, org string
	body                     []byte
}

// fakeAuto is a minimal in-memory auto service speaking the measured wire:
// /v1/health, /v1/pieces, /v1/flows (list, 201 create), /v1/flows/{id} (get,
// patch, 204 delete), /v1/flows/{id}/publish (201), /v1/runs (list, 202
// dispatch), /v1/runs/{id}. Rows scope by the X-Org-Id header exactly as the
// product does: no header → 401, foreign org → 404.
type fakeAuto struct {
	mu     sync.Mutex
	calls  []upstreamCall
	flows  map[string]map[string]any // id → flow record (org in "project")
	runs   map[string]map[string]any // id → run record (flow links the org)
	nextID int
	noOrg  bool // strip the org check (simulates a broken identity seam → 401)
}

func (f *fakeAuto) record(r *http.Request) []byte {
	body, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, upstreamCall{
		method: r.Method, path: r.URL.Path, query: r.URL.RawQuery,
		org: r.Header.Get("X-Org-Id"), body: body,
	})
	return body
}

func (f *fakeAuto) id() string {
	f.nextID++
	return fmt.Sprintf("%015d", f.nextID) // 15 chars, the product's id width
}

func (f *fakeAuto) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		body := f.record(r)
		js := func(status int, v any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(v)
		}
		org := r.Header.Get("X-Org-Id")
		if org == "" || f.noOrg {
			// The product's gateway-auth contract, measured verbatim.
			js(401, map[string]any{"data": map[string]any{}, "message": "Missing X-Org-Id (gateway auth required).", "status": 401})
			return
		}
		flowDTO := func(rec map[string]any) map[string]any {
			return map[string]any{"id": rec["id"], "name": rec["name"], "data": rec["data"], "created": "2026-07-31 05:47:01.849Z", "updated": "2026-07-31 05:47:01.849Z"}
		}
		switch {
		case r.URL.Path == "/v1/health":
			js(200, map[string]any{"message": "API is healthy.", "code": 200, "data": map[string]any{}})
		case r.URL.Path == "/v1/pieces":
			js(200, map[string]any{"items": []map[string]any{
				{"type": "webhook", "name": "Webhook", "category": "trigger"},
				{"type": "http", "name": "HTTP Request", "category": "action"},
				{"type": "set", "name": "Set Variable", "category": "action"},
			}})
		case r.URL.Path == "/v1/flows" && r.Method == http.MethodGet:
			items := []map[string]any{}
			for _, rec := range f.flows {
				if rec["project"] == org {
					items = append(items, flowDTO(rec))
				}
			}
			js(200, map[string]any{"items": items})
		case r.URL.Path == "/v1/flows" && r.Method == http.MethodPost:
			var in map[string]any
			_ = json.Unmarshal(body, &in)
			if in["name"] == nil || in["name"] == "" {
				js(400, map[string]any{"data": map[string]any{}, "message": "Name is required.", "status": 400})
				return
			}
			rec := map[string]any{"id": f.id(), "name": in["name"], "data": in["data"], "project": org}
			if rec["data"] == nil {
				rec["data"] = map[string]any{}
			}
			f.flows[rec["id"].(string)] = rec
			js(201, flowDTO(rec))
		case strings.HasSuffix(r.URL.Path, "/publish") && r.Method == http.MethodPost:
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/flows/"), "/publish")
			rec, ok := f.flows[id]
			if !ok || rec["project"] != org {
				js(404, map[string]any{"data": map[string]any{}, "message": "Flow not found.", "status": 404})
				return
			}
			js(201, map[string]any{"id": f.id(), "flow": id, "version": 1, "published": true, "created": "2026-07-31T05:47:01Z"})
		case strings.HasPrefix(r.URL.Path, "/v1/flows/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/flows/")
			rec, ok := f.flows[id]
			if !ok || rec["project"] != org {
				js(404, map[string]any{"data": map[string]any{}, "message": "Flow not found.", "status": 404})
				return
			}
			switch r.Method {
			case http.MethodGet:
				js(200, flowDTO(rec))
			case http.MethodPatch:
				var patch map[string]any
				_ = json.Unmarshal(body, &patch)
				maps.Copy(rec, patch)
				js(200, flowDTO(rec))
			case http.MethodDelete:
				delete(f.flows, id)
				w.WriteHeader(http.StatusNoContent)
			}
		case r.URL.Path == "/v1/runs" && r.Method == http.MethodPost:
			var in struct {
				FlowID string         `json:"flowId"`
				Input  map[string]any `json:"input"`
			}
			_ = json.Unmarshal(body, &in)
			rec, ok := f.flows[in.FlowID]
			if !ok || rec["project"] != org {
				js(404, map[string]any{"data": map[string]any{}, "message": "Flow not found.", "status": 404})
				return
			}
			run := map[string]any{"id": f.id(), "flowId": in.FlowID, "status": "running", "taskId": "auto-run-x", "input": in.Input, "output": map[string]any{}}
			f.runs[run["id"].(string)] = run
			js(202, run)
		case r.URL.Path == "/v1/runs" && r.Method == http.MethodGet:
			items := []map[string]any{}
			want := r.URL.Query().Get("flowId")
			for _, run := range f.runs {
				flow, ok := f.flows[run["flowId"].(string)]
				if !ok || flow["project"] != org {
					continue
				}
				if want != "" && run["flowId"] != want {
					continue
				}
				items = append(items, run)
			}
			js(200, map[string]any{"items": items})
		case strings.HasPrefix(r.URL.Path, "/v1/runs/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/runs/")
			run, ok := f.runs[id]
			if ok {
				if flow, ok2 := f.flows[run["flowId"].(string)]; ok2 && flow["project"] == org {
					js(200, run)
					return
				}
			}
			js(404, map[string]any{"data": map[string]any{}, "message": "Run not found.", "status": 404})
		default:
			js(404, map[string]any{"data": map[string]any{}, "message": "Not Found.", "status": 404})
		}
	})
}

// compose installs what a HOST installs. A subsystem never installs cloud.Bridge
// (routes() says why): the program's composer installs it once at the root, after
// the identity check that mints the validated org and before any subsystem
// registers a route. In production that composer is serve.go. In a test the test
// IS the composer, so it owes the same thing.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

// harness mounts the app against a fake upstream and returns both.
func harness(t *testing.T) (*zip.App, *fakeAuto) {
	t.Helper()
	f := &fakeAuto{flows: map[string]map[string]any{}, runs: map[string]map[string]any{}}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	t.Setenv("AUTO_UPSTREAM", srv.URL)

	app := zip.New(zip.Config{Logger: luxlog.New("autotest"), DisableStartupMessage: true})
	compose(app)
	if err := Mount(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
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
	resp, err := app.Test(req)
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
	doc, err := openapi.Spec(app, openapi.Info{Title: "auto", Version: "v1"})
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
		t.Fatal("auto serves no operations at all — the mount did not register")
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
	doc, err := openapi.Spec(app, openapi.Info{Title: "auto", Version: "v1"})
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

// A request with no validated principal is refused 403 and NEVER reaches the
// upstream — the forged-X-Org-Id path is dead.
func TestNoPrincipalIs403AndNoUpstreamByte(t *testing.T) {
	app, f := harness(t)
	for _, probe := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/auto/flows", ""},
		{http.MethodPost, "/v1/auto/flows", `{"name":"x"}`},
		{http.MethodGet, "/v1/auto/flows/000000000000001", ""},
		{http.MethodPost, "/v1/auto/runs", `{"flow":"000000000000001"}`},
		{http.MethodGet, "/v1/auto/pieces", ""},
		{http.MethodGet, "/v1/auto/status", ""},
	} {
		status, body := do(t, app, probe.method, probe.path, "", "victim", probe.body)
		if status != http.StatusForbidden {
			t.Errorf("%s %s without principal = %d, want 403; body=%s", probe.method, probe.path, status, body)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 0 {
		t.Errorf("upstream was contacted %d time(s) for unvalidated requests; must be 0", len(f.calls))
	}
}

// Every upstream call carries the VALIDATED principal's org on X-Org-Id — the
// product's gateway-auth contract — and rows scope by it: the full loop works
// for the owner while every foreign access answers 404, indistinguishable
// from a nonexistent id, for read, update, delete, publish, run start, run
// read, and the run list alike.
func TestFlowsAreOrgScoped(t *testing.T) {
	app, f := harness(t)

	// acme creates a flow.
	status, body := do(t, app, http.MethodPost, "/v1/auto/flows", "u1", "acme",
		`{"name":"wf-a","data":{"nodes":[{"id":"t","type":"webhook"}],"edges":[]}}`)
	if status != http.StatusOK {
		t.Fatalf("create = %d body=%s", status, body)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil || created.ID == "" {
		t.Fatalf("create relay unparseable: %s", body)
	}

	// Every upstream call so far rode acme's org.
	f.mu.Lock()
	for _, c := range f.calls {
		if c.org != "acme" {
			t.Errorf("upstream %s %s carried org %q, want the validated principal's %q", c.method, c.path, c.org, "acme")
		}
	}
	f.mu.Unlock()

	// acme sees it.
	status, body = do(t, app, http.MethodGet, "/v1/auto/flows", "u1", "acme", "")
	if status != http.StatusOK || !strings.Contains(body, created.ID) {
		t.Fatalf("acme list = %d %s", status, body)
	}

	// globex sees an empty list, and every foreign access answers 404.
	status, body = do(t, app, http.MethodGet, "/v1/auto/flows", "u2", "globex", "")
	if status != http.StatusOK || strings.Contains(body, created.ID) {
		t.Fatalf("globex list leaked acme's flow: %d %s", status, body)
	}
	for _, probe := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/auto/flows/" + created.ID, ""},
		{http.MethodPatch, "/v1/auto/flows/" + created.ID, `{"name":"stolen"}`},
		{http.MethodDelete, "/v1/auto/flows/" + created.ID, ""},
		{http.MethodPost, "/v1/auto/flows/" + created.ID + "/publish", ""},
		{http.MethodPost, "/v1/auto/runs", `{"flow":"` + created.ID + `","input":{}}`},
	} {
		status, body = do(t, app, probe.method, probe.path, "u2", "globex", probe.body)
		if status != http.StatusNotFound {
			t.Errorf("globex %s %s = %d, want 404; body=%s", probe.method, probe.path, status, body)
		}
	}

	// acme's own full loop stays green: read, update, publish, start, run
	// read, run list, delete.
	if status, body = do(t, app, http.MethodGet, "/v1/auto/flows/"+created.ID, "u1", "acme", ""); status != http.StatusOK {
		t.Fatalf("acme get = %d %s", status, body)
	}
	if status, body = do(t, app, http.MethodPatch, "/v1/auto/flows/"+created.ID, "u1", "acme", `{"name":"wf-a2"}`); status != http.StatusOK || !strings.Contains(body, "wf-a2") {
		t.Fatalf("acme patch = %d %s", status, body)
	}
	if status, body = do(t, app, http.MethodPost, "/v1/auto/flows/"+created.ID+"/publish", "u1", "acme", ""); status != http.StatusOK || !strings.Contains(body, `"published":true`) {
		t.Fatalf("acme publish = %d %s", status, body)
	}
	status, body = do(t, app, http.MethodPost, "/v1/auto/runs", "u1", "acme", `{"flow":"`+created.ID+`","input":{"who":"cloud"}}`)
	if status != http.StatusOK || !strings.Contains(body, `"status":"running"`) {
		t.Fatalf("acme start = %d %s", status, body)
	}
	var run struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(body), &run)
	if status, body = do(t, app, http.MethodGet, "/v1/auto/runs/"+run.ID, "u1", "acme", ""); status != http.StatusOK {
		t.Fatalf("acme run get = %d %s", status, body)
	}
	if status, body = do(t, app, http.MethodGet, "/v1/auto/runs/"+run.ID, "u2", "globex", ""); status != http.StatusNotFound {
		t.Fatalf("globex run get = %d, want 404; %s", status, body)
	}
	if status, body = do(t, app, http.MethodGet, "/v1/auto/runs?flow="+created.ID, "u1", "acme", ""); status != http.StatusOK || !strings.Contains(body, run.ID) {
		t.Fatalf("acme runs = %d %s", status, body)
	}
	if status, body = do(t, app, http.MethodDelete, "/v1/auto/flows/"+created.ID, "u1", "acme", ""); status != http.StatusOK {
		t.Fatalf("acme delete = %d %s", status, body)
	}
}

// ── the relay and the seam ──────────────────────────────────────────────────

// The upstream payload reaches the caller VERBATIM — including an int64
// beyond float64, the case that fails silently if a relay ever decodes into
// `any` and re-encodes.
func TestRelayIsVerbatim(t *testing.T) {
	if got, _ := (autoResult{raw: json.RawMessage(`{"id":9007199254740993}`)}).MarshalJSON(); string(got) != `{"id":9007199254740993}` {
		t.Fatalf("relay mangled the payload: %s", got)
	}
	if got, _ := (autoResult{}).MarshalJSON(); string(got) != `{}` {
		t.Fatalf("empty relay = %s, want {}", got)
	}
}

// An upstream 401 means the org header did not survive the seam — a
// deployment fault. The caller sees 503, never a 401 that would read as their
// own auth failing.
func TestUpstreamAuthRefusalIs503(t *testing.T) {
	app, f := harness(t)
	f.mu.Lock()
	f.noOrg = true
	f.mu.Unlock()
	status, body := do(t, app, http.MethodGet, "/v1/auto/flows", "u1", "acme", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("seam refusal surfaced as %d (%s), want 503", status, body)
	}
}

// The product's own 503 — its engine refusing dispatch because the tasks
// plane is unreachable — relays as 503 with the product's reason: dispatch is
// real or it is refused, never faked.
func TestEngineDown503Relays(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"data":{},"message":"Tasks worker is not available.","status":503}`))
	}))
	defer srv.Close()
	t.Setenv("AUTO_UPSTREAM", srv.URL)
	app := zip.New(zip.Config{Logger: luxlog.New("autotest"), DisableStartupMessage: true})
	compose(app)
	if err := Mount(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	status, body := do(t, app, http.MethodPost, "/v1/auto/runs", "u1", "acme", `{"flow":"000000000000001","input":{}}`)
	if status != http.StatusServiceUnavailable || !strings.Contains(body, "Tasks worker is not available") {
		t.Fatalf("engine-down = %d %s, want the product's own 503 reason", status, body)
	}
}

// The reachability lens answers honestly in both directions: reachable:true
// when the product is up, reachable:false — not an error — when nothing
// listens.
func TestStatusIsAnHonestLens(t *testing.T) {
	app, _ := harness(t)
	status, body := do(t, app, http.MethodGet, "/v1/auto/status", "u1", "acme", "")
	if status != http.StatusOK || !strings.Contains(body, `"reachable":true`) {
		t.Fatalf("status(up) = %d %s", status, body)
	}

	down := zip.New(zip.Config{Logger: luxlog.New("autotest"), DisableStartupMessage: true})
	compose(down)
	t.Setenv("AUTO_UPSTREAM", "http://127.0.0.1:1") // nothing listens
	if err := Mount(down, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	status, body = do(t, down, http.MethodGet, "/v1/auto/status", "u1", "acme", "")
	if status != http.StatusOK || !strings.Contains(body, `"reachable":false`) {
		t.Fatalf("status(down) = %d %s, want an honest reachable:false", status, body)
	}
}

// An id that is not the product's record-id shape never reaches the upstream:
// the path is the addressing authority and its shape is validated before any
// splice.
func TestIDShapeIsValidatedBeforeUpstream(t *testing.T) {
	app, f := harness(t)
	for _, probe := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/auto/flows/..%2F..%2Fetc%2Fpasswd", ""},
		{http.MethodPost, "/v1/auto/runs", `{"flow":"../../etc/passwd"}`},
		{http.MethodGet, "/v1/auto/runs?flow=..%2Fpasswd", ""},
	} {
		status, _ := do(t, app, probe.method, probe.path, "u1", "acme", probe.body)
		if status != http.StatusBadRequest {
			t.Errorf("hostile id via %s %s = %d, want 400", probe.method, probe.path, status)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.Contains(c.path, "passwd") || strings.Contains(c.query, "passwd") {
			t.Fatalf("hostile id reached the upstream: %s?%s", c.path, c.query)
		}
	}
}
