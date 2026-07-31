package dns

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// This file makes the DNS head's untyped-ness a MEASURED fact rather than a
// sentence in Mount's comment. The refusal is real (see untypedByDesign below),
// but a refusal nobody can re-check is how a convertible route stays untyped
// forever — and how a reason that has stopped being true keeps being believed.
//
// The consequence being pinned is worth stating plainly: /v1/dns publishes seven
// operations at one greedy wildcard, and NOT ONE of them carries a description, a
// summary, an MCP tool or a CLI command. This whole subsystem projects to nothing
// a caller can read. That is a deliberate cost, and this file is where it stops
// being deliberate the moment someone adds a route that did not have to pay it.

// untypedByDesign is the closed list of operations that are NOT typed ops, each
// with the WIRE FACT that typing it would move. Keyed by the address form the
// document uses, so a stale entry names something and can be checked.
var untypedByDesign = map[string]string{
	"DELETE /v1/dns/{wildcard1}":  reasonForward,
	"GET /v1/dns/{wildcard1}":     reasonForward,
	"OPTIONS /v1/dns/{wildcard1}": reasonForward,
	"PATCH /v1/dns/{wildcard1}":   reasonForward,
	"POST /v1/dns/{wildcard1}":    reasonForward,
	"PUT /v1/dns/{wildcard1}":     reasonForward,
	"TRACE /v1/dns/{wildcard1}":   reasonForward,
}

// reasonForward is the one reason all seven share, because all seven ARE one
// registration: `app.Group("/v1/dns").All("/*", e.forward)` (dns.go). Three wire
// facts each independently forbid a typed op, all three verified against zip
// v1.18.12 rather than inherited as prose:
//
//   - ONE registration, EVERY method. zip's typed registrars are per-method and
//     there is no All[In, Out]; seven ops would each have to declare a body this
//     relay never parses.
//   - a GREEDY wildcard. fiber names the segment `*1` and the document
//     `{wildcard1}`, and its value is a whole sub-path — not a scalar
//     zip's bindURL (typed.go setScalar) can set on an In field.
//   - a VERBATIM response. forward answers c.Bytes(res.StatusCode, out) with the
//     upstream's own Content-Type and Location. A typed op's only response path is
//     `c.JSON(out)` under the status it DECLARED (zip typed.go:302-311), so the
//     status, the content type and the redirect header all move.
//
// Typing this means giving the DNS control plane a typed surface IN THAT PLANE,
// not wrapping it here.
const reasonForward = "forward. One All() registration for every method, over a greedy wildcard, " +
	"relaying the DNS plane's own status code and Content-Type verbatim. zip has no All[In, Out], " +
	"no In field can bind a whole sub-path, and a typed op can only answer c.JSON(out) under its " +
	"declared status (zip v1.18.12 typed.go:302-311) — so all three of method, path and response move."

// TestEveryRouteIsTypedOrNamed fails when a dns operation is neither a typed op
// nor named above, so the next route added here is typed BY DEFAULT. It also
// fails on a stale reason naming a route dns no longer serves — the half that
// keeps a refusal honest as the code moves under it.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	app := dnsApp(t, "http://127.0.0.1:1")
	doc, err := openapi.Spec(app, openapi.Info{Title: "dns", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	served := map[string]bool{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/dns") {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	if len(served) == 0 {
		t.Fatal("the dns head serves nothing at all — the router moved and this gate is now blind")
	}
	typed := map[string]bool{}
	for key := range reg.Ops {
		if i := strings.Index(key, " "); i > 0 && strings.HasPrefix(key[i+1:], "/v1/dns") {
			typed[key] = true
		}
	}
	var untyped []string
	for key := range served {
		if typed[key] || untypedByDesign[key] != "" {
			continue
		}
		untyped = append(untyped, key)
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("untyped and unnamed: %s\nAn untyped route projects to NOTHING — no prose, no MCP tool, "+
			"no CLI command, no typed SDK method. Convert it (zip.Get/Post/... on the group), or add it to "+
			"untypedByDesign with the WIRE FACT that typing it would move.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %s, which dns does not serve — a stale reason nobody can re-check", key)
		}
		if typed[key] {
			t.Errorf("untypedByDesign names %s, which IS a typed op — remove the reason", key)
		}
	}
	if len(typed)+len(untypedByDesign) != len(served) {
		t.Errorf("%d typed + %d named != %d served", len(typed), len(untypedByDesign), len(served))
	}
}
