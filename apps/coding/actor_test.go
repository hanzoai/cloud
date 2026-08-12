package coding

// actor_test.go pins WHO a run acts as on the forge, and it exists because two
// separate defects reached production through this one function.
//
// The first was the id: the door passed X-User-Id — the token's `sub`, a UUID —
// to Sudo, which takes a LOGIN, so the forge answered "unknown actor" for every
// caller and every run refused.
//
// The second was worse and was the FIX's own doing. Deriving the login from the
// address drops the domain, so many addresses derive one login — and every
// self-serve signup lands in the same org as the staff (account.SignupOrg is
// "hanzo", which forge.Owner maps to the estate's own namespace). A stranger
// could sign up, derive a colleague's login, and be handed a write key to the
// monorepo. The derivation is a guess; only the forge can confirm it.

import (
	"context"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// asCaller builds the context a door hands Start, carrying the identity headers
// the gateway mints from validated claims.
func asCaller(c zip.Caller) context.Context {
	return zip.WithCaller(context.Background(), c)
}

// THE ADDRESS, NEVER THE SUBJECT. The id is not a login and must not be handed
// to the forge as one.
func TestActorOf_IsTheAddressAndNeverTheSubject(t *testing.T) {
	const sub = "b474eaa5-e000-474d-b317-c37bbd9a6945"
	got := actorOf(asCaller(zip.Caller{
		Org: "hanzo", User: sub, Name: "a", Email: "a@hanzo.ai",
	}))
	if got == sub {
		t.Fatal("actorOf returned the subject UUID; Sudo takes a login and the forge " +
			"answers `unknown actor` for every real user")
	}
	if got != "a@hanzo.ai" {
		t.Fatalf("actorOf = %q, want the validated address", got)
	}
}

// No address is no identity. The IAM username is NOT a fallback: it derives a
// login just as well and there is nothing to check it against, which is exactly
// the unproven guess this design removes.
func TestActorOf_HasNoUsernameFallback(t *testing.T) {
	got := actorOf(asCaller(zip.Caller{
		Org: "hanzo", User: "b474eaa5-e000-474d-b317-c37bbd9a6945", Name: "a",
	}))
	if got != "" {
		t.Fatalf("actorOf = %q; a caller with no address must resolve to no identity", got)
	}
}

// THE ESCALATION, end to end through the door's own resolution.
//
// `z@attacker.example` derives login `z`, which on the forge belongs to a member
// of staff. Without the ownership check the stranger acts as them and is handed
// a write deploy key on the estate's monorepo.
func TestResolveActor_RefusesAStrangerWhoDerivedAColleaguesLogin(t *testing.T) {
	minted := forgeAs(t, map[string]bool{"z": true},
		func(login string) string { return login + "@hanzo.ai" })
	ctx := asCaller(zip.Caller{Org: "hanzo", User: "sub", Email: "z@attacker.example"})

	// The attacker controls their ADDRESS and nothing else — the login is not a
	// parameter of the door, it is resolved from that address. So the attack is
	// exactly this call, and it must not produce an identity.
	actor, err := resolveActor(ctx, actorOf(ctx))
	if err == nil {
		t.Fatalf("a stranger was resolved to the forge identity %q", actor)
	}
	// Nothing was registered on the way to refusing them: the resolution happens
	// before anything is minted, so a refused caller leaves no trace on the
	// repository they were reaching for.
	if len(*minted) != 0 {
		t.Fatalf("a refused caller still registered a key: %v", *minted)
	}
}

// The owner of that address still resolves, and still gets a key. A control
// that refuses everybody is an outage.
func TestResolveActor_TheOwnerResolvesAndIsEntitled(t *testing.T) {
	minted := forgeAs(t, map[string]bool{"z": true},
		func(login string) string { return login + "@hanzo.ai" })
	ctx := asCaller(zip.Caller{Org: "hanzo", User: "sub", Email: "z@hanzo.ai"})

	actor, err := resolveActor(ctx, actorOf(ctx))
	if err != nil {
		t.Fatalf("the owner was refused their own login: %v", err)
	}
	if actor != "z" {
		t.Fatalf("resolved actor = %q, want z", actor)
	}
	if _, err := delegate(ctx, "hanzo", actor, "cloud", "sess_1"); err != nil {
		t.Fatalf("an entitled owner was refused a key: %v", err)
	}
	if len(*minted) != 1 {
		t.Fatalf("no key was minted for an entitled caller: %v", *minted)
	}
}

// A caller with no address is refused before the forge is asked anything.
func TestResolveActor_NoAddressRefuses(t *testing.T) {
	forgeAs(t, map[string]bool{"z": true}, func(login string) string { return login + "@hanzo.ai" })
	ctx := asCaller(zip.Caller{Org: "hanzo", User: "sub", Name: "z"})

	_, err := resolveActor(ctx, actorOf(ctx))
	if err == nil {
		t.Fatal("a caller with no address was given a forge identity")
	}
	if !strings.Contains(err.Error(), "no verified address") {
		t.Fatalf("refused for the wrong reason: %v", err)
	}
}
