package company

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// This file makes company's typed partition a GATE instead of a paragraph. "20 of
// 22, and the 2 are permanent" was prose, and prose cannot fail: a route added
// tomorrow as a raw func(*zip.Ctx) error would leave the claim standing and the
// route invisible to every projection. The two refusals below already have WIRE
// tests (TestDeckTakesRawBytes, TestPaymentDenialWire) pinning what each route
// sends; what was missing is the EXHAUSTIVENESS check — that those two are the
// only ones, and that they still exist. Here the claim is a test, so the route
// that falsifies it says so.

// untypedByDesign is the CLOSED list of company operations that are NOT typed
// ops, each with the wire fact that keeps it raw. A typed op is a route PLUS a
// registry entry — the one value the OpenAPI operation, the MCP tool, the CLI
// command and the SDK method all come from — so an operation missing from that
// registry is invisible to all four. These two are missing on purpose. The
// address is written the way the DOCUMENT writes it, which is the identity every
// projection keys on.
//
// Both refusals are blocked by the same shape of gap: zip's typed path has
// exactly one body codec and exactly one error shape, and these two routes need
// neither. Both are re-checked against zip v1.18.6 source, not inherited as
// prose.
//
// Being exempt from the REGISTRY is not being exempt from the DOCUMENT. Both of
// these declare their bodies through openapi.Register (company.go init), so the
// cost of staying untyped is exactly the three things that come from zip's
// registry — prose, an MCP tool, a CLI command — and not also a wrong description.
// TestTheUntypedRoutesStillDeclareTheirBodies is the other half of each exemption.
var untypedByDesign = map[string]string{
	// The deck is document BYTES of any content type, named by ?name=. zip's
	// typed path decodes EVERY non-empty request body with jsonenc.Unmarshal
	// before the handler runs (zip@v1.18.6 typed.go:232 — `if len(rawIn) > 0 {
	// dec(rawIn, &in) }`), and that decode is unconditional: it does not depend
	// on the In having any bound field, so even an empty In would not escape it.
	// A PDF is not JSON, so a typed op here turns the 201 this route has always
	// sent into 400 "invalid body". zip v1.18.6 has no octet-stream/binary
	// request declaration to opt out with. Pinned by TestDeckTakesRawBytes.
	"POST /v1/company/fundraise/deck": "the deck is raw document bytes of any content type; zip's typed " +
		"path unconditionally jsonenc.Unmarshals every non-empty body (typed.go:232), so a typed In would " +
		"turn this route's 201 into 400 on the PDF it exists to accept, and v1.18.6 has no binary request " +
		"declaration to decline the decode.",

	// A formation-fee denial answers the FLEET-WIDE billing contract: 402
	// insufficient_balance, 402 spend_cap_exceeded, 503 balance_unavailable,
	// each a body that NESTS {code,message} under "error" (cloud.DenyResource,
	// resource_billing.go). zip's error type is a FLAT {status,code,error}
	// (zip@v1.18.6 ctx.go:201) and errorHandler is the only path a typed op's
	// error can take (ctx.go:223) — it re-marshals whatever it is handed into
	// that flat shape. Writing the nested body from inside the op does not help
	// either: returning a nil Out makes zip stamp `cmp.Or(op.Status, 204)` over
	// the 402 (typed.go:298), and returning an error makes errorHandler replace
	// the body. So there is no way to type this route without reshaping the
	// money-path error for every metered client. Pinned by TestPaymentDenialWire.
	"POST /v1/company/payment": "a billing denial answers the fleet-wide nested {\"error\":{code,message}} " +
		"contract at 402/503 (cloud.DenyResource); zip's error type is a flat {status,code,error} and " +
		"errorHandler is the only path a typed op's error takes, so typing this silently reshapes the " +
		"money wire for every metered client. Needs zip errors that can carry a body.",
}

// companyOps reads BOTH projections of the live router at their one shared
// address form: what the document says is served, and which of those carry a
// typed registry entry. EVERY served operation counts, so a route mounted at an
// address nobody expected is caught rather than filtered out.
func companyOps(t *testing.T) (served map[string]bool, typed map[string]string, schemas map[string]any) {
	t.Helper()
	app, _, _ := mountFake(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "company", Version: "v1"})
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

// TestEveryRouteIsTypedOrNamed fails when a company operation is neither a typed
// op nor named above — so the next route added here is typed by default, and
// dropping one out of the registry takes a deliberate edit carrying a reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed, _ := companyOps(t)

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
			"method. Convert it (zip.Get/Post/... on the /v1/company group), or add it to untypedByDesign "+
			"with the reason typing it would move the wire.", strings.Join(untyped, ", "))
	}
	// The reasons must describe operations that exist, or the list is stale prose.
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which company no longer serves", key)
		}
	}
	// A typed op named as a refusal is a contradiction — one of the two is wrong.
	for key := range untypedByDesign {
		if _, ok := typed[key]; ok {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
}

// TestTheUntypedRoutesStillDeclareTheirBodies is the OTHER half of both
// exemptions: these two routes cannot be typed ops, but the cost of that must be
// exactly the three things zip's registry supplies (prose, an MCP tool, a CLI
// command) and not a FOURTH — a document that describes them wrongly.
//
// Before this, both published an operationId and a tag and nothing else. That is
// indistinguishable, to every consumer of the document, from a route that takes no
// body and returns none, so an SDK generated off openapi.yaml offered a deck
// upload with nowhere to put the deck and no return type for either call. The deck
// declares its request as bytes (application/octet-stream, string/binary —
// OpenAPI's own spelling for an opaque body, and what an SDK generator turns into
// a file parameter); both declare the response the handler actually marshals.
//
// The success shape lands under the "2XX" range key, not "200"/"201": the exact
// status lives in the handler body and is not derivable from a registration, so the
// range is what this generator can honestly assert.
func TestTheUntypedRoutesStillDeclareTheirBodies(t *testing.T) {
	app, _, _ := mountFake(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "company", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}

	// The deck: a byte request, and the data room receipt back.
	deck := doc.Paths["/v1/company/fundraise/deck"]["post"]
	if deck == nil {
		t.Fatal("POST /v1/company/fundraise/deck is not served — the ledger is stale")
	}
	if deck.RequestBody == nil {
		t.Error("the deck declares no request body — openapi.Register is what tells an SDK " +
			"this route takes a file, and without it the document says the route takes nothing")
	} else {
		req := decodeBody(t, deck.RequestBody)
		if media, ok := req.Content["application/octet-stream"]; !ok {
			t.Errorf("the deck declares no application/octet-stream request; have %v",
				sortedKeys(req.Content))
		} else if media.Schema.Type != "string" || media.Schema.Format != "binary" {
			t.Errorf("deck request schema = %s/%s, want string/binary",
				media.Schema.Type, media.Schema.Format)
		}
	}
	assertSuccessBody(t, "POST /v1/company/fundraise/deck", deck.Responses)

	// Payment takes NO body (pay never reads one), so the absence of a request
	// declaration here is the true statement, not a missing one. Its success shape
	// is the formation view every other action on this surface answers with.
	pay := doc.Paths["/v1/company/payment"]["post"]
	if pay == nil {
		t.Fatal("POST /v1/company/payment is not served — the ledger is stale")
	}
	if pay.RequestBody != nil {
		t.Errorf("POST /v1/company/payment declares a request body, but the handler never reads "+
			"one — declaring a body it ignores would be invention; have %v",
			sortedKeys(decodeBody(t, pay.RequestBody).Content))
	}
	assertSuccessBody(t, "POST /v1/company/payment", pay.Responses)
}

// body is the shape of a requestBody/response object this test reads. The Document
// carries both as `any` so the router projection and the typed fold can share one
// field, so they are round-tripped through JSON to be inspected.
type body struct {
	Content map[string]struct {
		Schema struct {
			Type   string `json:"type"`
			Format string `json:"format"`
			Ref    string `json:"$ref"`
		} `json:"schema"`
	} `json:"content"`
}

func decodeBody(t *testing.T, from any) body {
	t.Helper()
	var out body
	remarshalJSON(t, from, &out)
	return out
}

// assertSuccessBody proves a route states SOME success body. Keyed on "2XX"
// because that is the only key registration.apply can honestly emit: the exact
// status lives in the handler and is not derivable from a registration.
func assertSuccessBody(t *testing.T, key string, responses any) {
	t.Helper()
	var byStatus map[string]body
	remarshalJSON(t, responses, &byStatus)
	r, ok := byStatus["2XX"]
	if !ok {
		t.Errorf("%s states no 2XX success body — a consumer cannot tell it from a route that "+
			"returns nothing; have %v", key, sortedKeys(byStatus))
		return
	}
	if _, ok := r.Content["application/json"]; !ok {
		t.Errorf("%s 2XX declares no application/json body; have %v", key, sortedKeys(r.Content))
	}
}

func remarshalJSON(t *testing.T, from, into any) {
	t.Helper()
	b, err := json.Marshal(from)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(b, into); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestEveryTypedOpIsDescribed proves the lifted prose reached the binary. That
// prose IS the product surface: it becomes the OpenAPI description AND the MCP
// tool description a model reads to pick the tool. zipdoc_gen.go is what carries
// it in, so an op added without regenerating shows up here as a nameless tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed, _ := companyOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed company ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/company/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the op-level
// gate above cannot see. Typing a route documents its ADDRESS and its SHAPE; it
// does not document the shape's FIELDS, and those come from a different place —
// doc comments on the In/Out struct fields, which zipdoc lifts per field. A
// reader of the API could otherwise see that `equityBps` is an integer and
// nowhere that it is BASIS POINTS of 10000.
//
// So the response side is gated the same way the op side is: every property of
// every schema a company op publishes must carry a description. Add a field to a
// row type without saying what it is and this fails.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	_, _, schemas := companyOps(t)
	if len(schemas) == 0 {
		t.Fatal("no company schemas in the typed registry at all")
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
			"doc comment on the struct field and run: go generate -run zipdoc ./apps/company/...",
			strings.Join(bare, ", "))
	}
}
