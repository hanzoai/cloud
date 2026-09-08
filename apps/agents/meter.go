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

	"github.com/hanzoai/account"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/metering"
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
func payerOf(ctx context.Context, org string) account.Account {
	if w := cloud.PayerOf(ctx).Wallet; !w.Zero() {
		return w
	}
	return account.PayerOf("", org)
}

// sessionRunning describes one session to the runtime meter.
//
// The payer falls back to the ORG for a session opened before this meter existed:
// those rows name no wallet, and billing an org's own pool is right where the org
// is the payer and merely coarse where it is not — which beats not billing.
func sessionRunning(x Session, rate int64) cloud.Running {
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
		EndedAt:   x.EndedAt,
		Rate:      rate,
	}
}

// botRunning describes one resident bot. Model says "bot" rather than "session"
// so an invoice distinguishes the two things this rate covers.
func botRunning(a Agent, rate int64) cloud.Running {
	payer := a.Payer
	if payer == "" {
		payer = a.Org
	}
	return cloud.Running{
		ID:        a.ID,
		Payer:     payer,
		Model:     "bot",
		MeteredAt: a.MeteredAt,
		Rate:      rate,
	}
}

// A session's right-hand edge — the moment it ended, or now while it is still
// open — used to be computed here. It is [cloud.Running.EndedAt] now, because the
// sweep needs it per row to bill a batch in one pass, and one rule for "where does
// this span end" beats one per subsystem.

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
// posture Record takes — because a debit that could not be recorded must not
// turn a working mode change into a refusal.
func closeResidency(ctx context.Context, s *cloud.Service[state], sto *Store, org string, a Agent) {
	ns, err := cloud.OrgNamespace(org, "")
	if err != nil {
		s.Log.Warn("runtime meter: a bot left residency unbilled", "org", org, "agent", a.Name, "err", err)
		return
	}
	// The SAME sweep the tick runs, over one row: the tail is claimed, the
	// watermark is shipped, and only then is the debit emitted. A mode change is
	// a write by a request, and a request only reaches this store on the replica
	// that owns it — so there is no owner gate here, and the ship is still what
	// decides whether the charge is real.
	_, err = cloud.RuntimeSweep(ctx, []cloud.Running{botRunning(a, cloud.RuntimeRate(ctx))}, time.Now().Unix(),
		func(ctx context.Context, id string, was, at int64) (bool, error) {
			return sto.Advance(ctx, org, a.Name, was, at)
		},
		func() (bool, error) { return s.State.stores.Sync(ns) },
		// The sweep carries the session's STORED payer key, so it is parsed into an
		// address here by the one rule rather than the store learning a new type.
		func(payer string, u metering.Usage) { s.Bill.Record(account.PayerOf("", payer), meterKind, u) })
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
//
// IT METERS THE ORGS THIS REPLICA OWNS AND NO OTHERS. Every pod holds every org's
// file that its volume carries, so an ungated sweep is every pod billing every
// org: two pods, one span, two debits, and the local watermark CAS cannot stop it
// because the two pods are CASing two different files. Ownership is the fence's
// answer (stores.Owned) and the ship is what settles it.
//
// It takes `now` and `emit` rather than reading a clock and holding a
// Meter, so a test drives THIS function over a real store instead of
// reassembling it — which is the shape a reassembled harness gets wrong: the
// owner gate and the ship are what this pass IS, and a copy of the loop that
// predates them proves the arithmetic of a meter nobody runs.
func meterRuntime(ctx context.Context, st *state, log logger, now int64, emit func(payer string, u metering.Usage)) {
	// A PUBLISHED ZERO IS A PRICE, AND A PRICE STILL MOVES THE CLOCK. Runtime is
	// free this week, so nothing is charged — but the span still HAPPENED, and the
	// watermark is what says it is accounted for. Returning here instead left the
	// watermark where it was, so restoring the price on Monday billed every free
	// hour of the promotion RETROACTIVELY at the new rate, in one debit, to every
	// tenant. cloud.RuntimeCharge already does the right thing with a zero: it
	// advances first and emits only when the span is worth something, so the whole
	// fix is to let it be asked.
	rate := cloud.RuntimeRate(ctx)
	_ = st.eachStore(func(ns namespace.Namespace, sto *Store, openErr error) {
		if ctx.Err() != nil {
			return
		}
		if openErr != nil {
			log.Warn("runtime meter: open store", "namespace", ns, "err", openErr)
			return
		}
		org := ns.ID()
		if !st.stores.Owned(ns) {
			return // another replica bills this org, or nobody can prove who does
		}
		ship := func() (bool, error) { return st.stores.Sync(ns) }

		// SESSIONS AND BOTS ARE TWO SPANS ON ONE FILE, so they are two passes and
		// two ships. Folding them into one would mean an unreadable bot list held
		// back the sessions that were already claimed, which is a debit deferred
		// for a reason that has nothing to do with it.
		owed, err := sto.Unbilled(ctx, org)
		if err != nil {
			log.Warn("runtime meter: list unbilled sessions", "org", org, "err", err)
		}
		sessions := make([]cloud.Running, 0, len(owed))
		for _, x := range owed {
			sessions = append(sessions, sessionRunning(x, rate))
		}
		advanceSession := func(ctx context.Context, id string, was, at int64) (bool, error) {
			return sto.AdvanceSession(ctx, org, id, was, at)
		}
		if _, err := cloud.RuntimeSweep(ctx, sessions, now, advanceSession, ship, emit); err != nil {
			log.Warn("runtime meter: session spans not billed", "org", org, "err", err)
		}

		bots, err := sto.Resident(ctx, org)
		if err != nil {
			log.Warn("runtime meter: list resident bots", "org", org, "err", err)
			return
		}
		// A BOT IS ADDRESSED BY TWO DIFFERENT NAMES AND BOTH ARE RIGHT. The LEDGER
		// keys a debit on the agent's id, because that is the row's identity and
		// what a ref has to survive a rename with; the STORE keys the watermark on
		// the agent's NAME, because that is its primary key. A per-row closure hid
		// the difference by capturing the agent and ignoring the id it was handed —
		// which is fine until the pass is batched and the closure has to serve every
		// row, at which point advancing by id silently moves nothing and every
		// resident bot runs free. So the two names are carried together, explicitly.
		residents := make([]cloud.Running, 0, len(bots))
		named := make(map[string]string, len(bots))
		for _, a := range bots {
			residents = append(residents, botRunning(a, rate))
			named[a.ID] = a.Name
		}
		advanceBot := func(ctx context.Context, id string, was, at int64) (bool, error) {
			ok, err := sto.Advance(ctx, org, named[id], was, at)
			if ok && err == nil && rate > 0 && at > was {
				// The same span the org is billed for, on the bot's own tally.
				if cerr := sto.Consume(ctx, org, named[id], "", "", cloud.ComponentComputer, (at-was)*rate/3600); cerr != nil {
					log.Warn("runtime meter: bot spend not recorded", "org", org, "agent", named[id], "err", cerr)
				}
			}
			return ok, err
		}
		if _, err := cloud.RuntimeSweep(ctx, residents, now, advanceBot, ship, emit); err != nil {
			log.Warn("runtime meter: bot spans not billed", "org", org, "err", err)
		}
	})
}
