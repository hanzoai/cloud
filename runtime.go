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
	now, rate int64,
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
	micros := RuntimeCost(rate, secs)
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

// RuntimeSweep is [RuntimeCharge] over every row that is running, and returns how
// many were charged. A store read that failed is the CALLER's to report: a sweep
// that cannot see the rows must not conclude there are none.
func RuntimeSweep(
	ctx context.Context,
	rows []Running,
	now, rate int64,
	advance func(ctx context.Context, id string, was, now int64) (bool, error),
	emit func(payer string, u metering.Usage),
) (int, error) {
	billed := 0
	for _, r := range rows {
		micros, err := RuntimeCharge(ctx, r, now, rate, advance, emit)
		if err != nil {
			return billed, err
		}
		if micros > 0 {
			billed++
		}
	}
	return billed, nil
}

// runtimeRef names one span so the ledger can recognise it again: the row, and the
// two edges of the window. It is the act's identity (metering.Usage.Ref), which is
// why it is derived from server-held values only — a caller cannot reach any of the
// three and so cannot pin a debit onto an earlier one.
func runtimeRef(id string, from, to int64) string {
	return "rt_" + id + "_" + strconv.FormatInt(from, 10) + "_" + strconv.FormatInt(to, 10)
}
