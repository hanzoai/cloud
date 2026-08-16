package allowance

import (
	"context"
	"sync"
	"testing"
	"time"

	// devmaster keys this test binary: cek opens nothing without a master and a
	// test process has no KMS.
	_ "github.com/hanzoai/cloud/internal/devmaster"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// A limit of N admits exactly N calls and refuses the N+1th, and the count STOPS at
// the ceiling — a customer reads "3 of 3", never "9 of 3", because refusals are not
// usage.
func TestTakeAdmitsExactlyTheLimit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const day = "2026-08-15"

	for i := int64(1); i <= 3; i++ {
		used, spent, err := s.Take(ctx, "hanzo/z", day, 3)
		if err != nil {
			t.Fatalf("take %d: %v", i, err)
		}
		if spent {
			t.Fatalf("call %d of 3 was refused", i)
		}
		if used != i {
			t.Fatalf("call %d counted %d", i, used)
		}
	}
	for i := 0; i < 5; i++ {
		used, spent, err := s.Take(ctx, "hanzo/z", day, 3)
		if err != nil {
			t.Fatalf("take past the ceiling: %v", err)
		}
		if !spent {
			t.Fatal("a call past the ceiling was admitted")
		}
		if used != 3 {
			t.Fatalf("the count climbed past the ceiling to %d — a refusal is not usage", used)
		}
	}
}

// The period IS the row, so nothing resets anything: a count from another day reads
// as zero today, for every subject at the same instant and with no job to have run.
func TestAnotherPeriodCountsAsNone(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, _, err := s.Take(ctx, "hanzo/z", "2026-08-15", 3); err != nil {
			t.Fatalf("take: %v", err)
		}
	}
	if _, spent, _ := s.Take(ctx, "hanzo/z", "2026-08-15", 3); !spent {
		t.Fatal("the subject should be out on the day they spent it")
	}

	used, spent, err := s.Take(ctx, "hanzo/z", "2026-08-16", 3)
	if err != nil {
		t.Fatalf("take on the next day: %v", err)
	}
	if spent {
		t.Fatal("yesterday's count refused today's call")
	}
	if used != 1 {
		t.Fatalf("the new period started at %d, want 1", used)
	}
}

// One subject can neither read nor spend another's allowance: the subject is the
// primary key and every statement binds it.
func TestSubjectsDoNotShare(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const day = "2026-08-15"

	if _, _, err := s.Take(ctx, "hanzo/a", day, 1); err != nil {
		t.Fatalf("take: %v", err)
	}
	if _, spent, _ := s.Take(ctx, "hanzo/a", day, 1); !spent {
		t.Fatal("a should be out")
	}
	if _, spent, _ := s.Take(ctx, "hanzo/b", day, 1); spent {
		t.Fatal("b was refused for a's spending — the buckets are shared")
	}
}

// TWO CALLERS CANNOT BOTH TAKE THE LAST UNIT. This is the whole reason taking is one
// statement inside one transaction: a read followed by a separate increment would
// let both see it free.
func TestTheLastUnitGoesToExactlyOneCaller(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const day, limit = "2026-08-15", int64(10)

	var wg sync.WaitGroup
	admitted := make([]bool, 40)
	for i := range admitted {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, spent, err := s.Take(ctx, "hanzo/z", day, limit)
			admitted[i] = err == nil && !spent
		}(i)
	}
	wg.Wait()

	got := 0
	for _, ok := range admitted {
		if ok {
			got++
		}
	}
	if int64(got) != limit {
		t.Fatalf("%d callers were admitted against a limit of %d", got, limit)
	}
	used, err := s.Read(ctx, "hanzo/z", day)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if used != limit {
		t.Fatalf("the store recorded %d against a limit of %d", used, limit)
	}
}

// A plan with no ceiling has no state to keep: nothing is counted and nothing is
// written, so an unbounded caller costs this store no rows at all.
func TestNoLimitCountsNothing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		used, spent, err := s.Take(ctx, "hanzo/z", "2026-08-15", 0)
		if err != nil {
			t.Fatalf("take: %v", err)
		}
		if spent || used != 0 {
			t.Fatalf("an unbounded caller was counted: used=%d spent=%v", used, spent)
		}
	}
	if used, _ := s.Read(ctx, "hanzo/z", "2026-08-15"); used != 0 {
		t.Fatalf("an unbounded caller left %d behind", used)
	}
}

// Asking does not spend, so a page that polls the number costs the caller nothing.
func TestReadTakesNothing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const day = "2026-08-15"

	for i := 0; i < 10; i++ {
		if used, err := s.Read(ctx, "hanzo/z", day); err != nil || used != 0 {
			t.Fatalf("read %d: used=%d err=%v", i, used, err)
		}
	}
	if _, spent, _ := s.Take(ctx, "hanzo/z", day, 1); spent {
		t.Fatal("reads consumed the allowance")
	}
}

// The period is the UTC day and the reset is the next UTC midnight — one rule, one
// timezone, so a traveller cannot take two days' worth in one afternoon.
func TestPeriodIsTheUTCDay(t *testing.T) {
	late := time.Date(2026, 8, 15, 23, 59, 59, 0, time.UTC)
	if got := Day(late); got != "2026-08-15" {
		t.Fatalf("Day = %q, want 2026-08-15", got)
	}
	// The same instant, named in a zone that has already turned over.
	east := late.In(time.FixedZone("east", 10*3600))
	if got := Day(east); got != "2026-08-15" {
		t.Fatalf("Day of the same instant in another zone = %q — the period moved with the caller", got)
	}
	want := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	if got := Midnight(late); !got.Equal(want) {
		t.Fatalf("Midnight = %v, want %v", got, want)
	}
	if got := Midnight(time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)); !got.Equal(want) {
		t.Fatalf("Midnight at the start of the day = %v, want %v", got, want)
	}
}
