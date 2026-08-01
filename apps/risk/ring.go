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
	"fmt"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/types"
	"github.com/luxfi/aml/pkg/velocity"
	"github.com/zap-proto/zip"
)

// perKeyBytes is the measured cost of ONE velocity key across the standard
// window set: 60+96+168+120 = 444 ring slots at 48 bytes each, plus ring and map
// overhead. It is written down because both bounds below are DERIVED from a
// memory budget rather than picked, and a derivation nobody can check is a guess.
const perKeyBytes = 444*48 + 512

// residentRingBudget is what ONE tenant's aggregates may cost. It is the
// TENANT-FACING bound, and it is the whole point: a tenant that needs more
// subjects than this evicts its own least-recently-active subject and degrades
// only itself.
const residentRingBudget = 8 << 20

// residentKeys is that budget in subjects. velocity applies the bound per shard
// (MaxKeys/64+1), so the effective capacity is a little under this figure and a
// tenant near its bound starts evicting its own oldest subjects slightly early —
// stated because a bound that is approximate and undocumented is a bound nobody
// can reason about.
const residentKeys = residentRingBudget / perKeyBytes

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

// ringRows bounds how much of a tenant's record is replayed when its rings are
// rebuilt. Bounded because a rebuild happens on a request path (first touch after
// a deploy or an eviction) and an unbounded one is a tenant's own volume turned
// into its own latency.
const ringRows = 100_000

// newRings mints ONE tenant's aggregates. It is the ONLY constructor in this
// package: there is no exported one, no package-level store, and no call to
// velocity.New anywhere else on the live path, so a process-wide ring set is not
// a thing this package can be made to build.
func newRings() *velocity.Store {
	return velocity.New(velocity.Config{MaxKeys: residentKeys})
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
// ONE definition, used at every door: the learn wire ([mlEvent.observation]) and
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
// and cannot get any other way.
func (p *plane) note(t tenant, obs []observation) error {
	if len(obs) == 0 {
		return nil
	}
	sh, err := p.for_(t)
	if err != nil {
		return err
	}
	tx, err := sh.db.Begin()
	if err != nil {
		return fmt.Errorf("risk: record observations: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	ins, err := tx.Prepare(`INSERT INTO observation (tenant, id, kind, subject, peer, device, usd, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(tenant, id) DO NOTHING`)
	if err != nil {
		return fmt.Errorf("risk: record observations: %w", err)
	}
	defer func() { _ = ins.Close() }()
	for _, o := range obs {
		if _, err := ins.Exec(string(t), o.ID, o.Kind, o.Subject, o.Peer, o.Device, o.USD, o.At.UTC().Unix()); err != nil {
			return fmt.Errorf("risk: record observation %q: %w", o.ID, err)
		}
	}
	// TWO RETENTION BOUNDS, BOTH PER TENANT, both in this transaction. Pruning
	// here rather than on a sweep keeps them unconditional — there is no schedule
	// to miss.
	//
	// By AGE, because a record the aggregates could never hold again is a record
	// kept for nothing.
	if _, err := tx.Exec(`DELETE FROM observation WHERE tenant = ? AND at < ?`,
		string(t), p.now().UTC().Add(-ringWindow).Unix()); err != nil {
		return fmt.Errorf("risk: prune observations: %w", err)
	}
	// By COUNT, because age alone bounds nothing a caller controls. The shelves of
	// every tenant share one volume, so an unbounded per-tenant record is a tenant
	// filling a disk that other tenants' models are stored on — the same
	// cross-tenant failure as a shared cap, wearing a filesystem. [ringRows] is
	// already the most a rebuild will ever replay, so rows beyond it are rows kept
	// for nothing too, and the bound is the one the replay states rather than a
	// second number.
	//
	// The cut is on (at, id) as a PAIR and not on `at` alone. Timestamps are stored
	// at one-second resolution, so a caller sending a thousand events inside one
	// second gives every one of them the same `at` — and a cut at `at < boundary`
	// then deletes none of them, which is an unbounded record reached by ordinary
	// batching. The pair is the record's own order, so the cut is exact.
	//
	// The boundary row is the ringRows-th newest and it SURVIVES — everything
	// strictly older goes — so exactly ringRows remain. The subselect walks the
	// tenant's own covering index and stops there; below the bound it yields NULL
	// and the comparison deletes nothing.
	if _, err := tx.Exec(`DELETE FROM observation WHERE tenant = ? AND (at, id) < (
			SELECT at, id FROM observation WHERE tenant = ? ORDER BY at DESC, id DESC LIMIT 1 OFFSET ?)`,
		string(t), string(t), ringRows-1); err != nil {
		return fmt.Errorf("risk: prune observations: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("risk: record observations: %w", err)
	}
	return nil
}

// rings rebuilds one tenant's aggregates from its own durable record, and returns
// the newest timestamp it carries.
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
func (p *plane) rings(t tenant) (*velocity.Store, time.Time, int, error) {
	vel := newRings()
	sh, err := p.for_(t)
	if err != nil {
		return vel, time.Time{}, 0, err
	}
	// Newest first with a bound, then reversed: the bound has to select the RECENT
	// end of the record, and the replay has to run oldest first.
	rows, err := sh.db.Query(
		`SELECT kind, subject, peer, device, usd, at FROM observation
		 WHERE tenant = ? AND at >= ? ORDER BY at DESC, id DESC LIMIT ?`,
		string(t), p.now().UTC().Add(-ringWindow).Unix(), ringRows)
	if err != nil {
		return vel, time.Time{}, 0, fmt.Errorf("risk: replay observations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	held := make([]observation, 0, 256)
	for rows.Next() {
		var o observation
		var at int64
		if err := rows.Scan(&o.Kind, &o.Subject, &o.Peer, &o.Device, &o.USD, &at); err != nil {
			return vel, time.Time{}, 0, fmt.Errorf("risk: replay observations: %w", err)
		}
		o.At = time.Unix(at, 0).UTC()
		held = append(held, o)
	}
	if err := rows.Err(); err != nil {
		return vel, time.Time{}, 0, fmt.Errorf("risk: replay observations: %w", err)
	}
	var edge time.Time
	for i := len(held) - 1; i >= 0; i-- {
		o := held[i]
		record(vel, o.tx(t))
		if o.At.After(edge) {
			edge = o.At
		}
	}
	return vel, edge, len(held), nil
}

// record puts one transaction into a ring set on every axis it names. It is the
// one place velocity.Record is called with the reporting threshold, so the live
// path, the rebuild and the search sandbox cannot disagree about what a key is or
// what "just under" means.
func record(vel *velocity.Store, tx types.Transaction) {
	for _, k := range anomaly.Keys(tx) {
		vel.Record(k, tx.Timestamp, tx.USD, reportingThreshold)
	}
}
