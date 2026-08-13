// Copyright © 2026 Hanzo AI. MIT License.

package fleet_test

// THE ASSISTANT COULD NOT CHECK THE WEATHER, and it was right not to try.
//
// Measured on the deployed door: tools/list carried 88 grouped tools and NONE of
// them was websearch, crawl, index or exec. Not because the fleet cannot search
// the web — apps/websearch is a working keyless meta-search and apps/crawl is a
// working fetch-and-extract, both in-process, both serving over HTTP the whole
// time — but because ONLY TYPED OPS PROJECT. A raw handler appends nothing to
// zip's op registry (zip typed.go, registeredOp), and that registry is the single
// value every projection reads: the REST route, the OpenAPI operation, the SDK
// method, the CLI command and the MCP tool. A subsystem of raw routes therefore
// serves perfectly and is invisible to the agent, which is tracker #190 showing
// up as a product failure rather than as a documentation gap.
//
// So this test asks the question the way a client asks it, and it asks it of the
// REAL thing at every hop: each subsystem composed the way cloud.Serve composes a
// plugin child (cloud.App → its own Mount → the console last), listening on its
// own unix socket, with the fleet's composed door over the top. Nothing is
// stubbed, and in particular [refuse] is not stubbed — a name that trips the
// disclosure or authority rules is dropped in [Door.gather] before the routing
// table is written, so an operation that passes here is one an agent can actually
// reach.
//
// It asserts the OPERATIONS, not the tool count. The door projects one tool per
// subsystem and carries the operations in that tool's `op` enum (fleet/grouped.go),
// so a `websearch` tool existing is not the claim — `search_web` being
// inside it is.

import (
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/ask"
	"github.com/hanzoai/cloud/apps/crawl"
	"github.com/hanzoai/cloud/apps/exec"
	"github.com/hanzoai/cloud/apps/websearch"
	"github.com/hanzoai/cloud/fleet"
)

// reach is one capability the agent needs, and the operation that is its door.
type reach struct {
	app   string
	mount cloud.MountFunc
	op    string
	// why is what the assistant cannot do while this operation is not projected.
	why string
}

// TestTheAgentCanReachTheWeb drives all three over one door at once, because that
// is the composition a client meets: a single tools/list over the whole fleet.
func TestTheAgentCanReachTheWeb(t *testing.T) {
	want := []reach{
		{"websearch", websearch.Mount, "search_web",
			"answer any question about what is happening now — the weather, an outage, a release"},
		{"crawl", crawl.Mount, "read_page",
			"read a page it was given the URL of"},
		{"exec", exec.Mount, "post_exec",
			"run a snippet and report what it printed"},
		{"ask", ask.Mount, "research_web",
			"research a question across many pages and answer it with sources cited"},
	}

	apps := make([]string, 0, len(want))
	kids := map[string]*child{}
	for _, w := range want {
		kids[w.app] = darkChild(t, w.app, w.mount)
		apps = append(apps, w.app)
	}

	h := host(t, apps, kids)
	res := rpc(t, h, toolsListBody)

	// A subsystem that did not answer would make every claim below vacuous: an
	// absent operation and an unreachable app read the same in the enum.
	for _, o := range outages(t, res) {
		t.Fatalf("the door could not ask %s: %s", o.App, o.Error)
	}

	offering := offered(res)
	sort.Strings(offering)
	for _, w := range want {
		// The enum carries the name the door PUBLISHES for an operation, so that
		// is what a model reads and that is what is asked for here. A DECLARED id
		// (read_page, search_web, research_web) is published verbatim; only a
		// route-derived one is rephrased. fleet/verbs.go is why.
		as := fleet.Phrase(w.op)
		if !contains(offering, as) {
			t.Errorf("%s (offered as %s) does NOT project — so the assistant still cannot %s.\n"+
				"  the door offers: %s", w.op, as, w.why, strings.Join(offering, " "))
			continue
		}
		// …and the name resolves back to the operation the subsystem actually
		// serves. describe answers out of the gathered set, so this is the door
		// mapping a published name onto a REAL child's own descriptor — not a
		// string this test computed twice.
		desc := rpc(t, h, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"`+
			fleet.Describe+`","arguments":{"op":"`+as+`"}}}`)
		content, _ := desc["content"].([]any)
		if len(content) == 0 {
			t.Errorf("%s describes to nothing: %v", as, desc)
			continue
		}
		first, _ := content[0].(map[string]any)
		text, _ := first["text"].(string)
		if !strings.Contains(text, `"name":"`+w.op+`"`) {
			t.Errorf("%s describes to something that is not %s: %s", as, w.op, text)
			continue
		}
		t.Logf("%-16s → %-24s projects and resolves, so the assistant can %s", w.op, as, w.why)
	}

	// The tools themselves, for the record: one per subsystem, the operations
	// inside. A reader of this test's output should be able to see the shape.
	t.Logf("door offers %d tools: %s", len(names(res)), strings.Join(names(res), " "))
}

// The OTHER half — that the gate still withholds what it must — is not repeated
// here. It has one home already: `survivors` and `refusals` in
// surface_internal_test.go are where a name is checked against the rule, and the
// three names above are in the survivors table. A second gate assertion would be
// a second place the policy is stated, which is the thing fleet/surface.go's own
// note spends a page avoiding.

func contains(xs []string, want string) bool {
	return slices.Contains(xs, want)
}
