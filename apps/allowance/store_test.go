package allowance

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/coord/coordtest"

	"github.com/hanzoai/cloud/sdk/coord"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	return &Store{c: coord.Alone()}
}

// at is a moment on the day the store tests count in.
var at = time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

// dayOf is the daily window at now, held to limit.
func dayOf(now time.Time, limit int64) []bound {
	return []bound{{window: "day", row: "day", period: Day(now), limit: limit, resets: Midnight(now)}}
}

// A limit of N admits exactly N calls and refuses the N+1th, and the count STOPS at
// the ceiling — a customer reads "3 of 3", never "9 of 3", because refusals are not
// usage.
func TestTakeAdmitsExactlyTheLimit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for i := int64(1); i <= 3; i++ {
		used, ok := s.Take(ctx, "hanzo/z", dayOf(at, 3), at)
		if !ok {
			t.Fatalf("call %d of 3 was refused", i)
		}
		if used[0] != i {
			t.Fatalf("call %d counted %d", i, used[0])
		}
	}
	for range 5 {
		used, ok := s.Take(ctx, "hanzo/z", dayOf(at, 3), at)
		if ok {
			t.Fatal("a call past the ceiling was admitted")
		}
		if used[0] != 3 {
			t.Fatalf("the count climbed past the ceiling to %d — a refusal is not usage", used[0])
		}
	}
}

// The period IS the key, so nothing resets anything: a count from another day reads
// as zero today, for every subject at the same instant and with no job to have run.
func TestAnotherPeriodCountsAsNone(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for range 3 {
		s.Take(ctx, "hanzo/z", dayOf(at, 3), at)
	}
	if _, ok := s.Take(ctx, "hanzo/z", dayOf(at, 3), at); ok {
		t.Fatal("the subject should be out on the day they spent it")
	}
	next := at.AddDate(0, 0, 1)
	used, ok := s.Take(ctx, "hanzo/z", dayOf(next, 3), next)
	if !ok {
		t.Fatal("yesterday's count refused today's call")
	}
	if used[0] != 1 {
		t.Fatalf("the new period started at %d, want 1", used[0])
	}
}

// One subject can neither read nor spend another's allowance: the subject is in
// every key.
func TestSubjectsDoNotShare(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	s.Take(ctx, "hanzo/a", dayOf(at, 1), at)
	if _, ok := s.Take(ctx, "hanzo/a", dayOf(at, 1), at); ok {
		t.Fatal("a should be out")
	}
	if _, ok := s.Take(ctx, "hanzo/b", dayOf(at, 1), at); !ok {
		t.Fatal("b was refused for a's spending — the counts are shared")
	}
}

// TWO CALLERS CANNOT BOTH TAKE THE LAST UNIT: taking is one step, not a read
// followed by an increment that would let both see it free.
func TestTheLastUnitGoesToExactlyOneCaller(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const limit = 10
	var wg sync.WaitGroup
	admitted := make([]bool, 40)
	for i := range admitted {
		wg.Go(func() {
			_, admitted[i] = s.Take(ctx, "hanzo/z", dayOf(at, limit), at)
		})
	}
	wg.Wait()
	got := 0
	for _, ok := range admitted {
		if ok {
			got++
		}
	}
	if got != limit {
		t.Fatalf("%d callers were admitted against a limit of %d", got, limit)
	}
	if used := s.Read(ctx, "hanzo/z", dayOf(at, limit), at); used[0] != limit {
		t.Fatalf("the store recorded %d against a limit of %d", used[0], limit)
	}
}

// Asking does not spend, so a page that polls the number costs the caller nothing.
func TestReadTakesNothing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for i := range 10 {
		if used := s.Read(ctx, "hanzo/z", dayOf(at, 1), at); used[0] != 0 {
			t.Fatalf("read %d: used=%d", i, used[0])
		}
	}
	if _, ok := s.Take(ctx, "hanzo/z", dayOf(at, 1), at); !ok {
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

// TestARefusedCallBurnsNoOtherWindow is why a take asks every window before it
// charges any: a caller at their daily ceiling would otherwise burn an hour's worth
// of rate on every attempt, and the hour would never recover while they kept trying.
func TestARefusedCallBurnsNoOtherWindow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 27, 19, 30, 0, 0, time.UTC)
	both := []bound{
		{window: "hour", row: "hour", period: Hour(now), limit: 10, resets: NextHour(now)},
		{window: "day", row: "day", period: Day(now), limit: 2, resets: Midnight(now)},
	}
	for range 2 {
		if _, ok := s.Take(ctx, "hanzo/z", both, now); !ok {
			t.Fatal("seeding the day was refused")
		}
	}
	if _, ok := s.Take(ctx, "hanzo/z", both, now); ok {
		t.Fatal("the day was full, yet the call was allowed")
	}
	if used := s.Read(ctx, "hanzo/z", both, now); used[0] != 2 || used[1] != 2 {
		t.Errorf("a refused call moved the counts to %v — the hour was spent on nothing", used)
	}
}

// THE COUNT IS THE DEPLOYMENT'S. Two pods on one KV hold a person to one count, so
// spreading calls over pods buys nothing.
func TestTheCountIsOneAcrossPods(t *testing.T) {
	pod := coordtest.Memory(t)
	a, b := &Store{c: pod("cloud-0")}, &Store{c: pod("cloud-1")}
	ctx := context.Background()
	admitted := 0
	for i := range 2 * free {
		s := a
		if i%2 == 1 {
			s = b
		}
		if _, ok := s.Take(ctx, "hanzo/z", sessionOf(free), at.Add(time.Duration(i)*time.Minute)); ok {
			admitted++
		}
	}
	if admitted != int(free) {
		t.Fatalf("%d calls over two pods admitted %d against %d a session", 2*free, admitted, free)
	}
}

// A REFUSED CALL OPENS NO SESSION. With a day an admin set already spent, the call
// after the last session ended is refused on the day, and no session runs after it:
// the next one opens at the next call that is admitted.
func TestARefusedCallOpensNoSession(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	both := func(now time.Time) []bound { return append(sessionOf(3), dayOf(now, 1)...) }
	if _, ok := s.Take(ctx, "hanzo/z", both(at), at); !ok {
		t.Fatal("the first call was refused")
	}
	later := at.Add(fiveHours + time.Minute)
	bs := both(later)
	if _, ok := s.Take(ctx, "hanzo/z", bs, later); ok {
		t.Fatal("a call past the day was admitted")
	}
	if got := s.c.Spanned(ctx, quotaName, spanKey("hanzo/z", bs[0]), later, fiveHours); !got.IsZero() {
		t.Fatalf("the refused call opened a session at %v", got)
	}
}

// sessionOf is a five-hour session window held to limit, not yet placed.
func sessionOf(limit int64) []bound {
	return []bound{{window: "session", row: "session", limit: limit, span: fiveHours}}
}

// A GIVE RETURNS THE UNIT a take counted, and never takes a count below zero: the
// call it admitted was not served, so its place is open again.
func TestAGiveReturnsTheUnit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for range 3 {
		s.Take(ctx, "hanzo/z", dayOf(at, 3), at)
	}
	s.Give(ctx, "hanzo/z", dayOf(at, 3), 0)
	if used := s.Read(ctx, "hanzo/z", dayOf(at, 3), at); used[0] != 2 {
		t.Fatalf("after a give the count is %d, want 2", used[0])
	}
	if _, ok := s.Take(ctx, "hanzo/z", dayOf(at, 3), at); !ok {
		t.Fatal("the unit given back could not be taken again")
	}
	other := testStore(t)
	other.Give(ctx, "hanzo/z", dayOf(at, 3), 0)
	if used := other.Read(ctx, "hanzo/z", dayOf(at, 3), at); used[0] != 0 {
		t.Fatalf("a give with nothing taken left the count at %d", used[0])
	}
}

// A give on one pod returns the unit a take counted on another: the count is the
// deployment's, both ways.
func TestAGiveIsOneAcrossPods(t *testing.T) {
	pod := coordtest.Memory(t)
	a, b := &Store{c: pod("cloud-0")}, &Store{c: pod("cloud-1")}
	ctx := context.Background()
	a.Take(ctx, "hanzo/z", dayOf(at, 3), at)
	a.Take(ctx, "hanzo/z", dayOf(at, 3), at)
	b.Give(ctx, "hanzo/z", dayOf(at, 3), 0)
	b.Give(ctx, "hanzo/z", dayOf(at, 3), 0)
	b.Give(ctx, "hanzo/z", dayOf(at, 3), 0)
	if used := a.Read(ctx, "hanzo/z", dayOf(at, 3), at); used[0] != 0 {
		t.Fatalf("after giving back both units on another pod the count is %d, want 0", used[0])
	}
}
