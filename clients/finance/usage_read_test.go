package finance

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/clients/money"
	"github.com/hanzoai/cloud/types"
)

// TestListUsage proves the co-resident usage read returns the SAME debits
// RecordUsage wrote (magnitude + model), most-recent-first, and excludes deposits —
// so /v1/billing/usage can answer from the ledger instead of self-dispatching.
func TestListUsage(t *testing.T) {
	t.Setenv("CLOUD_KMS_MASTER_KEY_REF", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	f := New(t.TempDir())
	ctx := context.Background()
	const org = "acme"

	// A grant (deposit) must NOT appear as usage.
	if _, err := f.Deposit(ctx, types.DepositInput{Org: org, Subject: org, Amount: money.FromCents(10_000), Ref: "grant1"}); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	// Two usage debits.
	if err := f.RecordUsage(ctx, types.UsageInput{Org: org, Subject: org, Amount: money.FromCents(150), Model: "gpt-x", RequestID: "r1"}); err != nil {
		t.Fatalf("usage r1: %v", err)
	}
	if err := f.RecordUsage(ctx, types.UsageInput{Org: org, Subject: org, Amount: money.FromCents(75), Model: "embed-y", RequestID: "r2"}); err != nil {
		t.Fatalf("usage r2: %v", err)
	}

	rows, err := f.ListUsage(ctx, org, 100)
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
	if err := f.RecordUsage(ctx, types.UsageInput{Org: org, Subject: org, Amount: money.FromCents(150), Model: "gpt-x", RequestID: "r1"}); err != nil {
		t.Fatalf("usage r1 replay: %v", err)
	}
	rows2, err := f.ListUsage(ctx, org, 100)
	if err != nil {
		t.Fatalf("ListUsage after replay: %v", err)
	}
	if len(rows2) != 2 {
		t.Errorf("idempotent replay must not add a row; want 2, got %d", len(rows2))
	}

	// An org with no file yet reads empty, not an error.
	empty, err := f.ListUsage(ctx, "neverused", 100)
	if err != nil {
		t.Fatalf("ListUsage empty org: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("want 0 rows for unused org, got %d", len(empty))
	}
}
