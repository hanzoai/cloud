package cloud

import (
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

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

// ── the limit crossing the boundary ─────────────────────────────────────────
//
// Everything above asks Covers about a grant it holds in its hand. A request
// does not: the boundary resolves the credential and the handler asks about it
// several middlewares later, through GrantOf, which reads ONE field of the
// boundary's attestation and consults nothing else. So the rule is only as true
// as that field, and a mint that copied the org and left the limit behind
// answers the empty grant for every key in the estate — which restricts nothing,
// the right answer for a key issued without a scope and the wrong one for a key
// issued with one. It reads as working from either side alone: the boundary
// hands the credential's scope in, Covers narrows correctly when handed a grant,
// and in between the value is gone while the operator's own listing still calls
// the key restricted.
//
// So it is asserted over the COMPOSITION — minted on one middleware, asked on
// the handler, the way a request actually runs.

// probeGrant runs one request whose boundary minted `scope`, and reports what
// the handler's GrantOf says about the model the key names and one it does not.
func probeGrant(t *testing.T, scope string) (named, another bool) {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Use(zip.H(func(c *zip.Ctx) error {
		principal.Mint(c, principal.Principal{Org: "acme", User: "u-1", Limit: ParseGrant(scope)})
		return c.Continue()
	}))
	app.Get("/probe", func(c *zip.Ctx) error {
		g := GrantOf(c)
		named, another = g.Covers("model", "zen5"), g.Covers("model", "zen6")
		return c.JSON(200, map[string]bool{"named": named, "another": another})
	})
	if _, err := app.Test(httptest.NewRequest("GET", "/probe", nil)); err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	return named, another
}

func TestAMintedPrincipalCarriesTheGrantItsCredentialWasIssuedWith(t *testing.T) {
	for _, tc := range []struct {
		what           string
		scope          string
		named, another bool
	}{
		// THE REFUSAL. A key issued for one model must not reach another, and
		// this is the only place in the rule where that can be lost.
		{"issued for one model", "model:zen5", true, false},
		// ...and the three shapes that must keep working, because a limit that
		// over-narrows is an outage rather than a leak.
		{"issued with no limit at all", "", true, true},
		{"issued for the whole kind", "model:*", true, true},
		{"issued for a different kind", "project:acme", true, true},
	} {
		t.Run(tc.what, func(t *testing.T) {
			named, another := probeGrant(t, tc.scope)
			if named != tc.named {
				t.Errorf("reaches the model it was issued for = %v, want %v", named, tc.named)
			}
			if another != tc.another {
				t.Errorf("reaches a model it was not issued for = %v, want %v — "+
					"the credential's limit did not survive the mint, so the key the listing "+
					"calls restricted reaches everything", another, tc.another)
			}
		})
	}
}

// The minted limit is the boundary's OWN copy, entry by entry. The credential's
// grant is shared with the resolver that read it and cached it, so an aliased
// mint would let anything holding the attestation rewrite what the NEXT request
// on that key is told it may reach.
func TestTheMintedLimitIsTheBoundarysOwnCopy(t *testing.T) {
	scope := ParseGrant("model:zen5")
	var got Grant
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Use(zip.H(func(c *zip.Ctx) error {
		principal.Mint(c, principal.Principal{Org: "acme", User: "u-1", Limit: scope})
		return c.Continue()
	}))
	app.Get("/probe", func(c *zip.Ctx) error {
		got = GrantOf(c)
		return c.JSON(200, map[string]bool{"ok": true})
	})
	if _, err := app.Test(httptest.NewRequest("GET", "/probe", nil)); err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	scope[0] = "model:*"
	if got.Covers("model", "zen6") {
		t.Error("rewriting the credential's own grant widened one already minted")
	}
}
