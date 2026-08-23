package plan

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/hanzoai/cloud/apps/goja"
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
	// Lower bound, not an exact pin — an exact count is a staleness magnet. The bound
	// is a FLOOR on the vocabulary the catalog must still speak, not a growth curve:
	// it read >=10 on the premise that the vocabulary only grows, and v1.4.10 falsified
	// that by retiring the base/sites product lines along with their namespaces.
	if len(body.Namespaces) < 9 {
		t.Fatalf("namespaces = %d, want >=9", len(body.Namespaces))
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

// TestEntitlements_RetiredTierIsAnError pins what happens to a plan id the catalog
// no longer sells, because the answer decides whether a retired tier fails safe.
//
// It used to assert the World tiers — world-free/pro/team/enterprise — resolve through
// the Go/goja path. @hanzo/plans carried them at v1.4.4 alongside a tier-level `bundles`
// field that granted them, and DELETED both at v1.4.11 with nothing in their place.
// World is not a separately billable product: its limits resolve to the free floor
// (apps/world/entitlement.go), which is the whole answer rather than a degraded one.
//
// What is worth keeping from that test is the client it exercised, so this asserts both
// halves: a live tier still resolves entitlements through goja, and a retired id
// ERRORS rather than answering an empty map. The distinction matters — an empty map
// reads as "this tier grants nothing", which is a confident wrong answer that would
// silently strip a paying subscriber of everything they bought.
func TestEntitlements_RetiredTierIsAnError(t *testing.T) {
	prev := host
	host = newHost(t)
	defer func() { host.Close(); host = prev }()

	ctx := context.Background()

	// A tier the catalog still sells resolves, and carries something.
	live, err := Entitlements(ctx, "enterprise")
	if err != nil {
		t.Fatalf("Entitlements(enterprise): %v", err)
	}
	if len(live) == 0 {
		t.Fatal("Entitlements(enterprise) returned an empty map — the goja path resolved nothing")
	}

	// A retired tier is refused, not answered emptily.
	for _, retired := range []string{"world-pro", "world-free", "world-enterprise"} {
		got, err := Entitlements(ctx, retired)
		if err == nil {
			t.Fatalf("Entitlements(%s) = %v with no error — a retired tier must not "+
				"answer as though it grants nothing", retired, got)
		}
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

// TestPlans_Ladder pins the commercial model on the surface GET
// /v1/plan/subscriptions serves: the personal ladder go $9 / dev $19 / pro $49 /
// max $99, and team $25 per-seat with a 2-seat minimum. Stripe lookup keys are part
// of the contract — each carries its price, so a reprice mints a new key rather than
// moving an immutable one.
//
// This is a CANARY on a money surface: it is meant to fail loudly when the catalog
// reprices, so the change is deliberate and reviewed. It last fired for real when
// plans v1.4.10 replaced the pro $20 / plus $100 / max $200 ladder — the same change
// that retired plus/team-max/custom (see paid_test.go).
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
	price := map[string]float64{"free": 0, "go": 9, "dev": 19, "pro": 49, "max": 99, "team": 25}
	lookup := map[string]string{"free": "", "go": "hanzo_go_9", "dev": "hanzo_dev_19", "pro": "hanzo_pro_49", "max": "hanzo_max_99", "team": "hanzo_team_25"}
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
// hanzo.team: a signed license for pro, max AND team must carry
// licensing.product:team, and go (the entry tier) must NOT — the gate fails
// closed for a tier that never bought team access.
func TestLicenseEntitlement_TeamProduct(t *testing.T) {
	prev := host
	host = newHost(t)
	defer func() { host.Close(); host = prev }()
	ctx := context.Background()

	for _, id := range []string{"pro", "max", "team"} {
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
	if _, feats, found, err := LicenseEntitlement(ctx, "go"); err != nil || !found {
		t.Fatalf("LicenseEntitlement(go): found=%v err=%v", found, err)
	} else if slices.Contains(feats, "licensing.product:team") {
		t.Error("go must not carry licensing.product:team")
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
