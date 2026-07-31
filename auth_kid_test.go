package cloud

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

// A token is verified by the KEY IT NAMED, or not at all — asserted through cloud's
// own boundary, because this is the boundary that runs in front of production.
//
// cloud used to hold its own key selection, and it fell back: after the kid-matched
// key failed it looped over every RSA signing key in the JWKS and accepted the first
// that verified. So a token naming cert-hanzo was accepted on a signature from
// cert-lux, and a token naming NO key was accepted on any of them.
//
// It was not exploitable by a tenant: IAM publishes only certs owned by a reserved
// platform org (isSigningCert → store.IsSigningCertOwner), so a customer cannot get a
// key into the set to sign with. What the fallback cost is the INVARIANT, and with
// the invariant gone any future widening of what reaches the JWKS becomes an
// impersonation path silently.
func TestTokenIsVerifiedByTheKeyItNamed(t *testing.T) {
	named, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	// Two PLATFORM keys, both legitimately published — which is the real shape: key
	// rotation is additive and each brand has its own cert.
	jwk := func(kid string, pub *rsa.PublicKey) map[string]any {
		return map[string]any{
			"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}
	}
	body, _ := json.Marshal(map[string]any{"keys": []map[string]any{
		jwk("cert-hanzo", &named.PublicKey),
		jwk("cert-lux", &other.PublicKey),
	}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	v := newIdentityValidator(testIssuer, srv.URL, 0)

	sign := func(key *rsa.PrivateKey, kid string) string {
		t.Helper()
		c := tokenClaims("hanzo-console", "acme", "", false, time.Now().Add(time.Hour))
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c.Claims)
		if kid != "" {
			tok.Header["kid"] = kid
		}
		raw, err := tok.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	if _, err := v.validate(sign(named, "cert-hanzo")); err != nil {
		t.Fatalf("a token signed by the key it named was refused: %v", err)
	}
	if _, err := v.validate(sign(other, "cert-hanzo")); err == nil {
		t.Error("SECURITY: a token naming cert-hanzo verified against a DIFFERENT published key")
	}
	if _, err := v.validate(sign(named, "")); err == nil {
		t.Error("SECURITY: a token naming NO key verified against the set")
	}
}
