package cloud

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver: it registers the
	// "sqlite" database/sql name under both build tags (cgo → mattn+SQLCipher,
	// encrypted at rest + FTS5; !cgo → pure-Go modernc, FTS5 incl. the trigram
	// tokenizer). Importing modernc/mattn directly would double-register "sqlite"
	// under CGO and panic at init. TenantDB is the SOLE place cloud opens a tenant
	// SQLite file, so this blank import lives HERE, once — a subsystem that resolves
	// its store through TenantDB / TenantStore never imports the driver itself.
	_ "github.com/hanzoai/sqlite"
)

// TenantDB is the ONE way any cloud subsystem opens a per-tenant SQLite file
// (HIP-0302 physical tenant isolation). It resolves the path, creates the
// parent directory 0700, opens via the sole "sqlite" driver, and applies the
// single-writer + WAL pragmas every tenant store shares. The caller owns
// migration (its schema is its own) and Close.
//
// Path convention — scope is chosen by project:
//
//	project != ""  →  {DataDir}/orgs/{orgSlug}/projects/{projectSlug}/{subsystem}.db
//	project == ""  →  {DataDir}/orgs/{orgSlug}/{subsystem}.db
//
// org and project MUST be the VALIDATED principal values (principal.Tenant and,
// when project-scoped, principal.Project) — never a raw request body/header.
// TenantDB folds each through SanitizeOrg, the ONE injective tenant slugger, so
// two distinct tenants can never share a file (case-fold on a case-insensitive
// filesystem, or a "-"/"." fold, would otherwise collapse them) and no segment
// can traverse out of DataDir. An org (or, when project-scoped, a project) that
// SanitizeOrg refuses is an error — never a silent fall-through to another
// tenant's file.
func TenantDB(dataDir, org, project, subsystem string) (*sql.DB, error) {
	path, err := tenantDBPath(dataDir, org, project, subsystem)
	if err != nil {
		return nil, err
	}
	return openTenantDB(path)
}

// tenantDBPath builds the on-disk path for a tenant DB, folding org and (when
// project-scoped) project through the injective SanitizeOrg slugger and failing
// closed on any input that does not yield a safe, non-empty segment.
func tenantDBPath(dataDir, org, project, subsystem string) (string, error) {
	if dataDir == "" {
		return "", fmt.Errorf("cloud: TenantDB empty dataDir")
	}
	if subsystem == "" {
		return "", fmt.Errorf("cloud: TenantDB empty subsystem")
	}
	orgSlug := SanitizeOrg(org)
	if orgSlug == "" {
		return "", fmt.Errorf("cloud: TenantDB invalid org %q", org)
	}
	dir := filepath.Join(dataDir, "orgs", orgSlug)
	if project != "" {
		projSlug := SanitizeOrg(project)
		if projSlug == "" {
			return "", fmt.Errorf("cloud: TenantDB invalid project %q", project)
		}
		dir = filepath.Join(dir, "projects", projSlug)
	}
	return filepath.Join(dir, subsystem+".db"), nil
}

// openTenantDB creates the parent dir 0700 and opens the SQLite file with the
// single-writer + WAL pragmas shared by every tenant store. MaxOpenConns(1)
// serializes writes against the file lock (and makes a read-modify-write such as
// tracker's per-project issue-number allocation a safe transaction).
func openTenantDB(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("cloud: TenantDB mkdir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("cloud: TenantDB open %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("cloud: TenantDB pragma %q: %w", pragma, err)
		}
	}
	return db, nil
}

// TenantStore is the lazily-opened, cached set of per-tenant stores of type T
// for one subsystem, each keyed by its resolved DB path so a tenant's SQLite
// file is opened (and migrated) exactly once. It is the caching layer over
// TenantDB: every open routes through the same path resolver and pragmas, so
// there is ONE way a subsystem opens its tenant DBs and ONE hand-rolled map is
// replaced by this shared value. T is the subsystem's own store handle (it owns
// its schema via the open func's migration); T must Close its DB.
type TenantStore[T io.Closer] struct {
	dataDir   string
	subsystem string
	open      func(*sql.DB) (T, error)

	mu     sync.Mutex
	byPath map[string]T
}

// NewTenantStore builds a per-tenant store cache for subsystem under dataDir.
// open wraps a freshly-opened *sql.DB (already pragma'd by TenantDB) into the
// subsystem's store handle, running its migration; it is called once per tenant
// file.
func NewTenantStore[T io.Closer](dataDir, subsystem string, open func(*sql.DB) (T, error)) *TenantStore[T] {
	return &TenantStore[T]{
		dataDir:   dataDir,
		subsystem: subsystem,
		open:      open,
		byPath:    map[string]T{},
	}
}

// For returns the store for (org, project), opening and migrating it on first
// use and caching it thereafter. Pass project=="" for an org-scoped subsystem;
// pass principal.Project(c) for a project-scoped one. Isolation is PHYSICAL: a
// distinct (org[, project]) resolves to a distinct file, so a query in one can
// never reach another's rows.
func (c *TenantStore[T]) For(org, project string) (T, error) {
	var zero T
	path, err := tenantDBPath(c.dataDir, org, project, c.subsystem)
	if err != nil {
		return zero, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if st, ok := c.byPath[path]; ok {
		return st, nil
	}
	db, err := openTenantDB(path)
	if err != nil {
		return zero, err
	}
	st, err := c.open(db)
	if err != nil {
		_ = db.Close()
		return zero, err
	}
	c.byPath[path] = st
	return st, nil
}

// CloseAll closes every open per-tenant store. Idempotent; returns the first
// close error, if any.
func (c *TenantStore[T]) CloseAll() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var first error
	for _, st := range c.byPath {
		if err := st.Close(); err != nil && first == nil {
			first = err
		}
	}
	c.byPath = map[string]T{}
	return first
}

// SanitizeOrg reduces a gateway org id to a lowercase [a-z0-9-] slug that is
// INJECTIVE in the raw owner: an org bearing any whitespace/control/format rune
// is REFUSED (→ "") at the boundary — folding it would not survive TrimSpace /
// transport OWS-trim and would collapse "acme " onto "acme" — and every accepted
// owner then maps to the identity on a DNS-1123 label, else a folded slug
// disambiguated with "-" + the first 16 hex of SHA-256(raw owner). Without the
// suffix the fold was lossy — ToLower + every non-[a-z0-9-]→"-" + a 32-char
// truncation collapse distinct owners (`Acme`/`acme`, `team.a`/`team-a`) onto one
// slug, and since the whole tenant→bucket/DB/namespace hashes THIS slug, that was
// a cross-tenant collision.
//
// This is the ONE org-slug normalizer for the cloud tenant layer; it lives in
// the root package beside OrgHasUnsafeRune (the identity-middleware twin) and
// TenantDB (which folds every tenant DB path through it). provisioning.SanitizeOrg
// (shared with S3/KMS/knowledge) delegates here, so the slug is byte-identical
// across every physical namespace a tenant touches.
//
// The identity fast-path is withheld from a clean slug that ITSELF looks like a
// suffixed output (`<label>-<16 lowercase hex>`): such a slug is ambiguous with a
// folded owner's disambiguation, so it too is re-suffixed — otherwise a squatted
// org literally named "foo-<sha256(Foo)[:8]>" would alias non-slug owner "Foo".
func SanitizeOrg(s string) string {
	// Reject at the boundary — an empty org, or one carrying any whitespace /
	// control / zero-width-format rune, is a NON-INJECTIVE tenant identifier:
	// strings.TrimSpace (here or upstream) and fasthttp's header-value OWS trim
	// silently collapse "acme " onto "acme", so distinct IAM orgs would fold onto
	// one physical namespace/bucket/DB. Refusing (→ "", which every caller gates
	// on) is fail-secure and, with the raw-byte hash below, makes the map
	// injective end-to-end. This mirrors OrgHasUnsafeRune at the identity
	// middleware; enforcing it HERE too covers non-header callers.
	if s == "" || OrgHasUnsafeRune(s) {
		return ""
	}
	lower := strings.ToLower(s)
	if isDNSLabel(lower) && lower == s && len(lower) <= 32 && !looksSuffixed(lower) {
		return lower // already a clean, unambiguous slug: identity, no suffix needed
	}
	var b strings.Builder
	for _, r := range lower {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	folded := strings.Trim(b.String(), "-")
	if len(folded) > 32 {
		folded = strings.Trim(folded[:32], "-")
	}
	// Disambiguate with a 64-bit hash of the RAW (unsafe-rune-free, untrimmed)
	// owner so distinct owners that fold together stay distinct. The suffix is
	// derived from the exact input bytes — NEVER a trimmed copy — so a collision
	// would need a SHA-256 collision on the owner bytes themselves.
	sum := sha256.Sum256([]byte(s))
	return folded + "-" + hex.EncodeToString(sum[:8])
}

// isDNSLabel reports whether s is a non-empty [a-z0-9-] string that is not "."
// or "..". Such an owner needs no disambiguation (SanitizeOrg is the identity on
// it).
func isDNSLabel(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}

// looksSuffixed reports whether s ends in "-" + exactly 16 lowercase-hex chars —
// the shape of SanitizeOrg's own disambiguation suffix. A clean slug of this
// shape is denied the identity fast-path (and re-suffixed) so it can never alias
// a folded non-slug owner's output. This is the ONLY collision class the identity
// fast-path could otherwise admit.
func looksSuffixed(s string) bool {
	if len(s) < 17 || s[len(s)-17] != '-' {
		return false
	}
	for _, r := range s[len(s)-16:] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}
