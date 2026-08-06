package sqlstore

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/apps/treasury/ledger"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/namespace"
)

// legacyUsage writes a usage entry the way it was written BEFORE a usage ref became
// unique per wallet: the two real legs, and an EMPTY program. It goes in through the same
// adapter production uses, so the only thing the test fabricates is the empty program.
func legacyUsage(t *testing.T, s *Store, id, ref, wallet string, cents int64) {
	t.Helper()
	amt := money.FromCents(cents)
	if err := s.Tx(context.Background(), func(tx ledger.Tx) error {
		return tx.Insert(
			ledger.JournalEntry{ID: id, Kind: "finance.usage", Program: "", Ref: ref, Amount: amt, CreatedAt: 1},
			[]ledger.Posting{
				{Account: wallet, Amount: amt.Neg()},
				{Account: "revenue:platform", Amount: amt},
			})
	}); err != nil {
		t.Fatalf("write legacy usage %s: %v", id, err)
	}
}

// programOf reads an entry's program column straight out of the file, so the assertion
// is about what is STORED rather than about what a reader reconstructs.
func programOf(t *testing.T, s *Store, id string) string {
	t.Helper()
	var program string
	if err := s.db.QueryRow(`SELECT program FROM treasury_entries WHERE id=?`, id).Scan(&program); err != nil {
		t.Fatalf("read program of %s: %v", id, err)
	}
	return program
}

// reopen closes the store and opens the SAME file again, which is the only way the
// migration — and so the backfill — runs a second time.
func reopen(t *testing.T, s *Store, dir string) *Store {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	next, err := Open(namespace.System(), houseSubsystem, dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = next.Close() })
	return next
}

// A usage entry written before a ref was scoped to its wallet is given that wallet on
// the next open, read off the leg the money actually left by.
//
// Without it the probe that asks "is this act already paid for?" looks under the wallet
// and finds nothing, so one act is debited twice across the deploy — see
// TestBackfilledProgramStopsTheSecondDebit, which charges the money.
func TestBackfillUsageProgramNamesTheDebitedWallet(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(namespace.System(), houseSubsystem, dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	legacyUsage(t, s, "use_pool", "act_1", "wallet", 300)
	legacyUsage(t, s, "use_bob", "act_2", "wallet:bob", 700)
	if got := programOf(t, s, "use_pool"); got != "" {
		t.Fatalf("fixture is not a pre-deploy row: program = %q, want empty", got)
	}

	s = reopen(t, s, dir)

	if got := programOf(t, s, "use_pool"); got != "wallet" {
		t.Errorf("org-pool usage program = %q; want %q — the backfill must read the debited leg", got, "wallet")
	}
	if got := programOf(t, s, "use_bob"); got != "wallet:bob" {
		t.Errorf("per-user usage program = %q; want %q — each row takes ITS OWN wallet, not one for the file",
			got, "wallet:bob")
	}
}

// A DEPOSIT keeps its empty program, and that is deliberate: a settlement ref is unique
// across the whole org's books rather than per wallet, so re-keying deposits would move
// every payment ever taken out from under its own idempotency.
//
// The house book's own kinds are left alone for the same reason — this migration is about
// one writer's key, not about every entry that happens to have no program.
func TestBackfillLeavesEveryOtherKindAlone(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(namespace.System(), houseSubsystem, dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	amt := money.FromCents(500)
	if err := s.Tx(context.Background(), func(tx ledger.Tx) error {
		if derr := tx.Insert(
			ledger.JournalEntry{ID: "dep_1", Kind: "finance.deposit", Ref: "sq_pay_1", Amount: amt, CreatedAt: 1},
			[]ledger.Posting{
				{Account: "funding:platform", Amount: amt.Neg()},
				{Account: "wallet", Amount: amt},
			}); derr != nil {
			return derr
		}
		return tx.Insert(
			ledger.JournalEntry{ID: "seed_1", Kind: "seed", Ref: "seed:1", Amount: amt, CreatedAt: 1},
			[]ledger.Posting{
				{Account: "equity:house", Amount: amt.Neg()},
				{Account: "reserve", Amount: amt},
			})
	}); err != nil {
		t.Fatalf("write fixtures: %v", err)
	}

	s = reopen(t, s, dir)

	if got := programOf(t, s, "dep_1"); got != "" {
		t.Errorf("deposit program = %q; want empty — a settlement ref is unique across the books", got)
	}
	if got := programOf(t, s, "seed_1"); got != "" {
		t.Errorf("house-book program = %q; want empty — only finance.usage is re-keyed", got)
	}
}

// The backfill runs on EVERY open, so it has to be a no-op after the first one: a row
// already carrying its wallet must not be re-read, and a row written today must not be
// touched at all.
func TestBackfillUsageProgramIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(namespace.System(), houseSubsystem, dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	legacyUsage(t, s, "use_pre", "act_1", "wallet:bob", 300)
	s = reopen(t, s, dir)
	first := programOf(t, s, "use_pre")

	// A row written TODAY already names its wallet, and the two must be indistinguishable
	// afterwards — the migration is a repair, not a second rule about who pays.
	amt := money.FromCents(100)
	if err := s.Tx(context.Background(), func(tx ledger.Tx) error {
		return tx.Insert(
			ledger.JournalEntry{ID: "use_now", Kind: "finance.usage", Program: "wallet:bob", Ref: "act_2", Amount: amt, CreatedAt: 2},
			[]ledger.Posting{
				{Account: "wallet:bob", Amount: amt.Neg()},
				{Account: "revenue:platform", Amount: amt},
			})
	}); err != nil {
		t.Fatalf("write current-shape usage: %v", err)
	}

	for range 3 {
		s = reopen(t, s, dir)
	}
	if got := programOf(t, s, "use_pre"); got != first {
		t.Errorf("re-running the backfill moved a repaired row: %q -> %q", first, got)
	}
	if got := programOf(t, s, "use_now"); got != "wallet:bob" {
		t.Errorf("a row written today was rewritten: program = %q, want %q", got, "wallet:bob")
	}
}

// A usage row with NO negative leg is not a debit this ledger wrote, and the backfill
// SKIPS it rather than writing NULL into a NOT NULL column.
//
// The difference is the whole org's books: an aborted migration is a store that will not
// open, so one malformed row would lock a customer out of their own wallet — the failure
// mode a repair must never have.
func TestBackfillSkipsAnEntryWithNoDebitedLeg(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(namespace.System(), houseSubsystem, dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if _, err := s.db.Exec(
		`INSERT INTO treasury_entries (id,kind,program,ref,memo,amount,created_at) VALUES (?,?,?,?,?,?,?)`,
		"use_legless", "finance.usage", "", "act_x", "", "0", 1); err != nil {
		t.Fatalf("write malformed entry: %v", err)
	}
	legacyUsage(t, s, "use_ok", "act_y", "wallet", 100)

	s = reopen(t, s, dir) // must NOT fail: the whole point is that the open still succeeds

	if got := programOf(t, s, "use_legless"); got != "" {
		t.Errorf("legless entry program = %q; want empty — there is no wallet to read", got)
	}
	if got := programOf(t, s, "use_ok"); got != "wallet" {
		t.Errorf("a well-formed row beside it was not repaired: program = %q, want %q", got, "wallet")
	}
}
