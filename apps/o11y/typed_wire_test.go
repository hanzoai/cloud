package o11y

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/openapi"
)

// There is ONE path family now. The Hanzo Sentry product face used to be a
// second — /v1/sentinel, a wildcard this package registered — and the gate below
// had to read both or leave half the surface it claims to cover outside it. The
// face folded under /v1/o11y/sentinel in the module (v1.5.67), so one prefix is
// the whole surface again and there is no second list to keep in step.

// surfaceApp mounts the WHOLE observability surface through the REAL Mount,
// so the assertions below read the router the document is generated from rather
// than a reconstruction of it — a route added anywhere inside that mount (or in
// the upstream module it ends with) shows up here without anyone remembering to
// list it.
//
// One branch is taken deliberately, and it is the SAME one `bin/o11y openapi`
// takes (mk/plugin.mk runs it with GIT_SSH_ADDR and nothing else): with no
// Datastore DSN the runtime installs its reverse-proxy fallback instead of the
// in-process engine. Probes are off because they knock on in-cluster Services a
// test has no business reaching.
func surfaceApp(t *testing.T) *zip.App {
	t.Helper()
	t.Setenv("O11Y_PROBES", "false")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	// The COMPOSER's middleware, installed at the root exactly as cloud.App does
	// (app.go) — because since "identity is the composer's" no subsystem installs
	// it, and Mount least of all: a second copy at this prefix is the install
	// that took the surface down. Without it every typed op below answers 403 no
	// matter what it was asked, which reads like a live outage and is only ever a
	// harness that stopped composing the way the real process does. scopeApp
	// (scope_test.go) already states this; surfaceApp is where it was missed.
	app.Use(cloud.Bridge())
	if err := Mount(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
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
	// The two /v1/o11y/vm/* entries that stood here are GONE, and their absence is
	// the point: they were untyped because they answered VictoriaMetrics' status
	// code and its Prometheus envelope verbatim, and a wire fact about a store we
	// no longer run cannot keep a route out of the registry. What is still
	// measured is served typed, at /v1/o11y/availability (availability.go).
	// The two builder-query proxies that stood here are GONE, and their absence is
	// the point. Each rewrote r.URL.Path onto /api/v3/<resource> to pin the
	// runtime's v3 engine, and the runtime has no such address: it registers every
	// route at its full public path and dropped prefix-stripping, so it serves no
	// /api/* route at all, and queryRangeV3 has no caller left. They forwarded into
	// the runtime's terminal /* catch-all. hanzoai/o11y's v5 querier answers at
	// POST /v1/o11y/query_range now, typed.
	"GET /v1/o11y/sessions": "a relay into the runtime's /v1/o11y/llm/sessions (sessions.go). The org is " +
		"pinned at the cloud boundary, then the runtime's llmobstypes.GettableSessions body, its status " +
		"and its headers ride through unchanged.",
	"GET /v1/o11y/alerts/last": "answers text/plain, not JSON — c.String with the delivery ring joined by " +
		"newlines so `curl … | tail` reads in arrival order, and \"(none)\" when empty. A typed op " +
		"marshals JSON, which would break every operator's grep.",
	"POST /v1/o11y/alerts/{receiver}": "answers text/plain \"ok\" and deliberately ACCEPTS an unparseable " +
		"body — it is a delivery receipt, so a body that will not parse still proves delivery and a 400 " +
		"would make Alertmanager retry forever. zip decodes a typed In before the handler runs, so " +
		"typing it would turn that 200 into a 400.",
	// The upstream module's own hatches. hanzoai/o11y no longer registers a
	// /v1/o11y/* catch-all — every route it serves is named — so the routes a
	// wildcard used to hide are visible here, each with the wire fact that keeps
	// it un-typed (hanzoai/o11y mount.go mountHatches, health.go mountHealth).
	"GET /v1/o11y/healthz": upstreamProbeReason,
	"GET /v1/o11y/livez":   upstreamProbeReason,
	"GET /v1/o11y/readyz":  upstreamProbeReason,

	"GET /v1/o11y/logs/livetail": upstreamStreamReason + " — an unbounded stream of log records.",
	"GET /v1/o11y/query_progress": upstreamStreamReason + " — a long poll that holds the connection until " +
		"the next tick, so a typed op would answer only after the query it reports on had finished.",
	"POST /v1/o11y/export_raw_data": upstreamStreamReason + " — a chunked CSV/JSONL attachment with an " +
		"X-Response-Complete trailer.",

	"GET /v1/o11y/login": signinOutcomeReason,

	"GET /v1/o11y/complete/google": upstreamRedirectReason,
	"GET /v1/o11y/complete/oidc":   upstreamRedirectReason,
	"POST /v1/o11y/complete/saml":  upstreamRedirectReason,

	"POST /v1/o11y/api/{project_id}/envelope/": upstreamIngestReason,
	"POST /v1/o11y/api/{project_id}/store/":    upstreamIngestReason,
}

const (
	upstreamProbeReason = "registered by the upstream hanzoai/o11y module, not by this package — a " +
		"liveness/readiness path the runtime serves without identity so k8s probes pass."
	upstreamStreamReason = "an upstream hanzoai/o11y hatch that never produces one complete JSON value; " +
		"zip buffers a whole answer before decoding it, so typing it would hang the stream"
	upstreamRedirectReason = "an upstream hanzoai/o11y sign-in callback that answers 303 with a Location " +
		"header and no payload. A typed op declares a 2xx JSON contract, which would publish a schema for " +
		"a response that does not exist and hide the header that is the entire point of the call."
	signinOutcomeReason = "where the module's own failed sign-in callback redirects a BROWSER, " +
		"same-origin and with the reason in the query. Nothing calls it: an SDK, a CLI or an MCP tool " +
		"reaching this address would mean a sign-in it never started had failed. Typing it would publish " +
		"a method for that."
	upstreamIngestReason = "Sentry-compatible ingest received by upstream hanzoai/o11y: the body is an " +
		"application/x-sentry-envelope frame, not JSON, and the caller authenticates with a DSN public key " +
		"rather than a Hanzo principal. We RECEIVE this shape; we do not publish it."
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
	ours := func(p string) bool { return strings.HasPrefix(p, o11yPrefix) }
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

// TestUntypedRoutesKeepTheirWire measures the wire facts the reasons above
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
		resp, err := app.Test(rq, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("Test %s %s: %v", method, path, err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	t.Run("the receipt answers text/plain over a body that is not JSON, and never 4xx", func(t *testing.T) {
		// An egress that accepts, because the status code now reports DELIVERY
		// rather than arrival: without one this answers 503, which is the point
		// of that change and is pinned in alerts_egress_test.go.
		swapEgress(t, egress{name: "test", send: func(context.Context, string) error { return nil }})

		resp := send(t, http.MethodPost, "/v1/o11y/alerts/page-critical", "{not json at all")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("unparseable receipt = %d, want 200", resp.StatusCode)
		}
		// The wire fact this test exists for: a malformed payload is RECORDED,
		// never REFUSED. A 4xx would make Alertmanager retry it forever, and the
		// delivery still happened — which is the fact being recorded.
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			t.Fatalf("unparseable body was refused with %d — Alertmanager retries a 4xx forever", resp.StatusCode)
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

}

// ---- composing, measured -------------------------------------------------

// TestComposingQualifiesEveryPublishedType is the deliverable of composing o11y as
// one app: the fleet's schema namespace is FLAT, o11y names its types
// after ordinary nouns, and five other apps name theirs the same way.
//
// Unqualified, o11y's Service (a traced APM service) and ingress's Service (a
// backend pool) are one name with two shapes, and so are Account, Channel, Event,
// Host and TLSConfig — six collisions that made openapi.Compose refuse the whole
// fleet document. It refused correctly: a generated SDK binds whichever shape the
// merge read last.
//
// Origin is what answers it, unconditionally rather than on collision, so a name
// published here is never a function of who else is in the room. The assertion is
// therefore TOTAL — every schema, not a sample — because a rule that holds for
// most names is not this rule.
func TestComposingQualifiesEveryPublishedType(t *testing.T) {
	reg, err := openapi.Typed(surfaceApp(t))
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	if len(reg.Schemas) == 0 {
		t.Fatal("the composed document carries no component schemas at all")
	}
	var bare []string
	for name := range reg.Schemas {
		if !strings.HasPrefix(name, "o11y.") {
			bare = append(bare, name)
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("%d schema(s) published unqualified: %s\n"+
			"Every type o11y publishes must arrive through the composed app, which qualifies it by the app "+
			"that declared it. An unqualified name is a name another app may also claim.",
			len(bare), strings.Join(bare, ", "))
	}
	// The six that actually collided, named so a regression says WHICH contract
	// broke rather than counting.
	for _, n := range []string{
		"o11y.Service", "o11y.TLSConfig", "o11y.Account",
		"o11y.Channel", "o11y.Event", "o11y.Host",
	} {
		if _, ok := reg.Schemas[n]; !ok {
			t.Errorf("components.schemas has no %q", n)
		}
		if _, collides := reg.Schemas[strings.TrimPrefix(n, "o11y.")]; collides {
			t.Errorf("%q is still published unqualified — it collides with another app's", strings.TrimPrefix(n, "o11y."))
		}
	}
}

// TestComposingLeavesAddressesAlone is the other half: composing changes what the
// fleet CALLS o11y's types and nothing about where o11y answers or what an SDK
// method is named. Paths are absolute and untouched; operationIds are published SDK
// method names and rewriting one at compose time would make it a function of
// where the app is deployed.
func TestComposingLeavesAddressesAlone(t *testing.T) {
	served, typed := o11yOps(t)
	for _, want := range []string{
		"GET /v1/o11y/product/metrics", // cloud's own org-pinned RED read, at its own address
		"GET /v1/o11y/status",          // ditto
		"GET /v1/o11y/logs",            // the module's log-record read, at the bare name
		"GET /v1/o11y/metrics",         // the module's metric-name catalog, ditto
		"POST /v1/o11y/query_range",    // the module's v5 querier, ditto
		"GET /v1/o11y/version",         // the module's, relayed to the runtime
		"POST /v1/o11y/alerts/last",    // not a route: the negative control below
	} {
		if want == "POST /v1/o11y/alerts/last" {
			if served[want] {
				t.Errorf("%s is served, and this list's negative control assumes it is not", want)
			}
			continue
		}
		if !served[want] {
			t.Errorf("%s is not served by the composed router", want)
		}
	}
	reg, err := openapi.Typed(surfaceApp(t))
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	for key, op := range reg.Ops {
		if op.OperationID == "" {
			continue
		}
		if strings.Contains(op.OperationID, "o11y.") {
			t.Errorf("%s has operationId %q — the origin qualifies TYPES, never addresses", key, op.OperationID)
		}
	}
	if len(typed) == 0 {
		t.Fatal("no typed o11y ops in the registry at all")
	}
}

// TestTheThreeAddressesAreToldApartNotShared is the routing fact that REPLACED
// the one this test used to pin.
//
// Three addresses were declared by both halves, and while a wildcard hid the
// overlap the answer was decided by registration order. hanzoai/o11y names every
// route now, so a second declaration is a refusal to compose — surfaceApp panics
// — and the whole test file is the gate on that. Naming the addresses as taken
// (o11y.Claimed) only made the collision explicit; it still suppressed the
// module's real read at each one. So the three were told apart instead:
//
//   - the per-product RED read moved to /v1/o11y/product/metrics, and the bare
//     /v1/o11y/metrics is the module's metric-name CATALOG — a different question;
//   - cloud's /v1/o11y/logs had no caller and is gone, so the address is the
//     module's real log-record read;
//   - cloud's POST /v1/o11y/query_range pinned /api/v3/query_range, an address the
//     runtime no longer serves at all, so it is gone and the module's v5 querier
//     answers there.
//
// Each case below is the ANSWER only that half can give, measured on the real
// mount. Under surfaceApp there is no datastore and the runtime's upstream is
// unreachable, so the module's half answers 502 — which is exactly what
// distinguishes it from cloud's own handler's own sentence.
func TestTheThreeAddressesAreToldApartNotShared(t *testing.T) {
	app := surfaceApp(t)

	ask := func(t *testing.T, method, path string, org bool) (int, string) {
		t.Helper()
		rq := httptest.NewRequest(method, path, strings.NewReader("{}"))
		rq.Header.Set("Content-Type", "application/json")
		if org {
			rq.Header.Set("X-Org-Id", "acme")
			rq.Header.Set("X-User-Id", "u_acme")
		}
		resp, err := app.Test(rq, zip.TestConfig{Timeout: 20 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("Test %s %s: %v", method, path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read %s %s: %v", method, path, err)
		}
		return resp.StatusCode, string(b)
	}

	// CLOUD'S HALF, at its own address: the org-pinned RED read refuses with ITS
	// OWN sentence about ITS OWN dependency. A 502 here means the module's relay
	// answered instead — i.e. the move did not take.
	if code, body := ask(t, http.MethodGet, "/v1/o11y/product/metrics?product=kms", true); !strings.Contains(body, "o11y metrics: datastore not connected") {
		t.Errorf("GET /v1/o11y/product/metrics = %d %s, want scope.go's own datastore refusal", code, body)
	}

	// THE MODULE'S HALF, at the three bare names cloud used to take. Each is a
	// relay into a runtime that surfaceApp cannot reach, so each answers 502 — and
	// a 502 is the proof the module's declaration survived, because cloud has no
	// handler left at any of them that could answer anything.
	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/o11y/logs?limit=1"},
		{http.MethodGet, "/v1/o11y/metrics"},
		{http.MethodPost, "/v1/o11y/query_range"},
	} {
		code, body := ask(t, tc.method, tc.path, true)
		if code == http.StatusNotFound {
			t.Errorf("%s %s = 404 — the module's declaration was suppressed and nothing replaced it",
				tc.method, tc.path)
			continue
		}
		if strings.Contains(body, "datastore not connected") || strings.Contains(body, `"view":`) {
			t.Errorf("%s %s = %d %s — cloud's own handler answered at the module's address",
				tc.method, tc.path, code, body)
		}
	}
}
