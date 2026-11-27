package allowance

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/coord/coordtest"
	"github.com/hanzoai/cloud/tenant"
	"github.com/zap-proto/zip"
)

// freeService is a free-tier service over its own count, whose wallets are funded
// exactly where funded says.
func freeService(t *testing.T, funded map[string]bool) *service {
	t.Helper()
	s := &service{
		store: testStore(t),
		tier:  func(context.Context, string, string) (string, error) { return "free", nil },
	}
	if funded != nil {
		s.funded = func(_ context.Context, org, payer string) bool { return funded[org+"/"+payer] }
	}
	return s
}

// free is the Free plan's calls a session, as shipped.
var free = int64(sessions["free"])

// fiveHours is the session's length, as shipped.
const fiveHours = 5 * time.Hour

// THE OWNER'S RULE: three free calls every five hours. The first call opens the
// window at its own instant; calls two and three are admitted inside it; call four
// is refused naming the session and the exact instant five hours after call one —
// and UTC midnight, which used to turn the day, changes nothing. At that instant the
// next call opens a new window and three more are theirs.
func TestThreeCallsEveryFiveHours(t *testing.T) {
	s := freeService(t, nil)
	ctx := context.Background()
	take := func(at time.Time) *client.Allowance {
		t.Helper()
		out, err := s.take(ctx, calls, "hanzo", "hanzo/u1", "hanzo/u1", at)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	opened := time.Date(2026, 11, 2, 21, 40, 13, 600_000_000, time.UTC)
	reopens := time.Date(2026, 11, 3, 2, 40, 13, 0, time.UTC)
	for i := int64(1); i <= free; i++ {
		at := opened.Add(time.Duration(i-1) * time.Minute)
		if i == free {
			at = time.Date(2026, 11, 2, 23, 59, 59, 0, time.UTC)
		}
		out := take(at)
		if out.Spent || out.Used != i || out.Limit != free || out.Window != "session" || out.Resets != reopens.Unix() {
			t.Fatalf("call %d at %v: %+v, want admitted, %d of %d this session, resetting %v", i, at, out, i, free, reopens)
		}
	}
	for _, at := range []time.Time{time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC), reopens.Add(-time.Second)} {
		out := take(at)
		if !out.Spent || out.Used != free || out.Window != "session" || out.Resets != reopens.Unix() {
			t.Fatalf("call four at %v: %+v, want refused on the session until %v", at, out, reopens)
		}
	}
	if got, _ := s.read(ctx, calls, "hanzo", "hanzo/u1", "hanzo/u1", reopens.Add(-time.Minute)); !got.Spent || got.Used != free || got.Resets != reopens.Unix() {
		t.Fatalf("the read before the reset: %+v", got)
	}
	next := reopens.Add(fiveHours)
	for i := int64(1); i <= free; i++ {
		out := take(reopens.Add(time.Duration(i-1) * time.Minute))
		if out.Spent || out.Used != i || out.Resets != next.Unix() {
			t.Fatalf("call %d from the reset: %+v, want admitted in a window opened at %v", i, out, reopens)
		}
	}
	if out := take(reopens.Add(time.Hour)); !out.Spent || out.Window != "session" || out.Resets != next.Unix() {
		t.Fatalf("call four of the second window: %+v, want refused until %v", out, next)
	}
}

// The window opens at the call that finds none, exactly: a window that ended an
// hour ago leaves nothing behind, and the next call's reset is five hours after IT.
func TestTheWindowOpensAtTheNextCall(t *testing.T) {
	s := freeService(t, nil)
	ctx := context.Background()
	first := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	s.take(ctx, calls, "hanzo", "hanzo/u1", "hanzo/u1", first)
	idle := first.Add(fiveHours + time.Hour)
	got, _ := s.read(ctx, calls, "hanzo", "hanzo/u1", "hanzo/u1", idle)
	if got.Spent || got.Used != 0 || got.Limit != free || got.Window != "session" || got.Resets != 0 {
		t.Fatalf("the read with no window running: %+v, want all %d left and no reset", got, free)
	}
	out, _ := s.take(ctx, calls, "hanzo", "hanzo/u1", "hanzo/u1", idle)
	if out.Used != 1 || out.Resets != idle.Add(fiveHours).Unix() {
		t.Fatalf("the call after an idle hour: %+v, want the first of a window resetting %v", out, idle.Add(fiveHours))
	}
}

// A BURST CANNOT PASS THREE. Forty calls at once, from one instant and from forty
// different seconds, on one process and on two pods sharing the production script:
// exactly three are admitted, every answer names the one window that opened, and the
// store holds three in it.
func TestABurstIsHeldToThree(t *testing.T) {
	pods := coordtest.Memory(t)
	pod := func(id string) *Store { return &Store{c: pods(id)} }
	at := time.Date(2026, 11, 2, 23, 58, 0, 0, time.UTC)
	for what, tc := range map[string]struct {
		stores []*Store
		apart  time.Duration
	}{
		"one process, one instant":   {[]*Store{testStore(t)}, 0},
		"one process, forty seconds": {[]*Store{testStore(t)}, time.Second},
		"two pods, forty seconds":    {[]*Store{pod("cloud-0"), pod("cloud-1")}, time.Second},
	} {
		person := "hanzo/" + what
		var wg sync.WaitGroup
		outs := make([]*client.Allowance, int(free)+20)
		for i := range outs {
			wg.Go(func() {
				s := &service{store: tc.stores[i%len(tc.stores)], tier: func(context.Context, string, string) (string, error) { return "free", nil }}
				outs[i], _ = s.take(context.Background(), calls, "hanzo", person, person, at.Add(time.Duration(i)*tc.apart))
			})
		}
		wg.Wait()
		admitted, resets := 0, outs[0].Resets
		for i, out := range outs {
			if !out.Spent {
				admitted++
			}
			if out.Window != "session" || out.Resets != resets {
				t.Fatalf("%s: call %d answered %+v; call 0 reset %d — two windows", what, i, out, resets)
			}
		}
		if admitted != int(free) {
			t.Fatalf("%s: %d of forty admitted, want %d", what, admitted, free)
		}
		s := &service{store: tc.stores[0], tier: func(context.Context, string, string) (string, error) { return "free", nil }}
		if got, _ := s.read(context.Background(), calls, "hanzo", person, person, at.Add(time.Minute)); got.Used != free || !got.Spent || got.Resets != resets {
			t.Fatalf("%s: the window holds %+v, want %d used resetting %d", what, got, free, resets)
		}
	}
}

// A CALL NOT SERVED GIVES ITS UNIT BACK to the session its take answered (Start),
// and to no other: a unit taken in the last second of a session that has ended,
// given back after the next one opened, leaves the next one its three.
func TestAGiveReturnsTheUnitToItsOwnSession(t *testing.T) {
	s := freeService(t, nil)
	ctx := context.Background()
	open := time.Now().UTC().Truncate(time.Minute).Add(-fiveHours)
	first, _ := s.take(ctx, calls, "hanzo", "hanzo/u1", "hanzo/u1", open)
	if first.Start != open.Unix() {
		t.Fatalf("the take answered session %d, want the one it opened at %d", first.Start, open.Unix())
	}
	end := open.Add(fiveHours)
	late, _ := s.take(ctx, calls, "hanzo", "hanzo/u1", "hanzo/u1", end.Add(-500*time.Millisecond))
	if late.Spent || late.Start != open.Unix() {
		t.Fatalf("the last-second call: %+v, want admitted in the first session", late)
	}
	next, _ := s.take(ctx, calls, "hanzo", "hanzo/u1", "hanzo/u1", end)
	if next.Spent || next.Start != end.Unix() || next.Used != 1 {
		t.Fatalf("the call at the end: %+v, want the first of a session opened then", next)
	}
	// The last-second call fails slowly and gives its unit back, after the next opened.
	s.give(ctx, calls, "hanzo", "hanzo/u1", "hanzo/u1", end.Add(-time.Second), late.Start)
	admitted := 1
	for i := range int(free) + 2 {
		if out, _ := s.take(ctx, calls, "hanzo", "hanzo/u1", "hanzo/u1", end.Add(time.Duration(i+1)*time.Minute)); !out.Spent {
			admitted++
		}
	}
	if admitted != int(free) {
		t.Fatalf("the next session admitted %d, want %d: a give from the ended one reached it", admitted, free)
	}
	// The call that opened the next one gives back to it.
	if got, _ := s.give(ctx, calls, "hanzo", "hanzo/u1", "hanzo/u1", end, next.Start); got.Used != free-1 {
		t.Fatalf("after the opener's give: %+v, want %d used", got, free-1)
	}
	// A give that names no session gives nothing back to one.
	if got, _ := s.give(ctx, calls, "hanzo", "hanzo/u1", "hanzo/u1", end, 0); got.Used != free-1 {
		t.Fatalf("a give naming no session moved it to %d used", got.Used)
	}
}

// A COMMERCE BLIP OPENS NO SECOND COUNT. A Free person whose plan cannot be read is
// held to the floor on the SAME session they are spending, so the blip gives them
// nothing: three in the session, before, during and after it together.
func TestACommerceBlipKeepsTheSession(t *testing.T) {
	down := false
	s := &service{store: testStore(t), tier: func(context.Context, string, string) (string, error) {
		if down {
			return "", errors.New("commerce: deadline exceeded")
		}
		return "free", nil
	}}
	ctx := context.Background()
	at := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	admitted := 0
	for i := range int(free) - 1 {
		out, err := s.take(ctx, calls, "hanzo", "hanzo/u", "hanzo/u", at.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if !out.Spent {
			admitted++
		}
	}
	for i, blip := range []bool{true, true, true, false, false} {
		down = blip
		out, err := s.take(ctx, calls, "hanzo", "hanzo/u", "hanzo/u", at.Add(time.Duration(i+1)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if !out.Spent {
			admitted++
		}
		if out.Window != "session" || out.Resets != at.Add(fiveHours).Unix() {
			t.Fatalf("call %d (blip %v): %+v, want the one session opened at %v", i, blip, out, at)
		}
	}
	if admitted != int(free) {
		t.Fatalf("%d admitted across a commerce blip in one session, want %d", admitted, free)
	}
}

// A KV THAT CANNOT ANSWER, ON A DEPLOYMENT WHOSE SHARE ROUNDS TO NONE, refuses a
// person's first call honestly: no session opened, so it names none and no five-hour
// wait — it says to ask again in a minute (outage), which zen sends as Retry-After.
func TestAnOutageRefusalSaysAskAgainSoon(t *testing.T) {
	c := coordtest.Down(coord.Config{Deployment: "test", Self: coord.Member{ID: "p"}, Declared: int(free) + 10})
	s := &service{store: &Store{c: c}, tier: func(context.Context, string, string) (string, error) { return "free", nil }}
	at := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	out, err := s.take(context.Background(), calls, "hanzo", "hanzo/new", "hanzo/new", at)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Spent || out.Resets != at.Add(outage).Unix() || out.Start != 0 {
		t.Fatalf("the first call during an outage: %+v, want refused, asking again at %v, in no session", out, at.Add(outage))
	}
}

// A VISITOR KEEPS THE FLOOR. The public lane has no session: three a UTC day, the
// fourth refused naming the day and the next UTC midnight, as before.
func TestAVisitorKeepsThreeADay(t *testing.T) {
	s := &service{store: testStore(t)}
	ctx := context.Background()
	at := time.Date(2026, 11, 2, 21, 0, 0, 0, time.UTC)
	for i := int64(1); i <= strangers; i++ {
		out, _ := s.take(ctx, calls, tenant.Public, "v-1", "v-1", at)
		if out.Spent || out.Used != i || out.Window != "day" || out.Limit != strangers {
			t.Fatalf("visitor call %d: %+v, want admitted on the day", i, out)
		}
	}
	out, _ := s.take(ctx, calls, tenant.Public, "v-1", "v-1", at.Add(fiveHours/2))
	if !out.Spent || out.Window != "day" || out.Resets != Midnight(at).Unix() {
		t.Fatalf("visitor call four: %+v, want refused until 00:00 UTC", out)
	}
	if out, _ := s.take(ctx, calls, tenant.Public, "v-1", "v-1", Midnight(at)); out.Spent || out.Used != 1 {
		t.Fatalf("the visitor at 00:00 UTC: %+v, want the first of a new day", out)
	}
}

// EACH PERSON IS COUNTED ALONE. Every consumer signup shares one org and many share
// its pool, so the count is the person's: a second person in the same org, paid for
// by the same wallet, is untouched by the first one's three, and so is a person in
// another org.
func TestEachPersonIsCountedAlone(t *testing.T) {
	s := freeService(t, nil)
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	for range free + 5 {
		s.take(ctx, calls, "hanzo", "hanzo", "hanzo/u1", now)
	}
	for _, who := range []struct{ org, payer, person string }{
		{"hanzo", "hanzo", "hanzo/u2"},
		{"acme", "acme", "acme/u1"},
	} {
		out, _ := s.take(ctx, calls, who.org, who.payer, who.person, now)
		if out.Spent || out.Used != 1 {
			t.Fatalf("%s after %s's three: %+v, want their own first call", who.person, "hanzo/u1", out)
		}
	}
}

// A WALLET THAT HOLDS CREDIT IS PAYING. Its free calls are not counted, whatever its
// tier, so a person who bought credit is never stopped at three; the same person on
// an empty wallet is.
func TestAFundedWalletIsNotCounted(t *testing.T) {
	s := freeService(t, map[string]bool{"hanzo/funded": true})
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	for range 2 * free {
		out, _ := s.take(ctx, calls, "hanzo", "funded", "hanzo/u1", now)
		if out.Spent || out.Limit != 0 {
			t.Fatalf("a funded wallet's person was bounded: %+v", out)
		}
	}
	if got, _ := s.read(ctx, calls, "hanzo", "empty", "hanzo/u1", now); got.Used != 0 || got.Resets != 0 {
		t.Fatalf("a funded wallet's calls were counted, or opened a window: %+v", got)
	}
	for range free {
		s.take(ctx, calls, "hanzo", "empty", "hanzo/u1", now)
	}
	if out, _ := s.take(ctx, calls, "hanzo", "empty", "hanzo/u1", now); !out.Spent {
		t.Fatal("an empty wallet's person was not stopped at three")
	}
}

// THE FREE LEASE NEVER ASKS THE BALANCE. It is the lane of a wallet whose credit
// cannot cover a lease, so credit cannot lift its bound.
func TestCreditDoesNotLiftTheLeaseBound(t *testing.T) {
	s := freeService(t, map[string]bool{"acme/acme": true})
	if _, hourly, _, daily := s.limits(context.Background(), leases, "acme", "acme"); hourly <= 0 || daily <= 0 {
		t.Fatalf("a funded wallet leases free compute without bound (%d an hour, %d a day)", hourly, daily)
	}
}

// WITH THE KV DOWN THE COUNT FALLS BACK, IT DOES NOT FAIL OPEN. One pod counts
// exactly in its own process; each of two pods admits its share, so the pods
// together admit no more than three.
func TestAKVDownHoldsEachPodToItsShare(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		declared int
		want     int
	}{{1, int(free)}, {2, int(free) / 2}} {
		c := coordtest.Down(coord.Config{Deployment: "test", Self: coord.Member{ID: "p"}, Declared: tc.declared})
		s := &service{store: &Store{c: c}, tier: func(context.Context, string, string) (string, error) { return "free", nil }}
		admitted := 0
		for range int(free) + 10 {
			out, err := s.take(ctx, calls, "hanzo", "hanzo/u1", "hanzo/u1", now)
			if err != nil {
				t.Fatal(err)
			}
			if !out.Spent {
				admitted++
			}
		}
		if admitted != tc.want {
			t.Fatalf("%d pods, KV unreachable: this pod admitted %d, want %d", tc.declared, admitted, tc.want)
		}
	}
}

// The plane names the person and the wallet apart: the count is the subject's, the
// ceiling the payer's, and a call that names no payer is its subject's own.
func TestThePlaneNamesPersonAndPayer(t *testing.T) {
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "hanzo"})
	mounted = &service{}
	t.Cleanup(func() { mounted = nil })
	_, org, payer, subject, err := scope(ctx, &client.AllowanceIn{Subject: "hanzo/u1", Payer: "hanzo"})
	if err != nil || org != "hanzo" || payer != "hanzo" || subject != "hanzo/u1" {
		t.Fatalf("scope = %q %q %q %v", org, payer, subject, err)
	}
	if _, _, payer, _, _ := scope(ctx, &client.AllowanceIn{Subject: "acme"}); payer != "acme" {
		t.Fatalf("no payer named: payer %q, want the subject", payer)
	}
}

// Person is the IAM home org and username (the user id without one): a key counts
// against the user it was minted for, a machine acting for a person is that person,
// and a service with no user is its org.
func TestPersonIsHomeOrgAndUser(t *testing.T) {
	for want, who := range map[string]zip.Caller{
		"hanzo/ann":  {Owner: "hanzo", Org: "hanzo", User: "0b6f", Name: "ann"},
		"hanzo/0b6f": {Owner: "hanzo", Org: "acme", User: "0b6f"},
		"acme/0b6f":  {Org: "acme", User: "0b6f"},
		"acme/ann":   {Owner: "hanzo", Org: "hanzo", User: "acme/ann"},
		"acme":       {Org: "acme"},
		"":           {},
	} {
		if got := client.Person(who); got != want {
			t.Errorf("Person(%+v) = %q, want %q", who, got, want)
		}
	}
}
