package admin

import "github.com/hanzoai/cloud/apps/admin/core"

// Request and response shapes for /v1/admin/*. Each mirrors the operator's api.ts
// contract (admin/apps/operator/src/lib/api.ts) field-for-field — the JSON tags ARE the
// contract, so the operator's TypeScript types decode these one-to-one.
//
// Every op's Out is the /v1 envelope itself, spelled out per op rather than shared,
// because the envelope's `data` is what makes an op's contract specific and a Go generic
// over it produces an OpenAPI schema name no $ref can address. The four fields are always
// the same:
//
//	status — core.OK, or core.Err with msg set (still HTTP 200; see core.Err)
//	msg    — the failure reason, or an advisory note on a successful read
//	data   — the payload, null when the read failed
//	total  — the row count of a LIST read; absent on a single-value read
//
// The cross-cutting SourceStatus (freshness of one upstream) lives in clients/admin/core
// (core.SourceStatus) because revenue/finance/analytics share it; overview embeds it.

// adminMe is the operator identity (AdminMe / GET /v1/admin/me).
type adminMe struct {
	// Owner is the IAM org this operator belongs to: the reserved admin org for a
	// SuperAdmin, the tenant's own slug for a white-label admin. It is read off the
	// validated principal the gate just checked, never from a client claim.
	Owner string `json:"owner"`
	// Name is the IAM user name — the login handle, unique within Owner.
	Name string `json:"name"`
	// Email is the operator's email, and the actor an audited admin write is
	// recorded under.
	Email string `json:"email"`
	// DisplayName is the label to render. It carries the same value as Name today:
	// this answer is assembled from the sanitized identity headers, which carry no
	// separate display name.
	DisplayName string `json:"displayName"`
	// IsSuperAdmin is the platform-sudo fact: true iff Owner is the reserved admin
	// org. That is IAM's IsSuperAdmin predicate and the ONE thing every
	// cross-tenant surface checks — an org's own isAdmin is a different power and
	// never grants it.
	IsSuperAdmin bool `json:"isSuperAdmin"`
	// IsWhiteLabel marks the admitted NON-super tier: an admin of an enabled
	// white-label tenant org. Mutually exclusive with IsSuperAdmin (the gate lets
	// exactly one tier through). The operator SPA reads it to render the SUBTREE
	// cockpit — the fleet god-view nav (finance/revenue/metrics/o11y/providers) is
	// hidden — while a super sees the whole fleet.
	IsWhiteLabel bool `json:"isWhiteLabel"`
	// ScopeOrgs is the caller's visible tenant window: empty for a SuperAdmin (means
	// ALL orgs), or the WL tenant's own subtree (today the singleton {org}). The SPA
	// threads it through the faceting/drill-down layer so a WL tenant can never widen
	// a filter past their subtree.
	ScopeOrgs []string `json:"scopeOrgs,omitempty"`
}

// meOut is the GET /v1/admin/me envelope.
type meOut struct {
	// Status is "ok" or "error". An error here is still HTTP 200 — the transport
	// reads this field, not the status line.
	Status string `json:"status"`
	// Msg is the failure reason when Status is "error", empty otherwise.
	Msg string `json:"msg"`
	// Data is the caller's own identity and tenant window. Null when Status is
	// "error".
	Data *adminMe `json:"data"`
}

// overviewData is the fleet overview tiles (OverviewData / GET /v1/admin/overview).
// Every counter below is an honest zero when the upstream behind it did not answer,
// which is indistinguishable from an idle fleet — so read sources[] first.
type overviewData struct {
	// Orgs is how many tenants are in the caller's window: every org for a
	// SuperAdmin, their own subtree for a white-label admin.
	Orgs int `json:"orgs"`
	// Users is the member count summed over those orgs. An org whose IAM read
	// failed contributes zero rather than failing the tile.
	Users int `json:"users"`
	// Products is how many workloads the operator is observing across the platform
	// namespaces — one per App CR.
	Products int `json:"products"`
	// ActiveProducts is how many of those the operator reconciled to green health.
	// The gap against Products is everything not green, unknown health included.
	ActiveProducts int `json:"activeProducts"`
	// Drift is how many workloads the operator flagged as drifting — its declared
	// image and what is actually running disagree. It counts the operator's own
	// verdict; this layer does not re-derive it.
	Drift int `json:"drift"`
	// SpendCents30d is AI spend over the trailing 30 days, in US cents, folded from
	// the usage ledger (one row per served request).
	SpendCents30d int64 `json:"spendCents30d"`
	// Tokens30d is total tokens served over that same 30-day window.
	Tokens30d int64 `json:"tokens30d"`
	// CreditsCents is the credit balance customers still HOLD — the liability, in
	// US cents, summed over the window from the commerce wallet. It is not revenue
	// and not spend.
	CreditsCents int64 `json:"creditsCents"`
	// LastSync is when this answer was assembled, RFC 3339 UTC. admin caches
	// nothing — the read IS the sync — so it is the time of this request and never
	// of an earlier batch.
	LastSync string `json:"lastSync"`
	// Sources is one row per upstream this board fanned out to — iam, commerce,
	// usage, o11y, fleet — saying whether it answered, with how many rows, and why
	// not. It is what tells a zero above apart from a silence.
	Sources []core.SourceStatus `json:"sources"`
}

// overviewOut is the GET /v1/admin/overview envelope.
type overviewOut struct {
	// Status is "ok" or "error", at HTTP 200 either way. This board answers "ok"
	// even when upstreams are down — a tile board that fails whole because one
	// source is unreachable is useless — so a degraded read is reported in
	// data.sources, not here.
	Status string `json:"status"`
	// Msg is the failure reason when Status is "error", empty otherwise.
	Msg string `json:"msg"`
	// Data is the tile board. Null only when the caller was refused outright.
	Data *overviewData `json:"data"`
}

// orgRow is one tenant row (OrgRow / GET /v1/admin/orgs).
//
// This panel carries no sources[] channel, so a per-org read that failed degrades
// THAT column to zero rather than reporting itself.
type orgRow struct {
	// Org is the tenant slug — the key every other admin read joins on.
	Org string `json:"org"`
	// Display is the org's display name, falling back to the slug when IAM holds
	// none, so it is always renderable.
	Display string `json:"display"`
	// Users is the tenant's member count, from IAM's own total.
	Users int `json:"users"`
	// Products is always 0. The fleet workload registry is not attributed to
	// tenants yet, and a count of the platform's own workloads on a tenant row
	// would be a number about somebody else.
	Products int `json:"products"`
	// SpendCents is the tenant's AI spend over the trailing 30 days, in US cents,
	// from the usage ledger. A tenant with no rows in that window reads a true zero.
	SpendCents int64 `json:"spendCents"`
	// CreditsCents is the credit balance the tenant still holds, in US cents, from
	// the commerce wallet. What they have left, not what they were given.
	CreditsCents int64 `json:"creditsCents"`
	// Tokens is the tokens served for this tenant over that same 30-day window.
	Tokens int64 `json:"tokens"`
	// Created is when the org was created, as IAM records it (RFC 3339).
	Created string `json:"created"`
}

// orgsOut is the GET /v1/admin/orgs envelope. total == len(data): the directory is the
// caller's whole tenant window, unpaginated.
type orgsOut struct {
	// Status is "ok" or "error", at HTTP 200 either way.
	Status string `json:"status"`
	// Msg is the failure reason when Status is "error", empty otherwise.
	Msg string `json:"msg"`
	// Data is one row per tenant, sorted by slug. Null when the read failed, [] when
	// the caller's window is genuinely empty.
	Data []orgRow `json:"data"`
	// Total is the whole directory's row count BEFORE paging, so a console can page
	// against it. Absent when the read failed, since there is then nothing to count.
	Total *int `json:"total,omitempty"`
}

// orgsIn is the GET /v1/admin/orgs query. The directory pages because each ROW
// costs per-org reads, so an unpaged directory costs O(orgs) round trips on every
// load — 684 tenants today against the eighty-one this fan-out was written for.
type orgsIn struct {
	// Page is the 1-based page number. Defaults to "1".
	Page string `json:"p"`
	// PageSize is rows per page. Defaults to "200", the shared admin page size.
	// It bounds the fan-out: the page decides how many per-org reads happen, so
	// the directory costs the same at eighty tenants and at eight thousand.
	PageSize string `json:"pageSize"`
}

// usersIn is the GET /v1/admin/users query.
type usersIn struct {
	// Org narrows the directory to ONE tenant. Honoured for a SuperAdmin only — a
	// white-label admin is pinned to their own org and this is ignored.
	Org string `json:"org"`
	// Query is a free-text filter, matched by IAM as a "contains" over the user name.
	Query string `json:"q"`
	// Page is the 1-based page number. Defaults to "1"; IAM returns zero rows AND a
	// zero total when it is unset, so this layer never leaves it empty.
	Page string `json:"p"`
	// PageSize is rows per page. Defaults to "200", the shared admin page size.
	PageSize string `json:"pageSize"`
}

// usersOut is the GET /v1/admin/users envelope. total is IAM's REAL total across all
// pages, not len(data) — it is what the console pages against.
type usersOut struct {
	// Status is "ok" or "error", at HTTP 200 either way.
	Status string `json:"status"`
	// Msg is the failure reason when Status is "error", empty otherwise.
	Msg string `json:"msg"`
	// Data is ONE page of users. Null when the read failed.
	Data []operatorUser `json:"data"`
	// Total is IAM's count across ALL pages, not len(data) — it is what the console
	// pages against. Absent when the read failed.
	Total *int `json:"total,omitempty"`
}

// iamPageIn is the query shared by the verbatim IAM reads (roles, applications).
type iamPageIn struct {
	// Owner is the org whose rows to read. Defaults to the admin org, which owns the
	// platform's roles and applications.
	Owner string `json:"owner"`
	// Page is the 1-based page number. Forwarded only when set — IAM applies its own
	// default otherwise.
	Page string `json:"p"`
	// PageSize is rows per page. Forwarded only when set.
	PageSize string `json:"pageSize"`
}

// iamRowsOut is the envelope of a verbatim IAM read. `data` is IAM's own payload,
// forwarded byte-for-byte and therefore declared opaque: describing it here would be a
// second copy of IAM's schema, free to drift from the one IAM actually serves.
type iamRowsOut struct {
	// Status is "ok" or "error", at HTTP 200 either way.
	Status string `json:"status"`
	// Msg is the failure reason when Status is "error", empty otherwise.
	Msg string `json:"msg"`
	// Data is IAM's own row array, forwarded byte-for-byte and therefore left
	// undescribed here: IAM's schema has one home, and a second copy of it in this
	// document would be free to drift from the one IAM serves. An absent page is
	// [], never null.
	Data any `json:"data"`
	// Total is IAM's count across all pages. Absent when the read failed.
	Total *int `json:"total,omitempty"`
}

// operatorUser is one user in the cross-org directory (OperatorUser / GET
// /v1/admin/users).
type operatorUser struct {
	// Owner is the org this user belongs to. A user belongs to exactly one, so this
	// is also the tenant column of the directory.
	Owner string `json:"owner"`
	// Name is the IAM user name — the login handle, unique within Owner.
	Name string `json:"name"`
	// Email is the user's email address.
	Email string `json:"email"`
	// DisplayName is the human name IAM holds. Often empty; render Name when it is.
	DisplayName string `json:"displayName"`
	// IsAdmin is admin OF THEIR OWN ORG — they manage that org's users and apps. It
	// is not platform privilege and grants nothing outside Owner.
	IsAdmin bool `json:"isAdmin"`
	// IsSuperAdmin is platform sudo: true iff Owner is the reserved admin org. It is
	// derived here from Owner, which is the one predicate — never read off the user
	// row, so a tenant cannot set it on themselves.
	IsSuperAdmin bool `json:"isSuperAdmin"`
	// Tag is IAM's free-form label on the user. Empty unless the tenant uses it.
	Tag string `json:"tag"`
	// Created is when the account was created, as IAM records it (RFC 3339).
	Created string `json:"created"`
	// LastSignin is the last successful sign-in, as IAM records it. Empty for an
	// account that has never signed in — which is what an unused invite looks like.
	LastSignin string `json:"lastSignin"`
	// Forbidden is true when IAM has the account blocked: it exists and cannot sign
	// in. Distinct from deleted, which simply would not appear here.
	Forbidden bool `json:"forbidden"`
}

// usage roll-up (UsageData / GET /v1/admin/usage). Every number below is folded from
// the AI usage ledger, one row per served request, over the trailing 30 days.
type usageTotals struct {
	// SpendCents is what those requests cost, in US cents.
	SpendCents int64 `json:"spendCents"`
	// Tokens is prompt plus completion tokens across them.
	Tokens int64 `json:"tokens"`
	// Requests is how many were served — including the ones that errored, which
	// still consumed the call.
	Requests int64 `json:"requests"`
}

// usagePoint is one DAY of that window.
type usagePoint struct {
	// Date is the bucket's calendar day, YYYY-MM-DD in UTC. A day, not an instant:
	// the bucket covers the whole of it.
	Date string `json:"date"`
	// SpendCents is that day's cost, in US cents.
	SpendCents int64 `json:"spendCents"`
	// Tokens is that day's token count.
	Tokens int64 `json:"tokens"`
	// Requests is that day's served-request count.
	Requests int64 `json:"requests"`
}

// usageByModel is one model's slice of the window. The ledger's revenue-bearing
// dimension IS the model — there is no product column, and naming one implied a split
// this plane cannot make.
type usageByModel struct {
	// Model is the model id the ledger recorded. Rows the ledger left unattributed
	// are dropped rather than rendered as a nameless slice.
	Model string `json:"model"`
	// SpendCents is this model's share of the window's cost, in US cents.
	SpendCents int64 `json:"spendCents"`
	// Tokens is this model's share of the tokens.
	Tokens int64 `json:"tokens"`
}

// usageData is the three views of ONE window: the fold, the daily curve, and the split
// by model. They describe the same rows, so they cannot disagree about the window.
type usageData struct {
	// Totals is the whole window folded into one row.
	Totals usageTotals `json:"totals"`
	// Series is that window bucketed by day, oldest first. A day with no traffic has
	// no point — the series is not padded.
	Series []usagePoint `json:"series"`
	// ByModel is the window split by model, busiest first, capped at the top ten so
	// the ranking stays honest without paying for a tail nothing draws.
	ByModel []usageByModel `json:"byModel"`
}

// usageIn is the GET /v1/admin/usage query.
type usageIn struct {
	// Org reads ONE tenant's trailing-30-day total instead of the fleet sum. Honoured
	// for a SuperAdmin only — a white-label admin always reads their own org.
	//
	// The window is the one core.OrgMoney returns, and it is what the operator board
	// beside this already labelled ("Daily, last 30 days"). The wire used to say
	// month-to-date while that UI said 30 days; they agree now. This comment is
	// REGENERATED into plugin/admin/openapi.json and openapi.yaml as the ?org
	// parameter description, so a stale word here ships as a contradiction inside
	// one spec file — which is the drift this whole change set exists to remove.
	Org string `json:"org"`
}

// usageOut is the GET /v1/admin/usage envelope.
type usageOut struct {
	// Status is "ok" or "error", at HTTP 200 either way.
	Status string `json:"status"`
	// Msg is the failure reason when Status is "error", empty otherwise.
	Msg string `json:"msg"`
	// Data is the usage roll-up. A warehouse that is not connected yields zeros
	// here, not null — which reads as an idle fleet, so use the overview's
	// sources[] to tell the two apart.
	Data *usageData `json:"data"`
}

// syncStarted acknowledges the "Sync now" button. There is no job id because there is no
// job: the read that follows is the sync.
type syncStarted struct {
	// Started is always true on a successful call. There is nothing to poll and
	// nothing to cancel: the next read is the refresh.
	Started bool `json:"started"`
}

// syncOut is the POST /v1/admin/sync envelope.
type syncOut struct {
	// Status is "ok" or "error", at HTTP 200 either way.
	Status string `json:"status"`
	// Msg is the failure reason when Status is "error", empty otherwise.
	Msg string `json:"msg"`
	// Data is the acknowledgement. Null when the caller was refused.
	Data *syncStarted `json:"data"`
}

// productsIn is the GET /v1/admin/products query. Each filter is an exact match against
// the corresponding productRow field; empty means "every value".
type productsIn struct {
	// Kind matches the operator App CR's declared spec.role (sql|kv|generic|ingress).
	Kind string `json:"kind"`
	// Tier matches the derived infra grouping (cloud|data|edge|daemon|paas|app).
	Tier string `json:"tier"`
	// Env matches the lifecycle namespace (main|test|dev).
	Env string `json:"env"`
}

// productsOut is the GET /v1/admin/products envelope. total == len(data): the registry is
// the whole observed fleet after filtering, unpaginated.
type productsOut struct {
	// Status is "ok" or "error", at HTTP 200 either way.
	Status string `json:"status"`
	// Msg is the failure reason when Status is "error" — here, that the PaaS plane
	// could not be reached at all. A plane that answers with nothing observed yet is
	// a success carrying an empty list, which is a different fact.
	Msg string `json:"msg"`
	// Data is the matching workload rows. Null when the read failed.
	Data []productRow `json:"data"`
	// Total is len(data): the registry is the whole observed fleet after filtering,
	// unpaginated. Absent when the read failed.
	Total *int `json:"total,omitempty"`
}

// rangeIn is the time window shared by the warehouse-backed boards (o11y, aimetrics,
// analytics, compute).
type rangeIn struct {
	// Range is the lower time bound: 24h, 7d or 30d. Anything else reads as the
	// board's own default.
	Range string `json:"range"`
}

// rawOut is the envelope of a read this layer forwards VERBATIM from an upstream
// (the waitlist engine, commerce's spend-alert and promo CRUD, a credit-grant receipt).
// `data` is the upstream's own payload, declared opaque for the same reason as
// iamRowsOut: re-describing someone else's schema here would be a second copy of it.
//
// total is a POINTER because these reads differ on it — a passthrough list carries the
// upstream's total, a passthrough object carries none — and an added key is a wire change.
type rawOut struct {
	// Status is "ok" or "error", at HTTP 200 either way. It is THIS layer's verdict
	// on the forwarding; an upstream that answered carries its own status inside
	// data.
	Status string `json:"status"`
	// Msg is the failure reason when Status is "error", empty otherwise.
	Msg string `json:"msg"`
	// Data is the upstream's payload verbatim, left undescribed for the same reason
	// as iamRowsOut: re-describing someone else's schema here would be a second copy
	// of it, free to drift.
	Data any `json:"data"`
	// Total is the upstream's row count when it forwarded a list, and absent when it
	// forwarded a single object — which is why it is optional rather than zero.
	Total *int `json:"total,omitempty"`
}

// productRow is one product/workload row (ProductRow / GET /v1/admin/products) — the
// projection of a paas fleet AppView (the operator App CR + its Deployment + the drift
// verdict) onto the operator Infrastructure board. Tier is the DERIVED infra grouping
// (tierOf, a cloud-side classification over the real image repo / role / namespace — NOT
// an operator-declared field); Kind is the operator's OWN spec.role when it declares one.
type productRow struct {
	// Name is the operator App CR's name — the handle the operator, the PaaS board
	// and this one all address the workload by. Unique within its namespace.
	Name          string `json:"name"`
	Kind          string `json:"kind"`          // operator App CR spec.role (sql|kv|generic|ingress) or ""
	Tier          string `json:"tier"`          // derived: cloud|data|edge|daemon|paas|app (grouping)
	Org           string `json:"org"`           // image namespace (hanzoai|luxfi|docker.io/…)
	Cluster       string `json:"cluster"`       // hanzo-k8s
	Env           string `json:"env"`           // main|test|dev (lifecycle namespace)
	Namespace     string `json:"namespace"`     // k8s namespace
	Repo          string `json:"repo"`          // owner/repo image coordinate
	Phase         string `json:"phase"`         // operator status.phase (Running/Creating/…)
	DeclaredTag   string `json:"declaredTag"`   // spec.image.tag on the App CR (declared truth)
	RunningTag    string `json:"runningTag"`    // observed from the live Deployment
	LatestTag     string `json:"latestTag"`     // newest released tag (GH release reader — empty until wired)
	Health        string `json:"health"`        // green|yellow|red|unknown
	Drift         bool   `json:"drift"`         // any drift flag present
	DriftSeverity string `json:"driftSeverity"` // ok|yellow|red (rolled-up)
	// Updated is always empty. The App CR carries no per-row reconcile timestamp and
	// this observation is taken live, so there is no moment to report; the time that
	// applies to the whole answer is the overview's lastSync.
	Updated string `json:"updated"`
}

// IAM wire shapes (iamOrg/iamUser) now live in clients/admin/iam as iam.Org /
// iam.User — the upstream-client layer owns the get-* payload decode.
