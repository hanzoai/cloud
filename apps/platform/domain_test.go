package platform

import (
	"context"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
)

// TestValidateOrgDomainsBindsToOrg proves the only host under the apex an app
// may render is its own name, one label deep — never another app's name, a
// two-level host the apex certificate cannot cover, or a Hanzo apex (RED —
// domain hijack). With no store, no host can be a verified custom claim, so
// every other host is refused.
func TestValidateOrgDomainsBindsToOrg(t *testing.T) {
	ctx := context.Background()
	s := &cloud.Service[state]{State: state{sitesHost: "hanzo.app"}}
	if err := validateOrgDomains(s, ctx, "maxpower", "api", []string{"api.hanzo.app"}); err != nil {
		t.Errorf("the app's own name should be allowed, got %v", err)
	}
	bad := []string{
		"web.hanzo.app",          // another app's name
		"api.maxpower.hanzo.app", // two levels under the apex
		"api.hanzo.ai",           // Hanzo apex
		"hanzo.ai",               //
		"hanzo.app",              // the apex itself
		"evil.com",               // arbitrary
		"api.hanzo.app.evil.com", // suffix-in-the-middle trick
	}
	for _, d := range bad {
		if err := validateOrgDomains(s, ctx, "maxpower", "api", []string{d}); err == nil {
			t.Errorf("cross-tenant/apex domain %q MUST be refused", d)
		}
	}
	// empty is fine (no ingress).
	if err := validateOrgDomains(s, ctx, "maxpower", "api", nil); err != nil {
		t.Errorf("no domains should be allowed, got %v", err)
	}
}

// TestHTTPCrossOrgDomainRejected proves the boundary at the HTTP layer: creating
// an app that names another app's host is refused (403), and a custom host not
// yet verified is refused (501) — never silently rendered into the operator CR
// ingress.
func TestHTTPCrossOrgDomainRejected(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		seedProject(t, app, "maxpower", "web")
		code, _ := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{
			"name": "api", "source": "image", "image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1"},
			"domains": []string{"site.hanzo.app"}, // another app's name — hijack attempt
		})
		if code != http.StatusForbidden {
			t.Fatalf("another app's name want 403, got %d", code)
		}
		if code, _ := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{
			"name": "api", "source": "image", "image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1"},
			"domains": []string{"yourco.com"},
		}); code != http.StatusNotImplemented {
			t.Fatalf("an unverified custom domain want 501, got %d", code)
		}
		// The app's OWN name is accepted.
		if code, _ := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{
			"name": "ok", "source": "image", "image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1"},
			"domains": []string{"ok.hanzo.app"},
		}); code != http.StatusCreated {
			t.Fatalf("the app's own name want 201, got %d", code)
		}
	})
}
