package pricing

import (
	"testing"

	"github.com/hanzoai/cloud"
)

func models(ids ...string) []Model {
	out := make([]Model, 0, len(ids))
	for _, id := range ids {
		out = append(out, Model{"id": id})
	}
	return out
}

func ids(ms []Model) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, modelID(m))
	}
	return out
}

// A CREDENTIAL LIMIT IS A SECOND NARROWING, NOT A WIDER FIRST ONE.
//
// VisibleCatalog answers what an ORG may see; Reachable answers how much of that
// the key in the caller's hand carries. Braiding them into one function would
// make an answer that cannot say which rule hid a model — and would put the
// credential limit behind the isAdmin branch, which exists to show an admin
// everything.
func TestReachableNarrowsWhatTheOrgCanAlreadySee(t *testing.T) {
	catalog := models("zen5", "zen6", "opus")

	// No limit: the credential is not the thing narrowing, so nothing moves.
	if got := ids(Reachable(catalog, nil)); len(got) != 3 {
		t.Fatalf("an unlimited credential must see the whole catalog, got %v", got)
	}
	if got := ids(Reachable(catalog, cloud.ParseGrant(""))); len(got) != 3 {
		t.Fatalf("an empty grant must not restrict, got %v", got)
	}

	// A limit names what it reaches and excludes the rest.
	got := ids(Reachable(catalog, cloud.ParseGrant("model:zen5")))
	if len(got) != 1 || got[0] != "zen5" {
		t.Fatalf("want [zen5], got %v", got)
	}

	// A limit of ANOTHER kind is not a model limit. A key scoped to a project
	// must not thereby be scoped to zero models — this is the per-kind rule
	// arriving at a real call site.
	if got := ids(Reachable(catalog, cloud.ParseGrant("project:acme"))); len(got) != 3 {
		t.Fatalf("a project limit must not hide models, got %v", got)
	}

	// The whole kind.
	if got := ids(Reachable(catalog, cloud.ParseGrant("model:*"))); len(got) != 3 {
		t.Fatalf("model:* must reach every model, got %v", got)
	}
}

// THE LIMIT SURVIVES BEING AN ADMIN.
//
// isAdmin makes VisibleCatalog show every model, deliberately. The credential
// limit is a property of the KEY, so it must still apply — otherwise a limit is
// something any platform admin sheds by being one, and "a key restricted to one
// model" would be false for exactly the accounts that can do the most damage.
func TestAnAdminsLimitedKeyIsStillLimited(t *testing.T) {
	full := models("zen5", "hidden")
	snap := map[string]Overlay{overlayKey(kindModel, "hidden"): {Kind: kindModel, ID: "hidden", Enabled: false}}

	asAdmin := VisibleCatalog(full, snap, "acme", true)
	if len(asAdmin) != 2 {
		t.Fatalf("precondition: an admin sees every model, got %v", ids(asAdmin))
	}
	got := ids(Reachable(asAdmin, cloud.ParseGrant("model:zen5")))
	if len(got) != 1 || got[0] != "zen5" {
		t.Fatalf("an admin's limited key must still be limited, got %v", got)
	}

	// And the org rule still applies to a non-admin, independently: composing
	// the two must not let either stand in for the other.
	asMember := VisibleCatalog(full, snap, "acme", false)
	if len(asMember) != 1 || modelID(asMember[0]) != "zen5" {
		t.Fatalf("the org rule must still hide a disabled model, got %v", ids(asMember))
	}
	if got := ids(Reachable(asMember, nil)); len(got) != 1 {
		t.Fatalf("an unlimited credential must not re-widen what the org hid, got %v", got)
	}
}
