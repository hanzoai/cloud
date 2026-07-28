package cloud

import (
	"context"
	"strings"
	"testing"
)

// The tenant boundary ACROSS the internal plane.
//
// An aggregator (admin) reads another app's per-org data by asking that app over
// this socket, so the question "whose data is this?" is now answered on the wire
// rather than by a Go call in one address space. That makes it exactly the shape
// of bug this fleet has already shipped once: an org-scoped list that returned
// 200 with another tenant's rows because a scope filter was quietly dropped.
//
// So the boundary is asserted here, adversarially, against the REAL transport —
// a real socket, real envelopes, real capability packing — not a stub. The rule
// under test is the one every org-scoped method must follow:
//
//	the org comes from the CAPABILITY, never from the payload.
//
// tenantBooks stands in for any per-org store: a map that must only ever be read at
// the key the capability names.
var tenantBooks = map[string]int64{"acme": 5000, "initech": 99}

// exposeScopedRead publishes a method with the canonical scoping rule, so what
// the tests below attack is the real Expose/Call path.
func exposeScopedRead(t *testing.T) {
	t.Helper()
	Expose("test.balance", func(_ context.Context, who Ident, req []byte) ([]byte, error) {
		// Fail CLOSED on an absent tenant. Answering with a default, the first key,
		// or zero would each be a different way of inventing an answer nobody is
		// authorized to receive.
		if who.Org == "" {
			return nil, Fault(403, "no org on the capability")
		}
		// The payload names a SUBJECT, never an org — the codec cannot express one.
		if _, _, err := BalanceReq(req); err != nil {
			return nil, Fault(400, "bad request")
		}
		return PutI64(tenantBooks[who.Org]), nil
	})
}

// tenantCall drives one real round trip over the socket for app "bank".
func tenantCall(t *testing.T, p *Peer, payload []byte) (int64, error) {
	t.Helper()
	out, err := p.Call(context.Background(), "test.balance", payload)
	if err != nil {
		return 0, err
	}
	return I64(out)
}

// TestTenantBoundary_CapabilityScopesTheAnswer is the adversarial case: a caller
// holding acme's capability must NEVER be able to read initech, no matter what it
// puts in the payload or the subject.
func TestTenantBoundary_CapabilityScopesTheAnswer(t *testing.T) {
	t.Setenv(runDirEnv, t.TempDir())
	exposeScopedRead(t)
	c, err := Listen("bank", nil)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	// The honest read: acme's capability yields acme's books.
	got, err := tenantCall(t, Dial("bank").For("acme"), PutBalanceReq("acme", "usd"))
	if err != nil {
		t.Fatalf("acme read: %v", err)
	}
	if got != tenantBooks["acme"] {
		t.Fatalf("acme read %d, want %d", got, tenantBooks["acme"])
	}

	// THE ATTACK. acme's capability, but the payload's subject names the other
	// tenant — the "just pass the org in the body" mistake. The subject is a wallet
	// WITHIN the capability's org, so naming initech there must not cross the
	// boundary: the answer must still be acme's.
	got, err = tenantCall(t, Dial("bank").For("acme"), PutBalanceReq("initech", "usd"))
	if err != nil {
		t.Fatalf("attack call failed outright (expected acme's answer): %v", err)
	}
	if got == tenantBooks["initech"] {
		t.Fatalf("CROSS-TENANT READ: a payload naming initech returned initech's %d "+
			"while holding acme's capability", got)
	}
	if got != tenantBooks["acme"] {
		t.Fatalf("read %d, want acme's %d", got, tenantBooks["acme"])
	}
}

// TestTenantBoundary_NoCapabilityIsRefused pins the fail-closed direction. An
// anonymous call must be REFUSED, not served a default tenant — the difference
// between "you may not ask" and handing over whichever books were first.
func TestTenantBoundary_NoCapabilityIsRefused(t *testing.T) {
	t.Setenv(runDirEnv, t.TempDir())
	exposeScopedRead(t)
	c, err := Listen("bank", nil)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	// Dial with no As() and no For(): the zero Ident, which packs to no capability
	// at all rather than an empty claim.
	_, err = tenantCall(t, Dial("bank"), PutBalanceReq("acme", "usd"))
	if err == nil {
		t.Fatal("an org-less call was ANSWERED; it must be refused")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("refusal should carry the method's 403, got: %v", err)
	}
}

// TestBalanceReq_CannotCarryAnOrg is the structural half of the guarantee. The
// two tests above show the handler ignores a hostile payload; this shows the
// payload cannot even ASK. A field that does not exist cannot be trusted by a
// future handler that forgets the rule, which is why the org was left out of the
// codec rather than validated inside it.
func TestBalanceReq_CannotCarryAnOrg(t *testing.T) {
	subject, currency, err := BalanceReq(PutBalanceReq("wallet-1", "usd"))
	if err != nil {
		t.Fatalf("BalanceReq: %v", err)
	}
	if subject != "wallet-1" || currency != "usd" {
		t.Fatalf("round trip lost data: %q/%q", subject, currency)
	}
	// The decoder returns exactly two values plus an error. If an org field is ever
	// added to this codec, this test stops compiling — which is the point: adding a
	// way to name a tenant in the payload must be a deliberate, breaking act.
	var _ func([]byte) (string, string, error) = BalanceReq
}
