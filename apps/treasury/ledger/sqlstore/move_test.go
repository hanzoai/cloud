package sqlstore

import (
	"context"
	"reflect"
	"testing"

	"github.com/hanzoai/cloud/apps/treasury/ledger"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
)

// The house book leaves its file for hanzo-sql with every entry, posting, balance and
// the order of the journal, and the next entry after the move is the last one in it.
func TestTheHouseBookMovesFromItsFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv(locus.Env, "")
	s, err := Open(namespace.System(), "treasury", dir, "", testMaster)
	if err != nil {
		t.Fatal(err)
	}
	l := ledger.New(s)
	if _, _, err := l.Seed(ctx, "seed:1", "cap", 10_000, 1); err != nil {
		t.Fatal(err)
	}
	for i, prog := range []string{"referral", "reward", "referral"} {
		if _, backed, _, err := l.DebitReserve(ctx, prog, "r"+string(rune('1'+i)), "bonus", int64(100*(i+1)), int64(2+i)); err != nil || !backed {
			t.Fatalf("debit %d: backed=%v err=%v", i, backed, err)
		}
	}
	want := sqltest.Dump(t, s.db)
	wantEntries, err := s.Entries(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	wantReserve, err := s.Balance(ctx, ledger.AccountReserve)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	sqltest.Server(t)
	t.Setenv(locus.MoveEnv, "lazy")
	m, err := Open(namespace.System(), "treasury", dir, "", testMaster)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if !dialect.Of(m.db).Postgres() {
		t.Fatal("the house book did not move")
	}
	sqltest.Same(t, want, sqltest.Dump(t, m.db))

	gotEntries, err := m.Entries(ctx, 100)
	if err != nil || !reflect.DeepEqual(gotEntries, wantEntries) {
		t.Fatalf("the journal changed in the move: %v\n got %+v\nwant %+v", err, gotEntries, wantEntries)
	}
	if got, err := m.Balance(ctx, ledger.AccountReserve); err != nil || got.Cmp(wantReserve) != 0 {
		t.Fatalf("the reserve is %v after the move, %v; want %v", got, err, wantReserve)
	}

	// The next entry is the newest, by seq, and the book still balances.
	ml := ledger.New(m)
	if _, backed, _, err := ml.DebitReserve(ctx, "referral", "after", "bonus", 50, 20); err != nil || !backed {
		t.Fatalf("a debit after the move: backed=%v err=%v", backed, err)
	}
	after, err := m.Entries(ctx, 100)
	if err != nil || len(after) != len(wantEntries)+1 {
		t.Fatalf("%d entries after the move and a debit, %v; want %d", len(after), err, len(wantEntries)+1)
	}
}
