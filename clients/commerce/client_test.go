// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/commerce/billing/grant"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	commerceorg "github.com/hanzoai/cloud/clients/commerce/pkg/org"
	plansvc "github.com/hanzoai/cloud/clients/plan"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// plansOnce mounts the @hanzo/plans goja vocabulary exactly once for the package —
// CheckEntitlement resolves a plan tier's license features through it.
var plansOnce sync.Once

func mountPlansVocab(t *testing.T) {
	t.Helper()
	plansOnce.Do(func() {
		app := zip.New(zip.Config{Logger: luxlog.New("test-plans")})
		if err := plansvc.Mount(app, cloud.Deps{Logger: luxlog.New("test-plans"), Brand: "hanzo"}); err != nil {
			t.Fatalf("plansvc.Mount: %v", err)
		}
	})
}

// fakeCatalog is the minimal grant.PlanCatalog that seeds the "max" tier — the tier
// whose @hanzo/plans entitlements carry licensing.product_ids:["engine"], so a "max"
// subscriber is entitled to the "engine" product.
type fakeCatalog struct{}

func (fakeCatalog) Lookup(slug string) *grant.CatalogPlan {
	if slug == "max" {
		return &grant.CatalogPlan{Slug: "max", Name: "Max", Description: "Max tier", PriceCents: 20000, Currency: "usd"}
	}
	return nil
}

// bootCommerce boots the embedded commerce app (SQLite fallback, dev) once per test,
// sets its brand + publishes it, and returns the handle. Embed installs the process-
// global default datastore the entitlement resolver reads via commerceorg.Resolve.
func bootCommerce(t *testing.T) *Embedded {
	t.Helper()
	emb, err := Embed(context.Background(), EmbedConfig{DataDir: t.TempDir(), Dev: true})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	emb.brand = "hanzo"
	PublishEmbedded(emb)
	t.Cleanup(func() {
		PublishEmbedded(nil)
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

func hasFeature(features []string, want string) bool {
	for _, f := range features {
		if f == want {
			return true
		}
	}
	return false
}

// TestInProcessClient exercises the REAL in-process commerce.Client end-to-end:
// GetTenantConfig, a genuinely-entitled org (Active:true with the plan's real license
// features), a resolvable-but-not-entitled org (Active:false, no error — never a
// fabricated grant), and the fail-closed path when commerce is not co-resident. It
// boots the embedded commerce once so the process-global datastore stays this test's
// for the whole run.
func TestInProcessClient(t *testing.T) {
	mountPlansVocab(t)
	emb := bootCommerce(t)
	client := emb.Client()
	ctx := context.Background()

	t.Run("GetTenantConfig_echoes_org_and_brand", func(t *testing.T) {
		tc, err := client.GetTenantConfig(ctx, "acme")
		if err != nil {
			t.Fatalf("GetTenantConfig: %v", err)
		}
		if tc == nil || tc.OrgID != "acme" || tc.Brand != "hanzo" {
			t.Fatalf("GetTenantConfig = %+v, want {OrgID:acme Brand:hanzo}", tc)
		}
	})

	t.Run("entitled_org_gets_real_features", func(t *testing.T) {
		const org = "maxco"
		seedActiveGrant(t, ctx, org, "max")

		ent, err := client.CheckEntitlement(ctx, org, "engine")
		if err != nil {
			t.Fatalf("CheckEntitlement: %v", err)
		}
		if ent == nil || !ent.Active {
			t.Fatalf("engine on a max plan must be Active; got %+v", ent)
		}
		if ent.Plan != "max" {
			t.Errorf("Plan = %q, want max", ent.Plan)
		}
		// Real features come from @hanzo/plans toLicenseFeatures — NOT fabricated.
		if !hasFeature(ent.Features, "licensing.product:engine") {
			t.Errorf("Features %v missing product-scope token licensing.product:engine", ent.Features)
		}
		if !hasFeature(ent.Features, "inference") {
			t.Errorf("Features %v missing engine_feature 'inference'", ent.Features)
		}
	})

	t.Run("entitled_org_but_unlicensed_product_is_not_active_no_error", func(t *testing.T) {
		const org = "maxco2"
		seedActiveGrant(t, ctx, org, "max")

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
		prev := published.Load()
		published.Store(nil)
		t.Cleanup(func() { published.Store(prev) })

		ent, err := InProcessClient("hanzo").CheckEntitlement(ctx, "maxco", "engine")
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
