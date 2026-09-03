package plan

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/goja"
	"github.com/hanzoai/cloud/openapi"
	hplans "github.com/hanzoai/plans"
)

// This file is the PROOF that typing the fifteen /v1/plan operations did not
// move their wire, and the GATE that keeps the surface typed.
//
// It is a proof rather than a claim because it re-derives the pre-typing answer
// on every run instead of trusting a recorded golden. What the raw handlers wrote
// was exactly the bundle's status and the bundle's bytes (dispatch → c.Bytes), so
// the assertion is the whole contract: for every address, the LIVE typed route's
// status and body must equal what the bundle hands back for the same route and
// the same tenant. If a future Out reorders a key, renames a field, drops one the
// catalog added, or turns a 404 into something else, this fails.

// compose installs what a host installs. A subsystem never installs cloud.Bridge:
// the program's composer owns it — serve.go at the root of the fused host, the
// plugin constructor for a plugin program. In a test the test is the composer, so
// it owes the same install; skipping it drives a program where every org-scoped op
// answers 403 for a reason production callers never see.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

// planApp mounts the REAL subsystem — the real @hanzo/plans bundle and catalog —
// on a bare zip app. compose is what makes the identity path here the production
// one: the test is the composer, so it installs cloud.Bridge at the root and
// principal.OrgFrom resolves the same validated org a typed op sees behind Serve.
func planApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	t.Setenv("CLOUD_BRAND", "hanzo")
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return app
}

// bundleAnswer is the bundle's own status and bytes for one route and tenant —
// what the raw pass-through wrote, re-derived rather than recorded. It runs on a
// SEPARATE host so it cannot be the very host the route under test used.
func bundleAnswer(t *testing.T, route, tenant string, params map[string]string) (int, []byte) {
	t.Helper()
	bundle, err := hplans.Bundle()
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	data, err := hplans.Data()
	if err != nil {
		t.Fatalf("Data: %v", err)
	}
	h, err := goja.New(goja.Config{Name: "plans", Bundle: bundle, Globals: map[string]any{"__PLANS_DATA__": data}})
	if err != nil {
		t.Fatalf("goja.New: %v", err)
	}
	defer h.Close()
	resp, err := h.Dispatch(context.Background(), goja.Request{Route: route, Params: params, Tenant: tenant})
	if err != nil {
		t.Fatalf("Dispatch(%s): %v", route, err)
	}
	return resp.Status, resp.Body
}

// sectionRoutes maps each catalog address to the @hanzo/plans bundle route it
// serves — the same pairing Mount declares, written once more here so the test
// names the wire it checks rather than reaching into the router.
var sectionRoutes = map[string]string{
	"/v1/plan":               "plans",
	"/v1/plan/subscriptions": "subscriptions",
	"/v1/plan/blockchain":    "blockchain",
	"/v1/plan/dns":           "dns",
	"/v1/plan/gpu":           "gpu",
	"/v1/plan/regions":       "regions",
	"/v1/plan/storage":       "storage",
	"/v1/plan/tools":         "tools",
	"/v1/plan/policy":        "policy",
	"/v1/plan/schema":        "schema",
	"/v1/plan/vocab":         "vocab",
}

// TestSectionsAreByteIdenticalToTheBundle drives every catalog address on the
// live router and asserts its body is byte-for-byte the bundle's own answer.
//
// Three identity shapes, because the tenant is what selects a catalog and it must
// come from the VALIDATED principal and nowhere else: anonymous and a forged
// X-Org-Id with no validated user behind it must both read the public "hanzo"
// catalog, and a validated member org must read its own. A forged header that
// moved the answer would be a cross-tenant read the caller asserted for itself.
func TestSectionsAreByteIdenticalToTheBundle(t *testing.T) {
	fa := planApp(t).Fiber()

	ids := []struct {
		name   string
		tenant string
		hdr    map[string]string
	}{
		{"anonymous", "hanzo", nil},
		{"forged-org", "hanzo", map[string]string{"X-Org-Id": "acme-reseller"}},
		{"member", "acme-reseller", map[string]string{"X-Org-Id": "acme-reseller", "X-User-Id": "u_acme"}},
	}

	for path, route := range sectionRoutes {
		for _, id := range ids {
			wantStatus, wantBody := bundleAnswer(t, route, id.tenant, nil)
			if wantStatus != http.StatusOK {
				t.Fatalf("%s: bundle answered %d for %q — the shipped catalog holds every section",
					path, wantStatus, route)
			}
			req := httptest.NewRequest(http.MethodGet, path, nil)
			for k, v := range id.hdr {
				req.Header.Set(k, v)
			}
			resp, err := fa.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
			if err != nil {
				t.Fatalf("GET %s (%s): %v", path, id.name, err)
			}
			got, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != wantStatus {
				t.Errorf("GET %s (%s): status %d, bundle says %d", path, id.name, resp.StatusCode, wantStatus)
			}
			if !bytes.Equal(got, wantBody) {
				t.Errorf("GET %s (%s): body moved.\n typed: %s\nbundle: %s", path, id.name, got, wantBody)
			}
		}
	}
}

// TestResolutionIsByteIdenticalForEveryPlanInTheCatalog is the exhaustive half:
// the two parameterised addresses are driven over EVERY id the shipped catalog
// holds, plus the two failure ids, so "the Out reproduces the bundle's bytes" is
// measured across the whole catalog rather than sampled.
//
// It is what justifies typing `id` as a string and `license_features` as a string
// list instead of relaying them as raw JSON: if any record resolved to a null id
// or a null feature list, the round trip through those Go types would move the
// wire and this would say so.
func TestResolutionIsByteIdenticalForEveryPlanInTheCatalog(t *testing.T) {
	fa := planApp(t).Fiber()

	ids := catalogIDs(t)
	if len(ids) < 10 {
		t.Fatalf("catalog holds %d plan ids, expected the shipped ladder — the fixture is not loading", len(ids))
	}
	// The failure ids belong in the same sweep: a 404 and a 400 are answers this
	// surface gives, and a typed op reaches them through a different path (an
	// error) than a 200.
	ids = append(ids, "does-not-exist")

	for _, route := range []struct{ path, bundle string }{
		{"/v1/plan/resolve/", "resolve"},
		{"/v1/plan/entitlements/", "entitlements"},
	} {
		for _, id := range ids {
			wantStatus, wantBody := bundleAnswer(t, route.bundle, "hanzo", map[string]string{"id": id})
			resp, err := fa.Test(httptest.NewRequest(http.MethodGet, route.path+id, nil),
				fiber.TestConfig{Timeout: 30 * time.Second})
			if err != nil {
				t.Fatalf("GET %s%s: %v", route.path, id, err)
			}
			got, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != wantStatus {
				t.Errorf("GET %s%s: status %d, bundle says %d", route.path, id, resp.StatusCode, wantStatus)
			}
			if wantStatus == http.StatusOK {
				if !bytes.Equal(got, wantBody) {
					t.Errorf("GET %s%s: body moved.\n typed: %s\nbundle: %s", route.path, id, got, wantBody)
				}
				continue
			}
			// The recorded delta, pinned: a non-200 keeps the bundle's status and
			// the bundle's message under the bundle's own key, and gains zip's
			// `status` field because returning an error is the only way a typed op
			// can refuse. If that stops being the ONLY difference, this fails.
			assertBundleError(t, route.path+id, got, wantStatus, wantBody)
		}
	}
}

// assertBundleError pins what typing did to the error path: it did not move the
// message and it did not move the status. What it DID move is the vocabulary — a
// refusal is an RFC 9457 problem document, so the sentence the bundle files under
// `error` is filed under `detail`, beside the registered `type`, `title` and
// `status`. The property is the same one; only the member names changed.
func assertBundleError(t *testing.T, addr string, got []byte, wantStatus int, wantBody []byte) {
	t.Helper()
	var bundleErr struct{ Error string }
	if err := json.Unmarshal(wantBody, &bundleErr); err != nil || bundleErr.Error == "" {
		t.Fatalf("%s: bundle's %d body is not {\"error\":…}: %s", addr, wantStatus, wantBody)
	}
	var typedErr struct {
		Status int    `json:"status"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(got, &typedErr); err != nil {
		t.Fatalf("%s: typed %d body is not JSON: %s", addr, wantStatus, got)
	}
	if typedErr.Detail != bundleErr.Error {
		t.Errorf("%s: message moved: typed %q, bundle %q", addr, typedErr.Detail, bundleErr.Error)
	}
	if typedErr.Status != wantStatus {
		t.Errorf("%s: the refusal's status field = %d, want %d", addr, typedErr.Status, wantStatus)
	}
	// And nothing ELSE was added: the four registered members and no extension
	// member, because this refusal carries no domain detail beyond its sentence.
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(got, &keys); err != nil {
		t.Fatalf("%s: %v", addr, err)
	}
	for _, k := range []string{"type", "title", "status", "detail"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("%s: problem document is missing %q: %s", addr, k, got)
		}
	}
	if len(keys) != 4 {
		t.Errorf("%s: error body carries %d keys, want exactly the four registered members: %s", addr, len(keys), got)
	}
}

// catalogIDs is every plan id the shipped catalog holds — read from the embedded
// data, so a catalog bump widens the sweep instead of leaving new tiers untested.
func catalogIDs(t *testing.T) []string {
	t.Helper()
	data, err := hplans.Data()
	if err != nil {
		t.Fatalf("Data: %v", err)
	}
	seen := map[string]bool{}
	for _, file := range []string{"subscription.json", "plans.json"} {
		arr, ok := data[file].([]any)
		if !ok {
			t.Fatalf("%s is not a plan array", file)
		}
		for _, e := range arr {
			obj, ok := e.(map[string]any)
			if !ok {
				continue
			}
			for _, k := range []string{"id", "slug"} {
				if s, ok := obj[k].(string); ok && s != "" {
					seen[s] = true
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// TestContentTypeIsTheTypedWriters records the OTHER delta typing introduced, so
// it can never move again unnoticed: the raw pass-through set a bare
// "application/json"; the typed writer sets "application/json; charset=utf-8",
// which is what the health probe and every zip error on this surface already
// sent. JSON is UTF-8 by definition (RFC 8259) and `charset` is not a registered
// parameter of application/json, so no parser reads it — but it IS a byte on the
// wire, and an unrecorded byte is how a delta becomes a surprise.
func TestContentTypeIsTheTypedWriters(t *testing.T) {
	fa := planApp(t).Fiber()
	for _, path := range []string{"/v1/plan", "/v1/plan/vocab", "/v1/plan/health", "/v1/plan/resolve/pro"} {
		resp, err := fa.Test(httptest.NewRequest(http.MethodGet, path, nil), fiber.TestConfig{Timeout: 30 * time.Second})
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
			t.Errorf("GET %s: Content-Type %q, want %q", path, ct, "application/json; charset=utf-8")
		}
	}
}

// TestHealthIsUnchanged pins the one route that never touched the bundle: the
// bytes and the status are what the raw handler's map produced, keys sorted.
func TestHealthIsUnchanged(t *testing.T) {
	fa := planApp(t).Fiber()
	resp, err := fa.Test(httptest.NewRequest(http.MethodGet, "/v1/plan/health", nil), fiber.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("GET /v1/plan/health: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	const want = `{"service":"plans","status":"ok"}`
	if resp.StatusCode != http.StatusOK || string(got) != want {
		t.Errorf("health = %d %s, want 200 %s", resp.StatusCode, got, want)
	}
}

// ---- the gate ----

// planOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed
// registry entry. The app mounts this subsystem and nothing else, so every path
// in the document is this surface's.
func planOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := planApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "plan", Version: "v1"})
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
	return served, typed
}

// TestEveryRouteIsTypedOrNamed fails when a /v1/plan operation is not a typed
// op. The list of exceptions is EMPTY and there is no map to add one to on
// purpose: nothing on this surface has a wire fact that resists typing — every
// address is a GET that relays one bundle route — so the next route added here is
// typed by default, and dropping one out of the registry turns this red instead
// of quietly costing it its schema, its prose, its MCP tool and its SDK method.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := planOps(t)
	if len(served) != 14 {
		t.Errorf("the surface serves %d operations, expected 14 — if that is a deliberate "+
			"addition, type it and update this count", len(served))
	}
	var untyped []string
	for key := range served {
		if _, ok := typed[key]; !ok {
			untyped = append(untyped, key)
		}
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("operation(s) with no registry entry: %s\n"+
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command "+
			"and no SDK method. Convert it (zip.Get on the app, ops.go).", strings.Join(untyped, ", "))
	}
}

// TestEveryTypedOpIsDescribed fails on a typed op with no lifted prose, because
// that prose IS the product surface: it becomes the OpenAPI description AND the
// MCP tool description a model reads to pick the tool. zipdoc_gen.go is what
// carries it into the binary, so an op added without regenerating shows up here
// as a nameless tool rather than in production.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := planOps(t)
	if len(typed) != 14 {
		t.Fatalf("%d typed plan ops in the registry, want 14", len(typed))
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/plan/...", key)
		}
	}
}

// TestEveryOpIsAnMCPToolWithADescription checks the OTHER projection of the same
// registry entry, because a description that reaches the document and not the
// tool list is the exact failure the fleet already shipped once: zipdoc ran, the
// spec got its prose, and every MCP tool still served an empty description over a
// schema whose fields said nothing. A model picks a tool by reading that string,
// so an empty one makes the op unreachable to an agent however good the OpenAPI
// looks.
func TestEveryOpIsAnMCPToolWithADescription(t *testing.T) {
	tools := planApp(t).MCPTools()
	byName := map[string]string{}
	for _, tool := range tools {
		n, _ := tool["name"].(string)
		d, _ := tool["description"].(string)
		byName[n] = d
	}
	if len(byName) != 14 {
		t.Fatalf("%d MCP tools, want 14 — one per op", len(byName))
	}
	for name, desc := range byName {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("MCP tool %q has an empty description", name)
		}
	}
}

// TestOpaqueCatalogValuesPublishAnyJSON pins the reason every catalog value is
// json.RawMessage and not map[string]any: zip asks whether a type marshals itself
// BEFORE it asks what the type is made of, so a RawMessage publishes `{}` — "any
// JSON", the only true thing to say about a value @hanzo/plans owns the shape of.
// A map[string]any would publish `additionalProperties: {"type":"object"}`, which
// asserts every value is an object and is refuted by the first `"priceMonthly":
// 20` in the catalog.
//
// If zip regresses to describing a RawMessage by what it is made of (an array of
// integers, which is what a []byte is), this fails here rather than shipping a
// schema no client can satisfy into every generated SDK.
func TestOpaqueCatalogValuesPublishAnyJSON(t *testing.T) {
	app := planApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "plan", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	got, err := json.Marshal(doc.Components.Schemas["planSchemas"])
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	for _, want := range []string{`"entitlements":{"description":`, `"plan":{"description":`} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("planSchemas lost a described property: %s", got)
		}
	}
	// The described property must carry NO type constraint — description only.
	var s struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(got, &s); err != nil {
		t.Fatalf("%v", err)
	}
	for name, prop := range s.Properties {
		if len(prop) != 1 {
			t.Errorf("planSchemas.%s publishes %v — a json.RawMessage is unconstrained "+
				"JSON, and any added constraint is a claim about a shape @hanzo/plans owns", name, prop)
		}
	}
}

// TestTheTenantIsNeverAnInputField is the identity gate, read off the published
// document rather than off the source: not one operation on this surface may
// declare an org/tenant parameter. A tenant that arrived as an In field would be
// caller-supplied — a cross-tenant catalog read the caller asserted for itself —
// and the document is where such a field would become visible to every SDK.
func TestTheTenantIsNeverAnInputField(t *testing.T) {
	app := planApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "plan", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	banned := []string{"org", "org_id", "orgid", "tenant", "tenant_id", "tenantid"}
	for path, item := range doc.Paths {
		for method, op := range item {
			for _, p := range op.Parameters {
				for _, b := range banned {
					if strings.EqualFold(p.Name, b) {
						t.Errorf("%s %s declares a %q parameter — the tenant comes from the "+
							"validated principal (principal.OrgFrom), never from the caller",
							strings.ToUpper(method), path, p.Name)
					}
				}
			}
			if op.RequestBody != nil {
				t.Errorf("%s %s publishes a request body — every operation here is a GET",
					strings.ToUpper(method), path)
			}
		}
	}
}
