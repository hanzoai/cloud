package limits

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
)

// A key's spend is added to by each call, in its calendar month, and read back as
// integers: the month and the total across months. (A bare nanos beside excluded is
// ambiguous on PostgreSQL; the statement names the table.)
func TestAKeysSpendAddsUpAcrossCallsAndMonths(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "", 0, "")
		ctx := context.Background()
		for _, n := range []int64{100, 250, 5} {
			if err := w.l.Spend(ctx, "acme", "pk-one", n); err != nil {
				t.Fatalf("spend %d: %v", n, err)
			}
		}
		w.now = w.now.AddDate(0, 1, 0)
		if err := w.l.Spend(ctx, "acme", "pk-one", 1000); err != nil {
			t.Fatalf("spend next month: %v", err)
		}
		if err := w.l.Spend(ctx, "acme", "pk-other", 7); err != nil {
			t.Fatalf("spend another key: %v", err)
		}
		month, total, err := w.l.Spent(ctx, "acme", "pk-one")
		if err != nil || month != 1000 || total != 1355 {
			t.Fatalf("spent = %d this month, %d in all (%v), want 1000 and 1355", month, total, err)
		}
	})
}

// Writers of one payer run one after another and a payer is never made to wait for
// another: of 40 racing takes of a daily cap of 10, exactly ten are admitted, and a
// different payer's takes are all admitted beside them.
func TestConcurrentTakesAdmitExactlyTheCap(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "", 0, "")
		ctx := context.Background()
		now := w.now
		var wg sync.WaitGroup
		var mu sync.Mutex
		admitted := map[string]int{}
		for _, subject := range []string{"alice", "bob"} {
			for range 40 {
				wg.Go(func() {
					_, full, err := w.l.store.take(ctx, "acme", subject, "kai", now, 10)
					if err != nil {
						t.Errorf("take: %v", err)
						return
					}
					if !full {
						mu.Lock()
						admitted[subject]++
						mu.Unlock()
					}
				})
			}
		}
		wg.Wait()
		if admitted["alice"] != 10 || admitted["bob"] != 10 {
			t.Fatalf("admitted %v, want 10 each of a cap of 10", admitted)
		}
	})
}

// Text PostgreSQL would refuse is refused at the store on both engines, and the error
// never carries it.
func TestLimitsRefuseTextPostgresRefuses(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "", 0, "")
		ctx := context.Background()
		now := w.now
		for name, bad := range map[string]string{"NUL": "a\x00b", "not UTF-8": "a\xffb"} {
			_, _, takeErr := w.l.store.take(ctx, "acme", "alice", bad, now, 10)
			_, admitErr := w.l.store.admit(ctx, bad, "alice", "ours", now, terms{}, false, true, false)
			for what, err := range map[string]error{
				"take":  takeErr,
				"admit": admitErr,
				"know":  w.l.store.know(ctx, bad, "v"),
				"rate":  w.l.store.setRate(ctx, bad, 10, "actor", now),
				"spend": w.l.Spend(ctx, "acme", bad, 5),
			} {
				if !errors.Is(err, dialect.ErrText) {
					t.Errorf("%s with %s text: %v, want ErrText", what, name, err)
				}
				if err != nil && strings.Contains(err.Error(), bad) {
					t.Errorf("%s with %s text: the error carries the value", what, name)
				}
			}
			if _, _, err := w.l.Spent(ctx, "acme", bad); !errors.Is(err, dialect.ErrText) {
				t.Errorf("Spent with %s text: %v, want ErrText", name, err)
			}
		}
	})
}
