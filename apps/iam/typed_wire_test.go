package iam

// This file is the gate on what the GRAFT recovered, and it used to be the gate on
// what the wildcard destroyed.
//
// It held a permanent refusal: iam was "a WHOLLY OPAQUE product", five `app.All`
// wildcards relaying a nested app through zip.AdaptNetHTTP, and "none of the five
// can become a typed op … a structural fact about the mount, not a backlog item".
// The fact was true and the reason was wrong: it was a property of the SEAM, not of
// iam. zip.Graft composes the App instead of adapting a handler, so the nested
// registry arrives with it, and the refusal has nothing left to refuse.
//
// What replaces it is a RATCHET pointing the other way. iam publishes 182
// operations here; 94 of them are typed, which is every typed op the nested app
// holds. The 88 that are not are iam's OWN untyped routes, in github.com/hanzoai/iam
// — each one converted there lands in cloud's document on the next dependency bump,
// with no change to this file and none to apps/iam. The numbers below may only move
// in one direction, so that work cannot regress and cannot go unnoticed.
//
// It also still pins the two things a graft must not change: iam's own behaviour on
// the wire, and the fail-closed half covering every address the mounted half serves.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
)

// The ratchet. typedOps is every typed op the nested app holds and cloud therefore
// publishes with a schema, an MCP tool, a CLI command and an SDK method; untypedOps
// is what is left, and it is iam's own conversion backlog — measured HERE because
// this is where it becomes visible.
//
// typedOps may only RISE and untypedOps may only FALL. Both are asserted as
// inequalities rather than equalities so a conversion in github.com/hanzoai/iam is
// not a red build here; only a regression is.
//
// Before the graft these were 0 and 35: thirty-five placeholder operations across
// five wildcard path keys, none with a schema, a tool, a command or a method.
//
// They were 94 and 88 at iam v1.33.37, and the ratchet did its job: ten canonical
// noun addresses arrived as RAW handlers over the releases that followed — account,
// auth/application, preferences, verification-codes, tokens/issue, keys/mint,
// keys/revoke, mfa/disable, mfa/preferred, oauth/device/info — and the untyped
// count reached 98 before anything else noticed. iam v1.34.21 converted thirteen
// addresses, so both numbers move the only way they may.
const (
	typedOps   = 111
	untypedOps = 85
)

// mountApp mounts iam the way plugin/iam does — the whole Mount, so the ledgers below
// read the surface a deployed binary serves and not a test-only subset.
func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("iamtest"), DisableStartupMessage: true})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("iamtest"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

// iamOps reads BOTH projections of the live router at their one shared address form:
// what the document says is served, and which of those carry a typed registry entry.
func iamOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mountApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "iam", Version: "v1"})
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
		// The PROSE, wherever the op put it: iam declares its ops with
		// zip.WithSummary, cloud's own apps carry a zipdoc-lifted description.
		// Either is prose; neither is a bare id.
		typed[key] = strings.TrimSpace(op.Summary + op.Description)
	}
	return served, typed
}

// TestGraftRecoveredTheNestedRegistry is the deliverable, measured: the nested app's
// typed ops are cloud's typed ops, at their own absolute addresses, with their own
// prose — and not one wildcard is left.
func TestGraftRecoveredTheNestedRegistry(t *testing.T) {
	served, typed := iamOps(t)
	if len(served) == 0 {
		t.Fatal("iam serves no operations at all — the mount did not register")
	}

	// A wildcard here means the graft did not happen and something relayed a subtree
	// again. It is the single most important assertion in the file: {wildcardN} is
	// what a host publishes when it cannot see past a closure.
	var wild []string
	for key := range served {
		if strings.Contains(key, "{wildcard") {
			wild = append(wild, key)
		}
	}
	if len(wild) > 0 {
		sort.Strings(wild)
		t.Errorf("iam still publishes %d wildcard operation(s): %s\n"+
			"A wildcard stands in for a surface the host cannot describe. safeMount grafts the app; "+
			"if these are back, something is relaying a subtree through a handler again.",
			len(wild), strings.Join(wild, ", "))
	}

	if len(typed) < typedOps {
		t.Errorf("%d typed operations, want at least %d — the graft lost some of the nested registry",
			len(typed), typedOps)
	}
	if got := len(served) - len(typed); got > untypedOps {
		t.Errorf("%d untyped operations, want at most %d — a raw handler was added in "+
			"github.com/hanzoai/iam. A route that is not a typed op has no schema, no prose, no MCP "+
			"tool, no CLI command and no SDK method; convert it there (zip.Get/Post/... ) and this "+
			"ratchet falls on the next bump.", got, untypedOps)
	}

	// Every typed op must publish real detail, not a bare id. This is what the five
	// wildcards could never carry: a placeholder operation had an operationId and
	// nothing else.
	var bare []string
	for key, prose := range typed {
		if prose == "" {
			bare = append(bare, key)
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("%d grafted op(s) carry no prose: %s", len(bare), strings.Join(bare[:min(5, len(bare))], ", "))
	}
}

// TestGraftedOpsAreAddressedByTheirOwnPaths pins the two decisions that make the
// composed document usable: the child's absolute paths are untouched, and its
// operationIds are untouched. Rewriting either at compose time would make an SDK
// method name a function of where the app is deployed.
func TestGraftedOpsAreAddressedByTheirOwnPaths(t *testing.T) {
	_, typed := iamOps(t)
	for _, want := range []string{
		"GET /v1/iam/keys",             // the key entity iam owns, at iam's own address
		"POST /v1/iam/organizations",   // native REST create
		"GET /v1/iam/users",            // the entity CRUD
		"POST /v1/iam/update-provider", // a legacy verb alias, named by its address
	} {
		if _, ok := typed[want]; !ok {
			t.Errorf("%s is not a typed op in the composed document", want)
		}
	}
}

// TestGraftPublishesTheNestedSchemas: the 94 component schemas arrive too, qualified
// by the app that declared them. Unqualified, iam's Application (an OAuth client, 83
// properties) and the fleet's Application (a hiring application, 16) are one name
// with two shapes, which every generated SDK would bind to whichever it read last.
func TestGraftPublishesTheNestedSchemas(t *testing.T) {
	app := mountApp(t)
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	if len(reg.Schemas) == 0 {
		t.Fatal("the composed document carries no component schemas at all")
	}
	for _, want := range []string{"iam.Application", "iam.Role"} {
		if _, ok := reg.Schemas[want]; !ok {
			t.Errorf("components.schemas has no %q — a grafted type must be qualified by its origin", want)
		}
	}
	for _, unwanted := range []string{"Application", "Role"} {
		if _, ok := reg.Schemas[unwanted]; ok {
			t.Errorf("iam published %q unqualified — it would collide with the fleet's own", unwanted)
		}
	}
}

// TestServingIsUnchanged is the other half of the claim: a graft changes what cloud
// DESCRIBES and nothing about what it SERVES. Each body below is composed inside
// github.com/hanzoai/iam and reaches the caller unchanged — iam's own error shape,
// iam's own minted discovery document.
func TestServingIsUnchanged(t *testing.T) {
	app := mountApp(t)

	// The nested app's OWN Guard envelope reaches the caller unchanged. cloud's error
	// shape is nested under "error"; this one is flat with a numeric "status", which is
	// the tell that the response was composed inside github.com/hanzoai/iam — and it is
	// the proof that the graft carried iam's app.Use(Guard) seam with it rather than
	// copying its routes out from under it.
	status, body := get(t, app, "/v1/iam/users")
	if status != http.StatusUnauthorized {
		t.Errorf("GET /v1/iam/users = %d, want 401 from the nested app's Guard", status)
	}
	var guard struct {
		Status int    `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &guard); err != nil || guard.Status != 401 {
		t.Errorf("GET /v1/iam/users body = %q, want the nested Guard's {\"status\":401,…} envelope (err=%v)", body, err)
	}

	// OIDC discovery: a document minted by the nested app from the deployment's own
	// registered applications and signing certs, at the ISSUER root by spec.
	status, body = get(t, app, "/.well-known/openid-configuration")
	if status != http.StatusOK {
		t.Fatalf("GET /.well-known/openid-configuration = %d, want 200", status)
	}
	if !strings.Contains(body, `"authorization_endpoint"`) {
		t.Errorf("discovery body = %q, want the RFC 8414 metadata the nested app mints", body)
	}

	// And the graft NARROWED the surface rather than widening it: a path under iam's
	// prefix that iam does not declare falls through to cloud instead of reaching
	// iam's 404. The wildcard swallowed the whole subtree; this is what replaced it.
	if status, _ = get(t, app, "/v1/iam/no-such-address-anywhere"); status != http.StatusNotFound {
		t.Errorf("GET an undeclared path under /v1/iam = %d, want 404 from cloud", status)
	}
}

// TestFailClosedCoversEveryMountedAddress is the gate on the two halves agreeing. When
// IAM cannot boot, every address the graft would have served must answer the honest
// JSON 503 — because the terminal handler in every plugin binary is webui.Mount's `/*`
// console catch-all, so an address the degraded half misses does not 404, it answers
// 200 with the SPA's HTML. /.well-known/openid-configuration was exactly that hole: the
// first call every relying party makes, parsing a web page as its discovery document.
//
// The degraded half stays a WILDCARD and that is correct, not an oversight: a child
// that cannot boot has no registry to graft and no declaration to read, so the only
// thing left to state is the prefixes identity owns.
func TestFailClosedCoversEveryMountedAddress(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("iamtest"), DisableStartupMessage: true})
	mountFailClosed(app)

	// Concrete addresses, one per pattern — the bare prefixes included, since fiber's
	// greedy `/*` is what covers them and that is worth pinning rather than assuming.
	for _, p := range []string{
		"/v1/iam",
		"/v1/iam/oauth/token",
		"/v1/iam/.well-known/jwks",
		"/login/oauth",
		"/login/oauth/authorize",
		"/.well-known/openid-configuration",
		"/.well-known/oauth-authorization-server",
	} {
		if status, _ := get(t, app, p); status != http.StatusServiceUnavailable {
			t.Errorf("degraded %s = %d, want 503 (fail-closed, not the console SPA)", p, status)
		}
	}

	// And the set is derived, not listed twice: every pattern the degraded mount uses
	// is a pattern it registered.
	registered := map[string]bool{}
	for _, r := range app.Fiber().GetRoutes(true) {
		registered[r.Path] = true
	}
	for _, p := range patterns() {
		if !registered[p] {
			t.Errorf("mountFailClosed does not cover %q", p)
		}
	}
}

// get drives one request through the live router and returns the status and body.
func get(t *testing.T, app *zip.App, path string) (int, string) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil))
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET %s: read body: %v", path, err)
	}
	return resp.StatusCode, string(body)
}
