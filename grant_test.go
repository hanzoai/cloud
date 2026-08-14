package cloud

import "testing"

// The property that makes a grant safe is that it only ever NARROWS, and the
// property that makes it shippable is that an unrestricted key keeps working.
// Both are asserted here, because getting either wrong is silent: a grant that
// widens is a privilege bug nothing reports, and a default that restricts
// revokes every key in the estate on the deploy that ships it.

func TestEmptyGrantRestrictsNothing(t *testing.T) {
	// All eleven live keys carry no scope. If this is ever false, shipping the
	// feature is an outage.
	for _, scope := range []string{"", "   ", ",", " , "} {
		g := ParseGrant(scope)
		if !g.Covers("model", "zen5") || !g.Covers("project", "acme") {
			t.Fatalf("scope %q must restrict nothing, got %#v", scope, g)
		}
	}
}

func TestRestrictionIsPerKind(t *testing.T) {
	g := ParseGrant("project:acme")
	if !g.Covers("model", "zen5") {
		t.Fatal("a key limited to a project must not thereby be limited to zero models")
	}
	if g.Covers("project", "other") {
		t.Fatal("a named project must exclude the others")
	}
	if !g.Covers("project", "acme") {
		t.Fatal("the named project must be reachable")
	}
}

func TestWildcardIsTheWholeKind(t *testing.T) {
	g := ParseGrant("model:*")
	if !g.Covers("model", "zen5") || !g.Covers("model", "anything") {
		t.Fatal("model:* must cover the kind")
	}
}

func TestUnnamedTargetIsRefusedWhenTheKindIsRestricted(t *testing.T) {
	if ParseGrant("model:zen5").Covers("model", "") {
		t.Fatal("a credential naming a limit must not be satisfied by silence")
	}
	// `model:` is a malformed entry — it restricts the kind and names nothing.
	// It must not become the one grant that matches an unnamed target, which is
	// what an equality test alone would let it be.
	if ParseGrant("model:").Covers("model", "") {
		t.Fatal("an empty entry must not satisfy an unnamed target")
	}
	// ...but an unrestricted kind is still unrestricted, empty name or not.
	if !ParseGrant("").Covers("model", "") {
		t.Fatal("an unrestricted key must not start refusing unnamed targets")
	}
}

func TestPublishIsAClassNotAReach(t *testing.T) {
	g := ParseGrant("publish")
	if !g.Publishable() {
		t.Fatal("publish must still read as the publishable class")
	}
	if !g.Covers("model", "zen5") {
		t.Fatal("the publish CLASS must not be read as a model restriction")
	}
	// The two shapes coexist in one field.
	both := ParseGrant("publish,model:zen5")
	if !both.Publishable() {
		t.Fatal("a narrowed publishable key is still publishable")
	}
	if both.Covers("model", "other") {
		t.Fatal("the reach half must still restrict")
	}
	if !both.Covers("model", "zen5") {
		t.Fatal("the reach half must still admit what it names")
	}
	for _, scope := range []string{"model:zen5", "product:publishing", "project:publisher"} {
		if ParseGrant(scope).Publishable() {
			t.Fatalf("%q is a reach, not the publish class", scope)
		}
	}
}

func TestAGrantCannotWiden(t *testing.T) {
	// There is no entry that grants a kind a key's holder lacks — the only
	// verdicts are "unrestricted" and "narrowed". This pins that no spelling of a
	// grant makes Covers return true for a kind that is restricted and unnamed.
	for _, scope := range []string{"model:zen5", "model:zen5,model:zen6", "publish,model:zen5"} {
		if ParseGrant(scope).Covers("model", "not-named") {
			t.Fatalf("scope %q admitted a model it does not name", scope)
		}
	}
}

func TestALimitNothingEnforcesIsRefused(t *testing.T) {
	// The worst outcome is a limit that is stored, listed, and consulted by
	// nothing: it reads as a narrowing everywhere a human looks and is one
	// nowhere. So the kind set is closed at the mint.
	for _, bad := range []string{"region:eu", "ip:10.0.0.1", "model", "model:", ":zen5", "notakind:x"} {
		if _, err := ParseLimit([]string{bad}); err == nil {
			t.Fatalf("%q must be refused — nothing asks that question", bad)
		}
	}
	for _, ok := range []string{"model:zen5", "project:acme", "product:commerce", "model:*"} {
		if _, err := ParseLimit([]string{ok}); err != nil {
			t.Fatalf("%q must be accepted: %v", ok, err)
		}
	}
}

func TestALimitRoundTripsThroughTheOneStoredField(t *testing.T) {
	// "model: zen5" is the case the outer trim does NOT reach: the space is inside
	// the entry, so without the per-part trim it stores a name with a leading
	// space that no lookup will ever match — a limit that silently denies
	// everything, which is the failure that looks like a working restriction.
	g, err := ParseLimit([]string{" model:zen5 ", "project:acme", "model: zen5"})
	if err != nil {
		t.Fatal(err)
	}
	if got := g.String(); got != "model:zen5,project:acme" {
		t.Fatalf("duplicates must collapse and entries must trim, got %q", got)
	}
	back := ParseGrant(g.String())
	if !back.Covers("model", "zen5") || back.Covers("model", "zen6") {
		t.Fatal("the stored field must read back as the same narrowing")
	}
	if back.Covers("project", "other") || !back.Covers("project", "acme") {
		t.Fatal("every kind must survive the round trip")
	}
}

func TestEveryEnforceableKindIsAskedSomewhere(t *testing.T) {
	// GrantKinds is a promise that something consults each kind. This is the
	// half a reviewer cannot check by reading: it fails if a kind is added to
	// the list without the Covers call that asks it.
	for _, kind := range GrantKinds {
		g := ParseGrant(kind + ":only")
		if g.Covers(kind, "something-else") {
			t.Fatalf("%q is declared enforceable but does not narrow", kind)
		}
	}
}
