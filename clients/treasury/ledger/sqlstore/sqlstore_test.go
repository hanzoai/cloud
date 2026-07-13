package sqlstore

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud/cek"
	"github.com/hanzoai/cloud/clients/money"
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
	if bal, _ := s.Balance(ctx, ledger.AccountReserve); bal.Cents() != 6_000 {
		t.Fatalf("reserve = %d, want 6000", bal.Cents())
	}
	byProg, _ := s.BalancesWithPrefix(ctx, "payout:")
	if byProg["payout:referral"].Cents() != 4_000 {
		t.Fatalf("payout:referral = %d, want 4000", byProg["payout:referral"].Cents())
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
			ledger.Entry{ID: "e_x", Kind: "seed", Ref: "x", Amount: money.FromCents(100), CreatedAt: 1},
			[]ledger.Posting{{Account: ledger.AccountReserve, Amount: money.FromCents(100)}, {Account: ledger.AccountRevenue, Amount: money.FromCents(-100)}},
		); ierr != nil {
			return ierr
		}
		return sentinel // force rollback AFTER a write
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Tx err = %v, want sentinel", err)
	}
	if bal, _ := s.Balance(ctx, ledger.AccountReserve); !bal.IsZero() {
		t.Fatalf("rolled-back write persisted: reserve = %s, want 0", bal)
	}
	entries, _ := s.Entries(ctx, 10)
	if len(entries) != 0 {
		t.Fatalf("rolled-back entry persisted: %d entries", len(entries))
	}
}

// A legacy (int64-cents) ledger file migrates to the exact 18-decimal integer on Open: balances are
// materialized and every stored cents value is scaled ×1e16 with no loss.
func TestMigrateCentsToUnits(t *testing.T) {
	ctx := context.Background()

	// Hand-build the pre-migration schema and seed it as the old code would have.
	db, err := cek.Open(t.TempDir() + "/legacy.db")
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	legacy := []string{
		`CREATE TABLE treasury_accounts (id TEXT PRIMARY KEY, kind TEXT NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE TABLE treasury_entries (id TEXT PRIMARY KEY, kind TEXT NOT NULL, program TEXT NOT NULL DEFAULT '',
		   ref TEXT NOT NULL, memo TEXT NOT NULL DEFAULT '', amount_cents INTEGER NOT NULL, created_at INTEGER NOT NULL,
		   UNIQUE(kind, program, ref))`,
		`CREATE TABLE treasury_postings (id INTEGER PRIMARY KEY AUTOINCREMENT, entry_id TEXT NOT NULL,
		   account TEXT NOT NULL, amount INTEGER NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE TABLE treasury_policy (id INTEGER PRIMARY KEY CHECK (id=1), revenue_share_bps INTEGER NOT NULL DEFAULT 0,
		   updated_at INTEGER NOT NULL DEFAULT 0)`,
		`INSERT INTO treasury_accounts VALUES ('fund:reserve','fund',1),('revenue:platform','revenue',1),('payout:referral','payout',2)`,
		`INSERT INTO treasury_entries VALUES ('e_seed','seed','','seed:1','cap',10000,1),('e_pay','payout','referral','r1','bonus',4000,2)`,
		`INSERT INTO treasury_postings (entry_id,account,amount,created_at) VALUES
		   ('e_seed','fund:reserve',10000,1),('e_seed','revenue:platform',-10000,1),
		   ('e_pay','fund:reserve',-4000,2),('e_pay','payout:referral',4000,2)`,
	}
	for _, stmt := range legacy {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("legacy seed %q: %v", stmt, err)
		}
	}

	// Run the REAL migration in-process (migrate() = 18-decimal DDL no-op over the existing
	// legacy tables, then migrateCentsToUnits). Done on the open connection rather than
	// via a close+reopen, which a mis-linked local libsqlcipher cannot do (sqlcipher_export);
	// CI's real libsqlcipher runs Open() end to end.
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	reserve, _ := s.Balance(ctx, ledger.AccountReserve)
	if reserve.Cmp(money.FromCents(6000)) != 0 {
		t.Fatalf("reserve after migrate = %s, want $60", reserve)
	}
	if reserve.IntString() != "60000000000000000000" { // 6000 cents × 1e16
		t.Fatalf("reserve 18-dec = %s, want 6000×1e16", reserve.IntString())
	}
	if payout, _ := s.Balance(ctx, ledger.PayoutAccount("referral")); payout.Cmp(money.FromCents(4000)) != 0 {
		t.Fatalf("payout:referral after migrate = %s, want $40", payout)
	}
	entries, _ := s.Entries(ctx, 10)
	if len(entries) != 2 {
		t.Fatalf("migrated entries = %d, want 2", len(entries))
	}
	got := map[string]int64{}
	for _, e := range entries {
		got[e.Kind] = e.Amount.Cents()
		if len(e.Postings) != 2 {
			t.Fatalf("entry %s has %d postings, want 2", e.ID, len(e.Postings))
		}
	}
	if got["seed"] != 10000 || got["payout"] != 4000 {
		t.Fatalf("migrated entry amounts = %+v, want seed=10000 payout=4000", got)
	}
}
