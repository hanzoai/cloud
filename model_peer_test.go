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
