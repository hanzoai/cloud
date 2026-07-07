// onboarding.go — PURE org-naming + reserved-name policy, no transport/IAM. A
// faithful Go port of console's src/lib/server/onboarding.ts, decomplected from
// the handler so the naming rules are one testable thing (the route does the IAM
// calls; this decides the slug). Two concerns:
//
//   - NAMING: turn a human org name (or a username) into a valid IAM org slug —
//     lowercase, [a-z0-9-], collapsed, trimmed, bounded.
//   - RESERVED: refuse names that must never become a customer org — IAM system
//     owners (admin/built-in/app) and the brand/staff orgs (hanzo/lux/zoo/pars),
//     which the OrgGate routes to the admin host. Creating one would collide with
//     a staff tenant or a system principal.
package console

import "strings"

// reservedOrgs are brand/staff orgs + IAM system owners — never a customer org.
// Matches onboarding.ts RESERVED_ORGS exactly (kept as a set for O(1) lookup).
var reservedOrgs = map[string]bool{
	// IAM system owners
	"admin":    true,
	"built-in": true,
	"app":      true,
	// brand/staff orgs (the OrgGate sends these to the admin host)
	"hanzo": true,
	"lux":   true,
	"zoo":   true,
	"pars":  true,
}

const (
	// maxOrgSlug bounds a slug (IAM org name is varchar(100); keep it short/readable).
	maxOrgSlug = 60
	// minOrgSlug is the minimum length after normalization.
	minOrgSlug = 2
)

// slugifyOrg normalizes a human name to an IAM org slug: lowercase ASCII, runs of
// non-alphanumerics → '-', collapse repeats, trim leading/trailing '-', cap at
// maxOrgSlug (re-trimming a trailing '-' the slice may reveal). Returns "" when the
// input has no usable characters. Mirrors onboarding.ts slugifyOrg (Go has no NFKD
// normalize in std without golang.org/x/text; ASCII inputs — the real org-name
// case — are handled identically, and any non-ASCII rune is treated as a separator,
// which is the same net effect NFKD+[^a-z0-9] produces for the slug).
func slugifyOrg(input string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(input) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > maxOrgSlug {
		out = strings.TrimRight(out[:maxOrgSlug], "-")
	}
	return out
}

// personalOrgSlug is the default personal-org slug for a user. An email-like
// username collapses to its local part (dave@x.com → dave) so a personal org reads
// as the person, not their address; everything else is slugified as-is.
func personalOrgSlug(username string) string {
	base := username
	if at := strings.IndexByte(username, '@'); at > 0 {
		base = username[:at]
	}
	return slugifyOrg(base)
}

// isReservedOrg reports whether a slug is a brand/staff or system org — never
// creatable.
func isReservedOrg(slug string) bool { return reservedOrgs[slug] }

// orgNameResult is the outcome of validating a requested org name: a creatable slug
// or an honest client-facing error.
type orgNameResult struct {
	ok    bool
	slug  string
	error string
}

// validateOrgName validates + normalizes a requested org name into a creatable
// slug, or an honest error. The ONE place the create rules live (handler + tests
// share it). Mirrors onboarding.ts validateOrgName.
func validateOrgName(input string) orgNameResult {
	slug := slugifyOrg(input)
	if len(slug) < minOrgSlug {
		return orgNameResult{ok: false, error: "Use at least 2 letters or numbers."}
	}
	if isReservedOrg(slug) {
		return orgNameResult{ok: false, error: "“" + slug + "” is reserved. Choose a different name."}
	}
	return orgNameResult{ok: true, slug: slug}
}
