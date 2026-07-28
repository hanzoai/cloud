package kms

import "testing"

// TestDBFor_TenantCannotSpellReservedPartition pins the defense-in-depth the red
// team flagged: a tenant path literally spelling "/orgs/_platform/…" must be
// refused, never routed to cloud.PlatformDB (the deployment-wide facade store).
// The facade is reachable ONLY by the empty/non-"/orgs" route, and this holds
// even without validOrg having run first.
func TestDBFor_TenantCannotSpellReservedPartition(t *testing.T) {
	s := newSecretStore(t.TempDir(), false)
	// The facade partition is chosen by the boolean, never by a tenant org string.
	_, facade := fileOrg("/orgs/" + reservedPlatformSlug + "/secrets/x")
	if facade {
		t.Fatal("a tenant path /orgs/_platform routed to the facade partition — must not")
	}
	if _, f := fileOrg("/facade-secret"); !f {
		t.Fatal("a non-/orgs path must route to the facade partition")
	}
	// The tenant _platform path opens a DISTINCT store (not PlatformDB) and its DB
	// pointer differs from the real facade store — proof they never alias.
	tenantDB, err := s.dbFor("/orgs/"+reservedPlatformSlug+"/secrets/x", true)
	if err != nil || tenantDB == nil {
		t.Fatalf("tenant _platform path must open its own store: db=%v err=%v", tenantDB, err)
	}
	facadeDB, err := s.dbFor("/facade-secret", true)
	if err != nil || facadeDB == nil {
		t.Fatalf("facade route must open PlatformDB: db=%v err=%v", facadeDB, err)
	}
	if tenantDB == facadeDB {
		t.Fatal("tenant _platform store and the facade store are the SAME handle — they must never alias")
	}
}
