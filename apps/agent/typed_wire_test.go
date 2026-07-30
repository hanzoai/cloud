// typed_wire_test.go — the projection gate. NONE of this package's four operations
// is a typed op, and this file makes that a MEASURED fact rather than the prose at
// the top of agent.go alone: prose cannot go red. A route added here untyped goes
// red without anyone remembering to name it, a reason naming a route this package
// no longer serves goes red too, and the two ledgers must sum to the surface the
// live router actually serves. The day hanzoai/agent ships these as typed ops, the
// partition sum breaks and the ledger below is what a maintainer deletes.
package agent

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// untypedByDesign is the CLOSED list of agent operations that are NOT typed ops.
// All four share ONE binding fact: they are registered by github.com/hanzoai/agent
// v0.1.3 (agent.go:166-169) — untyped app.Post/app.Get in ANOTHER MODULE — so
// there is no registration in this repo to convert, and re-registering them here
// would re-derive upstream's wiring (the apps/tasks class). Each entry adds the
// wire fact that binds that op even after upstream grows a typed seam. Addresses
// are written the way the DOCUMENT writes them, which is the identity every
// projection keys on.
var untypedByDesign = map[string]string{
	"POST /v1/agent": "owned by hanzoai/agent (agent.go:166). It also relays an upstream " +
		"completion 4xx VERBATIM — c.Bytes(up.Status, up.Body), round.go:110-113 — so a 402 " +
		"insufficient_balance reaches the caller as itself, and it dispatches server-executed " +
		"tools with the LIVE *zip.Ctx (ToolPlane.Dispatch), which a context-only op does not have. " +
		"The apps/ml refusal class: it stays untyped even after upstream grows a per-request seam.",

	"GET /v1/agent/presets": "owned by hanzoai/agent (agent.go:167). Nothing here to convert; " +
		"it becomes typeable in hanzoai/agent, which owns it — the apps/tasks precedent.",

	"GET /v1/agent/conversations": "owned by hanzoai/agent (agent.go:168). Its caller arrives " +
		"through Deps.Principal, a func(*zip.Ctx) over the live request, where a typed op has only " +
		"a context — and hanzoai/agent imports neither cloud nor ai, so cloud.Bridge cannot park " +
		"the org for it; it needs a per-request seam of its own first.",

	"GET /v1/agent/conversations/{id}": "owned by hanzoai/agent (agent.go:169). Same caller seam " +
		"as the list: Deps.Principal reads the live request a typed op never sees.",
}

// mountAgentOnly mounts ONLY this subsystem on a bare app, so the gate below reads
// this package's surface and nothing else's. The per-org stores open lazily, so a
// temp DataDir and a logger are the whole world it needs.
func mountAgentOnly(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("mount agent: %v", err)
	}
	return app
}

// agentOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed registry
// entry. Reading the router — the real Mount, not a reconstruction of it — is what
// makes this a gate rather than prose.
func agentOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mountAgentOnly(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "agent", Version: "v1"})
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

// TestEveryRouteIsTypedOrNamed fails when an agent operation is neither a typed op
// nor one named above — so the next route added here is typed by default, and
// keeping one out of the registry takes a deliberate edit with a reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := agentOps(t)

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
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no "+
			"SDK method. Convert it (zip.Get/Post/... on the group), or add it to untypedByDesign with "+
			"the wire fact that typing it would move.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which agent no longer serves", key)
		}
	}
	// The two ledgers must SUM to the served surface: neither may quietly shrink —
	// and the day upstream ships these as typed ops, THIS is what goes red.
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("typed(%d) + named(%d) = %d, served = %d — the ledgers must partition the surface",
			len(typed), len(untypedByDesign), got, want)
	}
}
