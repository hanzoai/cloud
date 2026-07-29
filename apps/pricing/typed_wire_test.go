package pricing

import (
	"context"
	"sort"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
)

// This file is the GATE on the typed/raw partition of the pricing surface: the
// 17 raw routes are a CLOSED list, each named with the wire fact that keeps it
// raw, and any route that is neither a typed op nor on that list fails the
// suite — so the next pricing route is typed by default, and dropping one out
// of the registry takes a deliberate edit with a reason. Same shape as
// apps/team/typed_wire_test.go, which is the worked example of pinning a split
// tranche with a test instead of prose.

// verbatimProxy is the one wire fact behind fifteen of the refusals: the route
// is a byte-for-byte proxy of the @hanzo/pricing goja bundle. The bundle picks
// the status (200, or 503 when the section is absent) and its bytes are written
// unmodified (passthrough); a typed op answers the ONE status it declared, over
// a Go re-marshal that re-orders keys and re-formats numbers. Two wire changes,
// so the route stays raw until zip can express the proxy class. The GATED
// catalog routes do not have this property — they already decoded and
// re-marshalled through Go — which is why they are typed and these are not.
const verbatimProxy = "a verbatim status+bytes proxy of the @hanzo/pricing bundle; " +
	"a typed op answers one declared status over a Go re-marshal."

// untypedByDesign is the CLOSED list of pricing operations that are NOT typed
// ops, each with the reason it cannot be one. A typed op is a route PLUS a
// registry entry — the one value the OpenAPI operation, the MCP tool, the CLI
// command and the SDK method all come from — so an operation missing from that
// registry is invisible to all four. These 17 are missing on purpose. Addresses
// are written the way the DOCUMENT writes them, which is the identity every
// projection keys on. Each reason was re-verified against zip v1.18.6's own
// source (typed.go: invoke unmarshals before the handler, the dispatch ends in
// c.JSON(out) under one declared status; openapi.go: schemaOf reflects []byte
// as an integer array), because "cannot be typed" is a claim about a dependency
// and a dependency moves — re-check when zip ships raw passthrough (#78's
// family) and convert.
var untypedByDesign = map[string]string{
	"GET /v1/pricing/base":            verbatimProxy,
	"GET /v1/pricing/blockchain":      verbatimProxy,
	"GET /v1/pricing/cloud":           verbatimProxy,
	"GET /v1/pricing/cloud/plans":     verbatimProxy,
	"GET /v1/pricing/cloud/regions":   verbatimProxy,
	"GET /v1/pricing/cloud/storage":   verbatimProxy,
	"GET /v1/pricing/compute":         verbatimProxy,
	"GET /v1/pricing/compute/presets": verbatimProxy,
	"GET /v1/pricing/gpu":             verbatimProxy,
	"GET /v1/pricing/iam":             verbatimProxy,
	"GET /v1/pricing/paas":            verbatimProxy,
	"GET /v1/pricing/policy":          verbatimProxy,
	"GET /v1/pricing/subscriptions":   verbatimProxy,
	"GET /v1/pricing/tools":           verbatimProxy,
	"GET /v1/pricing-policy":          verbatimProxy,

	"PATCH /v1/admin/catalog/models/{wildcard1}": "the model id may contain '/' " +
		"(anthropic/claude-opus-4.6), so it routes through a greedy wildcard fiber names `*1`; " +
		"binding it needs an In field tagged json:\"*1\", which every projection would then " +
		"publish — a schema nobody can read is worse than none. Its body also carries " +
		"`overrides` (see providers/{name}).",
	"PATCH /v1/admin/catalog/providers/{name}": "the body carries `overrides`, a raw JSON " +
		"merge patch (RFC 7386): zip reflects json.RawMessage — it is []byte — as an ARRAY OF " +
		"INTEGERS, a false schema; and retyping the field map[string]any moves " +
		"{\"overrides\":null} from \"clear the override\" to \"leave it alone\", a wire change.",
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
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), Brand: "hanzo", DataDir: t.TempDir()}); err != nil {
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
// typed op nor one of the 17 above — so the next route added here is typed by
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
