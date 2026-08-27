package agents

// meter.go — agent RUNTIME, billed by the hour at the platform's one agentic rate.
//
// TWO ACTS, TWO CHARGES. The per-run fee (agents.go, agentFeeEnvPrefix) is what
// invoking an agent costs, once. RUNTIME is what an agent being ALIVE costs, per
// hour, and it is the same rate a held sandbox pays — because the catalog prices
// them as one thing: "every hour an agent runs bills at agentHourUSD, whether it
// is writing code, answering in a chat session, or sitting resident as a bot. One
// rate covers all of it" (@hanzo/plans seats.json, the source
// [cloud.RuntimeHourMicros] is pinned to).
//
// TWO THINGS ARE ALIVE HERE, and they are different rows because they are
// different facts:
//
//	a SESSION is alive from the moment it opens until its ended_at is written.
//	a BOT is alive for as long as its agent row says long-running — there is no
//	  session to read, because a resident bot HAS none: the scheduler invokes it
//	  and each invocation opens a session that is born and dies in one call. Its
//	  residency is the mode, so the mode is what accrues.
//
// WHY A SWEEP AND NOT A CLOSE HOOK. A bot never closes, which is the whole point
// of one; a session has FOUR writers of ended_at and a fifth would have to
// remember. Both are answered by one periodic pass over rows that still owe time,
// so a close needs no code and a thing that never closes still accrues.

import (
	"context"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/namespace"
)

// The cadence this pass runs on is not its own: it shares one loop with the reaper
// (reap.go startSweep), because a session's runtime and a session's END are two
// readings of the same rows and a second timer would let them disagree about when
// one of them stopped.

// payerOf is the wallet a runtime charge lands in.
//
// cloud.PayerOf is the ONE payer resolver and it answers from a request or from a
// stated caller alike. Where it answers nothing — a scheduler tick, a migration —
// the org's own pool pays, which is the honest answer for an act with no person
// behind it and is what account.Payer resolves a bare org slug to anyway.
func payerOf(ctx context.Context, org string) string {
	if w := cloud.PayerOf(ctx).Wallet; w != "" {
		return w
	}
	return org
}

// sessionRunning describes one session to the runtime meter.
//
// The payer falls back to the ORG for a session opened before this meter existed:
// those rows name no wallet, and billing an org's own pool is right where the org
// is the payer and merely coarse where it is not — which beats not billing.
func sessionRunning(x Session) cloud.Running {
	payer := x.Payer
	if payer == "" {
		payer = x.Org
	}
	return cloud.Running{
		ID:        x.ID,
		Payer:     payer,
		Project:   x.Project,
		Model:     "session",
		MeteredAt: x.MeteredAt,
	}
}

// botRunning describes one resident bot. Model says "bot" rather than "session"
// so an invoice distinguishes the two things this rate covers.
func botRunning(a Agent) cloud.Running {
	payer := a.Payer
	if payer == "" {
		payer = a.Org
	}
	return cloud.Running{
		ID:        a.ID,
		Payer:     payer,
		Model:     "bot",
		MeteredAt: a.MeteredAt,
	}
}

// endOf is a session's right-hand edge: the moment it ended, or now while it is
// still open. Clamping to ended_at is what stops a closed session accruing
// forever, and it is why the sweep can bill a close without a close hook.
func endOf(x Session, now int64) int64 {
	if x.EndedAt > 0 && x.EndedAt < now {
		return x.EndedAt
	}
	return now
}

// closeResidency charges a bot's unbilled tail at the moment it stops being one.
//
// LEAVING THE SET IS A CLOSE. The sweep bills what Resident can SEE, so a row that
// leaves that set takes its final span with it unless somebody charges it on the
// way out — the rule apps/sandbox already states for a lease whose row is deleted,
// applied to a row that merely stops matching. Two exits reach it: a PATCH to
// one-shot, and a delete.
//
// The mode flap is why it is not merely untidy. Stamp resets the watermark to now
// on the transition IN, so `executionMode: one-shot` followed by
// `executionMode: long-running` — two ordinary tenant calls, no privilege — would
// otherwise discard the tail each time and make a resident bot cost approximately
// nothing however long it ran.
//
// CHARGED BEFORE THE WRITE that removes the row from the set, for the reason
// apps/sandbox charges before its delete: crash after the charge and the row is
// still resident with an advanced watermark, which bills nothing twice; crash after
// the write and the span is gone. A failure is logged and dropped — the same
// posture MeterUsage takes — because a debit that could not be recorded must not
// turn a working mode change into a refusal.
func closeResidency(ctx context.Context, s *cloud.Service[state], sto *Store, org string, a Agent) {
	_, err := cloud.RuntimeCharge(ctx, botRunning(a), time.Now().Unix(), cloud.RuntimeRate(ctx),
		func(ctx context.Context, id string, was, at int64) (bool, error) {
			return sto.Advance(ctx, org, a.Name, was, at)
		},
		func(payer string, u metering.Usage) { s.Bill.MeterUsage(payer, meterKind, u) })
	if err != nil {
		s.Log.Warn("runtime meter: a bot left residency unbilled", "org", org, "agent", a.Name, "err", err)
	}
}

// meterRuntime bills every org's open sessions and resident bots for the span
// since the last tick. The ticker fires it; Shutdown's context ends it.
//
// A store that could not be opened or read is REPORTED and skipped, never read as
// "this org has nothing running" — the same rule the sandbox orphan sweep states,
// for the same reason: silence is not an answer.
func meterRuntime(ctx context.Context, st *state, log logger, bill *cloud.ResourceMeter) {
	// A PUBLISHED ZERO IS A PRICE, AND A PRICE STILL MOVES THE CLOCK. Runtime is
	// free this week, so nothing is charged — but the span still HAPPENED, and the
	// watermark is what says it is accounted for. Returning here instead left the
	// watermark where it was, so restoring the price on Monday billed every free
	// hour of the promotion RETROACTIVELY at the new rate, in one debit, to every
	// tenant. cloud.RuntimeCharge already does the right thing with a zero: it
	// advances first and emits only when the span is worth something, so the whole
	// fix is to let it be asked.
	rate := cloud.RuntimeRate(ctx)
	now := time.Now().Unix()
	emit := func(payer string, u metering.Usage) { bill.MeterUsage(payer, meterKind, u) }

	_ = st.eachStore(func(ns namespace.Namespace, sto *Store, openErr error) {
		if ctx.Err() != nil {
			return
		}
		if openErr != nil {
			log.Warn("runtime meter: open store", "namespace", ns, "err", openErr)
			return
		}
		org := ns.ID()

		owed, err := sto.Unbilled(ctx, org)
		if err != nil {
			log.Warn("runtime meter: list unbilled sessions", "org", org, "err", err)
		}
		for _, x := range owed {
			advance := func(ctx context.Context, id string, was, at int64) (bool, error) {
				return sto.AdvanceSession(ctx, org, id, was, at)
			}
			if _, err := cloud.RuntimeCharge(ctx, sessionRunning(x), endOf(x, now), rate, advance, emit); err != nil {
				log.Warn("runtime meter: session watermark would not move", "session", x.ID, "org", org, "err", err)
			}
		}

		bots, err := sto.Resident(ctx, org)
		if err != nil {
			log.Warn("runtime meter: list resident bots", "org", org, "err", err)
			return
		}
		for _, a := range bots {
			advance := func(ctx context.Context, id string, was, at int64) (bool, error) {
				// Agents are addressed by NAME in their own store; the id rides the
				// act's name so the ledger's key is still the row's own identity.
				return sto.Advance(ctx, org, a.Name, was, at)
			}
			if _, err := cloud.RuntimeCharge(ctx, botRunning(a), now, rate, advance, emit); err != nil {
				log.Warn("runtime meter: bot watermark would not move", "agent", a.Name, "org", org, "err", err)
			}
		}
	})
}
