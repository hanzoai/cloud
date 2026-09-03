// Copyright © 2026 Hanzo AI. MIT License.

package clients

import (
	"errors"
	"testing"
)

// TestRouteOnMovesOnlyForModelShapedRefusals is the whole safety of the walk. A
// bad prompt retried on a cheaper model fails twice and bills for both, so only a
// refusal ABOUT THE MODEL advances the route.
func TestRouteOnMovesOnlyForModelShapedRefusals(t *testing.T) {
	move := []string{
		`model "enso-flash" is not available. Use GET /v1/models`,
		"model_not_found",
		"unknown model: enso-ultra",
		`{"error":{"code":"insufficient_balance"}}`,
		"spend_cap_exceeded",
		"402 payment required",
	}
	for _, m := range move {
		if !routeOn(errors.New(m)) {
			t.Errorf("routeOn(%q) = false, want true — a model-shaped refusal should try the next hop", m)
		}
	}
	stay := []string{
		"invalid json body",
		"authentication required",
		"context length exceeded",
		"rate limited, please retry",
		"upstream returned no choices",
	}
	for _, m := range stay {
		if routeOn(errors.New(m)) {
			t.Errorf("routeOn(%q) = true, want false — this is not about the model", m)
		}
	}
	if routeOn(nil) {
		t.Error("routeOn(nil) = true, want false")
	}
}
