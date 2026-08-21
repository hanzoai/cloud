package sbom

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
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
	// The greedy resolve wildcard — the apps/pricing refusal, one subsystem over,
	// and re-measured against the PINNED zip (v1.18.12) rather than inherited.
	//
	// The BOUND name and the PUBLISHED name cannot agree. fiber names this capture
	// `*1` (zip's bindURL matches c.Route().Params, so an input field would need
	// `url:"*1"`), while the untyped projection publishes the address as
	// `/v1/sbom/{wildcard1}` with a PATH parameter of that name. A typed op
	// publishes op.Path VERBATIM, so the address would become `/v1/sbom/*` and `*1`
	// would be declared as a QUERY parameter — three published facts moved (path,
	// parameter name, parameter location) for a route whose wire did not.
	// TestResolveTakesASlashBearingRef is the wire this refusal protects.
	"GET /v1/sbom/{wildcard1}": "a greedy wildcard: fiber binds the capture as `*1` while the document " +
		"publishes it as the path parameter `{wildcard1}`, and a typed op publishes its path verbatim — so " +
		"the bound field and the published parameter cannot agree.",
}

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
		code, body := do(t, app, "GET", "/v1/sbom/"+ref, "", false)
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
	code, body := do(t, app, "GET", "/v1/sbom/health", "", false)
	if code != 200 {
		t.Fatalf("health: got %d (%s), want 200", code, body)
	}
	want := `{"datastore":false,"service":"sbom","status":"ok","table":"` + sbomTable + `"}`
	if got := strings.TrimSpace(string(body)); got != want {
		t.Fatalf("health bytes moved:\n got %s\nwant %s", got, want)
	}
	// The SuperAdmin gate still refuses a non-admin, and still before any store work.
	if code, _ := do(t, app, "POST", "/v1/sbom", `{"imageDigest":"sha256:a","document":{}}`, false); code != 403 {
		t.Fatalf("non-admin ingest: got %d, want 403", code)
	}
}
