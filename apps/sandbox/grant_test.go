package sandbox

import (
	"strings"
	"testing"
	"time"
)

// A grant authorises INFERENCE, for ONE org, from ONE sandbox, until ONE moment.
// Every test here is one of those words, because two credential designs have
// already failed review for holding authority beyond exactly this.

func TestGrantAnswersOnlyItsOwnOrg(t *testing.T) {
	g := newGrants()
	now := time.Now()
	a, err := g.mint(now, "acme", "sbx-1", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	b, err := g.mint(now, "other", "sbx-2", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	org, sbx, ok := g.check(now, a)
	if !ok || org != "acme" || sbx != "sbx-1" {
		t.Fatalf("acme's grant answered %q/%q ok=%v", org, sbx, ok)
	}
	// The whole cross-tenant question, asked directly: one org's credential can
	// never answer another's. This is the property the rejected design lost when
	// it widened a general resolver.
	if org, _, _ := g.check(now, b); org == "acme" {
		t.Fatal("another org's grant answered as acme")
	}
}

// A GRANT IS NOT SINGLE-USE, and that is deliberate rather than an oversight: a
// terminal ticket is spent on one dial, but a run calls the model for as long as
// it works, and redeeming would kill it on its second thought.
func TestGrantSurvivesRepeatedUse(t *testing.T) {
	g := newGrants()
	now := time.Now()
	tok, _ := g.mint(now, "acme", "sbx-1", now.Add(time.Hour))
	for i := 0; i < 50; i++ {
		if _, _, ok := g.check(now, tok); !ok {
			t.Fatalf("grant died on use %d — a run thinks more than once", i+1)
		}
	}
}

// THE LEASE IS THE BOUND. When the sandbox is over the credential inside it is
// already worthless, which is what makes "the pod was compromised" a bounded
// statement rather than an open one.
func TestGrantDiesWithItsLease(t *testing.T) {
	g := newGrants()
	now := time.Now()
	tok, _ := g.mint(now, "acme", "sbx-1", now.Add(time.Minute))
	if _, _, ok := g.check(now.Add(59*time.Second), tok); !ok {
		t.Fatal("grant expired before its lease")
	}
	if _, _, ok := g.check(now.Add(61*time.Second), tok); ok {
		t.Fatal("grant outlived its lease")
	}
}

// REVOCATION IS THE EARLY CASE, and expiry alone does not cover it. A sandbox
// released before its window ends — a run that finished, a user who stopped it —
// would otherwise leave a working credential for a pod that is gone.
func TestRevokeKillsTheGrantImmediately(t *testing.T) {
	g := newGrants()
	now := time.Now()
	mine, _ := g.mint(now, "acme", "sbx-1", now.Add(time.Hour))
	theirs, _ := g.mint(now, "acme", "sbx-2", now.Add(time.Hour))

	g.revoke("sbx-1")
	if _, _, ok := g.check(now, mine); ok {
		t.Fatal("a released sandbox's grant still works")
	}
	// And it revokes ONE sandbox, not the org: a second run for the same tenant
	// must not be collateral.
	if _, _, ok := g.check(now, theirs); !ok {
		t.Fatal("revoking one sandbox killed another's grant")
	}
}

func TestUnknownAndEmptyTokensAreRefused(t *testing.T) {
	g := newGrants()
	now := time.Now()
	for _, tok := range []string{"", "hsb_not-a-real-token", "acme", "hsb_"} {
		if _, _, ok := g.check(now, tok); ok {
			t.Fatalf("an invented token was accepted: %q", tok)
		}
	}
}

// The token must be unguessable and recognisable: 32 bytes of randomness so it
// cannot be searched for, and a prefix so a leaked one can be identified in a log
// and revoked without knowing which run it came from.
func TestGrantTokensAreRandomAndPrefixed(t *testing.T) {
	g := newGrants()
	now := time.Now()
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		tok, err := g.mint(now, "acme", "sbx", now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(tok, "hsb_") {
			t.Fatalf("token has no identifying prefix: %q", tok)
		}
		if len(tok) < 40 {
			t.Fatalf("token is too short to be unguessable: %d chars", len(tok))
		}
		if seen[tok] {
			t.Fatal("a token repeated")
		}
		seen[tok] = true
	}
}

// An expired grant must not sit in the map forever: mint sweeps, so a fleet that
// keeps leasing sandboxes cannot grow this without bound.
func TestExpiredGrantsAreSwept(t *testing.T) {
	g := newGrants()
	now := time.Now()
	for i := 0; i < 100; i++ {
		if _, err := g.mint(now, "acme", "sbx", now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if g.size() < 100 {
		t.Fatalf("expected 100 live grants, got %d", g.size())
	}
	if _, err := g.mint(now.Add(time.Hour), "acme", "sbx-new", now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := g.size(); n != 1 {
		t.Fatalf("mint did not sweep the expired: %d still live", n)
	}
}
