package cloud

// Platform authority is MEMBERSHIP of the reserved admin org, at any position in
// the signed `orgs` set — not the home org, and not the first element.
//
// This file exists because the distinction is invisible to every other test here.
// The gate read claims.homeOrg() == adminOrg, i.e. Claims.Orgs[0].Org, and IAM's
// MemberOrgRefs ALWAYS writes the user's own org at index 0 and appends granted
// memberships after it (iam internal/org/membership.go). So the positional read
// was equivalent to the set read for exactly the population the old tests built —
// a user whose row already lives in the admin org — and wrong for the population
// that actually operates the estate: an operator anchored in a brand org who was
// GRANTED admin-org membership.
//
// That grant is not incidental. IAM guards it as platform authority on the write
// side: memberships.mayGrant refuses to create a membership into a reserved org
// unless the caller is already a SuperAdmin, because it "seeds admin-org
// (SuperAdmin) tenancy". A grant the issuer treats as sudo must not be inert at
// the resource server. Production is the proof: z@hanzo.ai carries
// orgs:[{hanzo,admin},{admin,admin},{lux,admin},{pars,admin},{zoo,admin}] and was
// refused every superAdminOf surface — the CD plane, settings, version — because
// `admin` sits at index 1.

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/hanzoai/authz"
)

// TestSudoIsMembershipNotPosition drives real JWKS-validated tokens
// through SanitizeIdentity and reads the X-User-IsAdmin a downstream gate sees.
//
// The load-bearing case is "operator anchored in a brand org": it FAILS against a
// homeOrg/Orgs[0] predicate and passes against a set-membership one. Every other
// case pins that widening nothing else: a member of no reserved org gets nothing
// no matter how many orgs they hold or what role they hold in them.
func TestSudoIsMembershipNotPosition(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, 0)
	future := time.Now().Add(time.Hour)

	for _, tc := range []struct {
		name string
		orgs []authz.Membership
		// wantAdmin is the platform-sudo grant; wantOrg is the org the request acts
		// in when no org is selected — it must stay the ANCHOR, never the reserved org.
		wantAdmin bool
		wantOrg   string
	}{
		{
			// THE REGRESSION. Anchored in a brand org, granted the reserved org.
			// Orgs[0] is "hanzo", so a positional predicate refuses this operator.
			name:      "operator anchored in a brand org, granted admin membership",
			orgs:      []authz.Membership{{Org: "hanzo", Role: authz.Admin}, {Org: "admin", Role: authz.Admin}},
			wantAdmin: true,
			wantOrg:   "hanzo",
		},
		{
			// z@hanzo.ai's real production membership set, verbatim. `admin` at index 1.
			name: "the live operator set",
			orgs: []authz.Membership{
				{Org: "hanzo", Role: authz.Admin}, {Org: "admin", Role: authz.Admin},
				{Org: "lux", Role: authz.Admin}, {Org: "pars", Role: authz.Admin},
				{Org: "zoo", Role: authz.Admin},
			},
			wantAdmin: true,
			wantOrg:   "hanzo",
		},
		{
			// Position is irrelevant, not merely "index 1 also works".
			name:      "admin membership last in a long set",
			orgs:      []authz.Membership{{Org: "a", Role: authz.Member}, {Org: "b", Role: authz.Member}, {Org: "admin", Role: authz.Member}},
			wantAdmin: true,
			wantOrg:   "a",
		},
		{
			// Unchanged: a user whose ROW lives in the admin org. The population the
			// old predicate did serve must keep working.
			name:      "operator whose home org IS the admin org",
			orgs:      []authz.Membership{{Org: "admin", Role: authz.Admin}},
			wantAdmin: true,
			wantOrg:   "admin",
		},
		{
			// Membership of the reserved org is the fact; the ROLE within it is not a
			// second term. An admin-org user whose row carries isAdmin=false gets
			// {admin, member} from IAM's HomeRole and is a SuperAdmin today —
			// TestMasqueradeSpendsOwnBooks pins exactly that principal. Adding a role
			// term here would revoke sudo from them: a lockout, not a hardening.
			name:      "plain member of the reserved org is still an operator",
			orgs:      []authz.Membership{{Org: "hanzo", Role: authz.Admin}, {Org: "admin", Role: authz.Member}},
			wantAdmin: true,
			wantOrg:   "hanzo",
		},
		{
			// THE WIDENING TEST. Admin of every brand org in the estate, member of no
			// reserved org: still not platform authority.
			name:      "admin of many orgs, none of them reserved",
			orgs:      []authz.Membership{{Org: "hanzo", Role: authz.Admin}, {Org: "lux", Role: authz.Owner}, {Org: "zoo", Role: authz.Admin}},
			wantAdmin: false,
			wantOrg:   "hanzo",
		},
		{
			// Verbatim comparison, like every other org compare at this boundary. A
			// fold would make an org someone can self-serve the reserved one.
			name:      "a look-alike reserved org is not the reserved org",
			orgs:      []authz.Membership{{Org: "hanzo", Role: authz.Admin}, {Org: "Admin", Role: authz.Admin}},
			wantAdmin: false,
			wantOrg:   "hanzo",
		},
		{
			name:      "no memberships at all is a machine, never an operator",
			orgs:      nil,
			wantAdmin: false,
			wantOrg:   "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := tokenClaims("hanzo-cli", "some-app-org", "op@hanzo.ai", false, future)
			claims.Orgs = tc.orgs

			app, seen := newIdentityApp(t, v)
			probe(t, app, func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+signWith(t, key, claims))
				// Forged on top of the real token, so the ingress strip is exercised in
				// the same pass: a client copy must never be what grants this.
				r.Header.Set("X-User-IsAdmin", "true")
			})

			if seen.admin != tc.wantAdmin {
				t.Errorf("platform sudo = %v; want %v (orgs=%v)", seen.admin, tc.wantAdmin, tc.orgs)
			}
			if seen.org != tc.wantOrg {
				t.Errorf("effective org = %q; want %q — sudo must not move the anchor", seen.org, tc.wantOrg)
			}
		})
	}
}

// TestSudoDoesNotRideTheAppOrg pins that the grant reads the SUBJECT's
// membership set and never the `owner` claim, which IAM stamps with the
// APPLICATION's org. Signing in through an admin-org-owned app must not confer
// platform authority on a user who holds no reserved membership — otherwise the
// authority is a property of whichever client you logged in through.
func TestSudoDoesNotRideTheAppOrg(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, 0)

	// owner = "admin" (the APP's org), but the subject belongs only to "acme".
	claims := tokenClaims("admin-console", "admin", "user@acme.test", true, time.Now().Add(time.Hour))
	claims.Orgs = []authz.Membership{{Org: "acme", Role: authz.Admin}}

	app, seen := newIdentityApp(t, v)
	probe(t, app, bearer(signWith(t, key, claims)))

	if seen.admin {
		t.Error("the app's org conferred platform sudo on a user who holds no reserved membership")
	}
	if seen.org != "acme" {
		t.Errorf("effective org = %q; want %q (the SUBJECT's org, not the app's)", seen.org, "acme")
	}
}

// TestOperatorTokenIsNotSpecialToAnyClient pins that platform authority is a
// property of the PRINCIPAL, not of the client that minted the token. hanzo-cli is
// a PUBLIC client — it ships to users' machines and holds no secret by design — so
// the same operator arriving through it must get the same authority and no more.
// The audience is deliberately not a gate here (auth_identity.go: a valid
// signature from a trusted issuer already proves IAM minted it for one of its own
// registered apps), so this pins that a public client neither gains nor loses
// authority relative to a confidential one.
func TestOperatorTokenIsNotSpecialToAnyClient(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, 0)
	operator := []authz.Membership{{Org: "hanzo", Role: authz.Admin}, {Org: "admin", Role: authz.Admin}}
	tenant := []authz.Membership{{Org: "acme", Role: authz.Admin}}

	for _, aud := range []string{"hanzo-cli", "hanzo-console", "hanzo-studio"} {
		t.Run("operator via "+aud, func(t *testing.T) {
			claims := tokenClaims(aud, "hanzo", "z@hanzo.ai", false, time.Now().Add(time.Hour))
			claims.Orgs = operator
			app, seen := newIdentityApp(t, v)
			probe(t, app, bearer(signWith(t, key, claims)))
			if !seen.admin {
				t.Errorf("operator was refused platform sudo through client %q", aud)
			}
		})
		t.Run("tenant via "+aud, func(t *testing.T) {
			claims := tokenClaims(aud, "acme", "user@acme.test", true, time.Now().Add(time.Hour))
			claims.Orgs = tenant
			app, seen := newIdentityApp(t, v)
			probe(t, app, bearer(signWith(t, key, claims)))
			if seen.admin {
				t.Errorf("a tenant was granted platform sudo through client %q", aud)
			}
			if !seen.orgAdmin {
				t.Errorf("an org admin lost their OWN org's admin scope through client %q", aud)
			}
		})
	}
}

var _ = jwt.ClaimStrings{}
