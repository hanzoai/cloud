// Copyright © 2026 Hanzo AI. MIT License.
//
// Package organization — M1 regression: the "platform" org-name namespace
// bypass is gone.
//
// Previously Organization.Namespace() returned "" (the global/cross-org
// datastore namespace where every org's records live) when Name=="platform",
// keyed purely on an org-NAME string and fully decoupled from real
// platform-admin identity. Anyone who could land in (or forge owner=) an org
// named "platform" got a cross-org datastore view (Red — privilege escalation
// via the empty namespace). Cross-org access is now gated EXCLUSIVELY on
// auth.IAMClaims.GlobalAdmin() at the handler layer; the model never grants it
// by name.
package organization

import "testing"

// TestNamespace_NoPlatformBypass pins the fix: "platform" is just another org
// name, strictly scoped to itself — never the empty/global namespace.
func TestNamespace_NoPlatformBypass(t *testing.T) {
	if ns := (Organization{Name: "platform"}).Namespace(); ns != "platform" {
		t.Fatalf("platform org namespace = %q, want \"platform\" (empty-namespace bypass must be removed)", ns)
	}
}

// TestNamespace_StrictScoping proves ordinary orgs are scoped to their own
// name, and an empty name maps to the empty namespace with no special-casing.
func TestNamespace_StrictScoping(t *testing.T) {
	cases := map[string]string{
		"maxpower": "maxpower",
		"hanzo":    "hanzo",
		"admin":    "admin", // even the global-admin org is name-scoped here;
		//                       cross-org access is granted by GlobalAdmin(), not by Namespace().
		"": "",
	}
	for name, want := range cases {
		if got := (Organization{Name: name}).Namespace(); got != want {
			t.Errorf("Organization{Name:%q}.Namespace() = %q, want %q", name, got, want)
		}
	}
}
