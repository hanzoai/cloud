package risk

// ring.go — THE SLIDING AGGREGATES: one set per tenant, bounded per tenant, and
// durable as a projection of that tenant's own record.
//
// WHY THIS FILE EXISTS AT ALL. The rings are the only structure here whose size
// is chosen by the data rather than by us: a key is `<org, axis, subject>` and
// the subject comes off the wire. One process-wide store with a process-wide cap
// therefore has a property nobody would write down on purpose — the busiest
// organisation evicts the quietest one's keys, so a tenant that has done nothing
// wrong reads zero for eight of the nine model dimensions, scores as
// unremarkable, and raises nothing. No error, no log, no alert: the control is
// simply off. That is not a capacity trade, it is a cross-tenant denial of
// service that costs the attacker only ordinary use of their own account.
//
// SO THE SHAPE IS THE FIX, not a bigger number:
//
//   - A ring set belongs to ONE tenant. [newRings] is the only constructor in
//     this package and it always applies the per-tenant bound, so a shared or
//     unbounded store is not something this package can express.
//   - The bound is PER TENANT, derived from a per-tenant byte budget. A tenant
//     that outgrows its own bound evicts its OWN least-recently-active subject.
//     It can degrade itself and nothing else.
//   - The process ceiling is the PRODUCT of that budget and [maxResident], both
//     stated, so the worst case is arithmetic a reviewer can check rather than a
//     hope.
//
// AND THE RINGS ARE DURABLE, which they were not. The binary deploys one replica
// at a time with the old pod stopped before the new one starts, so in-memory
// aggregates are lost on EVERY rollout — every tenant's velocity features would
// read blind until traffic refilled them, which is the same silent-quiet failure
// arriving on a schedule. The rings are therefore not the state: they are a pure
// projection of [observationDDL], the tenant's own durable record of what it
// taught its model, replayed in one deterministic order. Losing a pod loses a
// cache, not a control.
//
// THE RINGS ONLY MOVE FORWARD. velocity folds an event older than a window's
// span to the leading edge and counts it late — correct for a compliance feed
// that must not drop a record, wrong for a live detector, because it means a
// backdated event is counted as having happened NOW. [placeable] refuses that:
// an observation the rings cannot hold at its own bucket does not enter them.
// The model still learns from it; the aggregates are not told a lie about when.

import (
	"container/list"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/types"
	"github.com/luxfi/aml/pkg/velocity"
	"github.com/zap-proto/zip"
)

// ── the bound every other bound is made of ───────────────────────────────────
//
// A CAP ON A COUNT OF CALLER-SIZED VALUES IS NOT A BOUND. Every ceiling below is
// a COUNT multiplied by the size of something a caller chose — a subject in a
// ring key, an event id in a row — so while that size is unbounded, so is the
// product, and a published "8 MiB of aggregates per tenant" derived from a KEY
// COUNT is understated by whatever the caller picked. [maxField] and [maxTenant]
// are what turn every product below from a hope into arithmetic, and
// [TestBounds_ArePublishedInTheDimensionThatBinds] MEASURES a real worst case
// against each published figure rather than recomputing the same formula.

// maxField bounds, IN BYTES, every string an observation carries that its caller
// chose: the event id, the subject, the counterparty and the device.
//
// 128 bytes is longer than any identifier a real system carries — a UUID is 36,
// a Stripe id 30, a SHA-256 fingerprint in hex 64 — and it is REFUSED rather than
// truncated at the one entry point that mints an observation ([observe]),
// because two subjects differing only past the cut would silently become one
// set of aggregates: a wrong answer wearing a right one's clothes.
const maxField = 128

// maxAxis is the longest aggregation axis name velocity keys on ("account",
// "device", "pair"), and maxKind the longest subject kind ("account"). Both are
// closed sets, so both are bounded by their own longest member.
const (
	maxAxis = len(anomaly.AxisAccount)
	maxKind = len(kindAccount)
)

// maxKeyText is the worst-case size of ONE velocity key, which is
// `<tenant>\x00<axis>\x00<value>` and whose widest value is the PAIR axis:
// `<kind>:<subject>\x1f<peer>`. Written as the sum rather than as a number so a
// change to any term moves the ceiling with it.
const maxKeyText = maxTenant + 1 + maxAxis + 1 + (maxKind + 1 + maxField) + 1 + maxField

// perSubjectBytes is what ONE of a tenant's subjects costs in memory: the
// standard window set is 60+96+168+120 = 444 ring slots at 48 bytes each, plus
// the key text held twice (once by the store's own map, once by the census that
// makes its eviction countable) and struct/map overhead for both.
const perSubjectBytes = 444*48 + 2*maxKeyText + 576

// shards is how many ways velocity divides its keyspace, MIRRORED here because
// it applies MaxKeys as `MaxKeys/shards+1` PER SHARD — so the most a store can
// actually hold is [ringKeyCeiling] and not MaxKeys, and a ceiling computed from
// MaxKeys alone is understated by a shard's worth of slack. The mirror is proved
// by measurement, not by reading upstream: [TestRings_TheCeilingIsMeasuredNotAsserted]
// fills one tenant's rings past the bound and fails if the store holds more keys
// than the ceiling states.
const shards = 64

// residentRingBudget is what ONE tenant's aggregates may cost. It is the
// TENANT-FACING bound, and it is the whole point: a tenant that needs more
// subjects than this forgets its OWN least-recently-active subject and degrades
// only itself.
const residentRingBudget = 8 << 20

// ringKeys is that budget in subjects, DISCOUNTED BY A SHARD'S SLACK so that the
// figure the budget actually buys is [ringKeyCeiling] — the number of keys the
// store can hold — rather than this one.
const ringKeys = residentRingBudget/perSubjectBytes - shards

// ringKeyCeiling is the most subjects one tenant's rings can hold, and it is the
// number [residentRingBudget] is spent on.
const ringKeyCeiling = shards * (ringKeys/shards + 1)

// maxResident bounds how many tenants are held at once, and with
// residentRingBudget it IS the process ceiling: 64 × (8 MiB of rings + a measured
// ~336 KiB of model counters) ≈ 533 MiB, against the 9 GiB GOMEMLIMIT the
// deployment sets.
//
// Eviction here is LOSSLESS and it is not silent. The model's learned state is
// snapshotted to the tenant's own shelf before the resident is dropped, the rings
// are a projection of a durable record, and the count is reported on the probe —
// so a tenant evicted by another tenant's arrival pays a rebuild and never a loss.
// That is the difference between this bound and the one it replaces.
const maxResident = 64

// planeRingCeiling is the worst case, stated so a test can hold it.
const planeRingCeiling = maxResident * residentRingBudget

// ringWindow is the longest span the aggregates keep, and therefore the retention
// of the durable record and the horizon a wire endpoint will accept a stamp
// inside. It is the same thirty days the feature inventory's widest window
// reads, because keeping a record the rings could never hold is keeping it for
// nothing.
const ringWindow = 30 * 24 * time.Hour

// burstWindow is the NARROWEST span the aggregates keep, which makes it the window
// the aggregate RULES read ([rings.pace] takes the shortest one the store actually
// holds) and therefore the horizon an observation must be placeable inside
// ([placeable]).
//
// IT IS MEASURED OFF THE WINDOWS THE STORE IS BUILT WITH, never written down as an
// hour. [newRings] is the one constructor and it takes velocity's own standard
// windows, so this is the same set — and a deployment that changed them moves this
// with them instead of leaving a rule reading a window nobody keeps.
//
// WHY THE NARROWEST IS THE ONE THAT BINDS. velocity folds an event older than a
// window's span to that window's leading edge, so the horizon a placement must
// clear is the SHORTEST window, not the longest: an event two hours old clears the
// thirty-day ring at its own bucket and is folded to NOW in the one-hour ring. The
// bound that used to be applied here was [ringWindow], which is the widest — it
// refused an event the widest ring could not hold and admitted every event that
// lands in the narrowest one as if it had just happened, which is the whole
// mechanism the burst bounds are read over.
var burstWindow = func() time.Duration {
	var out time.Duration
	for _, w := range velocity.StandardWindows() {
		if out == 0 || w.Span < out {
			out = w.Span
		}
	}
	return out
}()

// maxRowBytes is what ONE recorded observation costs on the tenant's own shelf,
// worst case, INCLUDING its covering index and SQLite's own per-row overhead. It
// is derived from the field bound and then MEASURED — a derivation nobody checks
// against a real file is a guess, and this is the term the disk ceiling is a
// multiple of.
const maxRowBytes = maxTenant + 4*maxField + maxKind + 16 + 512

// recordBudget is what ONE tenant's durable record may cost ON DISK. Bytes and
// not rows, because bytes is the dimension that is actually shared: every org's
// shelf lives on ONE volume, so a per-tenant record bounded only in rows is a
// tenant filling the disk that other tenants' models are stored on — the same
// cross-tenant failure as a shared cap, wearing a filesystem.
const recordBudget = 32 << 20

// recordRows is that budget in rows. It is the bound the prune enforces and the
// bound the replay reads, because they must be the same number: a record that
// keeps more than a rebuild will ever read is a record kept for nothing.
const recordRows = recordBudget / maxRowBytes

// rings is ONE tenant's aggregates.
//
// It wraps velocity's store rather than being it, for one reason: velocity's
// bound is enforced by SILENTLY evicting the least-recently-updated key, and a
// sensor that switches itself off without saying so is worse than no sensor. A
// forgotten subject reads as "has done nothing", scores as unremarkable, and
// raises nothing — which is exactly what a clean bill of health looks like.
//
// The CENSUS is what makes that countable: a least-recently-used set of the keys
// this tenant has introduced, held at the same [ringKeyCeiling] as the store it
// mirrors, so it is not a second unbounded structure and its cost is counted in
// [perSubjectBytes]. Every time the census has to drop a key to admit a new one,
// the store has had to do the same thing, and [strain.Forgotten] counts it.
//
// It is a LOWER BOUND and says so: velocity applies its cap PER SHARD, so it
// starts forgetting before a flat ceiling would. Under-reporting is the right
// direction for a number an operator acts on — it can only be worse than stated,
// never better.
type rings struct {
	vel *velocity.Store

	mu sync.Mutex
	// seen is the census: key text → its node in `order`.
	seen map[string]*list.Element
	// order is the census in least-recently-introduced order, front = oldest.
	order *list.List
	// live is the store's own key count as of the last [rings.reconcile].
	live int
	// lost is how many of this tenant's own subjects the CENSUS has had to forget to
	// stay inside its bound.
	lost int64
	// shed is how many of this tenant's subjects the STORE has dropped that the
	// census still lists — the per-shard eviction a flat count cannot see. It is a
	// high-water mark, set by [rings.reconcile]; see there for why the census misses
	// it and why it does not fall back.
	shed int
}

// strain is one tenant's aggregate pressure, in the two numbers that matter and
// one state that must never be inferred.
//
// It is REPORTED — on that organisation's own model state and, as counts with no
// tenant named, on the probe — because the failure this whole file exists to
// prevent is a control going quiet. Rings at their bound are a control that has
// started forgetting; saying so is the difference between capacity pressure an
// operator can act on and a detector that reads clean because it reads nothing.
type strain struct {
	// Subjects is how many of this organisation's subjects the aggregates hold.
	Subjects int
	// Bound is the most they can hold.
	Bound int
	// Forgotten is how many of its own subjects have been dropped to stay inside
	// that bound. Every one of them reads as "has done nothing" until it is
	// active again.
	Forgotten int64
	// Saturated is whether the bound is being hit right now. It is the state, and
	// the two counts are its evidence.
	Saturated bool
}

// newRings mints ONE tenant's aggregates. It is the ONLY constructor in this
// package: there is no exported one, no package-level store, and no call to
// velocity.New anywhere else, so a process-wide ring set is not a thing this
// package can be made to build ([TestRings_HaveOneConstructor]).
func newRings() *rings {
	return &rings{
		vel:   velocity.New(velocity.Config{MaxKeys: ringKeys}),
		seen:  make(map[string]*list.Element, 64),
		order: list.New(),
	}
}

// record puts one transaction into a ring set on every axis it names. It is the
// one place velocity.Record is called with the reporting threshold, so the live
// path, the rebuild and the search sandbox cannot disagree about what a key is or
// what "just under" means.
func (r *rings) record(tx types.Transaction) {
	for _, k := range anomaly.Keys(tx) {
		r.vel.Record(k, tx.Timestamp, tx.USD, reportingThreshold)
		r.census(k.OrgID + "\x00" + k.Kind + "\x00" + k.Value)
	}
}

// census notes one key, and counts the subject the bound displaced to make room.
func (r *rings) census(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if at, held := r.seen[id]; held {
		r.order.MoveToBack(at)
		return
	}
	if r.order.Len() >= ringKeyCeiling {
		oldest := r.order.Front()
		delete(r.seen, oldest.Value.(string))
		r.order.Remove(oldest)
		r.lost++
	}
	r.seen[id] = r.order.PushBack(id)
}

// reconcile reads the store's live key count and, with it, how many of this
// tenant's subjects the STORE has dropped that the census still lists. Call it
// once per batch and never per event: it locks every shard.
//
// THIS IS WHERE THE QUIET HALF OF THE BOUND BECOMES VISIBLE. velocity applies its
// cap PER SHARD — MaxKeys/shards+1, five keys against a census ceiling of 320 —
// and subjects hash unevenly, so a shard fills long before the total does and the
// store drops that shard's least-recently-used key. The census is a flat count and
// sees none of it: measured, 200 subjects in leaves the store holding 198 with the
// census reporting nothing forgotten and not saturated. Two of that organisation's
// own subjects read as "has done nothing", score as unremarkable, and raise
// nothing.
//
// The census and the store are fed from the same key set, one apiece per subject,
// so a store holding FEWER than the census lists holds exactly the difference in
// dropped subjects. Both numbers are read under r.mu so they describe one moment;
// read apart, a concurrent record between them shows up as a loss that did not
// happen.
//
// It is a HIGH-WATER MARK because forgetting is not undone: the subject is gone
// and stays gone until it is active again. A gauge that fell back to zero as the
// shard refilled would report a control switching itself on and off.
func (r *rings) reconcile() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.live = r.vel.Keys()
	if short := r.order.Len() - r.live; short > r.shed {
		r.shed = short
	}
}

// strain reports this tenant's aggregate pressure.
//
// Forgotten is the census's own evictions PLUS the store's, because they are
// different subjects lost to the same bound and an operator acting on the number
// needs all of them.
func (r *rings) strain() strain {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strain{
		Subjects:  r.live,
		Bound:     ringKeyCeiling,
		Forgotten: r.lost + int64(r.shed),
		Saturated: r.lost > 0 || r.shed > 0 || r.order.Len() >= ringKeyCeiling,
	}
}

// skew is how far AHEAD of this plane's clock an event may be stamped and still
// be believed. Two minutes covers ordinary clock drift between a caller's machine
// and ours; anything beyond it is not drift.
//
// The bound matters more than its size. The aggregates track a LEADING EDGE, so
// one event stamped in the future moves that edge there — and from then on every
// real event for that subject is older than every window, is folded to the edge,
// and reads back as if the subject had done nothing. One request, and that
// subject's velocity features are blind for as long as the state lives. It is
// per-subject detector evasion available to any authenticated caller, for free,
// so an unbelievable timestamp is REFUSED and never quietly accepted.
const skew = 2 * time.Minute

// within reports whether an event stamped at is inside the window the endpoint
// reading it covers, and says which side it fell out of.
//
// ONE definition, used at every endpoint: the learn wire
// ([riskEvent.observation]) and the surface replay ([replayable]) both call it,
// so a bound cannot be enforced on the path a reviewer looked at and missing
// on the one they did not. `back` is the endpoint's own lookback — thirty days
// for the live wire, the requested window for a replay — because how far back
// is legitimate is a property of the endpoint and how far FORWARD is legitimate
// never is.
func within(at, now time.Time, back time.Duration) error {
	switch {
	case at.After(now.Add(skew)):
		return zip.ErrBadRequest("'at' is in the future — an event that has not happened cannot be learned from")
	case at.Before(now.Add(-back)):
		return zip.ErrBadRequest("'at' is older than " + back.String() + " — history is folded in from your own event surface, not through this endpoint")
	default:
		return nil
	}
}

// placeable reports whether the rings may hold an observation at its own bucket,
// given the newest timestamp they already carry and the plane's own clock.
//
// TWO REFUSALS, ONE RULE, and it is the rings' own structural defence rather than
// a validation an endpoint might forget:
//
//   - AHEAD OF THE CLOCK. The aggregates track a leading edge, so an event from
//     the future moves it there and every later real event is then older than
//     every window — the subject reads as having done nothing, permanently. The
//     wire endpoint refuses such a stamp with a 400 ([within]); this refuses it
//     again, here, so no path into the rings can poison the edge.
//
//   - BEHIND THE WINDOW THE RULES READ. velocity folds anything older than a
//     window's span to the leading edge — the right call for a compliance
//     aggregate that must not drop a record, and the wrong one for a live
//     detector, where it means an event from last month lands in the last hour's
//     count.
//
//     THE WINDOW IS [burstWindow] AND NOT [ringWindow], and that is the
//     correction. The guard was applied against the WIDEST span the rings keep,
//     which is the one span a fold cannot reach: an event an hour and a minute old
//     is comfortably inside thirty days, so it was admitted — and then folded to
//     NOW in the one-hour ring, which is the ring [onPace] reads its count and its
//     accrual from. A backdated or out-of-order event therefore counted as having
//     just happened in the only window that decides anything, which is a burst
//     detector a caller can fill with history. Measuring against the narrowest
//     window is what makes the rings' own claim — that they only move forward —
//     true of the window the rules are read over.
//
//     What it costs is stated rather than hidden: an event more than an hour
//     behind the edge no longer enters the WIDER rings either, because velocity
//     writes all four windows in one call and the placement is per event, not per
//     ring. The model reads those wider windows, so it loses a late arrival — and
//     it still LEARNS from the event itself ([resident.mod] is told regardless).
//     A wider aggregate missing one late event is a slightly stale baseline; the
//     narrowest one gaining it is a rule freezing a payment for something that
//     happened last month.
//
// An observation that fails either is still LEARNED FROM. It just does not get to
// say it happened now.
func placeable(at, edge, now time.Time) bool {
	if at.After(now.Add(skew)) {
		return false
	}
	return edge.IsZero() || at.After(edge.Add(-burstWindow))
}

// ── the durable record ───────────────────────────────────────────────────────

// observationDDL is the tenant's own record of what it taught its model: the
// events it sent through the learn endpoint, and nothing else. The warehouse
// already holds what the organisation EMITTED; this holds what it TAUGHT, which
// is a different fact with a different owner.
//
// The primary key is (tenant, id) and not id. A shelf file is keyed on the bare
// org slug, so two brands' identically named organisations share one file — the
// qualified tenant is what tells their rows apart, and it is the leading term of
// the key and of every statement below.
const observationDDL = `CREATE TABLE IF NOT EXISTS observation (
	tenant  TEXT NOT NULL,
	id      TEXT NOT NULL,
	kind    TEXT NOT NULL,
	subject TEXT NOT NULL,
	peer    TEXT NOT NULL,
	device  TEXT NOT NULL,
	usd     REAL NOT NULL,
	at      INTEGER NOT NULL,
	PRIMARY KEY (tenant, id)
)`

// observationIndexDDL orders the record the way the replay reads it, so a rebuild
// is a range scan under the tenant and never a scan of the file.
const observationIndexDDL = `CREATE INDEX IF NOT EXISTS observation_at ON observation(tenant, at, id)`

// note writes observations to the tenant's own shelf and prunes what the rings
// could no longer hold, in ONE transaction.
//
// It runs BEFORE anything moves in memory. A learn that cannot be written down is
// a learn that a rollout silently undoes, so it is refused instead: the caller is
// told, rather than being given a verdict from state that will not survive the
// next deploy.
//
// It is idempotent on the caller's own event id — a retried batch converges
// instead of double-counting, which is the property a client with a timeout needs
// and cannot get any other way. It says SO, per observation: `first[i]` reports
// whether that event entered the record on this call, and the caller learns from
// exactly those. Convergence of the record alone is not convergence — the rings
// and the masses are in memory, and a retry that skipped the row and still moved
// them counts the event twice in the numbers a decision is made from.
func (p *plane) note(t tenant, obs []observation) (first []bool, err error) {
	if len(obs) == 0 {
		return nil, nil
	}
	sh, err := p.for_(t)
	if err != nil {
		return nil, err
	}
	tx, err := sh.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("risk: record observations: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	ins, err := tx.Prepare(`INSERT INTO observation (tenant, id, kind, subject, peer, device, usd, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(tenant, id) DO NOTHING`)
	if err != nil {
		return nil, fmt.Errorf("risk: record observations: %w", err)
	}
	defer func() { _ = ins.Close() }()
	first = make([]bool, len(obs))
	for i, o := range obs {
		res, err := ins.Exec(string(t), o.id, o.kind, o.subject, o.peer, o.device, o.usd, o.at.UTC().Unix())
		if err != nil {
			return nil, fmt.Errorf("risk: record observation %q: %w", o.id, err)
		}
		// A conflicting row affects nothing, so this IS the answer to "was it
		// already here" — read off the write itself rather than from a second
		// statement that could disagree with it.
		n, err := res.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("risk: record observation %q: %w", o.id, err)
		}
		first[i] = n > 0
	}
	// TWO RETENTION BOUNDS, BOTH PER TENANT, both in this transaction. Pruning
	// here rather than on a sweep keeps them unconditional — there is no schedule
	// to miss.
	//
	// By AGE, because a record the aggregates could never hold again is a record
	// kept for nothing.
	if _, err := tx.Exec(`DELETE FROM observation WHERE tenant = ? AND at < ?`,
		string(t), p.now().UTC().Add(-ringWindow).Unix()); err != nil {
		return nil, fmt.Errorf("risk: prune observations: %w", err)
	}
	// By SIZE, because age alone bounds nothing a caller controls. The cut is
	// spelled in rows and the bound is stated in BYTES ([recordBudget]): the row
	// count is the byte budget divided by [maxRowBytes], which is a real number
	// only because [maxField] bounds every caller-chosen string in the row. A cap
	// on the NUMBER of rows while the caller sizes each one is not a bound at all.
	//
	// The cut is on (at, id) as a PAIR and not on `at` alone. Timestamps are stored
	// at one-second resolution, so a caller sending a thousand events inside one
	// second gives every one of them the same `at` — and a cut at `at < boundary`
	// then deletes none of them, which is an unbounded record reached by ordinary
	// batching. The pair is the record's own order, so the cut is exact.
	//
	// The boundary row is the recordRows-th newest and it SURVIVES — everything
	// strictly older goes — so exactly recordRows remain. The subselect walks the
	// tenant's own covering index and stops there; below the bound it yields NULL
	// and the comparison deletes nothing.
	if _, err := tx.Exec(`DELETE FROM observation WHERE tenant = ? AND (at, id) < (
			SELECT at, id FROM observation WHERE tenant = ? ORDER BY at DESC, id DESC LIMIT 1 OFFSET ?)`,
		string(t), string(t), recordRows-1); err != nil {
		return nil, fmt.Errorf("risk: prune observations: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("risk: record observations: %w", err)
	}
	return first, nil
}

// rebuild reconstructs one tenant's aggregates from its own durable record, and
// returns the newest timestamp it carries.
//
// DETERMINISTIC, and that word is doing work: the replay is ordered by (at, id),
// the timestamps are stored at one-second resolution and the finest ring bucket
// is a minute, so the same record rebuilds the same rings on every process. That
// is what makes a rollout a rebuild rather than a blindness.
//
// The rebuild EQUALS what the live rings held when the events were taught in time
// order, which is what the learn endpoint asks for ("oldest first"). Taught
// out of order across a window boundary the two can differ, because a live ring
// cannot re-place an event whose leading edge has already moved past it and a
// replay can. Said here rather than claimed away: the property that holds
// unconditionally is that the same record rebuilds the same aggregates.
//
// A record that cannot be read yields EMPTY rings and the error. Empty rings are
// honest — every velocity feature then reads blind, which the model reports — and
// a tenant that cannot reach its own shelf must not be handed somebody's leftover
// aggregates instead.
func (p *plane) rebuild(t tenant) (*rings, time.Time, int, error) {
	vel := newRings()
	sh, err := p.for_(t)
	if err != nil {
		return vel, time.Time{}, 0, err
	}
	// Newest first with a bound, then reversed: the bound has to select the RECENT
	// end of the record, and the replay has to run oldest first.
	rows, err := sh.db.Query(
		`SELECT id, kind, subject, peer, device, usd, at FROM observation
		 WHERE tenant = ? AND at >= ? ORDER BY at DESC, id DESC LIMIT ?`,
		string(t), p.now().UTC().Add(-ringWindow).Unix(), recordRows)
	if err != nil {
		return vel, time.Time{}, 0, fmt.Errorf("risk: replay observations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	held := make([]observation, 0, 256)
	for rows.Next() {
		var id, kind, subject, peer, device string
		var usd float64
		var at int64
		if err := rows.Scan(&id, &kind, &subject, &peer, &device, &usd, &at); err != nil {
			return vel, time.Time{}, 0, fmt.Errorf("risk: replay observations: %w", err)
		}
		// THROUGH THE ONE ENTRY POINT, even on the way back in. A row written before a
		// bound existed, or by a build that did not have one, does not enter the
		// aggregates: the ceilings this plane publishes are counts of what [observe]
		// admits, so a row it would refuse is a row the ceiling does not cover.
		//
		// THE ROW IS SKIPPED AND SAID; THE REPLAY IS NOT ABANDONED. Refusing the whole
		// rebuild would make one unplaceable row a tenant-wide outage that repeats on
		// every residency until the row ages out of retention — every velocity feature
		// blind for up to thirty days, for a single bad value written by an older
		// build. That is a denial of service reachable through the learn endpoint,
		// and the endpoint is the tenant's own. Losing the row degrades the one
		// subject on it, which is the blast radius every other bound in this file is
		// held to.
		o, err := observe(id, actor{Kind: kind, Subject: subject, Peer: peer, Device: device}, usd, time.Unix(at, 0).UTC())
		if err != nil {
			p.log.Warn("a recorded observation cannot enter the aggregates; that subject rebuilds without it",
				"tenant", string(t), "event", id, "err", err)
			continue
		}
		held = append(held, o)
	}
	if err := rows.Err(); err != nil {
		return vel, time.Time{}, 0, fmt.Errorf("risk: replay observations: %w", err)
	}
	var edge time.Time
	for _, o := range slices.Backward(held) {
		vel.record(o.tx(t))
		if o.at.After(edge) {
			edge = o.at
		}
	}
	vel.reconcile()
	return vel, edge, len(held), nil
}

// ── what the aggregates already hold ─────────────────────────────────────────
//
// Everything above this line is how the aggregates are FILLED. This is how they
// are READ by a rule rather than by a model, and the two readers want different
// things: the model wants nine coordinates measured against this organisation's
// own baselines, and a rule wants two plain counts it can compare against a
// stated bound. Reading the model's features to recover a count would be reading
// a ratio to recover its numerator.
//
// IT IS THE SUBJECT'S HISTORY AND NOT THIS EVENT. [plane.score] does not record,
// so the event being judged is not in these numbers — which is the correct
// reading and worth saying out loud: the bounds below are what an identifier had
// ALREADY done when it arrived.

// The identifiers a determination can name, in the vocabulary an operator reads
// rather than the aggregation-axis names the engine keys on. Both are closed
// sets, so this is a translation and never an open string.
const (
	// axisSubject is the payer's own account, namespaced by its kind.
	axisSubject = "subject"
	// axisPair is the payer and one counterparty together.
	axisPair = "counterparty pair"
	// axisDevice is the device fingerprint.
	axisDevice = "device"
	// axisPeer is the counterparty alone. It is NOT an aggregation axis — velocity
	// keys a counterparty only in the pair — so it appears on the fan-out reading
	// and nowhere else.
	axisPeer = "counterparty"
)

// axisOf translates one aggregation axis into the word a determination says. An
// axis this app does not know is carried through as itself: a new upstream axis
// must read oddly in a cause, never silently as one of these.
func axisOf(kind string) string {
	switch kind {
	case anomaly.AxisAccount:
		return axisSubject
	case anomaly.AxisPair:
		return axisPair
	case anomaly.AxisDevice:
		return axisDevice
	}
	return kind
}

// paced is what the aggregates hold for ONE of an event's axes over their
// narrowest window: how many events, and how much they moved.
type paced struct {
	// Axis is which identifier this counts, from the closed set above.
	Axis string
	// Events and Nano are the window's plain count and its accrued value, the
	// latter in the unit the stated bounds are written in.
	Events int
	Nano   int64
	// Span is the window they were read over, carried so a test can hold the rule
	// to the window it claims rather than to whichever one it happened to get.
	Span time.Duration
}

// shared is how many DISTINCT subjects one of an event's LINK identifiers is
// already tied to.
type shared struct {
	// Axis is which identifier is shared, from the closed set above.
	Axis string
	// Subjects is how many distinct subjects the tenant's own record ties to it,
	// counted no further than the stated bound: the rule asks whether the bound is
	// reached and a larger number would answer a question nobody asked at a cost
	// nobody bounded.
	Subjects int
}

// reading is what ONE tenant's own aggregates already hold about the identifiers
// on ONE event, taken at the moment that event is judged.
//
// The ZERO VALUE is "the aggregates said nothing", which is the honest reading
// for an event whose identifiers this organisation has never seen — and it makes
// every rule over it silent rather than firing on a fresh subject.
type reading struct {
	// Pace is one entry per aggregation axis the event names ([anomaly.Keys]).
	Pace []paced
	// Shared is one entry per LINK identifier the event carries — its device and
	// its counterparty. An identifier the event does not carry is absent rather
	// than counted as the empty string, for the same reason [anomaly.Keys] omits
	// it: every anonymous event in the tenant would otherwise pool into one.
	Shared []shared
}

// pace reads what one key's aggregates hold over the NARROWEST window they keep,
// and reports whether they keep one at all.
//
// THE WINDOW IS TAKEN AND NEVER NAMED. velocity's own documentation says why a
// name is the wrong handle — "a caller that reads observations by name can check
// at construction that the names it needs exist rather than silently reading zero
// for a window nobody configured" — and a control that reads zero because a name
// was misspelled is a control that is off with nothing to see. So the window is
// selected by being the shortest one the store actually keeps: there is no name
// to get wrong, and a store keeping no windows at all is REFUSED here rather than
// answered with a zero that reads exactly like a quiet subject.
//
// The narrowest window is also the right one on its own terms. It is the burst
// window — the finest resolution the aggregates offer — and a burst is what these
// bounds are about; the wider windows answer "how much does this subject usually
// do", which is the model's question and not this one's.
//
// It does not take the ring set's own lock: [velocity.Store] is documented safe
// for concurrent use and shards its own locking, and the census that r.mu guards
// is not read here.
func (r *rings) pace(k velocity.Key) (velocity.Observation, bool) {
	var out velocity.Observation
	for _, o := range r.vel.Observe(k) {
		if out.Span == 0 || o.Span < out.Span {
			out = o
		}
	}
	return out, out.Span > 0
}

// sharedByDevice and sharedByPeer count how many DISTINCT subjects one
// identifier is already tied to across the tenant's own retained record.
//
// TWO STATEMENTS AND NOT ONE PARAMETERISED BY A COLUMN NAME. The column is the
// only difference and it is the one thing that must never come from a value, so
// it is spelled in the statement rather than substituted into it; every term that
// IS caller data is bound.
//
// The `at` predicate is the record's own retention ([ringWindow]) and it is what
// makes the read a bounded range scan of the tenant's own covering index
// (observation(tenant, at, id)) rather than a scan of the file. The LIMIT is
// [fanSubjects]: the rule asks whether the bound is REACHED, so counting past it
// is work with no reader, and DISTINCT under a LIMIT stops as soon as it is.
const sharedByDevice = `SELECT COUNT(*) FROM (
	SELECT DISTINCT kind, subject FROM observation
	 WHERE tenant = ? AND at >= ? AND device = ? LIMIT ?)`

const sharedByPeer = `SELECT COUNT(*) FROM (
	SELECT DISTINCT kind, subject FROM observation
	 WHERE tenant = ? AND at >= ? AND peer = ? LIMIT ?)`

// sharing counts the distinct subjects one identifier is tied to, no further than
// the stated bound.
func (p *plane) sharing(t tenant, statement, value string) (int, error) {
	sh, err := p.for_(t)
	if err != nil {
		return 0, err
	}
	var n int
	if err := sh.db.QueryRow(statement, string(t),
		p.now().UTC().Add(-ringWindow).Unix(), value, fanSubjects).Scan(&n); err != nil {
		return 0, fmt.Errorf("risk: count the subjects sharing an identifier: %w", err)
	}
	return n, nil
}

// prior reads what this tenant's own aggregates already hold about one event's
// identifiers, and it is the ONE entry point to that reading: the axes come from
// [anomaly.Keys], which is the same definition the ingest path records to and the
// model reads, so a rule can never be evaluated on a key the aggregates were
// never filled on.
//
// IT REFUSES RATHER THAN READING ZERO. Every failure here — an unreadable shelf,
// a ring set keeping no window — is a state in which the aggregate rules cannot
// decide, and an empty reading would make them SILENTLY allow. So it is an error,
// answered by the same fail policy as any other failure of this op, which is the
// caller's to apply. It is also a state the resident's own construction makes
// unreachable: [newRings] always keeps velocity's standard windows and the shelf
// is already open by the time a residency exists.
func (p *plane) prior(t tenant, o observation) (reading, error) {
	r, err := p.resident(t)
	if err != nil {
		return reading{}, err
	}
	// THE EDGE, TAKEN ONCE, UNDER THE MODEL'S OWN LOCK. It is what makes the pace
	// reading below a statement about NOW rather than about whenever this tenant was
	// last taught anything; read outside the lock it could be torn by a concurrent
	// learn advancing it.
	r.mu.Lock()
	edge := r.edge
	r.mu.Unlock()
	now := p.now().UTC()
	var out reading
	for _, k := range anomaly.Keys(o.tx(t)) {
		w, kept := r.vel.pace(k)
		if !kept {
			return reading{}, fmt.Errorf("risk: this organisation's aggregates keep no window, so no bound over them can be read")
		}
		// A STALE READING IS NOT A READING, and without this test it is a permanent
		// finding. A velocity window is anchored to the LAST EVENT the rings were
		// taught, not to the clock: the ring sums the buckets in [lead-N, lead], so a
		// subject that did sixty things in one hour last Tuesday reads sixty events in
		// "the last hour" forever after. Nothing clears it, because the only op that
		// could — a decide — deliberately records nothing, so the edge never advances
		// and the window never slides off. One busy hour, and every later payment by
		// that organisation is frozen by a burst that finished a week ago.
		//
		// So the reading is believed only while the aggregates have been taught
		// something inside the span they claim to describe. Past that the two aggregate
		// halves fall silent for this axis and the event's own stated facts are the
		// whole rule, which is the same reading a subject with no history gets — and
		// the honest one, since a window with nothing in it is what "has done nothing
		// lately" means. It is what makes the narrowest-window claim ([burstWindow])
		// true of the number a rule acts on rather than only of the ring it came from.
		//
		// THE EDGE IS THE TENANT'S, NOT THE KEY'S, and that is a stated limitation
		// rather than a hidden one. [velocity.Observation] carries no per-key leading
		// edge, so the finest recency this plane can test is "has this organisation
		// been taught anything inside the span". It clears the case that matters — a
		// self-serve organisation is its own payer, so its edge IS its payer's edge —
		// and it leaves one open: a busy organisation's quiet subject can still read a
		// week-old hour as current, because another subject's traffic keeps the edge
		// fresh. Closing that needs the per-key edge from the engine; until then this
		// is the bound that exists, said out loud.
		if now.Sub(edge) > w.Span {
			continue
		}
		out.Pace = append(out.Pace, paced{
			Axis: axisOf(k.Kind), Events: w.Count, Nano: nanoOfUSD(w.Sum), Span: w.Span,
		})
	}
	for _, link := range []struct{ axis, statement, value string }{
		{axisDevice, sharedByDevice, o.device},
		{axisPeer, sharedByPeer, o.peer},
	} {
		if link.value == "" {
			continue
		}
		n, err := p.sharing(t, link.statement, link.value)
		if err != nil {
			return reading{}, err
		}
		out.Shared = append(out.Shared, shared{Axis: link.axis, Subjects: n})
	}
	return out, nil
}

// nanoOfUSD converts a value the aggregates carry in USD into the unit the stated
// bounds are written in.
//
// It SATURATES rather than wrapping. A sum past the int64 nano ceiling is about
// nine billion dollars, which no real accrual reaches — but a conversion that
// wrapped would turn the largest accrual there is into a small or negative one,
// and the rule would read the worst event it will ever see as unremarkable.
// Saturating is the direction that can only make a bound fire, never disable it.
func nanoOfUSD(usd float64) int64 {
	switch {
	case !(usd > 0): // also catches NaN, which is neither > nor <= 0
		return 0
	case usd >= float64(math.MaxInt64)/nanoPerUSD:
		return math.MaxInt64
	}
	return int64(usd * nanoPerUSD)
}
