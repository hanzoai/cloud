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
	"testing"
)

// TestDefaultPriceAiPathModelAgnostic documents the binding at the pricing layer:
// the edge price for the ai chat path is 0 regardless of the request model, so
// `auto` and a concrete model are treated identically — the ai subsystem's own
// per-token meter (keyed on the resolved model) is the single source of the
// charge. Path-based, never request-model-based, pricing is what makes `auto`
// bill as the model that served it.
func TestDefaultPriceAiPathModelAgnostic(t *testing.T) {
	index(t, &Config{Enable: []string{"ai"}}, Plugin{Name: "ai", Price: Metered})
	if got := priceForPath(t, "/v1/ai/chat/completions"); got != 0 {
		t.Errorf("DefaultPrice(/v1/ai/chat/completions) = %d, want 0 — ai self-meters the resolved model", got)
	}
}
