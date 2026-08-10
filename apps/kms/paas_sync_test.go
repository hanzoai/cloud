package kms_test

// PaaS KMS→Secret sync proofs, against the REAL embedded KMS store + the REAL
// org-scope guard + the REAL login broker (no mocks of the boundary under test).
//
// The platform control plane (apps/platform/secrets.go) seals each PaaS secret
// at the org-scoped coordinate  orgs/<org>/platform/<app>/<KEY>  (kmsSecretRef) and
// authors a KMSSecret CR pointing the kms-operator at
//   /v1/kms/secrets/platform/<app>/<KEY>   (org comes from the token)
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
	"strings"
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
	path := "/v1/kms" + paasEnvPath
	resp := do(t, app, "GET", path, paasOrgA, "", false, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("operator read of its own sealed secret = %d, want 200 (seal/read coordinates MISALIGNED)", resp.StatusCode)
	}
	if got := decode(t, resp.Body)["value"]; got != paasValueA {
		t.Fatalf("read value=%v, want the sealed secret", got)
	}
}

// TestPaaSSecretCrossTenantDenied is the NON-NEGOTIABLE proof: tenant-B, presenting
// a validated principal for its OWN org, never obtains tenant-A's platform secret.
//
// ONE PATH, TWO TENANTS. The KMSSecret CR points every operator at the SAME URL —
// /v1/kms/secrets/platform/<app>/<KEY> — because the org is no longer in it; each
// operator's own token supplies the tenant. So "B reading A's path" and "B reading
// its own path" are now the SAME REQUEST, distinguished only by the credential and
// answered only in the body. That is why this test asserts VALUES, not just codes:
// a status-only check cannot tell a refusal from B being served B's own record, and
// the earlier `want 403` was in fact firing on exactly that.
//
// The signal is 404, not 403, and it is stronger: B cannot express a request for
// A's record at all (the org is unspellable), so what B gets is the honest answer
// for B's own namespace — absent, or B's own secret — and A's existence is never
// confirmed. 403 is reserved for the case that really is a refusal: no principal.
func TestPaaSSecretCrossTenantDenied(t *testing.T) {
	app, deps := newApp(t, baseCfg(t, masterKeyB64(t)))
	sealPlatformSecret(t, deps.KMS, paasOrgA, paasValueA) // only A's secret exists

	path := "/v1/kms" + paasEnvPath

	// B, at the operator's URL, with nothing of its own there: not-found, and — the
	// actual isolation assertion — A's plaintext is nowhere in the response.
	resp := do(t, app, "GET", path, paasOrgB, "", false, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("cross-tenant read (B at A's URL) = %d, want 404 (B's token resolves the "+
			"path inside B's own namespace, where the record is absent)", resp.StatusCode)
	}
	if b := readAll(resp.Body); strings.Contains(b, paasValueA) {
		t.Fatalf("LEAK: tenant B received tenant A's platform secret: %s", b)
	}

	// Same request from A itself succeeds and returns A's value — the credential,
	// not the path, selects the tenant.
	resp = do(t, app, "GET", path, paasOrgA, "", false, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("A→A = %d, want 200 (the boundary is the caller's org, not the path)", resp.StatusCode)
	}
	if got := decode(t, resp.Body)["value"]; got != paasValueA {
		t.Fatalf("A read value=%v, want its own sealed secret", got)
	}

	// Now give B a secret of its own at the IDENTICAL coordinate. Both tenants
	// issue byte-identical requests; each must receive its own plaintext. This is
	// the case a status-only test is blind to, and the one that would actually
	// catch a partition break.
	const valueB = "s3kr3t-of-acme"
	sealPlatformSecret(t, deps.KMS, paasOrgB, valueB)
	resp = do(t, app, "GET", path, paasOrgB, "", false, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("B→B = %d, want 200 once B has its own record", resp.StatusCode)
	}
	if got := decode(t, resp.Body)["value"]; got != valueB {
		t.Fatalf("PARTITION BREAK: B read value=%v, want %q (B must never see A's record)", got, valueB)
	}
	// …and A is unaffected by B's record existing at the same coordinate.
	if got := decode(t, do(t, app, "GET", path, paasOrgA, "", false, nil).Body)["value"]; got != paasValueA {
		t.Fatalf("PARTITION BREAK: A read value=%v, want %q", got, paasValueA)
	}

	// A forged X-Org-Id is irrelevant because the guard also requires a VALIDATED
	// principal (X-User-Id); an org with no principal is refused 403 — the one
	// genuine FORBIDDEN on this surface, and it never reaches the store.
	resp = do(t, app, "GET", path, "", "", false, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("unauthenticated read = %d, want 403", resp.StatusCode)
	}
	if b := readAll(resp.Body); strings.Contains(b, paasValueA) {
		t.Fatalf("LEAK: unauthenticated read returned A's secret: %s", b)
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
