// Package link is the unified AI login manager's registry: the org+user-scoped
// record of WHICH provider accounts (Claude Max, ChatGPT Plus, a Hanzo API key,
// a raw provider key) a developer has signed into, ON WHICH MACHINES, with each
// account's latest usage snapshot. It is the cross-machine view console renders
// as "AI Providers / Accounts" and the source the redundancy route policy reads.
//
// THE ATOM is a Link: one (user, device, provider, account) binding plus its
// kind, status, last-seen, and the latest usage snapshot. A device is the
// (machine, host, os) projection shared by a machine's Links — not a separate
// stored entity, so there is no device/link join and no orphan-device GC. A
// user's accounts across every machine are just that user's Links.
//
// NO SECRET LIVES HERE. The provider's OAuth token / API key stays device-local
// (exactly as @hanzo/usage keeps it, reading each provider's own login to meter
// usage); the registry holds LINK METADATA + usage snapshots only. The collector
// (@hanzo/usage's reporter) registers a Link and pushes its usage using the SAME
// IAM bearer it already carries — never a provider secret.
//
// BILLING FOLLOWS THE ACCOUNT, not the registry. This store never touches
// commerce — it holds no metering client, so it is structurally incapable of
// creating a charge. A subscription account's inference bills the user's monthly
// plan (metered here for visibility only); an api-key / hanzo account's inference
// bills via commerce on the existing gateway path, unchanged. Each Link carries
// the mode that says which (BillingMode), so a usage event's billing is explicit.
//
// ISOLATION: org is the tenant key and user is the owner key; every read and
// write leads with `org=? AND user=?` bound predicates, so a caller sees and
// mutates only their OWN accounts within their OWN org — fail-closed.
package link

import "strings"

// Link kinds — how an account is credentialed, which decides how its usage bills.
const (
	// KindSubscription is a provider account signed in with the user's own
	// subscription login (Claude Max, ChatGPT Plus). Its inference bills the
	// user's monthly plan; the registry meters it for visibility only.
	KindSubscription = "subscription"
	// KindAPIKey is an account credentialed by an API key (a raw provider key,
	// or a Hanzo hk- key). Its inference bills via commerce on the gateway path.
	KindAPIKey = "apikey"
)

// Link statuses. linked is active; revoked is a logged-out account whose sessions
// were stopped. A revoked Link is retained (not deleted) so its usage history and
// the audit trail survive a log-out.
const (
	StatusLinked  = "linked"
	StatusRevoked = "revoked"
)

// Billing modes — the value a Link and a route candidate carry so "how does this
// account's usage bill" is explicit, never inferred at the charge site.
const (
	// BillingPlan: the user's own subscription pays; NO commerce charge.
	BillingPlan = "plan"
	// BillingCommerce: usage bills via commerce (the gateway meter), as today.
	BillingCommerce = "commerce"
)

// Field bounds. Every string is a short identifier or label, never a document;
// the caps keep a hostile client from bloating the per-org SQLite and match the
// session plane's discipline.
const (
	maxUser     = 256
	maxMachine  = 256
	maxHost     = 256
	maxOS       = 64
	maxProvider = 64
	maxAccount  = 256
	maxPlan     = 128
	maxUsage    = 8 * 1024 // a usage snapshot is a handful of numbers, not a blob
)

// Link is one (user, device, provider, account) binding. Tenant isolation is the
// (org, user) pair, enforced on every query. It never stores a secret.
type Link struct {
	ID       string
	Org      string
	User     string // the owning subject (validated principal); a user sees only their own
	Machine  string // stable machine id (the device key)
	Host     string // hostname (device label)
	OS       string // platform label (darwin|linux|windows|…)
	Provider string // matches @hanzo/usage providerRegistry id (claude|codex|hanzo|openai|…)
	Account  string // the subscription/account id or label (e.g. an account email)
	Plan     string // the plan name from the provider identity (e.g. "Claude Max"); display only
	Kind     string // subscription | apikey
	Status   string // linked | revoked
	LastSeen int64  // unix; bumped on every usage report
	Usage    string // JSON of the latest Usage projection ("" until first report)

	CreatedAt int64
	UpdatedAt int64
}

// Usage is the projection of a provider's @hanzo/usage UsageSnapshot the collector
// pushes and the dashboard renders: the rate-limit windows, token totals, and
// spend. It is stored as JSON on the Link and parsed for the route policy's
// headroom. Money is USD cents end-to-end. Spend is always 0 for a pure
// subscription account (there is no per-call charge — the plan is flat).
type Usage struct {
	SessionPct   float64 `json:"sessionPct"`             // primary window used %, 0..100
	WeeklyPct    float64 `json:"weeklyPct"`              // secondary window used %, 0..100
	ResetsAt     string  `json:"resetsAt,omitempty"`     // primary window reset, RFC3339
	Tokens       int64   `json:"tokens"`                 // absolute token total when known
	InputTokens  int64   `json:"inputTokens,omitempty"`  //
	OutputTokens int64   `json:"outputTokens,omitempty"` //
	SpendCents   int64   `json:"spendCents"`             // provider spend (0 for a subscription)
	Currency     string  `json:"currency,omitempty"`     //
	Confidence   string  `json:"confidence,omitempty"`   // exact|estimated|percentOnly|unknown
	UpdatedAt    string  `json:"updatedAt,omitempty"`    // snapshot time, RFC3339
}

// validKind reports whether k is a known link kind.
func validKind(k string) bool { return k == KindSubscription || k == KindAPIKey }

// BillingMode returns how an account of this kind bills its usage: a subscription
// bills the user's plan (no commerce charge), everything else bills via commerce.
// It is the ONE place the subscription-vs-api-key distinction is decided, so a
// usage event's billing is a pure function of the account, never re-derived.
func BillingMode(kind string) string {
	if kind == KindSubscription {
		return BillingPlan
	}
	return BillingCommerce
}

// headroomPct is the remaining capacity of the tighter of a snapshot's two
// rate-limit windows (100 - the higher used%), clamped to [0,100]. A Link with no
// usage snapshot is treated as fully available (100) — absence of data is not
// evidence of exhaustion. This is the value the route policy orders candidates by.
func headroomPct(u *Usage) float64 {
	if u == nil {
		return 100
	}
	used := u.SessionPct
	if u.WeeklyPct > used {
		used = u.WeeklyPct
	}
	h := 100 - used
	if h < 0 {
		return 0
	}
	if h > 100 {
		return 100
	}
	return h
}

// clampPct bounds a percent to [0,100]; a provider that reports a nonsense value
// can never poison the headroom ordering.
func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// trim is strings.TrimSpace, aliased for terse boundary code.
func trim(s string) string { return strings.TrimSpace(s) }
