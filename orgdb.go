package cloud

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	// cek opens every org file encrypted at rest (the SOLE org-open seam).
	"github.com/hanzoai/cloud/cek"

	// internal/org is the HA-durable substrate: ha election (WHO writes) + vfs
	// FencedStore ship/hydrate (HOW it ships) + envelope Cipher (at rest). An
	// OrgStore configured WithDurable routes every per-org file through it.
	"github.com/hanzoai/cloud/internal/org"
	luxlog "github.com/luxfi/log"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver: it registers the
	// "sqlite" database/sql name under both build tags (cgo → mattn+SQLCipher,
	// encrypted at rest + FTS5; !cgo → pure-Go modernc, FTS5 incl. the trigram
	// tokenizer). Importing modernc/mattn directly would double-register "sqlite"
	// under CGO and panic at init. OrgDB is the SOLE place cloud opens an org SQLite
	// file; the blank import keeps the driver registered for cek's no-key fallback.
	_ "github.com/hanzoai/sqlite"
)

// Durability wires an OrgStore's per-org files through the HA-durable path
// (github.com/hanzoai/cloud/internal/org): each store is owned by the ha-elected
// single writer for its org, hydrated from the object store on open, and its writes
// shipped back fenced by the lease round. It is the per-deployment factory (one
// election+fence over one object store); a possibly-nil value means local-only
// (dev/single-node), the open path unchanged.
type Durability = org.Durability

// OrgStoreOption configures optional OrgStore behavior. With no options an OrgStore
// is exactly the local-only cache it has always been (cek open, no ship).
type OrgStoreOption func(*orgStoreOpts)

type orgStoreOpts struct {
	dur *Durability
	log luxlog.Logger
}

// WithDurable routes every per-org file through the HA-durable path. A nil dur is a
// no-op (local-only), so a caller passes its deployment's possibly-nil Durability
// directly and the store degrades to local on a deployment without an object store.
func WithDurable(dur *Durability) OrgStoreOption {
	return func(o *orgStoreOpts) { o.dur = dur }
}

// WithStoreLogger sets the logger used to report a degraded hydrate (the store still
// opens; the message is the operator's signal that a durable open ran read-only).
func WithStoreLogger(log luxlog.Logger) OrgStoreOption {
	return func(o *orgStoreOpts) { o.log = log }
}

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

	// dur (when non-nil) makes every per-org file HA-durable: forPath hydrates it
	// from the object store before opening and binds a Durable that Sync ships
	// fenced. nil ⇒ local-only, byte-identical to the pre-durability cache.
	dur *Durability
	log luxlog.Logger

	mu       sync.Mutex
	byPath   map[string]T
	durables map[string]*org.Durable  // parallel to byPath, populated only when dur != nil
	inflight map[string]*openState[T] // durable opens in progress, deduped by path (M1)
}

// durableOpTimeout bounds every object-store round-trip a durable OrgStore makes
// (hydrate on open, ship on Sync, final ship on close). It exists so a slow or hung
// SeaweedFS degrades to a bounded latency (open read-only / ship not-acked) rather
// than blocking a caller — or the store-wide lock — indefinitely.
const durableOpTimeout = 30 * time.Second

// openState records a durable open in flight for one path, so concurrent For() calls
// for the SAME org wait on the ONE open (its done channel) instead of double-opening,
// WITHOUT any of them holding the store-wide c.mu across the object-store I/O.
type openState[T io.Closer] struct {
	done chan struct{}
	st   T
	d    *org.Durable
	err  error
}

// NewOrgStore builds a per-org store cache for subsystem under dataDir.
// open wraps a freshly-opened *sql.DB (already pragma'd by OrgDB) into the
// subsystem's store handle, running its migration; it is called once per org
// file. Pass WithDurable to route every file through the HA-durable path; with no
// options the cache is exactly the local-only one it has always been.
func NewOrgStore[T io.Closer](dataDir, subsystem string, open func(*sql.DB) (T, error), opts ...OrgStoreOption) *OrgStore[T] {
	var o orgStoreOpts
	for _, fn := range opts {
		fn(&o)
	}
	return &OrgStore[T]{
		dataDir:   dataDir,
		subsystem: subsystem,
		open:      open,
		dur:       o.dur,
		log:       o.log,
		byPath:    map[string]T{},
		durables:  map[string]*org.Durable{},
		inflight:  map[string]*openState[T]{},
	}
}

// For returns the store for (org, project), opening and migrating it on first
// use and caching it thereafter. Pass project=="" for an org-scoped subsystem;
// pass principal.Project(c) for a project-scoped one. Isolation is PHYSICAL: a
// distinct (org[, project]) resolves to a distinct file, so a query in one can
// never reach another's rows.
func (c *OrgStore[T]) For(orgID, project string) (T, error) {
	path, err := orgDBPath(c.dataDir, orgID, project, c.subsystem)
	if err != nil {
		var zero T
		return zero, err
	}
	slug, dbKey := c.durKey(orgID, project)
	return c.forPath(slug, dbKey, path)
}

// durKey returns the org SLUG (the HRW election key + cipher AAD — the SAME slug the
// on-disk path and the shard router hash use) and the durable object key for this
// (org, project). The object layout mirrors the on-disk one (HIP-0302), so a store's
// durable slot is the canonical orgs/<slug>[/projects/<proj>]/<subsystem>.db.
func (c *OrgStore[T]) durKey(orgID, project string) (slug, dbKey string) {
	slug = SanitizeOrg(orgID)
	scope := ""
	if project != "" {
		scope = "projects/" + SanitizeOrg(project)
	}
	return slug, org.DBPath(slug, scope, c.subsystem)
}

// forPath opens (and migrates on first use) the store at an already-resolved DB
// path, caching by path. It is the shared core of For and Each: the cache key is
// the path, so an org reached via For(org) and the SAME file reached via Each's
// enumeration resolve to the ONE handle — never a second open of the same file
// (which the at-rest cek layer does not support concurrently).
func (c *OrgStore[T]) forPath(slug, dbKey, path string) (T, error) {
	var zero T
	c.mu.Lock()
	if st, ok := c.byPath[path]; ok {
		// Cache hit. If this durable store opened DEGRADED (read-only) and this replica
		// has since become the org's elected owner — a rolling-upgrade membership change
		// — promote it IN PLACE (M3): re-acquire the lease, hydrate the latest snapshot,
		// and swap in a writer handle, with no process restart. The check is I/O-free and
		// only does real work when the store is unowned AND newly elected (rare).
		d := c.durables[path]
		if d == nil || !d.PendingPromotion() {
			c.mu.Unlock()
			return st, nil
		}
		if inf, promoting := c.inflight[path]; promoting {
			c.mu.Unlock()
			<-inf.done
			return inf.st, inf.err
		}
		inf := &openState[T]{done: make(chan struct{})}
		c.inflight[path] = inf
		delete(c.byPath, path)
		delete(c.durables, path)
		c.mu.Unlock()

		st2, d2, err := c.promote(slug, dbKey, path, st, d)
		inf.st, inf.d, inf.err = st2, d2, err
		c.mu.Lock()
		delete(c.inflight, path)
		if d2 != nil { // a usable store (promoted writer, or the kept read-only one)
			c.byPath[path] = st2
			c.durables[path] = d2
		}
		c.mu.Unlock()
		close(inf.done)
		if err != nil && d2 != nil && c.log != nil {
			// Reopen degraded again (transient store/membership blip): still serving the
			// prior read-only state, so log and keep availability rather than surface it.
			c.log.Warn("org store promotion incomplete — serving prior state", "subsystem", c.subsystem, "org", slug, "err", err)
			return st2, nil
		}
		return st2, err
	}
	// Local-only (no Durability): open under c.mu — disk I/O only, unchanged from the
	// pre-durability cache.
	if c.dur == nil {
		defer c.mu.Unlock()
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
	// Durable: run the open (object-store hydrate + cek) with c.mu RELEASED so a
	// slow/hung SeaweedFS never stalls another org's open or a cache hit (M1). A
	// concurrent open of the SAME path waits on the in-flight record rather than
	// double-opening.
	if inf, ok := c.inflight[path]; ok {
		c.mu.Unlock()
		<-inf.done
		return inf.st, inf.err
	}
	inf := &openState[T]{done: make(chan struct{})}
	c.inflight[path] = inf
	c.mu.Unlock()

	inf.st, inf.d, inf.err = c.openDurable(slug, dbKey, path)

	c.mu.Lock()
	delete(c.inflight, path)
	if inf.err == nil {
		c.byPath[path] = inf.st
		c.durables[path] = inf.d
	}
	c.mu.Unlock()
	close(inf.done)
	return inf.st, inf.err
}

// openDurable hydrates the org's durable snapshot into the local file (as the elected
// owner, sealing the durable object to the lease round; as a non-owner, read-only)
// BEFORE opening the local handle, then opens through the SAME cek path and binds the
// handle so Sync can checkpoint on the store's sole connection. It runs WITHOUT the
// store-wide c.mu held and time-bounds the object-store round-trip: a degraded hydrate
// (store unreachable / timed out / not owner) NEVER blocks the open — the store always
// opens (reads serve local, writes fail closed via Sync) so a second replica can never
// break the store. It returns the store handle and its Durable for the caller to
// publish; it touches no shared map.
func (c *OrgStore[T]) openDurable(slug, dbKey, path string) (T, *org.Durable, error) {
	var zero T
	d := c.dur.For(slug, dbKey, path)
	ctx, cancel := context.WithTimeout(context.Background(), durableOpTimeout)
	err := d.Hydrate(ctx)
	cancel()
	if err != nil && c.log != nil {
		c.log.Warn("org store hydrate degraded — opening read-only", "subsystem", c.subsystem, "org", slug, "key", dbKey, "err", err)
	}
	db, err := openOrgDB(path)
	if err != nil {
		return zero, nil, err
	}
	d.Bind(db)
	st, err := c.open(db)
	if err != nil {
		_ = db.Close()
		return zero, nil, err
	}
	return st, d, nil
}

// promote upgrades a degraded (read-only) store to the writer, in place, when this
// replica has become the org's elected owner (M3 — no process restart). It PROBES first
// (TryClaim: only the lease CAS, no file I/O), so a transient membership/store blip that
// is not yet claimable leaves the read-only handle serving untouched. Only once the lease
// is claimed does it quiesce — close the read-only handle — and re-open as owner, whose
// Hydrate renews that same lease and CarryForward-restores the latest snapshot under the
// FRESH handle (never under the live one — the file swap is exactly why the reopen is
// required). The fence's monotone round makes this safe against a still-running prior
// owner: the reopen claims a strictly higher round, so any late ship from the deposed
// writer is rejected — never two live writers for one org.
//
// Returns the store to (re)publish and an error to LOG: (new writer, nil) on success;
// (the same read-only store, nil-or-err) when not yet claimable; (zero, err) only if the
// reopen hard-fails after the claim (a local disk error — the entry is then dropped and
// the failure surfaced).
func (c *OrgStore[T]) promote(slug, dbKey, path string, old T, oldDur *org.Durable) (T, *org.Durable, error) {
	ctx, cancel := context.WithTimeout(context.Background(), durableOpTimeout)
	claimed, err := oldDur.TryClaim(ctx)
	cancel()
	if err != nil || !claimed {
		return old, oldDur, err // not the owner yet / store blip: keep serving read-only.
	}
	_ = old.Close() // quiesce: release the read-only handle before CarryForward swaps the file.
	return c.openDurable(slug, dbKey, path)
}

// Sync ships the org's local file to its durable object, fenced at the lease round
// (the ship-before-ack step a durable subsystem calls after a write commits). It
// returns acked=false when this replica is not the owner or was deposed mid-request
// (the caller retries on the new owner). On a local-only store (no Durability) it is
// a successful no-op — the write is already as durable as configured. project is ""
// for an org-scoped subsystem.
func (c *OrgStore[T]) Sync(orgID, project string) (acked bool, err error) {
	if c.dur == nil {
		return true, nil
	}
	path, err := orgDBPath(c.dataDir, orgID, project, c.subsystem)
	if err != nil {
		return false, err
	}
	c.mu.Lock()
	d := c.durables[path]
	c.mu.Unlock()
	if d == nil {
		return false, fmt.Errorf("cloud: OrgStore.Sync for %s before its store was opened", path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), durableOpTimeout)
	defer cancel()
	return d.Sync(ctx)
}

// Each folds fn over every org that has a {subsystem}.db on disk under
// {dataDir}/orgs, handing it the org's SLUG (the on-disk directory name) and the
// SAME cached store handle For returns (opened through forPath, keyed by path — no
// second open). It is the cross-org sweep primitive a reconciler folds over: the
// filesystem is the source of truth for "which orgs have this store", so no derived
// registry can drift. The reserved platform partitions ({dataDir}/orgs/_*) are
// skipped (their '_' is a rune SanitizeOrg never emits, so no real org is dropped).
// A per-org OPEN failure is passed to fn as its err (fn decides skip vs. record); a
// missing orgs root (a writer with no stores yet) is not an error. Under horizontal
// sharding each writer's PVC holds only the orgs routed to it, so Each on a given
// writer enumerates exactly that writer's orgs.
func (c *OrgStore[T]) Each(fn func(slug string, st T, err error)) error {
	root := filepath.Join(c.dataDir, "orgs")
	ents, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "_") {
			continue
		}
		path := filepath.Join(root, e.Name(), c.subsystem+".db")
		if _, statErr := os.Stat(path); statErr != nil {
			continue // this org has no store for this subsystem
		}
		// e.Name() IS the on-disk org slug — the same value durKey folds to and the
		// election hashes; Each is org-root scoped, so the durable key has no project.
		st, openErr := c.forPath(e.Name(), org.DBPath(e.Name(), "", c.subsystem), path)
		fn(e.Name(), st, openErr)
	}
	return nil
}

// CloseAll closes every open per-org store. Idempotent; returns the first
// close error, if any.
func (c *OrgStore[T]) CloseAll() error {
	// Snapshot the durables, then ship each final state with c.mu RELEASED and
	// time-bounded (L3): a hung object store must not stall shutdown, and the final
	// ship never needs the store-wide lock (ship-before-ack already covers every
	// acknowledged write, so this is belt-and-suspenders). Durable never owns the
	// *sql.DB, so the store Close below is the sole handle close.
	c.mu.Lock()
	durs := make([]*org.Durable, 0, len(c.durables))
	for _, d := range c.durables {
		durs = append(durs, d)
	}
	c.mu.Unlock()
	for _, d := range durs {
		ctx, cancel := context.WithTimeout(context.Background(), durableOpTimeout)
		_ = d.Close(ctx)
		cancel()
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	var first error
	for _, st := range c.byPath {
		if err := st.Close(); err != nil && first == nil {
			first = err
		}
	}
	c.byPath = map[string]T{}
	c.durables = map[string]*org.Durable{}
	c.inflight = map[string]*openState[T]{}
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
