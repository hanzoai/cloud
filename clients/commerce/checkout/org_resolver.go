// Package checkout — OrgResolver is the canonical tenant resolver: the IAM org
// IS the tenant. There is no separate commerce-tenant registry to seed or drift.
//
// Resolution is host → brand → IAM org slug → Organization → public Tenant:
//   - brandForHost maps a request host to its brand + IAM app (pay.hanzo.ai →
//     hanzo, pay.lux.network → lux, …). Unknown hosts fall back to the
//     deployment's default org (COMMERCE_DEFAULT_TENANT, default "hanzo").
//   - the org's public Square config (application id + location + environment)
//     is resolved by the SAME authority as the charge path
//     (payment.SquarePublicConfig) and projected into the tenant JSON, so the
//     pay SPA's card iframe initializes with the exact application commerce
//     will charge — no build-time VITE_* env, no per-host seed row.
//
// Resolution never 404s for a well-formed host: a missing org row degrades to
// the brand defaults + env Square config, so "add credits" always renders.
package checkout

import (
	"os"
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/payment"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

// OrgLoader loads an Organization by its IAM slug (read-only). It is injected
// by the binary so this package stays decoupled from datastore wiring; a nil
// loader (or a miss) degrades to a brand-default synthetic org whose Square
// config resolves from the deployment's per-brand env (the cloud-org path).
//
// WARNING: Resolve runs on /v1/commerce/tenant — a PUBLIC, unauthenticated
// endpoint the pay SPA hits on every boot. Any real loader wired here MUST be
// cached AND deadline-bounded. A naive per-request DB query under an unbounded
// context blocks and can exhaust the connection pool (regression at 1.42.44).
// nil is the correct default: pure host→brand→env, no I/O, no hang.
type OrgLoader func(slug string) (*organization.Organization, bool)

// OrgResolver implements Resolver by mapping the Host header to an IAM org and
// projecting that org into a public Tenant.
type OrgResolver struct {
	load OrgLoader
}

// NewOrgResolver returns a Resolver backed by the IAM org model. load may be
// nil (every host resolves to a brand-default synthetic org + env Square).
func NewOrgResolver(load OrgLoader) *OrgResolver {
	return &OrgResolver{load: load}
}

// Resolve maps host → brand → org and projects a public Tenant. It returns
// ErrUnknownTenant only for a malformed Host (empty / control bytes); every
// well-formed host resolves (known brand or the deployment default).
func (r *OrgResolver) Resolve(host string) (Tenant, error) {
	h := normalizeHost(host)
	if h == "" {
		return Tenant{}, ErrUnknownTenant
	}
	b := brandForHost(h)

	var org *organization.Organization
	if r.load != nil {
		if o, ok := r.load(b.slug); ok && o != nil {
			org = o
		}
	}
	if org == nil {
		// No org row yet — the brand's env-backed Square config still resolves.
		org = &organization.Organization{}
		org.Name = b.slug
	}

	sq := payment.SquarePublicConfig(org)

	return Tenant{
		Name: b.slug,
		Brand: Brand{
			DisplayName:  b.displayName,
			LogoURL:      b.logoURL,
			PrimaryColor: b.primaryColor,
		},
		IAM: IAMConfig{
			Issuer:   b.iamIssuer,
			ClientID: b.iamClientID,
		},
		Providers:          enabledProviders(),
		ReturnURLAllowlist: returnHostsFor(b.slug),
		Square: SquarePublic{
			ApplicationID: sq.ApplicationID,
			LocationID:    sq.LocationID,
			Environment:   sq.Environment,
		},
	}, nil
}

// returnHostsFor is the per-brand allowlist of first-party app hosts the pay
// SPA may bounce ?return= back to after checkout/onboarding. The pay matcher
// (isAllowedReturnUrl) requires an EXACT https hostname match, so these are
// full origins, not wildcards — every entry is a brand-controlled app host.
// Bounding ?return= to them keeps open-redirect phishing off the table while
// letting a user return to the app that sent them here (e.g. the playground
// flow: playground.hanzo.bot → pay.hanzo.ai/onboard → back to playground).
//
// This is intentionally a static first-party list, not org-model data: the
// resolver runs nil-loader on the PUBLIC /v1/commerce/tenant path (no
// per-request DB read — see the OrgLoader warning above), and these hosts are
// deploy-stable. Add a host here when a new first-party app must round-trip
// through pay.
func returnHostsFor(slug string) []string {
	switch slug {
	case "hanzo":
		return []string{
			"https://playground.hanzo.bot",
			"https://hanzo.bot",
			"https://console.hanzo.ai",
			"https://cloud.hanzo.ai",
			"https://chat.hanzo.ai",
			"https://hanzo.chat",
			"https://analytics.hanzo.ai",
			"https://platform.hanzo.ai",
			"https://bot.hanzo.ai",
			"https://billing.hanzo.ai",
			"https://s3.hanzo.ai",
			"https://hanzo.ai",
			"https://www.hanzo.ai",
			// hanzo.agency — the agency marketing/onboarding site. Its
			// onboarding checkout mints a real Square link via the authed
			// storefront BFF and bounces the buyer to
			// https://hanzo.agency/onboarding-success on success. A distinct
			// registrable domain, so it belongs in the brand's first-party
			// host list (returnHostsFor), not brandDomains.
			"https://hanzo.agency",
			"https://www.hanzo.agency",
		}
	case "lux":
		return []string{
			"https://lux.network",
			"https://lux.id",
			"https://lux.finance",
			"https://console.lux.network",
		}
	case "zoo":
		return []string{
			"https://zoo.ngo",
			"https://zoo.network",
			"https://zoo.id",
		}
	case "pars":
		return []string{
			"https://pars.network",
			"https://pars.id",
			"https://pars.vote",
		}
	default:
		return nil
	}
}

// enabledProviders is the payment-method surface the pay SPA renders, honoring
// the deploy-wide disabled policy. Square is the fiat card processor (gated on
// the Stripe-off policy so "Square-only fiat" stays consistent with the charge
// path); crypto (MPC) and bank wire are always available. Stripe is NEVER
// surfaced for new charges.
func enabledProviders() []Provider {
	out := make([]Provider, 0, 3)
	if !processor.DisabledByPolicy(processor.Square) {
		out = append(out, Provider{Name: "square", Enabled: true})
	}
	out = append(out, Provider{Name: "crypto", Enabled: true})
	out = append(out, Provider{Name: "wire", Enabled: true})
	return out
}

// ─── Brand map (ONE place) ───────────────────────────────────────────────────

// brand is the per-brand tenant defaults: the IAM org slug, display branding,
// and the IAM app the SPA signs in against. These mirror the commerce
// deployment's IAM_ISSUER / IAM_ACCEPTED_ISSUERS / IAM_ACCEPTED_AUDIENCES.
type brand struct {
	slug         string
	displayName  string
	logoURL      string
	primaryColor string
	iamIssuer    string
	iamClientID  string
}

// brandDomains maps a registrable brand domain to its brand. Matching is
// exact-suffix (host == domain OR host endsWith "."+domain) so a spoof host
// like "pay.hanzo.ai.evil.com" can never match "hanzo.ai".
var brandDomains = []struct {
	domain string
	brand  brand
}{
	{"hanzo.ai", brandHanzo},
	{"hanzo.id", brandHanzo},
	{"hanzo.network", brandHanzo},
	{"hanzo.chat", brandHanzo},
	{"hanzo.computer", brandHanzo},
	{"lux.network", brandLux},
	{"lux.id", brandLux},
	{"lux.finance", brandLux},
	{"zoo.ngo", brandZoo},
	{"zoo.network", brandZoo},
	{"zoo.id", brandZoo},
	{"pars.network", brandPars},
	{"pars.id", brandPars},
	{"pars.vote", brandPars},
}

var (
	brandHanzo = brand{slug: "hanzo", displayName: "Hanzo", primaryColor: "#ffffff", iamIssuer: "https://hanzo.id", iamClientID: "hanzo-app"}
	brandLux   = brand{slug: "lux", displayName: "Lux", primaryColor: "#ffffff", iamIssuer: "https://lux.id", iamClientID: "lux-app"}
	brandZoo   = brand{slug: "zoo", displayName: "Zoo", primaryColor: "#ffffff", iamIssuer: "https://zoolabs.id", iamClientID: "zoo-app"}
	brandPars  = brand{slug: "pars", displayName: "Pars", primaryColor: "#ffffff", iamIssuer: "https://pars.id", iamClientID: "pars-app"}
)

// brandForHost resolves a normalized host to its brand. Unknown hosts fall back
// to the deployment default org (COMMERCE_DEFAULT_TENANT, default "hanzo") so a
// same-cluster host that isn't a public brand domain still resolves to the org
// this commerce serves.
func brandForHost(host string) brand {
	for _, bd := range brandDomains {
		if host == bd.domain || strings.HasSuffix(host, "."+bd.domain) {
			return bd.brand
		}
	}
	return defaultBrand()
}

// defaultBrand is the deployment's fallback tenant. It is brandHanzo unless
// COMMERCE_DEFAULT_TENANT names another known brand (so a lux/zoo/pars-only
// commerce deploy can flip its default without code changes).
func defaultBrand() brand {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("COMMERCE_DEFAULT_TENANT"))) {
	case "lux":
		return brandLux
	case "zoo":
		return brandZoo
	case "pars":
		return brandPars
	default:
		return brandHanzo
	}
}
