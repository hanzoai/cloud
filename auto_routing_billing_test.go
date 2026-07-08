// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

package cloud

// Auto-routing billing binding at the cloud edge.
//
// The ai subsystem serves a virtual `auto`/`zen-router` model: it resolves the
// request to a concrete model id BEFORE pricing/billing, meters its own LLM
// token cost to commerce keyed on the SERVED model, and reports that id via the
// `X-Routed-Model` response header (and the response body `model` field).
//
// The cloud edge must therefore do two things for an `auto` request, both
// verified here end-to-end through the real BillingGate + DefaultPrice:
//  1. NOT re-price it by the (virtual) request model — /v1/ai/* is self-metered,
//     so the edge gate delegates all LLM billing to the ai subsystem. That
//     subsystem bills the resolved model, so `auto` bills as what served it.
//  2. Pass the `X-Routed-Model` header through untouched, so the model the
//     client sees reported is exactly the model that was billed.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// TestAutoRoutingBillsAsResolvedModel drives an `auto` chat request through the
// real edge gate (BillingGate + DefaultPrice) in front of a handler that
// simulates the ai subsystem after it resolved auto→zen4-coder.
func TestAutoRoutingBillsAsResolvedModel(t *testing.T) {
	fc := &fakeCommerce{balanceBody: `{"available":5000}`}
	srv := fc.server(t)
	m := mustClient(t, srv.URL, false /* fail-closed */)

	app := zip.New(zip.Config{})
	// The genuine edge gate with the genuine price function.
	app.Use(BillingGate(m, DefaultPrice))
	// Stand-in for the mounted ai subsystem: auto already resolved to zen4-coder,
	// which it reports on the header + body (and meters itself — not modeled here).
	app.Post("/v1/ai/chat/completions", func(c *zip.Ctx) error {
		c.SetHeader("X-Routed-Model", "zen4-coder")
		return c.JSON(http.StatusOK, map[string]string{"model": "zen4-coder"})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/ai/chat/completions",
		strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"refactor this"}]}`))
	req.Header.Set("X-Org-Id", "hanzo")
	req.Header.Set("X-User-Id", "alice")
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// (1) The edge did NOT bill: /v1/ai/* is self-metered (DefaultPrice 0), so the
	// gate short-circuits before Authorize/Record. Billing is delegated to the ai
	// subsystem, which meters the RESOLVED model — never the virtual `auto`.
	// (Give any erroneous async Record a moment to land before asserting zero.)
	if fc.usages() != 0 {
		t.Fatalf("edge recorded %d usage(s) for /v1/ai/*, want 0 (self-metered by ai)", fc.usages())
	}
	if waitFor(func() bool { return fc.usages() > 0 }, 50*time.Millisecond) {
		t.Fatalf("edge double-billed an ai request (usages=%d): auto must be billed by the ai meter, not the edge", fc.usages())
	}

	// (2) Header pass-through: the resolved model the ai meter billed reaches the
	// client, so what's reported == what's billed.
	if got := resp.Header.Get("X-Routed-Model"); got != "zen4-coder" {
		t.Errorf("X-Routed-Model = %q, want zen4-coder (must pass through the edge)", got)
	}
}

// TestDefaultPriceAiPathModelAgnostic documents the binding at the pricing layer:
// the edge price for the ai chat path is 0 regardless of the request model, so
// `auto` and a concrete model are treated identically — the ai subsystem's own
// per-token meter (keyed on the resolved model) is the single source of the
// charge. Path-based, never request-model-based, pricing is what makes `auto`
// bill as the model that served it.
func TestDefaultPriceAiPathModelAgnostic(t *testing.T) {
	if got := priceForPath(t, "/v1/ai/chat/completions"); got != 0 {
		t.Errorf("DefaultPrice(/v1/ai/chat/completions) = %d, want 0 — ai self-meters the resolved model", got)
	}
}
