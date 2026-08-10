package main

// This program assembled its own app by hand and installed the identity boundary
// without the context a typed op reads the result from. The subsystem compensated
// from inside its own Mount, on a group node that owned no routes, and zip refused
// to compose the program — which is how this binary stopped serving. These pin the
// repaired shape: whatever else newApp does, an app it returns delivers the
// validated org to a handler.
//
// The token is real. It is RS256-signed by a key generated here, published through
// a JWKS endpoint served here, and validated by the code that validates production
// traffic. A test that merely set X-Org-Id would assert the opposite of what
// matters, because deleting the client's copy of that header is the boundary's
// entire job — which is what the second test holds.

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
	"github.com/hanzoai/authz"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

const (
	testIssuer = "https://test.iam"
	testOrg    = "acme"
)

// issue starts a JWKS endpoint and returns its URL with a token signed by the key
// it publishes, so the app under test resolves a real signature over the path
// production uses.
func issue(t *testing.T) (jwksURL, token string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	keys, err := json.Marshal(map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "kid": "test-key", "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
	}}})
	if err != nil {
		t.Fatalf("marshal keys: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(keys)
	}))
	t.Cleanup(srv.Close)

	claims := authz.Claims{
		Owner: testOrg,
		Email: "joe@acme.io",
		Orgs:  []authz.Membership{{Org: testOrg, Role: authz.Member}},
	}
	claims.Issuer = testIssuer
	claims.Subject = "u-" + testOrg
	claims.Audience = jwt.ClaimStrings{"hanzo-console"}
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Hour))
	claims.IssuedAt = jwt.NewNumericDate(time.Now())

	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = "test-key"
	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return srv.URL, signed
}

// observed is what a handler beneath the whole chain saw.
type observed struct {
	org string
	ok  bool
	ran bool
}

// ask builds this program's own app, hangs a leaf off it that reports the org a
// TYPED op would resolve, and drives one request through the chain.
func ask(t *testing.T, jwksURL string, mutate func(*http.Request)) observed {
	t.Helper()
	cfg := &cloud.Config{Brand: "hanzo", IAMIssuer: testIssuer, JWKSURL: jwksURL}
	app := newApp(cfg, cloud.Deps{})

	var got observed
	app.Get("/v1/o11y/probe", func(c *zip.Ctx) error {
		got.ran = true
		// principal.OrgFrom reads the CONTEXT, which is all a typed op receives.
		// Reading c.Org() instead would pass even with the enrichment missing, and
		// would therefore assert nothing about the failure this repairs.
		got.org, got.ok = principal.OrgFrom(c.Context())
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/o11y/probe", nil)
	if mutate != nil {
		mutate(req)
	}
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer res.Body.Close()
	if !got.ran {
		t.Fatalf("leaf never ran (status %d) — the app refused to compose, or the chain answered first", res.StatusCode)
	}
	return got
}

// TestValidatedOrgReachesTheHandler is the assertion the outage cost us.
func TestValidatedOrgReachesTheHandler(t *testing.T) {
	jwksURL, token := issue(t)
	got := ask(t, jwksURL, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
	})
	if !got.ok || got.org != testOrg {
		t.Fatalf("org on the context = %q (present=%v), want %q — the identity the boundary validated never reached the handler",
			got.org, got.ok, testOrg)
	}
}

// TestForgedOrgHeaderReachesNothing keeps the test above honest: if a header could
// satisfy it, it would be asserting nothing.
func TestForgedOrgHeaderReachesNothing(t *testing.T) {
	jwksURL, _ := issue(t)
	got := ask(t, jwksURL, func(r *http.Request) {
		r.Header.Set(authz.HeaderOrg, "victim")
		r.Header.Set(authz.HeaderUserAdmin, "true")
	})
	if got.ok || got.org != "" {
		t.Fatalf("org on the context = %q (present=%v), want empty — a forged header was honoured", got.org, got.ok)
	}
}
