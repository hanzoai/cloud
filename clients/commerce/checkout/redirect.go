package checkout

import (
	"net/url"
	"strings"
)

// AllowedCheckoutRedirect reports whether rawURL is a well-formed http(s) URL
// whose host is one the org legitimately controls, and is therefore a safe
// success/cancel redirect to bake into a minted hosted-checkout (Square) link.
//
// This is the ONE server-side redirect allowlist for the checkout money path.
// Without it, POST /v1/checkout/sessions would happily point a REAL payment
// link's post-payment redirect at any attacker host — an open-redirect /
// phishing pivot on a link that carries the org's brand and a real charge.
//
// A host is allowed when it is:
//   - one of the org's OWN registered website hosts (websiteURLs), OR
//   - a first-party host of the org's brand (returnHostsFor), OR
//   - equal to, or a subdomain of, one of that brand's registrable domains.
//
// orgName picks a brand org's (hanzo/lux/zoo/pars) own brand; any other
// (custom) org inherits the brand this commerce deployment serves, resolved
// from requestHost. Matching is exact host or registrable-domain suffix, so a
// spoof host like "hanzo.ai.evil.com" never matches "hanzo.ai".
func AllowedCheckoutRedirect(rawURL, orgName string, websiteURLs []string, requestHost string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	h := normalizeHost(u.Host)
	if h == "" {
		return false
	}

	// (1) The org's OWN registered website hosts (exact match).
	for _, w := range websiteURLs {
		if wh := redirectHost(w); wh != "" && h == wh {
			return true
		}
	}

	slug := brandSlugFor(orgName, requestHost)

	// (2) First-party hosts of the brand (exact match).
	for _, allowed := range returnHostsFor(slug) {
		if ah := redirectHost(allowed); ah != "" && h == ah {
			return true
		}
	}

	// (3) The brand's registrable domains (exact or subdomain — the same
	// exact-suffix rule brandForHost uses, so ".evil.com" suffixes can't match).
	for _, bd := range brandDomains {
		if bd.brand.slug != slug {
			continue
		}
		if h == bd.domain || strings.HasSuffix(h, "."+bd.domain) {
			return true
		}
	}
	return false
}

// brandSlugFor resolves the brand whose domains an org may redirect to. A known
// brand org maps to its own brand; any other (custom) org inherits the brand
// this deployment serves, via the same host→brand map the tenant resolver uses.
func brandSlugFor(orgName, requestHost string) string {
	switch strings.ToLower(strings.TrimSpace(orgName)) {
	case "hanzo", "lux", "zoo", "pars":
		return strings.ToLower(strings.TrimSpace(orgName))
	}
	return brandForHost(normalizeHost(requestHost)).slug
}

// redirectHost extracts a normalized host from an allowlist entry that may be a
// full URL ("https://app.hanzo.ai") or a bare host ("app.hanzo.ai"). Returns ""
// for unparseable input.
func redirectHost(entry string) string {
	s := strings.TrimSpace(entry)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return ""
	}
	return normalizeHost(u.Host)
}
