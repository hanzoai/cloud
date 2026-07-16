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

	// cek opens every org file encrypted at rest (the SOLE org-open seam).
	"github.com/hanzoai/cloud/cek"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver: it registers the
	// "sqlite" database/sql name under both build tags (cgo → mattn+SQLCipher,
	// encrypted at rest + FTS5; !cgo → pure-Go modernc, FTS5 incl. the trigram
	// tokenizer). Importing modernc/mattn directly would double-register "sqlite"
	// under CGO and panic at init. OrgDB is the SOLE place cloud opens an org SQLite
	// file; the blank import keeps the driver registered for cek's no-key fallback.
	_ "github.com/hanzoai/sqlite"
)

// OrgDB is the ONE way any cloud subsystem opens a per-org SQLite file
// (HIP-0302 physical org isolation). It resolves the path, creates the
// parent directory 0700, opens via the sole "sqlite" driver, and applies the
// single-writer + WAL pragmas every org store shares. The caller owns
// migration (its schema is its own) and Close.
//
// Path convention — scope is chosen by project:
//
//	project != ""  →  {DataDir}/orgs/{orgSlug}/projects/{projectSlug}/{subsystem}.db
//	project == ""  →  {DataDir}/orgs/{orgSlug}/{subsystem}.db
//
// org and project MUST be the VALIDATED principal values (principal.Org and,
// when project-scoped, principal.Project) — never a raw request body/header.
// OrgDB folds each through SanitizeOrg, the ONE injective org slugger, so
// two distinct orgs can never share a file (case-fold on a case-insensitive
// filesystem, or a "-"/"." fold, would otherwise collapse them) and no segment
// can traverse out of DataDir. An org (or, when project-scoped, a project) that
// SanitizeOrg refuses is an error — never a silent fall-through to another
// org's file.
func OrgDB(dataDir, org, project, subsystem string) (*sql.DB, error) {
	path, err := orgDBPath(dataDir, org, project, subsystem)
	if err != nil {
		return nil, err
	}
	return openOrgDB(path)
}

// orgDBPath builds the on-disk path for an org DB, folding org and (when
// project-scoped) project through the injective SanitizeOrg slugger and failing
// closed on any input that does not yield a safe, non-empty segment.
func orgDBPath(dataDir, org, project, subsystem string) (string, error) {
	if dataDir == "" {
		return "", fmt.Errorf("cloud: OrgDB empty dataDir")
	}
	if subsystem == "" {
		return "", fmt.Errorf("cloud: OrgDB empty subsystem")
	}
	orgSlug := SanitizeOrg(org)
	if orgSlug == "" {
		return "", fmt.Errorf("cloud: OrgDB invalid org %q", org)
	}
	dir := filepath.Join(dataDir, "orgs", orgSlug)
	if project != "" {
		projSlug := SanitizeOrg(project)
		if projSlug == "" {
			return "", fmt.Errorf("cloud: OrgDB invalid project %q", project)
		}
		dir = filepath.Join(dir, "projects", projSlug)
	}
	return filepath.Join(dir, subsystem+".db"), nil
}

// openOrgDB creates the parent dir 0700 and opens the SQLite file with the
// single-writer + WAL pragmas shared by every org store. MaxOpenConns(1)
// serializes writes against the file lock (and makes a read-modify-write such as
// tracker's per-project issue-number allocation a safe transaction).
func openOrgDB(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("cloud: OrgDB mkdir: %w", err)
	}
	db, err := cek.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cloud: OrgDB open %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("cloud: OrgDB pragma %q: %w", pragma, err)
		}
	}
	return db, nil
}

// reservedPlatformSlug is the org directory holding a per-org subsystem's
// genuinely DEPLOYMENT-WIDE (non-tenant) records. It contains '_', a rune
// SanitizeOrg never emits (it folds '_'→'-'), so no tenant owner can ever resolve
// to this directory — the platform partition can never collide with a real org's
// file. See PlatformDB.
const reservedPlatformSlug = "_platform"

// PlatformDB opens the deployment's reserved, NON-tenant partition of an
// otherwise per-org subsystem at {DataDir}/orgs/_platform/{subsystem}.db, through
// the SAME cek + single-writer + WAL path as OrgDB. It is the home for records a
// per-org subsystem holds that belong to no single tenant (e.g. a platform-wide
// HMAC key): everything tenant-scoped stays in its own {DataDir}/orgs/{slug}/…
// file. Because the slug carries a '_' that SanitizeOrg never produces, this file
// is guaranteed disjoint from every tenant's. The caller owns migration + Close.
func PlatformDB(dataDir, subsystem string) (*sql.DB, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("cloud: PlatformDB empty dataDir")
	}
	if subsystem == "" {
		return nil, fmt.Errorf("cloud: PlatformDB empty subsystem")
	}
	return openOrgDB(filepath.Join(dataDir, "orgs", reservedPlatformSlug, subsystem+".db"))
}

// OrgStore is the lazily-opened, cached set of per-org stores of type T
// for one subsystem, each keyed by its resolved DB path so an org's SQLite
// file is opened (and migrated) exactly once. It is the caching layer over
// OrgDB: every open routes through the same path resolver and pragmas, so
// there is ONE way a subsystem opens its org DBs and ONE hand-rolled map is
// replaced by this shared value. T is the subsystem's own store handle (it owns
// its schema via the open func's migration); T must Close its DB.
type OrgStore[T io.Closer] struct {
	dataDir   string
	subsystem string
	open      func(*sql.DB) (T, error)

	mu     sync.Mutex
	byPath map[string]T
}

// NewOrgStore builds a per-org store cache for subsystem under dataDir.
// open wraps a freshly-opened *sql.DB (already pragma'd by OrgDB) into the
// subsystem's store handle, running its migration; it is called once per org
// file.
func NewOrgStore[T io.Closer](dataDir, subsystem string, open func(*sql.DB) (T, error)) *OrgStore[T] {
	return &OrgStore[T]{
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
func (c *OrgStore[T]) For(org, project string) (T, error) {
	var zero T
	path, err := orgDBPath(c.dataDir, org, project, c.subsystem)
	if err != nil {
		return zero, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if st, ok := c.byPath[path]; ok {
		return st, nil
	}
	db, err := openOrgDB(path)
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

// CloseAll closes every open per-org store. Idempotent; returns the first
// close error, if any.
func (c *OrgStore[T]) CloseAll() error {
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
// slug, and since the whole org→bucket/DB/namespace hashes THIS slug, that was
// a cross-org collision.
//
// This is the ONE org-slug normalizer for the cloud org layer; it lives in
// the root package beside OrgHasUnsafeRune (the identity-middleware twin) and
// OrgDB (which folds every org DB path through it). provisioning.SanitizeOrg
// (shared with S3/KMS/knowledge) delegates here, so the slug is byte-identical
// across every physical namespace an org touches.
//
// The identity fast-path is withheld from a clean slug that ITSELF looks like a
// suffixed output (`<label>-<16 lowercase hex>`): such a slug is ambiguous with a
// folded owner's disambiguation, so it too is re-suffixed — otherwise a squatted
// org literally named "foo-<sha256(Foo)[:8]>" would alias non-slug owner "Foo".
func SanitizeOrg(s string) string {
	// Reject at the boundary — an empty org, or one carrying any whitespace /
	// control / zero-width-format rune, is a NON-INJECTIVE org identifier:
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
