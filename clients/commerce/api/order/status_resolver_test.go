// Copyright © 2026 Hanzo AI. MIT License.

package order

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	dsquery "github.com/hanzoai/cloud/clients/commerce/datastore/query"
	"github.com/hanzoai/cloud/clients/commerce/db"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
)

// TestStatus_ResolverInstalled_FindsPerOrgOrder is the Red MED-1 regression
// guard. It is the ONLY kind of test that can catch a datastore.New /
// NewNamespaced split, because the standard ae harness leaves orgDBResolver nil
// — with no resolver, NewNamespaced falls back to systemDB and is
// indistinguishable from New, so every other test in the suite passes whether a
// handler calls New or NewNamespaced.
//
// Here we install a REAL per-org resolver (as production does via
// datastore.SetOrgDBResolver(app.DB.Org)). Now New(ctx) binds the shared
// systemDB while NewNamespaced(ns) binds the caller org's OWN store — they
// diverge. We seed an order into org "acme"'s store via NewNamespaced (exactly
// how order creation writes it) and assert:
//
//  1. the Status handler (which we routed through NewNamespaced) FINDS it → 200
//     with the right id. Had Status still used datastore.New, it would query
//     systemDB, miss the per-org order, and return 404.
//  2. the same order is genuinely ABSENT from systemDB (New) — proving the split
//     is real and the fix is load-bearing, not cosmetic.
func TestStatus_ResolverInstalled_FindsPerOrgOrder(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Real manager with per-org SQLite + the resolver INSTALLED (unlike ae).
	dir := t.TempDir()
	cfg := db.DefaultConfig()
	cfg.DataDir = dir
	cfg.UserDataDir = filepath.Join(dir, "users")
	cfg.OrgDataDir = filepath.Join(dir, "orgs")
	cfg.EnableDatastore = false // SQLite only
	cfg.EnableVectorSearch = false

	mgr, err := db.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	sys, err := mgr.Org("system")
	if err != nil {
		t.Fatalf("Org(system): %v", err)
	}

	datastore.SetDefaultDB(sys)
	dsquery.SetDefaultDB(sys)
	datastore.SetOrgDBResolver(mgr.Org)
	t.Cleanup(func() {
		// Restore the "no resolver" state the rest of the suite expects, then drop
		// the manager so a dangling resolver can't point at a closed store.
		datastore.SetOrgDBResolver(nil)
		mgr.Close()
	})

	const ns = "acme"

	// Seed an order into acme's OWN store, exactly as order creation does
	// (NewNamespaced → per-org store).
	seedDB := datastore.NewNamespaced(nscontext.WithNamespace(context.Background(), ns))
	ord := order.New(seedDB)
	ord.Total = 5000
	ord.Paid = 5000
	if err := ord.Create(); err != nil {
		t.Fatalf("seed per-org order: %v", err)
	}
	orderId := ord.Id()
	if orderId == "" {
		t.Fatal("seeded order has empty id")
	}

	// Guard #2: the order must NOT exist in systemDB (datastore.New). If this ever
	// fails, New and NewNamespaced stopped diverging and the regression guard is
	// toothless — fix the harness, not this assertion.
	sysMiss := order.New(datastore.New(context.Background()))
	if err := sysMiss.GetById(orderId); err == nil {
		t.Fatal("order unexpectedly found in systemDB — resolver split not in effect; test cannot catch MED-1")
	}

	// Drive the real Status handler with an acme-scoped request.
	org := &organization.Organization{}
	org.Name = ns

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("organization", org)
	c.Params = gin.Params{{Key: "orderid", Value: orderId}}

	Status(c)

	// Guard #1: handler routed to acme's store → found → 200 with the right id.
	if w.Code != 200 {
		t.Fatalf("Status returned %d, want 200 — handler missed the per-org order (MED-1 regression: reading systemDB, not org store). body=%s", w.Code, w.Body.String())
	}
	var res StatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode StatusResponse: %v (body=%s)", err, w.Body.String())
	}
	if res.Id != orderId {
		t.Fatalf("Status returned order id %q, want %q", res.Id, orderId)
	}
}
