package agentskills

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// This file makes the agent-skills discovery surface's typed partition a GATE
// instead of a paragraph. "0 of 2" is prose, and prose cannot fail — in EITHER
// direction. It fails if a route is added here without a reason, and it fails if a
// reason names a route this package no longer serves, so the two refusals below
// cannot quietly become inherited folklore.

// untypedByDesign is the CLOSED list of discovery operations that are NOT typed
// ops, each with the wire fact that keeps it raw. The address is written the way the
// DOCUMENT writes it, which is the identity every projection keys on.
//
// Both were re-read against the PINNED zip (v1.18.11) rather than inherited, and
// both are STRUCTURAL: there is no shape of In/Out that serves these wires.
var untypedByDesign = map[string]string{
	// The catalogue. Three independent facts, any one sufficient.
	//
	//  1. The response is the EMBEDDED FILE'S BYTES, and index.json carries a sha256
	//     per skill computed over the served SKILL.md. A typed Out re-marshals
	//     through encoding/json, which re-orders keys and re-indents — the document
	//     a client verifies would stop being the document that was generated.
	//  2. Cache-Control: public, max-age=300. zip's typed path writes the JSON body
	//     and the status and nothing else.
	//  3. A miss answers {"error": …} at 404, where a typed op's returned error
	//     renders zip's flat {"status","code","error"}.
	//
	// TestIndexIsServedVerbatimWithItsCacheHeader is that measurement.
	"GET /.well-known/agent-skills/index.json": "serves the embedded catalogue BYTES verbatim (its sha256 " +
		"digests are computed over what is served), under a Cache-Control a typed op cannot set, with a " +
		"{\"error\":…} 404 body zip's errorHandler does not produce.",

	// One skill document. Structural: the response is text/markdown — a DOCUMENT,
	// not a JSON value — and a typed op marshals its Out with c.JSON and always
	// answers application/json. TestSkillIsMarkdown is that measurement.
	"GET /.well-known/agent-skills/{skill}/SKILL.md": "answers text/markdown; a typed op marshals its Out " +
		"as JSON, so no In/Out shape serves this wire at all.",
}

// skillOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed registry
// entry. EVERY served operation counts.
func skillOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := newApp(t, "hanzo")
	doc, err := openapi.Spec(app, openapi.Info{Title: "agentskills", Version: "v1"})
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

// TestEveryRouteIsTypedOrNamed fails when a discovery operation is neither a typed
// op nor named above, and when a reason names an operation that is gone.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := skillOps(t)

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
			t.Errorf("untypedByDesign names %q, which agentskills no longer serves", key)
		}
		if _, ok := typed[key]; ok {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("typed(%d) + named(%d) = %d, served = %d — the ledgers must partition the surface",
			len(typed), len(untypedByDesign), got, want)
	}
	// The MEASURED partition, so the prose cannot drift from the binary.
	if len(served) != 2 || len(typed) != 0 {
		t.Errorf("served = %d (want 2), typed = %d (want 0)", len(served), len(typed))
	}
}

// TestIndexIsServedVerbatimWithItsCacheHeader is the measurement behind the first
// refusal: the response is the embedded file byte-for-byte, and it carries the
// discovery convention's Cache-Control. Both are things a typed op cannot do.
func TestIndexIsServedVerbatimWithItsCacheHeader(t *testing.T) {
	app := newApp(t, "hanzo")
	code, body, hdr := get(t, app, "/.well-known/agent-skills/index.json", "api.hanzo.ai")
	if code != 200 {
		t.Fatalf("index: %d", code)
	}
	want, err := catalogFS.ReadFile("catalog/hanzo/index.json")
	if err != nil {
		t.Fatalf("read embedded catalogue: %v", err)
	}
	if string(body) != string(want) {
		t.Fatal("index.json is not served byte-for-byte — its sha256 digests are computed over what is " +
			"served, so a re-marshal (which is what a typed Out does) breaks verification")
	}
	if got := hdr.Get("Cache-Control"); got != "public, max-age=300" {
		t.Fatalf("Cache-Control = %q, want the discovery convention's value — zip's typed path cannot "+
			"set a response header, which is the other half of this refusal", got)
	}
}

// TestSkillIsMarkdown is the measurement behind the second refusal: the response is
// a text/markdown DOCUMENT, and a typed op always answers application/json.
func TestSkillIsMarkdown(t *testing.T) {
	app := newApp(t, "hanzo")
	// Any skill the embedded hanzo catalogue carries.
	entries, err := catalogFS.ReadDir("catalog/hanzo")
	if err != nil {
		t.Fatalf("read catalogue: %v", err)
	}
	skill := ""
	for _, e := range entries {
		if e.IsDir() {
			skill = e.Name()
			break
		}
	}
	if skill == "" {
		t.Skip("the embedded hanzo catalogue carries no skill directories")
	}
	code, body, hdr := get(t, app, "/.well-known/agent-skills/"+skill+"/SKILL.md", "api.hanzo.ai")
	if code != 200 {
		t.Fatalf("skill %s: %d", skill, code)
	}
	if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("Content-Type = %q, want text/markdown — a typed op answers application/json, so no "+
			"In/Out shape serves this wire", ct)
	}
	if len(body) == 0 {
		t.Fatal("SKILL.md served empty")
	}
}
