package platform

import (
	"context"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud"
)

// TestValidateOrgDomainsBindsToOrg proves a custom ingress host must be under the
// caller's OWN "<org>.<sitesHost>" subtree — a tenant can never claim another
// org's host or a Hanzo apex (RED — domain hijack). With no store, no host can be
// a verified custom claim, so every non-subtree host is refused.
func TestValidateOrgDomainsBindsToOrg(t *testing.T) {
	ctx := context.Background()
	s := &cloud.Service[state]{State: state{sitesHost: "hanzo.app"}}
	ok := []string{"api.maxpower.hanzo.app", "web.staging.maxpower.hanzo.app"}
	for _, d := range ok {
		if err := validateOrgDomains(s, ctx, "maxpower", []string{d}); err != nil {
			t.Errorf("own-org domain %q should be allowed, got %v", d, err)
		}
	}
	bad := []string{
		"api.acme.hanzo.app",              // another org's subtree
		"api.hanzo.ai",                    // Hanzo apex
		"hanzo.ai",                        //
		"maxpower.hanzo.app",              // exact suffix, no label — not a real host under it
		"evil.com",                        // arbitrary
		"api.maxpower.hanzo.app.evil.com", // suffix-in-the-middle trick
	}
	for _, d := range bad {
		if err := validateOrgDomains(s, ctx, "maxpower", []string{d}); err == nil {
			t.Errorf("cross-tenant/apex domain %q MUST be refused", d)
		}
	}
	// empty is fine (no ingress).
	if err := validateOrgDomains(s, ctx, "maxpower", nil); err != nil {
		t.Errorf("no domains should be allowed, got %v", err)
	}
}

// TestHTTPCrossOrgDomainRejected proves the boundary at the HTTP layer: creating
// an app that claims another org's domain is refused (501, not silently rendered
// into the operator CR ingress).
func TestHTTPCrossOrgDomainRejected(t *testing.T) {
	app := mountApp(t)
	seedProject(t, app, "maxpower", "web")
	code, _ := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{
		"name": "api", "source": "image", "image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1"},
		"domains": []string{"api.acme.hanzo.app"}, // acme's subtree — hijack attempt
	})
	if code != http.StatusNotImplemented {
		t.Fatalf("cross-org domain claim want 501, got %d", code)
	}
	// The tenant's OWN subtree is accepted.
	if code, _ := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{
		"name": "ok", "source": "image", "image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1"},
		"domains": []string{"ok.maxpower.hanzo.app"},
	}); code != http.StatusCreated {
		t.Fatalf("own-org domain want 201, got %d", code)
	}
}
