package cloud

// The per-org PaaS-KMS sync identity authenticates as its own IAM application
// "<owner>-platform-kms" (client_credentials), so its token carries owner=<org> and
// aud=<owner>-platform-kms. Validation no longer gates on the audience at all (trust
// is signature + issuer + expiry), so a machine token clears validate() like any
// other. The owner-bound machine aud survives only to IDENTIFY such a principal
// (isKMSMachinePrincipal) so SanitizeIdentity can DENY it SuperAdmin even in the admin
// org — a client_credentials machine identity must never wield platform-admin. These
// are white-box unit tests of that identification; the end-to-end proof through
// SanitizeIdentity + the real guard lives in clients/kms (v6_aud_e2e_test.go). Reuses
// the jwksServer/signWith/tokenClaims helpers from middleware_identity_test.go.

import (
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"
)

func TestIdentityValidator_KMSMachinePrincipal(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, 0)
	future := time.Now().Add(time.Hour)

	t.Run("own-org machine token validates and is recognised as a machine principal", func(t *testing.T) {
		c, err := v.validate(signWith(t, key, tokenClaims("maxpower-platform-kms", "maxpower", "", false, future)))
		if err != nil {
			t.Fatalf("machine token rejected: %v", err)
		}
		if c.Owner != "maxpower" {
			t.Fatalf("owner=%q, want maxpower", c.Owner)
		}
		if !isKMSMachinePrincipal(c) {
			t.Fatal("aud==<owner>-platform-kms must be recognised as a machine principal")
		}
	})

	t.Run("admin-org machine token is recognised so SuperAdmin is denied", func(t *testing.T) {
		c, err := v.validate(signWith(t, key, tokenClaims("admin-platform-kms", "admin", "", true, future)))
		if err != nil {
			t.Fatalf("admin machine token rejected: %v", err)
		}
		if !isKMSMachinePrincipal(c) {
			t.Fatal("admin-org machine token must be recognised (SanitizeIdentity denies it SuperAdmin)")
		}
	})

	t.Run("machine aud bound to a DIFFERENT org is not this owner's machine principal", func(t *testing.T) {
		// owner=maxpower, aud=acme-platform-kms: the machine-principal match is bound to
		// the token's OWN owner (maxpower-platform-kms), not a "*-platform-kms" wildcard.
		// It validates (aud is not gated) and is owner-scoped to maxpower downstream.
		c, err := v.validate(signWith(t, key, tokenClaims("acme-platform-kms", "maxpower", "", false, future)))
		if err != nil {
			t.Fatalf("token rejected: %v", err)
		}
		if isKMSMachinePrincipal(c) {
			t.Fatal("a cross-org machine aud must not count as this owner's machine principal")
		}
	})

	t.Run("ordinary app token is not a machine principal", func(t *testing.T) {
		c, err := v.validate(signWith(t, key, tokenClaims("hanzo-console", "maxpower", "", false, future)))
		if err != nil {
			t.Fatalf("token rejected: %v", err)
		}
		if isKMSMachinePrincipal(c) {
			t.Fatal("an ordinary app token is not a machine principal")
		}
	})

	t.Run("empty-owner token is never a machine principal (fail closed)", func(t *testing.T) {
		c, err := v.validate(signWith(t, key, tokenClaims("-platform-kms", "", "", false, future)))
		if err != nil {
			t.Fatalf("token rejected: %v", err)
		}
		if isKMSMachinePrincipal(c) {
			t.Fatal(`empty-owner token must never be a machine principal (kmsMachineAudience("")=="")`)
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
