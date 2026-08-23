package pref

// typed_wire_test.go is the ledger: every operation this app serves is either a
// typed op or a NAMED refusal carrying the wire fact that keeps it raw, and the two
// must SUM to the served surface.
//
// It exists because a reason written only at a registration cannot fail. Two
// refusals in this fleet outlived their cause by months — the agents session writes
// and the guide step transitions both named a capability that zip then shipped —
// and nothing noticed, because a comment is not a gate.

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// untypedByDesign is the CLOSED list, each entry re-read against the PINNED zip
// rather than inherited.
var untypedByDesign = map[string]string{
	"PATCH /v1/pref": "three facts at once, none of them expressible. A 16 KiB REQUEST-BYTE cap " +
		"answers 413 before any parse, and zip's global BodyLimit is far larger, so a typed op cannot " +
		"see it. An empty body and a literal `null` body each answer 400, where op.invoke skips the " +
		"decode for the first and decodes the second to a nil map. And the key space is OPEN — the " +
		"only carrier is map[string]any, whose typeName is \"\" so no request body is published at all.",
}

// TestEveryRouteIsTypedOrNamed fails three ways: a served route that is neither
// typed nor named, a name this app no longer serves, and a name that has BECOME a
// typed op — which is how a refusal that stops being true gets noticed.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	app := mountPrefs(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "pref", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed: %v", err)
	}

	served, typed := map[string]bool{}, map[string]bool{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/pref") {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key := range reg.Ops {
		if _, path, ok := strings.Cut(key, " "); ok && strings.HasPrefix(path, "/v1/pref") {
			typed[key] = true
		}
	}

	var untyped []string
	for key := range served {
		if !typed[key] {
			if _, named := untypedByDesign[key]; !named {
				untyped = append(untyped, key)
			}
		}
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("served but neither typed nor named: %s\n"+
			"A raw route publishes no schema, no MCP tool, no CLI command and no typed SDK method. "+
			"Convert it, or name it in untypedByDesign with the wire fact that keeps it raw.",
			strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which this app no longer serves", key)
		}
		if typed[key] {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("the two ledgers must sum to the served surface: typed %d + named %d = %d, served %d",
			len(typed), len(untypedByDesign), got, want)
	}
}
