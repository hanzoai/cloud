package cloud

import "strings"

// Brand white-label registry (HIP-0111).
//
// The cloud binary is one artifact serving any brand. Brand is a per-deployment
// value (CLOUD_BRAND / --brand); this registry maps a request Host to the brand
// whose name the response carries (Server header, console title). These are
// public brand domains, so they live in code.

// BrandInfo is the PUBLIC per-brand identity used for hostname→brand detection.
// No secrets.
type BrandInfo struct {
	// ID is the canonical brand key.
	ID string
	// Domain is the brand's primary domain.
	Domain string
	// AltDomains are additional registrable domains that ALSO belong to this
	// brand, used ONLY for hostname→brand detection (BrandForHostOK).
	AltDomains []string
}

// brands is the brand registry. Keys are the canonical brand IDs accepted by
// CLOUD_BRAND.
var brands = map[string]BrandInfo{
	"hanzo":    {ID: "hanzo", Domain: "hanzo.ai", AltDomains: []string{"hanzo.cloud", "hanzo.app"}},
	"lux":      {ID: "lux", Domain: "lux.network", AltDomains: []string{"lux.cloud"}},
	"zoo":      {ID: "zoo", Domain: "zoo.ngo", AltDomains: []string{"zoo.network", "zoo.cloud"}},
	"pars":     {ID: "pars", Domain: "pars.network", AltDomains: []string{"pars.ai"}},
	"bootnode": {ID: "bootnode", Domain: "bootno.de"},
}

// DefaultIAMIssuer is the issuer a local Hanzo IAM answers as: `iam serve
// --http http://127.0.0.1:8000` on the same machine. CLOUD_IAM_ISSUER points
// the binary at any other IAM.
const DefaultIAMIssuer = "http://127.0.0.1:8000"

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
