package cloud

// An org's agent identity (<org>-agent, client_credentials) must never wield
// platform sudo. That is the hole a previous run credential was reverted for: in
// the reserved `admin` org it satisfied a bare owner=="admin" compare and became
// SuperAdmin.
//
// The protection already exists and is STRUCTURAL rather than a list of names —
// isClientCredentialsPrincipal reads the token's own shape. This pins that the
// new identity is covered by it, so nobody adds a second mechanism later (I
// nearly did: a suffix allowlist beside a predicate that already answered).

import (
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hanzoai/authz"
)

// agentToken is what IAM mints for <org>-agent under client_credentials: the
// client obtains a token FOR ITSELF, so azp is the client, the sole audience is
// that same client, and the subject names it as "<org>/<app>".
func agentToken(org string) *idClaims {
	app := org + "-agent"
	return &idClaims{Claims: authz.Claims{
		Owner: org,
		Azp:   app,
		// The claim IAM stamps on a client_credentials token and on nothing else.
		Type: authz.Program,
		RegisteredClaims: jwt.RegisteredClaims{
			Audience: jwt.ClaimStrings{app},
			Subject:  org + "/" + app,
		},
	}}
}

func TestAgentIdentityIsAClientCredentialsPrincipal(t *testing.T) {
	if !isClientCredentialsPrincipal(agentToken("acme")) {
		t.Fatal("an <org>-agent token must be recognised as client-credentials — " +
			"that recognition is what denies it the admin grant")
	}
}

// THE ESCALATION, named. An agent provisioned in the reserved admin org carries
// owner=="admin", the predicate SuperAdmin is read from. It must still be
// recognised as a machine.
func TestAgentInAdminOrgIsDeniedSudo(t *testing.T) {
	if !isClientCredentialsPrincipal(agentToken("admin")) {
		t.Fatal("an agent identity in the admin org must be recognised as a machine — " +
			"otherwise owner==\"admin\" reads as SuperAdmin, which is the exact " +
			"escalation a previous run credential was reverted for")
	}
}

// A person is not a machine: their token is minted for an app they signed into,
// so the subject names the USER, not the client.
func TestAPersonIsNotAMachine(t *testing.T) {
	person := &idClaims{Claims: authz.Claims{
		Owner: "acme", Azp: "hanzo-console",
		RegisteredClaims: jwt.RegisteredClaims{
			Audience: jwt.ClaimStrings{"hanzo-console"},
			Subject:  "acme/z",
		},
	}}
	if isClientCredentialsPrincipal(person) {
		t.Fatal("a signed-in person must not be read as a machine")
	}
}
