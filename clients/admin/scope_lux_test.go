package admin

// Brand-named tenant-scope NEGATIVE tests for admin.<brand> (the operator cockpit).
//
// These make the generic escalation-line invariant concrete for the real brands the
// ONE shared cockpit binary serves: an ADMITTED Lux white-label tenant admin
// (z@lux.network, org=lux — the identity IAM mints on admin.lux.cloud) is HARD-PINNED to
// the Lux subtree and can NEVER reach another brand's data (Zoo, Hanzo) or a fleet
// DigitalOcean god-view. A cross-brand read here would break white-label isolation — the
// exact thing admin.lux.cloud must guarantee for every tenant.
//
// Lux is ENABLED as a white-label tenant in each test (s.State.WLTenants), so the caller
// is ADMITTED past GuardScoped — the denials below are the SCOPE CEILING (data hard-
// limited to the caller's own org), not an admission refusal. Admission-refusal for a
// NON-enabled org is covered by TestScope_NonWhiteLabelOrgAdminDenied.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// luxAdminHdr is a validated Lux-tenant org admin: a pinned own-org (lux) + the
// unforgeable X-User-IsOrgAdmin bit SanitizeIdentity mints for a validated isAdmin
// principal, but NO global X-User-IsAdmin (SuperAdmin) flag — exactly what the boundary
// presents for z@lux.network signing into the cockpit at admin.lux.cloud.
var luxAdminHdr = map[string]string{
	"X-Org-Id": "lux", "X-User-Id": "lux/z", "X-User-Email": "z@lux.network", "X-User-IsOrgAdmin": "true",
}

// TestScope_LuxAdminCannotReachZooOrgs proves the cross-brand escalation line: a Lux
// tenant admin who explicitly asks for Zoo's org data (?org=zoo) is hard-pinned to their
// OWN subtree — the response is EXACTLY [lux], never zoo. The client-supplied ?org= is
// ignored for a non-super caller; the scope is the sanitized org.
func TestScope_LuxAdminCannotReachZooOrgs(t *testing.T) {
	iam := newScopeIAM()
	defer iam.server.Close()
	commerce := newFakeCommerce()
	defer commerce.server.Close()
	do, s, _ := mountService(t, iam.server.URL, commerce.server.URL, "")
	s.State.WLTenants = map[string]bool{"lux": true} // Lux ENABLED → admitted, then scoped.

	resp, body := do("GET", "/v1/admin/orgs?org=zoo", luxAdminHdr)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admitted Lux WL-tenant admin must reach the scoped orgs panel, got %d (%s)", resp.StatusCode, body)
	}
	var env struct {
		Data []orgRow `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data) != 1 || env.Data[0].Org != "lux" {
		t.Fatalf("Lux admin must see ONLY lux, got %+v — cross-brand leak into Zoo (?org=zoo honored)!", env.Data)
	}
}

// TestScope_LuxAdminCannotReachHanzoUsers proves the users read is HARD-PINNED: a Lux
// admin listing "hanzo" users drives the IAM query with owner=lux, never hanzo. The pin
// is the sanitized org; the ?org= is never trusted for a non-super caller.
func TestScope_LuxAdminCannotReachHanzoUsers(t *testing.T) {
	iam := newScopeIAM()
	defer iam.server.Close()
	commerce := newFakeCommerce()
	defer commerce.server.Close()
	do, s, _ := mountService(t, iam.server.URL, commerce.server.URL, "")
	s.State.WLTenants = map[string]bool{"lux": true}

	if resp, body := do("GET", "/v1/admin/users?org=hanzo", luxAdminHdr); resp.StatusCode != http.StatusOK {
		t.Fatalf("admitted Lux WL-tenant admin must reach the scoped users panel, got %d (%s)", resp.StatusCode, body)
	}
	iam.mu.Lock()
	owner := iam.lastUsersOwner
	iam.mu.Unlock()
	if owner != "lux" {
		t.Fatalf("Lux admin users read NOT hard-pinned: IAM saw owner=%q, want lux — cross-brand escalation into Hanzo!", owner)
	}
}

// TestScope_LuxAdminCannotReachDO proves the fleet ceiling: the DigitalOcean god-views —
// compute (fleet infra) and finance (fleet DO billing) — are SuperAdmin-only (core.Guard).
// An admitted Lux WL tenant is 403 on BOTH: a white-label tenant reads its own subtree but
// NEVER a fleet DO resource. The gate refuses BEFORE the handler, so no DO call is made
// (which also keeps this test free of any upstream I/O). That a SuperAdmin DOES pass these
// gates is proven by TestGate_AllowsSuperAdmin + TestScope_SuperSeesAllOrgs.
func TestScope_LuxAdminCannotReachDO(t *testing.T) {
	do, s, _ := mountService(t, "http://127.0.0.1:0", "http://127.0.0.1:0", "")
	s.State.WLTenants = map[string]bool{"lux": true} // even an ENABLED WL tenant is denied the DO god-views.

	for _, path := range []string{"/v1/admin/compute", "/v1/admin/finance"} {
		if resp, body := do("GET", path, luxAdminHdr); resp.StatusCode != http.StatusForbidden {
			t.Errorf("Lux WL-tenant admin GET %s: got %d, want 403 — a WL tenant must never reach a fleet DO god-view (body=%s)",
				path, resp.StatusCode, body)
		}
	}
}

// TestScope_LuxAdminSpendCapWriteHardPinned pins the highest-value cross-tenant vector — a
// STATE-CHANGING write. A Lux admin who tries to set a spend cap on Zoo (POST
// /v1/admin/spend-caps?org=zoo) must have the write hard-pinned to owner=lux downstream:
// the ?org= is ignored for a non-super caller (targetOrg → sc.Orgs[0]). We record the
// X-Org-Id commerce actually receives and assert it is lux, never zoo. The read-path pins
// are covered above; this closes the write path.
func TestScope_LuxAdminSpendCapWriteHardPinned(t *testing.T) {
	var mu sync.Mutex
	var wroteOrg string
	commerce := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/v1/billing/spend-alerts") && r.Method == http.MethodPost {
			mu.Lock()
			wroteOrg = r.Header.Get("X-Org-Id") // commerce.Forward pins the target org here
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"ok"}`)
	}))
	defer commerce.Close()

	do, s, _ := mountService(t, "http://127.0.0.1:0", commerce.URL, "")
	s.State.WLTenants = map[string]bool{"lux": true}

	resp, body := do("POST", "/v1/admin/spend-caps?org=zoo", luxAdminHdr)
	if resp.StatusCode == http.StatusForbidden {
		t.Fatalf("admitted Lux WL admin must reach the scoped spend-caps WRITE, got 403 (%s)", body)
	}
	mu.Lock()
	org := wroteOrg
	mu.Unlock()
	if org != "lux" {
		t.Fatalf("Lux admin spend-cap WRITE targeted org=%q, want lux — cross-brand WRITE into Zoo (?org=zoo honored)!", org)
	}
}
