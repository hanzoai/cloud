package finance

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/types"
)

// TestListUsage proves the co-resident usage read returns the SAME debits
// RecordUsage wrote (magnitude + model), most-recent-first, and excludes deposits —
// so /v1/billing/usage can answer from the ledger instead of self-dispatching.
func TestListUsage(t *testing.T) {
	f := New(Local(t.TempDir()))
	ctx := context.Background()
	const org = "acme"

	// A grant (deposit) must NOT appear as usage.
	if _, err := f.Deposit(ctx, types.DepositInput{Org: org, Subject: org, Amount: money.FromCents(10_000), Ref: "grant1"}); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	// Two usage debits.
	if err := f.RecordUsage(ctx, types.UsageInput{Org: org, Subject: org, Amount: money.FromCents(150), Model: "gpt-x", Ref: "r1"}); err != nil {
		t.Fatalf("usage r1: %v", err)
	}
	if err := f.RecordUsage(ctx, types.UsageInput{Org: org, Subject: org, Amount: money.FromCents(75), Model: "embed-y", Ref: "r2"}); err != nil {
		t.Fatalf("usage r2: %v", err)
	}

	rows, err := f.ListUsage(ctx, org, false, 100)
	if err != nil {
		t.Fatalf("ListUsage: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 usage rows (deposit excluded), got %d: %+v", len(rows), rows)
	}
	var total int64
	sawModel := map[string]int64{}
	for _, r := range rows {
		if r.ID == "" || r.CreatedAt == 0 {
			t.Errorf("row missing id/createdAt: %+v", r)
		}
		total += r.Cents
		sawModel[r.Model] = r.Cents
	}
	if total != 225 {
		t.Errorf("want total 225 cents, got %d", total)
	}
	if sawModel["gpt-x"] != 150 || sawModel["embed-y"] != 75 {
		t.Errorf("model→cents mismatch: %+v", sawModel)
	}

	// Idempotent replay of a recorded request must not double-count.
	if err := f.RecordUsage(ctx, types.UsageInput{Org: org, Subject: org, Amount: money.FromCents(150), Model: "gpt-x", Ref: "r1"}); err != nil {
		t.Fatalf("usage r1 replay: %v", err)
	}
	rows2, err := f.ListUsage(ctx, org, false, 100)
	if err != nil {
		t.Fatalf("ListUsage after replay: %v", err)
	}
	if len(rows2) != 2 {
		t.Errorf("idempotent replay must not add a row; want 2, got %d", len(rows2))
	}

	// An org with no file yet reads empty, not an error.
	empty, err := f.ListUsage(ctx, "neverused", false, 100)
	if err != nil {
		t.Fatalf("ListUsage empty org: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("want 0 rows for unused org, got %d", len(empty))
	}
}

// TestListEntriesAnswersOneWallet — the ledger read answers for the SUBJECT the
// balance answers for.
//
// A tenant org has one wallet, so its file and its wallet hold the same rows and any
// read of either is right. The shared signup org is where they part: every self-serve
// customer is a distinct billing subject inside ONE org's file (account.Payer resolves
// them to <org>/<name>), so alice and bob's entries sit side by side while their money
// does not. A page drawn from the FILE described a total the customer does not have,
// made of somebody else's movements.
//
// Both directions, because a filter that answers nothing passes a test that only looks
// for absence: alice reads her own two entries and none of bob's, and the standing
// balance over the same wallet agrees with them.
func TestListEntriesAnswersOneWallet(t *testing.T) {
	f := New(Local(t.TempDir()))
	ctx := context.Background()
	const org = "hanzo" // the shared signup org: strangers, one file, a wallet each

	for _, w := range []struct {
		subject string
		grant   int64
		spend   int64
	}{
		{org + "/alice", 50_000, 1_200},
		{org + "/bob", 900_000, 5_000},
	} {
		if _, err := f.Deposit(ctx, types.DepositInput{
			Org: org, Subject: w.subject, Amount: money.FromCents(w.grant), Ref: "g-" + w.subject,
		}); err != nil {
			t.Fatalf("deposit %s: %v", w.subject, err)
		}
		if err := f.RecordUsage(ctx, types.UsageInput{
			Org: org, Subject: w.subject, Amount: money.FromCents(w.spend), Model: "zen", Ref: "u-" + w.subject,
		}); err != nil {
			t.Fatalf("usage %s: %v", w.subject, err)
		}
	}

	for _, w := range []struct {
		subject string
		grant   int64
		spend   int64
	}{
		{org + "/alice", 50_000, 1_200},
		{org + "/bob", 900_000, 5_000},
	} {
		rows, err := f.ListEntries(ctx, org, w.subject, false, 0)
		if err != nil {
			t.Fatalf("ListEntries %s: %v", w.subject, err)
		}
		if len(rows) != 2 {
			t.Fatalf("%s reads %d entries, want their own grant and debit: %+v",
				w.subject, len(rows), rows)
		}
		var in, out int64
		for _, r := range rows {
			switch r.Kind {
			case KindDeposit:
				in += r.Amount.Cents()
			case KindUsage:
				out += r.Amount.Cents()
			default:
				t.Errorf("%s: entry %s classifies as nothing", w.subject, r.ID)
			}
		}
		if in != w.grant || out != w.spend {
			t.Errorf("%s reads %d¢ in / %d¢ out, want %d¢ / %d¢ — the read answered from "+
				"the file, not the wallet", w.subject, in, out, w.grant, w.spend)
		}
		// The list and the total are one account: Balance sums walletAcct(subject),
		// and these are the movements of that same wallet.
		bal, err := f.Balance(ctx, org, w.subject, "usd", false)
		if err != nil {
			t.Fatalf("balance %s: %v", w.subject, err)
		}
		if got := bal.Cents(); got != in-out {
			t.Errorf("%s: the balance says %d¢ and its own movements say %d¢",
				w.subject, got, in-out)
		}
	}

	// An empty subject is every wallet in the org — the read the revenue ingest takes.
	all, err := f.ListEntries(ctx, org, "", false, 0)
	if err != nil {
		t.Fatalf("ListEntries org-wide: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("the org-wide read holds %d entries, want all 4", len(all))
	}
}

// TestListEntriesPagesTheWalletNotTheFile — the reason the narrowing is in the QUERY
// and not in the caller.
//
// limit is a page size, and the two orders answer differently. Page the file first
// and keep what survives, and a quiet wallet behind a busy one reads back EMPTY while
// its money sits there — the customer sees no ledger at all. Page the WALLET and the
// limit means what the caller asked for.
func TestListEntriesPagesTheWalletNotTheFile(t *testing.T) {
	f := New(Local(t.TempDir()))
	ctx := context.Background()
	const org = "hanzo"

	// alice writes first, so her rows are the OLDEST in the file.
	if _, err := f.Deposit(ctx, types.DepositInput{
		Org: org, Subject: org + "/alice", Amount: money.FromCents(50_000), Ref: "g-alice",
	}); err != nil {
		t.Fatalf("deposit alice: %v", err)
	}
	for i := range 5 {
		if err := f.RecordUsage(ctx, types.UsageInput{
			Org: org, Subject: org + "/bob", Amount: money.FromCents(10), Model: "gpu",
			Ref: "u-bob-" + string(rune('a'+i)),
		}); err != nil {
			t.Fatalf("usage bob %d: %v", i, err)
		}
	}

	rows, err := f.ListEntries(ctx, org, org+"/alice", false, 2)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("alice reads %d entries behind 5 of bob's, want her own one: %+v",
			len(rows), rows)
	}
	if rows[0].Amount.Cents() != 50_000 {
		t.Errorf("alice's page holds %d¢, want her own 50000¢ grant", rows[0].Amount.Cents())
	}
}
