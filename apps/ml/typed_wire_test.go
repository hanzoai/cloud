package ml

// This file makes ml's typed partition a GATE instead of a paragraph. "3 of 7"
// is prose, and prose cannot fail: a route added tomorrow as a raw
// func(*zip.Ctx) error would leave the claim standing and the route invisible to
// the document, the MCP tool list, the CLI and every generated SDK. Here the
// claim is a test, so the route that falsifies it says so.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// untypedByDesign is the CLOSED list of ml operations that are NOT typed ops,
// each with the WIRE fact that keeps it raw. A typed op is a route PLUS a registry
// entry — the one value the OpenAPI operation, the MCP tool, the CLI command and
// the SDK method all come from — so an operation missing from that registry is
// invisible to all four. These three are missing on purpose. The address is
// written the way the DOCUMENT writes it, which is the identity every projection
// keys on.
var untypedByDesign = map[string]string{
	// An RFC 7386 JSON merge patch, handed to the Kubernetes API as the RAW request
	// bytes (k8stypes.MergePatchType, c.Body()). A typed In must decode and
	// re-encode it, and re-encoding a merge patch changes what it MEANS: through
	// map[string]any every number becomes a float64, so `{"replicas":1000000}`
	// re-marshals as 1e+06 and patches a float over an integer field. The
	// empty-body 400 is also checked on the raw bytes, before any parse.
	"PATCH /v1/ml/models/{name}": "an opaque RFC 7386 merge patch relayed VERBATIM to the Kubernetes API — " +
		"a typed In would decode and re-encode it, and re-encoding a merge patch changes what it means " +
		"(an integer round-trips through float64).",

	// A verbatim proxy of the kserve v2 data plane.
	"POST /v1/ml/models/{name}/predict": "the predictor's own status code, body bytes and Content-Type are " +
		"returned unchanged (c.Bytes(resp.StatusCode, rb)) so a model-side error surfaces honestly — a typed " +
		"Out is one JSON shape at one declared status and can carry none of the three.",

	// The real probe.
	"GET /v1/ml/health": healthWire,
}

const healthWire = "a REAL probe: 503 carries the degraded REPORT as its body " +
	"(status/k8s/error/crds), which is the whole point of it. A typed op reaches a non-2xx only by " +
	"returning an error, and zip renders that as its own envelope, dropping the report."

// mountApp mounts ml the way plugin/ml does — the whole Mount, so the projection
// ledgers below read the surface a deployed binary serves and not a test-only
// subset. It is independent of whether this box can resolve a cluster: routes are
// registered either way.
func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("mltest"), DisableStartupMessage: true})
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	return app
}

// wireApp mounts the surface over a state the TEST pins, so a wire assertion does
// not depend on whether the box running the suite has a kubeconfig. dyn nil is the
// fail-closed posture; a fake client is the reachable one.
func wireApp(t *testing.T, dyn dynamic.Interface) *zip.App {
	t.Helper()
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	deps := cloud.Deps{}
	s := &cloud.Service[state]{
		Base: cloud.NewBase(deps, "ml"),
		State: state{
			dyn:     dyn,
			initErr: "no in-cluster config and no kubeconfig",
			hc:      &http.Client{},
			bill:    cloud.NewMeter(deps, "compute"),
		},
	}
	app := zip.New(zip.Config{Logger: luxlog.New("mltest"), DisableStartupMessage: true})
	mount(s, app)
	return app
}

// mlOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed
// registry entry. EVERY served operation counts, so a route mounted at an address
// nobody expected is caught rather than filtered out.
func mlOps(t *testing.T) (served map[string]bool, typed map[string]string, schemas map[string]any) {
	t.Helper()
	app := mountApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "ml", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	served, typed = map[string]bool{}, map[string]string{}
	for path, item := range doc.Paths {
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		typed[key] = op.Description
	}
	return served, typed, reg.Schemas
}

// TestEveryRouteIsTypedOrNamed fails when an ml operation is neither a typed op
// nor named above — so the next route added here is typed by default, and dropping
// one out of the registry takes a deliberate edit carrying a reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed, _ := mlOps(t)

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
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no SDK "+
			"method. Convert it (zip.Get/Post/... on the /v1/ml group), or add it to "+
			"untypedByDesign with the reason typing it would move the wire.", strings.Join(untyped, ", "))
	}
	// The reasons must describe operations that exist, or the list is stale prose.
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which ml no longer serves", key)
		}
	}
	// A typed op named as a refusal is a contradiction — one of the two is wrong.
	for key := range untypedByDesign {
		if _, ok := typed[key]; ok {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
	// The two ledgers must sum to the whole surface: 3 typed + 4 named = 7.
	if got := len(typed) + len(untypedByDesign); got != len(served) {
		t.Errorf("%d typed + %d named = %d, but ml serves %d operations",
			len(typed), len(untypedByDesign), got, len(served))
	}
}

// TestEveryTypedOpIsDescribed proves the lifted prose reached the binary. That
// prose IS the product surface: it becomes the OpenAPI description AND the MCP
// tool description a model reads to pick the tool. zipdoc_gen.go is what carries
// it in, so an op added without regenerating shows up here as a nameless tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed, _ := mlOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed ml ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/ml/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the op-level
// gate cannot see. Typing a route documents its ADDRESS and its SHAPE; it does not
// document the shape's FIELDS, and those come from a different place — doc comments
// on the In/Out struct fields, which zipdoc lifts per field. A reader of the API
// could otherwise see that `spec` is an object and nowhere that it is the
// Kubernetes spec verbatim.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	_, _, schemas := mlOps(t)
	if len(schemas) == 0 {
		t.Fatal("no ml schemas in the typed registry at all")
	}
	var bare []string
	for name, raw := range schemas {
		sch, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		props, ok := sch["properties"].(map[string]any)
		if !ok {
			continue // a scalar or an array schema has no properties to describe
		}
		for field, praw := range props {
			p, ok := praw.(map[string]any)
			if !ok {
				continue
			}
			if desc, _ := p["description"].(string); strings.TrimSpace(desc) == "" {
				bare = append(bare, name+"."+field)
			}
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("published propert(ies) with no description: %s\n"+
			"Every field of a published schema is read by SDK users and by a model choosing a tool. Write a "+
			"doc comment on the struct field and run: go generate -run zipdoc ./apps/ml/...",
			strings.Join(bare, ", "))
	}
}

// ── the wire the conversion had to preserve ──────────────────────────────────

func req(t *testing.T, app *zip.App, method, path, org, user string) (int, []byte) {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	if org != "" {
		r.Header.Set("X-Org-Id", org)
	}
	if user != "" {
		r.Header.Set("X-User-Id", user)
	}
	resp, err := app.Test(r)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// A typed op receives only a context, so the validated org reaches it through
// cloud.Bridge and NEVER as an In field — an In field is caller-supplied, so a
// tenant key read from one is a cross-tenant read the caller asserted for itself.
// ml has no In field that could carry an org, and these prove the identity
// gate is live on the typed routes: a request with no validated principal is
// refused with the same 403 the untyped handlers give, and the refusal comes
// BEFORE any Kubernetes reach. (Mount found no cluster here, so a request that
// passed the gate would answer 503 instead — which is what makes the 403 a
// measurement rather than an accident.)
func TestTypedReadsRefuseAnUnvalidatedPrincipal(t *testing.T) {
	// A REACHABLE cluster, so a 403 here is the identity gate refusing and not the
	// fail-closed 503 standing in front of it.
	app := wireApp(t, dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()))
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/ml/models"},
		{http.MethodGet, "/v1/ml/models/m1"},
		{http.MethodDelete, "/v1/ml/models/m1"},
	} {
		// An X-Org-Id with no X-User-Id is exactly the forged-header case: the
		// header survived ingress but no credential minted it.
		code, body := req(t, app, tc.method, tc.path, "acme", "")
		if code != http.StatusForbidden {
			t.Errorf("%s %s = %d %s, want 403 for an unvalidated principal", tc.method, tc.path, code, body)
		}
	}
}

// ready() runs BEFORE the tenant gate on every data route, and it did before the
// conversion too — an unconfigured Kubernetes client is 503 whoever asks. This
// pins that ORDER, which is the part a reordering would silently move.
func TestTypedReadsFailClosedWithoutKubernetes(t *testing.T) {
	app := wireApp(t, nil) // no client resolved — the posture Mount logs and carries
	code, body := req(t, app, http.MethodGet, "/v1/ml/models", "acme", "u_acme")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("GET /v1/ml/models = %d %s, want 503 with no kubernetes client", code, body)
	}
	// A refusal is an RFC 9457 problem document, so the sentence is `detail`.
	var e struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if !strings.Contains(e.Detail, "kubernetes client not configured") {
		t.Fatalf("503 body %s does not name the real reason", body)
	}
}
