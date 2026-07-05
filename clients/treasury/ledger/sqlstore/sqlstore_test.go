package sqlstore

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud/clients/treasury/ledger"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/treasury.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// The adapter satisfies the port and round-trips a balanced entry: balances,
// prefix rollup and entry+postings all read back.
func TestSQLStore_RoundTrip(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	l := ledger.New(s)

	if _, _, err := l.Seed(ctx, "seed:1", "cap", 10_000, 1); err != nil {
		t.Fatal(err)
	}
	if _, backed, _, err := l.DebitReserve(ctx, "referral", "r1", "bonus", 4_000, 2); err != nil || !backed {
		t.Fatalf("debit: backed=%v err=%v", backed, err)
	}
	if bal, _ := s.Balance(ctx, ledger.AccountReserve); bal != 6_000 {
		t.Fatalf("reserve = %d, want 6000", bal)
	}
	byProg, _ := s.BalancesWithPrefix(ctx, "payout:")
	if byProg["payout:referral"] != 4_000 {
		t.Fatalf("payout:referral = %d, want 4000", byProg["payout:referral"])
	}
	entries, _ := s.Entries(ctx, 10)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	for _, e := range entries {
		if len(e.Postings) != 2 {
			t.Fatalf("entry %s has %d postings, want 2", e.ID, len(e.Postings))
		}
	}
}

// Tx rolls back on a returned error: a partial write inside a failing tx leaves NO
// trace (atomicity — the property the balance guard relies on).
func TestSQLStore_TxRollback(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	sentinel := errors.New("boom")
	err := s.Tx(ctx, func(tx ledger.Tx) error {
		if ierr := tx.Insert(
			ledger.Entry{ID: "e_x", Kind: "seed", Ref: "x", AmountCents: 100, CreatedAt: 1},
			[]ledger.Posting{{Account: ledger.AccountReserve, Amount: 100}, {Account: ledger.AccountRevenue, Amount: -100}},
		); ierr != nil {
			return ierr
		}
		return sentinel // force rollback AFTER a write
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Tx err = %v, want sentinel", err)
	}
	if bal, _ := s.Balance(ctx, ledger.AccountReserve); bal != 0 {
		t.Fatalf("rolled-back write persisted: reserve = %d, want 0", bal)
	}
	entries, _ := s.Entries(ctx, 10)
	if len(entries) != 0 {
		t.Fatalf("rolled-back entry persisted: %d entries", len(entries))
	}
}
