// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package cloud

import "testing"

// TestIamEdgePublic pins the sign-in surface that must bypass the tenant gate
// (else console login is a chicken-and-egg: /v1/iam/get-app-login + /v1/iam/login
// 401 "sign in to continue" before any session exists) — WITHOUT opening any
// tenant-data route. The exact-match sign-in verbs, the OAuth token endpoint, and
// OIDC discovery are public; every org-scoped CRUD verb stays gated.
func TestIamEdgePublic(t *testing.T) {
	e := &iamEdge{}

	public := []string{
		"get-app-login", "login", "signin", "signup",
		"get-captcha", "send-verification-code", "verify-code",
		"oauth/access_token", "login/oauth/access_token",
		".well-known/openid-configuration", ".well-known/jwks",
	}
	for _, seg := range public {
		if !e.public(seg) {
			t.Errorf("sign-in route %q must be public (login would 401)", seg)
		}
	}

	// Tenant CRUD + org metadata must NEVER be public — they carry tenant data and
	// rely on the org pin for cross-tenant isolation.
	gated := []string{
		"get-users", "get-user", "get-roles", "get-organization",
		"get-organization-projects", "add-user", "update-user", "delete-user",
		"update-organization", "add-project", "delete-project",
	}
	for _, seg := range gated {
		if e.public(seg) {
			t.Errorf("tenant route %q must stay behind the gate, not be public", seg)
		}
	}
}
