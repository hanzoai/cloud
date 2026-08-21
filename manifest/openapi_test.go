package manifest

// The gates under the fleet's ONE spec door, GET /v1/openapi.json.
//
// It is the host's — cmd/cloud's spec(), serving the weave of every plugin's
// build-time subset — for the same reason POST /v1/mcp is (mcp_test.go): the
// answer is about the WHOLE fleet, and a plugin cannot see past itself. What is
// pinned here is the routing half of that claim, asked of the router rather than
// of any document.

import (
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// TestNoAppClaimsAHostDoor: no manifest row may claim a host door exactly.
//
// Specificity is what makes the host's static route win over ai's "/v1" — a
// static path beats the wildcard containing it whatever order they register in.
// An EQUAL claim is the one thing that defeats it, and it defeats it silently:
// a Load registers All(prefix), fiber merges byte-identical patterns into one
// route with both handlers chained, and the host's GET would sit BEHIND the
// proxy handler and never run. Same trap, same shape, as the MCP door.
//
// It asks openapi.Routed rather than openapi.Door because the trap needs a route
// to spring: the two INDEX doors are answered ahead of the router precisely
// because a row already claims where they sit — ai's "/v1" remainder is one of
// them — so a claim there merges with nothing. See openapi.Routed.
func TestNoAppClaimsAHostDoor(t *testing.T) {
	for _, a := range Apps {
		for _, p := range a.Prefixes {
			if openapi.Routed(p) {
				t.Fatalf("app %q claims %q, one of the host's own doors (openapi.Door). A Load "+
					"there registers All(%q), which fiber merges with the host's GET into one "+
					"route — the door would sit behind the proxy handler and never run, and the "+
					"fleet would answer for the whole API with %s's single-app view of it. Claim "+
					"a DEEPER prefix or none.", a.Name, p, p, a.Name)
			}
		}
	}
}

// TestTheManifestAloneMisroutesTheSpecDoor is the production defect, recorded at
// the layer that causes it.
//
// Ask the router what the manifest ALONE does with /v1/openapi.json and the
// answer is "ai": no row names it, so it falls to the only prefix that covers it
// — ai's bare "/v1" — and reaches the ai child, whose whole AI surface is one
// greedy All("/v1/*"). api.hanzo.ai then published EIGHT paths of the child's own
// router (health, iam edge, zap, console catch-all, wildcard) as the Hanzo Cloud
// API, 200 OK, to every SDK generator that read it.
//
// So this is not an assertion that the misroute is FINE — it is the statement of
// what the host's registration is for, kept true so that the day an app row does
// name the path deeper, this goes red and someone reads why the host claims it.
// The fix itself is pinned where it lives, end to end, in cmd/cloud/openapi_test.go.
func TestTheManifestAloneMisroutesTheSpecDoor(t *testing.T) {
	if to := destination(t, router(t), openapi.Path); to != "ai" {
		t.Fatalf("the manifest routes %s to %q, not to the /v1 catch-all this fleet has always "+
			"sent it to. Something claimed the spec door — check that cmd/cloud's spec() still "+
			"wins, because nothing else describes the whole fleet.", openapi.Path, to)
	}
}
