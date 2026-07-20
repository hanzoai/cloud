package cloud

// The `orgs` membership-set claim must survive verification into
// VerifiedIdentity.Orgs — that is the value clients/team copies into a session so
// a user's workspaces union across every org. These drive a real RSA-signed token
// through the SAME validator the identity boundary uses.

import (
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/hanzoai/iam"
)

// TestVerifiedIdentityCarriesOrgs proves a signed `orgs` claim is verified and
// copied verbatim (order preserved, home first) into VerifiedIdentity.Orgs.
func TestVerifiedIdentityCarriesOrgs(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := &TokenValidator{v: newIdentityValidator(testIssuer, jwks.URL, []string{"hanzo-team"}, 0)}

	claims := tokenClaims("hanzo-team", "maxpower", "dave@example.com", false, time.Now().Add(time.Hour))
	claims.Orgs = []iam.OrgRef{
		{Org: "maxpower", Role: "admin"},
		{Org: "acme", Role: "member"},
	}
	id, err := v.Validate(signWith(t, key, claims))
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if id.Owner != "maxpower" {
		t.Fatalf("owner = %q, want maxpower", id.Owner)
	}
	if len(id.Orgs) != 2 ||
		id.Orgs[0] != (iam.OrgRef{Org: "maxpower", Role: "admin"}) ||
		id.Orgs[1] != (iam.OrgRef{Org: "acme", Role: "member"}) {
		t.Fatalf("orgs = %+v, want [maxpower/admin acme/member]", id.Orgs)
	}
}

// TestVerifiedIdentityLegacyNoOrgs proves a token minted before the claim shipped
// (no `orgs`) verifies with an empty set — the caller falls back to Owner.
func TestVerifiedIdentityLegacyNoOrgs(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := &TokenValidator{v: newIdentityValidator(testIssuer, jwks.URL, []string{"hanzo-team"}, 0)}

	claims := tokenClaims("hanzo-team", "acme", "ada@example.com", false, time.Now().Add(time.Hour))
	id, err := v.Validate(signWith(t, key, claims))
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(id.Orgs) != 0 {
		t.Fatalf("legacy token orgs = %+v, want empty", id.Orgs)
	}
	if id.Owner != "acme" {
		t.Fatalf("owner = %q, want acme", id.Owner)
	}
}
