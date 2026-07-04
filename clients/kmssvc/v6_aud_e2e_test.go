package kmssvc_test

// V6 END-TO-END (the activation blocker). A REAL, RSA-signed client_credentials-
// style bearer — owner=<org>, aud=<org>-platform-kms (the tenant's own IAM
// application clientId, which is by construction NEVER in the configured audience
// allowlist) — flows through cloud's ACTUAL SanitizeIdentity middleware
// (cloud.IdentityMiddleware) and the ACTUAL /v1/kms org-scope guard.
//
// This is the gap Red flagged: the existing kms_test/paas_sync_test do() helper
// HEADER-INJECTS X-Org-Id / X-User-Id and never exercises validation. Here the org
// is derived by SanitizeIdentity from the SIGNED owner claim, exactly as in
// production, so the test proves the whole chain:
//
//   real signed machine token → SanitizeIdentity (audience accepted, owner derived)
//                             → /v1/kms guard (owner == :org) → 200 own org / 403 else
//
// If the V6 audience fix regressed, case (1) would 403 (machine aud rejected →
// anonymous → guard denies) and the sync would stay pending — the failure this locks.

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gojose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

const e2eIssuer = "https://test.iam"

// machineClaims mirrors the IAM JWT fields cloud's validator reads. Its JSON shape
// matches the token IAM's client_credentials grant mints for a per-tenant
// "<org>-platform-kms" application (owner=<org>, aud=<org>-platform-kms).
type machineClaims struct {
	jwt.Claims
	Owner string `json:"owner"`
}

// e2eJWKS serves a single-key JWKS (kid=test-key) for pub — the endpoint the
// identity validator fetches IAM signing keys from.
func e2eJWKS(t *testing.T, pub *rsa.PublicKey) *httptest.Server {
	t.Helper()
	set := gojose.JSONWebKeySet{Keys: []gojose.JSONWebKey{{
		Key: pub, KeyID: "test-key", Algorithm: "RS256", Use: "sig",
	}}}
	body, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	t.Cleanup(srv.Close)
	return srv
}

// mintMachineToken signs a client_credentials-shaped token. owner + aud are passed
// independently so negative cases (owner-mismatched aud, arbitrary aud) can be built.
func mintMachineToken(t *testing.T, key *rsa.PrivateKey, owner, aud string, exp time.Time) string {
	t.Helper()
	signer, err := gojose.NewSigner(
		gojose.SigningKey{Algorithm: gojose.RS256, Key: key},
		(&gojose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key"),
	)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	raw, err := jwt.Signed(signer).Claims(machineClaims{
		Claims: jwt.Claims{
			Issuer: e2eIssuer,
			// The application principal id — non-empty so SanitizeIdentity sets
			// X-User-Id, which principal.Validated (and thus the guard) requires.
			Subject:  owner + "/" + owner + "-platform-kms",
			Audience: jwt.Audience{aud},
			Expiry:   jwt.NewNumericDate(exp),
			IssuedAt: jwt.NewNumericDate(time.Now()),
		},
		Owner: owner,
	}).Serialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return raw
}

func e2eCfg(t *testing.T, jwksURL string) *cloud.Config {
	t.Helper()
	return &cloud.Config{
		Brand:     "hanzo",
		Domain:    "api.hanzo.ai",
		IAMIssuer: e2eIssuer,
		JWKSURL:   jwksURL,
		// The machine aud (<org>-platform-kms) is deliberately ABSENT here, so a 200
		// below proves the OWNER-BOUND machine-aud FIX, not a broadened allowlist.
		JWTAudiences:    []string{"hanzo-console"},
		AdminOrg:        "admin",
		DataDir:         t.TempDir(),
		Enable:          []string{"kmssvc"},
		KMSMasterKeyRef: masterKeyB64(t),
	}
}

// newAppWithIdentity wires the REAL identity boundary (cloud.IdentityMiddleware) in
// front of the REAL kmssvc routes — the production pipeline minus the gateway. It
// deliberately does NOT reuse kms_test's newApp (which omits identity so its
// header-injection tests work); here validation must actually run.
func newAppWithIdentity(t *testing.T, cfg *cloud.Config) (*zip.App, cloud.Deps) {
	t.Helper()
	deps := cloud.BuildDeps(cfg)
	app := zip.New(zip.Config{Logger: deps.Logger})
	app.Use(middleware.Recover())
	app.Use(cloud.IdentityMiddleware(cfg))
	if err := cloud.MountAll(app, cfg, deps); err != nil {
		t.Fatalf("MountAll: %v", err)
	}
	return app, deps
}

func getWithBearer(t *testing.T, app *zip.App, path, token string) *http.Response {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

func TestPaaSSyncMachineTokenEndToEnd(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := e2eJWKS(t, &key.PublicKey)
	app, deps := newAppWithIdentity(t, e2eCfg(t, jwks.URL))

	// Seal maxpower's platform secret at the exact coordinate the KMSSecret CR points
	// the operator at (reuses the paas_sync_test seal helper — one seal path).
	sealPlatformSecret(t, deps.KMS, paasOrgA, paasValueA) // paasOrgA == "maxpower"
	aPath := "/v1/kms/orgs/" + paasOrgA + paasEnvPath
	future := time.Now().Add(time.Hour)

	// (1) maxpower's REAL machine token reads maxpower's secret → 200. Its aud
	// (maxpower-platform-kms) is NOT in the allowlist; acceptance is the V6 fix.
	own := mintMachineToken(t, key, paasOrgA, paasOrgA+"-platform-kms", future)
	if resp := getWithBearer(t, app, aPath, own); resp.StatusCode != 200 {
		t.Fatalf("machine token → own org = %d, want 200 (activation blocker still open?)", resp.StatusCode)
	} else if got := decode(t, resp.Body)["value"]; got != paasValueA {
		t.Fatalf("read value=%v, want the sealed secret", got)
	}

	// (2) Cross-tenant: acme's REAL machine token cannot read maxpower's secret → 403.
	// SanitizeIdentity pins owner=acme from the signed claim; the guard denies.
	cross := mintMachineToken(t, key, paasOrgB, paasOrgB+"-platform-kms", future) // paasOrgB == "acme"
	if resp := getWithBearer(t, app, aPath, cross); resp.StatusCode != 403 {
		t.Fatalf("cross-tenant machine token (acme→maxpower) = %d, want 403", resp.StatusCode)
	}

	// (3) Owner-bound: a maxpower token bearing acme's machine aud is INVALID — the
	// audience is bound to the token's OWN owner, so validation fails → anonymous →
	// guard 403. Proves the aud is not a blanket "*-platform-kms" wildcard.
	if resp := getWithBearer(t, app, aPath, mintMachineToken(t, key, paasOrgA, paasOrgB+"-platform-kms", future)); resp.StatusCode != 403 {
		t.Fatalf("owner-mismatched machine aud = %d, want 403", resp.StatusCode)
	}

	// (4) A token with an arbitrary audience is not accepted → 403 (fix is scoped).
	if resp := getWithBearer(t, app, aPath, mintMachineToken(t, key, paasOrgA, "some-random-app", future)); resp.StatusCode != 403 {
		t.Fatalf("arbitrary-audience token = %d, want 403", resp.StatusCode)
	}

	// (5) An expired machine token is anonymous → 403 (fail closed on expiry).
	if resp := getWithBearer(t, app, aPath, mintMachineToken(t, key, paasOrgA, paasOrgA+"-platform-kms", time.Now().Add(-time.Hour))); resp.StatusCode != 403 {
		t.Fatalf("expired machine token = %d, want 403", resp.StatusCode)
	}

	// (6) No credential at all → 403 (the guard requires a validated principal).
	if resp := getWithBearer(t, app, aPath, ""); resp.StatusCode != 403 {
		t.Fatalf("no-credential read = %d, want 403", resp.StatusCode)
	}

	// (7) maxpower's own token reading its OWN absent scope is 404, not 403 — proving
	// (1)'s 200 was the org boundary admitting the caller, not a blanket allow.
	absent := "/v1/kms/orgs/" + paasOrgA + "/secrets/platform/api/NOPE?env=default"
	if resp := getWithBearer(t, app, absent, own); resp.StatusCode != 404 {
		t.Fatalf("own-org absent secret = %d, want 404 (boundary is org, not blanket-deny)", resp.StatusCode)
	}
}
