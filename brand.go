package cloud

import "strings"

// Brand white-label registry (HIP-0111).
//
// The cloud binary is one artifact serving every brand's API host
// (api.hanzo.ai, api.lux.cloud, api.zoo.cloud, api.cloud.pars.network, ...).
// Brand is a per-deployment value (CLOUD_BRAND / --brand). This registry maps a
// brand to its PUBLIC IAM facts — the canonical OIDC issuer the deployment must
// validate JWTs against. These are public (issuer host + brand domain), so they
// live in code, not in KMS.
//
// One source of truth: nothing else in the binary hardcodes a per-brand issuer.
// Config.IAMIssuer is derived from here when the operator does not pin one, so a
// lux deployment validates against lux.id, a zoo deployment against zoo.id, etc.,
// instead of silently defaulting every brand to iam.hanzo.ai.

// BrandInfo is the PUBLIC per-brand identity used for token validation + URL
// scoping. No secrets.
type BrandInfo struct {
	// ID is the canonical brand key.
	ID string
	// IAMIssuer is the OIDC issuer (JWKS source) for this brand — the value the
	// JWT `iss` claim must equal and whose /v1/iam/.well-known/jwks signs tokens.
	IAMIssuer string
	// Domain is the brand's primary marketing/site domain (for response scoping).
	Domain string
}

// brands is the brand→IAM registry. Keys are the canonical brand IDs accepted
// by CLOUD_BRAND. Per HIP-0111 §Brands: hanzo→hanzo.id, lux→lux.id,
// zoo→zoo.id, pars→pars.id, bootnode→id.bootno.de.
//
// IAMIssuer MUST equal the `iss` IAM actually stamps AND host the signing JWKS.
// For hanzo the live .well-known/openid-configuration on BOTH hanzo.id and
// iam.hanzo.ai reports issuer=https://hanzo.id + jwks_uri=
// https://hanzo.id/v1/iam/.well-known/jwks (iam.hanzo.ai is a routing alias, not
// the issuer), and the cloud CLI already defaults to hanzo.id. Pinning
// iam.hanzo.ai here would fail the issuer check on every real token, anonymizing
// every principal — global admin would 403 platform-wide (fail-secure, but
// broken). lux/zoo/pars already correctly point at their own .id issuers.
var brands = map[string]BrandInfo{
	"hanzo":    {ID: "hanzo", IAMIssuer: "https://hanzo.id", Domain: "hanzo.ai"},
	"lux":      {ID: "lux", IAMIssuer: "https://lux.id", Domain: "lux.network"},
	"zoo":      {ID: "zoo", IAMIssuer: "https://zoo.id", Domain: "zoo.ngo"},
	"pars":     {ID: "pars", IAMIssuer: "https://pars.id", Domain: "pars.network"},
	"bootnode": {ID: "bootnode", IAMIssuer: "https://id.bootno.de", Domain: "bootno.de"},
}

// DefaultBrand is the fallback brand when CLOUD_BRAND is unknown.
const DefaultBrand = "hanzo"

// BrandFor returns the BrandInfo for id, falling back to the Hanzo brand for an
// unknown id. Lookup is case-insensitive.
func BrandFor(id string) BrandInfo {
	if b, ok := brands[strings.ToLower(strings.TrimSpace(id))]; ok {
		return b
	}
	return brands[DefaultBrand]
}

// IssuerForBrand returns the canonical OIDC issuer for a brand id.
func IssuerForBrand(id string) string {
	return BrandFor(id).IAMIssuer
}
