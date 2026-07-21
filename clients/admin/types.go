package admin

import "github.com/hanzoai/cloud/clients/admin/core"

// Response shapes for /v1/admin/*. Each mirrors the operator's api.ts contract
// (admin/apps/operator/src/lib/api.ts) field-for-field — the JSON tags ARE the
// contract, so the operator's TypeScript types decode these one-to-one.
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

type usageByProduct struct {
	Product    string `json:"product"`
	SpendCents int64  `json:"spendCents"`
	Tokens     int64  `json:"tokens"`
}

type usageData struct {
	Totals    usageTotals      `json:"totals"`
	Series    []usagePoint     `json:"series"`
	ByProduct []usageByProduct `json:"byProduct"`
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
