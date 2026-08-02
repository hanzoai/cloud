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
// truncated at the one door that mints an observation ([observe]), because two
// subjects differing only past the cut would silently become one set of
// aggregates: a wrong answer wearing a right one's clothes.
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

// ringWindow is the longest span the aggregates keep, and therefore both the
// retention of the durable record and the horizon beyond which an observation
// cannot be placed. It is the same thirty days the feature inventory's widest
// window reads, because keeping a record the rings could never hold is keeping it
// for nothing.
const ringWindow = 30 * 24 * time.Hour

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
	// lost is how many of this tenant's own subjects these aggregates have had to
	// forget to stay inside their bound.
	lost int64
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

// reconcile reads the store's live key count. Call it once per batch and never
// per event: it locks every shard.
func (r *rings) reconcile() {
	live := r.vel.Keys()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.live = live
}

// strain reports this tenant's aggregate pressure.
func (r *rings) strain() strain {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strain{
		Subjects:  r.live,
		Bound:     ringKeyCeiling,
		Forgotten: r.lost,
		Saturated: r.lost > 0 || r.order.Len() >= ringKeyCeiling,
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

// within reports whether an event stamped at is inside the window the door
// reading it covers, and says which side it fell out of.
//
// ONE definition, used at every door: the learn wire ([riskEvent.observation]) and
// the surface replay ([replayable]) both call it, so a bound cannot be enforced
// on the path a reviewer looked at and missing on the one they did not. `back` is
// the door's own lookback — thirty days for the live wire, the requested window
// for a replay — because how far back is legitimate is a property of the door and
// how far FORWARD is legitimate never is.
func within(at, now time.Time, back time.Duration) error {
	switch {
	case at.After(now.Add(skew)):
		return zip.ErrBadRequest("'at' is in the future — an event that has not happened cannot be learned from")
	case at.Before(now.Add(-back)):
		return zip.ErrBadRequest("'at' is older than " + back.String() + " — history is folded in from your own event surface, not through this door")
	default:
		return nil
	}
}

// placeable reports whether the rings may hold an observation at its own bucket,
// given the newest timestamp they already carry and the plane's own clock.
//
// TWO REFUSALS, ONE RULE, and it is the rings' own structural defence rather than
// a validation a door might forget:
//
//   - AHEAD OF THE CLOCK. The aggregates track a leading edge, so an event from
//     the future moves it there and every later real event is then older than
//     every window — the subject reads as having done nothing, permanently. The
//     wire door refuses such a stamp with a 400 ([within]); this refuses it again,
//     here, so no path into the rings can poison the edge.
//   - BEHIND THE WINDOW. velocity folds anything older than a window's span to
//     the leading edge — the right call for a compliance aggregate that must not
//     drop a record, and the wrong one for a live detector, where it means an
//     event from last month lands in the last hour's count.
//
// An observation that fails either is still LEARNED FROM. It just does not get to
// say it happened now.
func placeable(at, edge, now time.Time) bool {
	if at.After(now.Add(skew)) {
		return false
	}
	return edge.IsZero() || at.After(edge.Add(-ringWindow))
}

// ── the durable record ───────────────────────────────────────────────────────

// observationDDL is the tenant's own record of what it taught its model: the
// events it sent through the learn door, and nothing else. The warehouse already
// holds what the organisation EMITTED; this holds what it TAUGHT, which is a
// different fact with a different owner.
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
// order, which is what the learn door asks for ("oldest first"). Taught out of
// order across a window boundary the two can differ, because a live ring cannot
// re-place an event whose leading edge has already moved past it and a replay
// can. Said here rather than claimed away: the property that holds unconditionally
// is that the same record rebuilds the same aggregates.
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
		// THROUGH THE ONE DOOR, even on the way back in. A row written before a
		// bound existed, or by a build that did not have one, is refused here rather
		// than silently rebuilding aggregates the ceiling does not cover.
		o, err := observe(id, actor{Kind: kind, Subject: subject, Peer: peer, Device: device}, usd, time.Unix(at, 0).UTC())
		if err != nil {
			return vel, time.Time{}, 0, fmt.Errorf("risk: replay observations: %w", err)
		}
		held = append(held, o)
	}
	if err := rows.Err(); err != nil {
		return vel, time.Time{}, 0, fmt.Errorf("risk: replay observations: %w", err)
	}
	var edge time.Time
	for i := len(held) - 1; i >= 0; i-- {
		o := held[i]
		vel.record(o.tx(t))
		if o.at.After(edge) {
			edge = o.at
		}
	}
	vel.reconcile()
	return vel, edge, len(held), nil
}
