package clients

import (
	"context"
	"testing"
)

// TestLocalCommerceGetTenantConfig proves the in-process client answers tenant
// config directly (no network hop) with the org echoed and the deployment brand —
// the two fields types.TenantConfig carries. This is the co-resident default
// build.go wires via CommerceInProcess(LocalCommerce(brand)) when commerce is
// enabled.
func TestLocalCommerceGetTenantConfig(t *testing.T) {
	c := LocalCommerce("hanzo")
	tc, err := c.GetTenantConfig(context.Background(), "acme")
	if err != nil {
		t.Fatalf("GetTenantConfig: %v", err)
	}
	if tc == nil {
		t.Fatal("GetTenantConfig returned nil config")
	}
	if tc.OrgID != "acme" {
		t.Errorf("OrgID = %q, want %q", tc.OrgID, "acme")
	}
	if tc.Brand != "hanzo" {
		t.Errorf("Brand = %q, want %q", tc.Brand, "hanzo")
	}
}

// TestLocalCommerceCheckEntitlementFailsClosed proves the entitlement gate fails
// CLOSED: until commerce exports its subscription→plan→features resolver the
// in-process client returns an error and NO entitlement, so clients/entitlements
// refuses a non-super-admin product enable (503 "cannot verify plan") rather than
// fabricating a grant. "Cannot verify ⇒ never open" is the specified secure default.
func TestLocalCommerceCheckEntitlementFailsClosed(t *testing.T) {
	c := LocalCommerce("hanzo")
	ent, err := c.CheckEntitlement(context.Background(), "acme", "engine")
	if err == nil {
		t.Fatal("CheckEntitlement returned nil error — must fail closed until commerce exports resolution")
	}
	if ent != nil {
		t.Errorf("CheckEntitlement returned a non-nil entitlement %+v — must never fabricate a grant", ent)
	}
}

// TestCommerceInProcessIdentity proves the CommerceInProcess wrapper is a typed
// pass-through: the impl handed in is the client handed back (zero marshalling,
// direct Go dispatch), the property build.go relies on when it wraps LocalCommerce.
func TestCommerceInProcessIdentity(t *testing.T) {
	impl := LocalCommerce("hanzo")
	if got := CommerceInProcess(impl); got != impl {
		t.Error("CommerceInProcess did not return the same impl (must be a pass-through)")
	}
}
