package finance

import (
	"context"
	"fmt"
	"testing"

	"github.com/hanzoai/cloud/clients/money"
	"github.com/hanzoai/cloud/types"
)

// BenchmarkListUsage measures the co-resident usage read that replaced the
// commerceinproc self-dispatch (BUG 2): a single per-org SQLite query over the
// finance ledger. Seeds N usage debits, then reads them back — the exact path
// GET /v1/billing/usage now takes co-resident.
func BenchmarkListUsage(b *testing.B) {
	for _, n := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprintf("entries=%d", n), func(b *testing.B) {
			b.Setenv("CLOUD_KMS_MASTER_KEY_REF", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
			f := New(b.TempDir())
			ctx := context.Background()
			for i := 0; i < n; i++ {
				if err := f.RecordUsage(ctx, types.UsageInput{
					Org: "acme", Subject: "acme",
					Amount: money.FromCents(int64(i + 1)), Model: "zen-1", RequestID: fmt.Sprintf("r%d", i),
				}); err != nil {
					b.Fatalf("seed: %v", err)
				}
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				rows, err := f.ListUsage(ctx, "acme", n+1)
				if err != nil {
					b.Fatalf("ListUsage: %v", err)
				}
				if len(rows) != n {
					b.Fatalf("want %d rows, got %d", n, len(rows))
				}
			}
		})
	}
}
