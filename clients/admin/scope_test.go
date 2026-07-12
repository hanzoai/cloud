package admin

// Tests for the TWO-SCOPE predicate (scope.go) — the security core of the two-tier
// cockpit. They prove the ONE invariant that must never break: a SuperAdmin is
// cross-tenant, and a non-super caller is HARD-limited to their own org — a scoped caller
// can NEVER read another tenant, for ANY input (the escalation line).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
)

// scopeIAM is a fake IAM serving the org directory + single-org + users reads. It RECORDS
// the owner query param it last saw on get-users, so a test can prove a scoped caller's
// read is hard-pinned to their own org (never a client-chosen ?org=).
type scopeIAM struct {
	server         *httptest.Server
	mu             sync.Mutex
	lastUsersOwner string
}

func newScopeIAM() *scopeIAM {
	f := &scopeIAM{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/get-organizations"):
			io.WriteString(w, `{"status":"ok","msg":"","data":[
				{"owner":"admin","name":"hanzo","displayName":"Hanzo","createdTime":"2020-01-01T00:00:00Z"},
				{"owner":"admin","name":"maxpower","displayName":"MaxPower","createdTime":"2021-02-02T00:00:00Z"}
			],"data2":2}`)
		case strings.HasSuffix(r.URL.Path, "/get-organization"):
			id := r.URL.Query().Get("id") // owner/name
			name := id
			if i := strings.LastIndex(id, "/"); i >= 0 {
				name = id[i+1:]
			}
			fmt.Fprintf(w, `{"status":"ok","msg":"","data":{"owner":"admin","name":%q,"displayName":%q,"createdTime":"2021-02-02T00:00:00Z"}}`, name, name)
		case strings.HasSuffix(r.URL.Path, "/get-users"):
			f.mu.Lock()
			f.lastUsersOwner = r.URL.Query().Get("owner")
			f.mu.Unlock()
			io.WriteString(w, `{"status":"ok","msg":"","data":[
				{"owner":"maxpower","name":"dave","email":"dave@maxpower.test","displayName":"Dave","isAdmin":true}
			],"data2":3}`)
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"status":"error","msg":"not found"}`)
		}
	}))
	return f
}

// superHdr / orgAdminHdr are the two tiers' SANITIZED identities (as SanitizeIdentity
// would mint them): a SuperAdmin carries X-User-IsAdmin=true; an org admin carries a
// validated X-User-Id + their pinned X-Org-Id + the X-User-IsOrgAdmin bit the boundary
// mints for any validated isAdmin principal, but NO GLOBAL admin flag. memberHdr is a
// validated but NON-admin member of an org: same identity MINUS the org-admin bit — the
// caller the over-visibility gap used to admit, now refused.
var superHdr = map[string]string{"X-User-IsAdmin": "true", "X-Org-Id": "admin", "X-User-Id": "admin/z"}
var orgAdminHdr = map[string]string{"X-Org-Id": "maxpower", "X-User-Id": "maxpower/dave", "X-User-Email": "dave@maxpower.test", "X-User-IsOrgAdmin": "true"}
var memberHdr = map[string]string{"X-Org-Id": "maxpower", "X-User-Id": "maxpower/eve", "X-User-Email": "eve@maxpower.test"}

func TestScope_SuperSeesAllOrgs(t *testing.T) {
	iam := newScopeIAM()
	defer iam.server.Close()
	commerce := newFakeCommerce()
	defer commerce.server.Close()
	do := mount(t, iam.server.URL, commerce.server.URL, "")

	resp, body := do("GET", "/v1/admin/orgs", superHdr)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("super orgs: %d (%s)", resp.StatusCode, body)
	}
	var env struct {
		Data []orgRow `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data) != 2 {
		t.Fatalf("SuperAdmin must see EVERY org (2), got %d: %+v", len(env.Data), env.Data)
	}
}

func TestScope_OrgAdminSeesOnlyOwnOrg(t *testing.T) {
	iam := newScopeIAM()
	defer iam.server.Close()
	commerce := newFakeCommerce()
	defer commerce.server.Close()
	do := mount(t, iam.server.URL, commerce.server.URL, "")

	// dave asks for hanzo, but must see ONLY maxpower — the escalation line.
	resp, body := do("GET", "/v1/admin/orgs?org=hanzo", orgAdminHdr)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("org-admin orgs: %d (%s)", resp.StatusCode, body)
	}
	var env struct {
		Data []orgRow `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data) != 1 || env.Data[0].Org != "maxpower" {
		t.Fatalf("org admin must see ONLY maxpower, got %+v (cross-tenant leak!)", env.Data)
	}
}

func TestScope_UsersHardPinnedToOwnOrg(t *testing.T) {
	iam := newScopeIAM()
	defer iam.server.Close()
	commerce := newFakeCommerce()
	defer commerce.server.Close()
	do := mount(t, iam.server.URL, commerce.server.URL, "")

	// dave tries to list hanzo's users; the read MUST be pinned to maxpower.
	if resp, body := do("GET", "/v1/admin/users?org=hanzo", orgAdminHdr); resp.StatusCode != http.StatusOK {
		t.Fatalf("org-admin users: %d (%s)", resp.StatusCode, body)
	}
	iam.mu.Lock()
	owner := iam.lastUsersOwner
	iam.mu.Unlock()
	if owner != "maxpower" {
		t.Fatalf("org-admin users read was NOT hard-pinned: IAM saw owner=%q, want maxpower (cross-tenant escalation!)", owner)
	}
}

func TestScope_AnalyticsScopedToOwnOrg(t *testing.T) {
	iam := newScopeIAM()
	defer iam.server.Close()
	commerce := newFakeCommerce()
	defer commerce.server.Close()
	do := mount(t, iam.server.URL, commerce.server.URL, "")

	resp, body := do("GET", "/v1/admin/analytics", orgAdminHdr)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("org-admin analytics: %d (%s)", resp.StatusCode, body)
	}
	var env struct {
		Data analyticsData `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.TotalCustomers != 1 {
		t.Fatalf("org-admin analytics TotalCustomers = %d, want 1 (their own org only)", env.Data.TotalCustomers)
	}
}

func TestScope_PlatformRouteDeniesOrgAdminButScopedAdmits(t *testing.T) {
	do := mount(t, "http://127.0.0.1:0", "http://127.0.0.1:0", "")

	// flags is platform sudo — an org admin is 403.
	if resp, _ := do("GET", "/v1/admin/flags", orgAdminHdr); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("org admin must be 403 on platform route /v1/admin/flags, got %d", resp.StatusCode)
	}
	// me is org-scoped — the same org admin is admitted (200), and sees a scoped identity.
	resp, body := do("GET", "/v1/admin/me", orgAdminHdr)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("org admin must be admitted on scoped /v1/admin/me, got %d", resp.StatusCode)
	}
	var env struct {
		Data adminMe `json:"data"`
	}
	_ = json.Unmarshal(body, &env)
	if env.Data.IsGlobalAdmin {
		t.Fatalf("org admin me.IsGlobalAdmin must be false")
	}
	if env.Data.Owner != "maxpower" {
		t.Fatalf("org admin me.Owner = %q, want maxpower", env.Data.Owner)
	}
}

// TestScope_MemberWithoutOrgAdminDenied closes the same-tenant over-visibility gap: a
// VALIDATED member of an org that is NOT an org admin (a real X-User-Id + pinned X-Org-Id
// but NO X-User-IsOrgAdmin) must be 403 on EVERY org-scoped panel — not just hard-scoped,
// REFUSED. Before the fix GuardScoped admitted any validated org member; it now requires
// the unforgeable org-admin bit, so a plain member can never reach their org's admin data.
func TestScope_MemberWithoutOrgAdminDenied(t *testing.T) {
	do := mount(t, "http://127.0.0.1:0", "http://127.0.0.1:0", "")
	for _, r := range scopedAdminRoutes {
		if resp, body := do(r.method, r.path, memberHdr); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s [validated non-admin member]: got %d, want 403 — over-visibility gap open (body=%s)",
				r.method, r.path, resp.StatusCode, body)
		}
	}
}

// TestScope_DescendantsSingletonToday pins the honest RECURSION-SEAM gap: IAM has no
// parent-org field yet, so the subtree is the singleton. When IAM adds the parent link,
// this test changes (and descendants becomes the BFS) — nothing else does.
func TestScope_DescendantsSingletonToday(t *testing.T) {
	s := &cloud.Service[core.State]{State: core.State{AdminOrg: "admin"}}
	got := core.Descendants(s, "maxpower")
	if len(got) != 1 || got[0] != "maxpower" {
		t.Fatalf("descendants(maxpower) = %v, want [maxpower] (IAM has no parent-org field yet)", got)
	}
	if core.Descendants(s, "") != nil {
		t.Fatalf("descendants(\"\") must be nil")
	}
}

func TestScope_ScopedToOrg(t *testing.T) {
	super := core.TenantScope{Super: true}
	if !super.ScopedToOrg("anything") {
		t.Fatal("super admits any org")
	}
	scoped := core.TenantScope{Orgs: []string{"maxpower"}}
	if !scoped.ScopedToOrg("maxpower") {
		t.Fatal("scoped admits its own org")
	}
	if scoped.ScopedToOrg("hanzo") {
		t.Fatal("scoped must NOT admit another tenant's org")
	}
	if scoped.ScopedToOrg("") {
		t.Fatal("scoped must NOT admit an empty org")
	}
}
