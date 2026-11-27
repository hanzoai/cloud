package patent

// This file makes this app's typed partition a GATE rather than a paragraph.
// "Everything here is typed" is prose, and prose cannot fail — not when a route is
// added raw, and not when a reason names an address this package no longer serves.
// Both directions are checked below.
//
// The claim matters more here than on most surfaces: this is the one product whose
// entire value is that a THIRD PARTY can call it, describe it and verify it without
// holding this repository. A raw route publishes an address and nothing else, and a
// caller that cannot read what an evidence record means cannot rely on the evidence.

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/spec"
)

// untypedByDesign is the CLOSED list of served operations that are not typed ops,
// each with the wire fact that keeps it raw. It is empty, and it is written out
// rather than omitted so that an entry added later has to be a deliberate edit
// naming a capability zip lacks.
var untypedByDesign = map[string]string{}

// surface reads both projections of the live router at their shared address form:
// what the document says is served, and which of those operations carry a typed
// registry entry. Every served operation counts, which is what makes a route added
// anywhere in routes() show up here without anyone remembering this file.
func surface(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	// The REAL mount, not a reconstruction of it: an assertion about a route table
	// this test assembled by hand is an assertion about the test.
	app := mountPatent(t)
	doc, err := spec.Of(app, spec.Info{Title: "patent", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := spec.Typed(app)
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

// TestEveryRouteIsTypedOrNamed is the gate in both directions: a served operation
// that is neither typed nor named here fails, and a ledger entry naming an address
// this app no longer serves fails too.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		served, typed := surface(t)
		for want, why := range untypedByDesign {
			if !served[want] {
				t.Errorf("untypedByDesign names %s, which is not served — its reason (%s) is now folklore", want, why)
			}
			if _, ok := typed[want]; ok {
				t.Errorf("%s is BOTH a typed op and listed as untypable; delete the ledger entry", want)
			}
		}
		var raw []string
		for got := range served {
			if _, ok := typed[got]; ok {
				continue
			}
			if _, ok := untypedByDesign[got]; ok {
				continue
			}
			raw = append(raw, got)
		}
		sort.Strings(raw)
		if len(raw) != 0 {
			t.Errorf("%d served operation(s) are untyped and unnamed: %v — type them, or write down the wire fact that blocks each", len(raw), raw)
		}
		if len(typed) != len(served) {
			t.Errorf("typed %d of %d served operations", len(typed), len(served))
		}

	})
}

// TestEveryTypedOpIsDescribed asks the question the registry answers rather than
// the one the router can: an operation with no description reaches the document,
// every generated SDK and the MCP tool list as a name and a shape, which is exactly
// the half a caller reading an evidence record needs and cannot reconstruct.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, typed := surface(t)
		var mute []string
		for key, desc := range typed {
			if strings.TrimSpace(desc) == "" {
				mute = append(mute, key)
			}
		}
		sort.Strings(mute)
		if len(mute) != 0 {
			t.Errorf("operations publishing no description: %v — the sentence is lifted from the handler's doc comment by zipdoc, so this is either a missing comment or a generate pass that never ran", mute)
		}

	})
}

// TestTheLedgerSumsToTheSurface pins the arithmetic the two ledgers above are
// meant to express, so a count quoted in prose cannot drift from the binary:
// typed + named == served, and the served set is the whole product.
func TestTheLedgerSumsToTheSurface(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		served, typed := surface(t)
		if got := len(typed) + len(untypedByDesign); got != len(served) {
			t.Errorf("typed %d + named %d = %d against %d served", len(typed), len(untypedByDesign), got, len(served))
		}
		if len(served) < 12 {
			t.Errorf("the live router serves %d operations; the surface shrank", len(served))
		}

	})
}
