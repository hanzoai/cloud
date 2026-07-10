package clients

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud/types"
)

// localCommerce is the IN-PROCESS types.CommerceClient used when commerce is
// co-resident in this cloud binary (the clients/commercesvc subsystem is enabled).
// It answers the typed inter-subsystem calls with a direct Go method — no network
// hop, no HTTP round-trip — the co-resident default per HIP-0106.
//
// GetTenantConfig is complete: at this inter-subsystem layer a tenant's config IS
// its org id + the deployment brand (types.TenantConfig has exactly those two
// fields), both known to the co-resident binary without touching commerce state.
//
// CheckEntitlement FAILS CLOSED. Resolving "does org X hold an active entitlement
// for product P, and what features does its plan grant" requires commerce to expose
// its subscription→plan→features resolution (the toLicenseFeatures vocab contract);
// commerce does not export that as an in-process API yet, so this returns a clear
// error rather than a fabricated grant/deny. clients/entitlements already treats an
// erroring commerce client as fail-closed (503 "cannot verify plan") — the SPECIFIED
// secure behaviour ("cannot verify ⇒ never open") — so a non-super-admin can never
// enable an unverifiable product. Phase 2 replaces this body with the real resolver
// once commerce exports it; the seam (in-process, default) does not change.
type localCommerce struct {
	brand string
}

// LocalCommerce returns the in-process types.CommerceClient for a co-resident
// commerce. brand is the deployment's white-label brand, surfaced in TenantConfig.
func LocalCommerce(brand string) types.CommerceClient {
	return &localCommerce{brand: brand}
}

func (c *localCommerce) GetTenantConfig(_ context.Context, orgID string) (*types.TenantConfig, error) {
	return &types.TenantConfig{OrgID: orgID, Brand: c.brand}, nil
}

func (c *localCommerce) CheckEntitlement(_ context.Context, orgID, productID string) (*types.LicenseEntitlement, error) {
	return nil, fmt.Errorf("commerce: in-process entitlement resolution not available (org=%s product=%s) — plan cannot be verified", orgID, productID)
}
