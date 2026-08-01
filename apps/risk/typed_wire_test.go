package risk

// This file makes "every route is a typed op" a GATE instead of a paragraph.
// Prose cannot fail: a route added tomorrow as a raw func(*zip.Ctx) error would
// leave the claim standing and the route invisible to the document, the MCP tool
// list, the CLI and every generated SDK. Here the claim is a test, so the route
// that falsifies it says so.

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// untypedByDesign is the CLOSED list of risk operations that are NOT typed ops,
// each with the WIRE fact that keeps it raw. A typed op is a route PLUS a
// registry entry — the one value the OpenAPI operation, the MCP tool, the CLI
// command and the SDK method all come from — so an operation missing from that
// registry is invisible to all four. This one is missing on purpose. The address
// is written the way the DOCUMENT writes it, which is the identity every
// projection keys on.
var untypedByDesign = map[string]string{
	// The real probe. It answers 503 CARRYING THE DEGRADED REPORT as its body
	// (status/model/tenants/warehouse/billing), which is the whole point of it: a
	// probe that says only "unhealthy" tells an operator nothing about which of
	// the four planes is down. A typed op reaches a non-2xx only by returning an
	// error, and zip renders that as its own envelope, dropping the report.
	"GET /v1/risk/health": healthWire,
}

const healthWire = "a REAL probe: 503 carries the degraded REPORT as its body " +
	"(status/model/tenants/warehouse/billing), which is the whole point of it. A typed op reaches a " +
	"non-2xx only by returning an error, and zip renders that as its own envelope, dropping the report."

// The two ops that COULD have been forced untyped, and were not — recorded here
// so a reviewer can check the reasoning and not only the list.
//
// POST /v1/risk/decide is TYPED AND IS NEVER BALANCE-GATED. cloud.DenyResource
// renders a pre-work refusal as the fleet's NESTED {"error":{"code","message"}}
// 402 with c.JSON, and a typed op's returned error renders as the FLAT
// {"status","code","error"} envelope — so a gated decide would either change the
// 402 body every balance-aware client parses, or have to be untyped. It meters
// AFTER instead: the screen is billed on the decision that was actually
// produced, the hot path stays typed, and no existing client's parse moves.
//
// POST /v1/ml/train and POST /v1/ml/search ARE gated before the work, because
// both are real CPU on a shared pod. They stay typed and return a typed 402:
// they are NEW routes, so no client parses a nested body from them, and a new
// route may as well carry the contract we want rather than the one we inherited.

// mountApp mounts risk the way plugin/risk does — the whole Mount, so the
// projection ledgers below read the surface a deployed binary serves and not a
// test-only subset.
func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("risktest"), DisableStartupMessage: true})
	if err := Mount(app, cloud.Deps{
		Logger: luxlog.New("risktest"), DataDir: t.TempDir(), Brand: "hanzo",
	}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

// riskOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed
// registry entry. EVERY served operation counts, so a route mounted at an
// address nobody expected is caught rather than filtered out.
func riskOps(t *testing.T) (served map[string]bool, typed map[string]string, schemas map[string]any) {
	t.Helper()
	app := mountApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "risk", Version: "v1"})
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

// TestEveryRouteIsTypedOrNamed fails when a risk operation is neither a typed op
// nor named above — so the next route added here is typed by default, and
// dropping one out of the registry takes a deliberate edit carrying a reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed, _ := riskOps(t)

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
			"SDK method. Convert it (zip.Get/Post/... on the /v1/risk or /v1/ml group), or add it to "+
			"untypedByDesign with the reason typing it would move the wire.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which risk no longer serves", key)
		}
		if _, ok := typed[key]; ok {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
	if got := len(typed) + len(untypedByDesign); got != len(served) {
		t.Errorf("%d typed + %d named = %d, but risk serves %d operations",
			len(typed), len(untypedByDesign), got, len(served))
	}
}

// TestTheSurfaceIsWhatWasPromised pins the operations this app exists to serve.
// A rename or a silent drop is a broken SDK for every caller, and a diff of a
// list is the only thing that catches it before they do.
func TestTheSurfaceIsWhatWasPromised(t *testing.T) {
	served, _, _ := riskOps(t)
	want := []string{
		// decide
		"POST /v1/risk/decide",
		// record
		"GET /v1/risk/decisions",
		"GET /v1/risk/decisions/{id}",
		"POST /v1/risk/decisions/{id}/label",
		"GET /v1/risk/subjects/{kind}/{id}",
		"GET /v1/risk/activity",
		// govern
		"POST /v1/risk/simulate",
		"GET /v1/risk/rules", "POST /v1/risk/rules",
		"PATCH /v1/risk/rules/{id}", "DELETE /v1/risk/rules/{id}",
		"GET /v1/risk/lists", "POST /v1/risk/lists",
		"POST /v1/risk/lists/{name}/entries",
		"DELETE /v1/risk/lists/{name}/entries/{value}",
		"GET /v1/risk/suppressions", "POST /v1/risk/suppressions",
		"DELETE /v1/risk/suppressions/{id}",
		"GET /v1/risk/controls", "POST /v1/risk/controls",
		"DELETE /v1/risk/controls/{id}",
		"GET /v1/risk/dictionary",
		"GET /v1/risk/mode", "PUT /v1/risk/mode",
		"GET /v1/risk/health",
		// learn
		"POST /v1/ml/score",
		"POST /v1/ml/train",
		"GET /v1/ml/state", "PUT /v1/ml/state/appetite",
		"GET /v1/ml/features",
		"POST /v1/ml/search", "GET /v1/ml/search/{id}",
		"POST /v1/ml/snapshot", "POST /v1/ml/restore",
		// the model's lifecycle: the registry, the schedule and drift
		"POST /v1/ml/fits", "GET /v1/ml/fits", "GET /v1/ml/fits/{id}",
		"POST /v1/ml/fits/{id}/cancel", "PUT /v1/ml/fits/{id}/role",
		"GET /v1/ml/fits/{id}/tally",
		"GET /v1/ml/schedule", "PUT /v1/ml/schedule",
		"GET /v1/ml/drift",
	}
	for _, w := range want {
		if !served[w] {
			t.Errorf("%s is not served — it is in the contract and not on the router", w)
		}
	}
	if len(served) != len(want) {
		var extra []string
		for k := range served {
			if !containsString(want, k) {
				extra = append(extra, k)
			}
		}
		sort.Strings(extra)
		t.Errorf("risk serves %d operations, the contract names %d; unlisted: %s",
			len(served), len(want), strings.Join(extra, ", "))
	}
}

func containsString(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// TestEveryTypedOpIsDescribed proves the lifted prose reached the binary. That
// prose IS the product surface: it becomes the OpenAPI description AND the MCP
// tool description a model reads to pick the tool. zipdoc_gen.go is what carries
// it in, so an op added without regenerating shows up here as a nameless tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed, _ := riskOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed risk ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/risk/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the op-level
// gate cannot see. Typing a route documents its ADDRESS and its SHAPE; it does
// not document the shape's FIELDS, and those come from doc comments on the In/Out
// struct fields, which zipdoc lifts per field.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	_, _, schemas := riskOps(t)
	if len(schemas) == 0 {
		t.Fatal("no risk schemas in the typed registry at all")
	}
	var bare []string
	for name, raw := range schemas {
		sch, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		props, ok := sch["properties"].(map[string]any)
		if !ok {
			continue
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
			"Every field of a published schema is read by SDK users and by a model choosing a tool. "+
			"Write a doc comment on the struct field and run: go generate -run zipdoc ./apps/risk/...",
			strings.Join(bare, ", "))
	}
}

// TestEverySchemaNameCarriesItsFace guards the ONE failure mode the fleet weave
// cannot recover from. openapi.Weave refuses one schema name with two shapes
// across apps because a generated SDK binds whichever it read last — and this
// app introduces forty types into a namespace already flat across the whole
// fleet. `ref`, `list`, `page` and `state` are exactly the names the next app
// reaches for.
func TestEverySchemaNameCarriesItsFace(t *testing.T) {
	_, _, schemas := riskOps(t)
	for name := range schemas {
		if strings.HasPrefix(name, "risk") || strings.HasPrefix(name, "ml") {
			continue
		}
		t.Errorf("schema %q carries no face — prefix it risk* or ml*, or the fleet weave will "+
			"eventually refuse it against another app's type of the same name", name)
	}
}
