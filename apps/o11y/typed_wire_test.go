package o11y

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/openapi"
)

// sentryPrefix is the SECOND path family this package owns — the Hanzo Sentry
// product face, delegating to the same gated runtime handler as the /v1/o11y
// wildcard (mountSentry, o11y.go). The gate below has to read both, or half the
// surface it claims to cover is outside it.
const sentryPrefix = "/v1/sentry"

// surfaceApp mounts the WHOLE observability surface through the REAL MountO11y,
// so the assertions below read the router the document is generated from rather
// than a reconstruction of it — a route added anywhere inside that mount (or in
// the upstream module it ends with) shows up here without anyone remembering to
// list it.
//
// Two branches are taken deliberately, and they are the SAME two `bin/o11y
// openapi` takes (mk/plugin.mk runs it with GIT_SSH_ADDR and nothing else):
// with no Datastore DSN the runtime installs its reverse-proxy fallback instead
// of the in-process engine, and mountEventIngest returns before registering
// POST /v1/event/ingestion. Probes are off because they knock on in-cluster
// Services a test has no business reaching.
func surfaceApp(t *testing.T) *zip.App {
	t.Helper()
	t.Setenv("O11Y_PROBES", "false")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := MountO11y(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("MountO11y: %v", err)
	}
	t.Cleanup(func() { _ = shutdownAnnotationQueues() })
	return app
}

// untypedByDesign is the CLOSED list of o11y operations that are NOT typed ops,
// each with the wire fact that keeps it out. A typed op is a route PLUS a
// registry entry — the one value the OpenAPI operation, the MCP tool, the CLI
// command and the SDK method all come from — so an operation missing from that
// registry is invisible to all four. These are missing on purpose: every one of
// them would answer a DIFFERENT wire as a typed op, and typing is a description
// task. Addresses are written the way the DOCUMENT writes them, which is the
// identity every projection keys on.
var untypedByDesign = map[string]string{
	"GET /v1/o11y/vm/query": "returns VictoriaMetrics' own status code and its Prometheus envelope " +
		"VERBATIM (vmProxy: c.Bytes(status, body)). A typed op answers its ONE declared status and " +
		"marshals a Go value, so a VM 4xx would become a 200 and the envelope would be re-shaped.",
	"GET /v1/o11y/vm/query_range": "same verbatim status+envelope passthrough as /vm/query, over the " +
		"range form. Its `values` entries are [unixSeconds, \"sample\"] pairs — a heterogeneous JSON " +
		"array no Go struct field can hold without becoming []any, which publishes a schema the wire " +
		"does not have.",
	"POST /v1/o11y/query": "a reverse proxy into the o11y runtime's v3 engine route (builderQueryHandler: " +
		"zip.AdaptNetHTTP, r.URL.Path rewritten to /api/v3/query). Request body, query string, upstream " +
		"status, headers and body all ride through untouched; there is no Go type for \"whatever the " +
		"runtime answered\".",
	"POST /v1/o11y/query_range": "the same reverse proxy over /api/v3/query_range. It carries the console's " +
		"v3 composite payload (compositeQuery.{queryType,builderQueries}), which is the engine's shape " +
		"and not this package's to declare.",
	"GET /v1/o11y/sessions": "a reverse proxy into the runtime's /api/sessions (sessions.go). The org gate " +
		"runs at the cloud boundary, then the runtime's llmobstypes.GettableSessions body and its status " +
		"ride through unchanged.",
	"GET /v1/o11y/alerts/last": "answers text/plain, not JSON — c.String with the delivery ring joined by " +
		"newlines so `curl … | tail` reads in arrival order, and \"(none)\" when empty. A typed op " +
		"marshals JSON, which would break every operator's grep.",
	"POST /v1/o11y/alerts/{receiver}": "answers text/plain \"ok\" and deliberately ACCEPTS an unparseable " +
		"body — it is a delivery receipt, so a body that will not parse still proves delivery and a 400 " +
		"would make Alertmanager retry forever. zip decodes a typed In before the handler runs, so " +
		"typing it would turn that 200 into a 400.",
	// The two catch-alls and the upstream module's probes. A wildcard has no
	// operation to type, and these three probe paths are not registered by this
	// package at all.
	"GET /v1/o11y/{wildcard1}":       wildcardReason,
	"POST /v1/o11y/{wildcard1}":      wildcardReason,
	"PUT /v1/o11y/{wildcard1}":       wildcardReason,
	"PATCH /v1/o11y/{wildcard1}":     wildcardReason,
	"DELETE /v1/o11y/{wildcard1}":    wildcardReason,
	"OPTIONS /v1/o11y/{wildcard1}":   wildcardReason,
	"TRACE /v1/o11y/{wildcard1}":     wildcardReason,
	"GET /v1/sentry/{wildcard1}":     sentryReason,
	"POST /v1/sentry/{wildcard1}":    sentryReason,
	"PUT /v1/sentry/{wildcard1}":     sentryReason,
	"PATCH /v1/sentry/{wildcard1}":   sentryReason,
	"DELETE /v1/sentry/{wildcard1}":  sentryReason,
	"OPTIONS /v1/sentry/{wildcard1}": sentryReason,
	"TRACE /v1/sentry/{wildcard1}":   sentryReason,
	"GET /v1/o11y/api/v2/healthz":    upstreamProbeReason,
	"GET /v1/o11y/api/v2/livez":      upstreamProbeReason,
	"GET /v1/o11y/api/v2/readyz":     upstreamProbeReason,
}

const (
	wildcardReason = "the hanzoai/o11y module's terminal All(\"/v1/o11y/*\") — one route standing for the " +
		"runtime's whole route surface, resolved per-request. A wildcard has no operation to type."
	sentryReason = "the /v1/sentry/* wildcard (mountSentry) forwarding to the same gated runtime handler. " +
		"A wildcard has no operation to type."
	upstreamProbeReason = "registered by the upstream hanzoai/o11y module, not by this package — a " +
		"liveness/readiness path the runtime serves without identity so k8s probes pass."
)

// o11yOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed
// registry entry. Reading the router (not the source) is what makes this a gate
// rather than prose.
func o11yOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := surfaceApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "o11y", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool {
		return strings.HasPrefix(p, o11yPrefix) || strings.HasPrefix(p, sentryPrefix)
	}
	served, typed = map[string]bool{}, map[string]string{}
	for path, item := range doc.Paths {
		if !ours(path) {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		if i := strings.Index(key, " "); i > 0 && ours(key[i+1:]) {
			typed[key] = op.Description
		}
	}
	return served, typed
}

// TestEveryRouteIsTypedOrNamed fails when an o11y operation is neither a typed op
// nor one named above — so the next route added here is typed by default, and
// dropping one out of the registry takes a deliberate edit with a reason. The
// eight wire-bound refusals were prose in apps/o11y/LLM.md until this existed,
// which is a promise, not a gate: prose cannot go red.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := o11yOps(t)

	var untyped []string
	for key := range served {
		if _, ok := typed[key]; ok {
			continue
		}
		if _, named := untypedByDesign[key]; named {
			continue
		}
		untyped = append(untyped, key)
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("operation(s) with no registry entry and no reason: %s\n"+
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no "+
			"SDK method. Convert it (zip.Get/Post/... on the o11y group), or add it to untypedByDesign "+
			"with the wire fact that typing it would move.", strings.Join(untyped, ", "))
	}
	// The reasons must describe operations that exist, or the list is stale prose.
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which o11y no longer serves", key)
		}
	}
}

// TestEveryTypedOpIsDescribed holds the prose to the same bar as the schema,
// because that prose IS the product surface: it becomes the OpenAPI description
// AND the MCP tool description a model reads to pick the tool. zipdoc_gen.go is
// what carries it into the binary, so an op added without regenerating shows up
// here as a nameless tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := o11yOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed o11y ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/o11y/...", key)
		}
	}
}

// TestUntypedRoutesKeepTheirWire measures the four wire facts the reasons above
// CLAIM, on the real router, so the refusals are evidence rather than assertion.
// Each is a fact a typed op could not answer: a text/plain body, a 200 over a
// body that is not JSON, and a non-JSON content type on the replay read.
func TestUntypedRoutesKeepTheirWire(t *testing.T) {
	app := surfaceApp(t)

	send := func(t *testing.T, method, path string, body string) *http.Response {
		t.Helper()
		var r *strings.Reader = strings.NewReader(body)
		rq := httptest.NewRequest(method, path, r)
		rq.Header.Set("X-Org-Id", "acme")
		rq.Header.Set("X-User-Id", "u_acme")
		resp, err := app.Fiber().Test(rq, fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("Test %s %s: %v", method, path, err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	t.Run("the receipt answers 200 text/plain over a body that is not JSON", func(t *testing.T) {
		resp := send(t, http.MethodPost, "/v1/o11y/alerts/page-critical", "{not json at all")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("unparseable receipt = %d, want 200 — any other status makes Alertmanager retry forever", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("Content-Type = %q, want text/plain — a typed op would answer JSON", ct)
		}
	})

	t.Run("the replay answers text/plain", func(t *testing.T) {
		resp := send(t, http.MethodGet, "/v1/o11y/alerts/last", "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("replay = %d, want 200", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("Content-Type = %q, want text/plain — the ring is greppable lines, not JSON", ct)
		}
	})

	t.Run("the VM proxy stays SuperAdmin-only and allowlisted", func(t *testing.T) {
		// Not the passthrough itself (that needs a live VM), but the two refusals
		// that prove this handler — not a typed binder — owns the boundary.
		if resp := send(t, http.MethodGet, "/v1/o11y/vm/query?query=up", ""); resp.StatusCode != http.StatusForbidden {
			t.Errorf("non-admin /vm/query = %d, want 403", resp.StatusCode)
		}
	})
}
