package ai

// run_test.go asks the only questions that matter about a credential handed to a
// box running model output: whose money can it spend, what else can it open, and
// when does it stop working.

import (
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// mint issues a grant directly against the table, which is what the plane door
// does once it has settled the caller's org. Tests that care about the DOOR (that
// the org is the caller's and never an argument) go through planeRunGrant.
func mint(t *testing.T, org, run string, ttl time.Duration) (token, handle string) {
	t.Helper()
	token, handle, err := granted.issue(runGrant{org: org, run: run}, ttl)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	t.Cleanup(func() { granted.revoke(org, handle) })
	return token, handle
}

// ONE ORG'S RUN CANNOT SPEND ANOTHER ORG'S MONEY. This is the property the whole
// design exists for: the org is IN the grant, so there is no argument on the
// resolution path that could name a different one, and two live grants for two
// tenants never see each other.
func TestAGrantSpendsOnlyItsOwnOrg(t *testing.T) {
	a, _ := mint(t, "acme", "run-a", time.Minute)
	b, _ := mint(t, "globex", "run-b", time.Minute)

	for token, want := range map[string]string{a: "acme", b: "globex"} {
		gr, ok := granted.lookup(token)
		if !ok {
			t.Fatal("a live grant did not resolve")
		}
		if gr.org != want {
			t.Fatalf("grant billed %q, want %q — one tenant's run reached another's ledger", gr.org, want)
		}
	}

	// And a token nobody issued is nobody's grant, however well-formed.
	if _, ok := granted.lookup(runPrefix + "forged"); ok {
		t.Fatal("a forged token resolved to a grant")
	}
}

// A GRANT IS NOT A KEY. cloud sends anything wearing one of its API-key prefixes
// to IAM, which resolves it to a USER — and a user in a box running model output
// is the credential that opens /v1/kms/secrets. The spelling is what keeps this
// token off that path, so the spelling is asserted.
func TestAGrantIsNotAnApiKeyAndIsNeverSentToIAM(t *testing.T) {
	token, _ := mint(t, "acme", "run-1", time.Minute)

	for _, p := range cloud.APIKeyPrefixes {
		if strings.HasPrefix(token, p) {
			t.Fatalf("a run grant is spelled like an API key (%q) — it would be sent to IAM "+
				"and resolved to a user, which is the widening this design removes", p)
		}
	}
	if cloud.IsPublishableKey(token) {
		t.Fatal("a run grant reads as a publishable key")
	}
	// It is also not a JWT, so nothing tries to verify it as one.
	if strings.Count(token, ".") == 2 {
		t.Fatal("a run grant reads as a JWT")
	}
}

// A GRANT DIES. The TTL is the backstop for a run that never says goodbye, and an
// expired grant is simply not a grant — it is not renewed, not warned about, and
// not usable.
func TestAnExpiredGrantIsNotAGrant(t *testing.T) {
	token, _, err := granted.issue(runGrant{org: "acme", run: "r"}, time.Nanosecond)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, ok := granted.lookup(token); ok {
		t.Fatal("an expired grant still resolves — a finished run can still spend")
	}
}

// A GRANT IS WITHDRAWN BY ITS OWN ORG AND BY NOBODY ELSE. Knowing a handle is not
// authority over it: a neighbour who learns one must not be able to end another
// tenant's run, which would be a cross-tenant denial of service.
func TestOnlyTheOwningOrgCanWithdrawAGrant(t *testing.T) {
	token, handle := mint(t, "acme", "run-1", time.Minute)

	granted.revoke("globex", handle) // a stranger holding the handle
	if _, ok := granted.lookup(token); !ok {
		t.Fatal("another org revoked acme's grant — knowing a handle became authority over it")
	}

	granted.revoke("acme", handle) // the owner
	if _, ok := granted.lookup(token); ok {
		t.Fatal("the owning org revoked its grant and it still resolves")
	}
}

// THE ORG COMES FROM THE CALLER, NEVER FROM THE REQUEST. plane.RunGrantIn has no
// org field at all, which is what makes it impossible for an app acting for one
// tenant to mint a grant that spends another's balance. A caller with no org gets
// nothing rather than a default.
func TestTheDoorRefusesACallerWithNoOrg(t *testing.T) {
	if _, err := planeRunGrant(t.Context(), &plane.RunGrantIn{Run: "r1"}); err == nil {
		t.Fatal("a caller with no org was granted the right to spend somebody's money")
	}
}

// A grant is always FOR something. A run that cannot be named cannot be attributed,
// and an unattributed debit is one the org cannot read back.
func TestTheDoorRefusesAGrantForNoRun(t *testing.T) {
	ctx := cloud.For(t.Context(), "acme")
	if _, err := planeRunGrant(ctx, &plane.RunGrantIn{}); err == nil {
		t.Fatal("a grant was minted for no run — its spend would be unattributable")
	}
}

// The door mints for the caller's org, and the grant it returns resolves to
// exactly that org — the two halves of the contract, checked together so a change
// to either is caught.
func TestTheDoorMintsForTheCallersOwnOrg(t *testing.T) {
	ctx := cloud.For(t.Context(), "acme")
	out, err := planeRunGrant(ctx, &plane.RunGrantIn{Run: "run-1"})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	t.Cleanup(func() { granted.revoke("acme", out.Handle) })

	gr, ok := granted.lookup(out.Token)
	if !ok {
		t.Fatal("the door returned a token that does not resolve")
	}
	if gr.org != "acme" || gr.run != "run-1" {
		t.Fatalf("minted %+v, want org acme run run-1", gr)
	}
	if out.ExpiresAt <= time.Now().Unix() {
		t.Fatal("the grant is already expired")
	}
	// The handle is the token's digest and not the token, so carrying it back never
	// means presenting the secret twice.
	if out.Handle == out.Token || strings.Contains(out.Handle, out.Token) {
		t.Fatal("the handle carries the secret")
	}
}
