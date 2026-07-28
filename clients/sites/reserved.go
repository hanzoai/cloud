package sites

import (
	"strings"
	"sync"
)

// The reserved-subdomain policy — the ONE source of truth for which `<label>.apex`
// subdomains may NOT be published sites. It is consulted in THREE places that must
// never disagree:
//
//   - serve time (Server.siteSlug): a reserved host falls through to the normal
//     API/console pipeline instead of the site server;
//   - create time (projects.createProject): a project can never be created with a
//     reserved slug;
//   - bind time (projects Store.BindHost): the global site_hosts table can never
//     physically contain a reserved host, whatever the caller.
//
// Because create AND bind both reject, `site_hosts` can NEVER hold a reserved host,
// so even if the ingress reserved-regex ever drifts and a reserved host reaches the
// pod, ResolveHost misses and the site server 404s BEFORE any auth surface is
// shadowed. The serve gate is a backstop, not the sole guard.
//
// baseReserved is baked in and CANNOT be removed by operator config — trimming the
// env only ever ADDS via SetReservedExtra, never subtracts. It covers:
//   - the apex itself (empty label) and its canonical alias (www);
//   - infrastructure / app-host labels that route real services on the apex;
//   - auth/payment/security-sensitive labels (anti-phishing / cookie-tossing);
//   - brand terms an impersonator would want.
var baseReserved = func() map[string]bool {
	labels := []string{
		"", "www",
		// SECURITY INVARIANT — reserved ⊇ every `<label>.<apex>` the SHARED brand
		// `<brand>-app` IAM client registers as an OAuth redirect. If such a label were
		// publishable, an attacker could first-come-claim `<label>.<apex>`, serve their
		// own page, then run authorize with client_id=<brand>-app + a redirect that IS
		// on the shared client's exact-match list → the victim's code lands on the
		// attacker's page → token with aud=<brand>-app that the API trusts (account
		// takeover). `www` (above) + `stg` are the current hanzo-app redirect labels;
		// keep this in lock-step with init_data.json (TestReservedCoversSharedAppRedirects).
		"stg",
		// infrastructure / app hosts
		"api", "app", "apps", "admin", "administrator", "root", "console", "dashboard",
		"portal", "sites", "site", "host", "hosting", "internal", "gateway", "proxy",
		"router", "ingress", "cdn", "static", "assets", "mail", "smtp", "imap", "ftp",
		"ns", "ns1", "ns2", "dns", "status", "health", "healthz", "metrics", "grafana",
		// auth / payment / security-sensitive (anti-phishing, cookie-tossing surface)
		"login", "logout", "signin", "signup", "auth", "oauth", "oauth2", "sso", "session",
		"secure", "security", "account", "accounts", "verify", "verification", "token",
		"pay", "payment", "payments", "billing", "checkout", "wallet", "bank", "id",
		// Hanzo platform surfaces + brand terms
		"iam", "kms", "cloud", "hanzo", "hanzoai", "lux", "luxfi", "zoo", "zooai",
		"official", "support", "help", "team", "docs", "blog", "store", "cowork",
	}
	m := make(map[string]bool, len(labels))
	for _, l := range labels {
		m[l] = true
	}
	return m
}()

var (
	reservedMu    sync.RWMutex
	extraReserved = map[string]bool{}
)

// SetReservedExtra registers operator-supplied extra reserved labels (from
// CLOUD_SITES_RESERVED). It ADDS to baseReserved; it can never remove a baked-in
// reserved label. Called once at startup by New.
func SetReservedExtra(labels []string) {
	m := make(map[string]bool, len(labels))
	for _, l := range labels {
		if l = strings.ToLower(strings.TrimSpace(l)); l != "" {
			m[l] = true
		}
	}
	reservedMu.Lock()
	extraReserved = m
	reservedMu.Unlock()
}

// IsReserved reports whether a subdomain label may NOT be a published site. This is
// the ONE predicate every enforcement point calls, so serve/create/bind never drift.
// The comparison is on the lowercased label.
func IsReserved(label string) bool {
	label = strings.ToLower(strings.TrimSpace(label))
	if baseReserved[label] {
		return true
	}
	reservedMu.RLock()
	ok := extraReserved[label]
	reservedMu.RUnlock()
	return ok
}

// selfDomains is the registrable domains WE run — the sites apex (hanzo.app), the
// brand domain (hanzo.ai), and any operator-supplied SelfDomains. Same shared-source
// shape as the reserved labels above, for the same reason: the SERVE gate
// (Server.customCandidate) and the CLAIM gate (projects setDomains) must read ONE set
// or they drift. They did drift — serve excluded every self domain while claim
// excluded only the sites apex, so a customer could claim `api.hanzo.ai`, our
// production API host. It could never SERVE (the serve gate held), but the claim row
// is first-come, so the claim permanently DENIED the host to its real owner. A gate
// that runs only at read time cannot keep a table clean.
var selfDomains []string

// SetSelfDomains registers the domains we operate, from the same list the serve gate
// is built with. Called once at startup by New, beside SetReservedExtra.
func SetSelfDomains(domains []string) {
	out := make([]string, 0, len(domains))
	for _, d := range domains {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			out = append(out, d)
		}
	}
	reservedMu.Lock()
	selfDomains = out
	reservedMu.Unlock()
}

// IsSelfHost reports whether host is one of OUR registrable domains or anything
// beneath it. Those names are ours to assign and no DNS proof is even possible for
// them (a customer cannot publish a TXT record in a zone we run), so they are never
// claimable. The ONE self-domain predicate; serve and claim both call it.
func IsSelfHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	reservedMu.RLock()
	defer reservedMu.RUnlock()
	for _, d := range selfDomains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}
