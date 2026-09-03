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

	"github.com/hanzoai/cloud/finance"
	"github.com/zap-proto/zip"
)

// noFinance is the PLUGIN-PROCESS shape: no co-resident ledger at all. It is not
// publishFinance(t, nil) — that hands over a typed nil, which is a non-nil Client the
// consumer then calls — but a cleared client, which is what finance.Current() reports in
// a `--enable billing` child where installFinance never ran.
func noFinance(t *testing.T) {
	t.Helper()
	finance.Publish(nil)
	t.Cleanup(func() { finance.Publish(nil) })
}

// s2sCall issues a request bearing an Authorization token, which the plain `call`
// helper cannot do.
// userCall reads as a validated principal: X-User-Id is what the identity
// boundary mints from a verified credential, so its presence is what makes the
// org trusted. orgCall presents the org header alone, which is a tenant the
// caller chose for itself.
func userCall(t *testing.T, app *zip.App, path, user, org string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if user != "" {
		req.Header.Set("X-User-Id", user)
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

func orgCall(t *testing.T, app *zip.App, path, org string) (int, []byte) {
	t.Helper()
	return userCall(t, app, path, "", org)
}

// TestBalance_S2SDoesNotWidenScope is the other half: accepting a service token
// must not become a way to read someone else's books, or to skip auth entirely.
//
// The org on the S2S path comes from the gateway-pinned X-Org-Id — a header the
// gateway STRIPS from every client request — never from a caller-supplied field.
// A wrong token, an absent token, or a token with no org must all still refuse.
func TestBalance_NoPrincipalIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, org string }{
		{"an org header alone", "hanzo"},
		{"nothing at all", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fin := &fakeFinance{wallets: map[string]int64{"hanzo|hanzo": 14953300}}
			publishFinance(t, fin)
			app := mountApp(t, "")
			code, _ := orgCall(t, app, "/v1/billing/balance", tc.org)
			if code != http.StatusUnauthorized {
				t.Errorf("want 401, got %d", code)
			}
			if fin.calls != 0 {
				t.Errorf("the ledger was read %d times for a caller with no principal", fin.calls)
			}
		})
	}
}

// TestProxyLeg_NoPrincipalIsRefused guards the second resolution the same way the
// first is guarded: with no principal the proxy leg refuses too, and commerce is
// never called for it.
func TestProxyLeg_NoPrincipalIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, org string }{
		{"an org header alone", "hanzo"},
		{"nothing at all", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			noFinance(t)
			f := &fakeCommerce{status: 200, body: `{"balance":1,"holds":0,"available":1}`}
			app := mountApp(t, f.server(t).URL)
			code, _ := orgCall(t, app, "/v1/billing/balance", tc.org)
			if code != http.StatusUnauthorized {
				t.Errorf("want 401, got %d", code)
			}
			f.mu.Lock()
			called := f.gotPath
			f.mu.Unlock()
			if called != "" {
				t.Errorf("commerce was called (%s) for a caller with no principal", called)
			}
		})
	}
}
