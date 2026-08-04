package sites

import (
	"os"
	"strings"
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
// binding can never shadow a real Hanzo host. Empty ⇒ read CLOUD_DOMAIN.
func ConfigFromEnv(domain string) Config {
	apex := env("CLOUD_SITES_APEX", "hanzo.app")
	if domain = strings.TrimSpace(domain); domain == "" {
		domain = env("CLOUD_DOMAIN", "api.hanzo.ai")
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
		FirstPartyApex:  env("CLOUD_SITES_FIRSTPARTY_APEX", "hanzo.ai"),
		FirstPartySites: list("CLOUD_SITES_FIRSTPARTY", "cd", "flow", "gallery"),
		FirstPartyOrg:   env("CLOUD_SITES_FIRSTPARTY_ORG", "hanzo"),
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

// registrableDomain returns the last two dot-separated labels of a host (a
// pragmatic "registrable domain" without a public-suffix list): api.hanzo.ai →
// hanzo.ai, hanzo.app → hanzo.app. A host with fewer than two labels is returned
// unchanged. This only seeds the self-domain exclusion set; it never gates org
// isolation (which is the S3-prefix boundary in the serve path).
func registrableDomain(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	parts := strings.Split(strings.Trim(host, "."), ".")
	if len(parts) < 2 {
		return host
	}
	return strings.Join(parts[len(parts)-2:], ".")
}

// env reads a non-blank environment value, else def.
func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

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
