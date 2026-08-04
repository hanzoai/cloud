package cloud

// The client_credentials principal: an application authenticating AS ITSELF.
//
// homeOrg resolves such a token to its `owner` — the application's own organization —
// because a machine cannot choose an org the way a human choosing an app can. That is
// the same reasoning the KMS branch already rests on; these tests exist because the
// recognition is by SHAPE rather than by a derivable audience, so every part of the
// shape needs a test that fails when it is loosened.
//
// The dangerous direction is second-to-last: a HUMAN with no `orgs` must still resolve
// nothing. That is the estate rule, it is what makes an org-less request fail closed
// everywhere, and widening machine recognition must never reach it.

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hanzoai/authz"
)

// machineClaims builds a client_credentials token as IAM mints one: the client is the
// subject ("<org>/<app>"), the sole audience, and the authorized party — and there is
// no membership set, which is correct for a machine rather than a degraded token.
func machineClaims(app, owner string, exp time.Time) idClaims {
	c := idClaims{Claims: authz.Claims{Owner: owner, Organization: owner, Azp: app}}
	c.Issuer = testIssuer
	c.Subject = "admin/" + app
	c.Audience = jwt.ClaimStrings{app}
	c.ExpiresAt = jwt.NewNumericDate(exp)
	c.IssuedAt = jwt.NewNumericDate(time.Now())
	return c
}

func TestClientCredentialsPrincipalResolvesItsOwnerOrg(t *testing.T) {
	c := machineClaims("hanzo-studio", "hanzo", time.Now().Add(time.Hour))

	if !isClientCredentialsPrincipal(&c) {
		t.Fatal("a token whose client is its own subject and sole audience is a machine principal")
	}
	if got := c.homeOrg(); got != "hanzo" {
		t.Fatalf("homeOrg=%q, want hanzo — a machine's org is its owner, which it cannot choose", got)
	}
}

// The failure this fixes, stated as the thing that used to happen.
//
// studio's token carries owner=hanzo and no `orgs`, so Home() returned "" and
// SanitizeIdentity minted X-User-Id with no X-Org-Id. Every org-scoped gate refused
// it; the one that mattered was the durable queue, which answered 403 "identity
// required" — so no render could be enqueued at all, and thirteen jobs sat queued for
// up to nineteen hours while both GPUs polled an empty namespace and reported healthy.
func TestMachineTokenWithNoOrgsIsNotOrgless(t *testing.T) {
	c := machineClaims("hanzo-studio", "hanzo", time.Now().Add(time.Hour))
	if len(c.Orgs) != 0 {
		t.Fatal("precondition: a machine token carries no membership set")
	}
	if c.Claims.Home() != "" {
		t.Fatal("precondition: the estate rule alone resolves nothing here")
	}
	if c.homeOrg() == "" {
		t.Fatal("an org-less machine principal reaches every org gate as anonymous and is refused")
	}
}

// A HUMAN with no membership set still resolves nothing. This is the rule the machine
// branches must never reach: a human's org follows their token, and a token that
// proves no membership proves no org.
func TestHumanWithoutOrgsStillFailsClosed(t *testing.T) {
	c := idClaims{Claims: authz.Claims{Owner: "hanzo", Azp: "hanzo-console"}}
	c.Issuer = testIssuer
	c.Subject = "u-alice" // a USER, not "<org>/<app>"
	c.Audience = jwt.ClaimStrings{"hanzo-console"}
	c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Hour))

	if isClientCredentialsPrincipal(&c) {
		t.Fatal("a human subject must never be read as a machine, whatever else matches")
	}
	if got := c.homeOrg(); got != "" {
		t.Fatalf("homeOrg=%q, want empty — falling back to owner is the mis-attribution this prevents", got)
	}
}

// Each half of the shape is load-bearing. A token that matches only part of it is a
// human token that happens to resemble a machine, and must not be admitted.
func TestPartialMachineShapeIsRefused(t *testing.T) {
	exp := time.Now().Add(time.Hour)

	t.Run("azp is not the audience — the token was issued for a DIFFERENT client", func(t *testing.T) {
		c := machineClaims("hanzo-studio", "hanzo", exp)
		c.Audience = jwt.ClaimStrings{"some-other-app"}
		if isClientCredentialsPrincipal(&c) {
			t.Fatal("a client holding a token minted for another audience is not that audience")
		}
	})

	t.Run("more than one audience — not a token an app got for itself", func(t *testing.T) {
		c := machineClaims("hanzo-studio", "hanzo", exp)
		c.Audience = jwt.ClaimStrings{"hanzo-studio", "hanzo-console"}
		if isClientCredentialsPrincipal(&c) {
			t.Fatal("a multi-audience token is not the self-issued shape")
		}
	})

	t.Run("subject does not name the client — a user signed in THROUGH the app", func(t *testing.T) {
		c := machineClaims("hanzo-studio", "hanzo", exp)
		c.Subject = "admin/somebody-else"
		if isClientCredentialsPrincipal(&c) {
			t.Fatal("the subject must BE the client; anything else is a human using it")
		}
	})

	t.Run("no owner — nothing to resolve, and no guess to make", func(t *testing.T) {
		c := machineClaims("hanzo-studio", "", exp)
		if isClientCredentialsPrincipal(&c) {
			t.Fatal("without an owner there is no org to return")
		}
	})

	t.Run("bare subject with no org segment", func(t *testing.T) {
		c := machineClaims("hanzo-studio", "hanzo", exp)
		c.Subject = "hanzo-studio"
		if isClientCredentialsPrincipal(&c) {
			t.Fatal("<org>/<app> is the shape; a bare name is not it")
		}
	})
}
