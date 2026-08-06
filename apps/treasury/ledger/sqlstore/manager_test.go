package sqlstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/treasury/ledger"
	"github.com/hanzoai/namespace"

	// devmaster keys this test binary: cek opens nothing without a master and a
	// test process has no KMS.
	_ "github.com/hanzoai/cloud/internal/devmaster"
)

// storePath renders where a manager's store for ns lands, through the SAME
// namespace.Path the manager opens by — so an assertion here can never be about
// a different file than the one the code wrote.
func storePath(t *testing.T, dir string, ns namespace.Namespace, subsystem string) string {
	t.Helper()
	p, err := namespace.Path(dir, ns, subsystem)
	if err != nil {
		t.Fatalf("namespace.Path(%s, %s): %v", ns, subsystem, err)
	}
	return p
}

// TestManager_PerTenantIsolation is the core guarantee: a write in one tenant's
// ledger NEVER appears in another tenant's read, because each resolves to its OWN
// Base file. It also proves the house ledger is a third, separate file and that the
// cache returns a stable per-tenant handle.
func TestManager_PerTenantIsolation(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir, kindUsage)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	ctx := context.Background()

	nsA := namespace.MustOrgProject("orga", "")
	nsB := namespace.MustOrgProject("orgb", "")

	sa, err := m.Get(nsA)
	if err != nil {
		t.Fatalf("Get(orga): %v", err)
	}
	sb, err := m.Get(nsB)
	if err != nil {
		t.Fatalf("Get(orgb): %v", err)
	}

	// A $50 write into org A's ledger.
	if _, _, err := ledger.New(sa).Seed(ctx, "seed:a", "cap", 5_000, 1); err != nil {
		t.Fatalf("seed A: %v", err)
	}

	// org A sees it; org B sees NOTHING (its own file, untouched).
	if bal, _ := ledger.New(sa).ReserveCents(ctx); bal != 5_000 {
		t.Fatalf("org A reserve = %d, want 5000", bal)
	}
	if bal, _ := ledger.New(sb).ReserveCents(ctx); bal != 0 {
		t.Fatalf("org B reserve = %d, want 0 (org A's write leaked across the tenant boundary)", bal)
	}

	// The house ledger is a THIRD separate file; a house write is invisible to A and B.
	house, err := m.House()
	if err != nil {
		t.Fatalf("House: %v", err)
	}
	if _, _, err := ledger.New(house).Seed(ctx, "seed:house", "reserve", 9_000, 1); err != nil {
		t.Fatalf("seed house: %v", err)
	}
	if bal, _ := ledger.New(sa).ReserveCents(ctx); bal != 5_000 {
		t.Fatalf("org A reserve after house write = %d, want 5000 (house leaked into a tenant)", bal)
	}

	// Cache identity: re-Get returns the SAME handle (one open connection per tenant).
	if again, _ := m.Get(nsA); again != sa {
		t.Fatal("Get(orga) must return the cached store handle")
	}

	// Three distinct files on disk — CLOSED first, because the pure-Go codec seals
	// a database back to its path only when the handle closes.
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, want := range []string{
		storePath(t, dir, nsA, tenantSubsystem),
		storePath(t, dir, nsB, tenantSubsystem),
		storePath(t, dir, namespace.System(), houseSubsystem),
	} {
		if _, err := os.Stat(want); err != nil {
			t.Fatalf("expected store %s: %v", want, err)
		}
	}
}

// hostileTenants are the tenant strings the fold has to survive on the way to a
// name: traversal, separators, dot segments, the house's own rendering, a case
// variant, over-length. The Manager is handed the NAME, so this is the door one
// step up — but it is the same question, and it is answered here because this is
// where the file it decides lives.
var hostileTenants = []string{
	"../../etc/passwd", "a/b", "..", ".", "a.b.c", "UPPER", "sp ace",
	"_platform", "platform", "house", "treasury", "orgs", strings.Repeat("x", 100),
}

// tenantNS folds one tenant string into the name its ledger lives under. ok is
// false when the deployment refuses the name outright, which names no file at all.
func tenantNS(tenant string) (ns namespace.Namespace, ok bool) {
	ns, err := namespace.OrgProject(tenant, "")
	return ns, err == nil
}

// TestManager_HouseUnreachableByTenantName is the property that used to need a
// reserved slug and needs none now: the house fund lives in the SYSTEM namespace,
// which is a different KIND from every org namespace, so no tenant string resolves
// to it however it is spelled.
func TestManager_HouseUnreachableByTenantName(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir, kindUsage)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	house, err := m.House()
	if err != nil {
		t.Fatalf("House: %v", err)
	}
	housePath := storePath(t, dir, namespace.System(), houseSubsystem)

	for _, tenant := range hostileTenants {
		ns, ok := tenantNS(tenant)
		if !ok {
			continue
		}
		if got := storePath(t, dir, ns, tenantSubsystem); got == housePath {
			t.Fatalf("tenant %q resolves to the house file %s", tenant, housePath)
		}
		s, err := m.Get(ns)
		if err != nil {
			t.Fatalf("Get(%q → %s): %v", tenant, ns, err)
		}
		if s == house {
			t.Fatalf("tenant %q reached the house ledger", tenant)
		}
	}
}

// TestManager_DistinctTenantsDistinctFiles proves the tenant→file mapping never
// folds: two distinct tenants — case variants and punctuation variants included —
// always land on two files, because one file for two tenants IS the cross-tenant
// break this layer exists to prevent.
func TestManager_DistinctTenantsDistinctFiles(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir, kindUsage)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	byPath := map[string]string{}
	byStore := map[*Store]string{}
	for _, tenant := range append([]string{"acme", "ACME", "acme-corp", "acme.corp", "a"}, hostileTenants...) {
		ns, ok := tenantNS(tenant)
		if !ok {
			continue
		}
		p := storePath(t, dir, ns, tenantSubsystem)
		if prev, dup := byPath[p]; dup {
			t.Fatalf("tenants %q and %q share one file %s", prev, tenant, p)
		}
		byPath[p] = tenant

		s, err := m.Get(ns)
		if err != nil {
			t.Fatalf("Get(%q → %s): %v", tenant, ns, err)
		}
		if prev, dup := byStore[s]; dup {
			t.Fatalf("tenants %q and %q share one store handle", prev, tenant)
		}
		byStore[s] = tenant
	}
}

// TestManager_TraversalStaysInDir proves a path-traversal tenant key lands a real
// file INSIDE the data directory and never writes outside it. namespace SANITISES
// rather than rejects, so the property is "cannot escape", not "errors".
func TestManager_TraversalStaysInDir(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir, kindUsage)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	orgsRoot := filepath.Join(dir, "orgs") + string(os.PathSeparator)
	for _, tenant := range hostileTenants {
		ns, ok := tenantNS(tenant)
		if !ok {
			continue
		}
		if p := storePath(t, dir, ns, tenantSubsystem); !strings.HasPrefix(p, orgsRoot) {
			t.Fatalf("tenant %q escaped: %s is outside %s", tenant, p, orgsRoot)
		}
		if _, err := m.Get(ns); err != nil {
			t.Fatalf("Get(%q → %s): %v", tenant, ns, err)
		}
	}

	// And on disk: closing seals every store to its path, and every one of them is
	// under orgs/.
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".db") {
			return nil
		}
		if !strings.HasPrefix(p, orgsRoot) {
			t.Fatalf("traversal escaped: %s is outside %s", p, orgsRoot)
		}
		return nil
	})
}
