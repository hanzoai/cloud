package cloud

// The per-org PaaS-KMS sync identity authenticates as its own IAM application
// "<owner>-platform-kms" (client_credentials), so its token carries owner=<org> and
// aud=<owner>-platform-kms. Validation no longer gates on the audience at all (trust
// is signature + issuer + expiry), so a machine token clears validate() like any
// other. The owner-bound machine aud survives only to IDENTIFY such a principal
// as a NAME only; the signed kind is what denies it SuperAdmin even in the admin
// org — a client_credentials machine identity must never wield platform-admin. These
// are white-box unit tests of that identification; the end-to-end proof through
// SanitizeIdentity + the real guard lives in clients/kms (v6_aud_e2e_test.go). Reuses
// the jwksServer/signWith/tokenClaims helpers from middleware_identity_test.go.

import (
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/hanzoai/authz"
)

func TestIdentityValidator_KMSMachinePrincipal(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, 0)
	future := time.Now().Add(time.Hour)

	// A program, spelled the way IAM spells one.
	program := func(aud, owner string, isAdmin bool, exp time.Time) idClaims {
		c := tokenClaims(aud, owner, "", isAdmin, exp)
		c.Type = authz.Program
		c.Orgs = nil
		return c
	}

	t.Run("a machine token resolves the org it cannot choose", func(t *testing.T) {
		c, err := v.validate(signWith(t, key, program("maxpower-platform-kms", "maxpower", false, future)))
		if err != nil {
			t.Fatalf("machine token rejected: %v", err)
		}
		if !appPrincipal(c) {
			t.Fatal("IAM signed this as a program and cloud did not read it as one")
		}
		if got := c.homeOrg(); got != "maxpower" {
			t.Fatalf("homeOrg=%q, want maxpower", got)
		}
	})

	// THE PROPERTY THIS FILE EXISTS FOR. It used to be enforced by recognising an
	// owner-bound audience and subtracting sudo afterwards. The audience is no
	// longer read: a program is one because IAM said so, and Sudo refuses every
	// machine, so the denial is the same fact rather than a second one.
	t.Run("a machine in the reserved org holds no platform authority", func(t *testing.T) {
		c, err := v.validate(signWith(t, key, program("admin-platform-kms", "admin", true, future)))
		if err != nil {
			t.Fatalf("admin machine token rejected: %v", err)
		}
		if platformSudo(c) {
			t.Fatal("a client_credentials identity in the reserved org wielded platform admin")
		}
		if orgAdmin(c, "admin") {
			t.Fatal("a machine held an org's self-service surface")
		}
		if !appPrincipal(c) {
			t.Fatal("it is still a program, and still scoped to its own org")
		}
	})

	// The audience is not the proof and no longer needs to be bound: a foreign or
	// absent one changes nothing, because the kind and the org are both claims.
	t.Run("the audience decides nothing", func(t *testing.T) {
		for _, aud := range []string{"acme-platform-kms", "hanzo-console", "-platform-kms"} {
			c, err := v.validate(signWith(t, key, program(aud, "maxpower", false, future)))
			if err != nil {
				t.Fatalf("aud %q: token rejected: %v", aud, err)
			}
			if got := c.homeOrg(); got != "maxpower" {
				t.Fatalf("aud %q: homeOrg=%q, want maxpower — the org is `owner`, not the audience", aud, got)
			}
			if platformSudo(c) {
				t.Fatalf("aud %q: a machine gained platform authority", aud)
			}
		}
	})

	t.Run("a person is not admitted as a program however its token is addressed", func(t *testing.T) {
		c, err := v.validate(signWith(t, key, tokenClaims("maxpower-platform-kms", "maxpower", "z@example.test", false, future)))
		if err != nil {
			t.Fatalf("token rejected: %v", err)
		}
		if appPrincipal(c) {
			t.Fatal("a token IAM never called a program was admitted as one")
		}
	})

	t.Run("machine token expiry still enforced", func(t *testing.T) {
		if _, err := v.validate(signWith(t, key, program("maxpower-platform-kms", "maxpower", false, time.Now().Add(-time.Hour)))); err == nil {
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
