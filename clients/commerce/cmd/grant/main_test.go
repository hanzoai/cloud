package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/billing/grant"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/subscription"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// fakeCatalog is a tiny in-memory catalog for tests.
type fakeCatalog map[string]*grant.CatalogPlan

func (f fakeCatalog) Lookup(slug string) *grant.CatalogPlan { return f[slug] }

func newCatalog() fakeCatalog {
	return fakeCatalog{
		"world-pro": {
			Slug:        "world-pro",
			Name:        "World Pro",
			Description: "World Pro test plan",
			PriceCents:  2900,
			Currency:    "usd",
		},
		"world-team": {
			Slug:        "world-team",
			Name:        "World Team",
			Description: "World Team test plan",
			PriceCents:  9900,
			Currency:    "usd",
		},
	}
}

// TestGrant_CreatesNewSubscription is the happy path — no prior subscription
// exists, Grant should mint one and seed the plan from the catalog.
func TestGrant_CreatesNewSubscription(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	db := datastore.New(ctx)

	got, err := grant.Grant(context.Background(), db, newCatalog(), grant.Request{
		UserId:    "hanzo/hunter",
		PlanSlug:  "world-pro",
		Duration:  12 * 30 * 24 * time.Hour,
		Reason:    "beta gift",
		GrantedBy: "zach@hanzo.ai",
	})
	if err != nil {
		t.Fatalf("Grant returned error: %v", err)
	}
	if got.SubscriptionID == "" {
		t.Fatal("Grant returned empty SubscriptionID")
	}
	if got.UserId != "hanzo/hunter" {
		t.Errorf("UserId = %q, want hanzo/hunter", got.UserId)
	}
	if got.PlanSlug != "world-pro" {
		t.Errorf("PlanSlug = %q, want world-pro", got.PlanSlug)
	}
	if got.PeriodEnd.Before(time.Now().Add(300 * 24 * time.Hour)) {
		t.Errorf("PeriodEnd %s should be ~12 months from now", got.PeriodEnd)
	}

	// Verify the subscription is actually in the datastore.
	sub := subscription.New(db)
	found, err := sub.Query().Filter("UserId=", "hanzo/hunter").Get()
	if err != nil {
		t.Fatalf("query subscription: %v", err)
	}
	if !found {
		t.Fatal("subscription not persisted")
	}
	if sub.Status != subscription.Active {
		t.Errorf("Status = %q, want active", sub.Status)
	}
	if sub.ProviderType != "manual_gift" {
		t.Errorf("ProviderType = %q, want manual_gift", sub.ProviderType)
	}
}

// TestGrant_IsIdempotent guarantees a second grant for the same user+plan
// does not create a duplicate — it extends the existing subscription.
func TestGrant_IsIdempotent(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	db := datastore.New(ctx)

	req := grant.Request{
		UserId:    "hanzo/alice",
		PlanSlug:  "world-pro",
		Duration:  30 * 24 * time.Hour, // 1 month
		Reason:    "trial 1",
		GrantedBy: "zach@hanzo.ai",
	}

	first, err := grant.Grant(context.Background(), db, newCatalog(), req)
	if err != nil {
		t.Fatalf("first grant: %v", err)
	}

	// Second grant: longer duration, different reason.
	req.Duration = 365 * 24 * time.Hour
	req.Reason = "trial 2"
	second, err := grant.Grant(context.Background(), db, newCatalog(), req)
	if err != nil {
		t.Fatalf("second grant: %v", err)
	}

	if first.SubscriptionID != second.SubscriptionID {
		t.Errorf("idempotency broken: first=%s second=%s", first.SubscriptionID, second.SubscriptionID)
	}
	if !second.PeriodEnd.After(first.PeriodEnd) {
		t.Errorf("second PeriodEnd (%s) should extend first (%s)", second.PeriodEnd, first.PeriodEnd)
	}
}

// TestGrant_RejectsUnknownPlan ensures missing plan slugs fail fast rather
// than creating a zombie subscription.
func TestGrant_RejectsUnknownPlan(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	db := datastore.New(ctx)

	_, err := grant.Grant(context.Background(), db, newCatalog(), grant.Request{
		UserId:   "hanzo/bob",
		PlanSlug: "world-does-not-exist",
		Duration: 30 * 24 * time.Hour,
	})
	if err == nil {
		t.Fatal("expected error for unknown plan")
	}
	if !errors.Is(err, grant.ErrPlanNotFound) {
		t.Errorf("error %v should wrap ErrPlanNotFound", err)
	}
}

// TestGrant_RejectsBlankUser ensures a missing user id fails fast.
func TestGrant_RejectsBlankUser(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	db := datastore.New(ctx)

	_, err := grant.Grant(context.Background(), db, newCatalog(), grant.Request{
		UserId:   "",
		PlanSlug: "world-pro",
	})
	if !errors.Is(err, grant.ErrInvalidUser) {
		t.Fatalf("err = %v, want ErrInvalidUser", err)
	}
}

// TestGrant_AutoCreatesPlanFromCatalog — when a plan is not yet in the DB,
// Grant pulls it from the supplied catalog and persists it.
func TestGrant_AutoCreatesPlanFromCatalog(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	db := datastore.New(ctx)

	// Use world-team which is in catalog but not pre-seeded in DB.
	_, err := grant.Grant(context.Background(), db, newCatalog(), grant.Request{
		UserId:   "hanzo/carol",
		PlanSlug: "world-team",
		Duration: 30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	// Now a second grant for the SAME plan should reuse the DB record.
	_, err = grant.Grant(context.Background(), db, newCatalog(), grant.Request{
		UserId:   "hanzo/dave",
		PlanSlug: "world-team",
		Duration: 30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("second grant (plan reuse): %v", err)
	}
}
