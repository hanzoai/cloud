package finance

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/money"
	"github.com/hanzoai/cloud/types"
)

// SumUsageSince is the usage-cap's period-spend source: the org's total metered
// usage since a cutoff, deposits excluded, sandbox books for a test org. This is what
// makes the cap enforce on the finance ledger (where the unified binary records
// usage) instead of the empty commerce transaction store.
func TestSumUsageSince_OrgTotal(t *testing.T) {
	t.Setenv("CLOUD_KMS_MASTER_KEY_REF", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	f := New(t.TempDir())
	ctx := context.Background()
	const org = "cap-org"

	// A deposit (grant) is NOT usage — it must NOT count toward the cap.
	if _, err := f.Deposit(ctx, types.DepositInput{Org: org, Subject: org, Amount: money.FromCents(50_000), Ref: "grant"}); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	// $1.50 + $0.75 of usage = 225 cents.
	if err := f.RecordUsage(ctx, types.UsageInput{Org: org, Subject: org, Amount: money.FromCents(150), Model: "m", RequestID: "u1"}); err != nil {
		t.Fatalf("usage u1: %v", err)
	}
	if err := f.RecordUsage(ctx, types.UsageInput{Org: org, Subject: org, Amount: money.FromCents(75), Model: "m", RequestID: "u2"}); err != nil {
		t.Fatalf("usage u2: %v", err)
	}

	// Since epoch → all usage, deposit excluded.
	if got, err := f.SumUsageSince(ctx, org, false, 0); err != nil || got != 225 {
		t.Fatalf("SumUsageSince = %d,%v, want 225,nil (usage only, deposit excluded)", got, err)
	}
	// Since a FUTURE instant → nothing in-window (the monthly-reset semantics: spend
	// before the window start does not count).
	if got, _ := f.SumUsageSince(ctx, org, false, time.Now().Add(time.Hour).Unix()); got != 0 {
		t.Fatalf("SumUsageSince(future cutoff) = %d, want 0 (out of window)", got)
	}
	// An org that never spent → 0, never an error (a cap can't over-count a fresh org).
	if got, err := f.SumUsageSince(ctx, "never-spent", false, 0); err != nil || got != 0 {
		t.Fatalf("SumUsageSince(unknown org) = %d,%v, want 0,nil", got, err)
	}
	// Test-mode books are separate: the live sum sees the live org only.
	if err := f.RecordUsage(ctx, types.UsageInput{Org: org, Subject: org, Amount: money.FromCents(999), Model: "m", Test: true, RequestID: "t1"}); err != nil {
		t.Fatalf("test usage: %v", err)
	}
	if got, _ := f.SumUsageSince(ctx, org, false, 0); got != 225 {
		t.Fatalf("live sum = %d, want 225 (test-mode spend must not leak into live)", got)
	}
	if got, _ := f.SumUsageSince(ctx, org, true, 0); got != 999 {
		t.Fatalf("test sum = %d, want 999 (sandbox books)", got)
	}
}

// The usage hook fires after a committed debit — the seam the cap's alert-fire rides,
// carrying org + test + scope, WITHOUT finance importing commerce.
func TestUsageHook_FiresAfterDebit(t *testing.T) {
	t.Setenv("CLOUD_KMS_MASTER_KEY_REF", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	f := New(t.TempDir())
	ctx := context.Background()

	var mu sync.Mutex
	var gotOrg string
	var gotTest bool
	fired := make(chan struct{}, 1)
	SetUsageHook(func(org string, test bool, _, _ string) {
		mu.Lock()
		gotOrg, gotTest = org, test
		mu.Unlock()
		select {
		case fired <- struct{}{}:
		default:
		}
	})
	defer SetUsageHook(nil)

	if err := f.RecordUsage(ctx, types.UsageInput{Org: "hook-org", Subject: "hook-org", Amount: money.FromCents(100), Test: true, RequestID: "h1"}); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("usage hook did not fire after a committed debit")
	}
	mu.Lock()
	defer mu.Unlock()
	if gotOrg != "hook-org" || !gotTest {
		t.Fatalf("hook got org=%q test=%v, want hook-org/true", gotOrg, gotTest)
	}
}
