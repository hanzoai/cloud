// Copyright © 2026 Hanzo AI. MIT License.

package store

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/datastore/query"
	"github.com/hanzoai/cloud/clients/commerce/db"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	commercestore "github.com/hanzoai/cloud/clients/commerce/models/store"
)

// withResolver installs the SAME per-org DB resolver the production commerce
// bootstrap installs (commerce.go: datastore.SetOrgDBResolver(app.DB.Org)), so
// datastore.NewNamespaced routes each org to its OWN physical store — the wiring
// the getCurrent fix depends on. Mirrors clients/commerce/test/perorg.
func withResolver(t *testing.T) func() {
	t.Helper()
	dir, err := os.MkdirTemp("", "storecurrent-*")
	if err != nil {
		t.Fatal(err)
	}
	cfg := db.DefaultConfig()
	cfg.DataDir = dir
	cfg.EnableVectorSearch = false
	cfg.EnableDatastore = false
	mgr, err := db.NewManager(cfg)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatalf("NewManager: %v", err)
	}
	sys, err := mgr.Org("system")
	if err != nil {
		mgr.Close()
		os.RemoveAll(dir)
		t.Fatalf("Org(system): %v", err)
	}
	datastore.SetDefaultDB(sys)
	query.SetDefaultDB(sys)
	datastore.SetOrgDBResolver(mgr.Org)

	return func() {
		datastore.SetOrgDBResolver(nil)
		datastore.SetDefaultDB(nil)
		query.SetDefaultDB(nil)
		mgr.Close()
		os.RemoveAll(dir)
	}
}

type storeResp struct {
	Store struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
	} `json:"store"`
}

// callCurrent drives the real getCurrent handler with the given org bound in
// context exactly as middleware.TokenRequired does (c.Set("organization", …)),
// and returns the decoded response. An empty org omits the binding, exercising
// the no-identity fallback.
func callCurrent(t *testing.T, org string) storeResp {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/store/current", nil)
	if org != "" {
		c.Set("organization", &organization.Organization{Name: org})
	}

	getCurrent(c)

	if w.Code != http.StatusOK {
		t.Fatalf("org %q: getCurrent status = %d, want 200", org, w.Code)
	}
	var resp storeResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("org %q: decode body %q: %v", org, w.Body.String(), err)
	}
	return resp
}

// TestGetCurrentProvisionsOrgScopedStore is the round-trip proof for the fix: an
// authenticated org's GET /v1/store/current resolves (and lazily provisions) a
// REAL org-scoped store id — never the phantom shared "default" — the store id
// the content storefront edge needs to publish product imagery.
func TestGetCurrentProvisionsOrgScopedStore(t *testing.T) {
	defer withResolver(t)()

	// First visit for an org with no store yet → a real store id is provisioned.
	karma := callCurrent(t, "karma")
	if karma.Store.ID == "" || karma.Store.ID == "default" {
		t.Fatalf("karma store id = %q, want a real provisioned id (not empty/\"default\")", karma.Store.ID)
	}
	if karma.Store.Slug != commercestore.DefaultSlug {
		t.Fatalf("karma store slug = %q, want %q", karma.Store.Slug, commercestore.DefaultSlug)
	}

	// Idempotent: a second visit returns the SAME store, not a duplicate.
	karma2 := callCurrent(t, "karma")
	if karma2.Store.ID != karma.Store.ID {
		t.Fatalf("second GET /store/current provisioned a new store: %q != %q", karma2.Store.ID, karma.Store.ID)
	}

	// Org isolation: a different org resolves its OWN store, never karma's.
	other := callCurrent(t, "acme")
	if other.Store.ID == "" || other.Store.ID == "default" {
		t.Fatalf("acme store id = %q, want a real provisioned id", other.Store.ID)
	}
	if other.Store.ID == karma.Store.ID {
		t.Fatalf("CROSS-TENANT LEAK: acme resolved karma's store id %q", karma.Store.ID)
	}
}

// TestGetCurrentNoOrgFallsBackToDefault proves the endpoint degrades cleanly (no
// panic, no 500) when reached with no org in context — returning the minimal
// default payload rather than provisioning against a missing tenant.
func TestGetCurrentNoOrgFallsBackToDefault(t *testing.T) {
	defer withResolver(t)()

	resp := callCurrent(t, "")
	if resp.Store.ID != "default" {
		t.Fatalf("no-org store id = %q, want \"default\"", resp.Store.ID)
	}
}

// TestEnsureDefaultIsIdempotent proves the canonical provisioning primitive is
// idempotent at the datastore layer: repeated calls in one org's namespace return
// the SAME store, and a second org gets a distinct one.
func TestEnsureDefaultIsIdempotent(t *testing.T) {
	defer withResolver(t)()

	ctxA := (&organization.Organization{Name: "org-a"}).Namespaced(context.Background())
	a1, err := commercestore.EnsureDefault(datastore.NewNamespaced(ctxA))
	if err != nil {
		t.Fatalf("EnsureDefault org-a: %v", err)
	}
	a2, err := commercestore.EnsureDefault(datastore.NewNamespaced(ctxA))
	if err != nil {
		t.Fatalf("EnsureDefault org-a (2): %v", err)
	}
	if a1.Id() == "" || a1.Id() != a2.Id() {
		t.Fatalf("EnsureDefault not idempotent: %q vs %q", a1.Id(), a2.Id())
	}

	ctxB := (&organization.Organization{Name: "org-b"}).Namespaced(context.Background())
	b1, err := commercestore.EnsureDefault(datastore.NewNamespaced(ctxB))
	if err != nil {
		t.Fatalf("EnsureDefault org-b: %v", err)
	}
	if b1.Id() == a1.Id() {
		t.Fatalf("CROSS-TENANT: org-b store id == org-a store id (%q)", a1.Id())
	}
}
