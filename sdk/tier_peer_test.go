// Copyright © 2026 Hanzo AI. MIT License.

package cloud

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// A tier is asked of commerce once for a pair, then served while it is refreshed in
// the background, so a plan change reaches the gates within a refresh.
func TestATierIsAskedOnceAndRefreshedBehind(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	plane.Unbind()
	ResetPlane()
	t.Cleanup(ResetPlane)
	var asks atomic.Int64
	var plan atomic.Value
	plan.Store("free")
	Plane().Post("/billing/tier", func(ctx context.Context, in *client.TierIn) (*client.Tier, error) {
		asks.Add(1)
		return &client.Tier{User: in.Subject, Tier: client.TierLimits{Name: plan.Load().(string)}}, nil
	}, zip.WithOperationID(client.BillingTier))
	stop, err := ServePlane("commerce", nil)
	if err != nil {
		t.Fatalf("serve commerce: %v", err)
	}
	t.Cleanup(func() { _ = stop() })
	prev := tierTTL
	tierTTL = 50 * time.Millisecond
	t.Cleanup(func() { tierTTL = prev })

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if got, err := Tier(ctx, "tiercache/u", "tiercache"); err != nil || got != "free" {
			t.Fatalf("tier = %q, %v", got, err)
		}
	}
	if n := asks.Load(); n != 1 {
		t.Fatalf("commerce asked %d times for one pair, want 1", n)
	}
	plan.Store("pro")
	time.Sleep(60 * time.Millisecond)
	if got, _ := Tier(ctx, "tiercache/u", "tiercache"); got != "free" {
		t.Fatalf("a stale answer is served while it refreshes, got %q", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got, _ := Tier(ctx, "tiercache/u", "tiercache"); got == "pro" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the refreshed plan never reached the gate")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Unlimited is read from the plan: commerce files it under the pro tier.
//
// MUTATION: read the tier again in TierAllows and the Unlimited payer reads false.
func TestAnUnlimitedPayerIsKnownByItsPlanNotItsTier(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	plane.Unbind()
	ResetPlane()
	t.Cleanup(ResetPlane)
	Plane().Post("/billing/tier", func(ctx context.Context, in *client.TierIn) (*client.Tier, error) {
		switch in.Subject {
		case account.Org("rung-unlimited").Subject():
			return &client.Tier{User: in.Subject, Plan: "unlimited", PlanName: "Unlimited", Tier: client.TierLimits{Name: "pro"}}, nil
		case account.Org("rung-3x").Subject():
			return &client.Tier{User: in.Subject, Plan: "pro-3x", PlanName: "Pro 3x", Tier: client.TierLimits{Name: "pro"}}, nil
		case account.Org("rung-ent").Subject():
			return &client.Tier{User: in.Subject, Plan: "dedicated", Tier: client.TierLimits{Name: "enterprise"}}, nil
		}
		return &client.Tier{User: in.Subject, Tier: client.TierLimits{Name: "free"}}, nil
	}, zip.WithOperationID(client.BillingTier))
	stop, err := ServePlane("commerce", nil)
	if err != nil {
		t.Fatalf("serve commerce: %v", err)
	}
	t.Cleanup(func() { _ = stop() })

	ctx := context.Background()
	for org, want := range map[string]bool{"rung-unlimited": true, "rung-3x": false, "rung-ent": false, "rung-free": false} {
		if got := TierAllows(ctx, org, account.Org(org)); got != want {
			t.Errorf("TierAllows(%s) = %v, want %v", org, got, want)
		}
	}
	if tier, err := Tier(ctx, account.Org("rung-unlimited").Subject(), "rung-unlimited"); err != nil || tier != "pro" {
		t.Fatalf("the Unlimited payer's tier = %q, %v; want pro", tier, err)
	}
	if plan, err := Plan(ctx, account.Org("rung-unlimited").Subject(), "rung-unlimited"); err != nil || plan != "unlimited" {
		t.Fatalf("the Unlimited payer's plan = %q, %v; want unlimited", plan, err)
	}
	// A plan that cannot be read is not permission: the plane has no commerce for it.
	ResetPlane()
	plane.Unbind()
	if TierAllows(ctx, "rung-gone", account.Org("rung-gone")) {
		t.Fatal("a payer whose plan cannot be read read as Unlimited")
	}
}

// Callers such as hanzoai/ai's RateLimiter pass a composite payer key ("hanzo/<user>")
// as both subject and org for users in the signup org. Tier and Plan normalize the org
// namespace to the org slug ("hanzo") before calling commerce so paid subscribers are
// not downgraded to the free tier, and share one cache entry with the plain org call.
func TestTierAndPlanNormalizeCompositeOrg(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	plane.Unbind()
	ResetPlane()
	t.Cleanup(ResetPlane)
	var asks atomic.Int64
	Plane().Post("/billing/tier", func(ctx context.Context, in *client.TierIn) (*client.Tier, error) {
		asks.Add(1)
		if got := zip.CallerOf(ctx).Org; got != "hanzo" {
			return &client.Tier{User: in.Subject, Tier: client.TierLimits{Name: "free"}}, nil
		}
		return &client.Tier{User: in.Subject, Plan: "unlimited", Tier: client.TierLimits{Name: "pro"}}, nil
	}, zip.WithOperationID(client.BillingTier))
	stop, err := ServePlane("commerce", nil)
	if err != nil {
		t.Fatalf("serve commerce: %v", err)
	}
	t.Cleanup(func() { _ = stop() })

	ctx := context.Background()
	if got, err := Tier(ctx, "hanzo/composite-u", "hanzo/composite-u"); err != nil || got != "pro" {
		t.Fatalf("Tier(hanzo/composite-u, hanzo/composite-u) = %q, %v; want pro", got, err)
	}
	// Plain org call hits the same cache entry.
	if got, err := Tier(ctx, "hanzo/composite-u", "hanzo"); err != nil || got != "pro" {
		t.Fatalf("Tier(hanzo/composite-u, hanzo) = %q, %v; want pro", got, err)
	}
	if n := asks.Load(); n != 1 {
		t.Fatalf("commerce asked %d times for normalized pair, want 1", n)
	}
	if got, err := Plan(ctx, "hanzo/composite-u", "hanzo/composite-u"); err != nil || got != "unlimited" {
		t.Fatalf("Plan(hanzo/composite-u, hanzo/composite-u) = %q, %v; want unlimited", got, err)
	}
}
