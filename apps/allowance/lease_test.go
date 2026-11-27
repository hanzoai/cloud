package allowance

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// A LEASE HOLDS A POD WHATEVER THE PLAN SAYS. The lease meter bounds every tier,
// a paid one included: this lane is for a wallet that cannot cover a lease, and a
// paid plan with money never reaches it.
func TestEveryTierIsBoundedOnLeases(t *testing.T) {
	for _, name := range []string{"free", "starter", "pro", "enterprise"} {
		tier, hourly, session, daily := tiered(name, nil).limits(context.Background(), leases, "acme", "acme")
		if session != 0 {
			t.Errorf("%s: leases are held to a session of %d; the lease meter counts none", name, session)
		}
		if tier != name || daily != int64(leases.daily[name]) || hourly != int64(leases.hourly[name]) {
			t.Errorf("%s: tier %q at %d an hour, %d a day; want the seeded %d and %d",
				name, tier, hourly, daily, leases.hourly[name], leases.daily[name])
		}
		if daily <= 0 || hourly <= 0 {
			t.Errorf("%s leases free compute without bound (%d an hour, %d a day)", name, hourly, daily)
		}
	}
}

// Leases and AI calls keep their own counts: a day of free sandboxes spends no free
// call.
func TestLeasesAndCallsCountApart(t *testing.T) {
	plan, asked := "free", 0
	s := planned(t, &plan, nil, nil, &asked)
	ctx := context.Background()
	now := time.Date(2026, 9, 29, 19, 30, 0, 0, time.UTC)

	var out *client.Allowance
	var err error
	for range leases.hourly["free"] + 1 {
		if out, err = s.take(ctx, leases, "acme", "acme", "acme", now); err != nil {
			t.Fatal(err)
		}
	}
	if !out.Spent || out.Window != "hour" || out.Used != int64(leases.hourly["free"]) {
		t.Fatalf("after %d leases in one hour: %+v, want the hour spent at %d", leases.hourly["free"]+1, out, leases.hourly["free"])
	}
	got, err := s.read(ctx, calls, "acme", "acme", "acme", now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Used != 0 || got.Spent {
		t.Fatalf("free calls read %+v after leases only — the meters share a count", got)
	}
	if u := s.store.Read(ctx, "acme", []bound{{row: calls.row("session"), span: fiveHours}}, now)[0]; u != 0 {
		t.Fatalf("the calls' session holds %d, want 0", u)
	}
	if u := s.store.Read(ctx, "acme", []bound{{row: leases.row("hour"), period: Hour(now)}}, now)[0]; u != int64(leases.hourly["free"]) {
		t.Fatalf("the leases' hour holds %d, want %d", u, leases.hourly["free"])
	}
}

// A plane call names its meter, and a meter nobody defined is refused rather than
// read as AI calls.
func TestAnUnknownMeterIsRefused(t *testing.T) {
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme"})
	if _, _, _, _, err := scope(ctx, &client.AllowanceIn{Subject: "acme", Meter: "gpu"}); err == nil {
		t.Fatal("a meter nobody defined was accepted")
	}
}

// TestTheShippedLeaseCeilingsAreThePolicy states the free lane of compute outright:
// twenty leases a day, six an hour, for every tier; one for a caller whose plan
// could not be read.
func TestTheShippedLeaseCeilingsAreThePolicy(t *testing.T) {
	for _, tier := range []string{"free", "starter", "pro", "enterprise"} {
		if leases.daily[tier] != 20 || leases.hourly[tier] != 6 {
			t.Errorf("%s gets %d free leases a day and %d an hour, want 20 and 6", tier, leases.daily[tier], leases.hourly[tier])
		}
	}
	if leases.strangers != 1 {
		t.Errorf("an unnamed caller gets %d free leases a day, want 1", leases.strangers)
	}
	if leases.row("day") == calls.row("day") {
		t.Error("leases and calls write the same row")
	}
	if leases.session != nil || runs.session != nil {
		t.Error("the lease or automation meter counts a session; only free AI calls do")
	}
}
