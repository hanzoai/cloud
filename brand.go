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
	// Domain is the brand's primary marketing/site domain (for response scoping
	// and base-URL derivation, e.g. api.<Domain>).
	Domain string
	// AltDomains are additional registrable domains that ALSO belong to this
	// brand, used ONLY for hostname→brand white-label detection (BrandForHostOK).
	// A brand's real serving surfaces span more than its marketing domain — the
	// cloud console runs on <brand>.cloud hosts (console.lux.cloud,
	// console.zoo.cloud), and a request Host there must brand as Lux/Zoo, never
	// fall through to Hanzo. Base-URL/issuer scoping still uses the primary Domain.
	AltDomains []string
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
// every principal — SuperAdmin would 403 platform-wide (fail-secure, but
// broken). lux/zoo/pars already correctly point at their own .id issuers.
var brands = map[string]BrandInfo{
	"hanzo":    {ID: "hanzo", IAMIssuer: "https://hanzo.id", Domain: "hanzo.ai", AltDomains: []string{"hanzo.cloud", "hanzo.app"}},
	"lux":      {ID: "lux", IAMIssuer: "https://lux.id", Domain: "lux.network", AltDomains: []string{"lux.cloud"}},
	"zoo":      {ID: "zoo", IAMIssuer: "https://zoo.id", Domain: "zoo.ngo", AltDomains: []string{"zoo.network", "zoo.cloud"}},
	"pars":     {ID: "pars", IAMIssuer: "https://pars.id", Domain: "pars.network", AltDomains: []string{"pars.ai"}},
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

// BrandForHostOK resolves a request Host to a brand id from the same `brands`
// registry, mirroring the hostname→brand semantics of platform.ts's
// getWhiteLabelBrand: a Host at or under a brand's Domain (api.lux.network,
// lux.network) is that brand. The port is stripped and the compare is
// case-insensitive; the longest matching Domain wins so a nested brand domain is
// never shadowed by a shorter one. ok is false when NO brand domain matches, so
// the caller can choose its own fallback (the deployment brand) rather than
// silently emitting Hanzo branding on, say, a Zoo pod hit with an odd Host.
func BrandForHostOK(host string) (string, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	// A fully-qualified Host may carry a trailing root dot ("api.lux.network.");
	// strip it so the suffix match still resolves the brand instead of failing to
	// neutral.
	host = strings.TrimSuffix(host, ".")
	best, bestLen := "", -1
	for id, b := range brands {
		for _, d := range append([]string{b.Domain}, b.AltDomains...) {
			d = strings.ToLower(d)
			if d == "" {
				continue
			}
			if (host == d || strings.HasSuffix(host, "."+d)) && len(d) > bestLen {
				best, bestLen = id, len(d)
			}
		}
	}
	return best, best != ""
}

// BrandForHost is BrandForHostOK with the Hanzo default for an unmatched Host.
func BrandForHost(host string) string {
	if b, ok := BrandForHostOK(host); ok {
		return b
	}
	return DefaultBrand
}

// brandDisplay is a brand id's human display name: the id with an upper-cased
// first letter (lux → "Lux", hanzo → "Hanzo"). Derived from the id — one source
// of truth with the brands registry, no hand-maintained display list. Used to
// build the white-label console <title> (webui.go).
func brandDisplay(id string) string {
	if id == "" {
		id = DefaultBrand
	}
	return strings.ToUpper(id[:1]) + id[1:]
}

// BrandIssuers returns the OIDC issuer of every configured white-label brand. The
// in-binary identity validator (auth_identity.go) trusts a token whose `iss` is
// any of these, so ONE cloud binary validates hanzo AND lux/zoo/pars tokens. One
// source of truth: derived from the same `brands` registry above.
func BrandIssuers() []string {
	out := make([]string, 0, len(brands))
	for _, b := range brands {
		if b.IAMIssuer != "" {
			out = append(out, b.IAMIssuer)
		}
	}
	return out
}

// BrandAudiences returns the OAuth `aud` (== IAM client_id == app name) of every
// white-label brand's cloud login app: `<brand>-cloud` (hanzo-cloud, lux-cloud,
// zoo-cloud, pars-cloud, bootnode-cloud). A brand's session token carries
// aud=<brand>-cloud (HIP-0111: client_id == app == aud), so the audience allowlist
// must include each to accept a lux/zoo/pars token on the ONE binary. Derived from
// the same `brands` registry as BrandIssuers — one source of truth, no hand-listing.
func BrandAudiences() []string {
	out := make([]string, 0, len(brands))
	for id := range brands {
		if id != "" {
			out = append(out, id+"-cloud")
		}
	}
	return out
}
