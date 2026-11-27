// Copyright © 2026 Hanzo AI. MIT License.

package datastore

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// analyticalLedgerSim models ClickHouse's MergeTree + SummingMergeTree(fact_commerce_ledger)
// and ReplacingMergeTree(version) (state_org_balances, state_subscriptions) semantics in memory
// so financial correctness invariants can be hammered deterministically under -race.
type analyticalLedgerSim struct {
	mu       sync.Mutex
	deduper  *ledgerDeduper
	facts    []CommerceLedgerFact
	balances map[string]OrgBalanceState   // key: org + ":" + currency
	subs     map[string]SubscriptionState // key: org + ":" + id
}

func newAnalyticalLedgerSim() *analyticalLedgerSim {
	return &analyticalLedgerSim{
		deduper:  newLedgerDeduper(10_000),
		balances: make(map[string]OrgBalanceState),
		subs:     make(map[string]SubscriptionState),
	}
}

func (s *analyticalLedgerSim) ingestFact(f CommerceLedgerFact) error {
	if err := validateCommerceLedgerFact(f); err != nil {
		return err
	}
	unique := s.deduper.acquire([]CommerceLedgerFact{f})
	if len(unique) == 0 {
		return nil // idempotent replay: zero duplicate effect
	}
	s.mu.Lock()
	s.facts = append(s.facts, unique...)
	s.mu.Unlock()
	s.deduper.commit(unique)
	return nil
}

// upsertBalance applies ReplacingMergeTree(version) semantics: a row only replaces the
// stored state if its Version >= existing Version (out-of-order lower versions are ignored).
func (s *analyticalLedgerSim) upsertBalance(st OrgBalanceState) {
	cur := st.Currency
	if cur == "" {
		cur = "usd"
		st.Currency = cur
	}
	key := st.Org + ":" + cur
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.balances[key]; ok && st.Version < prev.Version {
		return // out-of-order stale version rejected by ReplacingMergeTree(version)
	}
	s.balances[key] = st
}

// upsertSub applies ReplacingMergeTree(version) semantics for subscriptions.
func (s *analyticalLedgerSim) upsertSub(st SubscriptionState) {
	key := st.Org + ":" + st.ID
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.subs[key]; ok && st.Version < prev.Version {
		return
	}
	s.subs[key] = st
}

// netFlowByOrgCurrency simulates `analytics.rollup_commerce_daily_mv` SummingMergeTree
// signed accumulation: sum(toInt64(sign) * cash_minor) and sum(toInt64(sign) * credit_minor).
func (s *analyticalLedgerSim) netFlowByOrgCurrency() map[string]OrgBalanceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]OrgBalanceState)
	for _, f := range s.facts {
		cur := f.Currency
		if cur == "" {
			cur = "usd"
		}
		key := f.Org + ":" + cur
		acc := out[key]
		acc.Org = f.Org
		acc.Currency = cur
		acc.CashMinor += int64(f.Sign) * f.CashMinor
		acc.CreditMinor += int64(f.Sign) * f.CreditMinor
		out[key] = acc
	}
	return out
}

func (s *analyticalLedgerSim) activeMRRByCurrency() map[string]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int64)
	for _, sub := range s.subs {
		if sub.Deleted || sub.Test || sub.Status != "active" {
			continue
		}
		cur := sub.Currency
		if cur == "" {
			cur = "usd"
		}
		out[cur] += sub.MRRMinor
	}
	return out
}

func (s *analyticalLedgerSim) snapshotBalances() map[string]OrgBalanceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]OrgBalanceState, len(s.balances))
	for k, v := range s.balances {
		out[k] = v
	}
	return out
}

func TestFinancialCorrectness_RefundsReplaysAndExactReconciliation(t *testing.T) {
	sim := newAnalyticalLedgerSim()
	now := time.Date(2026, 11, 25, 4, 0, 0, 0, time.UTC)

	events := []CommerceLedgerFact{
		// Deposit $100.00 cash
		{EntryID: "dep:acme:1", Org: "acme", Product: "commerce", Kind: "deposit", Sign: 1, AmountMinor: 10000, CashMinor: 10000, Currency: "usd", EventTS: now},
		// Grant $25.00 credit
		{EntryID: "grant:acme:1", Org: "acme", Product: "commerce", Kind: "grant", Sign: 1, AmountMinor: 2500, CreditMinor: 2500, Currency: "usd", EventTS: now},
		// Usage debit: $15.50 cash + $5.00 credit (reduces balance via Sign=-1 flow adjustment)
		{EntryID: "use:acme:1", Org: "acme", Product: "ai", Kind: "usage", Sign: -1, AmountMinor: 2050, CashMinor: 1550, CreditMinor: 500, Currency: "usd", EventTS: now},
		// Refund $10.00 cash back to processor (Sign=-1)
		{EntryID: "ref:acme:1", Org: "acme", Product: "billing", Kind: "refund", Sign: -1, AmountMinor: 1000, CashMinor: 1000, Currency: "usd", EventTS: now},
		// Adjustment restore +$2.50 cash (Sign=+1)
		{EntryID: "adj:acme:1", Org: "acme", Product: "commerce", Kind: "adjustment", Sign: 1, AmountMinor: 250, CashMinor: 250, Currency: "usd", EventTS: now},
	}

	// Deliver every event 3 times to simulate duplicate delivery / retries.
	for attempt := range 3 {
		for _, ev := range events {
			if err := sim.ingestFact(ev); err != nil {
				t.Fatalf("attempt %d ingestFact(%s): %v", attempt, ev.EntryID, err)
			}
		}
	}

	// Authoritative expected balance:
	// Cash:   10000 - 1550 - 1000 + 250 = 7700 minor ($77.00)
	// Credit: 2500 - 500 = 2000 minor ($20.00)
	authoritative := map[string]OrgBalanceState{
		"acme:usd": {Org: "acme", Currency: "usd", Books: true, CashMinor: 7700, CreditMinor: 2000},
	}
	analytical := sim.netFlowByOrgCurrency()
	rep := ReconcileOrgBalances(authoritative, analytical)
	if !rep.Exact || rep.TotalCashDriftMinor != 0 || rep.TotalCreditDriftMinor != 0 {
		t.Fatalf("expected zero drift after signed refunds & 3x replay, got %+v", rep)
	}
}

func TestFinancialCorrectness_MultiCurrencyIsolation(t *testing.T) {
	sim := newAnalyticalLedgerSim()
	v1 := NextAnalyticsVersion()
	sim.upsertBalance(OrgBalanceState{Org: "globalco", Currency: "usd", Books: true, CashMinor: 50000, CreditMinor: 1000, Version: v1})
	sim.upsertBalance(OrgBalanceState{Org: "globalco", Currency: "eur", Books: true, CashMinor: 30000, CreditMinor: 500, Version: v1})
	sim.upsertBalance(OrgBalanceState{Org: "globalco", Currency: "gbp", Books: true, CashMinor: 20000, CreditMinor: 0, Version: v1})

	// Exact match across all 3 currencies.
	authExact := map[string]OrgBalanceState{
		"globalco:usd": {Org: "globalco", Currency: "usd", CashMinor: 50000, CreditMinor: 1000},
		"globalco:eur": {Org: "globalco", Currency: "eur", CashMinor: 30000, CreditMinor: 500},
		"globalco:gbp": {Org: "globalco", Currency: "gbp", CashMinor: 20000, CreditMinor: 0},
	}
	if rep := ReconcileOrgBalances(authExact, sim.snapshotBalances()); !rep.Exact {
		t.Fatalf("expected exact multi-currency match, got %+v", rep)
	}

	// 1-cent drift in EUR must be caught independently without touching USD or GBP.
	authEurDrift := map[string]OrgBalanceState{
		"globalco:usd": {Org: "globalco", Currency: "usd", CashMinor: 50000, CreditMinor: 1000},
		"globalco:eur": {Org: "globalco", Currency: "eur", CashMinor: 29999, CreditMinor: 500},
		"globalco:gbp": {Org: "globalco", Currency: "gbp", CashMinor: 20000, CreditMinor: 0},
	}
	rep := ReconcileOrgBalances(authEurDrift, sim.snapshotBalances())
	if rep.Exact || rep.MismatchOrgs != 1 || rep.TotalCashDriftMinor != 1 || rep.Mismatches[0].Currency != "eur" {
		t.Fatalf("expected 1-cent EUR mismatch, got %+v", rep)
	}
}

func TestFinancialCorrectness_OutOfOrderDeliveryAndVersionGuard(t *testing.T) {
	sim := newAnalyticalLedgerSim()
	vOld := NextAnalyticsVersion()
	vMid := NextAnalyticsVersion()
	vNew := NextAnalyticsVersion()

	// Deliver newest first, then stale out-of-order updates.
	sim.upsertBalance(OrgBalanceState{Org: "acme", Currency: "usd", Books: true, CashMinor: 9500, CreditMinor: 400, Version: vNew})
	sim.upsertBalance(OrgBalanceState{Org: "acme", Currency: "usd", Books: true, CashMinor: 10000, CreditMinor: 500, Version: vOld})
	sim.upsertBalance(OrgBalanceState{Org: "acme", Currency: "usd", Books: true, CashMinor: 9800, CreditMinor: 450, Version: vMid})

	auth := map[string]OrgBalanceState{
		"acme:usd": {Org: "acme", Currency: "usd", CashMinor: 9500, CreditMinor: 400},
	}
	rep := ReconcileOrgBalances(auth, sim.snapshotBalances())
	if !rep.Exact || rep.TotalCashDriftMinor != 0 || rep.TotalCreditDriftMinor != 0 {
		t.Fatalf("stale out-of-order version clobbered newer balance: %+v", rep)
	}
}

func TestFinancialCorrectness_SubscriptionUpgradesAndOutOfOrder(t *testing.T) {
	sim := newAnalyticalLedgerSim()
	v1 := NextAnalyticsVersion()
	v2 := NextAnalyticsVersion()
	v3 := NextAnalyticsVersion()

	// Pro ($20/mo) -> upgraded to Max ($200/mo) -> out-of-order stale Pro event arrives late.
	sim.upsertSub(SubscriptionState{Org: "acme", ID: "sub_1", PlanID: "pro", Status: "active", MRRMinor: 2000, Currency: "usd", Version: v1})
	sim.upsertSub(SubscriptionState{Org: "acme", ID: "sub_1", PlanID: "max", Status: "active", MRRMinor: 20000, Currency: "usd", Version: v2})
	sim.upsertSub(SubscriptionState{Org: "acme", ID: "sub_1", PlanID: "pro", Status: "active", MRRMinor: 2000, Currency: "usd", Version: v1})

	// Second subscription in EUR upgraded then canceled; test subscription must be excluded.
	sim.upsertSub(SubscriptionState{Org: "beta", ID: "sub_2", PlanID: "team", Status: "active", MRRMinor: 5000, Currency: "eur", Version: v1})
	sim.upsertSub(SubscriptionState{Org: "beta", ID: "sub_2", PlanID: "team", Status: "canceled", MRRMinor: 5000, Currency: "eur", Version: v3})
	sim.upsertSub(SubscriptionState{Org: "sandbox", ID: "sub_test", PlanID: "max", Status: "active", MRRMinor: 99900, Currency: "usd", Test: true, Version: v2})

	mrr := sim.activeMRRByCurrency()
	if mrr["usd"] != 20000 {
		t.Fatalf("usd MRR = %d, want 20000 (upgraded max, excluding test sub)", mrr["usd"])
	}
	if mrr["eur"] != 0 {
		t.Fatalf("eur MRR = %d, want 0 (canceled sub excluded)", mrr["eur"])
	}
}

func TestFinancialCorrectness_ConcurrentMutationsAndReplays(t *testing.T) {
	sim := newAnalyticalLedgerSim()
	const (
		numOrgs    = 10
		txPerOrg   = 25
		replayCopy = 2
	)

	var wg sync.WaitGroup
	for o := range numOrgs {
		org := fmt.Sprintf("org-%02d", o)
		for copyIdx := range replayCopy {
			wg.Add(1)
			go func(org string, _ int) {
				defer wg.Done()
				for i := range txPerOrg {
					// Deterministic entry_id: even i = +100 deposit, odd i = -30 usage debit
					sign := int8(1)
					amt := int64(100)
					kind := "deposit"
					if i%2 == 1 {
						sign = -1
						amt = 30
						kind = "usage"
					}
					_ = sim.ingestFact(CommerceLedgerFact{
						EntryID:     fmt.Sprintf("%s:%s:%d", kind, org, i),
						Org:         org,
						Product:     "ai",
						Kind:        kind,
						Sign:        sign,
						AmountMinor: amt,
						CashMinor:   amt,
						Currency:    "usd",
					})
				}
			}(org, copyIdx)
		}
	}
	wg.Wait()

	// Per org: 13 even indices (+100 each = +1300) and 12 odd indices (-30 each = -360) => net 940 minor.
	auth := make(map[string]OrgBalanceState, numOrgs)
	for o := range numOrgs {
		org := fmt.Sprintf("org-%02d", o)
		auth[org+":usd"] = OrgBalanceState{Org: org, Currency: "usd", CashMinor: 940}
	}
	rep := ReconcileOrgBalances(auth, sim.netFlowByOrgCurrency())
	if !rep.Exact || rep.CheckedOrgs != numOrgs || rep.TotalCashDriftMinor != 0 || rep.TotalCreditDriftMinor != 0 {
		t.Fatalf("concurrent mutation + replay reconciliation failed: %+v", rep)
	}
}
