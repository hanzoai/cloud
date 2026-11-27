// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"net/http"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hanzoai/authz"
)

// SuperAdmin is the boundary's verdict and nothing an org name spells. Through the
// real pipeline — boundary, trail, commerce — an application IAM minted under the
// admin org (a client_credentials program) and a person whose org is spelled
// "Admin" both reach commerce carrying X-User-Owner of that org. Commerce used to
// read that header (folding its case) as SuperAdmin, so both could edit prices
// here while the boundary had refused them platform authority.
func TestPriceWritesAreRefusedToAdminOrgMachinesAndCaseVariants(t *testing.T) {
	app, _, sign := authorityHarness(t)
	person := func(sub, org string) string {
		return sign(authz.Claims{
			RegisteredClaims:  jwt.RegisteredClaims{Subject: sub},
			Owner:             org,
			PreferredUsername: sub,
			Email:             sub + "@example.test",
			Orgs:              []authz.Membership{{Org: org, Role: authz.Owner}},
		})
	}
	super := person("z", "admin")
	machine := sign(authz.Claims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "admin-agent"},
		Owner:            "admin",
		Name:             "admin-agent",
		Type:             authz.Program,
	})
	callers := map[string]string{
		"an admin-org application": machine,
		"a person in Admin":        person("mallory", "Admin"),
		"a person in ADMIN":        person("mallory", "ADMIN"),
	}
	for _, w := range writes {
		if code, body := drive(t, app, http.MethodPost, w.create, super, w.row, nil); code != http.StatusCreated {
			t.Fatalf("POST %s as SuperAdmin: %d %s", w.create, code, body)
		}
		for name, tok := range callers {
			if code, body := drive(t, app, http.MethodPut, w.put, tok, w.edit, nil); code != http.StatusForbidden && code != http.StatusUnauthorized {
				t.Errorf("PUT %s as %s: %d %s, want 403", w.put, name, code, body)
			}
		}
		if code, body := drive(t, app, http.MethodPut, w.put, super, w.edit, nil); code != http.StatusOK {
			t.Errorf("PUT %s as SuperAdmin: %d %s, want 200", w.put, code, body)
		}
	}
}
