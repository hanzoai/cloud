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

// meterEvery is how often runtime money moves. The charge is (now − watermark),
// so this decides FRESHNESS and never the amount: a longer interval bills the same
// total in fewer, larger debits. Fifteen minutes keeps a resident bot's ledger
// within a quarter-hour of the truth without writing a row per minute per bot.
const meterEvery = 15 * time.Minute

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

// meterRuntime bills every org's open sessions and resident bots for the span
// since the last tick. The ticker fires it; Shutdown's context ends it.
//
// A store that could not be opened or read is REPORTED and skipped, never read as
// "this org has nothing running" — the same rule the sandbox orphan sweep states,
// for the same reason: silence is not an answer.
func meterRuntime(ctx context.Context, st *state, log interface {
	Warn(string, ...any)
}, bill *cloud.ResourceMeter) {
	rate := cloud.RuntimeRate(ctx)
	if rate <= 0 {
		return // a published zero is a price: runtime is free, so nothing is charged.
	}
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

// startMeter runs the runtime meter on a ticker until the returned stop is
// called. It is started by Mount and stopped by Shutdown, ahead of CloseAll.
//
// The FIRST tick is one interval away, deliberately: Mount is on the request path
// of a lazily-started plugin, and a sweep of every org's store is not something a
// caller's first request should wait behind.
func startMeter(s *cloud.Service[state]) func() {
	ctx, stop := context.WithCancel(context.Background())
	go func() {
		t := time.NewTicker(meterEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				meterRuntime(ctx, &s.State, s.Log, s.Bill)
			}
		}
	}()
	return stop
}
