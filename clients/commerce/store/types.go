// Package store is the hanzo/base-backed persistence seam for commerce.
//
// This package replaces the scattered hanzoai/datastore-go + bespoke model
// packages with a single, typed repository facade over a base.Collection set.
// The first collection migrated is `commerce_tenants` — other collections
// (orders, products, etc.) follow the same shape: Go model struct + typed
// repo + base migration. The legacy `commerce/datastore` package coexists
// during migration and is removed collection-by-collection.
//
// Security posture:
//   - Every repo method is scoped by the caller (handler derives tenant from
//     IAM session claims — never from the body). The store itself does not
//     enforce tenancy; it is the authoritative backing store and the
//     handler layer is the trust boundary.
//   - JSON columns (brand/iam/idv/providers/return_url_allowlist) are
//     validated at the handler boundary against the canonical Go types in
//     this file. Malformed JSON lands as a 400 at the handler, not a 500
//     deep inside the store.
//   - Secret fields on Provider (access_token, webhook_signature_key, etc.)
//     flow to KMS out-of-band; they never live in this store. The
//     Provider struct here intentionally has no credential fields.
package store

import "time"

// Tenant is the canonical in-memory shape backed by the `commerce_tenants`
// collection. Hostnames is exact-match only after normalization (lowercase,
// trailing dot stripped, port stripped) — suffix-match spoofing is
// rejected by design. See checkout/tenant.go normalizeHost for the rule.
type Tenant struct {
	ID                 string      `json:"id"`
	Name               string      `json:"name"`
	Hostnames          []string    `json:"hostnames"`
	Brand              BrandConfig `json:"brand"`
	IAM                IAMConfig   `json:"iam"`
	IDV                IDVConfig   `json:"idv"`
	Providers          []Provider  `json:"providers"`
	BDEndpoint         string      `json:"bd_endpoint"`
	ReturnURLAllowlist []string    `json:"return_url_allowlist"`
	Created            time.Time   `json:"created"`
	Updated            time.Time   `json:"updated"`
}

// BrandConfig is the SPA-rendered visible surface.
type BrandConfig struct {
	DisplayName  string `json:"display_name"`
	LogoURL      string `json:"logo_url"`
	PrimaryColor string `json:"primary_color"`
}

// IAMConfig points the SPA at the tenant's Hanzo IAM app. Only Issuer and
// ClientID are safe to surface publicly; they already ship in the OIDC
// well-known doc. No client secret — commerce never needs it; the confidential
// client flow runs in the tenant's own BD, not in commerce.
type IAMConfig struct {
	Issuer   string `json:"issuer"`
	ClientID string `json:"client_id"`
}

// IDVConfig is opaque to commerce; the SPA renders the redirect.
type IDVConfig struct {
	Provider       string   `json:"provider"`
	Endpoint       string   `json:"endpoint"`
	RequiredFields []string `json:"required_fields,omitempty"`
}

// Provider is a payment provider configured for the tenant. Credentials are
// stored in KMS under commerce/{tenant}/{provider}/{field} — this struct
// holds only the enable flag + a KMS reference (not the secret itself).
type Provider struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	KMSPath string `json:"kms_path,omitempty"`
}
