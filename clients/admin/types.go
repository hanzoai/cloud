package admin

// Response shapes for /v1/admin/*. Each mirrors the operator's api.ts contract
// (admin/apps/operator/src/lib/api.ts) field-for-field — the JSON tags ARE the
// contract, so the operator's TypeScript types decode these one-to-one.

// adminMe is the operator identity (AdminMe / GET /v1/admin/me).
type adminMe struct {
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	Email         string `json:"email"`
	DisplayName   string `json:"displayName"`
	IsGlobalAdmin bool   `json:"isGlobalAdmin"`
}

// sourceStatus is the freshness of one upstream the aggregator pulls from
// (SourceStatus / overview.sources[]).
type sourceStatus struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Rows  int    `json:"rows"`
	Error string `json:"error"`
	At    string `json:"at"`
}

// overviewData is the fleet overview tiles (OverviewData / GET /v1/admin/overview).
type overviewData struct {
	Orgs           int            `json:"orgs"`
	Users          int            `json:"users"`
	Products       int            `json:"products"`
	ActiveProducts int            `json:"activeProducts"`
	Drift          int            `json:"drift"`
	SpendCents30d  int64          `json:"spendCents30d"`
	Tokens30d      int64          `json:"tokens30d"`
	CreditsCents   int64          `json:"creditsCents"`
	LastSync       string         `json:"lastSync"`
	Sources        []sourceStatus `json:"sources"`
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
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	Email         string `json:"email"`
	DisplayName   string `json:"displayName"`
	IsAdmin       bool   `json:"isAdmin"`
	IsGlobalAdmin bool   `json:"isGlobalAdmin"`
	Tag           string `json:"tag"`
	Created       string `json:"created"`
	LastSignin    string `json:"lastSignin"`
	Forbidden     bool   `json:"forbidden"`
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

// productRow is one product/workload row (ProductRow / GET /v1/admin/products).
type productRow struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Org         string `json:"org"`
	Cluster     string `json:"cluster"`
	DeclaredTag string `json:"declaredTag"`
	RunningTag  string `json:"runningTag"`
	Health      string `json:"health"`
	Drift       bool   `json:"drift"`
	Updated     string `json:"updated"`
}

// ── IAM wire shapes (the subset admin decodes from get-* payloads) ─────────

// iamOrg is the IAM Organization subset the aggregators fold over.
type iamOrg struct {
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	CreatedTime string `json:"createdTime"`
}

// iamUser is the IAM User subset mapped into OperatorUser.
type iamUser struct {
	Owner          string `json:"owner"`
	Name           string `json:"name"`
	Email          string `json:"email"`
	DisplayName    string `json:"displayName"`
	Tag            string `json:"tag"`
	CreatedTime    string `json:"createdTime"`
	LastSigninTime string `json:"lastSigninTime"`
	IsAdmin        bool   `json:"isAdmin"`
	IsForbidden    bool   `json:"isForbidden"`
}
