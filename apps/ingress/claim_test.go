package ingress

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// TestRacingClaimsOnOneHostAreToldApart: sixteen orgs claim one host at once, from
// as many goroutines. The claim is asked and written in one transaction under the
// store's write lock, so exactly one wins and every other is told the host is
// taken, on either engine and however many replicas they come from, where an
// engine-worded constraint error could not tell a lost claim from a failure.
func TestRacingClaimsOnOneHostAreToldApart(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, err := openStore(t.TempDir())
		if err != nil {
			t.Fatalf("openStore: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		ctx := context.Background()

		const claimants = 16
		results := make([]error, claimants)
		var wg sync.WaitGroup
		for i := range claimants {
			wg.Go(func() {
				org := fmt.Sprintf("org%02d", i)
				results[i] = st.Put(ctx, org, KindRoute, "r1", `{"id":"r1"}`, "race.test", 1)
			})
		}
		wg.Wait()

		won := 0
		for i, err := range results {
			switch {
			case err == nil:
				won++
			case !errors.Is(err, ErrHostTaken):
				t.Errorf("claimant %d failed instead of being told the host is taken: %v", i, err)
			}
		}
		if won != 1 {
			t.Fatalf("%d claimants hold the host, want exactly 1", won)
		}

		// The winner may rewrite its own route, and a host nobody holds is free.
		for i, err := range results {
			if err != nil {
				continue
			}
			org := fmt.Sprintf("org%02d", i)
			if err := st.Put(ctx, org, KindRoute, "r1", `{"id":"r1","v":2}`, "race.test", 2); err != nil {
				t.Fatalf("the holder could not rewrite its own route: %v", err)
			}
			if err := st.Put(ctx, org, KindRoute, "r1", `{"id":"r1"}`, "elsewhere.test", 3); err != nil {
				t.Fatalf("the holder could not move its route: %v", err)
			}
		}
		if err := st.Put(ctx, "org99", KindRoute, "r9", `{"id":"r9"}`, "race.test", 4); err != nil {
			t.Fatalf("a released host was not free: %v", err)
		}
	})
}
