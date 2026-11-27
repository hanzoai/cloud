package sqlstore

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/apps/treasury/ledger"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/money"
)

// post writes one entry whose legs are given as (account, cents) pairs.
func post(t *testing.T, s *Store, id, kind, program, ref, project, service string, at int64, legs ...any) {
	t.Helper()
	var postings []ledger.Posting
	var gross int64
	for i := 0; i < len(legs); i += 2 {
		c := legs[i+1].(int64)
		postings = append(postings, ledger.Posting{Account: legs[i].(string), Amount: money.FromCents(c)})
		if c > 0 {
			gross += c
		}
	}
	if err := s.Tx(context.Background(), func(tx ledger.Tx) error {
		return tx.Insert(ledger.JournalEntry{
			ID: id, Kind: kind, Program: program, Ref: ref, Project: project, Service: service,
			Amount: money.FromCents(gross), CreatedAt: at,
		}, postings)
	}); err != nil {
		t.Fatalf("post %s: %v", id, err)
	}
}

func ids(es []ledger.JournalEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.ID
	}
	return out
}

func eq(t *testing.T, what string, got, want any) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// Every method of the store, run against one small book, must give the same answer on
// both engines. The book has two entries in the same second, so the order within it is
// the write order and not whatever the engine finds first.
func TestEveryReadAndWriteAgreesOnBothEngines(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		s := openOn(t, open, "usage")

		post(t, s, "e4", "deposit", "", "dep1", "", "", 50, "funding:platform", int64(-1000), "wallet:a", int64(1000))
		post(t, s, "e1", "usage", "wallet:a", "r1", "web", "ask", 100, "wallet:a", int64(-300), "revenue:platform", int64(300))
		post(t, s, "e2", "usage", "wallet:a", "r2", "web", "ai", 100, "wallet:a", int64(-200), "revenue:platform", int64(200))
		post(t, s, "e3", "usage", "wallet:b", "r3", "", "", 200, "wallet:b", int64(-700), "revenue:platform", int64(700))

		cents := func(a money.Amount, err error) int64 {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
			return a.Cents()
		}
		eq(t, "balance wallet:a", cents(s.Balance(ctx, "wallet:a")), 500)
		eq(t, "balance wallet:b", cents(s.Balance(ctx, "wallet:b")), -700)
		eq(t, "balance of an account never posted to", cents(s.Balance(ctx, "nobody")), 0)

		bs, err := s.Balances(ctx, "wallet:a", "wallet:b", "nobody")
		if err != nil {
			t.Fatal(err)
		}
		eq(t, "Balances", fmt.Sprint(bs["wallet:a"].Cents(), bs["wallet:b"].Cents(), bs["nobody"].Cents(), len(bs)), "500 -700 0 3")
		pre, err := s.BalancesWithPrefix(ctx, "wallet:")
		if err != nil {
			t.Fatal(err)
		}
		eq(t, "BalancesWithPrefix count", len(pre), 2)

		all, err := s.Entries(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		eq(t, "Entries newest first, ties in write order", ids(all), "[e3 e2 e1 e4]")
		eq(t, "postings travel with their entry", len(all[0].Postings), 2)
		two, _ := s.Entries(ctx, 2)
		eq(t, "Entries limited", ids(two), "[e3 e2]")
		on, err := s.EntriesOn(ctx, 0, "wallet:a")
		if err != nil {
			t.Fatal(err)
		}
		eq(t, "EntriesOn(wallet:a)", ids(on), "[e2 e1 e4]")
		onTwo, _ := s.EntriesOn(ctx, 2, "wallet:a", "wallet:b")
		eq(t, "EntriesOn two accounts, limited", ids(onTwo), "[e3 e2]")
		kind, err := s.EntriesOfKind(ctx, "usage")
		if err != nil {
			t.Fatal(err)
		}
		eq(t, "EntriesOfKind oldest first", ids(kind), "[e1 e2 e3]")
		eq(t, "EntriesOfKind legs", fmt.Sprint(len(kind[0].Postings), len(kind[2].Postings)), "2 2")

		eq(t, "SumProgramsSince(wallet:, 100)", cents(s.SumProgramsSince(ctx, "usage", "wallet:", 100)), 1200)
		eq(t, "SumProgramsSince(wallet:, 150)", cents(s.SumProgramsSince(ctx, "usage", "wallet:", 150)), 700)
		eq(t, "SumProgramSince(wallet:a, 100)", cents(s.SumProgramSince(ctx, "usage", "wallet:a", 100)), 500)
		eq(t, "SumLegsSince root", cents(s.SumLegsSince(ctx, "usage", 0, "revenue:platform")), 1200)
		eq(t, "SumLegsSince family", cents(s.SumLegsSince(ctx, "usage", 0, "wallet")), -1200)
		eq(t, "SumByKindSince all", cents(s.SumByKindSince(ctx, "usage", "", "", 0)), 1200)
		eq(t, "SumByKindSince project", cents(s.SumByKindSince(ctx, "usage", "web", "", 0)), 500)
		eq(t, "SumByKindSince service", cents(s.SumByKindSince(ctx, "usage", "", "ask", 0)), 300)
		eq(t, "SumByKindSince both", cents(s.SumByKindSince(ctx, "usage", "web", "ai", 0)), 200)

		if err := s.Tx(ctx, func(tx ledger.Tx) error {
			x := tx.(*txAdapter)
			legs, err := x.Legs("wallet:a")
			if err != nil {
				return err
			}
			var order []string
			for _, l := range legs {
				order = append(order, fmt.Sprint(l.Entry, ":", l.Amount.Cents()))
			}
			eq(t, "Legs in write order", order, "[e4:1000 e1:-300 e2:-200]")
			sum, err := x.SumProgram("usage", "wallet:a")
			if err != nil {
				return err
			}
			eq(t, "SumProgram", sum.Cents(), 500)
			p, err := x.Prefixed("wallet:")
			if err != nil {
				return err
			}
			eq(t, "Prefixed", p.Cents(), -200)
			e, ok, err := tx.EntryByRef("usage", "wallet:a", "r2")
			if err != nil || !ok {
				return fmt.Errorf("EntryByRef: %v %v", ok, err)
			}
			eq(t, "EntryByRef", fmt.Sprintf("%s %d", e.ID, len(e.Postings)), "e2 2")
			if _, ok, _ := tx.EntryByRef("usage", "wallet:a", "nope"); ok {
				t.Error("EntryByRef found a ref nobody wrote")
			}
			byID, ok, err := tx.EntryByID("e3")
			if err != nil || !ok {
				return fmt.Errorf("EntryByID: %v %v", ok, err)
			}
			eq(t, "EntryByID", byID.Ref, "r3")
			bal, err := tx.Balance("wallet:b")
			if err != nil {
				return err
			}
			eq(t, "tx.Balance", bal.Cents(), -700)
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		// The key an entry is unique under is enforced, and a refused write leaves no trace.
		dup := s.Tx(ctx, func(tx ledger.Tx) error {
			return tx.Insert(ledger.JournalEntry{ID: "dup", Kind: "usage", Program: "wallet:a", Ref: "r1", CreatedAt: 300},
				[]ledger.Posting{{Account: "wallet:a", Amount: money.FromCents(-1)}, {Account: "revenue:platform", Amount: money.FromCents(1)}})
		})
		if dup == nil {
			t.Error("a second entry under one (kind, program, ref) was accepted")
		}
		eq(t, "balance after a refused write", cents(s.Balance(ctx, "wallet:a")), 500)

		// Policy, limit and owing are single rows replaced by upsert.
		pol, _ := s.Policy(ctx)
		eq(t, "default policy", pol.RevenueShareBps, ledger.DefaultRevenueShareBps)
		for _, bps := range []int64{1500, 0} {
			if err := s.SetPolicy(ctx, ledger.SharePolicy{RevenueShareBps: bps, UpdatedAt: 5}); err != nil {
				t.Fatal(err)
			}
			pol, _ = s.Policy(ctx)
			eq(t, "policy after SetPolicy", fmt.Sprint(pol.RevenueShareBps, pol.UpdatedAt), fmt.Sprint(bps, 5))
		}
		eq(t, "default limit", cents(s.Limit(ctx)), 0)
		for _, c := range []int64{777, 5} {
			if err := s.SetLimit(ctx, money.FromCents(c), 9); err != nil {
				t.Fatal(err)
			}
			eq(t, "limit after SetLimit", cents(s.Limit(ctx)), c)
		}
		if s.SetLimit(ctx, money.FromCents(-1), 9) == nil {
			t.Error("a negative limit was accepted")
		}
		if n, _ := s.Owing(ctx); n != 0 {
			t.Errorf("default owing = %d", n)
		}
		for _, at := range []int64{55, 66} {
			if err := s.Tx(ctx, func(tx ledger.Tx) error { return tx.(*txAdapter).Owe(at) }); err != nil {
				t.Fatal(err)
			}
			n, _ := s.Owing(ctx)
			eq(t, "owing after Owe", n, at)
		}

		// The usage log: counted calls, and the journal's own usage entries that were never counted.
		if err := s.Tx(ctx, func(tx ledger.Tx) error {
			x := tx.(*txAdapter)
			if err := x.InsertUse(Use{ID: "u1", Program: "wallet:a", Ref: "r1", Entry: "e1", Model: "m", Units: 5,
				Amount: money.FromCents(300), Cost: money.FromCents(120), Project: "web", Service: "ask", CreatedAt: 101}); err != nil {
				return err
			}
			if err := x.InsertUse(Use{ID: "u2", Program: "wallet:a", Ref: "free", CreatedAt: 102}); err != nil {
				return err
			}
			got, found, err := x.UseByRef("wallet:a", "r1")
			if err != nil || !found {
				return fmt.Errorf("UseByRef: %v %v", found, err)
			}
			eq(t, "UseByRef", fmt.Sprintf("%s %d %d %d", got.ID, got.Units, got.Amount.Cents(), got.Cost.Cents()), "u1 5 300 120")
			if _, found, _ := x.UseByRef("wallet:a", "nope"); found {
				t.Error("UseByRef found a use nobody counted")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		uses, err := s.Uses(ctx, "usage", 0)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, u := range uses {
			got = append(got, fmt.Sprint(u.ID, ":", u.Amount.Cents()))
		}
		// u2 and u1 are counted calls; e3 and e2 are usage entries no use row names (e1 has u1).
		eq(t, "Uses", got, "[e3:700 u2:0 u1:300 e2:200]")
		limited, _ := s.Uses(ctx, "usage", 2)
		eq(t, "Uses limited", len(limited), 2)
	})
}

// Money is exact: an amount past 64 bits goes in and comes out as the same digits,
// and a balance built from them is the sum of the digits and not of a float.
func TestAmountsAreExactPastSixtyFourBits(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		s := openOn(t, open, "")
		huge, err := money.ParseInt("123456789012345678901234567890123")
		if err != nil {
			t.Fatal(err)
		}
		one, _ := money.ParseInt("1")
		for i, amt := range []money.Amount{huge, one} {
			if err := s.Tx(ctx, func(tx ledger.Tx) error {
				return tx.Insert(ledger.JournalEntry{ID: fmt.Sprint("big", i), Kind: "seed", Ref: fmt.Sprint("big", i), Amount: amt, CreatedAt: int64(i)},
					[]ledger.Posting{{Account: "fund:reserve", Amount: amt}, {Account: "equity:house", Amount: amt.Neg()}})
			}); err != nil {
				t.Fatal(err)
			}
		}
		bal, err := s.Balance(ctx, "fund:reserve")
		if err != nil {
			t.Fatal(err)
		}
		if got := bal.AttoString(); got != "123456789012345678901234567890124" {
			t.Fatalf("balance = %s, want 123456789012345678901234567890124", got)
		}
		entries, _ := s.Entries(ctx, 0)
		if entries[1].Amount.AttoString() != "123456789012345678901234567890123" {
			t.Fatalf("entry amount = %s", entries[1].Amount.AttoString())
		}
		var stored string
		if err := s.db.QueryRow(`SELECT balance FROM treasury_accounts WHERE id = $1`, "fund:reserve").Scan(&stored); err != nil || stored != "123456789012345678901234567890124" {
			t.Fatalf("the stored balance is %q (%v); it must be the decimal string on both engines", stored, err)
		}
	})
}

// A balance is read, weighed, and written back inside one transaction, so two writers
// that could read between each other's read and write would lose an update. Thirty-two
// writers post to the same two accounts at once; every cent is still there.
func TestConcurrentWritersLoseNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		s := openOn(t, open, "")
		const writers, each = 32, 100
		var wg sync.WaitGroup
		errs := make(chan error, writers)
		for w := range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- s.Tx(ctx, func(tx ledger.Tx) error {
					return tx.Insert(
						ledger.JournalEntry{ID: fmt.Sprint("w", w), Kind: "usage", Program: "wallet", Ref: fmt.Sprint("r", w), Amount: money.FromCents(each), CreatedAt: 1},
						[]ledger.Posting{{Account: "wallet", Amount: money.FromCents(-each)}, {Account: "revenue:platform", Amount: money.FromCents(each)}})
				})
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		w, _ := s.Balance(ctx, "wallet")
		r, _ := s.Balance(ctx, "revenue:platform")
		if w.Cents() != -writers*each || r.Cents() != writers*each {
			t.Fatalf("wallet %d, revenue %d after %d concurrent debits of %d", w.Cents(), r.Cents(), writers, each)
		}
		entries, _ := s.Entries(ctx, 0)
		seen := map[string]bool{}
		for _, e := range entries {
			seen[e.ID] = true
		}
		if len(entries) != writers || len(seen) != writers {
			t.Fatalf("%d entries (%d distinct) after %d writers", len(entries), len(seen), writers)
		}
	})
}
