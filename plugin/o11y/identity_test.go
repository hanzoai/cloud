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
	"github.com/hanzoai/cloud/sdk/oidctest"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/principal"
	// devmaster keys this test binary: the trail opens nothing without a master
	// and a test process has no KMS.
	_ "github.com/hanzoai/cloud/sdk/kit/master"
	"github.com/hanzoai/authz"
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
	jwksURL, sign, person := keyed(t)
	return jwksURL, sign(person)
}

// keyed is issue's parts: the JWKS URL, a signer under the key it publishes, and
// the person's claims, for a test that signs more than one token.
func keyed(t *testing.T) (jwksURL string, sign func(jwt.Claims) string, person authz.Claims) {
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

		Issuer:    testIssuer,
		Subject:   "u-" + testOrg,
		Audience:  jwt.ClaimStrings{"hanzo-console"},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		IssuedAt:  jwt.NewNumericDate(time.Now())}

	sign = func(c jwt.Claims) string {
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
		tok.Header["kid"] = "test-key"
		signed, err := tok.SignedString(key)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return signed
	}
	return srv.URL, sign, claims
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
	cfg := &cloud.Config{Brand: "hanzo", IAMIssuer: testIssuer, Discovery: oidctest.Discovery(testIssuer), DiscoveryClient: oidctest.Client(testIssuer, jwksURL)}
	app := newApp(cfg, cloud.Deps{})

	var got observed
	app.Raw(http.MethodGet, "/v1/o11y/probe", func(c *zip.Ctx) error {
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
	res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
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

// A support session — the person's claims, the operator in `act`, `imp: true` —
// is contained and witnessed in this process too, which cloud.Listen does not
// serve: newApp installs cloud.Witness itself. Every write here is refused to it,
// a dashboard's public link and a POST query included; its reads reach the
// handler; and every one of its requests is on the trail with the operator named.
// The person's own token writes, and only its write is recorded.
func TestASupportSessionChangesNothingHereAndIsWitnessed(t *testing.T) {
	jwksURL, sign, person := keyed(t)
	support := sign(struct {
		authz.Claims
		Act map[string]string `json:"act"`
		Imp bool              `json:"imp"`
	}{person, map[string]string{"sub": "u-op", "owner": "admin", "name": "z"}, true})
	own := sign(person)
	trail, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trail.Close() })
	app := newApp(&cloud.Config{Brand: "hanzo", IAMIssuer: testIssuer, Discovery: oidctest.Discovery(testIssuer), DiscoveryClient: oidctest.Client(testIssuer, jwksURL)}, cloud.Deps{Audit: trail})
	var reached int
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		app.Raw(m, "/v1/o11y/*", func(c *zip.Ctx) error {
			reached++
			return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
		})
	}
	do := func(method, path, bearer string) int {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		return res.StatusCode
	}
	writes := []struct{ method, path string }{
		{http.MethodPost, "/v1/o11y/gateway/ingestion_keys"}, {http.MethodPost, "/v1/o11y/dashboards"},
		{http.MethodPut, "/v1/o11y/dashboards/d1/public"}, {http.MethodPatch, "/v1/o11y/dashboards/d1"},
		{http.MethodDelete, "/v1/o11y/channels/c1"}, {http.MethodPost, "/v1/o11y/rules"},
		{http.MethodPost, "/v1/o11y/query_range"}, {http.MethodPut, "/v1/o11y/settings/ttl"},
	}
	for _, w := range writes {
		if code := do(w.method, w.path, support); code != http.StatusForbidden {
			t.Errorf("support session %s %s: %d, want 403", w.method, w.path, code)
		}
	}
	if reached != 0 {
		t.Fatalf("a refused write reached the handler %d times", reached)
	}
	if code := do(http.MethodGet, "/v1/o11y/dashboards", support); code != http.StatusOK {
		t.Fatalf("support session GET: %d, want the person's read", code)
	}
	if code := do(http.MethodPost, "/v1/o11y/dashboards", own); code != http.StatusOK {
		t.Fatalf("the person's own POST: %d, want it unaffected", code)
	}
	if code := do(http.MethodGet, "/v1/o11y/dashboards", own); code != http.StatusOK {
		t.Fatalf("the person's own GET: %d", code)
	}

	all, _, err := trail.Query(t.Context(), audit.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	// Each change is on the trail twice, begun before it ran and then its
	// outcome; the outcomes are read here.
	var rows []audit.Record
	for _, r := range all {
		if r.Outcome.Result != "begun" {
			rows = append(rows, r)
		}
	}
	if want, changes := len(writes)+2, len(writes)+1; len(rows) != want || len(all) != want+changes {
		t.Fatalf("trail rows = %d (%d outcomes), want %d and %d: every request of the session, and the person's own write",
			len(all), len(rows), want+changes, want)
	}
	op := audit.Act{Sub: "u-op", Owner: "admin", Name: "z"}
	ownWrite, read := rows[0], rows[1] // newest first
	if read.Method != http.MethodGet || read.Actor.Sub != "u-"+testOrg || read.Actor.Act != op {
		t.Fatalf("the session's read %+v, want the person as actor and the operator in act", read)
	}
	for _, refusal := range rows[2:] {
		if refusal.Outcome.Status != http.StatusForbidden || refusal.Actor.Act != op {
			t.Fatalf("refusal row %+v, want a 403 naming the operator", refusal)
		}
	}
	if ownWrite.Method != http.MethodPost || ownWrite.Actor.Act != (audit.Act{}) {
		t.Fatalf("the person's own write %+v, want it recorded with no operator", ownWrite)
	}
}
