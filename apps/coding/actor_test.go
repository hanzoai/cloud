package coding

// actor_test.go pins WHICH identity a run acts as on the forge.
//
// This is a live regression. The door passed X-User-Id — the token's `sub`, a
// UUID — to Forgejo's Sudo, which takes a LOGIN, so the forge answered
//
//	forge: unknown actor — no forge identity for this user: b474eaa5-…
//
// and every coding run refused with a 400. The two headers are deliberately
// distinct (middleware_identity.go stamps X-User-Name from preferred_username
// and X-User-Id from the subject), so a test that only checked "an actor was
// passed" would have passed throughout.

import (
	"context"
	"testing"

	"github.com/zap-proto/zip"
)

// asCaller builds the context a door hands Start, carrying the identity headers
// the gateway mints from validated claims.
func asCaller(c zip.Caller) context.Context {
	return zip.WithCaller(context.Background(), c)
}

// THE LOGIN, NOT THE SUBJECT. A real user token carries both, and only one of
// them is a thing the forge can look up.
func TestActorOf_IsTheLoginAndNeverTheSubject(t *testing.T) {
	const (
		sub   = "b474eaa5-e000-474d-b317-c37bbd9a6945" // X-User-Id, the token's sub
		login = "a"                                    // what the forge holds
	)
	got := actorOf(asCaller(zip.Caller{
		Org: "hanzo", User: sub, Name: login, Email: "a@hanzo.ai",
	}))
	if got == sub {
		t.Fatal("actorOf returned the subject UUID; Sudo takes a login and the forge " +
			"answers `unknown actor` for every real user")
	}
	if got != login {
		t.Fatalf("actorOf = %q, want the IAM username %q", got, login)
	}
}

// NO FALLBACK TO THE SUBJECT. A caller with an id and neither an email nor a
// name has no forge login, and a UUID is not one — falling back would move the
// failure from here to the forge, one layer further from its cause.
func TestActorOf_DoesNotFallBackToTheSubject(t *testing.T) {
	got := actorOf(asCaller(zip.Caller{Org: "hanzo", User: "b474eaa5-e000-474d-b317-c37bbd9a6945"}))
	if got != "" {
		t.Fatalf("actorOf = %q; a caller with no email and no username must resolve to no actor", got)
	}
}

// THE EMAIL DECIDES, because that is what the forge registered the user by
// (oauth2_client.USERNAME=email). Where the IAM username and the address
// disagree, following the username would address a different account — or none.
func TestActorOf_PrefersTheEmailDerivedLoginOverTheIAMUsername(t *testing.T) {
	got := actorOf(asCaller(zip.Caller{
		Org: "hanzo", User: "sub-uuid", Name: "zoo-admin", Email: "z@hanzo.ai",
	}))
	if got != "z" {
		t.Fatalf("actorOf = %q, want the login the forge derived from the address (%q)", got, "z")
	}
}

// The IAM username is the fallback for a principal carrying no email — an API
// key, for one. It is a different spelling of the SAME person, so it cannot
// escalate, and the forge still decides whether it knows the name.
func TestActorOf_FallsBackToTheUsernameWithNoEmail(t *testing.T) {
	got := actorOf(asCaller(zip.Caller{Org: "hanzo", User: "sub-uuid", Name: "a"}))
	if got != "a" {
		t.Fatalf("actorOf = %q, want the IAM username when there is no address", got)
	}
}

// Whitespace is not an identity. A header that is blank-but-present must read
// as absent, or it reaches Sudo as a login nobody holds.
func TestActorOf_BlankNameIsNoActor(t *testing.T) {
	for _, name := range []string{"", "   ", "\t"} {
		if got := actorOf(asCaller(zip.Caller{Org: "hanzo", User: "sub", Name: name, Email: name})); got != "" {
			t.Fatalf("actorOf(%q) = %q, want no actor", name, got)
		}
	}
}

// An unresolvable actor still REFUSES, and never falls back to the machine —
// the machine is a site administrator on this forge.
func TestDelegate_RefusesWhenTheCallerHasNoLogin(t *testing.T) {
	minted := forgeFor(t, "a")
	ctx := asCaller(zip.Caller{Org: "hanzo", User: "b474eaa5-e000-474d-b317-c37bbd9a6945"})

	if _, err := delegate(ctx, "hanzo", actorOf(ctx), "cloud", "sess_1"); err == nil {
		t.Fatal("a caller with no forge login was handed a key")
	}
	if len(*minted) != 0 {
		t.Fatalf("a refused delegation still registered a key: %v", *minted)
	}
}

// And the resolved login is the one the forge is asked about — end to end,
// through the same delegate the door calls.
func TestDelegate_SudoesAsTheResolvedLogin(t *testing.T) {
	minted := forgeFor(t, "a") // only login `a` may push
	ctx := asCaller(zip.Caller{
		Org: "hanzo", User: "b474eaa5-e000-474d-b317-c37bbd9a6945", Email: "a@hanzo.ai",
	})

	if _, err := delegate(ctx, "hanzo", actorOf(ctx), "cloud", "sess_1"); err != nil {
		t.Fatalf("the resolved login was refused by the forge: %v", err)
	}
	if len(*minted) != 1 {
		t.Fatalf("no key was minted for an entitled caller: %v", *minted)
	}
}
