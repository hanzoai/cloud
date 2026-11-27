// Copyright © 2026 Hanzo AI. MIT License.

package commerce_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/commerce/models/organization"
	commerceorg "github.com/hanzoai/commerce/pkg/org"

	"github.com/hanzoai/cloud/apps/commerce"
	"github.com/hanzoai/cloud/client"
)

func TestOurOrgsPayLikeCustomersE2E(t *testing.T) {
	// The Square sandbox merchant's variables, set before commerce is constructed.
	commerce.SandboxForTest(t)
	emb := bootCommerce(t)
	_ = emb

	// Square's environment is the org record, not SQUARE_ENVIRONMENT. This
	// process resolves orgs through a cache another test may have filled with
	// a live "hanzo", which would report production. This test's orgs are born
	// in test mode, and they are resolved from this boot, not from that cache.
	prevBorn := organization.Born
	organization.Born = organization.Test
	t.Cleanup(func() { organization.Born = prevBorn })
	for _, name := range []string{"admin", "hanzo", "lux", "zoo", "adnexus", "bootnode", "osage", "pars", "acme"} {
		commerceorg.Invalidate(name)
	}

	// Set Square sandbox credentials
	os.Setenv("SQUARE_ENVIRONMENT", "sandbox")
	os.Setenv("SQUARE_APPLICATION_ID", "sandbox-sq0idb--kZP78n8_TjQOu8qApr5iQ")
	os.Setenv("SQUARE_LOCATION_ID", "LD8DWE4KKB0WB")
	t.Cleanup(func() {
		os.Unsetenv("SQUARE_ENVIRONMENT")
		os.Unsetenv("SQUARE_APPLICATION_ID")
		os.Unsetenv("SQUARE_LOCATION_ID")
	})

	ctx := context.Background()

	// Our orgs resolve like customers: no org list, no enterprise tier by name, no
	// credit floor, no unlimited roster. Every org's tier is what its own recorded
	// subscriptions confer, and a balance is what its ledger holds.
	for _, org := range []string{"admin", "hanzo", "lux", "zoo", "adnexus", "bootnode", "osage", "pars", "acme"} {
		t.Run("tier_"+org, func(t *testing.T) {
			orgCtx := zip.WithCaller(ctx, zip.Caller{User: "owner", Org: org})
			tier, err := commerce.PlaneTierForTest(orgCtx, &client.TierIn{Subject: org})
			if err != nil {
				t.Fatalf("planeTier for %s: %v", org, err)
			}
			if tier.Tier.Name != "free" {
				t.Errorf("org %s tier %s, want free like any customer", org, tier.Tier.Name)
			}
			if tier.Balance.CreditsRemaining != 0 || tier.Balance.EffectiveAvailable != 0 || tier.Balance.PrepaidAvailable != 0 {
				t.Errorf("org %s credits %d effective %d prepaid %d, want 0 with nothing granted or paid",
					org, tier.Balance.CreditsRemaining, tier.Balance.EffectiveAvailable, tier.Balance.PrepaidAvailable)
			}
		})
	}

	// No org buys a plan on credit it was never granted, ours included.
	for _, org := range []string{"admin", "hanzo", "lux", "zoo", "adnexus", "bootnode", "osage", "pars"} {
		t.Run("no_plan_on_credit_nobody_granted_"+org, func(t *testing.T) {
			orgCtx := zip.WithCaller(ctx, zip.Caller{User: "z", Org: org})
			if sold, err := commerce.PlaneSubscribeForTest(orgCtx, &client.SaleIn{
				SourceID: "credits", PlanID: "dev", Quantity: 1, Subject: org, Currency: "usd",
			}); err == nil {
				t.Fatalf("%s bought a plan on credit it was never granted: %+v", org, sold)
			}
		})
	}

	t.Run("settings_square_sandbox", func(t *testing.T) {
		orgCtx := zip.WithCaller(ctx, zip.Caller{User: "z", Org: "hanzo"})
		// A live record reports production even with sandbox credentials set.
		// This subtest is the sandbox account, so the record is test mode.
		commerceorg.Invalidate("hanzo")
		o, err := commerceorg.Resolve(orgCtx, "hanzo")
		if err != nil {
			t.Fatalf("resolve hanzo: %v", err)
		}
		if o.Live {
			o.Live = false
			if err := o.Update(); err != nil {
				t.Fatalf("put hanzo in test mode: %v", err)
			}
		}
		settings, err := commerce.PlaneSettingsForTest(orgCtx)
		if err != nil {
			t.Fatalf("planeSettings: %v", err)
		}
		if settings.Environment != "sandbox" {
			t.Errorf("settings environment = %s, want sandbox", settings.Environment)
		}
		if settings.ApplicationID != "sandbox-sq0idb--kZP78n8_TjQOu8qApr5iQ" {
			t.Errorf("settings applicationId = %s, want sandbox-sq0idb--kZP78n8_TjQOu8qApr5iQ", settings.ApplicationID)
		}
		if settings.LocationID != "LD8DWE4KKB0WB" {
			t.Errorf("settings locationId = %s, want LD8DWE4KKB0WB", settings.LocationID)
		}
	})

	t.Run("subscribe_via_square_sandbox_card", func(t *testing.T) {
		// A person in one of our orgs, named for this test alone: the sandbox
		// merchant holds the customers of every org it serves, and this sale opens
		// its own.
		org, user := "lux", commerce.FreshForTest("z")
		subject := org + "/" + user
		orgCtx := zip.WithCaller(ctx, zip.Caller{User: user, Org: org})
		began := time.Now()

		// Square's sandbox card nonce cnon:card-nonce-ok, charged on the sandbox
		// merchant commerce resolves for a test-mode org.
		sold, err := commerce.PlaneSubscribeForTest(orgCtx, &client.SaleIn{
			SourceID:       "cnon:card-nonce-ok",
			PlanID:         "pro-3x",
			Quantity:       1,
			Subject:        subject,
			Currency:       "usd",
			Email:          "z@hanzo.ai",
			IdempotencyKey: commerce.FreshForTest("pro"),
		})
		if err != nil {
			t.Fatalf("planeSubscribe with square sandbox card for %s: %v", subject, err)
		}
		if sold.Sale == nil {
			t.Fatalf("sold.Sale is nil")
		}
		if sold.Sale.SubscriptionID == "" {
			t.Errorf("subscription ID is empty")
		}
		if sold.Sale.InvoiceID == "" {
			t.Errorf("invoice ID is empty")
		}
		if sold.Sale.AmountCents != 1500 { // Pro, $15 / 1500 cents
			t.Errorf("amount = %d cents, want 1500 ($15)", sold.Sale.AmountCents)
		}
		if sold.Sale.Interval != "month" {
			t.Errorf("interval = %q, want month (the default term)", sold.Sale.Interval)
		}
		commerce.SoldOnceForTest(t, org, sold.Sale.InvoiceID, 1500, began)
	})

	t.Run("subscribe_annual_through_the_plane", func(t *testing.T) {
		org := commerce.FreshForTest("yearly")
		orgCtx := zip.WithCaller(ctx, zip.Caller{User: "z", Org: org})
		began := time.Now()

		// The term the buyer chose reaches commerce, which charges the plan's annual
		// total now and opens a subscription that renews yearly.
		sold, err := commerce.PlaneSubscribeForTest(orgCtx, &client.SaleIn{
			SourceID:       "cnon:card-nonce-ok",
			PlanID:         "team",
			Quantity:       2,
			Interval:       "year",
			Subject:        org,
			Currency:       "usd",
			Email:          "z@hanzo.ai",
			IdempotencyKey: commerce.FreshForTest("team"),
		})
		if err != nil {
			t.Fatalf("planeSubscribe annual for %s: %v", org, err)
		}
		if sold.Sale == nil {
			t.Fatalf("sold.Sale is nil")
		}
		if sold.Sale.Interval != "year" {
			t.Errorf("interval = %q, want year", sold.Sale.Interval)
		}
		if sold.Sale.AmountCents != 48000 { // $480, 2 seats × $240/year
			t.Errorf("amount = %d cents, want 48000 ($480 a year for 2 seats)", sold.Sale.AmountCents)
		}
		commerce.SoldOnceForTest(t, org, sold.Sale.InvoiceID, 48000, began)
	})

	t.Run("non_partner_credits_insufficient_declined", func(t *testing.T) {
		orgCtx := zip.WithCaller(ctx, zip.Caller{User: "eve", Org: "acme"})
		_, err := commerce.PlaneSubscribeForTest(orgCtx, &client.SaleIn{
			SourceID: "credits",
			PlanID:   "team",
			Quantity: 1,
			Subject:  "acme",
			Currency: "usd",
		})
		if err == nil {
			t.Fatalf("acme subscribing with 0 credits must fail")
		}
	})

	t.Run("subscribe_replay_idempotency", func(t *testing.T) {
		// A person in a partner org, named for this test alone, buys by card: a plan
		// is never bought from credit or balance, which pay for usage only.
		org, user := "adnexus", commerce.FreshForTest("replay")
		orgCtx := zip.WithCaller(ctx, zip.Caller{User: user, Org: org})
		sale := &client.SaleIn{
			SourceID:       "cnon:card-nonce-ok",
			PlanID:         "pro-3x",
			Quantity:       1,
			Subject:        org + "/" + user,
			Currency:       "usd",
			Email:          "z@hanzo.ai",
			IdempotencyKey: commerce.FreshForTest("pro"),
		}
		began := time.Now()

		sold1, err := commerce.PlaneSubscribeForTest(orgCtx, sale)
		if err != nil {
			t.Fatalf("first attempt failed: %v", err)
		}
		if len(sold1.Replayed) > 0 {
			t.Errorf("first sale should not be replayed")
		}
		if sold1.Sale == nil {
			t.Fatalf("first sale receipt is nil")
		}

		// Second attempt with exact same idempotency key
		sold2, err := commerce.PlaneSubscribeForTest(orgCtx, sale)
		if err != nil {
			t.Fatalf("replay with same idempotency key failed: %v", err)
		}
		if len(sold2.Replayed) == 0 {
			t.Errorf("expected replayed receipt with non-empty Replayed bytes")
		}
		// Square holds one payment on the card's customer: the replay charged nothing.
		commerce.SoldOnceForTest(t, org, sold1.Sale.InvoiceID, sold1.Sale.AmountCents, began)
	})
}
