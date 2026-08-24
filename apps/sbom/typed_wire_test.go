package sbom

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// This file makes the SBOM surface's typed partition a GATE instead of a paragraph.
// "2 of 3" is prose, and prose cannot fail: a route added tomorrow as a raw
// func(*zip.Ctx) error would leave the claim standing and the route invisible to
// every projection — no schema, no description, no MCP tool, no CLI command, no SDK
// method.

// untypedByDesign is the CLOSED list of SBOM operations that are NOT typed ops,
// each with the wire fact that keeps it raw. The address is written the way the
// DOCUMENT writes it, which is the identity every projection keys on.
var untypedByDesign = map[string]string{
	"GET /v1/sbom/{wildcard1}": reasonWildcard,
}

// reasonWildcard is the wire fact behind the one refusal, and it is STRONGER
// than the sentence this ledger used to carry. "Three published facts move for a
// route whose wire did not" reads as a document that would be wrong; the
// document would not be PRODUCED.
//
// Three readings of one address, each correct on its own:
//
//   - zip's Template (address.go:61-73) rewrites only `:name` segments, so a `*`
//     segment survives VERBATIM, and zip keys an op by closeColonParams(op.Path)
//     (openapi.go:99, 518-520). A typed op here would publish `/v1/sbom/*`.
//   - cloud's router reading must give that segment a legal URI-template name and
//     calls it `{wildcard1}` (openapi/openapi.go:811-829 translate; fiber's own
//     key is `*1`, which is not one).
//   - fiber binds the capture as `*1`, so an In field would have to carry
//     `url:"*1"` — the parameter a caller sees named twice, differently.
//
// openapi.Fold (openapi/openapi.go:699) then looks the typed op up by zip's
// spelling, finds no live route at that key, and returns "typed op %q has no
// live route — the registry and the router disagree about its path" (:705).
// `describe` reaches Fold through openapi.FleetSpec → Spec, so the cost is not a
// mis-named parameter: `make -C apps/sbom describe` FAILS and this app publishes
// nothing at all. Same class as apps/kms's `/v1/kms/secrets/+`, which records it
// the same way.
//
// The address is NECESSARY rather than awkward. The value is an image REFERENCE
// carrying slashes and a tag or a digest, so a `:ref` segment (which matches one
// segment) or a mandatory percent-encoding would be a wire change:
// TestResolveTakesASlashBearingRef is that measurement.
//
// No version is cited, because a version in prose expires and this one already
// had — the entry went on naming zip v1.18.12 for four minors after go.mod
// stopped pinning it, which is how a refusal rots while reading as measured.
// TestTheWildcardCannotBeATypedOp RUNS the refusal instead, so the day zip's
// Template names a wildcard the way cloud's reading does, it goes green and says
// so rather than going stale in silence.
const reasonWildcard = "a greedy `*` capture: zip's Template (address.go:61-73) leaves the segment verbatim so a " +
	"typed op publishes `/v1/sbom/*`, while cloud's router reading names it `{wildcard1}` " +
	"(openapi/openapi.go:811-829). Fold looks the op up by zip's spelling, finds no live route, and " +
	"REFUSES the whole document (openapi/openapi.go:699,705) — not a mis-named parameter, no document."

// sbomOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed registry
// entry. EVERY served operation counts, so a route mounted at an address nobody
// expected is caught rather than filtered out.
func sbomOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mountApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "sbom", Version: "v1"})
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

// TestEveryRouteIsTypedOrNamed fails when an SBOM operation is neither a typed op
// nor named above — so the next route added here is typed by default, and dropping
// one out of the registry takes a deliberate edit carrying a reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := sbomOps(t)

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
			"method. Convert it, or add it to untypedByDesign with the reason typing it would move the wire.",
			strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which sbom no longer serves", key)
		}
		if _, ok := typed[key]; ok {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
	// The two ledgers must SUM to the served surface: neither may quietly shrink.
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("typed(%d) + named(%d) = %d, served = %d — the ledgers must partition the surface",
			len(typed), len(untypedByDesign), got, want)
	}
	// The MEASURED partition, so the prose cannot drift from the binary.
	if len(served) != 3 || len(typed) != 2 {
		t.Errorf("served = %d (want 3), typed = %d (want 2)", len(served), len(typed))
	}
}

// TestEveryTypedOpIsDescribed proves the lifted prose reached the binary. That
// prose IS the product surface: it becomes the OpenAPI description AND the MCP tool
// description a model reads to pick the tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := sbomOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed sbom ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/sbom/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed gates the half the op-level gate cannot
// see. Typing a route documents its ADDRESS and its SHAPE, never the shape's
// FIELDS: those come from a doc comment on each one, which zipdoc lifts per
// field, so a fully typed surface can still publish a document that says
// nothing. A property reaches openapi.yaml, every generated SDK and every MCP
// inputSchema, and its description is the only place the units, the closed
// vocabulary or the meaning of ABSENCE can travel with it.
//
// There is no exemption ledger here and there is nothing to exempt: all three
// published schemas come from typed ops, so zipdoc reaches every field of them.
// The four generator gaps that force one elsewhere — a schema declared by
// openapi.Register (reflection, and Go drops comments), an embedded struct, an
// anonymous struct, a defined type over another struct — apply to none of them.
// The day one does, the failure below says which property and why.
//
// openapi.Bare is the ONE walker: it reads the MARSHALLED document (Schemas is
// open-typed, so walking the Go value silently skips whichever half it did not
// expect) and descends into nested shapes, array items, additionalProperties and
// every alternative of allOf/anyOf/oneOf. A top-level-only check reports clean
// while an inline object inside a property ships bare.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(mountApp(t), openapi.Info{Title: "sbom", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("sbom publishes no schemas at all — the gate would pass vacuously")
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("%d published schema propert(ies) carry no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto the "+
			"first of them alone — then run: make -C apps/sbom describe. If the property arrived "+
			"through openapi.Register, an embedded struct, an anonymous struct or a defined type, no "+
			"comment can reach it: name it in a proseless ledger checked in BOTH directions "+
			"(apps/projects/prose_test.go is the reference), never by hand-writing a schema.",
			len(bare), strings.Join(bare, ", "))
	}
}

// TestTheWildcardCannotBeATypedOp is the refusal in untypedByDesign, RUN rather
// than asserted. It registers a typed op at the same greedy `*` address resolve
// serves and requires openapi.Spec to refuse to produce a document at all.
//
// A refusal nothing runs is a refusal that outlives its cause — this ledger's own
// entry cited a zip version four minors stale. This one cannot: the day zip's
// Template names a wildcard the way cloud's router reading does, the test goes
// green and names what to do about it.
func TestTheWildcardCannotBeATypedOp(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	g := app.Group("/v1/probe")
	zip.Get(g, "/*", func(context.Context, *noArgs) (*SbomHealth, error) { return &SbomHealth{}, nil })

	_, err := openapi.Spec(app, openapi.Info{Title: "probe", Version: "v1"})
	if err == nil {
		t.Fatal("openapi.Spec accepted a typed op on a `*` wildcard — zip and cloud now agree about " +
			"how to name that segment, so reasonWildcard has stopped being true. Type " +
			"GET /v1/sbom/* and delete the untypedByDesign entry.")
	}
	if !strings.Contains(err.Error(), "no live route") {
		t.Fatalf("refused for a different reason than the one recorded: %v", err)
	}
}

// TestResolveTakesASlashBearingRef is the MEASUREMENT behind the one refusal above.
// The value this route addresses is an image REFERENCE, which carries slashes and a
// colon (`oci.hanzo.ai/hanzo/cloud:v1`) — that is why it is a greedy wildcard
// and not a `:ref` segment, and it is what a typed op could not name today. The
// harness has no datastore, so the honest answer is 503 rather than a match; what
// this pins is that the ROUTE still matches a multi-segment ref at all.
func TestResolveTakesASlashBearingRef(t *testing.T) {
	app := mountApp(t)
	for _, ref := range []string{
		"sha256:abc",
		"oci.hanzo.ai/hanzo/cloud:v1",
		"oci.hanzo.ai/hanzo/cloud@sha256:abc",
	} {
		code, body := do(t, app, "GET", "/v1/sbom/"+ref, "", member)
		if code == 404 {
			t.Fatalf("GET /v1/sbom/%s did not match the resolve route (404) — a slash-bearing ref must "+
				"reach it; that greedy capture is why this route cannot be a typed op", ref)
		}
		if code != 503 {
			t.Fatalf("GET /v1/sbom/%s: got %d (%s), want 503 (no datastore in the harness)", ref, code, body)
		}
	}
}

// TestHealthAndIngestKeepTheirBytes pins the two shapes typing MOVED from a
// map[string]any to a struct. encoding/json emits a map's keys SORTED, so the
// structs spell their fields in that order and the bytes did not move — which is
// the only thing that proves a map→struct swap was a description change and not a
// wire change.
func TestHealthAndIngestKeepTheirBytes(t *testing.T) {
	app := mountApp(t)
	code, body := do(t, app, "GET", "/v1/sbom/health", "", anon)
	if code != 200 {
		t.Fatalf("health: got %d (%s), want 200", code, body)
	}
	want := `{"datastore":false,"service":"sbom","status":"ok","table":"` + sbomTable + `"}`
	if got := strings.TrimSpace(string(body)); got != want {
		t.Fatalf("health bytes moved:\n got %s\nwant %s", got, want)
	}
	// The SuperAdmin gate still refuses a non-admin, and still before any store work.
	if code, _ := do(t, app, "POST", "/v1/sbom", `{"imageDigest":"sha256:a","document":{}}`, member); code != 403 {
		t.Fatalf("non-admin ingest: got %d, want 403", code)
	}
}
