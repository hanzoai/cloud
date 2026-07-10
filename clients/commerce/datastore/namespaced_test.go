package datastore

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/db"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
)

// tprod is a minimal merchant-like entity used to prove per-org isolation of the
// namespaced datastore without importing a real model (which would import this
// package back). marshalForDB stores it as JSON in the generic _entities table.
type tprod struct {
	Id_  string `json:"id"`
	Name string `json:"name"`
}

// newTestManager builds a real db.Manager on a temp dir plus the shared "system"
// DB, and returns a cleanup. Vector search + ClickHouse are off for hermetic runs.
func newTestManager(t *testing.T) (*db.Manager, db.DB, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "commerce-nsroute-*")
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
	return mgr, sys, func() {
		mgr.Close()
		os.RemoveAll(dir)
	}
}

// saveGlobals snapshots the package-global default DB + resolver so a test can
// mutate them and restore afterwards (they are process-wide).
func saveGlobals(t *testing.T) {
	t.Helper()
	prevDB, prevResolver := defaultDB, orgDBResolver
	t.Cleanup(func() {
		defaultDB = prevDB
		orgDBResolver = prevResolver
	})
}

func ns(namespace string) context.Context {
	return nscontext.WithNamespace(context.Background(), namespace)
}

// TestNewNamespaced_Routing proves the routing decision: which physical store a
// namespaced datastore binds to. This is the merchant-model isolation seam.
func TestNewNamespaced_Routing(t *testing.T) {
	saveGlobals(t)
	mgr, sys, cleanup := newTestManager(t)
	defer cleanup()

	SetDefaultDB(sys)

	// No resolver installed (tests/dev bootstrap): a namespaced request still
	// gets the shared default DB — never a nil/broken store.
	SetOrgDBResolver(nil)
	if got := NewNamespaced(ns("org-a")).DB(); got != sys {
		t.Fatalf("no resolver: want default DB, got %v", got)
	}

	SetOrgDBResolver(mgr.Org)

	// Empty namespace (DefaultNamespace / global kinds like organization) → default DB.
	if got := NewNamespaced(context.Background()).DB(); got != sys {
		t.Fatalf("empty ns: want default DB, got %v", got)
	}

	// Distinct namespaces → distinct per-org DBs, none of which is the shared pool.
	a := NewNamespaced(ns("org-a")).DB()
	b := NewNamespaced(ns("org-b")).DB()
	if a == nil || b == nil {
		t.Fatalf("per-org routing returned nil: a=%v b=%v", a, b)
	}
	if a == b {
		t.Fatalf("org-a and org-b resolved to the SAME store — not isolated")
	}
	if a == sys || b == sys {
		t.Fatalf("per-org store must not be the shared default pool: a=%v b=%v sys=%v", a, b, sys)
	}

	// Unsafe namespace (path traversal / separators) → fail closed: NO database
	// bound. (Empty namespace is NOT unsafe — it is the global-kind path asserted
	// above, which resolves to the shared default DB.)
	for _, evil := range []string{"../evil", "a/b", "..", ".", `x\y`} {
		if got := NewNamespaced(ns(evil)).DB(); got != nil {
			t.Fatalf("unsafe ns %q: want nil DB (fail-closed), got %v", evil, got)
		}
	}
}

// TestHasOrgDBResolver_BootstrapFailClosedContract proves the signal the
// commerce Bootstrap uses to fail closed (Red CRIT-2). commerce.go calls
// SetOrgDBResolver(app.DB.Org) and then asserts HasOrgDBResolver(); if it is
// false the process refuses to start, so a boot that forgot to wire the per-org
// resolver can never silently run every tenant's money on the shared store.
func TestHasOrgDBResolver_BootstrapFailClosedContract(t *testing.T) {
	saveGlobals(t)

	// Unset ⇒ the fatal state Bootstrap must catch.
	SetOrgDBResolver(nil)
	if HasOrgDBResolver() {
		t.Fatal("HasOrgDBResolver()=true with no resolver installed — Bootstrap would NOT fail closed")
	}

	// Installed ⇒ the healthy state Bootstrap requires to proceed.
	mgr, sys, cleanup := newTestManager(t)
	defer cleanup()
	SetDefaultDB(sys)
	SetOrgDBResolver(mgr.Org)
	if !HasOrgDBResolver() {
		t.Fatal("HasOrgDBResolver()=false after SetOrgDBResolver — Bootstrap would refuse a correctly-wired boot")
	}
}

// TestNewNamespaced_PhysicalIsolation proves org-b can NEVER read a row org-a
// wrote — the core cross-tenant property. Isolation is physical (separate files).
func TestNewNamespaced_PhysicalIsolation(t *testing.T) {
	saveGlobals(t)
	mgr, sys, cleanup := newTestManager(t)
	defer cleanup()
	SetDefaultDB(sys)
	SetOrgDBResolver(mgr.Org)

	ctxA, ctxB := ns("org-a"), ns("org-b")

	// org-a writes a product into its OWN store.
	key, err := NewNamespaced(ctxA).Put("product", &tprod{Name: "widget"})
	if err != nil {
		t.Fatalf("org-a put: %v", err)
	}

	// org-a reads its own product back.
	var mine tprod
	if err := NewNamespaced(ctxA).Get(key, &mine); err != nil {
		t.Fatalf("org-a get own: %v", err)
	}
	if mine.Name != "widget" {
		t.Fatalf("org-a read wrong data: %+v", mine)
	}

	// org-b must NOT be able to read org-a's product by the very same key.
	var leaked tprod
	err = NewNamespaced(ctxB).Get(key, &leaked)
	if !errors.Is(err, ErrNoSuchEntity) {
		t.Fatalf("CROSS-TENANT LEAK: org-b read org-a's product: err=%v data=%+v", err, leaked)
	}
}

// TestNewNamespaced_FailClosed proves an unresolvable (unsafe) namespace yields a
// datastore with no DB, so every operation errors rather than silently reading or
// writing the shared pool.
func TestNewNamespaced_FailClosed(t *testing.T) {
	saveGlobals(t)
	mgr, sys, cleanup := newTestManager(t)
	defer cleanup()
	SetDefaultDB(sys)
	SetOrgDBResolver(mgr.Org)

	ds := NewNamespaced(ns("../evil"))
	if ds.DB() != nil {
		t.Fatalf("unsafe ns must bind no DB, got %v", ds.DB())
	}

	if _, err := ds.Put("product", &tprod{Name: "x"}); err == nil {
		t.Fatalf("fail-closed: Put on a no-DB datastore must error, got nil")
	}
	if err := ds.Get(ds.NewKey("product", "x", 0, nil), &tprod{}); err == nil {
		t.Fatalf("fail-closed: Get on a no-DB datastore must error, got nil")
	}
}
