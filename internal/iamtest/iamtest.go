// Package iamtest is a REAL IAM issuer for tests: a keypair, a JWKS endpoint, and
// tokens signed with it.
//
// It exists because a stubbed validator cannot observe the bugs that live in the
// claim mapping. What makes an IAM lane safe is HOW a verified token becomes a
// principal — which claim is read for the subject, what happens when `sub` is
// absent, whether an id_token is distinguishable from an access token — and a stub
// that returns a pre-built identity has already made every one of those decisions
// itself. A test built on one asserts its own fixture.
//
// It is one package rather than a copy per caller for the same reason everything
// else here is: two fixtures for one wire drift, and the one that drifts is the one
// whose test then passes against code that would fail in production.
//
// SIGNING HERE GRANTS NOTHING. The key is generated per test and its JWKS is served
// on a loopback address the test itself owns; no deployment trusts either. This is
// the "forging a bad token is how a verifier gets tested" case the token gate names
// as out of scope, made explicit and shared instead of retyped.
package iamtest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Issuer is the issuer the minted tokens name and the validator under test trusts.
const Issuer = "https://test.iam"

// Audience is the app the minted tokens are FOR, unless a case overrides it. A
// resource server that gates on audience is told this value; one that does not
// ignores it.
const Audience = "hanzo-team"

// Claims is the claim vocabulary a test varies. Every field maps to a real IAM
// claim; nothing here is a cloud-side invention.
type Claims struct {
	// Sub is the `sub` claim. EMPTY MEANS OMITTED — the subject-confusion case is
	// a token that carries none, which a typed struct could not express.
	Sub string
	// PreferredUsername is the IAM username claim, the half of the canonical-user-id
	// fallback chain that a subject-less token resolves through.
	PreferredUsername string
	// Name is IAM's display-name claim, the last fallback in that chain.
	Name string
	// Owner is the home org — the tenant every account-store query scopes to.
	Owner string
	// TokenType is IAM's `tokenType`. Empty defaults to an access token; "-" omits
	// the claim entirely, which is the pre-rollout token shape.
	TokenType string
	// Orgs is the signed membership set. Its FIRST entry is the home org — the
	// tenant rule the estate states in idClaims.homeOrg — so a case that omits it
	// mints a token with no home, which is what a machine credential looks like.
	Orgs []map[string]any
	// Aud overrides the audience, for the case where a token was minted for a
	// DIFFERENT app than the one being asked to accept it.
	Aud string
	// Exp defaults to an hour out. A past value mints an expired token.
	Exp time.Time
}

// Issuer0 is a signing issuer plus its published JWKS.
type Issuer0 struct {
	key *rsa.PrivateKey
	kid string
	// URL is the JWKS endpoint. Point a validator at it with CLOUD_JWKS_URL.
	URL string
}

// New stands up the JWKS endpoint and points cloud's validator at it through
// CLOUD_JWKS_URL — the SAME override a deployment uses to pin a custom JWKS, so
// the validator under test is assembled exactly as production assembles it.
func New(t *testing.T) *Issuer0 {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	iss := &Issuer0{key: key, kid: "cert-hanzo"}
	body, _ := json.Marshal(map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "kid": iss.kid, "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
	}}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	iss.URL = srv.URL
	t.Setenv("CLOUD_JWKS_URL", srv.URL)
	return iss
}

// Sign mints a signed token carrying exactly the claims given, as a plain map so a
// test can OMIT one.
func (i *Issuer0) Sign(t *testing.T, c Claims) string {
	t.Helper()
	if c.Exp.IsZero() {
		c.Exp = time.Now().Add(time.Hour)
	}
	if c.TokenType == "" {
		c.TokenType = "access-token"
	}
	if c.Aud == "" {
		c.Aud = Audience
	}
	claims := jwt.MapClaims{
		"iss": Issuer,
		"aud": c.Aud,
		"exp": c.Exp.Unix(),
		"iat": time.Now().Unix(),
	}
	for k, v := range map[string]string{
		"sub": c.Sub, "preferred_username": c.PreferredUsername,
		"name": c.Name, "owner": c.Owner,
	} {
		if v != "" {
			claims[k] = v
		}
	}
	if c.TokenType != "-" {
		claims["tokenType"] = c.TokenType
	}
	if c.Orgs != nil {
		claims["orgs"] = c.Orgs
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = i.kid
	raw, err := tok.SignedString(i.key)
	if err != nil {
		t.Fatalf("iamtest: sign: %v", err)
	}
	return raw
}
