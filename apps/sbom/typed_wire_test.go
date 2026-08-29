package sbom

import (
	"context"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// This file makes the SBOM surface's typed partition a measurement instead of a
// paragraph. "All of them are typed" is prose, and prose cannot fail: a route added
// tomorrow as a raw func(*zip.Ctx) error would leave the claim standing and the
// route invisible to every projection — no schema, no description, no MCP tool, no
// CLI command, no SDK method.

// untypedByDesign is the CLOSED list of SBOM operations that are NOT typed ops,
// each with the wire fact that keeps it raw. The address is written the way the
// DOCUMENT writes it, which is the identity every projection keys on.
//
// It is EMPTY, and the emptiness is the point: every operation this subsystem
// serves carries a registry entry, so each one has a schema, prose, an MCP tool, a
// CLI command and an SDK method. The last entry was `GET /v1/sbom/{wildcard1}`,
// held out because a typed op could not publish a greedy segment as the path
// parameter it is; zip declares it now (`wildcardN` in the document, bound through
// the router's own `*N`), so the entry went and the route was typed.
// TestTheAddressIsTheAuthority is what the refusal turned into.
var untypedByDesign = map[string]string{}

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
	if len(served) != 3 || len(typed) != 3 {
		t.Errorf("served = %d (want 3), typed = %d (want 3)", len(served), len(typed))
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

// TestResolvePublishesItsAddress is what the wildcard refusal turned into. The
// route is typed now, and this is the reason it could be: the greedy segment is
// published as the PATH parameter it is, described, and nothing about it leaks
// into the query string.
//
// It reads sbom's own router, not a throwaway one — there is no longer a shape
// that cannot be registered here, so the live surface is the honest subject.
//
// Three claims, each separately mutation-checkable:
//
//   - `{wildcard1}` is declared, in "path" and required, so a generated client and
//     the fleet's own dispatch can both fill it;
//   - it carries PROSE, which reaches it from the doc comment on the field it
//     binds — a path parameter with no description is an argument an SDK offers
//     with nothing to say about it;
//   - NO query parameter is published, because the router key `*1` is not a name
//     a caller can write and this address has no query half at all.
func TestResolvePublishesItsAddress(t *testing.T) {
	doc, err := openapi.Spec(mountApp(t), openapi.Info{Title: "sbom", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	const path = "/v1/sbom/{wildcard1}"
	op := doc.Paths[path]["get"]
	if op == nil {
		t.Fatalf("resolve is not published at %s; document holds %v", path, sortedPaths(doc))
	}

	var inPath, inQuery []openapi.Parameter
	for _, p := range op.Parameters {
		switch p.In {
		case "path":
			inPath = append(inPath, p)
		case "query":
			inQuery = append(inQuery, p)
		}
	}
	if len(inPath) != 1 || inPath[0].Name != "wildcard1" || !inPath[0].Required {
		t.Fatalf("path parameters = %+v, want exactly one required `wildcard1`.\n"+
			"The address carries one value and the document has to name it, or every client "+
			"generated from this document sends the braces literally.", inPath)
	}
	if strings.TrimSpace(inPath[0].Description) == "" {
		t.Errorf("the `wildcard1` parameter carries no description. It is lifted from the doc " +
			"comment on the SbomRef field it binds — write that comment, then run: " +
			"make -C apps/sbom describe")
	}
	if len(inQuery) > 0 {
		t.Errorf("resolve publishes query parameter(s) %+v — this address has no query half, so "+
			"a parameter there is the router's own capture key escaping into the document", inQuery)
	}

	// The same document read the way the fleet dispatches it: the address must be
	// fillable, or the GraphQL field publishes a call nobody can aim.
	f, ok := openapi.Fields(doc)[op.OperationID]
	if !ok {
		t.Fatalf("%s is not in the dispatch table at all", op.OperationID)
	}
	if want := []string{"wildcard1"}; !slices.Equal(f.Route, want) {
		t.Errorf("the dispatch table fills %v in %q, want %v — an address it cannot fill is sent "+
			"with its braces intact", f.Route, f.Path, want)
	}
}

// sortedPaths names what a document holds, for a failure that has to say what it
// found instead of what it wanted.
func sortedPaths(doc *openapi.Document) []string {
	out := make([]string, 0, len(doc.Paths))
	for p := range doc.Paths {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// TestTheAddressIsTheAuthority pins which half of the request names the image.
//
// The In carries a WIRE name as well as the router key — `json:"ref" url:"*1"` —
// because a caller off the HTTP path has no URL: the op-call plane and an MCP
// tools/call hand their arguments across as the body, and a field with no wire
// name is a tool that can never say which image it means. That name is also a
// second way in, so the two could disagree, and the address has to win: bindURL
// binds the body first, then the query, then the path, precisely so the segment
// the router MATCHED is the one the handler acts on.
//
// Each decoy below carries a different image from the one in the path, and the
// answer must be about the path's. The harness has no datastore, so what comes
// back is the 503 — the ref reaches the store either way, and the two refs differ
// in whether they are well formed, so the DECOY is chosen to be one that would
// answer differently if it won: an empty capture is a 400 before any store work.
func TestTheAddressIsTheAuthority(t *testing.T) {
	app := mountApp(t)
	for _, d := range []struct{ name, path, body string }{
		{"a query key spelled like the wire name", "/v1/sbom/sha256:real?ref=", ""},
		{"a query key spelled like the router's", "/v1/sbom/sha256:real?*1=", ""},
		{"a body, which a GET does not even read", "/v1/sbom/sha256:real", `{"ref":""}`},
	} {
		code, body := do(t, app, "GET", d.path, d.body, member)
		if code != 503 {
			t.Errorf("%s: got %d (%s), want 503 — the empty decoy won and the request was "+
				"refused as ref-less, so something other than the path segment named the image",
				d.name, code, body)
		}
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

// TestResolveKeepsTheBytesTheRawHandlerSent is the wire half of the conversion.
// The op moved from `c.JSON(http.StatusOK, view)` in a raw handler to a typed op
// returning `*SbomView`, and the whole claim of that move is that a caller cannot
// tell: same status, same bytes.
//
// It is measured rather than reasoned, because the two paths serialize through
// different code — the raw one marshals the value the handler hands c.JSON, the
// typed one marshals the Out zip receives — and a difference there (a wrapper, a
// changed encoder, an omitted empty field) is exactly the kind that reads as
// working until a client parses it. The datastore is absent in the harness, so the
// value is built by the same buildView the handler uses and served both ways off
// one router.
func TestResolveKeepsTheBytesTheRawHandlerSent(t *testing.T) {
	view := buildView([]map[string]any{{
		"image_digest": "sha256:abc", "image_ref": "oci.hanzo.ai/hanzo/cloud:v1",
		"source_repo": "hanzoai/cloud", "git_sha": "deadbeef",
		"component_name": "golang.org/x/net", "component_version": "v0.1.0",
		"component_type": "library", "purl": "pkg:golang/golang.org/x/net@v0.1.0",
		"license": "BSD-3-Clause", "ingested_at": "2020-01-02 03:04:05",
	}})

	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	// The shape resolve had before the conversion, and the shape it has after.
	app.Group("/v1/was").Get("/it", func(c *zip.Ctx) error { return c.JSON(200, view) })
	zip.Get(app.Group("/v1/is"), "/it", func(context.Context, *noArgs) (*SbomView, error) { return &view, nil })

	wasCode, was := do(t, app, "GET", "/v1/was/it", "", member)
	isCode, is := do(t, app, "GET", "/v1/is/it", "", member)
	if wasCode != isCode || isCode != 200 {
		t.Fatalf("status moved: raw %d, typed %d, want 200 from both", wasCode, isCode)
	}
	if string(was) != string(is) {
		t.Fatalf("resolve's bytes moved:\n raw %s\ntyped %s", was, is)
	}
	// The two agreeing is not enough — they marshal ONE struct, so a renamed or
	// re-tagged field moves both together and the comparison above still passes.
	// This is the literal wire: every property name, the order they are emitted in,
	// and `truncated` ABSENT because it is false and omitempty.
	const wire = `{"imageDigest":"sha256:abc","imageRef":"oci.hanzo.ai/hanzo/cloud:v1",` +
		`"sourceRepo":"hanzoai/cloud","gitSha":"deadbeef","ingestedAt":"2020-01-02T03:04:05Z",` +
		`"componentCount":1,"components":[{"name":"golang.org/x/net","version":"v0.1.0",` +
		`"type":"library","purl":"pkg:golang/golang.org/x/net@v0.1.0","license":"BSD-3-Clause"}]}`
	if got := strings.TrimSpace(string(is)); got != wire {
		t.Fatalf("resolve's wire moved:\n got %s\nwant %s", got, wire)
	}

	// `truncated` is omitempty and false above, so the wire pin cannot see its
	// NAME — the one property whose spelling the fixture leaves unmeasured. Both
	// of its states, on the smallest view that shows them.
	full := serveView(t, app, "whole", SbomView{Components: []SbomComponent{}})
	if strings.Contains(full, "truncated") {
		t.Errorf("an untruncated view names `truncated`: %s — it is omitempty, so a client "+
			"reads its ABSENCE as false", full)
	}
	capped := serveView(t, app, "capped", SbomView{Truncated: true, Components: []SbomComponent{}})
	if !strings.Contains(capped, `"truncated":true`) {
		t.Errorf("a capped view does not carry `\"truncated\":true`: %s — that flag is the only "+
			"way a caller learns the component list is short of the image's real total", capped)
	}
}

// serveView serves one view through a typed op and returns the bytes, so a shape
// is measured ON THE WIRE rather than through json.Marshal — which is the encoder
// the pin above exists to catch drifting. at names the route, because two
// registrations at one address are a panic, not a second answer.
func serveView(t *testing.T, app *zip.App, at string, v SbomView) string {
	t.Helper()
	zip.Get(app.Group("/v1/"+at), "/it", func(context.Context, *noArgs) (*SbomView, error) { return &v, nil })
	_, body := do(t, app, "GET", "/v1/"+at+"/it", "", member)
	return strings.TrimSpace(string(body))
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
