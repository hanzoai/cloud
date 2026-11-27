package cloud

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	// cek is the ONE opener: cek derives this database's key from the process
	// master and the namespace that owns it, and opens the file under it. cloud
	// holds no key material and no crypto of its own.
	"github.com/hanzoai/cek"

	// sdk/tenure is the HA-durable substrate: ha election (WHO writes) + vfs
	// FencedStore ship/hydrate (HOW it ships) + envelope Cipher (at rest). Every
	// per-org file routes through it when the deployment has one.
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/tenure"
	"github.com/hanzoai/namespace"
	sqlitedrv "github.com/hanzoai/sqlite"
	luxlog "github.com/luxfi/log"
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

// OrgDB is the ONE way any cloud subsystem opens a per-entity database
// (HIP-0302 physical isolation). It resolves the namespace to its path, creates
// the parent directory 0700, opens via the sole "sqlite" driver under that
// namespace's own key, and pins the pool to one connection. The caller owns
// migration (its schema is its own) and Close.
//
// With CLOUD_SQL_DSN set, a ported subsystem's database is a schema on hanzo-sql
// (locus.Open) once it has been moved there or made there, and its SQLite file until
// then; an unported subsystem's is always its file (locus.File). A store's SQL on
// PostgreSQL is written in the dialect both engines share (see sdk/dialect); a
// store that is not fails at its first statement, loudly.
//
// It takes the NAME rather than the parts a name is made of, so it cannot pair
// one namespace's path with another namespace's key, and so the question "could
// this have come from caller input" is asked once — at OrgNamespace, the only
// entry point — instead of again at every subsystem that opens a file.
//
// Path convention — see namespace.Key:
//
//	org/{slug}            →  {DataDir}/orgs/{slug}/{subsystem}.db
//	org/{slug}/{project}  →  {DataDir}/orgs/{slug}/projects/{project}/{subsystem}.db
//	system                →  {DataDir}/orgs/_platform/{subsystem}.db
func OrgDB(dataDir string, ns namespace.Namespace, subsystem string) (*sql.DB, error) {
	return openOrgDB(ns, subsystem, dataDir)
}

// portedToSQL is the subsystems whose stores run on hanzo-sql: the ones whose SQL is
// in the dialect both engines share and whose suites are green on PostgreSQL
// (internal/sqltest). With CLOUD_SQL_DSN set, OrgDB runs no other on it. A subsystem joins
// when its port lands, in the commit that lands it; the list is the one place a
// reviewer sees what the switch would carry. It is manifest.SQLStores, which also
// names the app that opens each (the host divides the connection budget among them):
// "treasury" is the house book and "finance" and "finance-test" are every org's two
// customer books, the ledger.

// openOrgDB opens the SQLite file under the single-writer discipline every org
// store shares: one connection, which serializes writes against the file lock and
// makes a read-modify-write such as todo's per-project issue-number allocation
// a safe transaction. The WAL/foreign-key/busy-timeout pragmas this used to set by
// hand are the driver's, applied per connection (see locus). On hanzo-sql the
// same call is a pool on the store's schema, and the serialization is the
// database's (dialect.Lock).
//
// A subsystem that is not ported stays its file with the DSN set, and a store of one
// that the registry says is on hanzo-sql is refused, since opening its file would split
// it in two.
func openOrgDB(ns namespace.Namespace, subsystem, dir string) (*sql.DB, error) {
	open := locus.Open
	if locus.DSN() != "" && !onSQL(subsystem) {
		open = locus.File
	}
	db, err := open(ns, subsystem, dir)
	if err != nil {
		return nil, fmt.Errorf("cloud: OrgDB open %s/%s: %w", ns, subsystem, err)
	}
	return db, nil
}

// OrgReader opens a snapshot's own read-only handle on an org file, through the SAME cek
// derivation the store's handle came from — cloud names the database and holds no key.
// A durable ship holds a read transaction on it while it copies the file, so the store
// keeps its own connection and the copy reads a file nothing is moving (sdk/tenure,
// pin). It is opened per ship and closed with it; nothing else may hold it.
//
// It answers (nil, nil) when the codec is not linked. That build keeps a keyed database
// as an envelope — one plaintext copy on tmpfs per handle, encrypted back to the real
// path at checkpoint — so the real path is not a database a second handle could open, and
// a second envelope open would be a second plaintext copy of which only the last to close
// survives. The linked codec keeps the database live on disk in SQLCipher pages, where a
// second connection is what SQLite is built for. Every binary that opens an org file is
// built against it (the plugins; cmd/cloud opens none), so this is the shipped path and
// the other answer is the honest one for a build that cannot take it.
func OrgReader(ns namespace.Namespace, subsystem, path string) (*sql.DB, error) {
	if !sqlitedrv.CodecLinked() {
		return nil, nil
	}
	db, err := cek.OpenAt(ns, subsystem, path)
	if err != nil {
		return nil, fmt.Errorf("cloud: org reader open %s/%s: %w", ns, subsystem, err)
	}
	locus.Single(db)
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
// the same file would be two entries and therefore two STORE handles on one
// SQLite — two writers, and on the envelope backend two plaintext copies of
// which only the last to close survives. (OrgReader's read-only handle is not
// one of these: it writes nothing, it exists only for the length of a ship, and
// it opens only on the backend that keeps the database live on disk.) Keyed by the value,
// that state cannot be constructed.
type OrgStore[T io.Closer] struct {
	dataDir   string
	subsystem string
	open      func(namespace.Namespace, *sql.DB) (T, error)

	// dur (when non-nil) makes every per-org file HA-durable: forNS hydrates it
	// from the object store before opening and binds a Durable that Sync ships
	// fenced. nil ⇒ local-only, byte-identical to the pre-durability cache.
	//
	// It is READ from the deployment (Base.Durable), never chosen here. Whether
	// this deployment has an object store it can fence against is a fact about
	// the deployment, discovered once at boot by buildDurability; a subsystem
	// that got to answer it per store was answering a question it cannot know,
	// and eleven of the fifteen answered it by omission.
	dur *tenure.Durability
	// sql is set when this subsystem's stores live on hanzo-sql (CLOUD_SQL_DSN set and
	// the subsystem ported): the database is then the one writer and the one copy, so
	// there is no file to hydrate, ship, fence or mark, and dur is nil by construction.
	// A store of it that has not been moved yet is still its file, opened as a file is
	// with no durability (the single replica's volume is its copy until it moves; see
	// locus.Move). It is read once, at construction, and never changes.
	sql bool
	// stop ends the moves in flight and the sweep that makes them: CloseAll cancels it.
	stop context.Context
	halt context.CancelFunc
	// swept is closed when the sweep that moves stores has ended; nil when there is none.
	swept chan struct{}
	// holder names this OrgStore as a lease holder: random, so two replicas never
	// share one, and two OrgStores in one process (a test's two replicas) do not.
	holder string
	// sqlSeen is every namespace the registry has named so far and sqlLoadedAt is when
	// it was last read: the registry is read again every registryEvery, so a store
	// another replica made is swept here too, and a namespace this process has
	// settled stays settled. Held by mu.
	sqlSeen     map[namespace.Namespace]bool
	sqlLoadedAt time.Time
	// leases is, per namespace, until when this OrgStore may treat the store's lease as
	// its own, by the local clock and conservatively. Held by leaseMu.
	leaseMu sync.Mutex
	leases  map[namespace.Namespace]time.Time
	// denied is, per namespace, until when a refusal stands: the holder's own term or
	// denialMax, whichever is shorter. leaseWarned is when a lease problem was last
	// said. Both held by leaseMu.
	denied      map[namespace.Namespace]time.Time
	leaseWarned time.Time
	// leaseAsked is set once this OrgStore has asked the database for any lease. One that
	// never did has nothing to give back, and CloseAll must not reach for the database to
	// say so. Held by leaseMu.
	leaseAsked bool
	// peered is Base.Peered — the deployment has more than one writer now. Asked
	// only where dur is nil, which is the one case Sync cannot ask a fence about,
	// and asked each time, because a second pod can join after this store opened.
	peered func() bool
	log    luxlog.Logger

	mu       sync.Mutex
	byNS     map[namespace.Namespace]T
	durables map[namespace.Namespace]*tenure.Durable // parallel to byNS, populated only when dur != nil
	inflight map[namespace.Namespace]*openState[T]   // durable opens in progress, deduped by namespace (M1)
	// used is when each open store was last handed to a caller — the input to the
	// idle deadline reclaim evicts on. Held by mu, cleared with byNS.
	used map[namespace.Namespace]time.Time
	// pending is the set of namespaces a sweep visits: the ones a caller has
	// asked for and no sweep has since proven have nothing left to do. asked is
	// when For last handed each one out. Both held by mu; pendingOnce reads the
	// set off disk the first time anything needs it.
	pending          map[namespace.Namespace]bool
	asked            map[namespace.Namespace]time.Time
	localPendingOnce sync.Once
	pendingOnce      sync.Once
	// maxOpen is the TARGET size of the open set; 0 disables reclaim entirely,
	// which is what a caller that genuinely wants every entity resident sets.
	// idleAfter is how long a store must go untouched before it may be released:
	// For hands out a bare handle, so anything more recent than this may still be
	// in a caller's hands. Together they are the whole policy.
	maxOpen   int
	idleAfter time.Duration
	// over is set while the open set is over maxOpen with nothing idle enough to
	// release, and warned is when that was last said. The warning is said when the
	// store enters that state and again only once per idleAfter while it stays,
	// never on every For; leaving it is said once. Both held by mu.
	over   bool
	warned time.Time
	// evictions counts stores released by reclaim, so a test can tell "the bound
	// held because nothing needed evicting" from "the bound held because eviction
	// works" — two very different green runs.
	evictions atomic.Int64
	// shipEvery is how often the open durable stores that changed are shipped (see
	// shipLoop); shipping starts with the first durable open and stops at CloseAll,
	// which closes done.
	shipEvery time.Duration
	shipOnce  sync.Once
	stopOnce  sync.Once
	done      chan struct{}
	// closed is set by CloseAll and never cleared, which is what makes closing
	// TERMINAL rather than a reset. Every caller of CloseAll is a Shutdown path,
	// and a request still in flight during a rollout reaches For() after it: with
	// the maps merely emptied, that request opened the file again — on a durable
	// deployment re-hydrating it and re-claiming the fence lease the SUCCESSOR pod
	// is claiming at that moment, which is two live writers for one org arriving
	// through a handle nobody thought was reachable. Held by mu.
	closed bool
}

// ErrStoreClosed is what every entry point that opens an org file answers after
// CloseAll. It is an ERROR and not a silent no-op because a caller that arrives
// after shutdown is a fact worth surfacing: the request fails, the operator sees
// why, and nothing resurrects.
var ErrStoreClosed = errors.New("cloud: org store is closed")

// durableOpTimeout bounds every object-store round-trip a durable OrgStore makes
// (hydrate on open, ship on Sync, final ship on close). It exists so a slow or hung
// object store degrades to a bounded latency (open read-only / ship not-acked) rather
// than blocking a caller — or the store-wide lock — indefinitely.
const durableOpTimeout = 30 * time.Second

// openState records a durable open in flight for one namespace, so concurrent For()
// calls for the SAME org wait on the ONE open (its done channel) instead of
// double-opening, WITHOUT any of them holding the store-wide c.mu across the
// object-store I/O.
type openState[T io.Closer] struct {
	done chan struct{}
	st   T
	d    *tenure.Durable
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
	return NewOrgStoreAt(b, subsystem, func(_ namespace.Namespace, db *sql.DB) (T, error) { return open(db) })
}

// NewOrgStoreAt is NewOrgStore for an open func that needs the namespace its database is
// for. A store that seals a column binds every ciphertext to its namespace, and a row of
// a per-org store carries no org of its own to bind to: the namespace is the org, and only
// the opener knows it. Every other store opens as before.
func NewOrgStoreAt[T io.Closer](b Base, subsystem string, open func(namespace.Namespace, *sql.DB) (T, error)) *OrgStore[T] {
	c := &OrgStore[T]{
		dataDir:   b.DataDir,
		subsystem: subsystem,
		open:      open,
		dur:       b.Durable,
		sql:       locus.DSN() != "" && onSQL(subsystem),
		peered:    b.Peered,
		log:       b.Log,
		byNS:      map[namespace.Namespace]T{},
		durables:  map[namespace.Namespace]*tenure.Durable{},
		inflight:  map[namespace.Namespace]*openState[T]{},
		used:      map[namespace.Namespace]time.Time{},
		pending:   map[namespace.Namespace]bool{},
		asked:     map[namespace.Namespace]time.Time{},
		maxOpen:   OpenStores(),
		idleAfter: StoreIdleAfter,
		shipEvery: StoreShipEvery,
		done:      make(chan struct{}),
	}
	c.stop, c.halt = context.WithCancel(context.Background())
	if c.sql {
		c.dur = nil // the database is the durability; nothing ships
		c.holder = rand.Text()
		c.sqlSeen = map[namespace.Namespace]bool{}
		c.leases = map[namespace.Namespace]time.Time{}
		c.denied = map[namespace.Namespace]time.Time{}
		c.sweepMoves()
	} else {
		c.rekeyHeld()
	}
	return c
}

// rekeyHeld moves every store of this subsystem on this disk that is still under
// a key held beside it (a .dek sidecar) to its derived key, before anything here
// opens one. A store moves on its open too (rekey), but a store nobody opens
// would keep a snapshot no pod without this disk can read; this is where those
// move. It runs at construction, when no handle and no hydrate can race it, and
// once every store has moved it is a stat per org.
func (c *OrgStore[T]) rekeyHeld() {
	ents, err := os.ReadDir(filepath.Join(c.dataDir, orgsRoot))
	if err != nil {
		return
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		var ns namespace.Namespace
		switch name := e.Name(); {
		case name == "_platform":
			ns = namespace.System()
		case strings.HasPrefix(name, "_"):
			continue
		default:
			if ns, err = principal.OrgOnDisk(name); err != nil {
				continue
			}
		}
		path, err := namespace.Path(c.dataDir, ns, c.subsystem)
		if err != nil || !exists(path+".dek") {
			continue
		}
		c.rekey(ns, path)
	}
}

// StoreShipEvery bounds how far a durable object may trail its local file. The
// local file and its -wal hold every committed write and SQLite replays the -wal
// when the database opens, so this is not what keeps a write: it is how long a
// write lives on one volume only. A host process can be stopped without warning,
// so the final ship at close is not one to count on.
const StoreShipEvery = time.Minute

// MaxOpenPerStore bounds how many per-entity SQLite handles ONE OrgStore keeps
// open at a time. It is a CEILING on unbounded growth, not a tuning knob: a
// deployment serving fewer entities than this never reaches it and never evicts.
//
// It exists because the cache had no upper bound at all. Every namespace ever
// asked for kept its handle — and its file descriptor, its page cache and, on the
// durable plane, its lease — for the life of the process, so a host's memory was
// a function of how many tenants had ever touched it rather than of how many were
// active. That is the opposite of the property the durable plane is built for: an
// org's state is re-derivable from the object store, which is exactly what makes
// a pod disposable, and holding every org open forever spends that property.
//
// It is PER STORE and a binary runs one per subsystem, so the process-wide worst
// case is this times the number of subsystems that keep per-entity files. Sized
// so that is comfortable rather than tight — the value here is that the number is
// FINITE, and the eviction below is what has to be correct.
//
// It is the DEFAULT, and a deployment states its own (CLOUD_OPEN_STORES, read by
// OpenStores). The bound must cover every namespace a store is asked for within
// StoreIdleAfter, because that is the only kind of store reclaim may take: a sweep
// that visits more orgs than the bound inside that window evicts each one just
// before the sweep comes back to it, so every pass ships and re-hydrates nearly
// every org.
const MaxOpenPerStore = 256

// StoreIdleAfter is how long a per-entity store must go untouched before reclaim
// may release it. It is the SAFETY half of the bound above, not a tuning knob:
// For returns a bare handle that its caller uses after the call returns, so a
// store touched more recently than this may still be in someone's hands and
// closing it is a use-after-close on a live database.
//
// Five minutes is chosen to be far longer than any request that holds one of
// these handles, so a store past it is one nobody is inside — the same reasoning
// (and the same shape) as the plugin reaper's own idle bound.
const StoreIdleAfter = 5 * time.Minute

// For returns the store the namespace names, opening and migrating it on first
// use and caching it thereafter. Isolation is PHYSICAL: a distinct namespace
// resolves to a distinct file, so a query in one can never reach another's rows.
//
// It also marks the namespace pending, so the sweeps (Each) visit it until one of
// them Settles it. That is the only way a namespace joins a sweep: an org's
// database opens because that org asked for it, never because a sweep walked the
// disk.
func (c *OrgStore[T]) For(ns namespace.Namespace) (T, error) {
	st, err := c.forNS(ns)
	if err == nil {
		c.mark(ns)
	}
	return st, err
}

// mark records that ns was asked for, and makes it pending on disk the first
// time, so a restart still knows which orgs have work a sweep must see.
//
// It loads only the LOCAL disk's pending set: listing the object store is a
// sweep's job ([OrgStore.Each]), and running it on For puts a bucket-wide LIST
// on the first live request of a cold pod.
func (c *OrgStore[T]) mark(ns namespace.Namespace) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return
	}
	c.loadLocalPending()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.asked[ns] = time.Now()
	if c.pending[ns] {
		return
	}
	// A store that is a file keeps its marker, as it always has, so a restart still knows
	// which orgs have work. One that is on hanzo-sql has none to keep: the registry
	// already names it.
	if path, err := c.pendingPath(ns); err == nil && (!c.sql || exists(strings.TrimSuffix(path, ".pending"))) {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_ = f.Close()
		} else if c.log != nil && !c.sql {
			c.log.Warn("org store: pending mark not written", "subsystem", c.subsystem, "namespace", ns, "err", err)
		}
	}
	c.pending[ns] = true
}

// Settle takes ns out of the sweeps. A sweep calls it after proving, with a
// query that began at since, that the namespace has nothing left for any sweep
// to do. It is refused while a caller may still hold a handle For gave out
// within StoreIdleAfter of since — the work that caller is about to write would
// otherwise land in an org no sweep visits. The next For marks it again.
func (c *OrgStore[T]) Settle(ns namespace.Namespace, since time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.pending[ns] || c.asked[ns].Add(StoreIdleAfter).After(since) {
		return
	}
	if path, err := c.pendingPath(ns); err == nil {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return // still marked on disk, so still pending
		}
	}
	delete(c.pending, ns)
	delete(c.asked, ns)
}

// pendingPath is the marker beside the namespace's own database file.
func (c *OrgStore[T]) pendingPath(ns namespace.Namespace) (string, error) {
	path, err := namespace.Path(c.dataDir, ns, c.subsystem)
	if err != nil {
		return "", err
	}
	return path + ".pending", nil
}

// loadLocalPending reads the pending set off the local disk, once, without
// touching the object store.
func (c *OrgStore[T]) loadLocalPending() {
	c.localPendingOnce.Do(func() {
		root := filepath.Join(c.dataDir, orgsRoot)
		seen := filepath.Join(root, "_pending", c.subsystem)
		// A store on hanzo-sql visits no file, so with the DSN set the markers of the
		// stores that are still files are read and nothing is written: a volume this
		// release has never swept is marked by the file plane, and hanzo-sql leaves an
		// empty one empty.
		fresh := !exists(seen) && !c.sql
		ents, err := os.ReadDir(root)
		if err != nil && !os.IsNotExist(err) {
			if c.log != nil {
				c.log.Warn("org store: read pending", "subsystem", c.subsystem, "err", err)
			}
			return
		}
		found := map[namespace.Namespace]bool{}
		for _, e := range ents {
			if !e.IsDir() || strings.HasPrefix(e.Name(), "_") {
				continue
			}
			ns, err := principal.OrgOnDisk(e.Name())
			if err != nil {
				continue
			}
			path, err := c.pendingPath(ns)
			if err != nil {
				continue
			}
			if exists(path) {
				found[ns] = true
				continue
			}
			if fresh && c.here(ns) {
				if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
					_ = f.Close()
					found[ns] = true
				}
			}
		}
		if fresh && c.dur == nil {
			if err := os.MkdirAll(filepath.Dir(seen), 0o700); err == nil {
				if f, err := os.OpenFile(seen, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
					_ = f.Close()
				}
			}
		}
		c.mu.Lock()
		for ns := range found {
			c.pending[ns] = true
		}
		c.mu.Unlock()
	})
}

// registryEvery is how often a sweep reads the registry again.
const registryEvery = 30 * time.Second

// loadSQLPending reads the pending set off the registry: every store this subsystem
// has on hanzo-sql. There is no disk to read and no marker to keep, so the set lives in
// memory and a restart rebuilds it from the registry, as a fresh durable volume
// rebuilds it from the object store.
//
// It is read again every registryEvery, and only a namespace not seen before becomes
// pending again: a store another replica made is swept here too, and one this
// process settled is not put back by a registry that still lists it. A registry that
// cannot be read leaves the set to what has been asked for, and the next sweep asks
// again.
func (c *OrgStore[T]) loadSQLPending() {
	c.loadLocalPending() // the stores that are still files keep the markers they always had
	c.mu.Lock()
	fresh := time.Since(c.sqlLoadedAt) < registryEvery
	c.mu.Unlock()
	if fresh {
		return
	}
	named, err := locus.Stores(c.subsystem)
	if err != nil {
		if c.log != nil {
			c.log.Warn("org store: the registry did not name its stores; sweeps visit only what is asked for until it does", "subsystem", c.subsystem, "err", err)
		}
		return
	}
	c.mu.Lock()
	for _, text := range named {
		ns, err := principal.OrgOnServer(text)
		if err != nil || c.sqlSeen[ns] { // a row this version cannot name is not a store it can open
			continue
		}
		c.sqlSeen[ns] = true
		c.pending[ns] = true
	}
	c.sqlLoadedAt = time.Now()
	c.mu.Unlock()
}

// loadPending reads the pending set off disk and, on a fresh durable volume,
// from the object store, once. A volume this release has never swept carries no
// markers, so every org that has this store is marked once; the first sweep
// settles the ones with nothing to do.
func (c *OrgStore[T]) loadPending() {
	if c.sql {
		c.loadSQLPending()
		return
	}
	c.loadLocalPending()
	c.pendingOnce.Do(func() {
		if c.dur == nil {
			return
		}
		root := filepath.Join(c.dataDir, orgsRoot)
		seen := filepath.Join(root, "_pending", c.subsystem)
		if exists(seen) {
			return
		}
		// A disk this release has never swept, on the durable plane, may be a new
		// pod's: every org whose store is in the object store is marked, as every
		// org on an old volume is (loadLocalPending asks only the disk, since this
		// one listing names the rest), and the first sweep settles the idle ones.
		// Without this a pod on an empty disk swept nothing — no meter, no reaper —
		// until each org's next request happened to open its store.
		named := true
		orgs, err := c.remote()
		if err != nil {
			named = false // seen stays unwritten, so the next process asks again
			if c.log != nil {
				c.log.Error("org store: the object store did not name its stores; sweeps visit only what is asked for until it does", "subsystem", c.subsystem, "err", err)
			}
		}
		found := map[namespace.Namespace]bool{}
		for _, ns := range orgs {
			path, err := c.pendingPath(ns)
			if err != nil {
				continue
			}
			if exists(path) {
				found[ns] = true
				continue
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				continue
			}
			if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
				_ = f.Close()
				found[ns] = true
			}
		}
		if named {
			if err := os.MkdirAll(filepath.Dir(seen), 0o700); err == nil {
				if f, err := os.OpenFile(seen, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
					_ = f.Close()
				}
			}
		}
		c.mu.Lock()
		for ns := range found {
			c.pending[ns] = true
		}
		c.mu.Unlock()
	})
}

// touch records that ns was just used. Held by c.mu.
func (c *OrgStore[T]) touch(ns namespace.Namespace) { c.used[ns] = time.Now() }

// reclaim releases stores that have gone IDLE, oldest first, while the open set
// is over maxOpen. It runs after every For, where in the ordinary case it is a
// length check and a return.
//
// AN EVICTION IS A DRAIN OF ONE ORG, NOT A CLOSED FILE HANDLE. It takes exactly
// the path CloseAll takes — the Durable's own Close, which ships the final state
// fenced at the lease round and releases ownership — because a handle closed out
// from under the fence leaves an org owned by a replica that is no longer serving
// it, and the next writer waits for a lease nobody will release.
//
// IDLE TIME IS THE CRITERION, AND RANK IS NOT. For returns a bare handle and the
// caller uses it after the call returns, so evicting the least recently USED
// store closes a database out from under whoever is holding it: under load a
// store touched a millisecond ago is still the coldest of N, and a rank-ordered
// eviction picks it. That is not a theoretical race — it was measured on the
// first draft of this function, which lost 21 acknowledged writes across a 40-org
// run and answered `sql: database is closed` to four live writers. A store
// touched within idleAfter is therefore NEVER a candidate, however many others
// arrive: no request holds a handle for minutes, so a store past that deadline is
// one nobody is inside.
//
// IT WILL EXCEED THE BOUND RATHER THAN TAKE A LIVE STORE, and that direction is
// chosen. If nothing is idle enough, the open set stays over maxOpen and says so
// — memory grows, which is visible and recoverable, where a database closed under
// a writer loses a tenant's data, which is neither. maxOpen is a target that
// idleAfter enforces, not a ceiling the process may violate correctness to hold.
//
// THE SHIP RACES A RE-OPEN, AND THE FENCE ANSWERS IT. The victim leaves byNS
// before its state ships, so a request arriving for that org in the interval
// opens it again and claims a strictly HIGHER round; the evicting ship then loses
// its conditional PUT and is refused rather than applied, which is the correct
// outcome and the reason eviction routes through the fence instead of around it.
// No acknowledged write is at stake either way — ship-before-ack has already
// shipped every write a caller was told succeeded.
//
// A namespace with an open in flight is never chosen, so eviction cannot race the
// open it would evict. The victim is picked by a scan rather than by a linked
// list: the set is bounded and a scan happens only when the bound is already
// exceeded, so the list's bookkeeping would cost more than it saves.
func (c *OrgStore[T]) reclaim() {
	for {
		c.mu.Lock()
		if c.closed || c.maxOpen <= 0 || len(c.byNS) <= c.maxOpen {
			if c.over && !c.closed && c.log != nil {
				c.log.Info("org stores back within the open bound",
					"subsystem", c.subsystem, "open", len(c.byNS), "bound", c.maxOpen)
			}
			c.over = false
			c.mu.Unlock()
			return
		}
		cutoff := time.Now().Add(-c.idleAfter)
		var victim namespace.Namespace
		var oldest time.Time
		found := false
		for ns := range c.byNS {
			if _, busy := c.inflight[ns]; busy {
				continue
			}
			u := c.used[ns]
			if u.After(cutoff) { // in use recently enough that a caller may hold it
				continue
			}
			if !found || u.Before(oldest) {
				victim, oldest, found = ns, u, true
			}
		}
		if !found {
			// Over the bound with nothing idle: every open store is live. Holding
			// them is the safe answer, and the operator is the one who can act on it
			// — by raising CLOUD_OPEN_STORES to the namespaces this store serves
			// within idle_after. Said on entering the state and once per idle_after
			// while it lasts: this runs on every For, and a line per call buried the
			// one fact in hundreds of copies of it.
			now := time.Now()
			if c.log != nil && (!c.over || now.Sub(c.warned) >= c.idleAfter) {
				c.log.Warn("org stores over the open bound and all in use — holding them rather than closing a live database; raise CLOUD_OPEN_STORES",
					"subsystem", c.subsystem, "open", len(c.byNS), "bound", c.maxOpen, "idle_after", c.idleAfter)
				c.warned = now
			}
			c.over = true
			c.mu.Unlock()
			return
		}
		st, d := c.byNS[victim], c.durables[victim]
		delete(c.byNS, victim)
		delete(c.durables, victim)
		delete(c.used, victim)
		c.mu.Unlock()

		if d != nil {
			ctx, cancel := context.WithTimeout(context.Background(), durableOpTimeout)
			_ = d.Close(ctx) // final fenced ship + lease release
			cancel()
		}
		_ = st.Close()
		c.evictions.Add(1)
		if c.log != nil {
			c.log.Debug("org store evicted", "subsystem", c.subsystem, "namespace", victim)
		}
	}
}

// Has reports whether the namespace ALREADY has a store for this subsystem —
// open here, on disk, or (on the durable plane) in the object store — without
// opening or creating anything.
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
//
// The object store is asked only when the disk says no, and that is not an edge
// case: a pod on an empty disk holds none of the deployment's stores until each
// is opened, and a disk-only answer told such a pod's readers (a KMS read, a
// public gallery) that an org with data had none. An object store that cannot
// answer is a no, said in the log: the caller's no is "not found", never a file
// minted for a name nobody has shown exists.
func (c *OrgStore[T]) Has(ns namespace.Namespace) bool {
	if c.here(ns) {
		return true
	}
	if c.dur == nil {
		return false
	}
	key, err := namespace.Key(ns, c.subsystem)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), hasReach)
	defer cancel()
	held, err := c.dur.Holds(ctx, key)
	if err != nil && c.log != nil {
		c.log.Warn("org store: the object store could not say whether a store exists", "subsystem", c.subsystem, "namespace", ns, "err", err)
	}
	return held
}

// here reports whether this pod holds the namespace's store: open, or on disk.
func (c *OrgStore[T]) here(ns namespace.Namespace) bool {
	c.mu.Lock()
	_, open := c.byNS[ns]
	c.mu.Unlock()
	if open {
		return true
	}
	if path, err := namespace.Path(c.dataDir, ns, c.subsystem); err == nil && exists(path) {
		return true
	}
	if c.sql {
		ok, err := locus.Exists(ns, c.subsystem)
		return err == nil && ok
	}
	return false
}

// openWait is how long a caller waits on the open of a store another caller is already
// making, on hanzo-sql: for a store coming off its file, that is the length of its move.
// A variable so this package's own tests can shorten it.
var openWait = time.Minute

// hasReach bounds Has's question to the object store. It sits on a read's path,
// so it is short; a store slower than this is a store that said no.
const hasReach = 5 * time.Second

// remote names every org that has this subsystem's store in the object store:
// keys orgs/<slug>/<subsystem>.db, the layout namespace.Key renders for an org.
// The system namespace and project-scoped files are not sweep units (loadPending
// reads org directories), so they are not named.
func (c *OrgStore[T]) remote() ([]namespace.Namespace, error) {
	ctx, cancel := context.WithTimeout(context.Background(), durableOpTimeout)
	defer cancel()
	keys, err := c.dur.Keys(ctx, orgsRoot+"/")
	if err != nil {
		return nil, err
	}
	var out []namespace.Namespace
	for _, k := range keys {
		parts := strings.Split(k, "/")
		if len(parts) != 3 || parts[2] != c.subsystem+".db" || strings.HasPrefix(parts[1], "_") {
			continue
		}
		ns, err := principal.OrgOnDisk(parts[1])
		if err != nil {
			continue
		}
		if key, err := namespace.Key(ns, c.subsystem); err != nil || key != k {
			continue // a slug that does not render back to its own key names no store
		}
		out = append(out, ns)
	}
	return out, nil
}

// exists reports whether a database file is on disk.
//
// It is the disk half of Has, and it is only half on purpose: the pure-Go codec
// holds a database in its envelope and writes the real file back when the handle
// CLOSES, so a store that is open right now has nothing here to find. The other
// half is the open set above — the handle is the only thing that knows about a
// store no byte of which has been sealed yet, and a sweep that consulted only
// the disk would skip precisely the stores that are in use.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// forNS opens (and migrates on first use) the store the namespace names, caching
// by that namespace. It is the shared core of For and Each: the cache key is the
// name, so an org reached via For(org) and the SAME file reached via Each's
// enumeration resolve to the ONE handle — never a second open of the same file
// (which the at-rest cek layer does not support concurrently).
func (c *OrgStore[T]) forNS(ns namespace.Namespace) (T, error) {
	// One reclaim site, after the open resolves and every lock this function takes
	// is released (the local-only path unlocks via its own defer, which LIFO puts
	// ahead of this one). On a cache hit inside the bound it is a length check.
	defer c.reclaim()
	var zero T
	var path string // the file; hanzo-sql has none
	if !c.sql {
		var err error
		if path, err = namespace.Path(c.dataDir, ns, c.subsystem); err != nil {
			return zero, err
		}
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return zero, fmt.Errorf("%w: %s/%s", ErrStoreClosed, ns, c.subsystem)
	}
	if st, ok := c.byNS[ns]; ok {
		// Cache hit. If this durable store opened DEGRADED (read-only) and this replica
		// has since become the org's elected owner — a rolling-upgrade membership change
		// — promote it IN PLACE (M3): re-acquire the lease, hydrate the latest snapshot,
		// and swap in a writer handle, with no process restart. The check is I/O-free and
		// only does real work when the store is unowned AND newly elected (rare).
		d := c.durables[ns]
		if d == nil || !d.PendingPromotion() {
			c.touch(ns)
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

		// DEFERRED, so the record is retired and the channel closed on EVERY exit
		// — a panic in promote included. Reached only by a normal return, a panic
		// here leaves the in-flight entry in place with its channel never closed;
		// and because byNS was just dropped above, every later caller for this
		// namespace falls through to the durable path, finds that entry, and
		// blocks on <-inf.done for the life of the process. One transient failure
		// would wedge one tenant permanently, with nothing in the log to say so.
		defer func() {
			// A panic leaves inf zero-valued. Waiters must get an error rather
			// than a nil store that reads as a successful open.
			if inf.err == nil && inf.d == nil {
				inf.err = fmt.Errorf("cloud: org store open abandoned: %s/%s", ns, c.subsystem)
			}
			c.mu.Lock()
			delete(c.inflight, ns)
			if inf.d != nil { // a usable store (promoted writer, or the kept read-only one)
				c.byNS[ns] = inf.st
				c.durables[ns] = inf.d
				c.touch(ns)
			}
			c.mu.Unlock()
			close(inf.done)
		}()

		st2, d2, err := c.promote(ns, path, st, d)
		inf.st, inf.d, inf.err = st2, d2, err
		if err != nil && d2 != nil && c.log != nil {
			// Reopen degraded again (transient store/membership blip): still serving the
			// prior read-only state, so log and keep availability rather than surface it.
			c.log.Warn("org store promotion incomplete — serving prior state", "subsystem", c.subsystem, "namespace", ns, "err", err)
			return st2, nil
		}
		return st2, err
	}
	if c.sql {
		return c.openSQL(ns) // releases c.mu
	}
	// Local-only (no Durability): open under c.mu — disk I/O only, unchanged from the
	// pre-durability cache.
	if c.dur == nil {
		defer c.mu.Unlock()
		c.rekey(ns, path)
		db, err := openOrgDB(ns, c.subsystem, c.dataDir)
		if err != nil {
			return zero, err
		}
		st, err := c.open(ns, db)
		if err != nil {
			_ = db.Close()
			return zero, err
		}
		c.byNS[ns] = st
		c.touch(ns)
		return st, nil
	}
	// Durable: run the open (object-store hydrate + cek) with c.mu RELEASED so a
	// slow/hung object store never stalls another org's open or a cache hit (M1). A
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

	// Deferred for the same reason as the promotion path above: openDurable runs
	// the object-store hydrate and the caller-supplied open, so a panic in either
	// must still retire the record and close the channel. Otherwise every later
	// caller for this namespace waits on a channel nobody will ever close.
	defer func() {
		if inf.err == nil && inf.d == nil {
			inf.err = fmt.Errorf("cloud: org store open abandoned: %s/%s", ns, c.subsystem)
		}
		c.mu.Lock()
		delete(c.inflight, ns)
		if inf.err == nil {
			c.byNS[ns] = inf.st
			c.durables[ns] = inf.d
			c.touch(ns)
		}
		c.mu.Unlock()
		close(inf.done)
	}()

	inf.st, inf.d, inf.err = c.openDurable(ns, path)
	return inf.st, inf.err
}

// openSQL opens the store on hanzo-sql, with c.mu held on entry and released before
// it returns.
//
// It is the durable open's shape without its object store: the first open of a
// store is a round trip to make its schema and run its migration, so it runs with
// the store-wide lock RELEASED — a slow database must not stall another org's cache
// hit — and a concurrent open of the same namespace waits on this one rather than
// migrating twice. There is no Durable to bind and nothing to ship.
//
// It is also where a store comes off its file: with CLOUD_SQL_MOVE set, locus.Use
// moves a store that is still a file before it opens it, and the callers that arrive
// meanwhile are the ones waiting on this record. That is the write exclusion of a
// move: no handle on the file exists in this process, and none is handed out until
// the store is on hanzo-sql (or, if the move failed, on its file again).
func (c *OrgStore[T]) openSQL(ns namespace.Namespace) (T, error) {
	var zero T
	if inf, ok := c.inflight[ns]; ok {
		c.mu.Unlock()
		// The open in flight may be a move of the store from its file, which is as long as
		// the store is big. A caller waits for it up to openWait and no longer: past that
		// it is told the store is being moved, which is true, and tries again.
		wait := time.NewTimer(openWait)
		defer wait.Stop()
		select {
		case <-inf.done:
			return inf.st, inf.err
		case <-wait.C:
			return zero, fmt.Errorf("%w: %s/%s", locus.ErrMoving, ns, c.subsystem)
		}
	}
	inf := &openState[T]{done: make(chan struct{})}
	c.inflight[ns] = inf
	c.mu.Unlock()

	// Deferred, so the record is retired and its channel closed on EVERY exit; see
	// forNS for what a panic here would otherwise leave behind.
	opened := false
	defer func() {
		if !opened && inf.err == nil {
			inf.err = fmt.Errorf("cloud: org store open abandoned: %s/%s", ns, c.subsystem)
		}
		c.mu.Lock()
		delete(c.inflight, ns)
		switch {
		case inf.err != nil:
		case c.closed: // CloseAll ran while this opened; it must not outlive it
			_ = inf.st.Close()
			inf.st, inf.err = zero, fmt.Errorf("%w: %s/%s", ErrStoreClosed, ns, c.subsystem)
		default:
			c.byNS[ns] = inf.st
			c.touch(ns)
		}
		c.mu.Unlock()
		close(inf.done)
	}()

	st, err := locus.Use(c.stop, c.log, ns, c.subsystem, c.dataDir, func(db *sql.DB) (T, error) { return c.open(ns, db) })
	if err != nil {
		inf.err = fmt.Errorf("cloud: OrgDB open %s/%s: %w", ns, c.subsystem, err)
		return zero, inf.err
	}
	inf.st, opened = st, true
	return st, nil
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
func (c *OrgStore[T]) openDurable(ns namespace.Namespace, path string) (T, *tenure.Durable, error) {
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
	d := c.dur.For(ns, c.subsystem, dbKey, path)
	ctx, cancel := context.WithTimeout(context.Background(), durableOpTimeout)
	err = d.Hydrate(ctx)
	cancel()
	if err != nil && c.log != nil {
		c.log.Warn("org store hydrate degraded — opening read-only", "subsystem", c.subsystem, "namespace", ns, "key", dbKey, "err", err)
	}
	if at := d.SetAside(); at != "" && c.log != nil {
		c.log.Error("org store: another writer shipped this store after the local copy last reconciled — the local copy is kept beside it, unmerged",
			"subsystem", c.subsystem, "namespace", ns, "key", dbKey, "kept", at)
	}
	c.shipOnce.Do(func() { go c.shipLoop() })
	c.rekey(ns, path)
	db, err := openOrgDB(ns, c.subsystem, c.dataDir)
	if err != nil {
		return zero, nil, err
	}
	d.Bind(db)
	st, err := c.open(ns, db)
	if err != nil {
		_ = db.Close()
		return zero, nil, err
	}
	return st, d, nil
}

// rekey moves a store written under a random key held beside it (a .dek sidecar)
// to its derived key before it opens (cek.Rekey), so the file, and every snapshot
// shipped from it, opens wherever the master is held. Until then a snapshot of it
// is ciphertext no pod without that sidecar can read: a new pod hydrating it
// would hold an unreadable store. The ship loop carries the moved file to the
// object store like any change. A store it cannot move opens as it is, through
// its sidecar, and the log says why.
func (c *OrgStore[T]) rekey(ns namespace.Namespace, path string) {
	if err := cek.Rekey(ns, c.subsystem, path); err != nil && c.log != nil {
		c.log.Error("org store: still under the key beside it — it opens, but a copy of it without that key does not", "subsystem", c.subsystem, "namespace", ns, "err", err)
	}
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
func (c *OrgStore[T]) promote(ns namespace.Namespace, path string, old T, oldDur *tenure.Durable) (T, *tenure.Durable, error) {
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
// (the caller retries on the new owner).
//
// WITHOUT A DURABILITY THERE ARE TWO ANSWERS AND THEY ARE NOT THE SAME. A lone
// writer's committed write is already as durable as the deployment is configured to
// be, so the ship is a successful no-op and always was. But a deployment with
// SEVERAL writers and no fence can prove nothing: no round orders the two copies,
// so an acknowledgement here is a claim the deployment cannot support. It used to
// ack anyway, which is how the end-of-lease path went on billing customers in the
// one regime whose whole point is that nobody may — the gate refused the sweep and
// the ship waved the debit through behind it.
//
// So it REFUSES, and the refusal is what defers the debit rather than doubling it:
// no watermark becomes durable, so the whole gap bills once, on whichever replica
// is provably the owner when the object store comes back.
func (c *OrgStore[T]) Sync(ns namespace.Namespace) (acked bool, err error) {
	if c.sql {
		return true, nil // a committed transaction on hanzo-sql is the durable write; there is nothing to ship
	}
	if c.dur == nil {
		if c.peered() {
			return false, fmt.Errorf("cloud: %s/%s cannot be made durable — this deployment has more "+
				"than one writer and no object store to fence them, so no write here may be acknowledged",
				ns, c.subsystem)
		}
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

// shipLoop ships, every shipEvery, each open durable store this replica owns whose
// local file changed since it last shipped. A subsystem that ships after its own
// writes (ship-before-ack) has nothing left for it; for every other subsystem it is
// what carries the local file to the object store at all.
func (c *OrgStore[T]) shipLoop() {
	t := time.NewTicker(c.shipEvery)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			c.shipChanged()
		}
	}
}

// shipChanged is one pass of shipLoop.
func (c *OrgStore[T]) shipChanged() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	open := make(map[namespace.Namespace]*tenure.Durable, len(c.durables))
	for ns, d := range c.durables {
		open[ns] = d
	}
	c.mu.Unlock()
	for ns, d := range open {
		if !d.Dirty() || !d.Owned() {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), durableOpTimeout)
		acked, err := d.Sync(ctx)
		cancel()
		if c.log == nil || errors.Is(err, sql.ErrConnDone) || err != nil && strings.Contains(err.Error(), "database is closed") {
			continue // evicted or closed under the pass; its own close shipped it
		}
		if err != nil || !acked {
			c.log.Warn("org store ship failed — the local file holds the writes, the object store trails it",
				"subsystem", c.subsystem, "namespace", ns, "acked", acked, "err", err)
		}
	}
}

// Owned reports whether this replica may ACT for the org — write its file, and
// take the actions outside the file that follow from what the file says.
//
// TWO GATES, TWO JOBS, AND NEITHER SUBSTITUTES FOR THE OTHER. Sync's fenced ship
// is the AUTHORITATIVE gate on a write: it settles ownership atomically at the
// lease round, and money is emitted only on its ack. This is the CHEAP gate ahead
// of it, and it answers the question a ship cannot be asked at all — whether to
// do the work in the first place. A sweep that meters and reaps every org on
// every pod does most of that work twice, and its reaper DELETES KUBERNETES PODS,
// which no file ship can fence: by the time a ship could refuse, Somebody else's
// tenant's sandbox is already gone. So the rule is: ask this before acting, ask
// Sync before believing.
//
// Being stale is bounded and is the safe direction. The lease can move between
// this answer and the act, which is exactly why the debit still waits on the ship
// — but a pod that has lost an org stops sweeping it within one refresh, where
// today every pod sweeps every org for ever.
//
// WITHOUT A FENCE, THIS ANSWERS YES, and the first version answered no. That was
// the wrong half of the deployment to stop. With no Durability a replica's orgs are
// the ones on its own volume — no peer holds that file — so acting on them is not
// only safe, it is the only way they are acted on at all. Answering no stopped the
// REAPER: on a deployment that merely lost its object store at boot, every expired
// lease kept its pod for ever, unbilled and uncounted, while the request path went
// on selling new ones. A resource leak with free compute at the end of it, entered
// by an S3 outage.
//
// What must stop in that regime is the MONEY, and that is Sync's answer rather than
// this one — a write nobody can prove durable is not acknowledged, so nothing is
// billed and every span defers. Two questions, two answers: this one gates the ACT,
// Sync gates the CLAIM. Folding them into one predicate is what made an outage stop
// the reaper and not the meter, which is exactly backwards.
func (c *OrgStore[T]) Owned(ns namespace.Namespace) bool {
	if c.sql {
		return c.leased(ns)
	}
	if c.dur == nil {
		return true
	}
	c.mu.Lock()
	d := c.durables[ns]
	c.mu.Unlock()
	// No Durable means the store is not open on this replica, so there is nothing
	// here to own — never a nil dereference, and never an assumed yes.
	return d != nil && d.Owned()
}

// The lease's clocks. leaseTerm is how long a lease lasts without renewal; leaseSlack
// is how much earlier than that this process stops trusting a grant, since it is
// judged by the database's clock and trusted by this one. denialMax is the longest a
// refusal is remembered: a replica that does not own a store asks about every store
// it sweeps, and one question per store per term is cheap enough, but a holder that
// gave its lease up (CloseAll) should not wait out its whole term for the next to
// notice. warnEvery bounds how often a lease problem is said.
const (
	leaseTerm  = 30 * time.Second
	leaseSlack = 2 * time.Second
	denialMax  = 5 * time.Second
	warnEvery  = 30 * time.Second
)

// warnLease says a lease problem, at most once per warnEvery per OrgStore: during an
// outage every row of every sweep asks, and a line each buries the one fact.
func (c *OrgStore[T]) warnLease(msg string, err error) {
	c.leaseMu.Lock()
	say := c.log != nil && time.Since(c.leaseWarned) >= warnEvery
	if say {
		c.leaseWarned = time.Now()
	}
	c.leaseMu.Unlock()
	if say {
		c.log.Warn(msg, "subsystem", c.subsystem, "err", err)
	}
}

// leased is Owned on hanzo-sql. The database is the one writer, but writing is not the
// only thing a replica does for an org: it meters, ends leases, deletes pods. Those
// run once per org, not once per replica, so each store has a lease in the database
// that one replica holds at a time.
//
// Asked per row of a sweep, it answers from memory until half the term is gone and
// then renews, one statement. A replica that stops asking lapses within the term and
// another takes over. A replica that cannot renew says no: it only tries in the second
// half of the term, so what it has left is less than the margin every yes carries
// (half a term), and a yes it cannot renew is one it would give a deleted pod on.
// Asked again before each act that cannot be undone, the answer is a fresh sample
// with at least that margin, and not the one taken at the top of a sweep that has
// since walked minutes of work.
func (c *OrgStore[T]) leased(ns namespace.Namespace) bool {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return false
	}
	c.leaseMu.Lock()
	until, held := c.leases[ns]
	refused := c.denied[ns]
	c.leaseMu.Unlock()
	if held && time.Until(until) > leaseTerm/2 {
		return true
	}
	if !held && time.Now().Before(refused) {
		return false // the holder had time left when it was last asked, or the database did not answer
	}
	if !held && !c.here(ns) {
		// A store nobody has made has nothing to own, and a lease taken on it would be
		// a row nothing ever reads or gives back.
		return false
	}
	began := time.Now()
	c.leaseMu.Lock()
	c.leaseAsked = true
	c.leaseMu.Unlock()
	got, retry, err := locus.Lease(ns, c.subsystem, c.holder, leaseTerm)

	c.mu.Lock()
	closed = c.closed
	c.mu.Unlock()
	if closed { // CloseAll ran while this asked: what it was given is given back
		if got {
			_ = locus.Unlease(c.holder)
		}
		return false
	}
	c.leaseMu.Lock()
	switch {
	case err != nil:
		// Remembered as a refusal is: while the database does not answer, every ask would
		// otherwise wait out its timeout, and a sweep asks for every row. The answer is
		// the safe one, no, and it is the same one for the same reason.
		delete(c.leases, ns)
		c.denied[ns] = began.Add(denialMax)
		c.leaseMu.Unlock()
		c.warnLease("org store: the lease could not be renewed, so it is not this replica's to act on", err)
		return false
	case !got:
		delete(c.leases, ns)
		c.denied[ns] = began.Add(min(retry, denialMax))
		c.leaseMu.Unlock()
		return false
	}
	c.leases[ns] = began.Add(leaseTerm - leaseSlack)
	delete(c.denied, ns)
	c.leaseMu.Unlock()
	return true
}

// SayDurability reports, once, which regime a money-moving sweep is running in.
//
// It exists because the regime is invisible from outside and decides whether the
// plane can bill at all. buildDurability already says whether the deployment has a
// fence, but it says it in the process that built the deps — and a sweep runs in
// its own plugin binary, on its own schedule, so "is THIS meter fenced" was a
// question with no answer anywhere in a log. Three regimes, each said plainly:
//
//	hanzo-sql    the database is the one writer; a debit is as durable as its commit
//	durable      a fence decides who bills an org; a debit waits on its ship
//	solo         one writer, no fence: this process bills every org it holds
//	unprovable   several writers, no fence: NOBODY bills, and spans defer until
//	             the object store is provable again
//
// The last is an ERROR, because it is a deployment that intends to be durable and
// is not, and its symptom is silence — no debits, no double-bill, and no complaint
// unless something says so here.
func SayDurability(log luxlog.Logger, b Base, what string) {
	switch {
	case locus.DSN() != "":
		log.Info(what + ": hanzo-sql — the database is the one writer, and a debit is as durable as its commit")
	case b.Durable != nil:
		log.Info(what + ": durable — the fence decides which orgs this replica bills, and a debit waits on its ship")
	case !b.Peered():
		log.Info(what + ": local-only, single writer — this process bills every org it holds, and a debit is as durable as its volume")
	default:
		log.Error(what + ": local-only on a MULTI-WRITER deployment — no fence can say which replica owns an org, so NOTHING is billed and every span defers until the durable plane is provable. Lifetimes are still enforced: leases end and pods are reclaimed, they are simply not charged. Configure S3_ADMIN_* to enable it.")
	}
}

// orgsRoot is the directory every namespace's file lives under: the first
// segment namespace.Key renders. Each and Stored walk it directly — they
// answer "WHICH entities have a store", which is a question about the disk
// and not about a name, so it is the one thing here that reads the layout
// instead of rendering it. TestOrgNamespaceNeutralisesTraversal holds the
// two spellings together by asserting a rendered key against this root.
const orgsRoot = "orgs"

// Each folds fn over the PENDING namespaces — the orgs a caller has asked for
// that no sweep has since settled — handing it each one's namespace and the SAME
// cached handle For returns (opened through forNS, no second open). It is the
// cross-org sweep primitive, and it opens only those orgs: walking the disk and
// opening every org that ever had a file held hundreds of databases open at once
// for tenants doing nothing, and kept them all "in use" so none could close.
//
// A sweep that proves an org has nothing left for it calls Settle; the org's
// next For puts it back. A per-org OPEN failure is passed to fn as its err (fn
// decides skip vs. record). Under horizontal sharding each writer's volume holds
// only the orgs routed to it, so Each on a writer sweeps only that writer's orgs.
func (c *OrgStore[T]) Each(fn func(ns namespace.Namespace, st T, err error)) error {
	c.mu.Lock()
	shut := c.closed
	c.mu.Unlock()
	if shut {
		return fmt.Errorf("%w: %s sweep", ErrStoreClosed, c.subsystem)
	}
	c.loadPending()
	c.mu.Lock()
	list := make([]namespace.Namespace, 0, len(c.pending))
	for ns := range c.pending {
		list = append(list, ns)
	}
	c.mu.Unlock()
	slices.SortFunc(list, func(a, b namespace.Namespace) int { return strings.Compare(a.String(), b.String()) })
	for _, ns := range list {
		if c.sql {
			c.visit(ns, fn)
			continue
		}
		if !c.Has(ns) {
			continue // marked, and its file is gone: nothing to open
		}
		st, err := c.forNS(ns)
		fn(ns, st, err)
	}
	return nil
}

// visit hands fn one store of a sweep on hanzo-sql, and does not keep it.
//
// A sweep that kept every store it visited would hold a pool, and a connection, for
// every org that ever had work, which is thousands, until the idle deadline let each
// go. So a store that is not already open is opened for the visit and closed after it,
// and one that is (a request opened it) is lent as it is. The pending namespace came
// from the registry or from a For, so the store exists; asking again would be a query
// per org per sweep.
//
// What it costs is the open func running at each visit, the migration included. An
// open func is a migration over a handle, and the file plane runs it at each cold
// open already.
func (c *OrgStore[T]) visit(ns namespace.Namespace, fn func(namespace.Namespace, T, error)) {
	var zero T
	c.mu.Lock()
	st, open := c.byNS[ns]
	if open {
		c.touch(ns)
	}
	closed := c.closed
	c.mu.Unlock()
	if open {
		fn(ns, st, nil)
		return
	}
	if closed {
		fn(ns, zero, fmt.Errorf("%w: %s/%s", ErrStoreClosed, ns, c.subsystem))
		return
	}
	st, err := locus.Use(c.stop, c.log, ns, c.subsystem, c.dataDir, func(db *sql.DB) (T, error) { return c.open(ns, db) })
	if err != nil {
		fn(ns, zero, err)
		return
	}
	defer func() { _ = st.Close() }()
	fn(ns, st, nil)
}

// Stored reports whether ANY namespace already has a store for this subsystem on
// disk, without opening one.
//
// It is the boot question a reader replica asks — has my volume been hydrated at
// all? — and it has to be answerable without opening, because a reader that
// opened every org's file to find out would pay the whole fleet's I/O for one
// boolean. It is Each's walk without the opens, and it shares Has, so the two
// cannot disagree about what counts as a store.
func (c *OrgStore[T]) Stored() bool {
	if c.sql {
		if named, err := locus.Stores(c.subsystem); err == nil && len(named) > 0 {
			return true
		}
	}
	// The deployment's own partition counts: a volume holding only the system
	// namespace's file still has something to serve, and a boot check that said
	// otherwise would refuse to start a replica that was in fact hydrated.
	if c.Has(namespace.System()) {
		return true
	}
	ents, _ := os.ReadDir(filepath.Join(c.dataDir, orgsRoot))
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		ns, err := principal.OrgOnDisk(e.Name())
		if err != nil {
			continue
		}
		if c.Has(ns) {
			return true
		}
	}
	if c.dur == nil {
		return false
	}
	orgs, err := c.remote()
	return err == nil && len(orgs) > 0
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
	durs := make([]*tenure.Durable, 0, len(c.durables))
	for _, d := range c.durables {
		durs = append(durs, d)
	}
	c.mu.Unlock()
	c.stopOnce.Do(func() { close(c.done) })
	c.halt()
	if c.swept != nil {
		// The move in flight puts its store back, and the sweep ends. Bounded: a
		// database that does not answer must not hold the way out of a process.
		select {
		case <-c.swept:
		case <-time.After(durableOpTimeout):
		}
	}
	if c.sql {
		// In this order, and each step before the next. Closed first, so from here
		// every For, Each and Owned refuses and none can take a lease back; the grants
		// forgotten next, so this replica stops saying yes at once and not when its
		// last grant would have run out; and only then the leases given up, so the
		// replica that takes them over is the only one that answers yes. Giving them
		// up first left both answering yes for the rest of a term. Bounded, because it
		// is the way out of a process and a database that does not answer must not
		// hold it; the leases lapse by themselves.
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		c.leaseMu.Lock()
		clear(c.leases)
		clear(c.denied)
		asked := c.leaseAsked
		c.leaseMu.Unlock()
		if asked { // a holder that never leased dials nothing at shutdown
			if err := locus.Unlease(c.holder); err != nil {
				c.warnLease("org store: leases not released; they lapse within the term", err)
			}
		}
	}
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
	// TERMINAL, and set before the maps are cleared so there is no window in which
	// the store is empty and still openable. Clearing alone was the defect: it made
	// "closed" indistinguishable from "nothing opened yet", which is precisely the
	// state a post-shutdown For() reads as an invitation.
	c.closed = true
	c.byNS = map[namespace.Namespace]T{}
	c.durables = map[namespace.Namespace]*tenure.Durable{}
	c.inflight = map[namespace.Namespace]*openState[T]{}
	c.used = map[namespace.Namespace]time.Time{}
	return first
}
