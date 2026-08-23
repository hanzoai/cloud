// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package billing

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/apps/finance"
	"github.com/zap-proto/zip"
)

// noFinance is the PLUGIN-PROCESS shape: no co-resident ledger at all. It is not
// publishFinance(t, nil) — that hands over a typed nil, which is a non-nil Client the
// consumer then calls — but a cleared client, which is what finance.Current() reports in
// a `--enable billing` child where wireFinance never ran.
func noFinance(t *testing.T) {
	t.Helper()
	finance.Publish(nil)
	t.Cleanup(func() { finance.Publish(nil) })
}

// s2sCall issues a request bearing an Authorization token, which the plain `call`
// helper cannot do.
func s2sCall(t *testing.T, app *zip.App, path, token, org string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestBalance_TrustedS2SReadIsServed is the regression guard for a fleet-wide
// inference outage.
//
// ai's prepaid gate reads /v1/billing/balance to decide whether to admit a paid
// request. build.go's wireFinance installs an in-process balanceReader hook so
// that read is a direct typed call — but a Go func var cannot cross a PROCESS
// boundary, and once ai became its own plugin process it fell back to the HTTP
// path that comment calls the split-deploy fallback. That request carries
// COMMERCE_SERVICE_TOKEN rather than a user session, so principal.Org was empty
// and this handler answered "sign in to view billing".
//
// The gate is fail-CLOSED — a balance it cannot verify must never degrade to free
// inference — so that single 401 denied EVERY paid call on v1.801.320:
//
//	[billing] GET /v1/billing/balance  err="sign in to view billing"
//	[ai] balance_gate: balance unverifiable for cold subject=hanzo:
//	     commerce returned 401 (fail-CLOSED, retryable)
func TestBalance_TrustedS2SReadIsServed(t *testing.T) {
	// mountApp sets COMMERCE_SERVICE_TOKEN itself, so the token must go THROUGH it —
	// setting it beforehand is silently overwritten with "".
	const token = "test-commerce-service-token"
	fin := &fakeFinance{wallets: map[string]int64{"hanzo|hanzo": 14953300}}
	publishFinance(t, fin)
	app := mountApp(t, "", token)

	code, body := s2sCall(t, app, "/v1/billing/balance", token, "hanzo")
	if code != http.StatusOK {
		t.Fatalf("trusted S2S read: want 200, got %d (%s)", code, body)
	}
	if fin.calls == 0 {
		t.Error("the ledger was never read on the trusted S2S path")
	}
}

// TestBalance_S2SDoesNotWidenScope is the other half: accepting a service token
// must not become a way to read someone else's books, or to skip auth entirely.
//
// The org on the S2S path comes from the gateway-pinned X-Org-Id — a header the
// gateway STRIPS from every client request — never from a caller-supplied field.
// A wrong token, an absent token, or a token with no org must all still refuse.
func TestBalance_S2SDoesNotWidenScope(t *testing.T) {
	const token = "test-commerce-service-token"

	for _, tc := range []struct {
		name  string
		token string
		org   string
	}{
		{"wrong token with an org", "not-the-token", "hanzo"},
		{"empty token with an org", "", "hanzo"},
		{"valid token but no org to scope to", token, ""},
		{"near-miss token (prefix)", "test-commerce-service-toke", "hanzo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fin := &fakeFinance{wallets: map[string]int64{"hanzo|hanzo": 14953300}}
			publishFinance(t, fin)
			app := mountApp(t, "", token)

			code, _ := s2sCall(t, app, "/v1/billing/balance", tc.token, tc.org)
			if code != http.StatusUnauthorized {
				t.Errorf("want 401, got %d", code)
			}
			if fin.calls != 0 {
				t.Errorf("the ledger was read %d times without a trusted caller", fin.calls)
			}
		})
	}
}

// TestBalance_TrustedS2SReadSurvivesTheProxyLeg is the SPLIT-DEPLOY half of the same
// outage, and the one the co-resident test above structurally cannot see.
//
// Every test here published a finance ledger, so balance() always took the co-resident
// return and the S2S rule it holds was the only one that ran. A PLUGIN PROCESS is the
// opposite shape and it is the shape prod runs: `cloud.Listen(…, []string{"billing"})`
// leaves cfg.Enabled("commerce") false, so build.go's wireFinance returns before
// finance.Publish and finance.Current() is nil FOREVER in that process. availableCents
// then falls to the plane, and when the plane cannot answer, balance() delegates to
// proxy() — which asked principal.Org a SECOND time, without the S2S rule, and refused
// the caller balance() had just admitted:
//
//	GET /v1/billing/balance  Bearer $COMMERCE_SERVICE_TOKEN  X-Org-Id: hanzo
//	  → 401 {"status":401,"error":"sign in to view billing"}
//
// measured in prod on cloud v1.801.340 with the request log carrying org=hanzo AND the
// refusal — the first resolution working, the second one denying. ai's gate is
// fail-CLOSED, so that was every chat/copilot/document completion answering 503
// balance_unavailable.
//
// No ledger is published here on purpose: that IS the plugin process.
func TestBalance_TrustedS2SReadSurvivesTheProxyLeg(t *testing.T) {
	const token = "test-commerce-service-token"
	noFinance(t) // no co-resident ledger — the plugin-process shape

	f := &fakeCommerce{status: 200, body: `{"balance":14953300,"holds":0,"available":14953300}`}
	app := mountApp(t, f.server(t).URL, token)

	code, body := s2sCall(t, app, "/v1/billing/balance", token, "hanzo")
	if code == http.StatusUnauthorized {
		t.Fatalf("the proxy leg refused the trusted S2S caller balance() admitted: %s", body)
	}
	if code != http.StatusOK {
		t.Fatalf("split-deploy S2S read: want 200, got %d (%s)", code, body)
	}
	f.mu.Lock()
	gotOrg, gotPath := f.gotOrg, f.gotPath
	f.mu.Unlock()
	if gotPath != "/v1/billing/balance" {
		t.Errorf("commerce path: want /v1/billing/balance, got %q", gotPath)
	}
	// The org rides the gateway-pinned header, never a caller-supplied field.
	if gotOrg != "hanzo" {
		t.Errorf("commerce X-Org-Id: want hanzo, got %q", gotOrg)
	}
}

// TestProxyLeg_S2SDoesNotWidenScope guards the second resolution the same way the
// first is guarded: a wrong, absent or org-less token must still be refused on the
// proxy leg, and commerce must never be called for it.
func TestProxyLeg_S2SDoesNotWidenScope(t *testing.T) {
	const token = "test-commerce-service-token"

	for _, tc := range []struct {
		name  string
		token string
		org   string
	}{
		{"wrong token with an org", "not-the-token", "hanzo"},
		{"empty token with an org", "", "hanzo"},
		{"valid token but no org to scope to", token, ""},
		{"near-miss token (prefix)", "test-commerce-service-toke", "hanzo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			noFinance(t)
			f := &fakeCommerce{status: 200, body: `{"balance":1,"holds":0,"available":1}`}
			app := mountApp(t, f.server(t).URL, token)

			code, _ := s2sCall(t, app, "/v1/billing/balance", tc.token, tc.org)
			if code != http.StatusUnauthorized {
				t.Errorf("want 401, got %d", code)
			}
			f.mu.Lock()
			called := f.gotPath
			f.mu.Unlock()
			if called != "" {
				t.Errorf("commerce was called (%s) without a trusted caller", called)
			}
		})
	}
}
