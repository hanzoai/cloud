package pricing

import (
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
// dependency moves, so the reason is not asserted from prose: the tests below
// register the shape at issue on a throwaway app and check that zip and
// openapi.translate still behave as it says. A blocker fixed upstream turns this
// suite RED and names the route that just became convertible, instead of leaving
// a stale reason standing.
//
// It was 17, then 2, and is 1. The fifteen fixed sections came off it once the
// premise was re-checked: they were held to be verbatim byte proxies of the
// @hanzo/pricing bundle, but apps/goja re-marshals the bundle's answer through
// Go's encoding/json (Host.DispatchWith) before any handler sees it, so a typed
// op re-marshalling the same decoded value is byte-identical — which
// sections_wire_test.go proves route by route against the live router. The
// sixteenth was PATCH providers/{name}, whose refusal named its own expiry
// condition and got it: zip publishes a json.RawMessage as the unconstrained
// value it is, so the merge patch stopped being a blocker and the route is an op.
var untypedByDesign = map[string]string{
	"PATCH /v1/admin/pricing/catalog/models/{wildcard1}": "GREEDY FIBER WILDCARD — the registry " +
		"and the router spell the path differently, so Fold refuses the whole document. The model " +
		"id may contain '/' (anthropic/claude-opus-4.6), so it is addressed by a greedy wildcard: " +
		"registered at apps/pricing/pricing.go:209 as `.../models/*` and read back at " +
		"apps/pricing/admin.go:196 with c.Param(\"*\"). zip keys a typed op by that fiber pattern " +
		"verbatim — Template rewrites `:name` segments and passes `*` through " +
		"(zip@v1.36.3/address.go:61) — while this document keys the same route by its URI template " +
		"`.../models/{wildcard1}` (openapi/openapi.go:811, because fiber's own capture name `*1` " +
		"is not a legal template name). openapi.Fold looks the op up under zip's spelling, finds " +
		"no live route, and errors (openapi/openapi.go:705) — and Spec builds ONE document, so " +
		"every other pricing operation goes down with it. " +
		"TWO CLAUSES THAT ARE NO LONGER REASONS, recorded so nobody re-derives them as blockers: " +
		"(1) the In does NOT need a field tagged json:\"*1\" — urlFieldName reads the `url:` tag " +
		"ahead of `json:` (zip@v1.36.3/openapi.go:884), so `json:\"-\" url:\"*1\"` binds the " +
		"capture and publishes nothing; (2) `overrides` is not a blocker either — zip publishes a " +
		"json.RawMessage as the unconstrained value it is, which is why PATCH providers/{name} is " +
		"an op. The wildcard alone holds this one. Its bodies ARE declared regardless " +
		"(apps/pricing/admin.go:165, openapi.Register): the wildcard blocks TYPING, not DESCRIBING.",
}

// pricingSpec mounts the REAL subsystem and projects its live router, which is
// what every gate below reads: a reconstruction of the routes would be evidence
// about the reconstruction.
func pricingSpec(t *testing.T) (*zip.App, *openapi.Document) {
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
	return app, doc
}

// pricingOps reads BOTH projections of the live router at their one shared
// address form: what the document says is served, and which of those carry a
// typed registry entry. The filter is the package's own Prefixes — the same two
// cloud.Declare scopes by, and the same two manifest/apps.go routes to this
// plugin — so a route mounted outside what the subsystem declares shows up as
// uncovered instead of slipping past the gate.
func pricingOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app, doc := pricingSpec(t)
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
		if _, isTyped := typed[key]; isTyped {
			t.Errorf("untypedByDesign names %q, which IS a typed op — the reason outlived its "+
				"cause. Delete the entry.", key)
		}
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which pricing no longer serves", key)
		}
	}
}

// TestTheSubsystemCarriesItsOwnBridge mounts pricing with NO composer — no
// app-wide cloud.Bridge, which is what `compose` stands in for everywhere else in
// this package — and requires an org-scoped op to still resolve the caller's org.
//
// It drives the ADMIN read, and the choice of route is the whole measurement.
// The TENANT is not what Bridge is load-bearing for here — principal.OrgFrom
// falls back to zip.CallerOf, which reads the request's own headers, so an
// org-scoped op resolves its org with no Bridge at all and a gate written on one
// would pass vacuously (measured: it did). What only Bridge parks is the REQUEST
// (cloud.Request reads the value it puts on the context), and callerIsAdmin is
// the one thing that needs it, because admin-ness lives in a header
// principal.OrgFrom does not carry. With no Bridge every admin op here answers
// 403 to a genuine SuperAdmin — in tests and in any composition without
// cloud.Serve, and never in production, which is the worst shape a gap can take.
func TestTheSubsystemCarriesItsOwnBridge(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")}) // deliberately NOT composed.
	if err := Mount(app, cloud.Deps{Brand: "hanzo", DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/pricing/catalog", nil)
	req.Header.Set("X-Org-Id", "admin")
	req.Header.Set("X-User-Id", "u_admin")
	req.Header.Set("X-User-IsAdmin", "true")
	resp, err := app.Fiber().Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("GET /v1/admin/pricing/catalog: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusForbidden {
		t.Fatalf("a SuperAdmin was refused with no composer present: %s\n"+
			"callerIsAdmin reads the request off the context and only cloud.Bridge parks it, so "+
			"the subsystem's own Bridge is missing or is installed after its leaves — which makes "+
			"every admin op here 403 wherever pricing is mounted without cloud.Serve.", body)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", resp.StatusCode, body)
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
// carries `overrides` exactly as admin.go's overlayPatch declares it.
type (
	probeIn  struct{}
	probeOut struct {
		OK bool `json:"ok"`
	}
	probePatch struct {
		Overrides *json.RawMessage `json:"overrides,omitempty"`
	}
)

// TestTypingTheWildcardRefusesTheWholeDocument proves the WHOLE of the
// models/{wildcard1} reason, and proves it is stronger than an unreadable
// parameter name: zip keys a typed op by the fiber pattern while this package's
// document keys the route by its URI template, so Fold cannot find the op's route
// and refuses — and Spec builds one document, so the refusal takes every other
// pricing operation with it.
//
// It used to prove the "first half" of a two-clause reason whose second clause
// was already false (a field tagged `url:"*1"` binds the capture and publishes
// nothing). The clause is gone from the ledger, so this is the only thing
// standing between that route and a conversion — which is why its failure says
// convert, without a second condition to check first.
func TestTypingTheWildcardRefusesTheWholeDocument(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	zip.Patch(app, "/v1/admin/pricing/catalog/models/*", func(context.Context, *probeIn) (*probeOut, error) {
		return &probeOut{OK: true}, nil
	})
	_, err := openapi.Spec(app, openapi.Info{Title: "probe", Version: "v1"})
	if err == nil {
		t.Fatal("openapi.Spec now accepts a typed op on a greedy wildcard — the " +
			"models/{wildcard1} reason has expired. Convert the route: bind the capture " +
			"with `json:\"-\" url:\"*1\"`, delete the untypedByDesign entry and the " +
			"openapi.Register/Describe pair the raw route needed.")
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
// the one GET /v1/admin/pricing/catalog returns under "_overlay", must carry the admin's
// own key order. That is why the field stays json.RawMessage and not map[string]any.
func TestMergePatchPublishesAnyJSON(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	zip.Patch(app, "/v1/admin/pricing/catalog/providers/:name", func(context.Context, *probePatch) (*probeOut, error) {
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

// TestTheUntypedRouteDeclaresItsBodies is the half of the raw route's cost that
// the op-level gate above cannot see. Prose alone left it publishing an
// operationId, a summary and a description and NO requestBody and NO responses —
// which is exactly what a route that takes nothing and returns nothing publishes,
// so every SDK generated off this document offered a model-overlay patch with
// nowhere to put the patch, and no reader could tell the two cases apart.
//
// The wildcard blocks TYPING, not DECLARING: openapi.Register states the shapes
// the handler actually binds. This asserts the $refs rather than the shapes,
// because the shapes are the Go types' business and naming them twice here is the
// drift Register exists to prevent.
//
// It reads the MARSHALLED document for the reason openapi.Bare does: an
// Operation's RequestBody and Responses are open-typed (`any`), because Register
// contributes cloud's own structs there and the typed fold contributes zip's
// maps, so walking the Go value would assert against whichever half this route
// happens to arrive through. The bytes are the artifact.
func TestTheUntypedRouteDeclaresItsBodies(t *testing.T) {
	_, doc := pricingSpec(t)
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal document: %v", err)
	}
	var published struct {
		Paths map[string]map[string]struct {
			RequestBody *bodyDecl            `json:"requestBody"`
			Responses   map[string]*bodyDecl `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &published); err != nil {
		t.Fatalf("decode document: %v", err)
	}

	const path = "/v1/admin/pricing/catalog/models/{wildcard1}"
	op, served := published.Paths[path]["patch"]
	if !served {
		t.Fatalf("%s is not served — the ledger and this gate are both stale", path)
	}
	if op.RequestBody == nil {
		t.Fatal("PATCH models/{wildcard1} declares NO request body — indistinguishable from a " +
			"route that takes none, so an SDK offers the patch with nowhere to put it. " +
			"openapi.Register in admin.go's init is what states it.")
	}
	if got := op.RequestBody.ref(); got != "#/components/schemas/overlayPatch" {
		t.Errorf("request body is %q, want the overlayPatch component — the raw route and the "+
			"typed providers op must declare ONE patch shape", got)
	}
	resp, answered := op.Responses["2XX"]
	if !answered {
		t.Fatalf("PATCH models/{wildcard1} declares no success response; got %v", statusKeys(op.Responses))
	}
	if got := resp.ref(); got != "#/components/schemas/Overlay" {
		t.Errorf("success response is %q, want the Overlay component — the route answers the "+
			"new effective overlay", got)
	}
}

// bodyDecl is a request or response body as the DOCUMENT carries it, read only
// far enough to name the component it points at.
type bodyDecl struct {
	Content map[string]struct {
		Schema struct {
			Ref string `json:"$ref"`
		} `json:"schema"`
	} `json:"content"`
}

// ref is the component the application/json half points at, and "" for every way
// it might not point at one — so a missing key reads as a named mismatch rather
// than as a panic inside the assertion.
func (b *bodyDecl) ref() string {
	if b == nil {
		return ""
	}
	return b.Content["application/json"].Schema.Ref
}

func statusKeys(m map[string]*bodyDecl) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// proseless is the CLOSED list of published properties that carry NO description
// because the CLIENT they arrived through cannot carry one — not because nobody
// wrote it. Every one of them HAS prose in the source; two generator gaps stand
// between that prose and the document, both recorded in CLAUDE.md, and neither is
// worked around here: the available workarounds (hand-writing a schema, or
// unrolling a shared shape into copies) each replace one true statement with two
// that can drift, and this shape is shared by two overlay writes precisely so they
// cannot disagree about what an overlay write accepts.
//
// It is exact in BOTH directions. A bare property anywhere else goes red, and an
// entry here that starts publishing prose goes red too — that is the day the
// generator learns, and this ledger shrinks then rather than outliving the gap.
var proseless = map[string]bool{
	// EMBEDDED STRUCT. providerPatchIn embeds overlayPatch, and encoding/json
	// PROMOTES those five fields to the outer object — so zip's schema builder
	// inlines them and looks each description up as `providerPatchIn.<field>`
	// (zip@v1.36.3/openapi.go:724), while zipdoc files a field's prose under the
	// type that DECLARES it, `overlayPatch.<field>` (zip@v1.36.3
	// internal/zipdoc/extract.go:678 — the embedded field itself is unexported, so
	// it is skipped there and the inner type is walked on its own). Both halves are
	// right and the two keys never meet. zipdoc_gen.go carries `overlayPatch.state`
	// today while the document publishes `providerPatchIn.state` bare, which is the
	// gap in one line. The fix is one change in zip, not five copies here.
	"providerPatchIn.beta":      true,
	"providerPatchIn.betaOrgs":  true,
	"providerPatchIn.enabled":   true,
	"providerPatchIn.overrides": true,
	"providerPatchIn.state":     true,

	// REFLECTION CLIENT. The same five fields again, published a second time under
	// their own name because the models PATCH declares its body with
	// openapi.Register (admin.go's init) — the only client a route the wildcard
	// keeps raw has. Register derives a schema by REFLECTION and Go drops comments
	// at compile time, so zipdoc — which walks zip's TYPED registrations — can
	// never reach a type that arrives this way. Publishing the shape bare is still
	// strictly better than publishing no body at all, which is what this route did.
	"overlayPatch.beta":      true,
	"overlayPatch.betaOrgs":  true,
	"overlayPatch.enabled":   true,
	"overlayPatch.overrides": true,
	"overlayPatch.state":     true,
}

// TestEveryPublishedFieldIsDescribed gates the FIELD half of this surface, which
// the op-level gates above are blind to: an app can be 100% typed and publish a
// wholly unreadable document, because an op's prose and a FIELD's prose are lifted
// from different comments.
//
// It matters here because two of these fields are the difference between safe and
// unsafe use rather than between clear and unclear. `betaOrgs` is consulted only
// while an entry is in beta, so a disabled entry carrying a non-empty list IS a
// beta while the same list on an `off` one grants nothing — read either way round
// without prose and an operator either leaks a model or believes they revoked one.
// `updatedAt` is SECONDS, in a fleet whose other timestamps are milliseconds.
//
// The gate checks presence, not meaning. A description that restates the field's
// name is worse than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	_, doc := pricingSpec(t)
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("pricing publishes no schemas at all — the gate would pass vacuously")
	}
	published, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}

	var bare []string
	live := map[string]bool{}
	for _, p := range published {
		live[p] = true
		if !proseless[p] {
			bare = append(bare, p)
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("published properties with no description: %s\n"+
			"A property reaches openapi.yaml, every generated SDK and every MCP inputSchema; its "+
			"description is the only place the units, the sign, the closed vocabulary or what "+
			"ABSENCE means can travel with it. Write a doc comment on the field.",
			strings.Join(bare, ", "))
	}

	// The other direction: an entry that started publishing prose is the day a
	// generator learned, and the ledger must shrink rather than outlive the gap.
	var stale []string
	for p := range proseless {
		if !live[p] {
			stale = append(stale, p)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("proseless names %s, which pricing now describes or no longer publishes. Delete "+
			"those entries — a ledger that outlives its cause is prose pretending to be a gate.",
			strings.Join(stale, ", "))
	}
}
