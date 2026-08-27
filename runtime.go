// Copyright © 2026 Hanzo AI. MIT License.

package cloud

// runtime.go — what an hour of agentic runtime costs, and the one way it is billed.
//
// ONE RATE FOR EVERY AGENTIC HOUR. A coding session, a chat session, a bot sitting
// resident, a sandbox somebody is holding — the platform sells the same thing in all
// four, an hour of an agent running, so there is one price and one meter name. The
// catalog says it in those words (@hanzo/plans seats.json: "every hour an agent runs
// bills at agentHourUSD, whether it is writing code, answering in a chat session, or
// sitting resident as a bot. One rate covers all of it"), and
// TestTheFloorIsTheCatalogsRate reads that file so this constant and the published
// number cannot drift apart in silence.
//
// A CAPACITY IS NOT AN ALLOWANCE. What a plan includes is how many agents may exist
// (subscription.json limits.agents/limits.bots); the hours they run are metered on
// top. Nothing here reads a plan, and nothing that reads a plan may hand out hours —
// a resident bot's month is ~720 of them.
//
// WHY A WATERMARK RATHER THAN A DURATION. Billing at close alone never bills the
// thing that never closes, which is exactly what a 24/7 bot is. So every row that
// can run carries the instant through which it has been billed, a tick charges the
// span since, and a close is that same charge with the row's end for its right-hand
// edge. One rule, two callers, and the long-running case is the ordinary one instead
// of the exception.

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hanzoai/cloud/apps/metering"
)

// RuntimeProduct and RuntimeMeter address this price in commerce's meter authority
// (apps/commerce/rate_rpc.go). An operator retunes the rate by editing that row at
// admin.hanzo.ai, with an audit trail; the constant below is what is charged until
// they do.
const (
	RuntimeProduct = "agents"
	RuntimeMeter   = "runtime-hour"
)

// RuntimeHourMicros is the compiled floor: $0.08 per agent-hour, in micro-USD.
//
// SOURCE OF TRUTH is the published catalog — @hanzo/plans `seats.json`, key
// `runtime.agentHourUSD` (0.08). It is a CONSTANT here rather than a read of that
// file because money is not a float: 0.08 has no exact binary form, and a price
// parsed from JSON on every debit would round differently than one written down
// once. The pin test reads the catalog and does the conversion in the test, where a
// wrong answer is a red build rather than a wrong invoice.
//
// It is the floor in [RateMicros]'s sense: a meter with no authority row, an
// authority that is unwell, and a process with no commerce beside it all charge it.
const RuntimeHourMicros int64 = 80_000

// secsPerHour converts the per-hour rate to the span actually run.
const secsPerHour int64 = 3600

// RuntimeRate is the published price of one agent-hour, in micro-USD, falling back
// to [RuntimeHourMicros].
//
// A var so a test can drive a published price with no authority beside it — the
// shape apps/provisioning's storageMonthlyCents already uses.
var RuntimeRate = func(ctx context.Context) int64 {
	return RateMicros(ctx, RuntimeProduct, RuntimeMeter, RuntimeHourMicros)
}

// RuntimeCost prices an elapsed span at a per-hour micro-USD rate, integer-exact and
// half-up: round(rate × secs / 3600). A non-positive rate or span is worth nothing,
// which is how a published price of zero makes runtime free rather than being
// rounded up to a charge an operator said not to make.
func RuntimeCost(microPerHour, secs int64) int64 {
	if microPerHour <= 0 || secs <= 0 {
		return 0
	}
	return (microPerHour*secs + secsPerHour/2) / secsPerHour
}

// Running is one row that is running now: what it is, whose books pay for it, and
// the instant through which it has already been billed.
//
// Payer is the WALLET — principal.Ledger at the act that started the row, persisted
// on it — never the row's org. The two differ in exactly one case and it is the case
// that matters: a platform SuperAdmin acting inside somebody else's org spends its
// own, so keying the recurring charge on the org would bill the tenant being
// inspected for the operator inspecting them.
type Running struct {
	ID      string // the session, agent or lease this span belongs to
	Payer   string // the wallet the debit lands in
	Project string // the org sub-scope, for the per-scope spend cap
	Model   string // what ran, as it reads on the invoice ("session", "bot", "sandbox/dev")

	// MeteredAt is the second through which this row has been billed. Zero means
	// it has never been seen, which is charged as nothing — see [RuntimeCharge].
	MeteredAt int64

	// Rate is what ONE HOUR of this row costs, in micro-USD.
	//
	// It is on the ROW because the price is a fact about what is running, not about
	// the pass: a sweep holds many kinds at once — an android sandbox against an
	// exec one is twelve times the memory — and a single rate read per tick is one
	// price for all of them, which sells most of a node for the price of a
	// sixteenth of one. A caller whose rows are all one kind sets the same value on
	// each, which costs nothing and keeps ONE source for what an hour costs.
	//
	// Zero is a PRICE and not an absence: something the platform meters and gives
	// away. The span still moves the watermark and emits nothing.
	Rate int64

	// EndedAt is this row's RIGHT-HAND edge: the second it stopped, or zero while
	// it is still running. Clamping to it is what stops a closed row accruing
	// after it closed, and it is what lets a sweep bill a close without a close
	// hook — a row whose ended_at is past its watermark still owes that tail.
	//
	// It is on the ROW because it differs per row: one sweep sees a session that
	// ended an hour ago beside a bot that has never stopped, and a single `now`
	// for both would bill the dead session for the hour it spent dead. A bot has
	// no such moment and reads zero, which is the honest value — not a sentinel.
	EndedAt int64
}

// edge is the right-hand end of the span this row still owes, at the moment a
// sweep asks. A row that stopped is billed to where it stopped; one still running
// is billed to now. A stop in the FUTURE (a clock that disagrees, a lease written
// ahead) is not an edge to bill to, so now still bounds it.
func (r Running) edge(now int64) int64 {
	if r.EndedAt > 0 && r.EndedAt < now {
		return r.EndedAt
	}
	return now
}

// RuntimeCharge bills one running row for the span since it was last billed, and
// returns the micro-USD charged.
//
// It is the whole of the exactly-once contract, and the ORDER is the contract:
//
//   - advance FIRST. It is the store's compare-and-set on the watermark, so the
//     winner owns this span and every concurrent or duplicate caller reads a
//     watermark that has already moved and charges nothing. A crash between the
//     advance and the debit drops one span; the reverse order would double-bill it,
//     because the next caller's right-hand edge is a later `now` and therefore a
//     different span. Losing a charge is recoverable, charging twice is not.
//   - the ref is DETERMINISTIC in the span, so a retry of the SAME span — the meter
//     re-sending after a lost reply, a debit replayed off the plane — is one movement
//     of money at the ledger (finance.RecordUsageOnce dedups on it) rather than two.
//     The CAS makes a second attempt rare; this makes it free.
//
// FIRST SIGHT IS NOT A DEBT. A row with no watermark — one that predates this meter,
// or has just been created — starts its clock at now and is charged nothing, so
// shipping the meter never back-charges anybody for hours that were free when they
// ran.
func RuntimeCharge(
	ctx context.Context,
	r Running,
	now int64,
	advance func(ctx context.Context, id string, was, now int64) (bool, error),
	emit func(payer string, u metering.Usage),
) (int64, error) {
	if r.ID == "" || r.Payer == "" {
		return 0, nil
	}
	if r.MeteredAt <= 0 {
		// Start the clock. Nothing is owed for the time before the meter could see it.
		_, err := advance(ctx, r.ID, r.MeteredAt, now)
		return 0, err
	}
	secs := now - r.MeteredAt
	if secs <= 0 {
		return 0, nil // already billed through now, or a clock that went backwards
	}
	won, err := advance(ctx, r.ID, r.MeteredAt, now)
	if err != nil || !won {
		return 0, err
	}
	micros := RuntimeCost(r.Rate, secs)
	if micros <= 0 {
		return 0, nil
	}
	emit(r.Payer, metering.Usage{
		AmountMicros: micros,
		Model:        r.Model,
		Project:      r.Project,
		Service:      RuntimeMeter,
		Ref:          runtimeRef(r.ID, r.MeteredAt, now),
	})
	return micros, nil
}

// RuntimeSweep is [RuntimeCharge] over every row of ONE entity's store, with the
// step the single-row rule cannot carry: the watermarks it just advanced are
// SHIPPED before a single debit is emitted. It returns how many rows were charged.
// A store read that failed is the CALLER's to report: a sweep that cannot see the
// rows must not conclude there are none.
//
// SHIP BEFORE EMIT, and it is the same argument ship-before-ack makes one plane
// over (apps/label, apps/research, apps/reference) with money in place of an HTTP
// 200. The watermark is what says a span is accounted for, and it lives in the
// entity's local file. Emitting first tells the ledger a span was billed while the
// fact that proves it was billed is not durable — so the successor pod hydrates a
// snapshot whose watermark predates the debit, bills the same span again, and the
// customer pays twice for one hour. A debit is not idempotent on content the way a
// compliance assertion is; there is nothing to converge on. So the ledger is told
// last, and only about spans whose watermarks are already on the durable object.
//
// ONE SHIP PER PASS, NOT PER ROW. The ship is a whole-file copy to an object
// store, and an entity with two hundred leases would otherwise send its database
// two hundred times to bill one minute. Every winning CAS is claimed first, the
// file goes once, and the debits follow together.
//
// A SHIP THAT IS NOT ACKED EMITS NOTHING, and it does NOT roll the watermarks
// back. Rolling back is the tempting repair and it is the one move that can
// double-bill: an unacked ship is not a failed ship — the object may have landed
// and only the answer been lost — so a replica that undoes its watermark on a
// timeout and re-bills the span is charging for it twice. Left standing, the
// advance is simply not durable, which is the honest state: the durable watermark
// has not moved, so whichever replica next opens this store from the object — a
// failover, a promotion, this pod's own restart — bills the span exactly once. The
// cost of a store outage is therefore a DEFERRED debit, never a doubled one and
// never one this process claimed was durable when it was not.
func RuntimeSweep(
	ctx context.Context,
	rows []Running,
	now int64,
	advance func(ctx context.Context, id string, was, now int64) (bool, error),
	ship func() (bool, error),
	emit func(payer string, u metering.Usage),
) (int, error) {
	var held []debit
	keep := func(payer string, u metering.Usage) { held = append(held, debit{payer, u}) }
	for _, r := range rows {
		if _, err := RuntimeCharge(ctx, r, r.edge(now), advance, keep); err != nil {
			return 0, err
		}
	}
	if len(held) == 0 {
		// Nothing was claimed, so nothing was written that a ship would carry. A
		// pass over rows that all read as already-billed owes no round trip.
		return 0, nil
	}
	acked, err := ship()
	if err != nil {
		return 0, err
	}
	if !acked {
		return 0, fmt.Errorf("the watermarks moved and could not be shipped, so %d debits are held rather than emitted; the durable watermark has not moved and the next owner bills these spans", len(held))
	}
	for _, d := range held {
		emit(d.payer, d.usage)
	}
	return len(held), nil
}

// debit is one charge waiting on the ship that makes it real.
type debit struct {
	payer string
	usage metering.Usage
}

// runtimeRef names one span so the ledger can recognise it again: the row, and the
// two edges of the window. It is the act's identity (metering.Usage.Ref), which is
// why it is derived from server-held values only — a caller cannot reach any of the
// three and so cannot pin a debit onto an earlier one.
func runtimeRef(id string, from, to int64) string {
	return "rt_" + id + "_" + strconv.FormatInt(from, 10) + "_" + strconv.FormatInt(to, 10)
}
