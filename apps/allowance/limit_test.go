package allowance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/tenant"
	"github.com/hanzoai/cloud/plane"
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
		if _, limit := s.limit(context.Background(), "hanzo/z", "hanzo"); limit != 0 {
			t.Errorf("tier %q is bounded at %d — a paying caller's free calls must not be counted", name, limit)
		}
	}
	s := tiered("free", nil)
	tier, limit := s.limit(context.Background(), "hanzo/z", "hanzo")
	if tier != "free" {
		t.Errorf("tier = %q, want free", tier)
	}
	if limit != int64(seed["free"]) {
		t.Errorf("free is bounded at %d, want the seeded %d", limit, seed["free"])
	}
}

// NOBODY WE CANNOT NAME IS EVER UNBOUNDED. The free pool does not always end at our
// own compute — a route stated at zero can be served by a vendor who bills us — so an
// unresolved caller is a bill with no payer, and one script is enough to run it up.
//
// Every way the question can go unanswered lands on the same floor: the reserved
// public lane, an empty tier name, a tier read that errored, and a deployment with no
// tier reader at all.
func TestAnUnnamedCallerIsNeverUnbounded(t *testing.T) {
	cases := map[string]struct {
		s   *service
		org string
	}{
		"public lane":      {tiered("pro", nil), tenant.Public}, // never even asks
		"reader error":     {tiered("free", errors.New("commerce unreachable")), "hanzo"},
		"no tier name":     {tiered("", nil), "hanzo"},
		"no reader at all": {&service{}, "hanzo"},
		"unlisted tier":    {tiered("some-future-plan", nil), "hanzo"},
	}
	for what, c := range cases {
		_, limit := c.s.limit(context.Background(), "hanzo/z", c.org)
		if limit <= 0 {
			t.Errorf("%s resolved to %d — an unnamed caller must never be unbounded", what, limit)
		}
		if limit != strangers {
			t.Errorf("%s resolved to %d, want the floor %d", what, limit, strangers)
		}
	}
}

// THE SWITCH TUNES THE CEILING; IT CANNOT DELETE IT. An operator may raise or lower
// the visitor limit live, but every non-positive setting — including the one an admin
// would reach for to turn it off — restores the shipped value.
func TestTheVisitorCeilingCannotBeTurnedOff(t *testing.T) {
	for _, set := range []int{0, -1, -1000} {
		if got := ceiling(set); got != strangers {
			t.Errorf("switch=%d gave %d, want the shipped %d — the ceiling was removable", set, got, strangers)
		}
	}
	for _, set := range []int{1, 3, 5, 500} {
		if got := ceiling(set); got != int64(set) {
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
	tier, limit := s.limit(context.Background(), tenant.Public+"/v-abc", tenant.Public)
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

	for what, out := range map[string]func() (*plane.Allowance, error){
		"read": func() (*plane.Allowance, error) { return s.read(context.Background(), "hanzo", "hanzo/z", now) },
		"take": func() (*plane.Allowance, error) { return s.take(context.Background(), "hanzo", "hanzo/z", now) },
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
