package cloud

import (
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"
)

// TestValidate_AudienceIsNotAGate proves the IAM-native trust model: a token signed
// by a trusted issuer validates REGARDLESS of its `aud` (the minting app's client_id).
// Cloud keeps no per-app audience allowlist mirroring IAM's registry — so a brand-new
// first-party app works with zero cloud change, and the specific app tokens the old
// per-app tests pinned (admin-guard, world, team, commerce) are accepted by the SAME
// rule as everything else. Trust is signature + issuer + expiry; org scope is the
// owner claim, enforced downstream. Replaces the three audience_*_test.go files that
// asserted a static allowlist which no longer exists.
func TestValidate_AudienceIsNotAGate(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, 0)
	future := time.Now().Add(time.Hour)

	// Every audience — the apps the deleted per-app tests pinned AND a never-registered
	// one — validates identically, because aud is not an access gate.
	for _, aud := range []string{
		"hanzo-admin-guard", // admin.hanzo.ai forward-auth cockpit
		"hanzo-world",       // world.hanzo.ai analyst tokens
		"hanzo-team",        // hanzo.team wallet page
		"hanzo-commerce",    // commerce.hanzo.ai admin AI assistant
		"a-brand-new-first-party-app-never-listed-anywhere",
	} {
		tok := signWith(t, key, tokenClaims(aud, "acme", "", false, future))
		id, err := v.validate(tok)
		if err != nil {
			t.Fatalf("aud=%q from a trusted issuer must validate (no allowlist), got %v", aud, err)
		}
		if id.Owner != "acme" {
			t.Errorf("aud=%q: owner must be carried through, got %q", aud, id.Owner)
		}
	}

	// Expiry is STILL enforced (dropping the aud gate must not disable time checks):
	// a token expired beyond the 2m leeway is rejected whatever its aud.
	expired := signWith(t, key, tokenClaims("hanzo-commerce", "acme", "", false, time.Now().Add(-time.Hour)))
	if _, err := v.validate(expired); err == nil {
		t.Error("expired token must be REJECTED even though audience is no longer gated")
	}
}
