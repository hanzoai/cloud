// Copyright © 2026 Hanzo AI. MIT License.

package commerce_test

import (
	"context"
	"os"
	"testing"

	"github.com/zap-proto/zip"

	billingapi "github.com/hanzoai/commerce/api/billing"
	"github.com/hanzoai/commerce/models/organization"
	"github.com/hanzoai/commerce/payment/processor"

	"github.com/hanzoai/cloud/apps/commerce"
	"github.com/hanzoai/cloud/client"
)

func TestPartnerOrgsAllowanceAndCreditsE2E(t *testing.T) {
	emb := bootCommerce(t)
	_ = emb

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

	partnerOrgs := []string{
		"admin", "hanzo", "lux", "zoo", "adnexus", "bootnode", "osage", "pars",
	}

	for _, org := range partnerOrgs {
		t.Run("partner_tier_"+org, func(t *testing.T) {
			orgCtx := zip.WithCaller(ctx, zip.Caller{User: "owner", Org: org})
			tier, err := commerce.PlaneTierForTest(orgCtx, &client.TierIn{Subject: org})
			if err != nil {
				t.Fatalf("planeTier for %s: %v", org, err)
			}
			if tier.Tier.Name != "enterprise" {
				t.Errorf("org %s tier = %s, want enterprise", org, tier.Tier.Name)
			}
			if !tier.Tier.UnlimitedAgents {
				t.Errorf("org %s unlimitedAgents = false, want true", org)
			}
			if tier.Balance.CreditsRemaining < 100000 {
				t.Errorf("org %s creditsRemaining = %d, want >= 100000 ($1000 floor)", org, tier.Balance.CreditsRemaining)
			}
			if tier.Balance.EffectiveAvailable < 100000 {
				t.Errorf("org %s effectiveAvailable = %d, want >= 100000", org, tier.Balance.EffectiveAvailable)
			}
		})
	}

	t.Run("non_partner_tier_acme", func(t *testing.T) {
		orgCtx := zip.WithCaller(ctx, zip.Caller{User: "eve", Org: "acme"})
		tier, err := commerce.PlaneTierForTest(orgCtx, &client.TierIn{Subject: "acme"})
		if err != nil {
			t.Fatalf("planeTier for acme: %v", err)
		}
		if tier.Tier.Name != "free" {
			t.Errorf("acme tier = %s, want free", tier.Tier.Name)
		}
		if tier.Balance.CreditsRemaining != 0 {
			t.Errorf("acme creditsRemaining = %d, want 0", tier.Balance.CreditsRemaining)
		}
		if tier.Balance.EffectiveAvailable != 0 {
			t.Errorf("acme effectiveAvailable = %d, want 0", tier.Balance.EffectiveAvailable)
		}
		if tier.Balance.PrepaidAvailable != 0 {
			t.Errorf("acme prepaidAvailable = %d, want 0", tier.Balance.PrepaidAvailable)
		}
	})

	t.Run("subscribe_via_credits_for_seats", func(t *testing.T) {
		org := "hanzo"
		orgCtx := zip.WithCaller(ctx, zip.Caller{User: "z", Org: org})

		// Subscribe to 5 team seats using credits as sourceId
		sold, err := commerce.PlaneSubscribeForTest(orgCtx, &client.SaleIn{
			SourceID: "credits",
			PlanID:   "team",
			Quantity: 5,
			Subject:  org,
			Currency: "usd",
		})
		if err != nil {
			t.Fatalf("planeSubscribe with credits for %s: %v", org, err)
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
		if sold.Sale.AmountCents != 12000 { // 5 * 2400 = 12000 cents ($120)
			t.Errorf("amount = %d cents, want 12000 ($120)", sold.Sale.AmountCents)
		}

		// Verify tier after subscribing: auto-recharge maintains 100,000 cents floor
		tier, err := commerce.PlaneTierForTest(orgCtx, &client.TierIn{Subject: org})
		if err != nil {
			t.Fatalf("tier check after subscribe: %v", err)
		}
		if tier.Balance.CreditsRemaining < 100000 {
			t.Errorf("creditsRemaining = %d, expected >= 100000 after auto-recharge", tier.Balance.CreditsRemaining)
		}
		if tier.Balance.EffectiveAvailable < 100000 {
			t.Errorf("effectiveAvailable = %d, expected >= 100000 after auto-recharge", tier.Balance.EffectiveAvailable)
		}
	})

	t.Run("settings_square_sandbox", func(t *testing.T) {
		orgCtx := zip.WithCaller(ctx, zip.Caller{User: "z", Org: "hanzo"})
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
		org := "lux"
		orgCtx := zip.WithCaller(ctx, zip.Caller{User: "z", Org: org})

		// Configure Square processor for this test
		cleanup := billingapi.SetProcessorsForOrg(func(*organization.Organization) *processor.Registry {
			reg := processor.NewRegistry(processor.DefaultConfig())
			reg.Register(billingapi.NewMockSquareProcessor("cust_lux_sbx", "card_lux_sbx", "sqpay_lux_123"))
			return reg
		})
		defer cleanup()

		// Subscribe using Square sandbox card nonce cnon:card-nonce-ok
		sold, err := commerce.PlaneSubscribeForTest(orgCtx, &client.SaleIn{
			SourceID: "cnon:card-nonce-ok",
			PlanID:   "dev",
			Quantity: 1,
			Subject:  org,
			Currency: "usd",
			Email:    "z@hanzo.ai",
		})
		if err != nil {
			t.Fatalf("planeSubscribe with square sandbox card for %s: %v", org, err)
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
		if sold.Sale.AmountCents != 1900 { // $19 / 1900 cents
			t.Errorf("amount = %d cents, want 1900 ($19)", sold.Sale.AmountCents)
		}
	})

	t.Run("subscribe_all_ecosystem_partners_via_credits", func(t *testing.T) {
		// Test that every single other partner org can also subscribe via credits
		otherPartners := []string{"admin", "zoo", "adnexus", "bootnode", "osage", "pars"}
		for _, org := range otherPartners {
			orgCtx := zip.WithCaller(ctx, zip.Caller{User: "admin", Org: org})
			sold, err := commerce.PlaneSubscribeForTest(orgCtx, &client.SaleIn{
				SourceID: "credits",
				PlanID:   "dev",
				Quantity: 1,
				Subject:  org,
				Currency: "usd",
			})
			if err != nil {
				t.Fatalf("planeSubscribe with credits for %s: %v", org, err)
			}
			if sold.Sale == nil || sold.Sale.SubscriptionID == "" {
				t.Errorf("org %s: invalid subscription receipt", org)
			}
			if sold.Sale.AmountCents != 1900 { // $19 flat
				t.Errorf("org %s: amount = %d, want 1900", org, sold.Sale.AmountCents)
			}

			// Verify floor maintained
			tier, err := commerce.PlaneTierForTest(orgCtx, &client.TierIn{Subject: org})
			if err != nil {
				t.Fatalf("tier check for %s: %v", org, err)
			}
			if tier.Balance.CreditsRemaining < 100000 {
				t.Errorf("org %s credits = %d, want >= 100000", org, tier.Balance.CreditsRemaining)
			}
		}
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
		org := "adnexus"
		orgCtx := zip.WithCaller(ctx, zip.Caller{User: "admin", Org: org})
		idemKey := "idem-test-replay-adnexus-1"

		sold1, err := commerce.PlaneSubscribeForTest(orgCtx, &client.SaleIn{
			SourceID:       "credits",
			PlanID:         "dev",
			Quantity:       1,
			Subject:        org + "/subuser",
			Currency:       "usd",
			IdempotencyKey: idemKey,
		})
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
		sold2, err := commerce.PlaneSubscribeForTest(orgCtx, &client.SaleIn{
			SourceID:       "credits",
			PlanID:         "dev",
			Quantity:       1,
			Subject:        org + "/subuser",
			Currency:       "usd",
			IdempotencyKey: idemKey,
		})
		if err != nil {
			t.Fatalf("replay with same idempotency key failed: %v", err)
		}
		if len(sold2.Replayed) == 0 {
			t.Errorf("expected replayed receipt with non-empty Replayed bytes")
		}
	})
}

