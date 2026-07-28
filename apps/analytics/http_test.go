// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package analytics

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

// do issues a request. user simulates the SanitizeIdentity-minted X-User-Id
// (present ONLY for a validated bearer); org simulates the minted X-Org-Id. In the
// harness there is no middleware, so c.User()/c.Org() read these headers directly
// — exactly the values SanitizeIdentity would set downstream.
func do(t *testing.T, app *zip.App, method, path, user, org string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

var dataEndpoints = []string{
	"/v1/analytics/overview",
	"/v1/analytics/timeseries",
	"/v1/analytics/top",
}

// TestNoPrincipalForbidden: no validated principal (no X-User-Id) → 403 on every
// data endpoint. This is the "no-Bearer → 403" contract.
func TestNoPrincipalForbidden(t *testing.T) {
	app := mountApp(t)
	for _, p := range dataEndpoints {
		if code, _ := do(t, app, http.MethodGet, p, "", ""); code != http.StatusForbidden {
			t.Fatalf("no-principal GET %s want 403, got %d", p, code)
		}
	}
}

// TestForgedOrgWithoutBearerForbidden: THE cross-tenant-forge proof. A caller that
// reaches the pod directly with a raw `X-Org-Id: maxpower` but NO validated
// principal (no X-User-Id) is refused 403 — it can never read maxpower's analytics
// off the Phase-1 header-passthrough path. (SanitizeIdentity leaves X-User-Id
// empty on that path; our tenant() gate rejects it.)
func TestForgedOrgWithoutBearerForbidden(t *testing.T) {
	app := mountApp(t)
	for _, p := range dataEndpoints {
		if code, _ := do(t, app, http.MethodGet, p, "", "maxpower"); code != http.StatusForbidden {
			t.Fatalf("forged-org-no-bearer GET %s want 403, got %d", p, code)
		}
	}
}

// TestDatastoreDisabledHonest503: a VALIDATED principal, but the datastore ledger
// is not connected (DatastoreEnabled()==false in this harness) → honest 503, never
// a fake 200 with zeros. Proves the "no fabricated metrics" invariant.
func TestDatastoreDisabledHonest503(t *testing.T) {
	app := mountApp(t)
	for _, p := range dataEndpoints {
		code, body := do(t, app, http.MethodGet, p, "user-dave", "maxpower")
		if code != http.StatusServiceUnavailable {
			t.Fatalf("datastore-down GET %s want 503, got %d (%s)", p, code, body)
		}
	}
}

// TestBadRangeIs400: a validated principal with an unknown ?range → 400 (before
// the datastore is even consulted).
func TestBadRangeIs400(t *testing.T) {
	app := mountApp(t)
	code, _ := do(t, app, http.MethodGet, "/v1/analytics/overview?range=bogus", "user-dave", "maxpower")
	if code != http.StatusBadRequest {
		t.Fatalf("bad range want 400, got %d", code)
	}
}

// TestHealthOwnedByAnalyticsHonest: /v1/analytics/health is the analytics
// subsystem's REAL probe (service=analytics, datastore bool), NOT serve.go's
// generic GET /v1/<name>/health fake-200 (which never mounts here, and in
// production is skipped because we register with cloud.HealthOwner). With the
// datastore down it 503s honestly. Health needs no principal — liveness must be
// probe-able.
func TestHealthOwnedByAnalyticsHonest(t *testing.T) {
	app := mountApp(t)
	code, body := do(t, app, http.MethodGet, "/v1/analytics/health", "", "")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("health (datastore down) want 503, got %d (%s)", code, body)
	}
	var h map[string]any
	if err := json.Unmarshal(body, &h); err != nil {
		t.Fatalf("health json: %v (%s)", err, body)
	}
	if h["service"] != "analytics" {
		t.Fatalf("health service want analytics (not generic liveness), got %v", h["service"])
	}
	if h["datastore"] != false {
		t.Fatalf("health datastore want false when disconnected, got %v", h["datastore"])
	}
	if h["status"] != "degraded" {
		t.Fatalf("health status want degraded when datastore down, got %v", h["status"])
	}
}
