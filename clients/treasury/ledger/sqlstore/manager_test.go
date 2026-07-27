package sqlstore

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/cek"

	"github.com/hanzoai/cloud/clients/treasury/ledger"
)

// safeSlug is the invariant every derived slug MUST satisfy: no path separators, no
// dot segments, no traversal — a single flat file stem.
var safeSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// TestManager_PerTenantIsolation is the core guarantee: a write in one tenant's
// ledger NEVER appears in another tenant's read, because each resolves to its OWN
// Base file. It also proves the house ledger is a third, separate file and that the
// cache returns a stable per-tenant handle.
func TestManager_PerTenantIsolation(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	ctx := context.Background()

	sa, err := m.Get("orga")
	if err != nil {
		t.Fatalf("Get(orga): %v", err)
	}
	sb, err := m.Get("orgb")
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

	// Distinct files on disk: orga.db, orgb.db (in finance/) and treasury.db (house).
	for _, name := range []string{"finance/orga.db", "finance/orgb.db", "treasury.db"} {
		// cek.Exists, not os.Stat: a store still OPEN has not materialized its
		// database file on the pure-Go codec — only its sidecar is on disk.
		if !cek.Exists(filepath.Join(dir, name)) {
			t.Fatalf("expected store %s", name)
		}
	}

	// Cache identity: re-Get returns the SAME handle (one open connection per tenant).
	if again, _ := m.Get("orga"); again != sa {
		t.Fatal("Get(orga) must return the cached store handle")
	}
}

// TestTenantSlug_InjectiveAndSafe proves the slug mapping is (1) injective — it never
// folds distinct tenants (case matters), (2) path-safe — traversal/separators can
// never escape the finance dir, and (3) reserved — no customer tenant can resolve to
// the house file.
func TestTenantSlug_InjectiveAndSafe(t *testing.T) {
	// A DNS-ish org keeps a readable, verbatim stem.
	if slug, err := tenantSlug("acme"); err != nil || slug != "acme" {
		t.Fatalf("tenantSlug(acme) = %q, %v; want acme,nil", slug, err)
	}

	// Case is NEVER folded: "acme" and "ACME" are distinct tenants → distinct files.
	lower, _ := tenantSlug("acme")
	upper, err := tenantSlug("ACME")
	if err != nil {
		t.Fatalf("tenantSlug(ACME): %v", err)
	}
	if upper == lower {
		t.Fatal("tenantSlug folded ACME into acme — a cross-tenant break")
	}
	if !strings.HasPrefix(upper, hashMarker) {
		t.Fatalf("tenantSlug(ACME) = %q, want hashed (%s…)", upper, hashMarker)
	}

	// Every hostile / non-DNS input maps to a SAFE flat slug (no separators, no dots,
	// no traversal), and distinct inputs never collide.
	hostile := []string{"../../etc/passwd", "a/b", "..", ".", "a.b.c", "UPPER", "sp ace", "house", "h-collide", strings.Repeat("x", 100)}
	seen := map[string]string{}
	for _, in := range hostile {
		slug, err := tenantSlug(in)
		if err != nil {
			t.Fatalf("tenantSlug(%q): unexpected error %v", in, err)
		}
		if !safeSlug.MatchString(slug) || strings.ContainsAny(slug, "/.\\") {
			t.Fatalf("tenantSlug(%q) = %q is not a safe flat slug", in, slug)
		}
		if prev, ok := seen[slug]; ok {
			t.Fatalf("tenantSlug collision: %q and %q both → %q", prev, in, slug)
		}
		seen[slug] = in
	}

	// Reserved: a literal "house" tenant must NOT resolve to the house file's slug.
	if slug, _ := tenantSlug(HouseSlug); slug == HouseSlug {
		t.Fatal("a customer tenant named \"house\" resolved to the reserved house slug")
	}

	// Empty / over-length tenants are refused (defense-in-depth over principal.Org).
	if _, err := tenantSlug(""); err == nil {
		t.Fatal("tenantSlug(\"\") must error")
	}
	if _, err := tenantSlug(strings.Repeat("a", maxTenantLen+1)); err == nil {
		t.Fatal("tenantSlug(over-length) must error")
	}
}

// TestManager_TraversalStaysInDir proves a path-traversal tenant key lands a real
// file INSIDE the finance dir and never writes outside it.
func TestManager_TraversalStaysInDir(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	if _, err := m.Get("../../../../etc/passwd"); err != nil {
		t.Fatalf("Get(traversal): %v", err)
	}
	// The only .db files anywhere under dir must be within the finance/ subdir.
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".db") {
			return nil
		}
		if !strings.HasPrefix(p, filepath.Join(dir, "finance")+string(os.PathSeparator)) {
			t.Fatalf("traversal escaped: %s is outside the finance dir", p)
		}
		return nil
	})
}
