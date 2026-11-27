package affiliate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(t.TempDir(), testMaster)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// approved enrolls org and approves it, answering the affiliate.
func approved(t *testing.T, s *Store, org string) Affiliate {
	t.Helper()
	ctx := context.Background()
	a, _, err := s.Apply(ctx, "aff_"+org, org, "", "", 1500)
	if err != nil {
		t.Fatalf("apply %s: %v", org, err)
	}
	a, err = s.Approve(ctx, a.ID, "", 1)
	if err != nil {
		t.Fatalf("approve %s: %v", org, err)
	}
	return a
}

// Payouts reserve against what is owed with one guarded UPDATE, so of any number of
// payers racing for the last of it no more is paid than was accrued, on any number of
// connections. 40 racers each ask for 100 of 1,000 owed: exactly ten are paid.
func TestConcurrentPayoutsNeverPayMoreThanAccrued(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		a := approved(t, s, "acme")
		if moved, err := s.Accrue(ctx, "acc1", a.ID, "beta", "2026-11", 1, 10000, 2000, 1000, 5); err != nil || !moved {
			t.Fatalf("accrue: %v %v", moved, err)
		}
		const racers = 40
		var wg sync.WaitGroup
		var mu sync.Mutex
		paid := 0
		for i := range racers {
			wg.Go(func() {
				_, err := s.RecordPayout(ctx, fmt.Sprintf("pay%02d", i), a.ID, 100, "wire", "ref", 9)
				switch {
				case err == nil:
					mu.Lock()
					paid++
					mu.Unlock()
				case !errors.Is(err, errInsufficientPending):
					t.Errorf("payout %d: %v", i, err)
				}
			})
		}
		wg.Wait()
		if paid != 10 {
			t.Fatalf("%d payouts of 100 were paid out of 1,000 owed, want 10", paid)
		}
		got, err := s.GetByID(ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.PaidCents != 1000 || got.PendingCents() != 0 {
			t.Fatalf("paid %d pending %d, want 1000 and 0", got.PaidCents, got.PendingCents())
		}
		rows, err := s.ListPayouts(ctx, a.ID, 100)
		if err != nil || len(rows) != 10 {
			t.Fatalf("payout rows: %d %v, want 10", len(rows), err)
		}
	})
}

// An accrual is created once and topped up toward the period's final share: of many
// sweeps reading increasing month-to-date spend, concurrently, the balance ends at
// the largest share and never above it, and it only ever rose.
func TestConcurrentAccrualsConvergeOnTheLargestShare(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		a := approved(t, s, "acme")
		const sweeps = 30
		var wg sync.WaitGroup
		for i := 1; i <= sweeps; i++ {
			wg.Go(func() {
				commission := int64(i) * 10
				if _, err := s.Accrue(ctx, fmt.Sprintf("acc%02d", i), a.ID, "beta", "2026-11", 1, commission*10, commission*2, commission, 5); err != nil {
					t.Errorf("accrue %d: %v", i, err)
				}
			})
		}
		wg.Wait()
		got, err := s.GetByID(ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.AccruedCents != sweeps*10 {
			t.Fatalf("accrued %d, want %d (the largest share, once)", got.AccruedCents, sweeps*10)
		}
		rows, err := s.AccrualsForSource(ctx, "beta")
		if err != nil || len(rows) != 1 || rows[0].CommissionCents != sweeps*10 {
			t.Fatalf("accrual rows: %+v %v, want one at %d", rows, err, sweeps*10)
		}
	})
}

// Text PostgreSQL would refuse is refused at the store on both engines, and the error
// never carries the value. A click code that is not text names no link and does not
// fail the batch of those that do.
func TestStoreRefusesTextPostgresRefuses(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		a := approved(t, s, "acme")
		for name, bad := range map[string]string{"NUL": "a\x00b", "not UTF-8": "a\xffb"} {
			_, _, applyErr := s.Apply(ctx, "x", "globex", bad, "", 1500)
			_, setErr := s.SetNote(ctx, a.ID, bad)
			_, handleErr := s.SetHandle(ctx, a.ID, bad)
			_, _, attrErr := s.Attribute(ctx, "r", a.ID, bad, "acme", "acme")
			_, linkErr := s.CreateLink(ctx, "l", a.ID, "spring", bad, 1)
			_, payErr := s.RecordPayout(ctx, "p", a.ID, 1, "wire", bad, 1)
			for what, err := range map[string]error{"Apply": applyErr, "SetNote": setErr, "SetHandle": handleErr,
				"Attribute": attrErr, "CreateLink": linkErr, "RecordPayout": payErr} {
				if !errors.Is(err, dialect.ErrText) {
					t.Errorf("%s with %s text: %v, want ErrText", what, name, err)
				}
				if err != nil && strings.Contains(err.Error(), bad) {
					t.Errorf("%s with %s text: the error carries the value", what, name)
				}
			}
		}
		if err := s.EnsureLink(ctx, "l1", a.ID, "spring", "Spring", 1); err != nil {
			t.Fatalf("ensure link: %v", err)
		}
		if err := s.FlushClicks(ctx, map[string]int64{"spring": 3, "no\x00pe": 2, "bad\xffcode": 1}); err != nil {
			t.Fatalf("a click code that is not text failed the batch: %v", err)
		}
		links, err := s.ListLinks(ctx, a.ID, 10)
		if err != nil || len(links) != 1 || links[0].Clicks != 3 {
			t.Fatalf("links after the flush: %+v %v, want one with 3 clicks", links, err)
		}
	})
}
