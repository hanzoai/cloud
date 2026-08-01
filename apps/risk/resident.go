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
// EVERY DEGRADATION IS SELF-INFLICTED AND LOUD.
//
//	own cardinality bound   evicts this tenant's own oldest key; counted, and
//	                        reported as `strained` on the decision and the probe.
//	own idleness            after idleReclaim of SILENCE the tenant is RETIRED:
//	                        its model is snapshotted onto its own file first, then
//	                        its aggregates, caches and file handle are released.
//	                        The trigger is its own silence — no other tenant's
//	                        traffic can cause it — and nothing durable is lost, so
//	                        its next request comes back with what it learned.
//	the pod is full         a NEW tenant is refused admission, loudly (503 + an
//	                        error log + a degraded probe). No incumbent is touched.
//	                        Admission pressure is a capacity event for an operator,
//	                        never a quiet disarm for a victim.
//
// RETIREMENT IS THE WHOLE LIFETIME, not a half of one. An earlier cut dropped a
// tenant's aggregates but KEPT its cell forever, so the map only ever grew: once
// tenantMax distinct tenants had passed through, tenant tenantMax+1 was refused
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
	"time"

	"github.com/hanzoai/cloud"
	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/velocity"
	"github.com/zap-proto/zip"
)

// resident is one tenant's live state.
type resident struct {
	t  Tenant
	db *sql.DB

	mu sync.Mutex
	// The ARM: the two in-memory planes and when they started. Nil when this
	// tenant has been reclaimed; re-armed on its next request, which is also
	// the path that reloads its model.
	vel   *velocity.Store
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

	// The governance cache. Loaded once per change, not once per decision: three
	// unbounded SELECTs on every authorization was the second defect. Every
	// writer calls dirty, and both live under this same mutex, so a read can
	// never see a half-applied change.
	loaded bool
	rules  []rule
	lists  map[string]map[string]bool
	sups   []suppression
	live   bool

	// agency memoises this tenant's agent-registry answers, under this tenant's
	// own bound. It has its own lock because a registry call must not be made
	// while holding the lock a decision needs.
	agency agencyCache
}

// residency is the bounded set of residents.
type residency struct {
	dataDir string

	mu    sync.Mutex
	cells map[Tenant]*resident
	// refused counts admissions turned away at the cap, and refusedAt is when
	// the last one was. Both are on the probe: a pod that is refusing tenants
	// must page an operator rather than be discovered in a support ticket.
	refused   int64
	refusedAt time.Time
}

func newResidency(dataDir string) *residency {
	return &residency{dataDir: dataDir, cells: map[Tenant]*resident{}}
}

// errFull is the capacity refusal. 503 and not 403: nothing about the caller is
// wrong, this pod is out of room, and a retry against a pod with room succeeds.
var errFull = zip.Errorf(503, "this node is at its tenant capacity and will not evict another tenant's state to make room")

// of resolves the caller's resident, admitting and arming it if this process has
// not seen it. It is the ONE entry: every typed op reaches its tenant through
// here, so admission, the bound, the reclaim and the model reload each have
// exactly one site.
func (rs *residency) of(t Tenant, log logger) (*resident, error) {
	now := time.Now()
	rs.mu.Lock()
	r, held := rs.cells[t]
	if !held {
		if len(rs.cells) >= tenantMax() {
			rs.retireLocked(now, idleReclaim(), log)
		}
		if len(rs.cells) >= tenantMax() {
			rs.refused, rs.refusedAt = rs.refused+1, now
			rs.mu.Unlock()
			log.Error("risk: refusing a new tenant — this node is at its tenant ceiling and will not evict an incumbent to make room",
				"tenant", t.String(), "tenants", tenantMax())
			return nil, errFull
		}
		// Touched at BIRTH, so a sweep between this line and the tenant's first
		// answer cannot retire a cell that has not served a request yet.
		r = &resident{t: t, touched: now}
		rs.cells[t] = r
	}
	rs.mu.Unlock()

	// Touched again before the work, so the retire threshold is measured from
	// the LAST time a request resolved this tenant, which is the fact the safety
	// of closing its file rests on.
	r.mu.Lock()
	r.touched = now
	r.mu.Unlock()

	if err := rs.open(r); err != nil {
		rs.abandon(t, r, held)
		return nil, err
	}
	if err := rs.arm(r, log); err != nil {
		rs.abandon(t, r, held)
		return nil, err
	}
	return r, nil
}

// abandon drops a cell this call CREATED and could not bring up. Only that one:
// a cell an earlier call published is another request's resident, and removing
// it because this one failed would be the cross-tenant reach in miniature.
func (rs *residency) abandon(t Tenant, r *resident, held bool) {
	if held {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.cells[t] != r {
		return
	}
	delete(rs.cells, t)
	r.mu.Lock()
	r.release()
	r.mu.Unlock()
}

// open resolves the tenant's file once. The ORG half is what cloud.OrgNamespace
// takes — the deployment does its own brand scoping through DataDir, so handing
// it the qualified key would put the brand in the name twice.
func (rs *residency) open(r *resident) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.db != nil {
		return nil
	}
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
	r.db = db
	return nil
}

// arm gives a resident its aggregates and its model, and restores what the
// tenant had learned. Idempotent: an already-armed resident is untouched.
//
// THE RELOAD IS HERE AND ONLY HERE, which is what closes the third defect. The
// process used to latch "restored" in a map that outlived the model, so a model
// dropped by the shared store's LRU was never reloaded — the tenant scored
// nothing for the rest of the process's life while reporting only "warming". A
// cell that has no model has no latch either, because the latch IS the model.
//
// IT TAKES ONE LOCK, AND THAT IS A RULE. rs.mu is only ever taken OUTSIDE r.mu
// (of, retireLocked, close); arming under both is what let an earlier cut call
// retireLocked — which locks every cell — while already holding one of them, and
// a process that deadlocks under its own admission path is a process that has
// stopped deciding.
func (rs *residency) arm(r *resident, log logger) error {
	r.mu.Lock()
	if r.vel != nil && r.model != nil {
		r.mu.Unlock()
		return nil
	}
	db := r.db
	r.mu.Unlock()

	vel := aggregates()
	model, err := forest(anomaly.Config{}, vel)
	if err != nil {
		return err
	}

	// The durable snapshot, read before the cell is armed so a concurrent arm
	// cannot see a half-restored model.
	kept, disarmed := int64(0), false
	if snap, ok, err := readSnapshot(db, r.t); err != nil {
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

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.vel != nil && r.model != nil { // lost the race; the winner's arm stands
		return nil
	}
	r.vel, r.model, r.since = vel, model, time.Now().UTC()
	r.kept, r.disarmed = kept, disarmed
	return nil
}

// retireLocked releases every tenant that has been SILENT for at least idle: its
// model is written to its own file, then its aggregates, its caches and its file
// handle go and its cell is dropped. Caller holds rs.mu.
//
// IT IS NOT EVICTION AND THE DIFFERENCE IS THE WHOLE POINT: the trigger is the
// retired tenant's own silence, so no tenant's traffic can ever cost another
// tenant a ring. Nothing durable is lost — the model is snapshotted first, and
// the rules, lists and decisions were always on the tenant's own file — so the
// tenant's next request comes back with what it learned. The aggregates are the
// one thing that does not survive, which is why `since` rides every decision
// instead of the loss being left for someone to notice.
//
// CLOSING THE FILE IS SAFE WITHOUT A REFERENCE COUNT because idle is floored
// above the longest any worker may hold one (see idleFloor): a cell reaches this
// line only when no request has resolved it for at least that long.
func (rs *residency) retireLocked(now time.Time, idle time.Duration, log logger) {
	for t, r := range rs.cells {
		r.mu.Lock()
		if now.Sub(r.touched) < idle {
			r.mu.Unlock()
			continue
		}
		if err := keep(r.db, r.t, r.model); err != nil {
			// Never drop a model we could not write down: the cell is HELD, and
			// the next sweep tries again. Losing learned state to a housekeeping
			// pass is exactly the silent disarm this file exists to prevent.
			log.Error("risk: a retiring tenant's learned state was not kept, so its cell is held", "tenant", r.t.String(), "err", err)
			r.mu.Unlock()
			continue
		}
		idleFor := now.Sub(r.touched)
		r.release()
		r.mu.Unlock()
		delete(rs.cells, t)
		log.Info("risk: a tenant was retired after its own idleness; its learned state is on its own file",
			"tenant", r.t.String(), "idle", idleFor.String())
	}
}

// release drops everything this cell holds. Caller holds r.mu.
func (r *resident) release() {
	r.vel, r.model = nil, nil
	r.loaded, r.rules, r.lists, r.sups = false, nil, nil, nil
	r.agency.clear()
	if r.db != nil {
		_ = r.db.Close()
		r.db = nil
	}
}

// sweep is the background retire. It runs on a timer so memory comes back from a
// silent tenant without waiting for a busy one to need it — the version that only
// reclaimed under pressure would let one tenant's arrival be the reason another's
// rings went away, which is the thing being ruled out.
func (rs *residency) sweep(log logger) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.retireLocked(time.Now(), idleReclaim(), log)
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

// count reports how many tenants this process holds, how many admissions it has
// refused, and when the last refusal was. All three are on the probe.
func (rs *residency) count() (tenants int, refused int64, at time.Time) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return len(rs.cells), rs.refused, rs.refusedAt
}

// close snapshots every armed tenant and closes every file. It is what makes a
// rollout survivable: cloud deploys Recreate at one replica, so every deploy
// drops the process, and a model that comes back with nothing learned declines to
// score for its whole warm period.
func (rs *residency) close(log logger) (kept, failed int) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for t, r := range rs.cells {
		r.mu.Lock()
		if r.model != nil {
			if err := keep(r.db, r.t, r.model); err != nil {
				failed++
				log.Error("risk: a tenant's learned state was not kept", "tenant", t.String(), "err", err)
			} else {
				kept++
			}
		}
		r.release()
		r.mu.Unlock()
		delete(rs.cells, t)
	}
	return kept, failed
}

// ── what an op reads off a resident ─────────────────────────────────────────

// arms returns the tenant's two in-memory planes and the instant they started.
// A disarmed resident cannot be reached this way: `of` arms before returning.
func (r *resident) arms() (*velocity.Store, *anomaly.Store, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.vel, r.model, r.since
}

// strained reports that this tenant's aggregates are at their own cardinality
// bound, so a count it reads may be an under-count of its own traffic.
//
// It is published rather than logged because the consumer is the tenant: a rule
// that fires on `velocity.ip.1h.count >= 5` stops firing when the key it counts
// was dropped, and there is no other way for the tenant to learn that its
// threshold is being measured against a partial ring.
func (r *resident) strained() bool {
	vel, _, _ := r.arms()
	if vel == nil {
		return false
	}
	return vel.Keys() >= maxKeys()
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
func (r *resident) governance() ([]rule, map[string]map[string]bool, []suppression, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loaded {
		return r.rules, r.lists, r.sups, r.live, nil
	}
	rules, err := loadRules(r.db)
	if err != nil {
		return nil, nil, nil, false, err
	}
	lists, err := loadLists(r.db)
	if err != nil {
		return nil, nil, nil, false, err
	}
	sups, err := loadSuppressions(r.db)
	if err != nil {
		return nil, nil, nil, false, err
	}
	r.rules, r.lists, r.sups = rules, lists, sups
	r.live = mode(r.db) == "live"
	r.loaded = true
	return r.rules, r.lists, r.sups, r.live, nil
}

// reload reinstates this tenant's learned state from its own pinned snapshot,
// over whatever the live model holds.
//
// It is what POST /v1/ml/restore does, and it is also the honest recovery from a
// disarmed model: a tenant told its control is off can put it back on with the
// state it kept. A snapshot belonging to another tenant is refused twice — here,
// by the tenant that asked, and again inside the engine.
func (r *resident) reload() error {
	r.mu.Lock()
	db, model := r.db, r.model
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
	r.loaded, r.rules, r.lists, r.sups = false, nil, nil, nil
	r.mu.Unlock()
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

// logger is the slice of the service's logger this file uses. Narrow on purpose:
// residency is reached from teardown and from a background sweep as well as from
// a request, and a package that took the whole service would be reaching for
// state it has no business touching.
type logger interface {
	Info(msg string, args ...any)
	Error(msg string, args ...any)
}
