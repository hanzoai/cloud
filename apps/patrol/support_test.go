package patrol

import (
	"net/http"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/apps/framework"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/zap-proto/zip"
)

// A SuperAdmin supporting a tenant (HIP-0527 §2): IAM's assume re-mints their
// token with the tenant in its signed `orgs`, and the identity boundary renders
// that as X-User-IsAdmin, X-User-IsOrgAdmin and the tenant in X-User-Orgs. The
// roles op decides on the two bits (apps/iam onecheck_test.go); these tests hold
// patrol to carrying them across the socket and to refusing a SuperAdmin whose
// token does not name the org.

const op = "op@admin.test"

func mountSupport(t *testing.T) (*zip.App, func() zip.Caller) {
	t.Helper()
	var mu sync.Mutex
	var seen zip.Caller
	app := mountWith(t, func() {
		planetest.Asked(t, func(who zip.Caller) client.Roles {
			if who.User == op {
				mu.Lock()
				seen = who
				mu.Unlock()
				if who.Admin && who.OrgAdmin {
					return client.Roles{Roles: []string{framework.RoleSystemManager}, Admin: true}
				}
				return client.Roles{}
			}
			return client.Roles{Roles: grants[who.User]}
		})
	})
	install(t, app, org)
	seedSite(t, app, org, "depot", "4401", "")
	return app, func() zip.Caller { mu.Lock(); defer mu.Unlock(); return seen }
}

func TestASuperAdminAssumedIntoTheOrgReadsItsEstate(t *testing.T) {
	app, seen := mountSupport(t)
	code, raw := hit(t, app, http.MethodGet, "/v1/patrol/snapshot", nil, map[string]string{
		"X-Org-Id": org, "X-User-Id": op, "X-User-IsAdmin": "true", "X-User-IsOrgAdmin": "true",
		"X-User-Orgs": "admin," + org,
	})
	if code != http.StatusOK {
		t.Fatalf("a SuperAdmin assumed into %s reading the snapshot want 200, got %d (%s)", org, code, first(raw, 300))
	}
	var out patrolEstate
	data(t, raw, &out)
	if len(out.Sites) != 1 {
		t.Fatalf("the support session read %d sites, want the one seeded", len(out.Sites))
	}
	if who := seen(); !who.Admin || !who.OrgAdmin || who.Org != org {
		t.Fatalf("IAM was asked with %+v; the boundary's two bits and the acting org must reach it", who)
	}
}

func TestASuperAdminWhoDidNotAssumeIsRefused(t *testing.T) {
	app, _ := mountSupport(t)
	code, raw := hit(t, app, http.MethodGet, "/v1/patrol/snapshot", nil, map[string]string{
		"X-Org-Id": org, "X-User-Id": op, "X-User-IsAdmin": "true", "X-User-Orgs": "admin",
	})
	if code != http.StatusForbidden {
		t.Fatalf("a SuperAdmin whose token does not name %s want 403, got %d (%s)", org, code, first(raw, 300))
	}
}

// The framework surface holds patrol's documents, so it asks the same one
// check: a SuperAdmin who only named the org reads and writes nothing there, and
// one assumed into it is its System Manager.
func TestTheFrameworkSurfaceAsksTheSameOneCheck(t *testing.T) {
	app, _ := mountSupport(t)
	header := map[string]string{"X-Org-Id": org, "X-User-Id": op, "X-User-IsAdmin": "true", "X-User-Orgs": "admin"}
	if code, raw := hit(t, app, http.MethodGet, "/v1/framework/"+DTSite.String(), nil, header); code != http.StatusForbidden {
		t.Fatalf("a header-only SuperAdmin listing %s want 403, got %d (%s)", DTSite, code, first(raw, 300))
	}
	body := map[string]any{"name": "intruder", "account": "1", "tier": "gold", "sla": 20, "lat": 1.0, "lon": 1.0, "status": SiteOK}
	if code, raw := hit(t, app, http.MethodPost, "/v1/framework/"+DTSite.String(), body, header); code != http.StatusForbidden {
		t.Fatalf("a header-only SuperAdmin writing %s want 403, got %d (%s)", DTSite, code, first(raw, 300))
	}
	assumed := map[string]string{"X-Org-Id": org, "X-User-Id": op, "X-User-IsAdmin": "true", "X-User-IsOrgAdmin": "true", "X-User-Orgs": "admin," + org}
	if code, raw := hit(t, app, http.MethodGet, "/v1/framework/"+DTSite.String(), nil, assumed); code != http.StatusOK {
		t.Fatalf("a SuperAdmin assumed into %s listing %s want 200, got %d (%s)", org, DTSite, code, first(raw, 300))
	}
}
