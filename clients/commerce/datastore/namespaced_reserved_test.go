// Copyright © 2026 Hanzo AI. MIT License.

package datastore

import "testing"

// TestNewNamespaced_ReservedNamespace_FailClosed is the Red LOW-1 guard. An
// untrusted tenant request carries its namespace from the verified JWT owner /
// X-Org-Id. If that value were ever "system", the resolver (app.DB.Org) would
// hand back the SHARED SQLite-fallback systemDB (Manager.Org("system")),
// silently pooling that tenant's money into the system store; "admin"/"default"
// are likewise reserved control namespaces. NewNamespaced must reject them —
// fail closed, no DB bound — BEFORE calling the resolver, so the check cannot be
// bypassed. The rejection is case-insensitive so a case-normalizing layer can't
// smuggle "System"/"ADMIN" through.
//
// The app's OWN systemDB creation calls Manager.Org("system") directly (never
// through NewNamespaced), so it is unaffected — which is exactly why this policy
// lives here and NOT in db.isSafeTenantID (that guards both paths and would
// break boot).
func TestNewNamespaced_ReservedNamespace_FailClosed(t *testing.T) {
	saveGlobals(t)
	mgr, sys, cleanup := newTestManager(t)
	defer cleanup()
	SetDefaultDB(sys)
	SetOrgDBResolver(mgr.Org)

	for _, reserved := range []string{
		"system", "admin", "default", // exact reserved names
		"System", "ADMIN", "Default", // case variants must also be rejected
	} {
		ds := NewNamespaced(ns(reserved))
		if ds.DB() != nil {
			t.Fatalf("reserved ns %q must bind NO DB (fail closed), got %v", reserved, ds.DB())
		}
		if _, err := ds.Put("product", &tprod{Name: "x"}); err == nil {
			t.Fatalf("reserved ns %q: Put must error on a no-DB datastore, got nil", reserved)
		}
		if err := ds.Get(ds.NewKey("product", "x", 0, nil), &tprod{}); err == nil {
			t.Fatalf("reserved ns %q: Get must error on a no-DB datastore, got nil", reserved)
		}
	}

	// A legitimate tenant is unaffected and still routes to its own per-org store.
	if ds := NewNamespaced(ns("acme")); ds.DB() == nil {
		t.Fatal(`normal ns "acme" must bind a per-org DB, got nil`)
	}
}
