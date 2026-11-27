package allowance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/client"
	commercepeer "github.com/hanzoai/cloud/client/commerce"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/cloud/tenant"
	"github.com/zap-proto/zip"
)

// tiered builds a service whose tier reader always answers name/err, with NO store:
// the cases here that resolve to an unbounded caller must never reach the store, and
// a nil pointer would be the proof that one did.
func tiered(name string, err error) *service {
	return &service{tier: func(context.Context, string, string) (string, error) { return name, err }}
}

// A PAID CALLER IS UNBOUNDED. Their spend is bounded by their wallet and their
// rolling cap; counting their free-model calls as well would refuse work they have
// already paid for. So the ceiling exists exactly where the money does not — and it
// is said only about a tier commerce actually named.
func TestOnlyANamedPaidTierIsUnbounded(t *testing.T) {
	for _, name := range []string{"starter", "pro", "enterprise"} {
		s := tiered(name, nil)
		if _, hourly, session, daily := s.limits(context.Background(), calls, "hanzo/z", "hanzo"); hourly != 0 || session != 0 || daily != 0 {
			t.Errorf("tier %q is bounded at %d/%d/%d — a paying caller's free calls must not be counted", name, hourly, session, daily)
		}
	}
	s := tiered("free", nil)
	tier, hourly, session, daily := s.limits(context.Background(), calls, "hanzo/z", "hanzo")
	if tier != "free" {
		t.Errorf("tier = %q, want free", tier)
	}
	if session != free || hourly != 0 || daily != 0 {
		t.Errorf("free is bounded at %d a session, %d an hour, %d a day; want the seeded %d a session alone", session, hourly, daily, free)
	}
}

// A NAMED CALLER IS HELD TO THE PLAN COMMERCE NAMES. Commerce runs in another
// process, so the mounted service asks commerce's tier op over the plane. The stub
// answers that op the way commerce does, with the org taken from the caller.
func TestANamedCallerGetsThePlanCommerceNames(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	plane.Unbind()
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	mount(t)

	plans := map[string]string{"acme/free": "free", "acme/pro": "pro"}
	cloud.Plane().Post("/billing/tier",
		func(ctx context.Context, in *client.TierIn) (*client.Tier, error) {
			if cloud.Who(ctx).Org != "acme" {
				return nil, zip.ErrForbidden("tier: no org on the call")
			}
			return &client.Tier{User: in.Subject, Tier: client.TierLimits{Name: plans[in.Subject]}}, nil
		},
		zip.WithOperationID(client.BillingTier))
	stop, err := cloud.ServePlane(commercepeer.App, nil)
	if err != nil {
		t.Fatalf("serve commerce: %v", err)
	}
	t.Cleanup(func() { _ = stop() })

	for subject, want := range map[string]int64{"acme/free": free, "acme/pro": 0} {
		tier, _, session, _ := mounted.limits(context.Background(), calls, subject, "acme")
		if tier != plans[subject] || session != want {
			t.Errorf("%s: tier %q at %d a session, want %q at %d", subject, tier, session, plans[subject], want)
		}
	}
}

// NOBODY WE CANNOT NAME IS EVER UNBOUNDED. The free pool does not always end at our
// own compute — a route stated at zero can be served by a vendor who bills us — so an
// unresolved caller is a bill with no payer, and one script is enough to run it up.
//
// Every way the question can go unanswered lands on the same floor: the reserved
// public lane per UTC day; an empty tier name, a tier read that errored, a tier this
// app has no opinion on, and a deployment with no tier reader at all per session —
// the count a Free person keeps, so a commerce blip opens no second one.
func TestAnUnnamedCallerIsNeverUnbounded(t *testing.T) {
	cases := map[string]struct {
		s       *service
		org     string
		session bool
	}{
		"public lane":      {tiered("pro", nil), tenant.Public, false}, // never even asks
		"reader error":     {tiered("free", errors.New("commerce unreachable")), "hanzo", true},
		"no tier name":     {tiered("", nil), "hanzo", true},
		"no reader at all": {&service{}, "hanzo", true},
		"unlisted tier":    {tiered("some-future-plan", nil), "hanzo", true},
	}
	for what, c := range cases {
		_, _, session, daily := c.s.limits(context.Background(), calls, "hanzo/z", c.org)
		floor, other := daily, session
		if c.session {
			floor, other = session, daily
		}
		if floor != strangers || other != 0 {
			t.Errorf("%s resolved to %d a session and %d a day, want the floor %d on the %s alone", what, session, daily, strangers,
				map[bool]string{true: "session", false: "day"}[c.session])
		}
	}
	// The lease meter counts no sessions: its floor is the day.
	if _, _, session, daily := tiered("", nil).limits(context.Background(), leases, "acme", "acme"); session != 0 || daily != int64(leases.strangers) {
		t.Errorf("an unnamed lease resolved to %d a session and %d a day, want its floor a day", session, daily)
	}
}

// THE SWITCH TUNES THE CEILING; IT CANNOT DELETE IT. An operator may raise or lower
// the visitor limit live, but every non-positive setting — including the one an admin
// would reach for to turn it off — restores the shipped value.
func TestTheVisitorCeilingCannotBeTurnedOff(t *testing.T) {
	for _, set := range []int{0, -1, -1000} {
		if got := calls.ceiling(set); got != strangers {
			t.Errorf("switch=%d gave %d, want the shipped %d — the ceiling was removable", set, got, strangers)
		}
	}
	for _, set := range []int{1, 3, 5, 500} {
		if got := calls.ceiling(set); got != int64(set) {
			t.Errorf("switch=%d gave %d, want %d — the switch must tune in both directions", set, got, set)
		}
	}
}

// The public lane answers WITHOUT asking commerce: there is no plan behind a visitor,
// so there is no lookup to fail and no latency to pay on every free message.
func TestThePublicLaneNeverAsks(t *testing.T) {
	asked := false
	s := &service{tier: func(context.Context, string, string) (string, error) {
		asked = true
		return "pro", nil
	}}
	tier, _, _, limit := s.limits(context.Background(), calls, tenant.Public+"/v-abc", tenant.Public)
	if asked {
		t.Error("the public lane asked commerce for a plan a visitor cannot have")
	}
	if tier != tenant.Public {
		t.Errorf("tier = %q, want %q", tier, tenant.Public)
	}
	if limit != strangers {
		t.Errorf("the public ceiling is %d, want %d", limit, strangers)
	}
}

// An unbounded caller's standing is answered without touching the store, and says so
// plainly: limit 0, nothing used, nothing spent.
func TestUnboundedStandingReadsWithoutAStore(t *testing.T) {
	s := tiered("pro", nil)
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

	for what, out := range map[string]func() (*client.Allowance, error){
		"read": func() (*client.Allowance, error) {
			return s.read(context.Background(), calls, "hanzo", "hanzo/z", "hanzo/z", now)
		},
		"take": func() (*client.Allowance, error) {
			return s.take(context.Background(), calls, "hanzo", "hanzo/z", "hanzo/z", now)
		},
	} {
		got, err := out()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if got.Limit != 0 || got.Used != 0 || got.Spent {
			t.Errorf("%s answered limit=%d used=%d spent=%v, want an unbounded caller",
				what, got.Limit, got.Used, got.Spent)
		}
		if got.Resets != Midnight(now).Unix() {
			t.Errorf("%s resets at %d, want the next UTC midnight %d", what, got.Resets, Midnight(now).Unix())
		}
	}
}

// TestTheShippedCeilingsAreThePolicy states the numbers outright.
//
// Every other test here names `strangers` and `sessions` symbolically, which is
// right for an invariant — "the public lane uses the floor" stays true whatever the
// floor is — and is exactly why the numbers could be edited without a single
// failure. They are not implementation detail: they are what a visitor and a free
// subscriber are given, and a change to any is a product decision that should be
// argued in a review rather than noticed in production.
//
// A paid tier is deliberately absent: 0 there means unbounded, which
// TestOnlyANamedPaidTierIsUnbounded already holds as the property it is.
func TestTheShippedCeilingsAreThePolicy(t *testing.T) {
	if strangers != 50 {
		t.Errorf("an anonymous visitor gets %d calls a day, want 50", strangers)
	}
	// The owner's rule: 50 every five hours, and nothing else binds a free person.
	if sessions["free"] != 50 || flags.SeedSessionHours != 5 {
		t.Errorf("a signed-in free subscriber gets %d calls every %d hours, want 50 every 5", sessions["free"], flags.SeedSessionHours)
	}
	if seed["free"] != 0 {
		t.Errorf("a free subscriber is held to %d calls a day as well; the rule is the session alone", seed["free"])
	}
	for _, tier := range append(calls.tiers(), tenant.Public) {
		if n := calls.rateFor(tier); n != 0 {
			t.Errorf("%s free calls ship with %d an hour; no hour binds them", tier, n)
		}
	}
}

// TestTheHourlyWindowBindsBeforeTheDaily is the shape of a tier an admin gives two
// windows: ten an hour inside fifty a day. A caller who sends ten in one hour is
// stopped by the RATE and told so, with forty still unspent — naming the daily quota
// there would be true and useless.
func TestTheHourlyWindowBindsBeforeTheDaily(t *testing.T) {
	bs := []bound{
		{window: "hour", period: "2026-08-27T19", limit: 10, resets: time.Unix(100, 0)},
		{window: "day", period: "2026-08-27", limit: 50, resets: time.Unix(900, 0)},
	}
	out := standing("free", bs, map[string]int64{"hour": 10, "day": 10}, time.Unix(0, 0))
	if !out.Spent {
		t.Fatal("ten of ten in the hour is not spent")
	}
	if out.Window != "hour" {
		t.Errorf("refused on the %q window, want hour", out.Window)
	}
	if out.Limit != 10 || out.Used != 10 {
		t.Errorf("reported %d of %d, want 10 of 10 — the hour's numbers", out.Used, out.Limit)
	}
	if out.Resets != 100 {
		t.Errorf("resets at %d, want the top of the hour (100), not midnight", out.Resets)
	}
}

// TestTheDailyWindowBindsWhenTheHourIsFresh: the same caller an hour later is held
// by the quota instead, and the answer switches to it on its own.
func TestTheDailyWindowBindsWhenTheHourIsFresh(t *testing.T) {
	bs := []bound{
		{window: "hour", period: "2026-08-27T20", limit: 10, resets: time.Unix(100, 0)},
		{window: "day", period: "2026-08-27", limit: 50, resets: time.Unix(900, 0)},
	}
	out := standing("free", bs, map[string]int64{"hour": 0, "day": 50}, time.Unix(0, 0))
	if !out.Spent || out.Window != "day" {
		t.Fatalf("spent=%v on %q, want spent on day", out.Spent, out.Window)
	}
	if out.Resets != 900 {
		t.Errorf("resets at %d, want midnight (900)", out.Resets)
	}
}

// TestTheAnswerNamesTheWindowThatWillStopYou: with neither window refused, the one
// with LEAST LEFT is reported, so Limit-Used is the number that actually runs out
// next. Nine of fifty daily is 41 left; four of ten hourly is 6.
func TestTheAnswerNamesTheWindowThatWillStopYou(t *testing.T) {
	bs := []bound{
		{window: "hour", period: "h", limit: 10, resets: time.Unix(100, 0)},
		{window: "day", period: "d", limit: 50, resets: time.Unix(900, 0)},
	}
	out := standing("free", bs, map[string]int64{"hour": 4, "day": 9}, time.Unix(0, 0))
	if out.Spent {
		t.Fatal("nothing is exhausted, yet it reads as spent")
	}
	if out.Window != "hour" || out.Limit-out.Used != 6 {
		t.Errorf("named %q with %d left, want hour with 6", out.Window, out.Limit-out.Used)
	}
}

// TestAnUnboundedTierNamesNoWindow: a paid subscriber is held by a wallet, not by
// a counter, so there is no window to name and nothing to report as remaining.
func TestAnUnboundedTierNamesNoWindow(t *testing.T) {
	bs := []bound{
		{window: "hour", period: "h", limit: 0},
		{window: "day", period: "d", limit: 0},
	}
	out := standing("pro", bs, map[string]int64{}, time.Unix(0, 0))
	if out.Spent || out.Limit != 0 || out.Window != "" {
		t.Errorf("pro reads limit=%d window=%q spent=%v, want unbounded and unnamed",
			out.Limit, out.Window, out.Spent)
	}
}

// An org with no override flag set keeps its plan's limits.
func TestOrgWithoutOverrideKeepsPlan(t *testing.T) {
	s := tiered("free", nil)
	tier, hourly, session, daily := s.limits(context.Background(), calls, "hanzo/z", "hanzo")
	if tier != "free" || daily != int64(calls.daily["free"]) || hourly != int64(calls.rateFor("free")) || session != int64(calls.session["free"]) {
		t.Fatalf("unexpected defaults: tier=%s hourly=%d session=%d daily=%d", tier, hourly, session, daily)
	}
}
