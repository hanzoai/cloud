package sites

import (
	"os"
	"strings"

	"github.com/hanzoai/cloud/brand"
	"github.com/hanzoai/cloud/internal/environ"
)

// The ONE resolution of the site-edge configuration.
//
// Two processes mount this middleware: the light router that owns the public port
// (cmd/cloud) and cloud.Listen in every per-app child. Each used to resolve Config
// for itself, from two SPELLINGS of the first-party keys — CLOUD_SITES_FIRSTPARTY_*
// against CLOUD_SITES_FIRST_PARTY_* — and two sets of defaults, so which policy
// applied depended on which process a request happened to reach. The edge resolved
// no first-party apex and therefore no brand self-domain, leaving every hanzo.ai
// host a custom-domain candidate: the per-request binding lookup that the
// self-domain exclusion exists to keep off the api/console path ran on api.hanzo.ai
// itself. The key names and the defaults now live here, in the package that owns
// the type, and both call sites read this function.
//
// domain is the deployment's primary API host (api.hanzo.ai) — the caller resolves
// it, because it is the deployment's fact and not this package's. It seeds the
// self-domain exclusion with its registrable domain (hanzo.ai), so a customer
// binding can never shadow a real Hanzo host. Empty ⇒ read CLOUD_DOMAIN, and
// failing that derive api.<brand apex> through the SAME brand.APIHost the root
// Config uses — this file used to spell the literal "api.hanzo.ai" for itself,
// which made the deployment's own host a fact stated in two places, brand-blind
// in both.
func ConfigFromEnv(domain string) Config {
	apex := environ.Or("CLOUD_SITES_APEX", "hanzo.app")
	if domain = strings.TrimSpace(domain); domain == "" {
		domain = environ.Or("CLOUD_DOMAIN", brand.APIHost(environ.Or("CLOUD_BRAND", brand.Default)))
	}
	return Config{
		Apex: apex,
		// Operator EXTRAS only. The baked-in denylist (reserved.go) is the floor and
		// is not expressible here: trimming this var can never un-reserve a label.
		Reserved: list("CLOUD_SITES_RESERVED"),
		// Operator EXTRAS only, for the same reason and by the same rule. These
		// names decide whether a host is OURS — the sole unconditional gate on the
		// site_hosts table (projects Store.bindHost asks sites.Ours) and the serve
		// gate's self-host exclusion. Drop one and every `<label>.<that domain>`
		// becomes a tenant's to claim: a first-come row on `api.hanzo.ai` that
		// denies our own host to us for good, which is verbatim the defect
		// SetSelfDomains exists to close.
		//
		// The list used to WIN OUTRIGHT over the derived set, so an operator adding
		// a vanity domain silently subtracted the brand domain — and nothing would
		// have noticed, because hanzo.ai reaches this set by DERIVATION from
		// CLOUD_DOMAIN and no deployment states it. A set whose job is to deny must
		// not be expressible as a replacement.
		SelfDomains:     append(selfFloor(apex, domain), list("CLOUD_SITES_SELF_DOMAINS")...),
		FirstPartyApex:  environ.Or("CLOUD_SITES_FIRSTPARTY_APEX", "hanzo.ai"),
		FirstPartySites: list("CLOUD_SITES_FIRSTPARTY", "cd", "flow", "gallery"),
		FirstPartyOrg:   environ.Or("CLOUD_SITES_FIRSTPARTY_ORG", "hanzo"),
	}
}

// selfFloor derives the self domains this deployment ALWAYS holds, from the sites
// apex and the primary API domain: hanzo.app plus the registrable domain of
// api.hanzo.ai (hanzo.ai). It is a floor, not a default — config adds to it and can
// never subtract from it, exactly as CLOUD_SITES_RESERVED only ever adds to
// baseReserved (reserved.go).
func selfFloor(apex, domain string) []string {
	out := make([]string, 0, 3)
	seen := map[string]bool{}
	add := func(d string) {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	add(apex)
	add(registrableDomain(apex))
	add(registrableDomain(domain))
	return out
}

// registrableDomain is brand.Apex.
//
// It was "the last two dot-separated labels", described as a pragmatic
// registrable domain without a public-suffix list. It is not pragmatic here: its
// output seeds SelfDomains, the set that decides a host is OURS and therefore not
// a tenant's to claim, and on any multi-label suffix the last two labels ARE the
// suffix — api.acme.co.uk yielded "co.uk", so a white-label deployment on one
// claimed every domain under it. The three brands we run today (hanzo.ai,
// lux.network, zoo.ngo) are all single-label suffixes, which is why nothing
// noticed. brand.Apex is the same reduction platform and git need, done once.
func registrableDomain(host string) string { return brand.Apex(host) }

// list splits a comma-separated environment value, dropping blanks, else def.
// Blanks MUST drop: an empty label is the apex itself, so a trailing comma would
// otherwise reserve — or self-claim — the whole zone.
func list(k string, def ...string) []string {
	out := make([]string, 0, len(def))
	for _, v := range strings.Split(os.Getenv(k), ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
}
