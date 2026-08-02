package risk

// resident.go is the ONE registry of live per-tenant state, and the only place a
// tenant's aggregates, model, file and governance cache are created, reclaimed or
// closed.
//
// ONE CELL PER TENANT, NOTHING SHARED. A resident holds this tenant's own
// velocity rings, its own half-space forest, its own SQLite file and its own
// cached rule set. No map inside any of them is indexed by more than one tenant,
// so there is no eviction, no read and no write that could cross a tenant even by
// mistake. That is the structural answer to the defect three planes shipped: a
// process-wide store with a GLOBAL cap, where one org's volume silently deletes
// another org's state.
//
// A CELL IS BUILT COMPLETE AND ONLY THEN PUBLISHED, and that is what makes the
// lifetime safe. An earlier cut published an empty cell, opened its file, and on
// any failure called abandon() — which released whatever cell it found in the
// map. For a concurrent first request on the same org that is the cell the OTHER
// request is already holding: release() closed the handle and nil'd it, and the
// winner's next query dereferenced a nil *sql.DB. cloud runs ONE replica, so a
// transient SQLITE_BUSY on a cold tenant was a total outage for every tenant on
// the pod. Here the only cell `of` can discard is one it built and nobody has
// ever seen, so there is no abandon to get wrong, and the handle is read through
// db() under the cell's own lock rather than off the struct.
//
// EVERY DEGRADATION IS SELF-INFLICTED OR LOUD.
//
//	own cardinality bound   the tenant's own gate refuses a NEW key, counts it,
//	                        and reports `strained` on the decision and the probe.
//	                        Its existing counters keep moving.
//	own idleness            after idleReclaim of SILENCE the tenant is RETIRED:
//	                        its model is snapshotted onto its own file first, then
//	                        its aggregates, caches and file handle are released.
//	                        Nothing durable is lost, so its next request comes
//	                        back with what it learned.
//	the node is full        cells silent past idleFloor are reclaimed FIRST —
//	                        counted, and readable on the probe. Only if there is
//	                        nothing to reclaim is a newcomer refused, loudly
//	                        (503 + an error log + a degraded probe). No tenant
//	                        that is USING the node is ever taken from.
//
// RETIREMENT IS THE WHOLE LIFETIME, not a half of one. An earlier cut dropped a
// tenant's aggregates but KEPT its cell forever, so the map only ever grew: once
// the ceiling of distinct tenants had passed through, the next one was refused
// for the life of the process even with the pod idle. A high-water mark is not a
// bound — it is the same "one tenant's presence denies another" defect wearing an
// admission badge.
//
// AND NOTHING IS EVER SILENTLY UNDER-COUNTED. `since` is the instant a tenant's
// rings started, and it rides every decision. A 30-day count computed from ten
// minutes of rings is a true number about the wrong period, and the only way that
// is not a lie is to publish the period. It matters on far more than reclaim:
// cloud deploys strategy Recreate at ONE replica, so every rollout starts every
// tenant's rings from zero.

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/zap-proto/zip"
)

// resident is one tenant's live state.
type resident struct {
	t Tenant
	// res is the node budget this cell draws on. Nil for a cell nothing is
	// accounting — a search sandbox — which is then bounded by its own maxKeys
	// alone.
	res *residency

	mu sync.Mutex
	// handle is the tenant's own encrypted file. Read through db() under this
	// lock and never off the struct: release() writes it, and an unsynchronised
	// read of a field another goroutine may be closing is how a nil dereference
	// takes a one-replica pod down.
	handle *sql.DB
	// The ARM: the two in-memory planes and when they started. Nil when this
	// tenant has been reclaimed; re-armed on its next request, which is also
	// the path that reloads its model.
	vel   *rings
	model *anomaly.Store
	since time.Time
	// touched is the last time this tenant asked for anything. The reclaim
	// sweep reads it and nothing else.
	touched time.Time
	// kept is what the tenant's DURABLE snapshot said it had learned when this
	// cell last armed. Compared against what the live model holds, it is how
	// "warming, because new" is told apart from "warming, because the learned
	// state is gone" — the difference between a control that is coming up and
	// one that is off.
	kept int64
	// disarmed says the learned state existed and this process does not have
	// it: the snapshot was unreadable, or refused by the engine. A decision
	// made in that condition carries RefusalDisarmed, never RefusalWarming.
	disarmed bool

	// retired latches the ONE release of this cell. Two sweeps racing the same
	// cell would otherwise each price it and each hand its bytes back, and a
	// budget that can be credited twice is not a budget.
	retired bool
	// replaying is this tenant's ONE slot for a synchronous replay of its own
	// history. Atomic rather than under mu because it is held across the replay
	// itself, and holding the cell's lock for the length of a 5,000-row read
	// would stall every other request for this tenant.
	replaying atomic.Bool
	// writes counts decisions recorded since this cell armed. It is what paces
	// the decision log's prune without a table scan on the authorization path.
	writes int

	// The governance cache. Loaded once per change, not once per decision: three
	// unbounded SELECTs on every authorization was the second defect. Every
	// writer calls dirty, and both live under this same mutex, so a read can
	// never see a half-applied change.
	loaded bool
	rules  []rule
	lists  map[string]map[string]bool
	sups   []suppression
	live   bool
	// govern is what the loaded governance measures, in bytes. Priced live
	// rather than reserved because a tenant with ten rules must not be charged
	// for a budget it is not using — that pricing is the whole operating point.
	govern int

	// agency memoises this tenant's agent-registry answers, under this tenant's
	// own bound. It has its own lock because a registry call must not be made
	// while holding the lock a decision needs. Its ceiling is reserved in
	// cellBytes.
	agency agencyCache
}

// residency is the set of residents, bounded in BYTES.
type residency struct {
	dataDir string

	mu    sync.Mutex
	cells map[Tenant]*resident
	// held is the live sum of what the cells cost. Kept as a running total
	// rather than recomputed, because a new key on the hot path must not walk
	// every tenant on the node to find out whether it fits.
	held int
	// building serialises the first touch of a tenant, so two concurrent
	// newcomers open one file rather than two. A second builder waits on the
	// first's channel and then finds the published cell.
	building map[Tenant]chan struct{}
	// The saturation view. All three are on the probe: a node that is refusing
	// tenants, or reclaiming them under pressure, must page an operator rather
	// than be discovered in a support ticket.
	refused   int64
	reclaimed int64
	refusedAt time.Time
}

func newResidency(dataDir string) *residency {
	return &residency{dataDir: dataDir, cells: map[Tenant]*resident{}, building: map[Tenant]chan struct{}{}}
}

// errFull is the capacity refusal. 503 and not 403: nothing about the caller is
// wrong, this node is out of memory and has nothing idle left to reclaim, and a
// retry against a node with room succeeds.
var errFull = zip.Errorf(503, "this node has no memory left for another tenant and will not take a live tenant's state to make room")

// of resolves the caller's resident, admitting and arming it if this process has
// not seen it. It is the ONE entry: every typed op reaches its tenant through
// here, so admission, the bound, the reclaim and the model reload each have
// exactly one site.
func (rs *residency) of(t Tenant, log logger) (*resident, error) {
	for {
		r, wait := rs.lookup(t)
		if r != nil {
			r.touch()
			return r, nil
		}
		if wait != nil {
			<-wait // another request is opening this tenant's file; take its cell
			continue
		}
		return rs.build(t, log)
	}
}

// lookup answers with the published cell, or with the channel to wait on when
// another request is building it, or with neither — in which case the caller
// holds the build claim and must call build.
func (rs *residency) lookup(t Tenant) (*resident, chan struct{}) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if r, held := rs.cells[t]; held {
		return r, nil
	}
	if ch, going := rs.building[t]; going {
		return nil, ch
	}
	rs.building[t] = make(chan struct{})
	return nil, nil
}

// build opens and arms a cell OFF the registry and publishes it whole. Nothing
// can observe a half-built cell, so nothing has to be undone if it fails: the
// handle this call opened is the only one that could be closed, and closing it
// is safe precisely because no other request has ever been handed it.
func (rs *residency) build(t Tenant, log logger) (*resident, error) {
	defer rs.finish(t)

	if err := rs.reserve(cellBytes, log); err != nil {
		log.Error("risk: refusing a new tenant — this node has no memory left and nothing idle to reclaim",
			"tenant", t.String(), "bytes", rs.bytes(), "bytes_max", memBytes())
		return nil, err
	}
	r := &resident{t: t, res: rs, touched: time.Now()}
	if err := rs.open(r); err != nil {
		rs.release(cellBytes)
		return nil, err
	}
	if err := rs.arm(r, log); err != nil {
		r.close()
		rs.release(cellBytes)
		return nil, err
	}

	rs.mu.Lock()
	rs.cells[t] = r
	rs.mu.Unlock()
	return r, nil
}

// finish publishes the build claim's completion to anyone waiting on it.
func (rs *residency) finish(t Tenant) {
	rs.mu.Lock()
	ch := rs.building[t]
	delete(rs.building, t)
	rs.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// reserve takes n bytes of the node's budget, RECLAIMING tenants that have been
// silent past the retire floor before it refuses.
//
// The order is the whole design. Reclaim first, refuse last: a node that turned
// a newcomer away while holding cells nobody has touched for hours is denying
// service to keep memory it is not using. But only cells past idleFloor may go,
// and that threshold is the tenant's OWN silence — so no tenant's arrival can
// ever cost a tenant that is working its rings, which is the difference between
// reclaim and eviction.
func (rs *residency) reserve(n int, log logger) error {
	rs.mu.Lock()
	if rs.held+n <= memBytes() {
		rs.held += n
		rs.mu.Unlock()
		return nil
	}
	rs.mu.Unlock()

	if freed := rs.pressure(n, log); freed {
		rs.mu.Lock()
		if rs.held+n <= memBytes() {
			rs.held += n
			rs.mu.Unlock()
			return nil
		}
		rs.mu.Unlock()
	}

	rs.mu.Lock()
	rs.refused, rs.refusedAt = rs.refused+1, time.Now()
	rs.mu.Unlock()
	return errFull
}

// release gives n bytes back to the node's budget.
func (rs *residency) release(n int) {
	rs.mu.Lock()
	rs.held -= n
	if rs.held < 0 {
		rs.held = 0
	}
	rs.mu.Unlock()
}

// room is the node gate a tenant's rings ask before taking a new key. Same
// budget, same reclaim, no refusal log: a key that does not fit is reported to
// the tenant as `strained`, which is the honest word for it.
func (rs *residency) room(n int) bool {
	rs.mu.Lock()
	if rs.held+n <= memBytes() {
		rs.held += n
		rs.mu.Unlock()
		return true
	}
	rs.mu.Unlock()
	if !rs.pressure(n, discard{}) {
		return false
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.held+n <= memBytes() {
		rs.held += n
		return true
	}
	return false
}

// pressure retires cells silent past idleFloor until n bytes are free, and
// reports whether it freed anything. Counted, so `reclaimed` on the probe tells
// an operator the node is running on reclamation rather than on headroom.
func (rs *residency) pressure(n int, log logger) bool {
	freed := false
	for _, r := range rs.idle(idleFloor) {
		if rs.retire(r, log) {
			freed = true
			rs.mu.Lock()
			rs.reclaimed++
			room := rs.held+n <= memBytes()
			rs.mu.Unlock()
			if room {
				break
			}
		}
	}
	return freed
}

// idle snapshots the cells silent for at least d, OLDEST FIRST.
//
// A SNAPSHOT, taken under rs.mu and walked outside it. Holding the registry lock
// while taking each cell's lock is a cross-tenant latency coupling on a path the
// package itself describes as sitting inside a card processor's authorization
// window: one cold tenant's file I/O would stall every other tenant's decide.
func (rs *residency) idle(d time.Duration) []*resident {
	now := time.Now()
	rs.mu.Lock()
	out := make([]*resident, 0, len(rs.cells))
	for _, r := range rs.cells {
		if now.Sub(r.idleFor()) >= d {
			out = append(out, r)
		}
	}
	rs.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].idleFor().Before(out[j].idleFor()) })
	return out
}

// idleFor is when this cell was last touched.
func (r *resident) idleFor() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.touched
}

// touch records that a request resolved this tenant. The retire threshold is
// measured from here, which is the fact the safety of closing its file rests on.
func (r *resident) touch() {
	r.mu.Lock()
	r.touched = time.Now()
	r.mu.Unlock()
}

// open resolves the tenant's file once. The ORG half is what cloud.OrgNamespace
// takes — the deployment does its own brand scoping through DataDir, so handing
// it the qualified key would put the brand in the name twice.
//
// It runs on an UNPUBLISHED cell, so no lock is taken across the file I/O:
// creating and seeding an encrypted SQLite file under a lock a request needs is
// how one cold tenant's first touch became every other tenant's latency.
func (rs *residency) open(r *resident) error {
	ns, err := cloud.OrgNamespace(r.t.org(), "")
	if err != nil {
		return err
	}
	db, err := cloud.OrgDB(rs.dataDir, ns, "risk")
	if err != nil {
		return err
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return err
	}
	if err := seed(db); err != nil {
		_ = db.Close()
		return err
	}
	// A search row still marked running was running in a process that no longer
	// exists — cloud deploys Recreate, so there is no other way for one to
	// survive. Left alone it would be read forever as "in progress", which is
	// the same bytes as a search that is about to answer and the opposite fact.
	if err := abandonSearches(db); err != nil {
		_ = db.Close()
		return err
	}
	// The decision log is pruned to its retention here as well as every
	// pruneEvery writes, so a tenant that writes fewer than that between
	// rollouts still comes back inside its budget.
	if err := prune(db); err != nil {
		_ = db.Close()
		return err
	}
	r.handle = db
	return nil
}

// arm gives an unpublished resident its aggregates and its model, and restores
// what the tenant had learned.
//
// THE RELOAD IS HERE AND ONLY HERE, which is what closes the silent-disarm
// defect. The process used to latch "restored" in a map that outlived the model,
// so a model dropped by the shared store's LRU was never reloaded — the tenant
// scored nothing for the rest of the process's life while reporting only
// "warming". A cell that has no model has no latch either, because the latch IS
// the model.
func (rs *residency) arm(r *resident, log logger) error {
	vel := aggregates()
	model, err := forest(anomaly.Config{}, vel)
	if err != nil {
		return err
	}

	kept, disarmed := int64(0), false
	if snap, ok, err := readSnapshot(r.handle, r.t); err != nil {
		disarmed = true
		log.Error("risk: a tenant's learned state is on file and could not be read; the model is DISARMED, not warming",
			"tenant", r.t.String(), "err", err)
	} else if ok {
		kept = snap.Learned
		if err := model.Restore(snap); err != nil {
			disarmed = true
			log.Error("risk: a tenant's learned state was refused by the engine; the model is DISARMED, not warming",
				"tenant", r.t.String(), "err", err)
		}
	}

	r.vel, r.model, r.since = vel, model, time.Now().UTC()
	r.kept, r.disarmed = kept, disarmed
	return nil
}

// retire releases ONE tenant that has been silent: its model is written to its
// own file, then its aggregates, its caches and its file handle go and its cell
// is dropped. Answers whether it did.
//
// IT IS NOT EVICTION AND THE DIFFERENCE IS THE WHOLE POINT: the trigger is the
// retired tenant's own silence, so no tenant's traffic can ever cost another
// tenant a ring. Nothing durable is lost — the model is snapshotted first, and
// the rules, lists and decisions were always on the tenant's own file — so the
// tenant's next request comes back with what it learned. The aggregates are the
// one thing that does not survive, which is why `since` rides every decision
// instead of the loss being left for someone to notice.
//
// CLOSING THE FILE IS SAFE WITHOUT A REFERENCE COUNT because the caller only
// ever hands it cells idle past idleFloor, which is floored above the longest
// any worker may hold one (see idleReclaim).
func (rs *residency) retire(r *resident, log logger) bool {
	r.mu.Lock()
	if r.retired {
		r.mu.Unlock()
		return false
	}
	if err := keep(r.handle, r.t, r.model); err != nil {
		// Never drop a model we could not write down: the cell is HELD, and the
		// next sweep tries again. Losing learned state to a housekeeping pass is
		// exactly the silent disarm this file exists to prevent.
		r.mu.Unlock()
		log.Error("risk: a retiring tenant's learned state was not kept, so its cell is held", "tenant", r.t.String(), "err", err)
		return false
	}
	idleFor := time.Since(r.touched)
	cost := r.costLocked()
	r.retired = true
	r.releaseLocked()
	r.mu.Unlock()

	rs.mu.Lock()
	if rs.cells[r.t] == r {
		delete(rs.cells, r.t)
	}
	rs.mu.Unlock()
	rs.release(cost)

	log.Info("risk: a tenant was retired after its own idleness; its learned state is on its own file",
		"tenant", r.t.String(), "idle", idleFor.String())
	return true
}

// costLocked is what this cell is charged to the node's budget. Caller holds
// r.mu.
func (r *resident) costLocked() int {
	n := cellBytes + r.govern
	if r.vel != nil {
		n += r.vel.keys() * bytesPerKey()
	}
	return n
}

// bytes is what this cell costs the node right now.
func (r *resident) bytes() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.costLocked()
}

// releaseLocked drops everything this cell holds. Caller holds r.mu.
func (r *resident) releaseLocked() {
	r.vel, r.model = nil, nil
	r.loaded, r.rules, r.lists, r.sups, r.govern = false, nil, nil, nil, 0
	r.agency.clear()
	if r.handle != nil {
		_ = r.handle.Close()
		r.handle = nil
	}
}

// close drops an UNPUBLISHED cell. The only caller is the build that made it,
// which is what makes closing the handle safe: nobody else has ever held it.
func (r *resident) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releaseLocked()
}

// sweep is the background retire. It runs on a timer so memory comes back from a
// silent tenant without waiting for a busy one to need it.
func (rs *residency) sweep(log logger) {
	for _, r := range rs.idle(idleReclaim()) {
		rs.retire(r, log)
	}
}

// tenants lists the tenants this process holds, in a stable order.
func (rs *residency) tenants() []Tenant {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make([]Tenant, 0, len(rs.cells))
	for t := range rs.cells {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// bytes is what every resident tenant costs this node right now.
func (rs *residency) bytes() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.held
}

// count reports the saturation view: how many tenants this process holds, how
// many admissions it has refused, how many cells it has reclaimed under memory
// pressure, and when the last refusal was. All four are on the probe — a
// control that runs out of room quietly is a control nobody knows is off.
func (rs *residency) count() (tenants int, refused, reclaimed int64, at time.Time) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return len(rs.cells), rs.refused, rs.reclaimed, rs.refusedAt
}

// strained is how many resident tenants are reading partial rings.
func (rs *residency) strained() int {
	rs.mu.Lock()
	cells := make([]*resident, 0, len(rs.cells))
	for _, r := range rs.cells {
		cells = append(cells, r)
	}
	rs.mu.Unlock()
	n := 0
	for _, r := range cells {
		if r.strained() {
			n++
		}
	}
	return n
}

// close snapshots every armed tenant and closes every file. It is what makes a
// rollout survivable: cloud deploys Recreate at one replica, so every deploy
// drops the process, and a model that comes back with nothing learned declines to
// score for its whole warm period.
func (rs *residency) close(log logger) (kept, failed int) {
	rs.mu.Lock()
	cells := make(map[Tenant]*resident, len(rs.cells))
	for t, r := range rs.cells {
		cells[t] = r
	}
	rs.cells = map[Tenant]*resident{}
	rs.held = 0
	rs.mu.Unlock()

	for t, r := range cells {
		r.mu.Lock()
		if r.model != nil {
			if err := keep(r.handle, r.t, r.model); err != nil {
				failed++
				log.Error("risk: a tenant's learned state was not kept", "tenant", t.String(), "err", err)
			} else {
				kept++
			}
		}
		r.retired = true
		r.releaseLocked()
		r.mu.Unlock()
	}
	return kept, failed
}

// ── what an op reads off a resident ─────────────────────────────────────────

// file is the tenant's own file handle, read under this cell's own lock.
//
// IT ANSWERS WITH AN ERROR RATHER THAN WITH A HANDLE NOBODY MAY USE, and that is
// the whole of it. `(*sql.DB)(nil).QueryRow` locks a nil mutex, so a cell whose
// file has been closed must never hand one out: on a one-replica pod that panic
// is every tenant's outage. Two things close a cell — retire, after the tenant's
// own idleFloor of silence, and teardown, which closes EVERY cell the instant
// SIGTERM lands. The second has no idle requirement and cloud deploys Recreate,
// so a request holding a cell it resolved a millisecond ago can find the handle
// gone on every single rollout.
//
// Returning (handle, error) is what makes the nil unrepresentable downstream: an
// op cannot obtain the file without also obtaining the reason it has none, and
// the check lives at the ONE door (tenantState) instead of at 27 call sites that
// each have to remember it.
func (r *resident) file() (*sql.DB, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.handle == nil {
		return nil, errRetired
	}
	return r.handle, nil
}

// arms returns the tenant's two in-memory planes and the instant they started.
// A disarmed resident cannot be reached this way: `of` arms before publishing.
func (r *resident) arms() (*rings, *anomaly.Store, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.vel, r.model, r.since
}

// recorded says one decision was written, and answers whether the log is due
// for its prune. Counted per cell, so nothing on the hot path counts rows.
func (r *resident) recorded() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes++
	return r.writes%pruneEvery == 0
}

// alone runs f while this tenant holds the ONE slot for a synchronous replay of
// its own history, and refuses rather than queues when it is taken.
//
// THE BOUND IS CONCURRENCY, NOT PRICE. /v1/risk/simulate reads up to 5,000 of
// this tenant's decisions and materialises them, and it is deliberately UNPRICED
// because it is the rehearsal a tenant runs before activating a rule — charging
// for the safe path discourages the safe path. But nothing bounded how many ran
// at once, so N connections held N x 5,000 rows, which appears in no ceiling.
// One at a time per tenant is the honest bound: the rehearsal stays free, and
// the memory it can reach is a fixed multiple of what one replay costs.
func (r *resident) alone(f func() error) error {
	if !r.replaying.CompareAndSwap(false, true) {
		return zip.Errorf(409, "this tenant already has a replay of its own history running; it reads up to %d decisions and runs one at a time", searchHistoryMax)
	}
	defer r.replaying.Store(false)
	return f()
}

// record writes an observation onto this tenant's rings, through this tenant's
// own cardinality gate and the node's byte gate. It is the ONLY write path into
// the aggregates, so a key that was never priced cannot exist.
func (r *resident) record(o observation) {
	vel, _, _ := r.arms()
	if vel == nil {
		return
	}
	vel.record(r.t, o, r.room)
}

// room is this cell's view of the node gate. A cell with no residency behind it
// — a search sandbox replaying history — is bounded by its own maxKeys and
// charges the node nothing, because the node already paid for the live rings the
// sandbox is a copy of.
func (r *resident) room(n int) bool {
	if r.res == nil {
		return true
	}
	return r.res.room(n)
}

// strained reports that this tenant's rings stopped tracking its own traffic:
// a key it needed was refused, by its own cardinality bound or by the node's
// memory. Either way a count it reads may be an under-count of its own traffic,
// and a rule written on that count is measuring a partial ring.
func (r *resident) strained() bool {
	vel, _, _ := r.arms()
	return vel != nil && vel.strained()
}

// grade turns THE MODEL'S OWN refusal into the word that is true of it, against
// what this tenant is known to have learned. Warming and disarmed are the same
// bytes on the wire and opposite facts about the system, so they are never the
// same word.
//
// It grades the model's reason and nothing else. An earlier cut ran it over the
// FINISHED refusal, after every reason had been merged into one word — so a
// decision that was also short of something ranked higher never reached the
// warming branch, and a disarmed model went out under the other reason's name.
// A grader that only fires when nothing else went wrong is not a grader.
func (r *resident) grade(reason string) string {
	if reason != RefusalWarming {
		return reason
	}
	r.mu.Lock()
	disarmed, kept, model := r.disarmed, r.kept, r.model
	r.mu.Unlock()
	if disarmed {
		return RefusalDisarmed
	}
	if model != nil && kept > 0 && model.State(r.t.String()).Learned < kept {
		return RefusalDisarmed
	}
	return reason
}

// governance is the tenant's rule set, list membership, suppressions and live
// switch, loaded ONCE per change.
//
// Three unbounded SELECTs on every authorization was the defect; a cache with no
// invalidation would be a worse one, so every writer on this tenant's file calls
// dirty and both live under this mutex. cloud runs one replica of this app and
// the shard router pins an org to one pod, so this process is the only writer of
// this file — the cache cannot be stale with respect to a writer it cannot see.
//
// IT IS PRICED, NOT GATED. What it loads is bounded per tenant by the write-time
// byte budgets (governMemo), and it is measured onto the node's total the moment
// it lands — but it is never refused. Refusing to load a tenant's rules would
// disarm that tenant's controls to save memory, which is the one degradation
// this package will not do; the node answers instead by admitting no new cells
// and no new counters until the total comes down.
func (r *resident) governance() ([]rule, map[string]map[string]bool, []suppression, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loaded {
		return r.rules, r.lists, r.sups, r.live, nil
	}
	if r.handle == nil {
		// Retired out from under a request. idleFloor is what makes this
		// unreachable — nothing in this process holds a cell for sixteen minutes
		// — so say so loudly rather than dereference a nil handle and take the
		// pod down with every tenant on it.
		return nil, nil, nil, false, errRetired
	}
	rules, err := loadRules(r.handle)
	if err != nil {
		return nil, nil, nil, false, err
	}
	lists, err := loadLists(r.handle)
	if err != nil {
		return nil, nil, nil, false, err
	}
	sups, err := loadSuppressions(r.handle)
	if err != nil {
		return nil, nil, nil, false, err
	}
	r.rules, r.lists, r.sups = rules, lists, sups
	r.live = mode(r.handle) == "live"
	r.loaded = true
	was := r.govern
	r.govern = governBytesOf(rules, lists, sups)
	if r.res != nil {
		r.res.charge(r.govern - was)
	}
	return r.rules, r.lists, r.sups, r.live, nil
}

// governBytesOf measures a loaded governance set. Each row is priced at what it
// costs in the maps the authorization path holds it in, at the caps bound.go
// publishes — so the node's total is the sum of real rows and never of budgets
// nobody is using.
func governBytesOf(rules []rule, lists map[string]map[string]bool, sups []suppression) int {
	n := len(rules)*ruleMax + len(sups)*supMax
	for _, l := range lists {
		n += len(l) * entryMax
	}
	return n
}

// charge moves the node's running total by delta, which may be negative.
func (rs *residency) charge(delta int) {
	if delta == 0 {
		return
	}
	rs.mu.Lock()
	rs.held += delta
	if rs.held < 0 {
		rs.held = 0
	}
	rs.mu.Unlock()
}

// reload reinstates this tenant's learned state from its own pinned snapshot,
// over whatever the live model holds.
//
// It is what POST /v1/risk/restore does, and it is also the honest recovery from
// a disarmed model: a tenant told its control is off can put it back on with the
// state it kept. A snapshot belonging to another tenant is refused twice — here,
// by the tenant that asked, and again inside the engine.
func (r *resident) reload() error {
	r.mu.Lock()
	db, model := r.handle, r.model
	r.mu.Unlock()
	if model == nil {
		return fmt.Errorf("risk: this tenant holds no model to restore into")
	}
	snap, ok, err := readSnapshot(db, r.t)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("risk: this tenant has no pinned snapshot")
	}
	if err := model.Restore(snap); err != nil {
		return err
	}
	r.mu.Lock()
	r.kept, r.disarmed = snap.Learned, false
	r.mu.Unlock()
	return nil
}

// dirty drops the governance cache. Called by every write to a governed plane,
// beside the write, so a new plane cannot be added without the author meeting
// this line.
func (r *resident) dirty() {
	r.mu.Lock()
	was := r.govern
	r.loaded, r.rules, r.lists, r.sups, r.govern = false, nil, nil, nil, 0
	res := r.res
	r.mu.Unlock()
	if res != nil {
		res.charge(-was)
	}
}

// ── the durable half of the model ───────────────────────────────────────────

// keep writes a tenant's learned state into its own file. Nothing learned is not
// a failure — a tenant that has scored nothing has nothing to keep.
func keep(db *sql.DB, t Tenant, model *anomaly.Store) error {
	if db == nil || model == nil {
		return nil
	}
	snap, ok := model.Snapshot(t.String())
	if !ok {
		return nil
	}
	body, err := encodeSnapshot(snap)
	if err != nil {
		return err
	}
	return putModel(db, snapshotKey, body)
}

// readSnapshot reads a tenant's kept state back.
//
// The snapshot's tenant must be the tenant asking for it. The engine checks this
// too; checking here as well means a restore of A's file into B's request is
// refused at the boundary that knows who asked.
func readSnapshot(db *sql.DB, t Tenant) (anomaly.Snapshot, bool, error) {
	body, err := getModel(db, snapshotKey)
	if err != nil {
		var he *zip.HTTPError
		if errors.As(err, &he) && he.Status == 404 {
			return anomaly.Snapshot{}, false, nil // no snapshot is the normal first run
		}
		return anomaly.Snapshot{}, false, err
	}
	snap, err := decodeSnapshot(body)
	if err != nil {
		return anomaly.Snapshot{}, false, err
	}
	if snap.OrgID != t.String() {
		return anomaly.Snapshot{}, false, fmt.Errorf("risk: snapshot belongs to another tenant")
	}
	return snap, true, nil
}

// errRetired is what a cell answers when its file has already been closed. A
// retry lands on a fresh cell, so it is a 503 and not a 500.
var errRetired = zip.Errorf(503, "this tenant's state was reclaimed while the request was in flight; retry")

// logger is the slice of the service's logger this file uses. Narrow on purpose:
// residency is reached from teardown and from a background sweep as well as from
// a request, and a package that took the whole service would be reaching for
// state it has no business touching.
type logger interface {
	Info(msg string, args ...any)
	Error(msg string, args ...any)
}

// discard is the logger the memory gate uses. A key that does not fit is
// reported to the tenant as `strained` and counted on the probe; logging a line
// per refused counter would be a log entry per request on exactly the node that
// has no room to spare.
type discard struct{}

func (discard) Info(string, ...any)  {}
func (discard) Error(string, ...any) {}
