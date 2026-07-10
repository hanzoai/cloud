// Package checkout — StoreResolver adapts a *store.Store to the Resolver
// interface so the legacy Deposits/DepositConfirm/DepositStatus/Webhook
// handlers can run off the hanzo/base-backed tenant collection without
// being rewritten.
//
// Until every legacy handler is migrated to a store-typed signature, the
// adapter projects store.Tenant → checkout.Tenant (the legacy type) on
// each lookup. Projection is read-only and stateless; no cache layer
// here — the underlying store.TenantRepo already serves from base's
// in-process cache.

package checkout

import (
	"github.com/hanzoai/cloud/clients/commerce/store"
)

// StoreResolver implements Resolver by reading from a commerce/store
// *store.Store. Lookups go through store.Tenants.FindByHostname which
// applies the same normalization rule as StaticResolver.Resolve.
type StoreResolver struct {
	S *store.Store
}

// NewStoreResolver returns a Resolver that resolves via s.
func NewStoreResolver(s *store.Store) *StoreResolver {
	return &StoreResolver{S: s}
}

// Resolve looks the host up in the store and projects the store.Tenant
// into the legacy checkout.Tenant shape. Returns ErrUnknownTenant on
// miss so callers keep the existing 404 path.
func (r *StoreResolver) Resolve(host string) (Tenant, error) {
	if r == nil || r.S == nil {
		return Tenant{}, ErrUnknownTenant
	}
	st, err := r.S.Tenants.FindByHostname(host)
	if err != nil || st == nil {
		return Tenant{}, ErrUnknownTenant
	}
	return tenantFromStore(st), nil
}

// tenantFromStore projects store.Tenant → checkout.Tenant. Hostnames are
// not copied — the Resolver answered the host lookup already. Provider
// secret fields stay zero — credentials flow via KMS, not the store.
func tenantFromStore(t *store.Tenant) Tenant {
	providers := make([]Provider, 0, len(t.Providers))
	for _, p := range t.Providers {
		providers = append(providers, Provider{
			Name:    p.Name,
			Enabled: p.Enabled,
			KMSPath: p.KMSPath,
		})
	}
	return Tenant{
		Name: t.Name,
		Brand: Brand{
			DisplayName:  t.Brand.DisplayName,
			LogoURL:      t.Brand.LogoURL,
			PrimaryColor: t.Brand.PrimaryColor,
		},
		IAM: IAMConfig{
			Issuer:   t.IAM.Issuer,
			ClientID: t.IAM.ClientID,
		},
		IDV: IDVConfig{
			Provider:       t.IDV.Provider,
			Endpoint:       t.IDV.Endpoint,
			RequiredFields: t.IDV.RequiredFields,
		},
		Providers:          providers,
		ReturnURLAllowlist: append([]string(nil), t.ReturnURLAllowlist...),
		Backend: BackendConfig{
			Kind: "bd",
			URL:  t.BDEndpoint,
		},
	}
}
