package provisioning

// The gate on provisioning's PROJECTION, not on its behaviour.
//
// A typed op is one registry entry with four projections — the OpenAPI operation,
// the MCP tool, the CLI command and every generated SDK method. An operation
// missing from that registry is invisible to all four, and for 28 operations here
// it was: seven kinds × four verbs, registered from a loop over `kinds` with a
// computed path, publishing no description, no summary, no request shape and no
// response shape. The two ledgers below make that measurable instead of asserted:
// every operation this package serves must be either a typed op or one named in
// untypedByDesign with the wire fact that keeps it out, and the reasons must name
// operations that still exist.

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
)

// mountGet and mountDelete register ONE typed op on a throwaway app. They exist
// because zip's registrars are package-level generic FUNCTIONS, not handler
// values: a test that wants to serve one typed route hands over a registration,
// which is what doReq takes.
func mountGet[In, Out any](path string, h zip.TypedHandler[In, Out]) func(*zip.App) {
	return func(a *zip.App) { zip.Get(a, path, h) }
}

func mountDelete[In, Out any](path string, h zip.TypedHandler[In, Out]) func(*zip.App) {
	return func(a *zip.App) { zip.Delete(a, path, h) }
}

// untypedByDesign is the CLOSED list of provisioning operations that are NOT
// typed ops, each with the wire fact that keeps it out. Addresses are written the
// way the DOCUMENT writes them, which is the identity every projection keys on.
//
// All seven are the same fact, once per kind: a create runs the pre-provision
// balance gate and renders a denial through cloud.DenyResource
// (provisioning.go's create), which answers the fleet-wide NESTED
// {"error":{"code","message"}} at 402 or 503. A typed op can only refuse by
// RETURNING an error, which zip renders as its flat {"status","code","error"}
// HTTPError — errorHandler is the only path a typed op's error can take — and
// writing the nested body from inside the op does not escape it either: a nil Out
// makes zip stamp cmp.Or(op.Status, 204) over the 402. Moving the gate into
// middleware does not rescue it, because middleware runs BEFORE the body decode
// and would turn today's 400-on-a-bad-name into a 402.
//
// They do NOT publish nothing: each declares its request and success shapes
// through openapi.Register (provisioning.go's init), so a generated SDK can
// construct the call. What staying untyped costs is exactly the three things
// zip's registry supplies — prose, an MCP tool and a CLI command.
var untypedByDesign = map[string]string{}

const denyReason = "the pre-provision balance gate answers 402/503 through cloud.DenyResource, " +
	"whose body is the fleet's NESTED {\"error\":{code,message}}. A typed op's error can only leave " +
	"through zip's errorHandler as a FLAT {status,code,error}, and writing the nested body from " +
	"inside the op does not escape it (a nil Out makes zip stamp cmp.Or(op.Status, 204) over the " +
	"402). Gating in middleware would move the 400-on-a-bad-name to a 402."

func init() {
	for _, kind := range kinds {
		untypedByDesign["POST /v1/provisioning/"+kind] = denyReason
	}
}

// surfaceApp mounts the WHOLE provisioning surface through the REAL routes(), so
// the assertions below read the router the document is generated from rather than
// a reconstruction of it — a route added anywhere in that mount shows up here
// without anyone remembering to list it.
func surfaceApp(t *testing.T) *zip.App {
	t.Helper()
	s, _ := newTestService(t, "sql")
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	routes(app, s)
	return app
}

// provisioningOps reads BOTH projections of the live router at their one shared
// address form: what the document says is served, and which of those carry a
// typed registry entry. Reading the router (not the source) is what makes this a
// gate rather than prose.
func provisioningOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := surfaceApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "provisioning", Version: "v1"})
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

// TestEveryRouteIsTypedOrNamed fails when a provisioning operation is neither a
// typed op nor one named above — so the next route added here is typed by
// default, and dropping one out of the registry takes a deliberate edit with a
// reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := provisioningOps(t)

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
			"A route that is not a typed op has no prose, no MCP tool, no CLI command and no SDK "+
			"method. Convert it (zip.Get/Delete/... in mountTyped), or add it to untypedByDesign with "+
			"the wire fact that typing it would move.", strings.Join(untyped, ", "))
	}
	// The reasons must describe operations that exist, or the list is stale prose.
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which provisioning no longer serves", key)
		}
	}
	// The two ledgers must SUM to the served surface, so neither can grow by
	// swallowing the other.
	if len(typed)+len(untypedByDesign) != len(served) {
		t.Errorf("typed %d + named %d != served %d", len(typed), len(untypedByDesign), len(served))
	}
}

// TestEveryTypedOpIsDescribed holds the prose to the same bar as the schema,
// because that prose IS the product surface: it becomes the OpenAPI description
// AND the MCP tool description a model reads to pick the tool. zipdoc_gen.go is
// what carries it into the binary, so an op added without regenerating shows up
// here as a nameless tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := provisioningOps(t)
	// 7 kinds × list/get/delete, plus the operator's two whole-backend reads of
	// the vector store this app allocates into (inventory.go).
	if len(typed) != 23 {
		t.Errorf("typed ops = %d, want 23 (7 kinds × list/get/delete, + 2 operator reads)", len(typed))
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/provisioning/...", key)
		}
	}
}

// TestTypedReadsKeepTheirWire pins the three wire facts the conversion could have
// moved and a status-code test would not have caught: an empty listing is an
// empty JSON ARRAY (not null, and not an object wrapping one), a delete answers
// 204 with a genuinely EMPTY body, and an unknown name is a 404 either way.
func TestTypedReadsKeepTheirWire(t *testing.T) {
	s, _ := newTestService(t, "sql")

	resp := doReq(t, mountGet("/v1/sql", ops{s}.listSQL), http.MethodGet, "/v1/sql", "acme", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "[]" {
		t.Errorf("empty listing body = %q, want `[]` — a null or an envelope is a wire break", body)
	}
	var rows []provisionedSummary
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Errorf("listing does not decode as an array: %v", err)
	}

	resp = doReq(t, mountDelete("/v1/sql/:name", ops{s}.dropSQL), http.MethodDelete, "/v1/sql/nope", "acme", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("delete of an unknown name = %d, want 404", resp.StatusCode)
	}
}

// TestTypedReadsFailClosedWithoutAPrincipal is the RED-HIGH gate carried across
// the conversion. A caller that forges X-Org-Id but presents NO validated
// principal (no X-User-Id) — the SanitizeIdentity "Phase-1 data path" residual an
// in-cluster pod could send with no bearer — must be refused 403. The typed ops
// resolve their tenant through the SAME tenant() the create uses (tenantOf,
// typed.go), which is what makes this hold for both halves at once.
func TestTypedReadsFailClosedWithoutAPrincipal(t *testing.T) {
	s, _ := newTestService(t, "sql")
	for _, tc := range []struct {
		name   string
		mount  func(*zip.App)
		method string
		path   string
	}{
		{"list", mountGet("/v1/sql", ops{s}.listSQL), http.MethodGet, "/v1/sql"},
		{"get", mountGet("/v1/sql/:name", ops{s}.getSQL), http.MethodGet, "/v1/sql/orders"},
		{"delete", mountDelete("/v1/sql/:name", ops{s}.dropSQL), http.MethodDelete, "/v1/sql/orders"},
	} {
		app := zip.New(zip.Config{DisableStartupMessage: true})
		app.Use(cloud.Bridge())
		tc.mount(app)
		req, _ := http.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("X-Org-Id", "victim") // forged, with no X-User-Id
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("%s: Test: %v", tc.name, err)
		}
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s with a forged org and no principal = %d, want 403", tc.name, resp.StatusCode)
		}
	}
}

// TestTypedReadsRefuseOffTheHTTPPath: cloud.Request is absent when an op is
// invoked by name (the CLI's LocalInvoke), and an org-scoped read has no honest
// answer there. It must refuse rather than fall through to an empty-org query,
// which would be a cross-tenant read of whatever the empty org bucket holds.
func TestTypedReadsRefuseOffTheHTTPPath(t *testing.T) {
	s, _ := newTestService(t, "sql")
	o := ops{s}
	if _, err := o.listSQL(t.Context(), &noInput{}); err == nil {
		t.Error("listSQL with no request answered instead of refusing")
	}
	if _, err := o.getSQL(t.Context(), &resourceRef{Name: "orders"}); err == nil {
		t.Error("getSQL with no request answered instead of refusing")
	}
	if _, err := o.dropSQL(t.Context(), &resourceRef{Name: "orders"}); err == nil {
		t.Error("dropSQL with no request answered instead of refusing")
	}
}

// TestTheCreatesStillDeclareTheirBodies: the seven refusals must not publish
// NOTHING. openapi.Register (provisioning.go's init) gives each create the body
// it reads and the shape it answers, so a generated SDK has somewhere to put the
// name — the one thing a route can lose by staying untyped that is NOT one of
// zip's three registry projections.
func TestTheCreatesStillDeclareTheirBodies(t *testing.T) {
	app := surfaceApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "provisioning", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	for _, kind := range kinds {
		op := doc.Paths["/v1/provisioning/"+kind]["post"]
		if op == nil {
			t.Fatalf("POST /v1/provisioning/%s is not in the document at all", kind)
		}
		// RequestBody is `any` on the shared Operation — the untyped seam and the
		// typed fold produce different (JSON-identical) shapes — so assert on the
		// one Register builds.
		rb, ok := op.RequestBody.(*openapi.RequestBody)
		if !ok {
			t.Errorf("POST /v1/provisioning/%s publishes no request body (%T) — an SDK caller has nowhere to put the name", kind, op.RequestBody)
			continue
		}
		if _, ok := rb.Content["application/json"]; !ok {
			t.Errorf("POST /v1/provisioning/%s request body is not application/json", kind)
		}
	}
	for _, name := range []string{"provisionRequest", "provisionResult"} {
		if doc.Components == nil || doc.Components.Schemas[name] == nil {
			t.Errorf("schema %q is not published", name)
		}
	}
}

// proseless is the CLOSED list of published properties that carry NO description
// because the SEAM they arrived through cannot carry one — not because nobody
// wrote it.
//
// REFLECTION SEAM. The seven creates stay untyped for the wire reason typed.go
// states, and declare their shapes through openapi.Register (provisioning.go's
// init) instead. Register keeps a reflect.Type (openapi/register.go:150) and
// builds the schema from it; Go drops comments at compile time, so the only
// prose it could publish is prose reflection can see, which is none. zipdoc
// cannot fill the gap from the other side either: it walks zip's TYPED
// registrations, and these two types are no typed op's In or Out, so nothing is
// lifted under their names at all.
//
// Both types ARE documented at their declaration, field by field, and that prose
// is what a reader of provisioning.go gets. Not worked around: hand-writing a
// schema beside the struct replaces one true statement with two that can drift,
// and typing the creates to reach zipdoc would change the wire — a 402 denial
// would start rendering as zip's flat error body.
//
// Exact in BOTH directions: a bare property anywhere else goes red, and an entry
// here that starts publishing prose goes red too, which is the day the seam
// learns and this ledger must shrink rather than outlive the gap.
var proseless = map[string]bool{
	"provisionRequest.instance":        true,
	"provisionRequest.name":            true,
	"provisionResult.connectionString": true,
	"provisionResult.database":         true,
	"provisionResult.host":             true,
	"provisionResult.id":               true,
	"provisionResult.kind":             true,
	"provisionResult.name":             true,
	"provisionResult.password":         true,
	"provisionResult.port":             true,
	"provisionResult.status":           true,
	"provisionResult.username":         true,
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the gates
// above cannot see. Typing a route documents its ADDRESS and its SHAPE; the
// shape's FIELDS come from a different place — a doc comment on each one, which
// zipdoc lifts one at a time.
//
// It matters here because provisioning splits sharply into what a caller SETS
// and what the server MINTS, and the shapes do not say which is which: a create
// body is `name` and `instance` and nothing else, while `id`, `host`, `port`,
// `database` and the credential all come back minted — `database` in particular
// is NOT `name`, because a fixed-width org hash namespaces it so two tenants
// cannot fold onto one backend resource. `status` is a two-word vocabulary where
// "provisioning" means the 201 arrived before the instance did. And
// `connectionString` and `password` are answered ONCE, on this response and
// nowhere else, which a caller who plans to re-read them finds out too late.
//
// Presence is all a gate can check, and it is checked against the ledger above.
// A description restating the field's name is worse than none, and only a reader
// catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(surfaceApp(t), openapi.Info{Title: "provisioning", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("provisioning publishes no schemas at all — the gate would pass vacuously")
	}
	published, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}

	var bare, stale []string
	seen := map[string]bool{}
	for _, path := range published {
		seen[path] = true
		if !proseless[path] {
			bare = append(bare, path)
		}
	}
	for path := range proseless {
		if !seen[path] {
			stale = append(stale, path)
		}
	}

	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/provisioning describe",
			len(bare), strings.Join(bare, ", "))
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("proseless names propert(ies) that are gone or now described: %s\n"+
			"An exemption that outlives its cause is how a generator gap becomes permanent — "+
			"delete the entr(ies).", strings.Join(stale, ", "))
	}
}
