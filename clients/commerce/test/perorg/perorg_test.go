// Package perorg is an end-to-end proof that the generic REST merchant model
// path (mixin.Model[T] over the commerce datastore) physically isolates every
// org's rows once the per-org DB resolver is installed — the production wiring.
//
// It uses the real product.Product (a mixin.Model[T], the same type served by
// the /v1/product REST routes) and drives it exactly as util/rest.newEntity
// does: datastore.NewNamespaced(ctx). It does NOT use the shared ginkgo suite
// context, so writes and reads route through the SAME per-org resolver and the
// test faithfully exercises the deployed path (unlike the shared-DB suite, which
// proves only namespace-column isolation).
package perorg

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/datastore/query"
	"github.com/hanzoai/cloud/clients/commerce/db"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
)

func withResolver(t *testing.T) (*db.Manager, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "perorg-*")
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

	return mgr, func() {
		datastore.SetOrgDBResolver(nil)
		datastore.SetDefaultDB(nil)
		query.SetDefaultDB(nil)
		mgr.Close()
		os.RemoveAll(dir)
	}
}

func nsCtx(namespace string) context.Context {
	return nscontext.WithNamespace(context.Background(), namespace)
}

// TestProductPerOrgIsolation is the merchant-surface isolation bar in a unit
// test: a product written by org-a is invisible to org-b on BOTH get-by-id and
// list, and org-a sees only its own. Proves the fix closes the cross-tenant leak
// for a real mixin.Model[T] end to end (write + read through the per-org store).
func TestProductPerOrgIsolation(t *testing.T) {
	_, cleanup := withResolver(t)
	defer cleanup()

	ctxA, ctxB := nsCtx("org-a"), nsCtx("org-b")

	// org-a creates a product in its OWN store (as POST /v1/product would).
	pa := product.New(datastore.NewNamespaced(ctxA))
	pa.Name = "org-a-widget"
	if err := pa.Create(); err != nil {
		t.Fatalf("org-a create: %v", err)
	}
	id := pa.Id()
	if id == "" {
		t.Fatal("org-a product has empty id")
	}

	// org-a GET /v1/product/{id} → finds its own product.
	own := product.New(datastore.NewNamespaced(ctxA))
	if err := own.GetById(id); err != nil {
		t.Fatalf("org-a GetById own product: %v", err)
	}
	if own.Name != "org-a-widget" {
		t.Fatalf("org-a read wrong product: %+v", own)
	}

	// org-b GET /v1/product/{id} → MUST NOT leak org-a's product (the pre-existing
	// leak: on single-tenant Postgres this returned 200 with org-a's row).
	leak := product.New(datastore.NewNamespaced(ctxB))
	err := leak.GetById(id)
	if !errors.Is(err, datastore.ErrNoSuchEntity) {
		t.Fatalf("CROSS-TENANT GET LEAK: org-b GetById(%s) err=%v name=%q", id, err, leak.Name)
	}

	// org-b GET /v1/product (list) → MUST be empty of org-a's product.
	var bList []*product.Product
	if _, err := product.New(datastore.NewNamespaced(ctxB)).Query().All().GetAll(&bList); err != nil {
		t.Fatalf("org-b list: %v", err)
	}
	for _, p := range bList {
		if p.Id_ == id || p.Name == "org-a-widget" {
			t.Fatalf("CROSS-TENANT LIST LEAK: org-b list contains org-a product %+v", p)
		}
	}

	// org-a GET /v1/product (list) → includes its own product.
	var aList []*product.Product
	if _, err := product.New(datastore.NewNamespaced(ctxA)).Query().All().GetAll(&aList); err != nil {
		t.Fatalf("org-a list: %v", err)
	}
	found := false
	for _, p := range aList {
		if p.Id_ == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("org-a list is missing its own product %s (reads must hit the SAME per-org store as writes)", id)
	}
}
