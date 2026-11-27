package sqlstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/hanzoai/cloud/apps/treasury/ledger"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/tenure"
	"github.com/hanzoai/cek"

	// devmaster keys this test binary: cek opens nothing without a master, and a
	// process with no KMS mints its own. It says so ONCE for the package, which is
	// why it sits in the file every test here reaches through rather than in the
	// one that happened to be written first — this import lived in a file that was
	// deleted, and every test in the package failed to open a database.
	"github.com/hanzoai/cloud/money"
	_ "github.com/hanzoai/cloud/sdk/kit/master"
	"github.com/hanzoai/namespace"
)

// testMaster is the data-plane master a test deployment is given (cloud.Deps.MasterKey).
var testMaster = bytes.Repeat([]byte{0x5a}, 32)

// testFields keys the memo column of a hand-built book, as Opener keys a real one.
func testFields(t *testing.T) *tenure.Fields {
	t.Helper()
	c, err := tenure.NewCipher(testMaster)
	if err != nil {
		t.Fatal(err)
	}
	f, err := c.Fields(namespace.System(), "treasury")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// openOn opens the house book on this leg's engine. Called again after a Close, it
// reaches the same book: that is how a test reopens one.
func openOn(t *testing.T, open sqltest.Open, usageKind string) *Store {
	t.Helper()
	s, err := Opener(testMaster, "treasury", usageKind)(namespace.System(), open(namespace.System(), "treasury"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// The adapter satisfies the port and round-trips a balanced entry: balances,
// prefix rollup and entry+postings all read back.
func TestSQLStore_RoundTrip(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		roundTrip(t, openOn(t, open, ""))
	})
}

func roundTrip(t *testing.T, s *Store) {
	ctx := context.Background()
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
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		txRollback(t, openOn(t, open, ""))
	})
}

func txRollback(t *testing.T, s *Store) {
	ctx := context.Background()
	sentinel := errors.New("boom")
	err := s.Tx(ctx, func(tx ledger.Tx) error {
		if ierr := tx.Insert(
			ledger.JournalEntry{ID: "e_x", Kind: "seed", Ref: "x", Amount: money.FromCents(100), CreatedAt: 1},
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

// A file written before entries carried a scope gains the project and service columns
// on open. (SQLite only: a pre-scope file is a SQLite file, built here by hand; no
// PostgreSQL database was ever made without the columns.) Its existing entries read no scope, so they count toward the unscoped sum
// and toward no scoped one, and new entries are written with theirs.
func TestAddEntryScope(t *testing.T) {
	ctx := context.Background()
	db, err := cek.Open(namespace.System(), "prescope", t.TempDir())
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, stmt := range []string{
		`CREATE TABLE treasury_accounts (id TEXT PRIMARY KEY, kind TEXT NOT NULL, balance TEXT NOT NULL DEFAULT '0', created_at INTEGER NOT NULL)`,
		`CREATE TABLE treasury_entries (id TEXT PRIMARY KEY, kind TEXT NOT NULL, program TEXT NOT NULL DEFAULT '',
		   ref TEXT NOT NULL, memo TEXT NOT NULL DEFAULT '', amount TEXT NOT NULL DEFAULT '0', created_at INTEGER NOT NULL,
		   UNIQUE(kind, program, ref))`,
		`CREATE TABLE treasury_postings (id INTEGER PRIMARY KEY AUTOINCREMENT, entry_id TEXT NOT NULL,
		   account TEXT NOT NULL, amount TEXT NOT NULL DEFAULT '0', created_at INTEGER NOT NULL)`,
		`INSERT INTO treasury_entries (id,kind,program,ref,memo,amount,created_at) VALUES ('e_old','usage','wallet','r0','m','` +
			money.FromCents(400).AttoString() + `',1)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("pre-scope seed %q: %v", stmt, err)
		}
	}

	s := &Store{db: db, fields: testFields(t)}
	if err := s.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := s.Tx(ctx, func(tx ledger.Tx) error {
		return tx.Insert(
			ledger.JournalEntry{ID: "e_new", Kind: "usage", Program: "wallet", Ref: "r1", Service: "ask", Project: "web",
				Amount: money.FromCents(300), CreatedAt: 2},
			[]ledger.Posting{{Account: "wallet", Amount: money.FromCents(-300)}, {Account: "revenue", Amount: money.FromCents(300)}},
		)
	}); err != nil {
		t.Fatalf("insert scoped entry: %v", err)
	}

	for _, c := range []struct {
		project, service string
		want             int64
	}{{"", "", 700}, {"", "ask", 300}, {"web", "", 300}, {"web", "ask", 300}, {"", "ai", 0}} {
		got, err := s.SumByKindSince(ctx, "usage", c.project, c.service, 0)
		if err != nil || got.Cents() != c.want {
			t.Errorf("SumByKindSince(project=%q, service=%q) = %d,%v, want %d", c.project, c.service, got.Cents(), err, c.want)
		}
	}
	entries, err := s.Entries(ctx, 0)
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	scope := map[string][2]string{}
	for _, e := range entries {
		scope[e.ID] = [2]string{e.Project, e.Service}
	}
	if scope["e_old"] != [2]string{"", ""} || scope["e_new"] != [2]string{"web", "ask"} {
		t.Fatalf("entry scopes = %v, want e_old unscoped and e_new web/ask", scope)
	}
	// A second open finds the columns present and changes nothing.
	if err := s.migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

// A legacy (int64-cents) ledger file (SQLite only, for the same reason as above) migrates to the exact 18-decimal integer on Open: balances are
// materialized and every stored cents value is scaled ×1e16 with no loss.
func TestMigrateCentsToUnits(t *testing.T) {
	ctx := context.Background()

	// Hand-build the pre-migration schema and seed it as the old code would have.
	db, err := cek.Open(namespace.System(), "legacy", t.TempDir())
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
	s := &Store{db: db, fields: testFields(t)}
	if err := s.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	reserve, _ := s.Balance(ctx, ledger.AccountReserve)
	if reserve.Cmp(money.FromCents(6000)) != 0 {
		t.Fatalf("reserve after migrate = %s, want $60", reserve)
	}
	if reserve.AttoString() != "60000000000000000000" { // 6000 cents × 1e16
		t.Fatalf("reserve 18-dec = %s, want 6000×1e16", reserve.AttoString())
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

// A file written before entries had a sequence is numbered in the order its rows were
// written (SQLite only: the rowid it reads is a SQLite file's own). Two entries of one
// second then list newest-first, the same as a file that had the column all along.
func TestEntrySeqNumbersALegacyFileInWriteOrder(t *testing.T) {
	ctx := context.Background()
	db, err := cek.Open(namespace.System(), "preseq", t.TempDir())
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, stmt := range []string{
		`CREATE TABLE treasury_entries (id TEXT PRIMARY KEY, kind TEXT NOT NULL, program TEXT NOT NULL DEFAULT '',
		   ref TEXT NOT NULL, memo TEXT NOT NULL DEFAULT '', project TEXT NOT NULL DEFAULT '', service TEXT NOT NULL DEFAULT '',
		   amount TEXT NOT NULL DEFAULT '0', created_at INTEGER NOT NULL, UNIQUE(kind, program, ref))`,
		`INSERT INTO treasury_entries (id,kind,ref,created_at) VALUES ('zzz_first','k','r1',7),('aaa_second','k','r2',7),('mmm_third','k','r3',7)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	s := &Store{db: db, fields: testFields(t)}
	if err := s.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := s.Tx(ctx, func(tx ledger.Tx) error {
		return tx.Insert(ledger.JournalEntry{ID: "new", Kind: "k", Ref: "r4", Amount: money.FromCents(1), CreatedAt: 7},
			[]ledger.Posting{{Account: "a", Amount: money.FromCents(1)}, {Account: "b", Amount: money.FromCents(-1)}})
	}); err != nil {
		t.Fatalf("insert after migrate: %v", err)
	}
	es, err := s.Entries(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(es); fmt.Sprint(got) != "[new mmm_third aaa_second zzz_first]" {
		t.Fatalf("entries of one second = %v; want newest first by write order, not by id", got)
	}
	// Opening again finds the column and the index and changes nothing.
	if err := s.migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

// A usage log written before it named who acted gains the actor and api_key columns on
// open. Its rows read none, and a use counted afterwards keeps both. A second open finds
// the columns present and changes nothing.
func TestAddUseAttribution(t *testing.T) {
	ctx := context.Background()
	s, err := Open(namespace.System(), "attribution", t.TempDir(), "usage", testMaster)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	// A file from before the columns: the log without them, one use in it.
	if _, err := s.db.Exec(`DROP TABLE treasury_usage`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TABLE treasury_usage (
	  id TEXT PRIMARY KEY, program TEXT NOT NULL, ref TEXT NOT NULL, entry_id TEXT NOT NULL DEFAULT '',
	  model TEXT NOT NULL DEFAULT '', provider TEXT NOT NULL DEFAULT '', class TEXT NOT NULL DEFAULT '',
	  units INTEGER NOT NULL DEFAULT 0, amount TEXT NOT NULL DEFAULT '0', cost TEXT NOT NULL DEFAULT '0',
	  paid_by TEXT NOT NULL DEFAULT '', project TEXT NOT NULL DEFAULT '', service TEXT NOT NULL DEFAULT '',
	  created_at INTEGER NOT NULL, UNIQUE(program, ref))`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO treasury_usage (id,program,ref,model,created_at) VALUES ('u_old','wallet','r0','zen',1)`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := s.Tx(ctx, func(tx ledger.Tx) error {
		return tx.(interface{ InsertUse(Use) error }).InsertUse(Use{
			ID: "u_new", Program: "wallet", Ref: "r1", Model: "openai/gpt-oss-20b", Amount: money.FromCents(5), Cost: money.Zero(),
			Actor: "acme/bob", Key: "acme/key-1", CreatedAt: 2,
		})
	}); err != nil {
		t.Fatalf("insert use: %v", err)
	}
	uses, err := s.Uses(ctx, "usage", 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][2]string{}
	for _, u := range uses {
		got[u.ID] = [2]string{u.Actor, u.Key}
	}
	if got["u_old"] != [2]string{"", ""} || got["u_new"] != [2]string{"acme/bob", "acme/key-1"} {
		t.Fatalf("uses = %v, want u_old naming none and u_new naming bob and acme/key-1", got)
	}
	if err := s.migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}
