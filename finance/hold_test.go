package finance

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud/money"
)

// A hold sets a payment aside when it is agreed: the wallet no longer counts it,
// nothing else can promise or spend it, and the payment made later under the
// same ref is paid from it however the wallet stands by then.
func TestAHoldIsPaidWhateverTheWalletSpentSince(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	if _, err := f.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(10_700), Ref: "g"}); err != nil {
		t.Fatal(err)
	}
	job := money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(10_000), Model: "job:j1", Ref: "x402_a"}
	id, posted, err := f.Hold(ctx, job)
	if err != nil || !posted || id == "" {
		t.Fatalf("hold = (%q, %v, %v), want a posted hold", id, posted, err)
	}
	mustBalance(t, f, "acme", "acme", 700)
	if again, posted, err := f.Hold(ctx, job); err != nil || posted || again != id {
		t.Fatalf("the same hold again = (%q, %v, %v), want the first %s and nothing moved", again, posted, err, id)
	}
	// The same balance cannot be promised twice, nor drawn by anything else.
	second := job
	second.Ref = "x402_b"
	if _, _, err := f.Hold(ctx, second); !errors.Is(err, ErrShortWallet) {
		t.Fatalf("a second hold of money already held: %v, want ErrShortWallet", err)
	}
	if _, _, err := f.Draw(ctx, money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(701), Ref: "plan:1"}); !errors.Is(err, ErrShortWallet) {
		t.Fatalf("a draw of held money: %v, want ErrShortWallet", err)
	}
	// A metered call spends only unheld funds and never touches the hold or incurs debt.
	if _, _, err := f.RecordUsageOnce(ctx, money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(1_200), Ref: "msg_1"}); err != nil {
		t.Fatal(err)
	}
	mustBalance(t, f, "acme", "acme", 0)
	paid, posted, err := f.Draw(ctx, job)
	if err != nil || !posted {
		t.Fatalf("the held payment = (%q, %v, %v), want it paid from the hold", paid, posted, err)
	}
	if back, err := f.Return(ctx, job); err != nil || !back.IsZero() {
		t.Fatalf("return after the payment = %s, %v; want nothing left to give back", back, err)
	}
	if spent, err := f.SumUsageSince(ctx, "acme", false, "", "", 0); err != nil || spent != 700 {
		t.Fatalf("spend = %d, %v; the hold or its payment counted as usage", spent, err)
	}
	rows, err := f.ListEntries(ctx, "acme", "acme", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]Kind{}
	for _, r := range rows {
		kinds[r.ID] = r.Kind
	}
	if kinds[id] != KindHold {
		t.Fatalf("entries = %+v; the hold %s must read back as a hold", rows, id)
	}
}

// A hold given up returns to the wallet once, and the ref it held under never
// holds again: the payment it set money aside for was given up.
func TestAHoldGivenUpReturnsToTheWalletOnce(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	if _, err := f.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(10_000), Ref: "g"}); err != nil {
		t.Fatal(err)
	}
	job := money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(6_000), Ref: "x402_r"}
	if _, _, err := f.Hold(ctx, job); err != nil {
		t.Fatal(err)
	}
	mustBalance(t, f, "acme", "acme", 4_000)
	if back, err := f.Return(ctx, job); err != nil || back.Cmp(money.FromCents(6_000)) != 0 {
		t.Fatalf("return = %s, %v; want the 60.00 back", back, err)
	}
	mustBalance(t, f, "acme", "acme", 10_000)
	if back, err := f.Return(ctx, job); err != nil || !back.IsZero() {
		t.Fatalf("a second return = %s, %v; want nothing", back, err)
	}
	mustBalance(t, f, "acme", "acme", 10_000)
	if _, _, err := f.Hold(ctx, job); !errors.Is(err, ErrReturned) {
		t.Fatalf("a hold again under a returned ref: %v, want ErrReturned", err)
	}
	other := job
	other.Amount = money.FromCents(1)
	if _, _, err := f.Hold(ctx, other); !errors.Is(err, ErrRefReused) {
		t.Fatalf("a hold of another amount under a held ref: %v, want ErrRefReused", err)
	}
	// With nothing held, a payment under the ref is an ordinary draw.
	if _, posted, err := f.Draw(ctx, job); err != nil || !posted {
		t.Fatalf("draw after the return: %v, %v", posted, err)
	}
	mustBalance(t, f, "acme", "acme", 4_000)
}

// A return is decided when it is asked. A hold whose caller stopped waiting for
// it and gave the payment up lands after the return: it is refused, and the
// wallet keeps its money, where a hold landing on a return of nothing used to set
// the amount aside for a payment already given up, with nothing left to return it.
func TestAHoldLandingAfterItsReturnMovesNothing(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	if _, err := f.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(10_000), Ref: "g"}); err != nil {
		t.Fatal(err)
	}
	late := money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(3_000), Ref: "x402_late"}
	if back, err := f.Return(ctx, late); err != nil || !back.IsZero() {
		t.Fatalf("a return before its hold landed = %s, %v; want nothing back", back, err)
	}
	if _, _, err := f.Hold(ctx, late); !errors.Is(err, ErrReturned) {
		t.Fatalf("a hold landing after its return: %v, want ErrReturned", err)
	}
	mustBalance(t, f, "acme", "acme", 10_000)
	if back, err := f.Return(ctx, late); err != nil || !back.IsZero() {
		t.Fatalf("a second return = %s, %v; want nothing", back, err)
	}
	// A return after the payment was made records the same: the ref holds nothing
	// again.
	paid := money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(1_000), Ref: "x402_paid"}
	if _, _, err := f.Hold(ctx, paid); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Draw(ctx, paid); err != nil {
		t.Fatal(err)
	}
	if back, err := f.Return(ctx, paid); err != nil || !back.IsZero() {
		t.Fatalf("a return after the payment = %s, %v; want nothing", back, err)
	}
	if _, _, err := f.Hold(ctx, paid); !errors.Is(err, ErrReturned) {
		t.Fatalf("a hold again after the payment and its return: %v, want ErrReturned", err)
	}
	mustBalance(t, f, "acme", "acme", 9_000)
}

// A settlement's payer half posted as metered usage — by the binary before the
// rail drew payments — is the payment a draw under the same ref makes: finishing
// that settlement charges the payer once.
func TestAPaymentIsItsUsageTwin(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	if _, err := f.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(1_000), Ref: "g"}); err != nil {
		t.Fatal(err)
	}
	half := money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(100), Ref: "x402_s"}
	used, _, err := f.RecordUsageOnce(ctx, half)
	if err != nil {
		t.Fatal(err)
	}
	id, posted, err := f.Draw(ctx, half)
	if err != nil || posted || id != used {
		t.Fatalf("draw under the usage's ref = (%q, %v, %v), want the usage %s and nothing moved", id, posted, err, used)
	}
	mustBalance(t, f, "acme", "acme", 900)
}
