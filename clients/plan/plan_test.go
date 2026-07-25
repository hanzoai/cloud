package plan

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/hanzoai/cloud/clients/goja"
	hplans "github.com/hanzoai/plans"
)

// newHost loads the REAL @hanzo/plans goja bundle + embedded catalog, so this
// test exercises the actual entitlements.mjs port running in goja.
func newHost(t *testing.T) *goja.Host {
	t.Helper()
	bundle, err := hplans.Bundle()
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	data, err := hplans.Data()
	if err != nil {
		t.Fatalf("Data: %v", err)
	}
	h, err := goja.New(goja.Config{
		Name:    "plans",
		Bundle:  bundle,
		Globals: map[string]any{"__PLANS_DATA__": data},
	})
	if err != nil {
		t.Fatalf("goja.New: %v", err)
	}
	return h
}

func TestPlans_Vocab(t *testing.T) {
	h := newHost(t)
	defer h.Close()
	resp, err := h.Dispatch(context.Background(), goja.Request{Route: "vocab", Tenant: "hanzo"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	var body struct {
		Namespaces []string       `json:"namespaces"`
		Keys       map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Lower bound, not an exact pin: the entitlement vocabulary only GROWS as the
	// @hanzo/plans catalog adds products (10 at v1.4.1, 12 at v1.4.4), so an exact
	// count is a staleness magnet — matches the `Keys < 40` lower bound just below.
	if len(body.Namespaces) < 10 {
		t.Fatalf("namespaces = %d, want >=10", len(body.Namespaces))
	}
	if len(body.Keys) < 40 {
		t.Fatalf("entitlement keys = %d, want >=40", len(body.Keys))
	}
}

func TestPlans_ResolveProducesLicenseFeatures(t *testing.T) {
	h := newHost(t)
	defer h.Close()
	resp, err := h.Dispatch(context.Background(), goja.Request{Route: "resolve", Tenant: "hanzo", Params: map[string]string{"id": "pro"}})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.Status != 200 {
		t.Fatalf("status = %d, want 200 (body=%s)", resp.Status, resp.Body)
	}
	var body struct {
		ID              string         `json:"id"`
		TenantID        string         `json:"tenant_id"`
		Entitlements    map[string]any `json:"entitlements"`
		LicenseFeatures []string       `json:"license_features"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.ID != "pro" || body.TenantID != "hanzo" {
		t.Fatalf("id/tenant = %q/%q", body.ID, body.TenantID)
	}
	if len(body.Entitlements) == 0 {
		t.Fatal("expected non-empty entitlements for pro")
	}
	if body.LicenseFeatures == nil {
		t.Fatal("expected license_features array (the engine gate input)")
	}
}

// TestEntitlements_WorldTiers exercises the exported Entitlements seam against the
// REAL embedded @hanzo/plans module, proving the bumped catalog serves the World
// tiers with the new world.model_api gate through the Go/goja path (not just node).
func TestEntitlements_WorldTiers(t *testing.T) {
	prev := host
	host = newHost(t)
	defer func() { host.Close(); host = prev }()

	ctx := context.Background()

	// world-pro: model API granted.
	pro, err := Entitlements(ctx, "world-pro")
	if err != nil {
		t.Fatalf("Entitlements(world-pro): %v", err)
	}
	if pro["world.model_api"] != true {
		t.Fatalf("world-pro world.model_api = %v, want true", pro["world.model_api"])
	}

	// world-free: model API denied (key absent -> fail closed).
	free, err := Entitlements(ctx, "world-free")
	if err != nil {
		t.Fatalf("Entitlements(world-free): %v", err)
	}
	if _, present := free["world.model_api"]; present {
		t.Fatalf("world-free must not carry world.model_api, got %v", free["world.model_api"])
	}

	// world-enterprise: new contact tier, unlimited API rate.
	ent, err := Entitlements(ctx, "world-enterprise")
	if err != nil {
		t.Fatalf("Entitlements(world-enterprise): %v", err)
	}
	if ent["world.api_rate_limit"] != float64(-1) {
		t.Fatalf("world-enterprise world.api_rate_limit = %v, want -1", ent["world.api_rate_limit"])
	}
	if ent["world.model_api"] != true {
		t.Fatalf("world-enterprise world.model_api = %v, want true", ent["world.model_api"])
	}
}

func TestEntitlements_UnknownPlanErrors(t *testing.T) {
	prev := host
	host = newHost(t)
	defer func() { host.Close(); host = prev }()
	if _, err := Entitlements(context.Background(), "does-not-exist"); err == nil {
		t.Fatal("expected error for unknown plan id")
	}
}

// TestPlans_Ladder pins the hanzo.team commercial model (plans v1.4.1) on the
// surface GET /v1/plans/subscriptions serves: the personal ladder pro $20 /
// plus $100 / max $200, and team $25 per-seat with a 2-seat minimum. Stripe
// lookup keys are part of the contract — pro moved to hanzo_pro_20 (the old
// hanzo_pro stays on the immutable $49 price).
func TestPlans_Ladder(t *testing.T) {
	h := newHost(t)
	defer h.Close()
	resp, err := h.Dispatch(context.Background(), goja.Request{Route: "subscriptions", Tenant: "hanzo"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	var body struct {
		Plans []struct {
			ID           string  `json:"id"`
			PriceMonthly float64 `json:"priceMonthly"`
			Limits       struct {
				MinSeats float64 `json:"minSeats"`
			} `json:"limits"`
			PriceRef struct {
				Recurring struct {
					PerSeat         bool   `json:"per_seat"`
					StripeLookupKey string `json:"stripe_lookup_key"`
				} `json:"recurring"`
			} `json:"price_ref"`
		} `json:"plans"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	price := map[string]float64{"pro": 20, "plus": 100, "max": 200, "team": 25}
	lookup := map[string]string{"pro": "hanzo_pro_20", "plus": "hanzo_plus", "max": "hanzo_max", "team": "hanzo_team_25"}
	seen := map[string]bool{}
	for _, p := range body.Plans {
		want, ok := price[p.ID]
		if !ok {
			continue
		}
		seen[p.ID] = true
		if p.PriceMonthly != want {
			t.Errorf("%s priceMonthly = %v, want %v", p.ID, p.PriceMonthly, want)
		}
		if p.PriceRef.Recurring.StripeLookupKey != lookup[p.ID] {
			t.Errorf("%s stripe lookup = %q, want %q", p.ID, p.PriceRef.Recurring.StripeLookupKey, lookup[p.ID])
		}
		if p.ID == "team" {
			if !p.PriceRef.Recurring.PerSeat {
				t.Error("team must price per seat")
			}
			if p.Limits.MinSeats != 2 {
				t.Errorf("team limits.minSeats = %v, want 2", p.Limits.MinSeats)
			}
		}
	}
	for id := range price {
		if !seen[id] {
			t.Errorf("plan %q missing from subscriptions", id)
		}
	}
}

// TestLicenseEntitlement_TeamProduct is the entitlement gate contract for
// hanzo.team: a signed license for pro, plus, max AND team must carry
// licensing.product:team, and developer (free) must NOT — the gate fails
// closed for a tier that never bought team access.
func TestLicenseEntitlement_TeamProduct(t *testing.T) {
	prev := host
	host = newHost(t)
	defer func() { host.Close(); host = prev }()
	ctx := context.Background()

	for _, id := range []string{"pro", "plus", "max", "team"} {
		ents, feats, found, err := LicenseEntitlement(ctx, id)
		if err != nil {
			t.Fatalf("LicenseEntitlement(%s): %v", id, err)
		}
		if !found {
			t.Fatalf("LicenseEntitlement(%s): plan not found", id)
		}
		if !slices.Contains(feats, "licensing.product:team") {
			t.Errorf("%s license_features = %v, want licensing.product:team", id, feats)
		}
		if id != "team" && ents["team.guests"] != float64(3) {
			t.Errorf("%s team.guests = %v, want 3", id, ents["team.guests"])
		}
	}
	if _, feats, found, err := LicenseEntitlement(ctx, "max"); err != nil || !found {
		t.Fatalf("LicenseEntitlement(max): found=%v err=%v", found, err)
	} else if !slices.Contains(feats, "licensing.product:engine") {
		t.Errorf("max license_features = %v, want licensing.product:engine", feats)
	}
	if _, feats, found, err := LicenseEntitlement(ctx, "developer"); err != nil || !found {
		t.Fatalf("LicenseEntitlement(developer): found=%v err=%v", found, err)
	} else if slices.Contains(feats, "licensing.product:team") {
		t.Error("developer must not carry licensing.product:team")
	}
}

func TestPlans_Resolve404(t *testing.T) {
	h := newHost(t)
	defer h.Close()
	resp, err := h.Dispatch(context.Background(), goja.Request{Route: "resolve", Tenant: "hanzo", Params: map[string]string{"id": "does-not-exist"}})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.Status != 404 {
		t.Fatalf("status = %d, want 404", resp.Status)
	}
}

func TestPlans_TenantScopingFallsBackToHanzo(t *testing.T) {
	h := newHost(t)
	defer h.Close()
	// A reseller with no overrides sees the hanzo default catalog.
	resp, err := h.Dispatch(context.Background(), goja.Request{Route: "subscriptions", Tenant: "acme-reseller"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	var body struct {
		Plans []map[string]any `json:"plans"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Plans) == 0 {
		t.Fatal("reseller should fall back to hanzo default catalog (non-empty)")
	}
}
