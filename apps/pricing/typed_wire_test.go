package pricing

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
)

// This file is the GATE on the typed/raw partition of the pricing surface: the
// 1 raw route is a CLOSED list, each named with the wire fact that keeps it
// raw, and any route that is neither a typed op nor on that list fails the
// suite — so the next pricing route is typed by default, and dropping one out
// of the registry takes a deliberate edit with a reason. Same shape as
// apps/team/typed_wire_test.go, which is the worked example of pinning a split
// tranche with a test instead of prose.

// untypedByDesign is the CLOSED list of pricing operations that are NOT typed
// ops, each with the reason it cannot be one. A typed op is a route PLUS a
// registry entry — the one value the OpenAPI operation, the MCP tool, the CLI
// command and the SDK method all come from — so an operation missing from that
// registry is invisible to all four. This 1 is missing on purpose. Addresses
// are written the way the DOCUMENT writes them, which is the identity every
// projection keys on. "Cannot be typed" is a claim about a DEPENDENCY, and a
// dependency moves, so neither reason is asserted from prose: the test at
// the bottom of this file registers the shape at issue on a throwaway app and
// check that zip and openapi.translate still behave as the reason says. A blocker
// fixed upstream turns this suite RED and names the route that just became
// convertible, instead of leaving a stale reason standing.
//
// It was 17, then 2. The fifteen fixed sections came off it once the premise was
// re-checked: they were held to be verbatim byte proxies of the @hanzo/pricing
// bundle, but apps/goja re-marshals the bundle's answer through Go's
// encoding/json (Host.DispatchWith) before any handler sees it, so a typed op
// re-marshalling the same decoded value is byte-identical — which
// sections_wire_test.go proves route by route against the live router.
var untypedByDesign = map[string]string{
	"PATCH /v1/admin/catalog/models/{wildcard1}": "the model id may contain '/' " +
		"(anthropic/claude-opus-4.6), so it routes through a greedy wildcard, and TWO facts follow. " +
		"First, typing it does not merely publish a bad parameter — it REFUSES THE WHOLE DOCUMENT: " +
		"zip keys a typed op by the fiber pattern (\".../models/*\") while this document keys the " +
		"route by its URI template (\".../models/{wildcard1}\", openapi.translate), so Fold cannot " +
		"find the op's route and errors, and Spec builds ONE document, so every other pricing " +
		"operation goes down with it. Second, past that, fiber binds the segment under the name " +
		"`*1`, so the In needs a field tagged json:\"*1\" — which every projection would publish, " +
		"and which the document's own {wildcard1} could not agree with. A schema nobody can read " +
		"is worse than none; a document that will not build is worse than both. Its body also " +
		"carries `overrides`, which is no longer a blocker on its own: zip v1.18.9 publishes " +
		"json.RawMessage as the unconstrained value it is, so PATCH providers/{name} became an " +
		"op. The wildcard is what still holds THIS one.",
}

// pricingOps reads BOTH projections of the live router at their one shared
// address form: what the document says is served, and which of those carry a
// typed registry entry. The filter is the package's own Prefixes — the same
// five cloud.Declare scopes by — so a route mounted outside what the subsystem
// declares shows up as uncovered instead of slipping past the gate, which is
// exactly the latent defect this surface had (it served five prefixes and
// declared one).
func pricingOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Mount(app, cloud.Deps{Brand: "hanzo", DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })

	doc, err := openapi.Spec(app, openapi.Info{Title: "pricing", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool {
		for _, pfx := range Prefixes {
			if p == pfx || strings.HasPrefix(p, pfx+"/") {
				return true
			}
		}
		return false
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

// TestEveryRouteIsTypedOrNamed fails when a pricing operation is neither a
// typed op nor the 1 above — so the next route added here is typed by
// default, and dropping one out of the registry takes a deliberate edit with a
// reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := pricingOps(t)

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
			"method. Convert it (zip.Get/Post/... on the app), or add it to untypedByDesign with the reason "+
			"typing it would move the wire.", strings.Join(untyped, ", "))
	}
	// The reasons must describe operations that exist, or the list is stale prose.
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which pricing no longer serves", key)
		}
	}
}

// TestEveryTypedOpIsDescribed fails on a typed op with no lifted prose, because
// that prose IS the product surface: it becomes the OpenAPI description AND the
// MCP tool description a model reads to pick the tool. zipdoc_gen.go is what
// carries it into the binary, so an op added without regenerating shows up here
// as a nameless tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := pricingOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed pricing ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/pricing/...", key)
		}
	}
}

// ---- the reason, made executable ----
//
// Each reason above is a claim about a DEPENDENCY, and a dependency moves. These
// two tests re-check the claims on every run instead of on somebody remembering
// to, so a blocker that gets fixed upstream turns this suite RED and names the
// route that just became convertible.
//
// Both probe a THROWAWAY app carrying the SHAPE at issue, never pricing's own
// router: the claim is about what the toolchain does with a shape, and asserting
// it on the live surface would mean registering the very op the reason says
// cannot be registered.

// probeIn/probeOut are the minimal typed op. The wildcard refusal is about the
// PATH, so that probe's payload is deliberately empty; the merge-patch probe
// carries `overrides` exactly as admin.go's patchBody declares it.
type (
	probeIn  struct{}
	probeOut struct {
		OK bool `json:"ok"`
	}
	probePatch struct {
		Overrides *json.RawMessage `json:"overrides,omitempty"`
	}
)

// TestTypingTheWildcardRefusesTheWholeDocument proves the FIRST half of the
// models/{wildcard1} reason, and proves it is stronger than an unreadable
// parameter name: zip keys a typed op by the fiber pattern while this package's
// document keys the route by its URI template, so Fold cannot find the op's route
// and refuses — and Spec builds one document, so the refusal takes every other
// pricing operation with it.
func TestTypingTheWildcardRefusesTheWholeDocument(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	zip.Patch(app, "/v1/admin/catalog/models/*", func(context.Context, *probeIn) (*probeOut, error) {
		return &probeOut{OK: true}, nil
	})
	_, err := openapi.Spec(app, openapi.Info{Title: "probe", Version: "v1"})
	if err == nil {
		t.Fatal("openapi.Spec now accepts a typed op on a greedy wildcard — the " +
			"models/{wildcard1} reason is stale on its first half. Re-check the second " +
			"(fiber binds the segment as `*1`, which no document can publish) and convert " +
			"the route if that is gone too.")
	}
	if !strings.Contains(err.Error(), "has no live route") {
		t.Fatalf("the wildcard refusal moved: %v — the reason names the mechanism, re-read it", err)
	}
}

// TestMergePatchPublishesAnyJSON is the other half of the tripwire that retired
// the providers/{name} refusal. It once pinned the LIE — json.RawMessage published
// as an array of integers, because schemaOf took the Slice arm before asking
// whether the type marshals itself — and went red when zip v1.18.9 stopped telling
// it. Now it pins the truth, so a regression that reintroduces the integer array
// fails here rather than shipping a schema no client can satisfy.
//
// The verbatim echo it protects is the point: the overlay this route returns, and
// the one GET /v1/admin/catalog returns under "_overlay", must carry the admin's
// own key order. That is why the field stays json.RawMessage and not map[string]any.
func TestMergePatchPublishesAnyJSON(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	zip.Patch(app, "/v1/admin/catalog/providers/:name", func(context.Context, *probePatch) (*probeOut, error) {
		return &probeOut{OK: true}, nil
	})
	doc, err := openapi.Spec(app, openapi.Info{Title: "probe", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	got, err := json.Marshal(doc.Components.Schemas["probePatch"])
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	const anyJSON = `{"properties":{"overrides":{}},"type":"object"}`
	if string(got) != anyJSON {
		t.Fatalf("the json.RawMessage schema moved:\n got %s\nwant %s\nAn unconstrained schema is "+
			"what a raw JSON value IS. If this went back to an array of integers, schemaOf is "+
			"reading the Slice arm before the marshaler again and PATCH providers/{name} is "+
			"publishing a shape no client can send.", got, anyJSON)
	}
}
