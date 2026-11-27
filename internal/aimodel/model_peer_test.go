// Copyright © 2026 Hanzo AI. MIT License.

package aimodel

import (
	"context"
	"testing"
)

// TestModelFallsBackToTheFloor is the property every caller depends on: a surface
// always names a model. With no settings peer in this process the ask fails, and
// the compiled floor is what the binary shipped with.
//
// It is also what makes the change safe to deploy ahead of anyone configuring a
// row: behaviour is byte-identical to the constant until someone sets one.
func TestModelFallsBackToTheFloor(t *testing.T) {
	for _, role := range []string{RoleDefault, RoleChat, RoleFallback} {
		if got := Model(context.Background(), role, DefaultModel); got != DefaultModel {
			t.Errorf("Model(%q) = %q with no authority, want the floor %q", role, got, DefaultModel)
		}
	}
}

// TestRolesAreTheDocumentsKeys pins that the words an operator types at
// admin.hanzo.ai are the words the code reads. They were separate strings once in
// the price authority and the two drifted; here there is one spelling.
func TestRolesAreTheDocumentsKeys(t *testing.T) {
	for role, want := range map[string]string{
		RoleDefault:  "defaultModel",
		RoleChat:     "chatModel",
		RoleFallback: "fallbackModel",
	} {
		if role != want {
			t.Errorf("role key = %q, want %q", role, want)
		}
	}
}

// A model of ours that cannot be served falls back to Enso's router, which the
// gateway serves to every caller; enso-auto is its own floor, and a Zen or upstream
// model has none.
func TestFloorIsEnsosRouter(t *testing.T) {
	for in, want := range map[string]string{
		"enso-flash": "enso-auto",
		"enso":       "enso-auto",
		"enso-max":   "enso-auto",
		"enso-ultra": "enso-auto",
		// already the floor: one hop, not the same name twice
		"enso-auto": "",
		// another product, with no model every caller is served
		"zen":       "",
		"zen-coder": "",
		// not ours: upstream price is upstream cost
		"deepseek-v4-pro": "",
		"kimi-k3":         "",
		"":                "",
	} {
		if got := Floor(in); got != want {
			t.Errorf("Floor(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRouteEndsAtTheFreeTier is the property that makes an unnamed request always
// answerable: whatever the configured default is, the route's last hop is a model
// we serve to everyone.
func TestRouteEndsAtTheFreeTier(t *testing.T) {
	r := Route(context.Background()) // no authority in this process → the floor
	if len(r) == 0 {
		t.Fatal("Route returned no hops; an unnamed request would have nothing to run on")
	}
	if r[0] != DefaultModel {
		t.Errorf("Route[0] = %q, want the floor %q", r[0], DefaultModel)
	}
	if last := r[len(r)-1]; last != "enso-auto" {
		t.Errorf("Route ends at %q, want Enso's router, enso-auto", last)
	}
}
