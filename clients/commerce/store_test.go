// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/db"
	"github.com/hanzoai/cloud/clients/commerce/pkg/auth"
)

// testHandler runs fn with the request context. Used by middleware tests
// so we don't bring in a Gin router for plain context-binding checks.
func testHandler(fn func(context.Context)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fn(r.Context())
		if w != nil {
			w.WriteHeader(http.StatusOK)
		}
	})
}

func newTestReq(org string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/commerce/health", nil)
	r.Header.Set(auth.HeaderOrgID, org)
	return r
}

func newTestManager(t *testing.T) *db.Manager {
	t.Helper()
	tmp := t.TempDir()
	mgr, err := db.NewManager(&db.Config{
		DataDir:            tmp,
		UserDataDir:        filepath.Join(tmp, "users"),
		OrgDataDir:         filepath.Join(tmp, "orgs"),
		EnableVectorSearch: false,
		IsDev:              true,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr
}

func TestStoreWithOrgIsolatesShards(t *testing.T) {
	mgr := newTestManager(t)
	s := NewStore(mgr)

	a := s.WithOrg("hanzo")
	b := s.WithOrg("lux")
	if a.Org() == b.Org() {
		t.Fatalf("expected distinct org bindings")
	}

	dba, err := a.DB()
	if err != nil {
		t.Fatalf("a.DB: %v", err)
	}
	dbb, err := b.DB()
	if err != nil {
		t.Fatalf("b.DB: %v", err)
	}
	if dba == dbb {
		t.Fatalf("hanzo and lux must NOT share the same *db.DB handle")
	}

	// Same org → same handle (manager memoizes).
	a2 := s.WithOrg("hanzo")
	dba2, err := a2.DB()
	if err != nil {
		t.Fatalf("a2.DB: %v", err)
	}
	if dba != dba2 {
		t.Fatalf("same-org calls must share the cached handle")
	}
}

func TestStoreFromContextHonorsHeader(t *testing.T) {
	mgr := newTestManager(t)
	s := NewStore(mgr)

	ctx := context.Background()
	if got := s.FromContext(ctx).Org(); got != "system" {
		t.Fatalf("empty ctx → system fallback, got %q", got)
	}

	// Simulate the middleware binding by going through the public API.
	mw := auth.RequireIdentity(false)
	var gotOrg string
	w := httptest.NewRecorder()
	mw(testHandler(func(c context.Context) { gotOrg = s.FromContext(c).Org() })).ServeHTTP(w, newTestReq("hanzo"))
	if gotOrg != "hanzo" {
		t.Fatalf("ctx-bound org: want hanzo got %q", gotOrg)
	}
}

func TestStoreWithEmptyOrgDefaultsToSystem(t *testing.T) {
	mgr := newTestManager(t)
	s := NewStore(mgr).WithOrg("")
	if s.Org() != "system" {
		t.Fatalf("empty WithOrg → system, got %q", s.Org())
	}
}

func TestStoreNilSafe(t *testing.T) {
	var s *Store
	if s.WithOrg("x") != nil {
		t.Fatalf("nil receiver WithOrg should stay nil")
	}
	if got := s.Org(); got != "" {
		t.Fatalf("nil receiver Org → \"\", got %q", got)
	}
}
