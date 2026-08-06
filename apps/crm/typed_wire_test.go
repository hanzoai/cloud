package crm

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// This file makes crm's typed partition a GATE instead of a paragraph. The count
// "19 of 20" was prose, and prose cannot fail: a route added tomorrow as a raw
// func(*zip.Ctx) error would leave the claim standing and the route invisible to
// every projection. Here the claim is a test, so the route that falsifies it says
// so.

// untypedByDesign is the CLOSED list of crm operations that are NOT typed ops,
// each with the wire fact that keeps it raw. A typed op is a route PLUS a registry
// entry — the one value the OpenAPI operation, the MCP tool, the CLI command and
// the SDK method all come from — so an operation missing from that registry is
// invisible to all four. This one is missing on purpose. The address is written
// the way the DOCUMENT writes it, which is the identity every projection keys on.
var untypedByDesign = map[string]string{
	// The public Startup Program intake. Two guards on this route are WIRE, and
	// both are defeated by projection rather than by declaration — which is why
	// the refusal is not "not looked at yet" but a named zip gap.
	//
	// The refusal is anchored to the NEWEST zip, not the pinned one: cloud is on
	// v1.18.6 and every line below was re-read in v1.18.8, which is the latest
	// published. v1.18.8 adds ask/declare/ops/peer/tenant and changes none of the
	// five facts, so 19 is still this package's honest floor and the next agent
	// here does not have to redo the reading.
	//
	// 1. THE RATE LIMIT IS A PROJECTION HOLE, NOT A DOCUMENTATION ONE. The limit
	//    is fiber middleware (routes(), middleware.RateLimit — keyed on a value
	//    that is not actually the client; see intakeRateLimit for that separate,
	//    live defect, which weakens the meter but does not move this refusal,
	//    since a projection bypasses the meter whatever it is keyed on).
	//    zip's MCP arm dispatches a tools/call straight into op.invoke
	//    (v1.18.8 mcp.go:152) and the CLI's LocalInvoke does the same
	//    (cli.go:427) — neither runs the route's middleware chain, and zip has no
	//    per-op way to decline a projection (the only OpOptions are still
	//    WithSummary, WithTags, WithOperationID and WithStatus, v1.18.8
	//    typed.go:80/83/86/110; MCP.Disabled is app-wide, zip.go:131).
	//    So typing this route publishes an UNMETERED alias of the one
	//    deliberately metered public write in the surface. It is worse than
	//    unmetered: apply() never calls tenant() — it writes into intakeOrg(s),
	//    the deployment BRAND's pipeline — so the alias would let any caller
	//    reaching /mcp inject unbounded rows into the brand's own CRM.
	// 2. The 64 KiB cap (maxIntakeBody) is checked on the RAW body before any
	//    parse. op.invoke json.Unmarshals before the handler runs (v1.18.8
	//    typed.go:234, ahead of fn at :259), so a typed op could only apply the
	//    cap after the parse it exists to prevent.
	//
	// Its 200-vs-201 split (idempotent refresh vs create) is the ordinary
	// conditional-status shape and would shim fine; the honeypot's third body
	// (`{"status":"received"}` with no id or stage) needs only omitempty. Neither
	// is what blocks this route. What blocks it is per-op projection SCOPE: a
	// route whose safety depends on HTTP middleware cannot be projected onto
	// transports that skip middleware. That is a distinct gap from multi-status
	// (#78) and it is not closable inside cloud.
	"POST /v1/crm/applications": "the IP rate limit and the pre-parse 64 KiB body cap are wire, and both " +
		"are bypassed by the MCP/CLI projections a typed op would publish (mcp.go:152, cli.go:427) — an " +
		"unmetered, uncapped alias of a metered public write that lands in the brand's own pipeline.",
}

// crmOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed
// registry entry. EVERY served operation counts, so a route mounted at an address
// nobody expected is caught rather than filtered out.
func crmOps(t *testing.T) (served map[string]bool, typed map[string]string, schemas map[string]any) {
	t.Helper()
	app := mountApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "crm", Version: "v1"})
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

// TestEveryRouteIsTypedOrNamed fails when a crm operation is neither a typed op
// nor named above — so the next route added here is typed by default, and dropping
// one out of the registry takes a deliberate edit carrying a reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed, _ := crmOps(t)

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
			"method. Convert it (zip.Get/Post/... on the /v1/crm group), or add it to untypedByDesign with "+
			"the reason typing it would move the wire.", strings.Join(untyped, ", "))
	}
	// The reasons must describe operations that exist, or the list is stale prose.
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which crm no longer serves", key)
		}
	}
	// A typed op named as a refusal is a contradiction — one of the two is wrong.
	for key := range untypedByDesign {
		if _, ok := typed[key]; ok {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
}

// TestEveryTypedOpIsDescribed proves the lifted prose reached the binary. That
// prose IS the product surface: it becomes the OpenAPI description AND the MCP
// tool description a model reads to pick the tool. zipdoc_gen.go is what carries
// it in, so an op added without regenerating shows up here as a nameless tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed, _ := crmOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed crm ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/crm/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the op-level
// gate above cannot see. Typing a route documents its ADDRESS and its SHAPE; it
// does not document the shape's FIELDS, and those come from a different place —
// doc comments on the In/Out struct fields, which zipdoc lifts per field. crm
// shipped fully-described request types (companyReq, patchApplicationIn, …) beside
// RESPONSE types with 65 bare properties: every field of Company, Contact,
// Opportunity, ProgramApplication, ScreenResult and StageEvent reached openapi.yaml, the
// generated SDKs and the MCP inputSchemas with no description at all, because those
// are store row types that nobody had written field prose on. A reader of the API
// could see that `arr` is an integer and nowhere that it is CENTS.
//
// So the response side is gated the same way the op side is: every property of
// every schema a crm op publishes must carry a description. Add a field to a row
// type without saying what it is and this fails.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	_, _, schemas := crmOps(t)
	if len(schemas) == 0 {
		t.Fatal("no crm schemas in the typed registry at all")
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
			"doc comment on the struct field and run: go generate -run zipdoc ./apps/crm/...",
			strings.Join(bare, ", "))
	}
}
