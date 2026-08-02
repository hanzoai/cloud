package cloud

import (
	"context"
	"database/sql"
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
	// FencedStore ship/hydrate (HOW it ships) + envelope Cipher (at rest). Every
	// per-org file routes through it when the deployment has one.
	"github.com/hanzoai/cloud/internal/org"
	"github.com/hanzoai/namespace"
	luxlog "github.com/luxfi/log"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver: it registers the
	// "sqlite" database/sql name under both build tags (cgo → mattn+SQLCipher,
	// encrypted at rest + FTS5; !cgo → pure-Go modernc, FTS5 incl. the trigram
	// tokenizer). Importing modernc/mattn directly would double-register "sqlite"
	// under CGO and panic at init. OrgDB is the SOLE place cloud opens an org SQLite
	// file; the blank import keeps the driver registered for cek's no-key fallback.
	_ "github.com/hanzoai/sqlite"
)

// Durability answers a question namespace does not ask, and keeping the two
// apart is the point. A namespace says WHICH database; org.Durability says who
// is allowed to write it (the ha-elected single owner), how its bytes reach the
// object store (a fenced ship at the lease round) and how they are sealed on the
// way (the per-org envelope). Braiding them would make "name a database" and
// "own a database" one decision, and they are made by different things at
// different times — the name by the request, the ownership by an election.
//
// The type is org.Durability and is spelled that way everywhere. It used to have
// a second name here — `type Durability = org.Durability` — which bought nothing
// and cost the reader a hop to find out the two were the same thing.

// OrgDB is the ONE way any cloud subsystem opens a per-entity SQLite file
// (HIP-0302 physical isolation). It resolves the namespace to its path, creates
// the parent directory 0700, opens via the sole "sqlite" driver under that
// namespace's own key, and applies the single-writer + WAL pragmas every store
// shares. The caller owns migration (its schema is its own) and Close.
//
// It takes the NAME rather than the parts a name is made of, so it cannot pair
// one namespace's path with another namespace's key, and so the question "could
// this have come from caller input" is asked once — at OrgNamespace, the only
// door — instead of again at every subsystem that opens a file.
//
// Path convention — see namespace.Key:
//
//	org/{slug}            →  {DataDir}/orgs/{slug}/{subsystem}.db
//	org/{slug}/{project}  →  {DataDir}/orgs/{slug}/projects/{project}/{subsystem}.db
//	system                →  {DataDir}/orgs/_platform/{subsystem}.db
func OrgDB(dataDir string, ns namespace.Namespace, subsystem string) (*sql.DB, error) {
	path, err := namespace.Path(dataDir, ns, subsystem)
	if err != nil {
		return nil, err
	}
	return openOrgDB(nsPrincipal(ns), path)
}

// openOrgDB creates the parent dir 0700 and opens the SQLite file with the
// single-writer + WAL pragmas shared by every org store. MaxOpenConns(1)
// serializes writes against the file lock (and makes a read-modify-write such as
// tracker's per-project issue-number allocation a safe transaction).
func openOrgDB(p cek.Principal, path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("cloud: OrgDB mkdir: %w", err)
	}
	db, err := cek.Open(p, path)
	if err != nil {
		// A store whose sidecar predates "a store's key names its owner" is wrapped
		// under the legacy Global derivation, so the owner-bound open above cannot
		// unwrap it. Rewrap carries the SAME DEK to the owner-bound wrapping and we
		// retry, ONCE — the migration runs where the need is discovered rather than
		// in a tool someone has to remember.
		//
		// It shipped as cmd/cek-rewrap alone and nothing invoked it, so the
		// derivation changed under stores that were never migrated and two of them
		// (git, sync) simply stopped opening in production — which took the native
		// git plane and the mirror engine down, and with them every deploy. A
		// migration that must be run by hand is a migration that has not shipped.
		//
		// Idempotent and conservative: Rewrap reports Already when the owner-bound
		// key already opens it, and refuses when NEITHER identity does — a genuinely
		// wrong master key or a corrupt sidecar stays an error, and is not papered
		// over by rewriting a sidecar we could not read.
		if res := cek.Rewrap(p, path); res.Err == nil && res.Rewrapped {
			db, err = cek.Open(p, path)
		}
		if err != nil {
			return nil, fmt.Errorf("cloud: OrgDB open %q: %w", path, err)
		}
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

// OrgStore is the lazily-opened, cached set of per-entity stores of type T for
// one subsystem, each keyed by the NAMESPACE that names it so an entity's
// SQLite file is opened (and migrated) exactly once. It is the caching layer
// over OrgDB: every open routes through the same name, path and pragmas, so
// there is ONE way a subsystem opens its org DBs and ONE hand-rolled map is
// replaced by this shared value. T is the subsystem's own store handle (it owns
// its schema via the open func's migration); T must Close its DB.
//
// The key is the namespace and not the path because the namespace is the fact
// and the path is a rendering of it. Keyed by path, two spellings that render
// the same file would be two entries and therefore two open handles on one
// SQLite — which the at-rest cek layer does not support. Keyed by the value,
// that state cannot be constructed.
type OrgStore[T io.Closer] struct {
	dataDir   string
	subsystem string
	open      func(*sql.DB) (T, error)

	// dur (when non-nil) makes every per-org file HA-durable: forNS hydrates it
	// from the object store before opening and binds a Durable that Sync ships
	// fenced. nil ⇒ local-only, byte-identical to the pre-durability cache.
	//
	// It is READ from the deployment (Base.Durable), never chosen here. Whether
	// this deployment has an object store it can fence against is a fact about
	// the deployment, discovered once at boot by buildDurability; a subsystem
	// that got to answer it per store was answering a question it cannot know,
	// and eleven of the fifteen answered it by omission.
	dur *org.Durability
	log luxlog.Logger

	mu       sync.Mutex
	byNS     map[namespace.Namespace]T
	durables map[namespace.Namespace]*org.Durable  // parallel to byNS, populated only when dur != nil
	inflight map[namespace.Namespace]*openState[T] // durable opens in progress, deduped by namespace (M1)
}

// durableOpTimeout bounds every object-store round-trip a durable OrgStore makes
// (hydrate on open, ship on Sync, final ship on close). It exists so a slow or hung
// SeaweedFS degrades to a bounded latency (open read-only / ship not-acked) rather
// than blocking a caller — or the store-wide lock — indefinitely.
const durableOpTimeout = 30 * time.Second

// openState records a durable open in flight for one namespace, so concurrent For()
// calls for the SAME org wait on the ONE open (its done channel) instead of
// double-opening, WITHOUT any of them holding the store-wide c.mu across the
// object-store I/O.
type openState[T io.Closer] struct {
	done chan struct{}
	st   T
	d    *org.Durable
	err  error
}

// NewOrgStore builds a per-org store cache for subsystem, in the deployment b
// describes. open wraps a freshly-opened *sql.DB (already pragma'd by OrgDB)
// into the subsystem's store handle, running its migration; it is called once
// per org file.
//
// It takes the DEPLOYMENT and not three loose parameters because all three —
// where files live, whether there is an object store to fence against, where a
// degraded hydrate is reported — are facts of the deployment that Base already
// holds together. Handed to the store as options, they became a subsystem's
// opinion: eleven of the fifteen stores simply never passed WithDurable, so
// their files were local-only on a deployment whose whole point is that they are
// not. An option that every caller should pass identically is not an option.
func NewOrgStore[T io.Closer](b Base, subsystem string, open func(*sql.DB) (T, error)) *OrgStore[T] {
	return &OrgStore[T]{
		dataDir:   b.DataDir,
		subsystem: subsystem,
		open:      open,
		dur:       b.Durable,
		log:       b.Log,
		byNS:      map[namespace.Namespace]T{},
		durables:  map[namespace.Namespace]*org.Durable{},
		inflight:  map[namespace.Namespace]*openState[T]{},
	}
}

// For returns the store the namespace names, opening and migrating it on first
// use and caching it thereafter. Isolation is PHYSICAL: a distinct namespace
// resolves to a distinct file, so a query in one can never reach another's rows.
func (c *OrgStore[T]) For(ns namespace.Namespace) (T, error) { return c.forNS(ns) }

// Has reports whether the namespace ALREADY has a store for this subsystem on
// disk, without opening or creating anything.
//
// It exists for reads whose org is named by an UNAUTHENTICATED caller — a public
// gallery route addressed as /{org}/{project}, say. For calls MkdirAll and opens,
// so asking it about a name a stranger supplied would let that stranger mint an
// empty directory and an open handle per name they invent. Has answers the
// question that route actually has ("is there anything here?") without the side
// effect, so the caller can 404 a name that names nothing.
//
// An authenticated, org-scoped caller does NOT want this: its org is real by
// construction and its store must be created on first touch.
func (c *OrgStore[T]) Has(ns namespace.Namespace) bool {
	path, err := namespace.Path(c.dataDir, ns, c.subsystem)
	if err != nil {
		return false
	}
	return cek.Exists(path)
}

// forNS opens (and migrates on first use) the store the namespace names, caching
// by that namespace. It is the shared core of For and Each: the cache key is the
// name, so an org reached via For(org) and the SAME file reached via Each's
// enumeration resolve to the ONE handle — never a second open of the same file
// (which the at-rest cek layer does not support concurrently).
func (c *OrgStore[T]) forNS(ns namespace.Namespace) (T, error) {
	var zero T
	path, err := namespace.Path(c.dataDir, ns, c.subsystem)
	if err != nil {
		return zero, err
	}
	c.mu.Lock()
	if st, ok := c.byNS[ns]; ok {
		// Cache hit. If this durable store opened DEGRADED (read-only) and this replica
		// has since become the org's elected owner — a rolling-upgrade membership change
		// — promote it IN PLACE (M3): re-acquire the lease, hydrate the latest snapshot,
		// and swap in a writer handle, with no process restart. The check is I/O-free and
		// only does real work when the store is unowned AND newly elected (rare).
		d := c.durables[ns]
		if d == nil || !d.PendingPromotion() {
			c.mu.Unlock()
			return st, nil
		}
		if inf, promoting := c.inflight[ns]; promoting {
			c.mu.Unlock()
			<-inf.done
			return inf.st, inf.err
		}
		inf := &openState[T]{done: make(chan struct{})}
		c.inflight[ns] = inf
		delete(c.byNS, ns)
		delete(c.durables, ns)
		c.mu.Unlock()

		st2, d2, err := c.promote(ns, path, st, d)
		inf.st, inf.d, inf.err = st2, d2, err
		c.mu.Lock()
		delete(c.inflight, ns)
		if d2 != nil { // a usable store (promoted writer, or the kept read-only one)
			c.byNS[ns] = st2
			c.durables[ns] = d2
		}
		c.mu.Unlock()
		close(inf.done)
		if err != nil && d2 != nil && c.log != nil {
			// Reopen degraded again (transient store/membership blip): still serving the
			// prior read-only state, so log and keep availability rather than surface it.
			c.log.Warn("org store promotion incomplete — serving prior state", "subsystem", c.subsystem, "namespace", ns, "err", err)
			return st2, nil
		}
		return st2, err
	}
	// Local-only (no Durability): open under c.mu — disk I/O only, unchanged from the
	// pre-durability cache.
	if c.dur == nil {
		defer c.mu.Unlock()
		db, err := openOrgDB(nsPrincipal(ns), path)
		if err != nil {
			return zero, err
		}
		st, err := c.open(db)
		if err != nil {
			_ = db.Close()
			return zero, err
		}
		c.byNS[ns] = st
		return st, nil
	}
	// Durable: run the open (object-store hydrate + cek) with c.mu RELEASED so a
	// slow/hung SeaweedFS never stalls another org's open or a cache hit (M1). A
	// concurrent open of the SAME namespace waits on the in-flight record rather
	// than double-opening.
	if inf, ok := c.inflight[ns]; ok {
		c.mu.Unlock()
		<-inf.done
		return inf.st, inf.err
	}
	inf := &openState[T]{done: make(chan struct{})}
	c.inflight[ns] = inf
	c.mu.Unlock()

	inf.st, inf.d, inf.err = c.openDurable(ns, path)

	c.mu.Lock()
	delete(c.inflight, ns)
	if inf.err == nil {
		c.byNS[ns] = inf.st
		c.durables[ns] = inf.d
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
func (c *OrgStore[T]) openDurable(ns namespace.Namespace, path string) (T, *org.Durable, error) {
	var zero T
	// The election key is the ENTITY — the org — not the file: every one of an
	// org's project-scoped files is owned by the same elected writer, and the
	// shard router hashes the same id. The object key is namespace.Key, the SAME
	// rendering the local path came from, so the file and its remote slot cannot
	// name different things.
	dbKey, err := namespace.Key(ns, c.subsystem)
	if err != nil {
		return zero, nil, err
	}
	d := c.dur.For(ns.ID(), dbKey, path)
	ctx, cancel := context.WithTimeout(context.Background(), durableOpTimeout)
	err = d.Hydrate(ctx)
	cancel()
	if err != nil && c.log != nil {
		c.log.Warn("org store hydrate degraded — opening read-only", "subsystem", c.subsystem, "namespace", ns, "key", dbKey, "err", err)
	}
	db, err := openOrgDB(nsPrincipal(ns), path)
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
func (c *OrgStore[T]) promote(ns namespace.Namespace, path string, old T, oldDur *org.Durable) (T, *org.Durable, error) {
	ctx, cancel := context.WithTimeout(context.Background(), durableOpTimeout)
	claimed, err := oldDur.TryClaim(ctx)
	cancel()
	if err != nil || !claimed {
		return old, oldDur, err // not the owner yet / store blip: keep serving read-only.
	}
	_ = old.Close() // quiesce: release the read-only handle before CarryForward swaps the file.
	return c.openDurable(ns, path)
}

// Sync ships the org's local file to its durable object, fenced at the lease round
// (the ship-before-ack step a durable subsystem calls after a write commits). It
// returns acked=false when this replica is not the owner or was deposed mid-request
// (the caller retries on the new owner). On a local-only store (no Durability) it is
// a successful no-op — the write is already as durable as configured.
func (c *OrgStore[T]) Sync(ns namespace.Namespace) (acked bool, err error) {
	if c.dur == nil {
		return true, nil
	}
	c.mu.Lock()
	d := c.durables[ns]
	c.mu.Unlock()
	if d == nil {
		return false, fmt.Errorf("cloud: OrgStore.Sync for %s/%s before its store was opened", ns, c.subsystem)
	}
	ctx, cancel := context.WithTimeout(context.Background(), durableOpTimeout)
	defer cancel()
	return d.Sync(ctx)
}

// orgsRoot is the directory every namespace's file lives under: the first
// segment namespace.Key renders. Each and Stored walk it directly — they
// answer "WHICH entities have a store", which is a question about the disk
// and not about a name, so it is the one thing here that reads the layout
// instead of rendering it. TestOrgNamespaceNeutralisesTraversal holds the
// two spellings together by asserting a rendered key against this root.
const orgsRoot = "orgs"

// Each folds fn over every org that has a {subsystem} store on disk under
// {dataDir}/orgs, handing it that org's NAMESPACE and the SAME cached store
// handle For returns (opened through forNS, keyed by the namespace — no second
// open). It is the cross-org sweep primitive a reconciler folds over: the
// filesystem is the source of truth for "which orgs have this store", so no derived
// registry can drift. The reserved platform partitions ({dataDir}/orgs/_*) are
// skipped (their '_' is a rune namespace.Sanitize never emits, so no real org is
// dropped). A per-org OPEN failure is passed to fn as its err (fn decides skip vs.
// record); a missing orgs root (a writer with no stores yet) is not an error.
// Under horizontal sharding each writer's PVC holds only the orgs routed to it,
// so Each on a given writer enumerates exactly that writer's orgs.
//
// This is the one thing here that hanzoai/orm/db.Namespaces cannot do and that
// is not an oversight. That registry's whole surface is With, Open, Close: it
// resolves a name you already have to a handle, and deliberately knows nothing
// about which names exist, because knowing would mean holding a directory that
// can drift from the filesystem. Each answers the opposite question — WHICH
// entities have a store — and it answers it by reading the disk every time, so
// there is nothing to drift. A reconciler needs that question answered; until a
// shared registry offers it without keeping a second copy of the truth, this
// stays here rather than being pushed upstream as a convenience.
func (c *OrgStore[T]) Each(fn func(ns namespace.Namespace, st T, err error)) error {
	var zero T
	root := filepath.Join(c.dataDir, orgsRoot)
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
		if !cek.Exists(path) {
			continue // this org has no store for this subsystem
		}
		// e.Name() IS the on-disk org slug — the value OrgNamespace wrote there, and
		// the one the election hashes; Each is org-root scoped, so no project group.
		ns, err := nsOnDisk(e.Name())
		if err != nil {
			// A directory no namespace can name is not an org's store: it predates
			// the constructor or was written by hand. Skipping it silently would
			// hide a store the sweep is meant to reach, so it is reported.
			fn(namespace.Namespace{}, zero, fmt.Errorf("cloud: %q under %s names no namespace: %w", e.Name(), root, err))
			continue
		}
		st, openErr := c.forNS(ns)
		fn(ns, st, openErr)
	}
	return nil
}

// Stored reports whether ANY namespace already has a store for this subsystem on
// disk, without opening one.
//
// It is the boot question a reader replica asks — has my volume been hydrated at
// all? — and it has to be answerable without opening, because a reader that
// opened every org's file to find out would pay the whole fleet's I/O for one
// boolean. It is Each's walk without the opens, and it shares Each's rendering
// of where a store lives, so the two cannot disagree about what counts as one.
func (c *OrgStore[T]) Stored() bool {
	// The deployment's own partition counts: a volume holding only the system
	// namespace's file still has something to serve, and a boot check that said
	// otherwise would refuse to start a replica that was in fact hydrated.
	if c.Has(PlatformNamespace()) {
		return true
	}
	ents, err := os.ReadDir(filepath.Join(c.dataDir, orgsRoot))
	if err != nil {
		return false
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		ns, err := nsOnDisk(e.Name())
		if err != nil {
			continue
		}
		if path, err := namespace.Path(c.dataDir, ns, c.subsystem); err == nil && cek.Exists(path) {
			return true
		}
	}
	return false
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
	for _, st := range c.byNS {
		if err := st.Close(); err != nil && first == nil {
			first = err
		}
	}
	c.byNS = map[namespace.Namespace]T{}
	c.durables = map[namespace.Namespace]*org.Durable{}
	c.inflight = map[namespace.Namespace]*openState[T]{}
	return first
}
