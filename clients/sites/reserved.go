package sites

import (
	"sort"
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

// ReservedLabels returns the sorted union of baked-in and operator-configured
// reserved labels (the empty apex label omitted). Exposed for diagnostics / tests.
func ReservedLabels() []string {
	reservedMu.RLock()
	defer reservedMu.RUnlock()
	set := make(map[string]bool, len(baseReserved)+len(extraReserved))
	for l := range baseReserved {
		if l != "" {
			set[l] = true
		}
	}
	for l := range extraReserved {
		set[l] = true
	}
	out := make([]string, 0, len(set))
	for l := range set {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}
