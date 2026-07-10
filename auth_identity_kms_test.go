package cloud

// V6 (the activation blocker) — the identity validator must accept a per-org
// PaaS-KMS sync machine token: a client_credentials JWT whose aud is the org's
// own IAM application clientId "<owner>-platform-kms" (a per-org value, NEVER in
// CLOUD_JWT_AUDIENCES) — but ONLY when that audience is bound to the token's OWN
// owner claim. Before the fix the machine token failed the audience check,
// SanitizeIdentity resolved anonymous, and the /v1/kms guard 403'd it, so the sync
// silently stayed pending. These are white-box unit tests of validate() itself;
// the end-to-end proof through SanitizeIdentity + the real guard lives in
// clients/kms (v6_aud_e2e_test.go). Reuses the jwksServer/signWith/tokenClaims
// helpers from middleware_identity_test.go (same package).

import (
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"
)

func TestIdentityValidator_KMSMachineAudience(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	// The static allowlist deliberately contains NO *-platform-kms audience, so any
	// acceptance below can come ONLY from the owner-bound machine-aud rule, not the
	// allowlist — this is what makes it a fix and not a config workaround.
	v := newIdentityValidator(testIssuer, jwks.URL, []string{"hanzo-console"}, 0)
	future := time.Now().Add(time.Hour)

	t.Run("machine token for its own org is accepted", func(t *testing.T) {
		c, err := v.validate(signWith(t, key, tokenClaims("maxpower-platform-kms", "maxpower", "", false, future)))
		if err != nil {
			t.Fatalf("machine token rejected: %v", err)
		}
		if c.Owner != "maxpower" {
			t.Fatalf("owner=%q, want maxpower", c.Owner)
		}
	})

	t.Run("machine aud for a DIFFERENT org is rejected (owner-bound)", func(t *testing.T) {
		// owner=maxpower but aud=acme-platform-kms: the accepted machine aud is bound
		// to the token's OWN owner (maxpower-platform-kms), so this must fail — it is
		// not a blanket "*-platform-kms" wildcard.
		if _, err := v.validate(signWith(t, key, tokenClaims("acme-platform-kms", "maxpower", "", false, future))); err == nil {
			t.Fatal("cross-org machine audience must be rejected (owner-bound)")
		}
	})

	t.Run("arbitrary audience still rejected (fix is scoped, not a disable)", func(t *testing.T) {
		if _, err := v.validate(signWith(t, key, tokenClaims("some-random-app", "maxpower", "", false, future))); err == nil {
			t.Fatal("an arbitrary audience must still be rejected")
		}
	})

	t.Run("machine aud with empty owner is rejected (fail closed)", func(t *testing.T) {
		// aud="-platform-kms" with owner="": kmsMachineAudience("")=="" so no machine
		// audience is granted and the bare suffix is not in the allowlist.
		if _, err := v.validate(signWith(t, key, tokenClaims("-platform-kms", "", "", false, future))); err == nil {
			t.Fatal("machine aud with empty owner must be rejected")
		}
	})

	t.Run("normal static-allowlist token still accepted (regression)", func(t *testing.T) {
		if _, err := v.validate(signWith(t, key, tokenClaims("hanzo-console", "maxpower", "", false, future))); err != nil {
			t.Fatalf("static-allowlist token rejected: %v", err)
		}
	})

	t.Run("machine token expiry still enforced", func(t *testing.T) {
		if _, err := v.validate(signWith(t, key, tokenClaims("maxpower-platform-kms", "maxpower", "", false, time.Now().Add(-time.Hour)))); err == nil {
			t.Fatal("expired machine token must be rejected")
		}
	})
}

// kmsMachineAudience is a pure helper; lock its contract directly.
func TestKMSMachineAudience(t *testing.T) {
	if got := kmsMachineAudience("maxpower"); got != "maxpower-platform-kms" {
		t.Fatalf("kmsMachineAudience(maxpower)=%q, want maxpower-platform-kms", got)
	}
	if got := kmsMachineAudience(""); got != "" {
		t.Fatalf("kmsMachineAudience(\"\")=%q, want \"\" (no machine aud for an org-less token)", got)
	}
}
