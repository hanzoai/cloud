// Copyright © 2026 Hanzo AI. MIT License.

package cloud

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

// TestFreeModelIsTheSameFamily pins the rule that makes the fallback honest: a
// paid tier degrades to the free tier of its OWN family, so the answer is still
// the product the caller asked for. Falling from enso to zen-free would answer as
// a different product.
func TestFreeModelIsTheSameFamily(t *testing.T) {
	for in, want := range map[string]string{
		"enso-flash": "enso-free",
		"enso":       "enso-free",
		"enso-ultra": "enso-free",
		"zen":        "zen-free",
		"zen-coder":  "zen-free",
		// already the floor of its family — one hop, not the same name twice
		"enso-free": "",
		"zen-free":  "",
		"free":      "",
		// not ours: upstream price is upstream cost, and there is no free tier
		// of somebody else's model
		"deepseek-v4-pro": "",
		"kimi-k3":         "",
		"":                "",
	} {
		if got := FreeModel(in); got != want {
			t.Errorf("FreeModel(%q) = %q, want %q", in, got, want)
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
	if last := r[len(r)-1]; last != "enso-free" {
		t.Errorf("Route ends at %q, want the family's free tier enso-free", last)
	}
}
