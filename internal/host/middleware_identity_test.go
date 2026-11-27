package host

// Tests for the in-binary identity trust boundary. They drive real requests
// through the zip/fiber stack against a real RSA-signed JWKS (httptest), so the
// whole path runs end-to-end: header strip -> token extract -> JWKS verify ->
// re-mint. The crux assertions: a raw X-User-IsAdmin never grants admin, and an
// ORG admin can never escalate to the SuperAdmin authority or another org.

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hanzoai/authz"
)

const testIssuer = "https://test.iam"

// jwksServer serves a single-key JWKS (kid=test-key) for pub.
func jwksServer(t *testing.T, pub *rsa.PublicKey) *httptest.Server {
	t.Helper()
	body, err := json.Marshal(map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "kid": "test-key", "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}}})
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// signWith signs claims with key, stamping kid=test-key.
func signWith(t *testing.T, key *rsa.PrivateKey, c idClaims) string {
	t.Helper()
	// golang-jwt, the library IAM SIGNS with and hanzoai/authz verifies with. Minting
	// with a different one certifies a reader nobody runs.
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c.Claims)
	tok.Header["kid"] = "test-key"
	raw, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return raw
}

// tokenClaims builds an IAM-shaped claim set for the ORDINARY case: the minting
// app's org and the user's own org AGREE, so `owner` and orgs[0] carry the same
// value.
//
// The `orgs` seed is not decoration — it is what makes this fixture resemble a real
// token. IAM has minted the claim since v1.33.0, and cloud reads the USER's org from
// it (idClaims.homeOrg), never from `owner`, which carries the APPLICATION's org.
// Every fixture here previously set `owner` alone, so the whole suite exercised a
// token shape production no longer emits — and, because the two values were always
// equal in the fixtures, no assertion could distinguish them. That is exactly how a
// caller-selectable tenant and a caller-selectable SuperAdmin gate survived.
//
// Tests that need the two to DISAGREE (the cross-app cases in
// middleware_identity_homeorg_test.go) override Orgs explicitly, as does any test
// modelling a pre-claim legacy token by clearing it.
func tokenClaims(aud, owner, email string, isAdmin bool, exp time.Time) idClaims {
	// The home's role is IAM's (store.HomeRole): admin for an account that
	// administers its own org, member otherwise.
	role := authz.Member
	if isAdmin {
		role = authz.Admin
	}
	c := idClaims{
		Owner:     owner,
		Email:     email,
		IsAdmin:   isAdmin,
		Orgs:      []authz.Membership{{Org: owner, Role: role}},
		Issuer:    testIssuer,
		Subject:   "u-" + owner,
		Audience:  jwt.ClaimStrings{aud},
		ExpiresAt: jwt.NewNumericDate(exp),
		IssuedAt:  jwt.NewNumericDate(time.Now())}
	return c
}

// idClaims is what the tests sign: IAM's own claim set.
type idClaims struct{ authz.Claims }
