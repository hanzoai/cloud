package account

import (
	"context"
	"net/http"
	"testing"

	"github.com/zap-proto/zip"
)

// mountBrand mounts the account surface for a given deployment brand (the embed
// entitlement + app-domain derivation are brand-scoped). IAM is unwired — embed-status
// does not use the confidential client.
func mountBrand(t *testing.T, brand string) *zip.App {
	t.Helper()
	t.Setenv("IAM_MINT_CLIENT_ID", "")
	t.Setenv("IAM_MINT_CLIENT_SECRET", "")
	return mountBoth(t, brand)
}

// stubProbe swaps the reachability probe for the duration of a test, recording how
// many times it ran (to prove a non-entitled caller is never probed).
func stubProbe(t *testing.T, up bool) *int {
	t.Helper()
	calls := 0
	prev := reachProbe
	reachProbe = func(context.Context, string) bool { calls++; return up }
	t.Cleanup(func() { reachProbe = prev })
	return &calls
}

func TestEmbedStatus_RequiresValidatedPrincipal(t *testing.T) {
	stubProbe(t, true)
	app := mountBrand(t, "hanzo")
	code, _ := callH(t, app, http.MethodGet, "/v1/embed-status?app=cms", nil, "")
	if code != http.StatusForbidden {
		t.Fatalf("no principal: want 403, got %d", code)
	}
}

func TestEmbedStatus_UnknownApp_400(t *testing.T) {
	stubProbe(t, true)
	app := mountBrand(t, "hanzo")
	code, _ := callH(t, app, http.MethodGet, "/v1/embed-status?app=nope",
		map[string]string{"X-User-Id": "alice", "X-Org-Id": "hanzo"}, "")
	if code != http.StatusBadRequest {
		t.Fatalf("unknown app: want 400, got %d", code)
	}
}

func TestEmbedStatus_BrandMemberEntitled_Reachable(t *testing.T) {
	calls := stubProbe(t, true)
	app := mountBrand(t, "hanzo")
	// A member of the owning brand org (hanzo) is entitled; the probe says up.
	code, body := callH(t, app, http.MethodGet, "/v1/embed-status?app=cms",
		map[string]string{"X-User-Id": "z", "X-Org-Id": "hanzo"}, "")
	if code != http.StatusOK {
		t.Fatalf("brand member: want 200, got %d (%s)", code, body)
	}
	var r embedStatusResp
	mustJSON(t, body, &r)
	if !r.Entitled || !r.Reachable || r.Phase != "ready" {
		t.Fatalf("entitled+reachable expected: %+v", r)
	}
	if r.Origin != "https://cms.hanzo.ai" || r.EmbedURL != "https://cms.hanzo.ai/admin" {
		t.Fatalf("origin/embedUrl wrong: %+v", r)
	}
	if *calls != 1 {
		t.Fatalf("entitled caller must be probed exactly once, got %d", *calls)
	}
}

func TestEmbedStatus_GlobalAdminEntitled(t *testing.T) {
	stubProbe(t, false)
	app := mountBrand(t, "hanzo")
	// A global admin from a DIFFERENT org is still entitled (isGlobalAdmin bypass);
	// the probe says down → not-provisioned but entitled with the embed URL.
	code, body := callH(t, app, http.MethodGet, "/v1/embed-status?app=erp",
		map[string]string{"X-User-Id": "root", "X-Org-Id": "acme", "X-User-IsAdmin": "true"}, "")
	if code != http.StatusOK {
		t.Fatalf("admin: want 200, got %d", code)
	}
	var r embedStatusResp
	mustJSON(t, body, &r)
	if !r.Entitled || r.Reachable || r.Phase != "not-provisioned" || r.EmbedURL != "https://erp.hanzo.ai/app" {
		t.Fatalf("admin entitled + not-provisioned expected: %+v", r)
	}
}

func TestEmbedStatus_CustomerOrgNotEntitled_NotProbed(t *testing.T) {
	calls := stubProbe(t, true)
	app := mountBrand(t, "hanzo")
	// A customer org (not the brand, not admin) is NOT entitled: no embed URL, no
	// probe, honest not-entitled phase — never a cross-tenant frame.
	code, body := callH(t, app, http.MethodGet, "/v1/embed-status?app=help",
		map[string]string{"X-User-Id": "alice", "X-Org-Id": "acme"}, "")
	if code != http.StatusOK {
		t.Fatalf("customer: want 200, got %d", code)
	}
	var r embedStatusResp
	mustJSON(t, body, &r)
	if r.Entitled || r.Reachable || r.EmbedURL != "" || r.Phase != "not-entitled" {
		t.Fatalf("customer must be not-entitled with no url: %+v", r)
	}
	if *calls != 0 {
		t.Fatalf("a non-entitled caller must NEVER be probed, got %d probes", *calls)
	}
}

func TestEmbedStatus_BrandDomainPerBrand(t *testing.T) {
	stubProbe(t, true)
	// lux apps live on lux.cloud (the app-hosting domain), NOT lux.network.
	app := mountBrand(t, "lux")
	code, body := callH(t, app, http.MethodGet, "/v1/embed-status?app=cms",
		map[string]string{"X-User-Id": "z", "X-Org-Id": "lux"}, "")
	if code != http.StatusOK {
		t.Fatalf("lux brand member: want 200, got %d", code)
	}
	var r embedStatusResp
	mustJSON(t, body, &r)
	if r.Origin != "https://cms.lux.cloud" {
		t.Fatalf("lux embed origin: want https://cms.lux.cloud, got %q", r.Origin)
	}
}

func TestEmbedUp_Classification(t *testing.T) {
	up := map[int]bool{200: true, 302: true, 401: true, 403: true, 404: false, 500: false, 502: false, 503: false, 0: false}
	for status, want := range up {
		if got := embedUp(status); got != want {
			t.Fatalf("embedUp(%d): want %v, got %v", status, want, got)
		}
	}
}

func TestEmbedBrandDomain_Fallback(t *testing.T) {
	if d := embedBrandDomain("zoo"); d != "zoo.cloud" {
		t.Fatalf("zoo domain: want zoo.cloud, got %q", d)
	}
	if d := embedBrandDomain("unknown-brand"); d != defaultEmbedDomain {
		t.Fatalf("unknown brand must fall back to %q, got %q", defaultEmbedDomain, d)
	}
}
