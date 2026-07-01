package plan

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hanzoai/cloud/clients/gojahost"
	hplans "github.com/hanzoai/plans"
)

// newHost loads the REAL @hanzo/plans goja bundle + embedded catalog, so this
// test exercises the actual entitlements.mjs port running in goja.
func newHost(t *testing.T) *gojahost.Host {
	t.Helper()
	bundle, err := hplans.Bundle()
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	data, err := hplans.Data()
	if err != nil {
		t.Fatalf("Data: %v", err)
	}
	h, err := gojahost.New(gojahost.Config{
		Name:    "plans",
		Bundle:  bundle,
		Globals: map[string]any{"__PLANS_DATA__": data},
	})
	if err != nil {
		t.Fatalf("gojahost.New: %v", err)
	}
	return h
}

func TestPlans_Vocab(t *testing.T) {
	h := newHost(t)
	defer h.Close()
	resp, err := h.Dispatch(context.Background(), gojahost.Request{Route: "vocab", Tenant: "hanzo"})
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
	if len(body.Namespaces) != 9 {
		t.Fatalf("namespaces = %d, want 9", len(body.Namespaces))
	}
	if len(body.Keys) < 40 {
		t.Fatalf("entitlement keys = %d, want >=40", len(body.Keys))
	}
}

func TestPlans_ResolveProducesLicenseFeatures(t *testing.T) {
	h := newHost(t)
	defer h.Close()
	resp, err := h.Dispatch(context.Background(), gojahost.Request{Route: "resolve", Tenant: "hanzo", Params: map[string]string{"id": "pro"}})
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

func TestPlans_Resolve404(t *testing.T) {
	h := newHost(t)
	defer h.Close()
	resp, err := h.Dispatch(context.Background(), gojahost.Request{Route: "resolve", Tenant: "hanzo", Params: map[string]string{"id": "does-not-exist"}})
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
	resp, err := h.Dispatch(context.Background(), gojahost.Request{Route: "subscriptions", Tenant: "acme-reseller"})
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
