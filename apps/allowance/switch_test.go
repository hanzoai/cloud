package allowance

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// switches serves the flag plane for one test with set as what an operator stored;
// every other switch is unset and reads as its caller's default.
func switches(t *testing.T, set map[string]int) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	plane.Unbind()
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	cloud.Plane().Post("/flags/value", func(_ context.Context, in *client.FlagValueIn) (*client.FlagValue, error) {
		if n, ok := set[in.Key]; ok {
			return &client.FlagValue{Value: strconv.Itoa(n), N: n, Set: true}, nil
		}
		n, _ := strconv.Atoi(in.Default)
		return &client.FlagValue{Value: in.Default, N: n}, nil
	}, zip.WithOperationID(client.FlagsValue))
	stop, err := cloud.ServePlane("flag", nil)
	if err != nil {
		t.Fatalf("serve flag plane: %v", err)
	}
	t.Cleanup(func() { _ = stop() })
}

// admitted is how many of n calls person makes on tier, a second apart from at,
// are admitted.
func admitted(t *testing.T, tier, org, person string, n int, at time.Time) int {
	t.Helper()
	s := &service{store: testStore(t), tier: func(context.Context, string, string) (string, error) { return tier, nil }}
	got := 0
	for i := range n {
		out, err := s.take(context.Background(), calls, org, person, person, at.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if !out.Spent {
			got++
		}
	}
	return got
}

var noon = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

// THE DAY IS UNSET, AND UNSET IS NO DAY. With ai_allowance_free unset, a Free person
// is held to the session alone: three in each of three sessions of one UTC day. The
// way to take a stored day back is to unset it ({"active": false}), never to store
// 0 — an image from before the session reads a stored 0 as no limit at all.
func TestAnUnsetDayIsNoDay(t *testing.T) {
	switches(t, map[string]int{})
	s := freeService(t, nil)
	got := 0
	for w := range 3 {
		for i := range int(free) + 1 {
			at := time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC).Add(time.Duration(w)*fiveHours + time.Duration(i)*time.Second)
			if out, _ := s.take(context.Background(), calls, "hanzo", "hanzo/u", "hanzo/u", at); !out.Spent {
				got++
			}
		}
	}
	if got != 3*int(free) {
		t.Fatalf("%d admitted over three sessions of one day, want %d: an unset day bound them", got, 3*free)
	}
}

// HANZO HAS NO SESSION SWITCH. hanzo is the signup org every consumer joins, so a
// session switch for it would be every consumer's: a 0 there would give each of them
// as many free calls as they like, and a figure would cap a paying subscriber. Only
// the platform org has one.
func TestHanzoHasNoSessionSwitch(t *testing.T) {
	switches(t, map[string]int{"ai_session_org_hanzo": 0})
	if got := admitted(t, "free", "hanzo", "hanzo/signup", int(free)+10, noon); got != int(free) {
		t.Fatalf("a consumer in org hanzo got %d of %d with ai_session_org_hanzo=0, want %d", got, int(free)+10, free)
	}
	switches(t, map[string]int{"ai_session_org_hanzo": 3})
	if got := admitted(t, "pro", "hanzo", "hanzo/paying", 10, noon); got != 10 {
		t.Fatalf("a pro subscriber in org hanzo got %d of 10 with ai_session_org_hanzo=3, want all", got)
	}
	for _, d := range flags.Defs() {
		if d.Key == "ai_session_org_hanzo" {
			t.Fatal("ai_session_org_hanzo is registered")
		}
	}
}

// THE PLATFORM ORG'S SESSION SWITCH is its own: 0 lifts its people's session, -1
// follows their plan.
func TestThePlatformOrgSessionSwitch(t *testing.T) {
	switches(t, map[string]int{"ai_session_org_admin": 0})
	if got := admitted(t, "free", "admin", "admin/z", int(free)+10, noon); got != int(free)+10 {
		t.Fatalf("org admin got %d of %d with its session switch at 0, want all", got, int(free)+10)
	}
	switches(t, map[string]int{"ai_session_org_admin": -1})
	if got := admitted(t, "free", "admin", "admin/z", int(free)+10, noon); got != int(free) {
		t.Fatalf("org admin got %d of %d following its plan, want %d", got, int(free)+10, free)
	}
}

// HANZO'S DAY AND HOUR SWITCHES ARE EVERY CONSUMER'S. They stay, and they read as
// what they are: a day set on org hanzo holds every consumer to it, whatever their
// session would allow — three calls across four sessions of one UTC day.
func TestHanzosDaySwitchHoldsEveryConsumer(t *testing.T) {
	switches(t, map[string]int{"ai_allowance_org_hanzo": 3})
	s := freeService(t, nil)
	got := 0
	for w := range 4 {
		for i := range 3 {
			at := time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC).Add(time.Duration(w)*fiveHours + time.Duration(i)*time.Second)
			if out, _ := s.take(context.Background(), calls, "hanzo", "hanzo/u", "hanzo/u", at); !out.Spent {
				got++
			}
		}
	}
	if got != 3 {
		t.Fatalf("ai_allowance_org_hanzo=3: %d calls in four sessions of one UTC day, want 3", got)
	}
}

// A NEGATIVE SESSION FIGURE IS NO FIGURE: the tier's shipped one, as an org's -1
// follows its tier. It never reads as unbounded.
func TestANegativeSessionFigureIsTheShippedOne(t *testing.T) {
	switches(t, map[string]int{"ai_session_free": -1})
	if got := admitted(t, "free", "hanzo", "hanzo/u", int(free)+10, noon); got != int(free) {
		t.Fatalf("ai_session_free=-1: %d of %d admitted, want %d", got, int(free)+10, free)
	}
}

// A SESSION LENGTH TOO LONG TO HOLD IS A WEEK. A typed figure past what a duration
// holds once overflowed into a window of no length, which counted nothing: forty
// calls at one instant all passed. Held to 168 hours, 50 pass and the next is
// told the week's end.
func TestAnOverlongSessionIsAWeek(t *testing.T) {
	switches(t, map[string]int{flags.PlanSessionHours: 3_000_000})
	s := freeService(t, nil)
	got := 0
	var last *client.Allowance
	for range int(free) + 10 {
		out, _ := s.take(context.Background(), calls, "hanzo", "hanzo/u", "hanzo/u", noon)
		if !out.Spent {
			got++
		}
		last = out
	}
	if got != int(free) || last.Resets != noon.Add(flags.MaxSessionHours*time.Hour).Unix() {
		t.Fatalf("ai_plan_session_hours=3000000: %d admitted, the last told %v; want %d and a week", got, time.Unix(last.Resets, 0).UTC(), free)
	}
}
