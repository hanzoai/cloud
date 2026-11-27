package manifest

import (
	"slices"
	"testing"
)

// TestGrantMatchesPrefixesForRoutedApps keeps Gates from becoming a SECOND list
// to maintain.
//
// Gates answers "what may this app's middleware wrap" and Prefixes answers "what
// does the host route to it". For an app that serves its own surface those are
// the same subtrees — you gate what you serve — so Gates must stay empty and
// GrantFor must fall through to Prefixes. A routed app that set Gates would have
// two lists that drift apart in different changes with nothing comparing them,
// which is precisely the failure TestNoPluginRestatesItsPrefixes was written for
// after v1.801.318 took inference down fleet-wide.
//
// The exemption is exactly the co-resident app, which routes nothing and wraps
// somebody else's subtree — the one case where the two questions have different
// answers, and the reason the field exists at all.
func TestGrantMatchesPrefixesForRoutedApps(t *testing.T) {
	for _, a := range Apps {
		if a.Coresident {
			continue
		}
		if len(a.Gates) > 0 {
			t.Errorf("%s: routed app sets Gates %v — a routed app gates what it serves, "+
				"so Prefixes is the one list and Gates must stay empty", a.Name, a.Gates)
			continue
		}
		if got, want := GrantFor(a.Name), PrefixesFor(a.Name); !slices.Equal(got, want) {
			t.Errorf("%s: GrantFor = %v, PrefixesFor = %v — they must coincide for a routed app", a.Name, got, want)
		}
	}
}

// TestCoresidentAppStatesItsGate closes the hole that actually shipped. A
// co-resident app claims no prefix, so UsePrefixes falls back to the
// conventional "/v1/<name>" — a subtree it does not serve and did not ask for.
// zen mounted its Claim on "/v1" against a grant of "/v1/zen", scope recorded the
// escape, and UseAll refused the mount: the zen plugin binary could not boot.
//
// A co-resident app is middleware BY DEFINITION, so it always wraps something,
// and that something can never be inferred from a routing table it is absent
// from. Naming it is the whole contract.
func TestCoresidentAppStatesItsGate(t *testing.T) {
	for _, a := range Apps {
		if !a.Coresident {
			continue
		}
		if len(a.Gates) == 0 {
			t.Errorf("%s: co-resident but names no Gates — it mounts as middleware on "+
				"another app's subtree, and without the grant its mount is refused", a.Name)
		}
		if got := GrantFor(a.Name); len(got) == 0 {
			t.Errorf("%s: GrantFor is empty; the grant is what scope bounds its middleware to", a.Name)
		}
	}
}
