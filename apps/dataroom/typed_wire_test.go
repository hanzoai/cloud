package dataroom

// typed_wire_test.go is the ledger the prose used to be.
//
// This plane's refusals were written at their registrations and nowhere else, so
// they could not fail — and they had already drifted: the record said SEVEN raw
// addresses while the binary served EIGHT. The one that arrived unclassified is
// GET /v1/dataroom/trust/center/{slug}/file/{item}, which came with the trust
// plane after that count was taken. A comment cannot notice a ninth.

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// untypedByDesign is a WIRE this stack cannot describe. Nothing here is work
// owed; each entry names the fact, and none of them expires when zip gains a
// capability — a byte stream stays a byte stream.
//
// Re-read against the pinned zip, v1.31.3: a typed op's only response path is
// c.JSON(out) (typed.go:567), and op.invoke unmarshals every non-empty body as
// JSON before the handler is entered (typed.go:242). Both are still true; what
// v1.31.x DID add — variadic WithStatus + StatusCoder, response headers, and
// HTTPError.Detail — touches statuses and error bodies, not raw bytes.
var untypedByDesign = map[string]string{
	"POST /v1/dataroom/documents": "the request body IS the file: raw bytes under the caller's own " +
		"Content-Type, named by ?name=. A typed In would have zip JSON-decode a PDF and answer 400.",
	"GET /v1/dataroom/documents/{id}/file": "answers a byte STREAM off object storage under the " +
		"document's own content type; no In/Out pair describes one.",
	"GET /v1/dataroom/trust/center/{slug}/file/{item}": "a public trust-centre artifact's bytes — the " +
		"same byte-stream fact as the admin download beside it. This is the entry the prose count missed.",
	"GET /v1/dataroom/view/{linkId}/document/{documentId}/file": "the viewer's byte stream, as above.",
	"POST /v1/dataroom/view/{linkId}/authenticate": "decodeBody enforces a PACKAGE-LOCAL maxBody and " +
		"answers 413 on the raw length before anything is parsed; zip's global BodyLimit is far larger, " +
		"so a typed op cannot see that cap and an over-sized body would be decoded instead of refused.",
	"POST /v1/dataroom/view/{linkId}/pageview": "the same 413-before-parse cap as authenticate.",
}

// typingOwed is work that is OWED, not refused: the mechanism exists, and what
// stops each entry is named. An entry is DELETED when its op is written — it is
// not a reason, it is a debt.
//
// Keeping the two lists apart is what stops a reader concluding this package is
// at its floor. It is not: two of the eight raw addresses are simply unwritten.
var typingOwed = map[string]string{
	"GET /v1/dataroom/health": "a bare 200 with a constant body — no second status, no error branch, " +
		"nothing wire-bound. It stays raw only because it must answer BEFORE the bundle loads, which is " +
		"a fact about WHERE it is registered and not about its wire: declared absolutely on the app " +
		"(zip.Get(zapp, \"/v1/dataroom/health\", …), the form apps/world, pricing, plan, prefs and share " +
		"already use) it registers at the same point and types cleanly.",
	"GET /v1/dataroom/view/{linkId}": "reads NO body (viewer(s, …, readBody=false)), so the 413 cap that " +
		"blocks its two POST siblings does not apply to it. Its tenancy is not a blocker either: the " +
		"handler resolves the org from the link index rather than from a principal, and a typed op can " +
		"call the same index. What it needs is an Out for the viewer payload, which the bundle owns — " +
		"apps/goja's Raw/Scalar kit, already used by this package, is the shape for that.",
}

// TestEveryRouteIsTypedOrNamed sums THREE terms against the served surface, so
// "typed + cannot + owed" has to account for every operation. A route added raw
// goes red, a reason naming an address this plane no longer serves goes red, and
// an entry that has become a typed op goes red — including one in typingOwed,
// which is how a debt gets closed rather than forgotten.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	app, _ := mountMCPApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "dataroom", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed: %v", err)
	}

	served, typed := map[string]bool{}, map[string]bool{}
	for path, item := range doc.Paths {
		if !strings.Contains(path, "dataroom") {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key := range reg.Ops {
		if _, path, ok := strings.Cut(key, " "); ok && strings.Contains(path, "dataroom") {
			typed[key] = true
		}
	}

	named := func(key string) bool {
		_, a := untypedByDesign[key]
		_, b := typingOwed[key]
		return a || b
	}

	var unaccounted []string
	for key := range served {
		if !typed[key] && !named(key) {
			unaccounted = append(unaccounted, key)
		}
	}
	if len(unaccounted) > 0 {
		sort.Strings(unaccounted)
		t.Errorf("served but neither typed nor accounted for: %s\n"+
			"A raw route publishes no schema, no MCP tool, no CLI command and no typed SDK method. "+
			"Convert it; or, if a wire fact forbids it, name it in untypedByDesign with that fact; "+
			"or, if it is merely unwritten, name it in typingOwed with what it needs.",
			strings.Join(unaccounted, ", "))
	}
	for _, ledger := range []struct {
		name string
		m    map[string]string
	}{{"untypedByDesign", untypedByDesign}, {"typingOwed", typingOwed}} {
		for key := range ledger.m {
			if !served[key] {
				t.Errorf("%s names %q, which this plane no longer serves", ledger.name, key)
			}
			if typed[key] {
				t.Errorf("%s names %q, which IS a typed op — delete the entry", ledger.name, key)
			}
		}
	}
	if got, want := len(typed)+len(untypedByDesign)+len(typingOwed), len(served); got != want {
		t.Errorf("the three ledgers must sum to the served surface: typed %d + cannot %d + owed %d = %d, served %d",
			len(typed), len(untypedByDesign), len(typingOwed), got, want)
	}
}
