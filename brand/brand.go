// Package brand is the white-label registry (HIP-0111): the map from a brand id
// (and from a request Host) to that brand's PUBLIC identity — its canonical OIDC
// issuer and its serving domains.
//
// It is a LEAF (imports only strings), on purpose. Three unrelated callers need
// it and none should drag the others in: package cloud derives Config.IAMIssuer
// and validates token `iss` from it; the light webui console reads it to write
// the per-Host <title>; and cmd/cloud — the light host, which cannot import
// package cloud — reaches it through webui to brand the "/" it serves. A brand
// map that lived in package cloud would be unreachable from the host without
// linking every subsystem, so the white-label fact lives here, once.
//
// The cloud binary is one artifact serving every brand's API host (api.hanzo.ai,
// api.lux.cloud, api.zoo.cloud, api.cloud.pars.network, ...). Brand is a
// per-deployment value (CLOUD_BRAND / --brand). These facts are public (issuer
// host + brand domain), so they live in code, not in KMS.
package brand

import (
	"strings"

	"golang.org/x/net/publicsuffix"
)

// Info is the PUBLIC per-brand identity used for token validation + URL scoping.
// No secrets.
type Info struct {
	// ID is the canonical brand key.
	ID string
	// IAMIssuer is the OIDC issuer (JWKS source) for this brand — the value the
	// JWT `iss` claim must equal and whose /v1/iam/.well-known/jwks signs tokens.
	IAMIssuer string
	// Domain is the brand's primary marketing/site domain (for response scoping
	// and base-URL derivation, e.g. api.<Domain>).
	Domain string
	// AltDomains are additional registrable domains that ALSO belong to this
	// brand, used ONLY for hostname→brand white-label detection (ForHostOK).
	// A brand's real serving surfaces span more than its marketing domain — the
	// cloud console runs on <brand>.cloud hosts (console.lux.cloud,
	// console.zoo.cloud), and a request Host there must brand as Lux/Zoo, never
	// fall through to Hanzo. Base-URL/issuer scoping still uses the primary Domain.
	AltDomains []string
}

// brands is the brand→IAM registry. Keys are the canonical brand IDs accepted
// by CLOUD_BRAND. Per HIP-0111 §Brands: hanzo→hanzo.id, lux→lux.id,
// zoo→zoolabs.id (zoo.id does not resolve; the live IAM stamps iss=zoolabs.id
// — verified against /.well-known/openid-configuration), pars→pars.id,
// bootnode→id.bootno.de.
//
// IAMIssuer MUST equal the `iss` IAM actually stamps AND host the signing JWKS.
// For hanzo the live .well-known/openid-configuration on BOTH hanzo.id and
// iam.hanzo.ai reports issuer=https://hanzo.id + jwks_uri=
// https://hanzo.id/v1/iam/.well-known/jwks (iam.hanzo.ai is a routing alias, not
// the issuer), and the cloud CLI already defaults to hanzo.id. Pinning
// iam.hanzo.ai here would fail the issuer check on every real token, anonymizing
// every principal — SuperAdmin would 403 platform-wide (fail-secure, but
// broken). lux/zoo/pars already correctly point at their own .id issuers.
var brands = map[string]Info{
	"hanzo":    {ID: "hanzo", IAMIssuer: "https://hanzo.id", Domain: "hanzo.ai", AltDomains: []string{"hanzo.cloud", "hanzo.app"}},
	"lux":      {ID: "lux", IAMIssuer: "https://lux.id", Domain: "lux.network", AltDomains: []string{"lux.cloud"}},
	"zoo":      {ID: "zoo", IAMIssuer: "https://zoolabs.id", Domain: "zoo.ngo", AltDomains: []string{"zoo.network", "zoo.cloud"}},
	"pars":     {ID: "pars", IAMIssuer: "https://pars.id", Domain: "pars.network", AltDomains: []string{"pars.ai"}},
	"bootnode": {ID: "bootnode", IAMIssuer: "https://id.bootno.de", Domain: "bootno.de"},
}

// Default is the fallback brand when CLOUD_BRAND is unknown.
const Default = "hanzo"

// For returns the Info for id, falling back to the Hanzo brand for an unknown
// id. Lookup is case-insensitive.
func For(id string) Info {
	if b, ok := brands[strings.ToLower(strings.TrimSpace(id))]; ok {
		return b
	}
	return brands[Default]
}

// Registered reports whether id names a brand in the registry. It is the
// FALLIBLE half of [For], which cannot say so because it answers Hanzo for
// everything it does not know — the right default for rendering a title, and the
// wrong one for any decision that turns on which brand a caller belongs to.
// Lookup is case-insensitive, exactly like For.
func Registered(id string) bool {
	_, ok := brands[strings.ToLower(strings.TrimSpace(id))]
	return ok
}

// ForIssuer resolves the brand whose IAM minted a token, from the token's own
// verified `iss`. It is the reverse of [IssuerFor] over the SAME registry, so
// the two cannot name different brands for one issuer.
//
// It exists because the deployment's brand and the token's brand are two facts,
// not one: cloud accepts every white-label brand's issuer (trustedIssuers), so a
// process configured CLOUD_BRAND=hanzo can hold a validly-signed token minted by
// lux.id. Anything keyed by brand — a tenant key, a derived-key salt — has to be
// able to tell those apart, and it can only do that from a value the ISSUER
// signed rather than one the process assumed.
//
// ok is false for an issuer no brand claims, so a caller fails closed instead of
// silently folding an unknown issuer onto the default brand.
func ForIssuer(iss string) (string, bool) {
	iss = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(iss)), "/")
	if iss == "" {
		return "", false
	}
	for id, b := range brands {
		if strings.TrimSuffix(strings.ToLower(b.IAMIssuer), "/") == iss {
			return id, true
		}
	}
	return "", false
}

// Issuer is the ONE resolution of "which IAM signs my token": a pinned value if
// there is one, else the brand's own. It trims, because an OIDC issuer is compared
// as a literal string — a pinned "https://hanzo.id/" and a derived
// "https://hanzo.id" are two different issuers to a validator, and the one that
// trims and the one that does not were both being used.
//
// It lives HERE, in the leaf, because its two callers cannot share anything
// heavier: package cloud resolves it for Config, and the light host cmd/cloud is
// built specifically NOT to link package cloud, so it had spelled the rule again
// inline — without the trim.
func Issuer(pinned, id string) string {
	if p := strings.TrimRight(strings.TrimSpace(pinned), "/"); p != "" {
		return p
	}
	return IssuerFor(id)
}

// IssuerFor returns the canonical OIDC issuer for a brand id.
func IssuerFor(id string) string {
	return For(id).IAMIssuer
}

// ForHostOK resolves a request Host to a brand id from the same `brands`
// registry, mirroring the hostname→brand semantics of platform.ts's
// getWhiteLabelBrand: a Host at or under a brand's Domain (api.lux.network,
// lux.network) is that brand. The port is stripped and the compare is
// case-insensitive; the longest matching Domain wins so a nested brand domain is
// never shadowed by a shorter one. ok is false when NO brand domain matches, so
// the caller can choose its own fallback (the deployment brand) rather than
// silently emitting Hanzo branding on, say, a Zoo pod hit with an odd Host.
func ForHostOK(host string) (string, bool) {
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

// ForHost is ForHostOK with the Hanzo default for an unmatched Host.
func ForHost(host string) string {
	if b, ok := ForHostOK(host); ok {
		return b
	}
	return Default
}

// Display is a brand id's human display name: the id with an upper-cased first
// letter (lux → "Lux", hanzo → "Hanzo"). Derived from the id — one source of
// truth with the brands registry, no hand-maintained display list. Used to build
// the white-label console <title>.
func Display(id string) string {
	if id == "" {
		id = Default
	}
	return strings.ToUpper(id[:1]) + id[1:]
}

// Issuers returns the OIDC issuer of every configured white-label brand. The
// in-binary identity validator (auth_identity.go) trusts a token whose `iss` is
// any of these, so ONE cloud binary validates hanzo AND lux/zoo/pars tokens. One
// source of truth: derived from the same `brands` registry above.
func Issuers() []string {
	out := make([]string, 0, len(brands))
	for _, b := range brands {
		if b.IAMIssuer != "" {
			out = append(out, b.IAMIssuer)
		}
	}
	return out
}

// IssuerByHost maps every identity host this deployment serves to the issuer it
// mints under: the issuer's own host, plus each brand domain and alternate.
//
// Derived, because it was written by hand — a thirteen-entry blob duplicated in
// two deployment files, so a new brand was three edits from minting under
// another brand's issuer, and a brand's own clients reject that.
func IssuerByHost() map[string]string {
	out := make(map[string]string, len(brands)*3)
	for _, b := range brands {
		if b.IAMIssuer == "" {
			continue
		}
		if h := strings.TrimPrefix(strings.TrimPrefix(b.IAMIssuer, "https://"), "http://"); h != "" {
			out[h] = b.IAMIssuer
		}
		for _, d := range append([]string{b.Domain}, b.AltDomains...) {
			if d == "" {
				continue
			}
			// Served at the domain and at id./iam. of it — zoolabs.id and
			// id.zoo.network are one brand.
			out[d] = b.IAMIssuer
			out["id."+d] = b.IAMIssuer
			out["iam."+d] = b.IAMIssuer
		}
	}
	return out
}

// Domains returns every domain the registry knows — each brand's own and its
// alternates — in no particular order.
//
// It answers the question a caller has when it must NAME our origins rather than
// resolve one it was handed: which hosts may frame a page, which may be told an
// origin is ours. ForHost answers "is this one" for a host in hand; this is the
// enumeration, derived from the same registry, so adding a brand adds its domains
// everywhere at once instead of in one more list somebody has to find.
func Domains() []string {
	out := make([]string, 0, len(brands)*2)
	for _, b := range brands {
		if b.Domain != "" {
			out = append(out, b.Domain)
		}
		out = append(out, b.AltDomains...)
	}
	return out
}

// Apex returns the registrable apex of a host: the domain one label below the
// public suffix ("api.hanzo.ai" -> "hanzo.ai", "hanzo.ai" -> "hanzo.ai"). It is
// THE ONE derivation of that value in this binary.
//
// It exists because a deployment's SIBLING hosts are its own: the forge
// (git.<apex>), CI (ci.<apex>), CD (cd.<apex>) and the status page
// (status.<apex>) are neither the API host nor children of it, and any code that
// must decide "is this host mine?" has to reduce to the apex first. Three
// packages needed that reduction and each wrote its own, by three different
// rules that agreed only on the three brand domains we happen to run today:
//
//	platform  publicsuffix                    api.acme.co.uk -> acme.co.uk
//	sites     last two labels                 api.acme.co.uk -> co.uk      (WRONG)
//	git       TrimPrefix "api." + "git."      cloud.hanzo.ai -> git.cloud.hanzo.ai (WRONG)
//
// The sites rule hands a bare public suffix to the self-domain floor, which is
// the set that decides a host is OURS and not a tenant's to claim — so a
// white-label on any multi-label suffix claimed every domain under it. The git
// rule advertised a forge host that platform's own allowlist would then refuse,
// which is verbatim the defect dc84b46d fixed in platform alone: the fix was
// correct and incomplete, because the derivation was duplicated rather than
// shared. One copy cannot disagree with itself.
//
// publicsuffix rather than "last two labels" so a multi-label suffix (co.uk,
// com.au, github.io) yields the registrable domain and not the suffix itself,
// which would trust — or claim — every domain under it. A name with no
// registrable form (localhost, a bare IP) is returned verbatim: it is not
// delegable, so it is its own apex.
func Apex(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return ""
	}
	if i := strings.IndexByte(h, ':'); i >= 0 { // tolerate host:port
		h = h[:i]
	}
	h = strings.TrimSuffix(h, ".") // a fully-qualified Host may carry the root dot
	apex, err := publicsuffix.EffectiveTLDPlusOne(h)
	if err != nil {
		return h
	}
	return apex
}

// Sibling is the host named `label` under host's registrable apex:
// Sibling("api.hanzo.ai", "git") == "git.hanzo.ai". It is how a deployment names
// the surfaces it owns beside its API — the forge, CI, CD, status — from the one
// domain it is configured with, so those names cannot drift apart per package.
func Sibling(host, label string) string {
	apex := Apex(host)
	if apex == "" || label == "" {
		return apex
	}
	return label + "." + apex
}

// APIHost is the public API host a deployment of brand `id` answers on:
// api.<that brand's apex> — api.hanzo.ai, api.lux.network, api.zoo.ngo.
//
// It is here, beside the apex it derives from, because two packages need the
// SAME answer and neither can hold it for the other: package cloud resolves
// Config.Domain from it, and apps/sites (a leaf that must never import the root
// package) resolves the self-domain floor from it. Each used to spell the
// literal "api.hanzo.ai" for itself, so the deployment's own host was stated
// twice, brand-blind in both places, and nothing made the two agree.
func APIHost(id string) string { return "api." + For(id).Domain }

// GitHost is the code-hosting host of brand `id`: git.<that brand's apex> —
// git.hanzo.ai, git.lux.network, git.zoo.ngo. Same derivation as APIHost and
// beside it for the same reason: a caller scoping a credential to the forge must
// name the forge, and a literal would be brand-blind.
func GitHost(id string) string { return "git." + For(id).Domain }
