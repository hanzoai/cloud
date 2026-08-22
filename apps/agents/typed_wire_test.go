package agents

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// untypedByDesign is the CLOSED list of agent operations that are NOT typed ops,
// each with the wire fact that keeps it raw.
//
// It USED TO HOLD ELEVEN. Five left in one change, and the reason they left is
// the reason this list is a test rather than a comment: their entry said
// verbatim "they go typed when zip can express a response with a body per
// status", zip v1.31.3 did exactly that, and nothing would have noticed. A
// refusal that names its own expiry condition still needs something that checks
// whether the condition arrived.
//
// SIX remain, in three families:
//
//   - THE CHAT ROUND (4). Registered by github.com/hanzoai/agent, not by this
//     package: apps/agents calls hz.MountAt and adds no route of its own there.
//     Two facts have to move upstream with them — every handler resolves its
//     caller through a func(*zip.Ctx) (Principal, bool), and the round dispatches
//     tools with the LIVE *zip.Ctx — so it is a per-REQUEST bridge that repo owns,
//     and the round additionally relays an upstream 4xx's status AND body
//     verbatim, which stays untypable even after the bridge lands.
//   - THE SESSION STREAM (1). An open Server-Sent Events response written by a
//     loop that OUTLIVES the handler (c.SendStreamWriter). A typed op returns one
//     marshalled value; there is no In/Out that describes a feed.
//   - THE RUN (1). Two different bodies on two different failures: a 502 answers
//     with the RECORDED RUN as its body, and a balance denial answers the
//     fleet-wide cloud.DenyResource envelope, whose NESTED {"error":{code,message}}
//     cannot ride HTTPError.Detail — the envelope is written last and would
//     overwrite the domain `error` key. That is the same collision sessions_typed.go
//     records, seen from the side where it bites.
var untypedByDesign = map[string]string{
	"POST /v1/agents/chat":                   "registered by github.com/hanzoai/agent, and it relays an upstream 4xx's status and body verbatim.",
	"GET /v1/agents/chat/conversations":      "registered by github.com/hanzoai/agent; typing it is that repo's change, not this one.",
	"GET /v1/agents/chat/conversations/{id}": "registered by github.com/hanzoai/agent; typing it is that repo's change, not this one.",
	"GET /v1/agents/chat/presets":            "registered by github.com/hanzoai/agent; typing it is that repo's change, not this one.",
	"GET /v1/agents/sessions/stream": "an open SSE response written by a loop that outlives the handler " +
		"(c.SendStreamWriter); a typed op returns one marshalled value and there is no In/Out for a feed.",
	"POST /v1/agents/{ref}/run": "two bodies on two failures: a 502 answers with the RECORDED RUN, and a balance " +
		"denial answers cloud.DenyResource's NESTED {\"error\":{code,message}} — which cannot ride " +
		"HTTPError.Detail, because the envelope is written last and would overwrite the domain `error` key.",
}

// TestEveryRouteIsTypedOrNamed requires the two ledgers to SUM to the served
// surface, so a route added raw goes red without anyone remembering this file,
// and an entry whose expiry condition arrived goes red the moment it is typed.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})
	doc, err := openapi.Spec(app, openapi.Info{Title: "agents", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed: %v", err)
	}

	served, typed := map[string]bool{}, map[string]bool{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/agents") {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key := range reg.Ops {
		if _, path, ok := strings.Cut(key, " "); ok && strings.HasPrefix(path, "/v1/agents") {
			typed[key] = true
		}
	}

	var untyped []string
	for key := range served {
		if typed[key] {
			continue
		}
		if _, named := untypedByDesign[key]; !named {
			untyped = append(untyped, key)
		}
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("served but neither typed nor named: %s\n"+
			"A raw route publishes no schema, no MCP tool, no CLI command and no typed SDK "+
			"method. Convert it, or name it in untypedByDesign with the WIRE FACT that keeps "+
			"it raw — re-read against the pinned zip, never inherited from an older pass.",
			strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which this surface no longer serves", key)
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
