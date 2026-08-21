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
// Spelled the way IAM spells one. `Type` is the claim the client_credentials
// grant stamps and a person's token never carries; a fixture without it is a
// token shape no issuer mints, and a reader validated against it proves nothing.
func machineClaims(app, owner string, exp time.Time) idClaims {
	c := idClaims{Claims: authz.Claims{Owner: owner, Organization: owner, Azp: app, Type: authz.Program}}
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

// appPrincipal is the same recognition, narrowed twice, and each narrowing is the
// predicate standing on its own rather than leaning on a caller's ordering. homeOrg
// checks subjectOrg before it asks this, so within homeOrg the first clause is
// invisible; the boundary asks it directly, and there it is the whole rule.
func TestAppPrincipalNarrowings(t *testing.T) {
	exp := time.Now().Add(time.Hour)

	t.Run("an API key is a person's credential, never an application", func(t *testing.T) {
		c := machineClaims("hanzo-kms", "hanzo", exp)
		c.subjectOrg = "hanzo" // resolved from an sk- key's own IAM user row
		if appPrincipal(&c) {
			t.Fatal("a key read as an application — every member of the org inherits the machine identity's reach")
		}
	})

	// The two kinds are exclusive from BOTH sides, and a membership set does not
	// move a token between them. IAM signs the kind; carrying memberships as well
	// used to be enough to read a program as a person, and a person in the
	// reserved org is an operator.
	t.Run("a program in the reserved org is never an operator", func(t *testing.T) {
		c := machineClaims("hanzo-kms", "admin", exp)
		c.Orgs = []authz.Membership{{Org: authz.AdminOrg, Role: authz.Admin}}
		if !appPrincipal(&c) {
			t.Fatal("IAM signed this as a program and the membership set outvoted the claim")
		}
		if platformSudo(&c) {
			t.Fatal("one principal was both platform sudo and an application — the two kinds must never coincide")
		}
	})

	t.Run("a person in the reserved org is the operator", func(t *testing.T) {
		c := machineClaims("hanzo-console", "admin", exp)
		c.Type = "" // a person's token carries no kind
		c.Subject = "admin/z"
		c.Orgs = []authz.Membership{{Org: authz.AdminOrg, Role: authz.Admin}}
		if !platformSudo(&c) {
			t.Fatal("precondition: a member of the reserved org is the operator this contrasts against")
		}
		if appPrincipal(&c) {
			t.Fatal("a person read as an application")
		}
	})
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

// THE SHAPE ALONE IS NOT THE PROOF.
//
// A person's token can wear every mark this file used to read as a machine: a
// subject that looks like "<org>/<app>", one audience equal to azp, and no
// membership set — which is what an unresolved membership lookup leaves behind.
// Reading that as an application handed it the org off `owner` and a build
// door's whole registry namespace.
//
// IAM signs the kind, so the answer no longer depends on a shape anyone can
// arrange. This pins the exact claims that used to pass.
func TestATokenShapedLikeAMachineIsNotOneWithoutTheClaim(t *testing.T) {
	c := machineClaims("hanzo-console", "hanzo", time.Now().Add(time.Hour))
	c.Subject = "acme/hanzo-console"
	c.Orgs = nil
	c.Type = "" // the one difference: IAM never signed this as a program

	if isClientCredentialsPrincipal(&c) != true {
		t.Fatal("precondition: this is exactly the shape the old reading accepted")
	}
	if appPrincipal(&c) {
		t.Fatal("a token IAM never called a program was admitted as one on shape alone")
	}
	if got := c.homeOrg(); got != "" {
		t.Fatalf("homeOrg=%q, want empty — nothing may be resolved for it", got)
	}
}
