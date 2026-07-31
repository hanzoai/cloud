package finance

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/apps/money"
	"github.com/hanzoai/cloud/types"
)

// TestKind_SurvivesTheWire pins the ONE property the peer path depends on: a kind the
// ledger writes, rendered as text for the internal plane and parsed back, is the same
// kind. Nothing else on that path is checkable at compile time — the wire is a string —
// so this is where a kind that stops round-tripping is caught.
func TestKind_SurvivesTheWire(t *testing.T) {
	for _, k := range []Kind{KindDeposit, KindUsage} {
		if got := ParseKind(string(k)); got != k {
			t.Errorf("%q did not survive the wire: parsed back as %q", k, got)
		}
	}
	// A kind this ledger never wrote is UNKNOWN, never quietly one of the two. A reader
	// that guessed would count a posting it cannot classify as somebody's spend.
	for _, s := range []string{"deposit", "withdraw", "credit", ""} {
		if got := ParseKind(s); got != KindUnknown {
			t.Errorf("%q is not this ledger's vocabulary but parsed as %q", s, got)
		}
	}
}

// TestKind_WrittenKindsReadBack closes the loop the wire test cannot: what the ledger
// actually PERSISTS must parse into the vocabulary readers classify on. A writer that
// starts stamping a kind no reader recognizes is exactly how a customer's credits page
// came to render empty against a wallet full of grants — and it fails nothing, anywhere,
// unless something asserts it here.
func TestKind_WrittenKindsReadBack(t *testing.T) {
	fin := New(t.TempDir())
	ctx := context.Background()
	if _, err := fin.Deposit(ctx, types.DepositInput{
		Org: "acme", Subject: "acme", Amount: money.FromCents(500), Currency: "usd", Ref: "d1",
	}); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	if err := fin.RecordUsage(ctx, types.UsageInput{
		Org: "acme", Subject: "acme", Amount: money.FromCents(120), Currency: "usd", RequestID: "u1",
	}); err != nil {
		t.Fatalf("record usage: %v", err)
	}

	rows, err := fin.ListEntries(ctx, "acme", 0)
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want both postings, got %d", len(rows))
	}
	seen := map[Kind]bool{}
	for _, r := range rows {
		if r.Kind == KindUnknown {
			t.Errorf("entry %s persisted a kind no reader classifies: %+v", r.ID, r)
		}
		seen[r.Kind] = true
	}
	if !seen[KindDeposit] || !seen[KindUsage] {
		t.Errorf("both directions must read back: %v", seen)
	}
}

// TestRecordUsageOnce_AnswersTheLedgersOwnKey — the debit's idempotency, ANSWERED. The
// GPU charge tells a replay from a first charge by this flag alone, and it comes from
// inside the insert's own transaction, so two concurrent replays cannot both act.
func TestRecordUsageOnce_AnswersTheLedgersOwnKey(t *testing.T) {
	fin := New(t.TempDir())
	ctx := context.Background()
	if _, err := fin.Deposit(ctx, types.DepositInput{
		Org: "acme", Subject: "acme", Amount: money.FromCents(50_000), Currency: "usd", Ref: "float",
	}); err != nil {
		t.Fatalf("fund: %v", err)
	}
	in := types.UsageInput{
		Org: "acme", Subject: "acme", Amount: money.FromCents(24_000), Currency: "usd",
		RequestID: "launch-abc",
	}

	id, posted, err := fin.RecordUsageOnce(ctx, in)
	if err != nil || !posted || id == "" {
		t.Fatalf("first debit: id=%q posted=%v err=%v", id, posted, err)
	}
	again, posted2, err := fin.RecordUsageOnce(ctx, in)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if posted2 {
		t.Error("a replay of the same RequestID must not post a second time")
	}
	if again != id {
		t.Errorf("a replay must answer the ORIGINAL entry: want %q, got %q", id, again)
	}
	bal, err := fin.Balance(ctx, "acme", "acme", "usd", false)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if bal.Cents() != 26_000 {
		t.Errorf("the wallet moved twice: want 26000 cents left, got %d", bal.Cents())
	}
}
