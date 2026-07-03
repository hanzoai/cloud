package kmssvc_test

// PaaS KMS→Secret sync proofs, against the REAL embedded KMS store + the REAL
// org-scope guard + the REAL login broker (no mocks of the boundary under test).
//
// The platform control plane (clients/platform/secrets.go) seals each PaaS secret
// at the org-scoped coordinate  orgs/<org>/platform/<app>/<KEY>  (kmsSecretRef) and
// authors a KMSSecret CR pointing the kms-operator at
//   /v1/kms/orgs/<org>/secrets/platform/<app>/<KEY>
// (projectSlug=<org>, secretsPath=platform/<app>). These tests prove:
//
//   (1) ALIGNMENT — that seal path and that read path resolve to the SAME record
//       (the drift between them was why the sync was inert), and
//   (2) CROSS-TENANT DENIAL — a validated principal for one tenant can read ONLY
//       its own scope; a different tenant's identical request is denied 403 by the
//       guard before the store is touched, and
//   (3) LOGIN BROKER — /v1/kms/auth/login exchanges the operator's per-tenant
//       clientId/clientSecret at IAM and returns IAM's owner-scoped token verbatim,
//       failing closed on a bad or malformed credential.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/hanzoai/cloud"
)

const (
	paasOrgA    = "maxpower"
	paasOrgB    = "acme"
	paasApp     = "api"
	paasKey     = "DB_PASSWORD"
	paasValueA  = "s3kr3t-of-maxpower"
	paasEnvPath = "/secrets/platform/" + paasApp + "/" + paasKey + "?env=default"
)

// sealPlatformSecret seals a secret at the EXACT coordinate clients/platform.
// kmsSecretRef(org, "api", "DB_PASSWORD") produces. Kept in lockstep with that
// function by construction; a drift here would resurrect the inert-sync bug.
func sealPlatformSecret(t *testing.T, kc cloud.KMSClient, org, value string) {
	t.Helper()
	ref := "orgs/" + org + "/platform/" + paasApp + "/" + paasKey
	if err := kc.PutSecret(context.Background(), ref, []byte(value)); err != nil {
		t.Fatalf("seal platform secret for %s: %v", org, err)
	}
}

// TestPaaSSecretSealReadAlignment proves the coordinate fix end-to-end: a secret
// sealed at the platform seal ref is readable through cloud's org-scoped /v1/kms
// surface at exactly the path the KMSSecret CR points the operator at.
func TestPaaSSecretSealReadAlignment(t *testing.T) {
	app, deps := newApp(t, baseCfg(t, masterKeyB64(t)))
	sealPlatformSecret(t, deps.KMS, paasOrgA, paasValueA)

	// projectSlug=org, secretsPath=platform/<app>; authenticated as owner=<org>.
	path := "/v1/kms/orgs/" + paasOrgA + paasEnvPath
	resp := do(t, app, "GET", path, paasOrgA, "", false, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("operator read of its own sealed secret = %d, want 200 (seal/read coordinates MISALIGNED)", resp.StatusCode)
	}
	if got := decode(t, resp.Body)["value"]; got != paasValueA {
		t.Fatalf("read value=%v, want the sealed secret", got)
	}
}

// TestPaaSSecretCrossTenantDenied is the NON-NEGOTIABLE proof: tenant-B, presenting
// a validated principal for its OWN org, cannot read tenant-A's platform secret —
// the guard denies 403 before the store is touched. The denial is the ORG boundary
// (B reading its own absent scope is 404, not 403), not a blanket failure.
func TestPaaSSecretCrossTenantDenied(t *testing.T) {
	app, deps := newApp(t, baseCfg(t, masterKeyB64(t)))
	sealPlatformSecret(t, deps.KMS, paasOrgA, paasValueA) // only A's secret exists

	aPath := "/v1/kms/orgs/" + paasOrgA + paasEnvPath
	if resp := do(t, app, "GET", aPath, paasOrgB, "", false, nil); resp.StatusCode != 403 {
		t.Fatalf("cross-tenant read (B→A) = %d, want 403 DENIED", resp.StatusCode)
	}
	// Same request from A itself succeeds — the credential, not the path, is the gate.
	if resp := do(t, app, "GET", aPath, paasOrgA, "", false, nil); resp.StatusCode != 200 {
		t.Fatalf("A→A = %d, want 200 (the boundary is the caller's org, not the path)", resp.StatusCode)
	}
	// B reading its OWN (absent) scope is 404, not 403 — the denial is org-scoped.
	bPath := "/v1/kms/orgs/" + paasOrgB + paasEnvPath
	if resp := do(t, app, "GET", bPath, paasOrgB, "", false, nil); resp.StatusCode != 404 {
		t.Fatalf("B→B (absent) = %d, want 404 (boundary is org, not blanket-deny)", resp.StatusCode)
	}
	// A forged X-Org-Id is irrelevant here because the guard also requires a
	// VALIDATED principal (X-User-Id); an org with no principal is refused.
	if resp := do(t, app, "GET", aPath, "", "", false, nil); resp.StatusCode != 403 {
		t.Fatalf("unauthenticated read = %d, want 403", resp.StatusCode)
	}
}

// TestPaaSLoginBrokerHappyPath proves /v1/kms/auth/login brokers the operator's
// credential to IAM's client_credentials endpoint and returns IAM's token verbatim
// (cloud is a relay, not an issuer). The upstream form carries the grant + creds.
func TestPaaSLoginBrokerHappyPath(t *testing.T) {
	var gotForm url.Values
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/iam/oauth/token" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = r.ParseForm()
		gotForm = r.Form
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "iam-jwt-owner-maxpower", "expires_in": 3600, "token_type": "Bearer",
		})
	}))
	defer iam.Close()

	cfg := baseCfg(t, masterKeyB64(t))
	cfg.IAMIssuer = iam.URL // login broker computes {issuer}/v1/iam/oauth/token
	app, _ := newApp(t, cfg)

	body, _ := json.Marshal(map[string]string{"clientId": "platform-kms@maxpower", "clientSecret": "shh"})
	resp := do(t, app, "POST", "/v1/kms/auth/login", "", string(body), false, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("login = %d, want 200", resp.StatusCode)
	}
	if got := decode(t, resp.Body)["accessToken"]; got != "iam-jwt-owner-maxpower" {
		t.Fatalf("accessToken=%v, want IAM's token verbatim", got)
	}
	if gotForm.Get("grant_type") != "client_credentials" {
		t.Fatalf("grant_type=%q, want client_credentials", gotForm.Get("grant_type"))
	}
	if gotForm.Get("client_id") != "platform-kms@maxpower" {
		t.Fatalf("client_id not passed through to IAM: %q", gotForm.Get("client_id"))
	}
}

// TestPaaSLoginBrokerFailClosed proves the broker fails closed: a bad credential
// (IAM 4xx) → 401, and a malformed request never reaches IAM → 400.
func TestPaaSLoginBrokerFailClosed(t *testing.T) {
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
	}))
	defer iam.Close()
	cfg := baseCfg(t, masterKeyB64(t))
	cfg.IAMIssuer = iam.URL
	app, _ := newApp(t, cfg)

	post := func(raw string) int {
		return do(t, app, "POST", "/v1/kms/auth/login", "", raw, false, nil).StatusCode
	}
	// Bad credential: IAM rejects → 401 (no oracle detail).
	if code := post(`{"clientId":"x","clientSecret":"wrong"}`); code != 401 {
		t.Fatalf("bad-cred login = %d, want 401", code)
	}
	// Missing clientSecret → 400, never reaches IAM.
	if code := post(`{"clientId":"x"}`); code != 400 {
		t.Fatalf("missing-secret login = %d, want 400", code)
	}
	// Control byte (NUL) in clientId → 400.
	if code := post("{\"clientId\":\"x\\u0000y\",\"clientSecret\":\"s\"}"); code != 400 {
		t.Fatalf("control-byte login = %d, want 400", code)
	}
	// Not JSON → 400.
	if code := post("not-json"); code != 400 {
		t.Fatalf("non-JSON login = %d, want 400", code)
	}
}
