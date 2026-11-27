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
	// Iss overrides the issuer, for a token minted under the discovered issuer of a
	// running Issuer0 (see Discover). Empty is Issuer.
	Iss string
	// Act is the `act` claim's subject: the actor IAM names on a token it mints
	// for someone acting as another (RFC 8693). Empty omits the claim.
	Act string
}

// Issuer0 is a signing issuer plus its published JWKS.
type Issuer0 struct {
	key *rsa.PrivateKey
	kid string
	// URL is the JWKS endpoint. Point a validator at it with Config(), which stands a
	// discovery document in front of it.
	URL string
}

// New stands up the JWKS endpoint. A validator is pointed at it through sdk/oidctest, which
// serves the OIDC discovery document that names it, so the validator under test is
// assembled exactly as production assembles it: from discovery.
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
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": srv.URL, "jwks_uri": srv.URL + "/jwks"})
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	iss.URL = srv.URL
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
	if c.Iss == "" {
		c.Iss = Issuer
	}
	claims := jwt.MapClaims{
		"iss": c.Iss,
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
	if c.Act != "" {
		claims["act"] = map[string]any{"sub": c.Act}
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = i.kid
	raw, err := tok.SignedString(i.key)
	if err != nil {
		t.Fatalf("iamtest: sign: %v", err)
	}
	return raw
}

// Discover points this process at the issuer as its OIDC provider, by the
// CLOUD_OIDC_DISCOVERY a deployment sets, and returns the issuer its document names
// (its own loopback address). Mint tokens for code that validates through the
// process's own discovery under that issuer: Claims{Iss: issuer}.
func (i *Issuer0) Discover(t *testing.T) string {
	t.Helper()
	t.Setenv("CLOUD_OIDC_DISCOVERY", i.URL+"/.well-known/openid-configuration")
	return i.URL
}
