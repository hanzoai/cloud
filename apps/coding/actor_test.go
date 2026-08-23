package coding

// actor_test.go pins WHO a run acts as on the forge. Three defects reached
// production through this one resolution, each exposed by fixing the last.
//
//	the id      X-User-Id is the token's sub, a UUID; Sudo takes a LOGIN, so the
//	            forge answered "unknown actor" for everyone and every run refused.
//	the guess   deriving the login from an address drops the domain, so many
//	            addresses derive one login — and every self-serve signup lands in
//	            the same org as the staff, so a stranger could derive a
//	            colleague's login and be handed a write key to the monorepo.
//	the claim   an address a person merely TYPED is not evidence they hold it, so
//	            the ownership check above could be satisfied by claiming one.
//
// So the chain is: subject (which the plane carries and nothing forges) → the
// address IAM holds for it → CONFIRMED → the forge agrees that login is theirs.

import (
	"context"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// asCaller builds the context an endpoint hands Start. Only the SUBJECT matters now —
// the address is read from the identity store, not from the request.
func asCaller(subject string) context.Context {
	return zip.WithCaller(context.Background(), zip.Caller{Org: "hanzo", User: subject})
}

// verified/unverified state the identity store's answer for a subject.
func verified(addr string) plane.Email   { return plane.Email{Address: addr, Verified: true} }
func unverified(addr string) plane.Email { return plane.Email{Address: addr, Verified: false} }

// THE OWNER RESOLVES AND IS ENTITLED. A control that refuses everybody is an
// outage, not a control — this is the case that must keep working.
func TestResolveActor_TheOwnerResolvesAndIsEntitled(t *testing.T) {
	iamAddr = map[string]plane.Email{"sub-z": verified("z@hanzo.ai")}
	minted := forgeAs(t, map[string]bool{"z": true},
		func(login string) string { return login + "@hanzo.ai" })
	ctx := asCaller("sub-z")

	actor, err := resolveActor(ctx)
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

// THE ESCALATION: a stranger whose address derives a colleague's login. The
// forge holds the colleague's address against that login, so the two disagree.
func TestResolveActor_RefusesAStrangerWhoDerivedAColleaguesLogin(t *testing.T) {
	iamAddr = map[string]plane.Email{"sub-x": verified("z@attacker.example")}
	minted := forgeAs(t, map[string]bool{"z": true},
		func(login string) string { return login + "@hanzo.ai" })

	actor, err := resolveActor(asCaller("sub-x"))
	if err == nil {
		t.Fatalf("a stranger was resolved to the forge identity %q", actor)
	}
	if len(*minted) != 0 {
		t.Fatalf("a refused caller still registered a key: %v", *minted)
	}
}

// AN UNVERIFIED ADDRESS IS REFUSED EVEN WHEN IT WOULD MATCH.
//
// This is the case the ownership check alone cannot see: the address IS the
// forge account's, so LoginFor would agree — but the person only typed it. A
// direct signup records it unconfirmed, which is exactly the attacker's path.
func TestResolveActor_RefusesAnUnverifiedAddress(t *testing.T) {
	iamAddr = map[string]plane.Email{"sub-imposter": unverified("z@hanzo.ai")}
	minted := forgeAs(t, map[string]bool{"z": true},
		func(login string) string { return login + "@hanzo.ai" })

	_, err := resolveActor(asCaller("sub-imposter"))
	if err == nil {
		t.Fatal("an unconfirmed address was accepted as an identity")
	}
	if !strings.Contains(err.Error(), "verify") {
		t.Fatalf("the refusal does not tell the person what to do: %v", err)
	}
	if len(*minted) != 0 {
		t.Fatalf("a refused caller still registered a key: %v", *minted)
	}
}

// AN UNREACHABLE IDENTITY STORE REFUSES. Never an assumption that the address
// was fine — the whole gate is that assumption's absence.
func TestResolveActor_RefusesWhenTheStoreCannotAnswer(t *testing.T) {
	iamAddr = map[string]plane.Email{} // the subject is unknown to the store
	forgeAs(t, map[string]bool{"z": true}, func(login string) string { return login + "@hanzo.ai" })

	if _, err := resolveActor(asCaller("sub-nobody")); err == nil {
		t.Fatal("a principal the identity store does not know was given an identity")
	}
}

// A subject the store knows but holds no address for is refused before the
// forge is asked anything.
func TestResolveActor_RefusesWithNoAddress(t *testing.T) {
	iamAddr = map[string]plane.Email{"sub-blank": verified("")}
	forgeAs(t, map[string]bool{"z": true}, func(login string) string { return login + "@hanzo.ai" })

	_, err := resolveActor(asCaller("sub-blank"))
	if err == nil {
		t.Fatal("a caller with no address was given a forge identity")
	}
	if !strings.Contains(err.Error(), "no address") {
		t.Fatalf("refused for the wrong reason: %v", err)
	}
}
