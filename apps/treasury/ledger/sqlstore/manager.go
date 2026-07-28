// manager.go is the PER-TENANT selector over the treasury Store: it resolves each
// request to its OWN Hanzo Base (SQLite) file instead of a process-wide singleton,
// so one tenant's finance/ledger writes can NEVER appear in another tenant's reads.
// This is the storage side of the standing Hanzo rule — Postgres stays a supported
// option (Formance, one layer up), but every tenant's books run live on its own
// Base file.
//
// TWO file classes, one opener:
//
//   - the HOUSE ledger — the platform's OWN reserve fund (fund:reserve, revenue:*,
//     payout:*): a SINGLE single-writer, overdraw-guarded file. It CANNOT be split
//     per tenant (the reserve overdraw guard is one atomic balance), so it is one
//     fixed file — the pre-existing {DataDir}/treasury.db, kept verbatim so live
//     reserve capital is never orphaned by a path change.
//   - a CUSTOMER ledger — one isolated file per tenant at {DataDir}/finance/{slug}.db,
//     opened on first use and cached.
//
// It CONSUMES the treasury's canonical Base opener (Open, over github.com/hanzoai/
// sqlite — the ONE Hanzo Base driver) rather than importing the ledger's per-tenant
// opener: that opener is a test-only helper deliberately kept unexported because it
// registers modernc's database/sql "sqlite" driver, which would COLLIDE with the
// hanzoai/sqlite registration this store already carries. the ledger's own contract
// is "production callers supply their own *bun.DB to New" — which Open does — so the
// per-tenant file selection lives HERE, over Open, and the slug guard matches
// the ledger's per-tenant guard exactly.
package sqlstore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// HouseSlug names the platform's OWN reserve/house ledger — the single
// (single-writer, overdraw-guarded) book behind every backed payout. It is
// RESERVED: tenantSlug routes a literal "house" tenant to a hashed slug, so a
// caller can never open the house fund by naming its org "house".
const HouseSlug = "house"

// maxTenantLen bounds the tenant key defensively (the caller resolves it from the
// validated IAM owner claim, itself bounded to 128 — clients/principal.MaxOrgLen).
// Kept as a local constant so this storage adapter stays decoupled from cloud and
// travels with the engine when it is lifted to hanzoai/finance.
const maxTenantLen = 128

// hashMarker is the reserved prefix for HASHED slugs. A verbatim tenant is NEVER
// accepted with this prefix, so the verbatim and hashed slug namespaces are
// disjoint (two distinct tenants can never resolve to the same file).
const hashMarker = "h-"

// tenantSlugPattern is the safe file-stem shape — IDENTICAL to the ledger's
// per-tenant opener guard: a leading alphanumeric then [a-z0-9_-], no path
// separators, no dots, no traversal, at most 64 chars. A tenant that already
// matches is used verbatim (a readable file); anything else is hashed.
var tenantSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Manager opens and caches one *Store per tenant. It is safe for concurrent use.
// Each distinct tenant maps to a distinct file; the mapping is INJECTIVE (it never
// folds "acme" and "ACME" into one bucket — that would itself be a cross-tenant
// break) and can never traverse the path or collide with the reserved house file.
type Manager struct {
	housePath string // the reserve/house ledger file — pre-existing {DataDir}/treasury.db
	tenantDir string // per-customer files live at {tenantDir}/{slug}.db

	mu    sync.Mutex
	cache map[string]*Store // key: safe slug (HouseSlug or a tenantSlug result)
}

// NewManager roots the per-tenant treasury stores under dataDir: the house ledger at
// {dataDir}/treasury.db (preserved) and customer ledgers under {dataDir}/finance/.
// The finance dir (and thus dataDir) is created if absent.
func NewManager(dataDir string) (*Manager, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, fmt.Errorf("sqlstore.NewManager: empty data dir")
	}
	tenantDir := filepath.Join(dataDir, "finance")
	if err := os.MkdirAll(tenantDir, 0o750); err != nil {
		return nil, fmt.Errorf("sqlstore.NewManager: create tenant dir %q: %w", tenantDir, err)
	}
	return &Manager{
		housePath: filepath.Join(dataDir, "treasury.db"),
		tenantDir: tenantDir,
		cache:     make(map[string]*Store),
	}, nil
}

// House opens (once, then cached) the platform's reserve/house ledger — the single
// file the ledger-of-record binds to.
func (m *Manager) House() (*Store, error) { return m.open(HouseSlug) }

// Get resolves the caller's OWN per-tenant store from the validated tenant key. The
// tenant is turned into a safe, injective, path-guarded slug FIRST, so a caller can
// only ever open its own file — never another tenant's, never the house fund, never
// a path outside the finance dir.
func (m *Manager) Get(tenant string) (*Store, error) {
	slug, err := tenantSlug(tenant)
	if err != nil {
		return nil, err
	}
	return m.open(slug)
}

// open returns the cached store for an ALREADY-SAFE slug, opening it on first use.
func (m *Manager) open(slug string) (*Store, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.cache[slug]; ok {
		return s, nil
	}
	s, err := Open(m.path(slug))
	if err != nil {
		return nil, err
	}
	m.cache[slug] = s
	return s, nil
}

// path maps a safe slug to its file. The house slug resolves to the fixed,
// pre-existing reserve file; every other slug is a per-tenant file under finance/.
func (m *Manager) path(slug string) string {
	if slug == HouseSlug {
		return m.housePath
	}
	return filepath.Join(m.tenantDir, slug+".db")
}

// Close closes every open store (house + tenants). Idempotent; returns the first
// close error, if any.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var firstErr error
	for slug, s := range m.cache {
		if err := s.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(m.cache, slug)
	}
	return firstErr
}

// tenantSlug maps a validated tenant (the IAM owner claim — used VERBATIM and
// case-sensitively) to a safe, INJECTIVE file slug. A DNS-ish org keeps a readable
// file stem; anything else — uppercase, dots, path separators, over-length, the
// reserved hashMarker prefix, or the reserved HouseSlug — maps to a
// collision-resistant sha256 slug in the disjoint "h-" namespace. Distinct tenants
// NEVER share a file, and no slug can traverse the path or reach the house fund.
func tenantSlug(tenant string) (string, error) {
	t := strings.TrimSpace(tenant)
	if t == "" || len(t) > maxTenantLen {
		return "", fmt.Errorf("sqlstore: invalid tenant %q", tenant)
	}
	if tenantSlugPattern.MatchString(t) && !strings.HasPrefix(t, hashMarker) && t != HouseSlug {
		return t, nil
	}
	sum := sha256.Sum256([]byte(t))
	return hashMarker + hex.EncodeToString(sum[:20]), nil
}
