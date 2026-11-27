package studio

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/sdk/spec"
)

// This file makes the studio surface's typed partition a GATE: a route added as a
// raw handler would be invisible to every projection — no schema, no description,
// no MCP tool, no CLI command, no SDK method — so it is a typed op or it is named
// here with the wire fact that keeps it raw.

// untypedByDesign is the CLOSED list of studio operations that are not typed ops.
var untypedByDesign = map[string]string{
	"GET /v1/studio/runs/{id}/events": "a Server-Sent Events stream that outlives the handler (SendStreamWriter); " +
		"a typed op answers one JSON value.",
	"POST /v1/studio/chat": "one chat turn answered as a Server-Sent Events stream: text deltas, then the runs it started.",
	"POST /v1/studio/render/{wildcard1}": "the render lane's door: the durable engine's own activity verbs, relayed for the " +
		"render workload alone; its claims answer 204 or an activity, its other verbs the engine's own JSON.",
}

func studioOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	r := mount(t)
	doc, err := spec.Of(r.app, spec.Info{Title: "studio", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := spec.Typed(r.app)
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

func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := studioOps(t)
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
		t.Errorf("operation(s) with no registry entry and no reason: %s", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which studio does not serve", key)
		}
		if _, ok := typed[key]; ok {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
}

// The lifted prose reached the binary: it is the OpenAPI description and the MCP
// tool description a model reads to pick the tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := studioOps(t)
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: make -C apps/studio generate", key)
		}
	}
}
