package kms_test

// KMS MACHINE-TOKEN END-TO-END. A REAL, RSA-signed client_credentials-style bearer —
// owner=<org>, aud=<org>-platform-kms (the tenant's own IAM application clientId) —
// flows through cloud's ACTUAL SanitizeIdentity middleware (cloud.IdentityMiddleware)
// and the ACTUAL /v1/kms org-scope guard.
//
// Audience is NOT an access gate: trust is signature + issuer + expiry, and reach is
// the SIGNED owner claim (guard: owner == :org). So a machine token validates like any
// other and is isolated to its own org by owner. The existing kms_test/paas_sync_test
// do() helper HEADER-INJECTS X-Org-Id / X-User-Id and never exercises validation; here
// the org is derived by SanitizeIdentity from the signed owner claim, exactly as in
// production, so the test proves the whole chain:
//
//   real signed machine token → SanitizeIdentity (signature/issuer/expiry, owner derived)
//                             → /v1/kms guard (owner == :org) → 200 own org / 403 else

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gojose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/hanzoai/cloud"
	model "github.com/hanzoai/iam/pkg/model"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

const e2eIssuer = "https://test.iam"

// machineClaims mirrors the IAM JWT fields cloud's validator reads. Its JSON shape
// matches the token IAM's client_credentials grant mints for a per-tenant
// "<org>-platform-kms" application (owner=<org>, aud=<org>-platform-kms).
type machineClaims struct {
	jwt.Claims
	Owner string         `json:"owner"`
	Orgs  []model.OrgRef `json:"orgs"` // membership SET, home org first.
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
		Orgs:  []model.OrgRef{{Org: owner}},
	}).Serialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return raw
}

func e2eCfg(t *testing.T, jwksURL string) *cloud.Config {
	t.Helper()
	return &cloud.Config{
		Brand:           "hanzo",
		Domain:          "api.hanzo.ai",
		IAMIssuer:       e2eIssuer,
		JWKSURL:         jwksURL,
		AdminOrg:        "admin",
		DataDir:         t.TempDir(),
		Enable:          []string{"kms"},
		KMSMasterKeyRef: masterKeyB64(t),
	}
}

// newAppWithIdentity wires the REAL identity boundary (cloud.IdentityMiddleware) in
// front of the REAL /v1/kms routes — the production pipeline minus the gateway. It
// deliberately does NOT reuse kms_test's newApp (which omits identity so its
// header-injection tests work); here validation must actually run.
func newAppWithIdentity(t *testing.T, cfg *cloud.Config) (*zip.App, cloud.Deps) {
	t.Helper()
	deps := cloud.BuildDeps(cfg)
	app := zip.New(zip.Config{Logger: deps.Logger})
	app.Use(middleware.Recover())
	app.Use(cloud.IdentityMiddleware(cfg))
	if err := cloud.MountAll(app, mountSpecs(), cfg, deps); err != nil {
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
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// TestPaaSSyncMachineTokenEndToEnd drives the real signed-token pipeline.
//
// THE PATH NO LONGER NAMES A TENANT, so "acme's path" and "maxpower's path" are one
// string and every cross-tenant assertion here has to be made on the VALUE returned,
// not the status. Both orgs are therefore seeded at the identical coordinate with
// distinct plaintexts: whichever string comes back names the org that was actually
// served, which is the only unambiguous evidence of scope. A cross-tenant attempt
// answers 404 (or the caller's own record), never 403 — the caller cannot express a
// request for another tenant's secret, so there is nothing to forbid.
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
	aPath := "/v1/kms" + paasEnvPath
	future := time.Now().Add(time.Hour)

	// (1) maxpower's REAL machine token reads maxpower's secret → 200. Audience is not an
	// access gate (trust is signature + issuer + expiry); owner=maxpower is the scope.
	own := mintMachineToken(t, key, paasOrgA, paasOrgA+"-platform-kms", future)
	if resp := getWithBearer(t, app, aPath, own); resp.StatusCode != 200 {
		t.Fatalf("machine token → own org = %d, want 200", resp.StatusCode)
	} else if got := decode(t, resp.Body)["value"]; got != paasValueA {
		t.Fatalf("read value=%v, want the sealed secret", got)
	}

	// (2) Cross-tenant: acme's REAL machine token, at the SAME URL, gets acme's own
	// namespace — empty — and never maxpower's plaintext. SanitizeIdentity pins
	// owner=acme from the signed claim, so the coordinate resolves under acme.
	cross := mintMachineToken(t, key, paasOrgB, paasOrgB+"-platform-kms", future) // paasOrgB == "acme"
	resp := getWithBearer(t, app, aPath, cross)
	if resp.StatusCode != 404 {
		t.Fatalf("cross-tenant machine token (acme at maxpower's URL) = %d, want 404 "+
			"(the org rides on the token, so acme's request resolves inside acme)", resp.StatusCode)
	}
	if b := readAll(resp.Body); strings.Contains(b, paasValueA) {
		t.Fatalf("LEAK: acme's machine token received maxpower's secret: %s", b)
	}

	// (2b) Give acme its OWN secret at the identical coordinate and re-issue the
	// byte-identical request: each machine token must receive its own tenant's
	// plaintext. This is the assertion a status-only check cannot make, and the one
	// that fails if the per-org partition ever breaks.
	const valueB = "s3kr3t-of-acme-e2e"
	sealPlatformSecret(t, deps.KMS, paasOrgB, valueB)
	if got := decode(t, getWithBearer(t, app, aPath, cross).Body)["value"]; got != valueB {
		t.Fatalf("PARTITION BREAK: acme's token read %v, want %q", got, valueB)
	}
	if got := decode(t, getWithBearer(t, app, aPath, own).Body)["value"]; got != paasValueA {
		t.Fatalf("PARTITION BREAK: maxpower's token read %v, want %q", got, paasValueA)
	}

	// (3) Audience is not a gate: a token with a brand-new, never-registered aud still
	// reads its OWN org (owner=maxpower) → 200 — the "new first-party app just works with
	// zero cloud change" invariant. The aud never widens reach beyond the owner.
	if resp := getWithBearer(t, app, aPath, mintMachineToken(t, key, paasOrgA, "a-brand-new-app-never-registered", future)); resp.StatusCode != 200 {
		t.Fatalf("never-registered aud reading OWN org = %d, want 200 (aud is not a gate)", resp.StatusCode)
	}

	// (4) …and the aud still cannot cross tenants. Carrying the VICTIM's machine aud
	// is the sharpest form of the attack, and the answer is decided entirely by the
	// owner claim: maxpower bearing ACME's machine aud is served MAXPOWER's value.
	// The status is 200 either way, so only the value distinguishes "owner governs"
	// from "aud widened reach" — which is why this assertion is on the body.
	audCross := mintMachineToken(t, key, paasOrgA, paasOrgB+"-platform-kms", future)
	if got := decode(t, getWithBearer(t, app, aPath, audCross).Body)["value"]; got != paasValueA {
		t.Fatalf("AUD WIDENED REACH: owner=maxpower bearing acme's machine aud read %v, want %q "+
			"(owner scopes, not aud)", got, paasValueA)
	}
	audCrossB := mintMachineToken(t, key, paasOrgB, paasOrgA+"-platform-kms", future)
	if got := decode(t, getWithBearer(t, app, aPath, audCrossB).Body)["value"]; got != valueB {
		t.Fatalf("AUD WIDENED REACH: owner=acme bearing maxpower's machine aud read %v, want %q", got, valueB)
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
	absent := "/v1/kms" + "/secrets/platform/api/NOPE?env=default"
	if resp := getWithBearer(t, app, absent, own); resp.StatusCode != 404 {
		t.Fatalf("own-org absent secret = %d, want 404 (boundary is org, not blanket-deny)", resp.StatusCode)
	}
}
