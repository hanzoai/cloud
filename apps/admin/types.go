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
//
// isSuperAdmin is the SuperAdmin key: true iff owner == AdminOrg (IAM's
// IsSuperAdmin predicate).
type adminMe struct {
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	Email        string `json:"email"`
	DisplayName  string `json:"displayName"`
	IsSuperAdmin bool   `json:"isSuperAdmin"`
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
	Status string   `json:"status"`
	Msg    string   `json:"msg"`
	Data   *adminMe `json:"data"`
}

// overviewData is the fleet overview tiles (OverviewData / GET /v1/admin/overview).
type overviewData struct {
	Orgs           int                 `json:"orgs"`
	Users          int                 `json:"users"`
	Products       int                 `json:"products"`
	ActiveProducts int                 `json:"activeProducts"`
	Drift          int                 `json:"drift"`
	SpendCents30d  int64               `json:"spendCents30d"`
	Tokens30d      int64               `json:"tokens30d"`
	CreditsCents   int64               `json:"creditsCents"`
	LastSync       string              `json:"lastSync"`
	Sources        []core.SourceStatus `json:"sources"`
}

// overviewOut is the GET /v1/admin/overview envelope.
type overviewOut struct {
	Status string        `json:"status"`
	Msg    string        `json:"msg"`
	Data   *overviewData `json:"data"`
}

// orgRow is one tenant row (OrgRow / GET /v1/admin/orgs).
type orgRow struct {
	Org          string `json:"org"`
	Display      string `json:"display"`
	Users        int    `json:"users"`
	Products     int    `json:"products"`
	SpendCents   int64  `json:"spendCents"`
	CreditsCents int64  `json:"creditsCents"`
	Tokens       int64  `json:"tokens"`
	Created      string `json:"created"`
}

// orgsOut is the GET /v1/admin/orgs envelope. total == len(data): the directory is the
// caller's whole tenant window, unpaginated.
type orgsOut struct {
	Status string   `json:"status"`
	Msg    string   `json:"msg"`
	Data   []orgRow `json:"data"`
	Total  *int     `json:"total,omitempty"`
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
	Status string         `json:"status"`
	Msg    string         `json:"msg"`
	Data   []operatorUser `json:"data"`
	Total  *int           `json:"total,omitempty"`
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
	Status string `json:"status"`
	Msg    string `json:"msg"`
	Data   any    `json:"data"`
	Total  *int   `json:"total,omitempty"`
}

// operatorUser is one user in the cross-org directory (OperatorUser / GET
// /v1/admin/users).
type operatorUser struct {
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	Email        string `json:"email"`
	DisplayName  string `json:"displayName"`
	IsAdmin      bool   `json:"isAdmin"`
	IsSuperAdmin bool   `json:"isSuperAdmin"`
	Tag          string `json:"tag"`
	Created      string `json:"created"`
	LastSignin   string `json:"lastSignin"`
	Forbidden    bool   `json:"forbidden"`
}

// usage roll-up (UsageData / GET /v1/admin/usage).
type usageTotals struct {
	SpendCents int64 `json:"spendCents"`
	Tokens     int64 `json:"tokens"`
	Requests   int64 `json:"requests"`
}

type usagePoint struct {
	Date       string `json:"date"`
	SpendCents int64  `json:"spendCents"`
	Tokens     int64  `json:"tokens"`
	Requests   int64  `json:"requests"`
}

// usageByModel is one model's slice of the window. The ledger's revenue-bearing
// dimension IS the model — there is no product column, and naming one implied a split
// this plane cannot make.
type usageByModel struct {
	Model      string `json:"model"`
	SpendCents int64  `json:"spendCents"`
	Tokens     int64  `json:"tokens"`
}

type usageData struct {
	Totals  usageTotals    `json:"totals"`
	Series  []usagePoint   `json:"series"`
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
	Status string     `json:"status"`
	Msg    string     `json:"msg"`
	Data   *usageData `json:"data"`
}

// syncStarted acknowledges the "Sync now" button. There is no job id because there is no
// job: the read that follows is the sync.
type syncStarted struct {
	Started bool `json:"started"`
}

// syncOut is the POST /v1/admin/sync envelope.
type syncOut struct {
	Status string       `json:"status"`
	Msg    string       `json:"msg"`
	Data   *syncStarted `json:"data"`
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
	Status string       `json:"status"`
	Msg    string       `json:"msg"`
	Data   []productRow `json:"data"`
	Total  *int         `json:"total,omitempty"`
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
	Status string `json:"status"`
	Msg    string `json:"msg"`
	Data   any    `json:"data"`
	Total  *int   `json:"total,omitempty"`
}

// productRow is one product/workload row (ProductRow / GET /v1/admin/products) — the
// projection of a paas fleet AppView (the operator App CR + its Deployment + the drift
// verdict) onto the operator Infrastructure board. Tier is the DERIVED infra grouping
// (tierOf, a cloud-side classification over the real image repo / role / namespace — NOT
// an operator-declared field); Kind is the operator's OWN spec.role when it declares one.
type productRow struct {
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
	Updated       string `json:"updated"`
}

// IAM wire shapes (iamOrg/iamUser) now live in clients/admin/iam as iam.Org /
// iam.User — the upstream-client layer owns the get-* payload decode.
