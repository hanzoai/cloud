// Copyright © 2026 Hanzo AI. MIT License.

package commerce_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/commerce"
	commercemod "github.com/hanzoai/commerce"
	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/hanzoai/commerce/billing/grant"
	"github.com/hanzoai/commerce/datastore"
	commerceplan "github.com/hanzoai/commerce/models/plan"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
)

// plansOnce mounts the @hanzo/plans goja vocabulary exactly once for the package —
// CheckEntitlement resolves a plan tier's license features through it.
func mountPlansVocab(t *testing.T) { t.Helper() }

// fakeCatalog is the minimal grant.PlanCatalog that seeds the "max-5x" tier — the
// tier whose @hanzo/plans entitlements carry licensing.product_ids:["engine"], so a
// "max-5x" subscriber is entitled to the "engine" product.
type fakeCatalog struct{}

func (fakeCatalog) Lookup(slug string) *grant.CatalogPlan {
	if slug == "max-5x" {
		return &grant.CatalogPlan{Slug: "max-5x", Name: "Max 5x", Description: "Max 5x tier", PriceCents: 10000, Currency: "usd"}
	}
	// "team-max" is a RETIRED tier: @hanzo/plans stopped publishing it at 1.4.5. It
	// is here because a subscription on it was opened while it WAS on sale, and that
	// subscriber is still being charged — which is exactly the case the entitlement
	// resolver must keep answering. It is a team tier, so the boot's migration of
	// retired personal rungs onto the ladder leaves it where it is.
	if slug == "team-max" {
		return &grant.CatalogPlan{Slug: "team-max", Name: "Team Max", Description: "Retired tier", PriceCents: 10000, Currency: "usd"}
	}
	return nil
}

// bootCommerce boots the embedded commerce app (SQLite fallback, dev) once per test,
// sets its brand + publishes it, and returns the handle. Embed installs the process-
// global default datastore the entitlement resolver reads via commerceorg.Resolve.
func bootCommerce(t *testing.T) *commercemod.Embedded {
	t.Helper()
	emb, err := commercemod.Embed(context.Background(), commercemod.EmbedConfig{DataDir: t.TempDir(), Dev: true})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	commerce.PublishEmbedded(emb)
	commerce.BootLinksForTest(t)
	t.Cleanup(func() {
		commerce.PublishEmbedded(nil)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = emb.Stop(ctx)
	})
	return emb
}

// seedActiveGrant creates an active manual subscription on the given plan tier for
// org, in the org's own datastore namespace — the same store the resolver reads.
func seedActiveGrant(t *testing.T, ctx context.Context, org, planSlug string) {
	t.Helper()
	o, err := commerceorg.Resolve(ctx, org)
	if err != nil {
		t.Fatalf("resolve org %q: %v", org, err)
	}
	db := datastore.New(o.Namespaced(ctx))
	if _, err := grant.Grant(ctx, db, fakeCatalog{}, grant.Request{
		UserId:    org + "/owner",
		PlanSlug:  planSlug,
		Duration:  365 * 24 * time.Hour,
		Reason:    "test seed",
		GrantedBy: "test",
	}); err != nil {
		t.Fatalf("grant %q on %q: %v", planSlug, org, err)
	}
}

// TestInProcessClient exercises the REAL in-process commerce.Client end-to-end: a
// genuinely-entitled org (Active:true with the plan's real license
// features), a resolvable-but-not-entitled org (Active:false, no error — never a
// fabricated grant), and the fail-closed path when commerce is not co-resident. It
// boots the embedded commerce once so the process-global datastore stays this test's
// for the whole run.
func TestInProcessClient(t *testing.T) {
	mountPlansVocab(t)
	emb := bootCommerce(t)
	client := commerce.InProcessClient()
	ctx := context.Background()

	t.Run("entitled_org_gets_real_features", func(t *testing.T) {
		const org = "maxco"
		seedActiveGrant(t, ctx, org, "max-5x")

		ent, err := client.CheckEntitlement(ctx, org, "engine")
		if err != nil {
			t.Fatalf("CheckEntitlement: %v", err)
		}
		if ent == nil || !ent.Active {
			t.Fatalf("engine on a max-5x plan must be Active; got %+v", ent)
		}
		if ent.Plan != "max-5x" {
			t.Errorf("Plan = %q, want max-5x", ent.Plan)
		}
		// Real features come from @hanzo/plans toLicenseFeatures — NOT fabricated.
		if !slices.Contains(ent.Features, "licensing.product:engine") {
			t.Errorf("Features %v missing product-scope token licensing.product:engine", ent.Features)
		}
		if !slices.Contains(ent.Features, "inference") {
			t.Errorf("Features %v missing engine_feature 'inference'", ent.Features)
		}
	})

	// A subscriber on a RETIRED tier must still resolve the products that tier
	// licensed. The catalog cannot answer — it lists what is on sale today, and this
	// tier is not — so the answer has to come from the tier's own authority row,
	// which commerce keeps resolvable after retirement and now backfills the
	// licensing block onto. Without that the org is refused a product it pays for.
	t.Run("retired_tier_still_licenses_its_products", func(t *testing.T) {
		const org = "teammaxco"

		// Production state: the row was archived back when a plan carried no
		// licensing block at all, so it has none.
		adb := commerceplan.AuthorityDB(ctx)
		row := commerceplan.New(adb)
		row.Slug, row.Category, row.Price = "team-max", "team", 10000
		row.Status, row.Managed = commerceplan.StatusArchived, true
		if err := row.Create(); err != nil {
			t.Fatalf("create archived team-max row: %v", err)
		}

		seedActiveGrant(t, ctx, org, "team-max")

		// Before the boot seed backfills it, the row cannot say what it licensed —
		// which is precisely the live defect.
		if ent, err := client.CheckEntitlement(ctx, org, "team"); err != nil {
			t.Fatalf("CheckEntitlement: %v", err)
		} else if ent.Active {
			t.Fatalf("un-backfilled archived row must not grant; got %+v", ent)
		}

		// The boot seed reconciles the catalog and backfills what retired tiers
		// licensed when they were last on sale.
		if _, _, err := commercebilling.SeedPlans(ctx); err != nil {
			t.Fatalf("SeedPlans: %v", err)
		}

		ent, err := client.CheckEntitlement(ctx, org, "team")
		if err != nil {
			t.Fatalf("CheckEntitlement: %v", err)
		}
		if !ent.Active {
			t.Fatalf("a subscriber on the retired %q tier must still hold its team licence; got %+v", "team-max", ent)
		}
		if ent.Plan != "team-max" {
			t.Errorf("Plan = %q, want team-max", ent.Plan)
		}
		if !slices.Contains(ent.Features, "licensing.product:team") {
			t.Errorf("Features %v missing licensing.product:team", ent.Features)
		}
	})

	t.Run("entitled_org_but_unlicensed_product_is_not_active_no_error", func(t *testing.T) {
		const org = "maxco2"
		seedActiveGrant(t, ctx, org, "max-5x")

		// A product the max plan does NOT license resolves to Active:false — a real
		// "not entitled" answer, never an error and never a fabricated grant.
		ent, err := client.CheckEntitlement(ctx, org, "some-unlicensed-product")
		if err != nil {
			t.Fatalf("CheckEntitlement (unlicensed product) must resolve, not error: %v", err)
		}
		if ent == nil || ent.Active {
			t.Fatalf("unlicensed product must be Active:false; got %+v", ent)
		}
	})

	// Early access is the org's, read off the row of every live plan it holds
	// (@hanzo/plans licensing.early_access, every Max rung): it answers whichever
	// product was asked about, licensed or not, and only a plan that carries it says
	// yes.
	t.Run("a_max_org_carries_early_access_whatever_was_asked", func(t *testing.T) {
		const org = "maxco3"
		seedActiveGrant(t, ctx, org, "max-5x")
		for _, product := range []string{"crm", "some-unlicensed-product"} {
			ent, err := client.CheckEntitlement(ctx, org, product)
			if err != nil {
				t.Fatalf("CheckEntitlement(%s): %v", product, err)
			}
			if !ent.EarlyAccess {
				t.Errorf("CheckEntitlement(%s) on max-5x: EarlyAccess false; every Max rung carries it", product)
			}
		}
		for _, org := range []string{"teammaxco", "poorco"} {
			ent, err := client.CheckEntitlement(ctx, org, "crm")
			if err != nil {
				t.Fatalf("CheckEntitlement(%s): %v", org, err)
			}
			if ent.EarlyAccess {
				t.Errorf("CheckEntitlement(%s): EarlyAccess true on a plan that does not carry it", org)
			}
		}
	})

	t.Run("org_with_no_subscription_is_not_active_no_error", func(t *testing.T) {
		ent, err := client.CheckEntitlement(ctx, "poorco", "engine")
		if err != nil {
			t.Fatalf("CheckEntitlement (no sub) must resolve, not error: %v", err)
		}
		if ent == nil || ent.Active {
			t.Fatalf("org with no subscription must be Active:false; got %+v", ent)
		}
	})

	t.Run("fails_closed_when_commerce_not_co_resident", func(t *testing.T) {
		// Temporarily un-publish so the lazy client resolves no Embedded: it MUST
		// return an error (cannot verify) and NO entitlement — never a fabricated grant.
		commerce.PublishEmbedded(nil)
		t.Cleanup(func() { commerce.PublishEmbedded(emb) })

		ent, err := commerce.InProcessClient().CheckEntitlement(ctx, "maxco", "engine")
		if err == nil {
			t.Fatal("CheckEntitlement must fail closed when commerce is not co-resident")
		}
		if ent != nil {
			t.Fatalf("must not fabricate an entitlement on the fail-closed path; got %+v", ent)
		}
	})

	t.Run("empty_args_fail_closed", func(t *testing.T) {
		if _, err := client.CheckEntitlement(ctx, "", "engine"); err == nil {
			t.Error("empty orgID must error")
		}
		if _, err := client.CheckEntitlement(ctx, "acme", ""); err == nil {
			t.Error("empty productID must error")
		}
	})
}
