// Copyright © 2026 Hanzo AI. MIT License.

package analytics

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// Accuracy classifies the mathematical precision of a registered metric.
const (
	AccuracyExact       = "exact"
	AccuracyApproximate = "approximate"
	AccuracyEstimated   = "estimated"
)

// TableKind distinguishes time-windowed rollup tables from current-state tables.
type TableKind string

const (
	// TableRollup is a time-bucketed aggregate table (minute, hourly, and/or daily tiers).
	TableRollup TableKind = "rollup"
	// TableState is a current-state or dimension table (point-in-time snapshot).
	TableState TableKind = "state"
)

// MetricDef defines one canonical registered metric in the unified analytics catalog.
// Every SQL expression, table name, and dimension allowlist in this struct is validated
// at registration time so user input can never alter SQL structure.
type MetricDef struct {
	// ID is the canonical dotted metric identifier (e.g. "commerce.revenue", "ai.tokens").
	ID string `json:"id"`
	// Domain groups metrics by business/operational plane across the 14 Hanzo domains.
	Domain string `json:"domain"`
	// Description explains what the metric measures and its exact semantic meaning.
	Description string `json:"description"`
	// Unit is the measurement unit (usd_cents, count, tokens, bytes, ratio, bps, ms, seats).
	Unit string `json:"unit"`
	// Dimensions lists the group_by dimension keys permitted for this metric.
	Dimensions []string `json:"dimensions"`
	// Source names the upstream data pipeline that populates this metric.
	Source string `json:"source"`
	// FreshnessTargetSec is the target end-to-end ingestion lag SLA in seconds.
	FreshnessTargetSec int `json:"freshnessTargetSec"`
	// Accuracy indicates whether the metric is exact, approximate (sketch/HLL/t-digest), or estimated.
	Accuracy string `json:"accuracy"`
	// Tables lists the pre-aggregated analytics.* / hanzo.* tables backing this metric.
	Tables []string `json:"tables"`

	// Planning metadata (excluded from JSON/OpenAPI wire schema, usable by analytics.Register).
	TableKind     TableKind `json:"-"`
	TableGroup    string    `json:"-"`
	MinuteTable   string    `json:"-"`
	HourlyTable   string    `json:"-"`
	DailyTable    string    `json:"-"`
	StateTable    string    `json:"-"`
	MinuteTimeCol string    `json:"-"`
	HourlyTimeCol string    `json:"-"`
	DailyTimeCol  string    `json:"-"`
	OrgCol        string    `json:"-"`
	Expr          string    `json:"-"`
	Additive      bool      `json:"-"`

	alias string
}

// dimensionDef maps a public group_by key to its safe constant SQL projection.
type dimensionDef struct {
	key         string
	sqlExpr     string
	needsMinute bool
	needsHour   bool
	isTimeDim   bool
}

// allowedDimensions is the CLOSED allowlist of SQL projections for group_by keys.
// A user-supplied dimension string is looked up here and never concatenated into SQL.
var allowedDimensions = map[string]dimensionDef{
	"org":         {key: "org", sqlExpr: "org"},
	"model":       {key: "model", sqlExpr: "model"},
	"provider":    {key: "provider", sqlExpr: "provider"},
	"service":     {key: "service", sqlExpr: "service"},
	"product":     {key: "product", sqlExpr: "product"},
	"plan":        {key: "plan", sqlExpr: "plan_id"},
	"tier":        {key: "tier", sqlExpr: "tier"},
	"interval":    {key: "interval", sqlExpr: "interval"},
	"status":      {key: "status", sqlExpr: "status"},
	"currency":    {key: "currency", sqlExpr: "currency"},
	"source":      {key: "source", sqlExpr: "source"},
	"kind":        {key: "kind", sqlExpr: "kind"},
	"event_name":  {key: "event_name", sqlExpr: "name"},
	"is_admin":    {key: "is_admin", sqlExpr: "is_admin"},
	"is_internal": {key: "is_internal", sqlExpr: "is_internal"},
	"day":         {key: "day", sqlExpr: "day", isTimeDim: true},
	"hour":        {key: "hour", sqlExpr: "hour", needsHour: true, isTimeDim: true},
	"minute":      {key: "minute", sqlExpr: "minute", needsMinute: true, isTimeDim: true},
}

// allowedPhysicalTables is the CLOSED allowlist of shared pre-aggregated physical tables
// permitted for metric registration. Enforces zero physical table proliferation and blocks
// direct scans of raw event.* tables.
var allowedPhysicalTables = map[string]bool{
	"analytics.rollup_ai_minute":      true,
	"analytics.rollup_ai_hourly":      true,
	"analytics.rollup_ai_daily":       true,
	"analytics.rollup_o11y_minute":    true,
	"analytics.rollup_o11y_hourly":    true,
	"analytics.rollup_product_hourly": true,
	"analytics.rollup_commerce_daily": true,
	"analytics.state_org_balances":    true,
	"analytics.state_subscriptions":   true,
	"analytics.state_invoices":        true,
	"analytics.dim_orgs":              true,
	"analytics.dim_users":             true,
	"analytics.dim_models":            true,
	"analytics.dim_products":          true,
	"hanzo.compute_samples_hourly":    true,
}

// canonicalDomains lists all 14 Hanzo platform domains covered by the semantic catalog.
var canonicalDomains = []string{
	"commerce",
	"iam",
	"ai",
	"enso",
	"kai",
	"compute",
	"storage",
	"network",
	"o11y",
	"product",
	"bots",
	"science",
	"dev",
	"cyber",
}

// CatalogDomains returns a copy of all 14 canonical Hanzo platform domains.
func CatalogDomains() []string {
	return slices.Clone(canonicalDomains)
}

var (
	reMetricID   = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$`)
	reSafeDomain = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	reUnsafeSQL  = regexp.MustCompile(`(?i)(;|--|/\*|\*/|\b(drop|delete|insert|update|alter|create|truncate|attach|detach|grant|revoke|union|into|outfile|system|exec|execute|sleep)\b)`)
	reValidExpr  = regexp.MustCompile(`^[A-Za-z0-9_(),.'\s+\-*/=<>!%]+$`)
	reSQLStrings = regexp.MustCompile(`'[A-Za-z0-9_.:/\- ]*'`)
)

func stripSingleQuotedLiterals(expr string) (string, bool) {
	// Ensure balanced simple string literals with no stray quotes after stripping.
	stripped := reSQLStrings.ReplaceAllString(expr, "''")
	if strings.Contains(strings.ReplaceAll(stripped, "''", ""), "'") {
		return "", false
	}
	return stripped, true
}

func validateMetricDef(def *MetricDef) error {
	def.ID = strings.TrimSpace(def.ID)
	if !reMetricID.MatchString(def.ID) {
		return fmt.Errorf("analytics: invalid metric id %q (must be dotted lowercase identifier)", def.ID)
	}
	def.Domain = strings.ToLower(strings.TrimSpace(def.Domain))
	if !reSafeDomain.MatchString(def.Domain) {
		return fmt.Errorf("analytics: metric %q has invalid domain %q", def.ID, def.Domain)
	}
	def.Description = strings.TrimSpace(def.Description)
	if def.Description == "" {
		return fmt.Errorf("analytics: metric %q missing description", def.ID)
	}
	def.Unit = strings.TrimSpace(def.Unit)
	if def.Unit == "" {
		return fmt.Errorf("analytics: metric %q missing unit", def.ID)
	}
	def.Source = strings.TrimSpace(def.Source)
	if def.Source == "" {
		return fmt.Errorf("analytics: metric %q missing source", def.ID)
	}
	if def.FreshnessTargetSec <= 0 {
		return fmt.Errorf("analytics: metric %q freshnessTargetSec must be > 0", def.ID)
	}
	switch def.Accuracy {
	case AccuracyExact, AccuracyApproximate, AccuracyEstimated:
	default:
		return fmt.Errorf("analytics: metric %q invalid accuracy %q", def.ID, def.Accuracy)
	}

	def.Expr = strings.TrimSpace(def.Expr)
	strippedExpr, okQuotes := stripSingleQuotedLiterals(def.Expr)
	if def.Expr == "" || !okQuotes || !reValidExpr.MatchString(def.Expr) || reUnsafeSQL.MatchString(strippedExpr) {
		return fmt.Errorf("analytics: metric %q has unsafe or empty SQL expression", def.ID)
	}
	depth := 0
	for _, r := range def.Expr {
		if r == '(' {
			depth++
		} else if r == ')' {
			depth--
			if depth < 0 {
				return fmt.Errorf("analytics: metric %q has unbalanced parentheses in SQL expression", def.ID)
			}
		}
	}
	if depth != 0 {
		return fmt.Errorf("analytics: metric %q has unbalanced parentheses in SQL expression", def.ID)
	}

	// Infer TableKind when omitted.
	if def.TableKind == "" {
		if def.StateTable != "" && def.HourlyTable == "" && def.DailyTable == "" && def.MinuteTable == "" {
			def.TableKind = TableState
		} else {
			def.TableKind = TableRollup
		}
	}
	if def.TableKind != TableRollup && def.TableKind != TableState {
		return fmt.Errorf("analytics: metric %q has invalid TableKind %q", def.ID, def.TableKind)
	}

	// Validate backing physical tables against the closed allowlist.
	var tables []string
	addTable := func(tbl string) error {
		tbl = strings.TrimSpace(tbl)
		if tbl == "" {
			return nil
		}
		if !allowedPhysicalTables[tbl] {
			return fmt.Errorf("analytics: metric %q references disallowed table %q", def.ID, tbl)
		}
		if !slices.Contains(tables, tbl) {
			tables = append(tables, tbl)
		}
		return nil
	}
	if err := addTable(def.MinuteTable); err != nil {
		return err
	}
	if err := addTable(def.HourlyTable); err != nil {
		return err
	}
	if err := addTable(def.DailyTable); err != nil {
		return err
	}
	if err := addTable(def.StateTable); err != nil {
		return err
	}
	if len(tables) == 0 {
		return fmt.Errorf("analytics: metric %q must declare at least one backing table", def.ID)
	}
	if def.TableKind == TableRollup && def.HourlyTable == "" && def.DailyTable == "" && def.MinuteTable == "" {
		return fmt.Errorf("analytics: rollup metric %q must declare minute, hourly, or daily table", def.ID)
	}
	if def.TableKind == TableState && def.StateTable == "" {
		return fmt.Errorf("analytics: state metric %q must declare state table", def.ID)
	}
	if def.TableKind == TableRollup {
		if def.HourlyTable == "" && def.DailyTable != "" {
			def.HourlyTable = def.DailyTable
		}
		if def.DailyTable == "" && def.HourlyTable != "" {
			def.DailyTable = def.HourlyTable
		}
		if def.MinuteTable != "" && def.MinuteTimeCol == "" {
			def.MinuteTimeCol = "minute"
		}
		if def.HourlyTable != "" && def.HourlyTimeCol == "" {
			if strings.HasSuffix(def.HourlyTable, "_daily") {
				def.HourlyTimeCol = "day"
			} else {
				def.HourlyTimeCol = "hour"
			}
		}
		if def.DailyTable != "" && def.DailyTimeCol == "" {
			if strings.HasSuffix(def.DailyTable, "_daily") {
				def.DailyTimeCol = "day"
			} else {
				def.DailyTimeCol = "hour"
			}
		}
	}
	if def.OrgCol == "" {
		def.OrgCol = "org"
	}
	if def.TableGroup == "" {
		def.TableGroup = tables[0]
	}

	// Validate every dimension against the closed allowlist.
	cleanDims := make([]string, 0, len(def.Dimensions))
	seenDims := make(map[string]bool, len(def.Dimensions))
	for _, rawDim := range def.Dimensions {
		d := strings.TrimSpace(rawDim)
		if _, ok := allowedDimensions[d]; !ok {
			return fmt.Errorf("analytics: metric %q references disallowed dimension %q", def.ID, d)
		}
		if !seenDims[d] {
			seenDims[d] = true
			cleanDims = append(cleanDims, d)
		}
	}
	def.Dimensions = cleanDims
	def.Tables = tables
	def.alias = strings.ReplaceAll(def.ID, ".", "_")
	return nil
}

// ── Shared physical table helpers (zero new physical tables) ─────────────────

func commerceDailyMetric(id, domain, desc, unit, expr string, additive bool) MetricDef {
	return MetricDef{
		ID:                 id,
		Domain:             domain,
		Description:        desc,
		Unit:               unit,
		Dimensions:         []string{"org", "day", "currency", "product"},
		Source:             "commerce",
		FreshnessTargetSec: 60,
		Accuracy:           AccuracyExact,
		TableKind:          TableRollup,
		TableGroup:         "rollup_commerce",
		HourlyTable:        "analytics.rollup_commerce_daily",
		DailyTable:         "analytics.rollup_commerce_daily",
		HourlyTimeCol:      "day",
		DailyTimeCol:       "day",
		OrgCol:             "org",
		Expr:               expr,
		Additive:           additive,
	}
}

func aiRollupMetric(id, domain, desc, unit, accuracy, expr string, additive bool) MetricDef {
	return MetricDef{
		ID:                 id,
		Domain:             domain,
		Description:        desc,
		Unit:               unit,
		Dimensions:         []string{"org", "model", "provider", "day", "hour", "minute"},
		Source:             "ai_ledger",
		FreshnessTargetSec: 2,
		Accuracy:           accuracy,
		TableKind:          TableRollup,
		TableGroup:         "rollup_ai",
		MinuteTable:        "analytics.rollup_ai_minute",
		HourlyTable:        "analytics.rollup_ai_hourly",
		DailyTable:         "analytics.rollup_ai_daily",
		MinuteTimeCol:      "minute",
		HourlyTimeCol:      "hour",
		DailyTimeCol:       "day",
		OrgCol:             "org",
		Expr:               expr,
		Additive:           additive,
	}
}

func o11yRollupMetric(id, domain, desc, unit, accuracy, expr string, additive bool) MetricDef {
	return MetricDef{
		ID:                 id,
		Domain:             domain,
		Description:        desc,
		Unit:               unit,
		Dimensions:         []string{"org", "service", "day", "hour", "minute"},
		Source:             "o11y",
		FreshnessTargetSec: 2,
		Accuracy:           accuracy,
		TableKind:          TableRollup,
		TableGroup:         "rollup_o11y",
		MinuteTable:        "analytics.rollup_o11y_minute",
		HourlyTable:        "analytics.rollup_o11y_hourly",
		DailyTable:         "analytics.rollup_o11y_hourly",
		MinuteTimeCol:      "minute",
		HourlyTimeCol:      "hour",
		DailyTimeCol:       "hour",
		OrgCol:             "org",
		Expr:               expr,
		Additive:           additive,
	}
}

func productRollupMetric(id, domain, desc, unit, accuracy, expr string, additive bool) MetricDef {
	return MetricDef{
		ID:                 id,
		Domain:             domain,
		Description:        desc,
		Unit:               unit,
		Dimensions:         []string{"org", "product", "event_name", "day", "hour"},
		Source:             "product_events",
		FreshnessTargetSec: 30,
		Accuracy:           accuracy,
		TableKind:          TableRollup,
		TableGroup:         "rollup_product",
		HourlyTable:        "analytics.rollup_product_hourly",
		DailyTable:         "analytics.rollup_product_hourly",
		HourlyTimeCol:      "hour",
		DailyTimeCol:       "hour",
		OrgCol:             "org",
		Expr:               expr,
		Additive:           additive,
	}
}

func stateMetric(id, domain, desc, unit, source, tableGroup, stateTable string, dims []string, freshnessSec int, expr string, additive bool) MetricDef {
	return MetricDef{
		ID:                 id,
		Domain:             domain,
		Description:        desc,
		Unit:               unit,
		Dimensions:         dims,
		Source:             source,
		FreshnessTargetSec: freshnessSec,
		Accuracy:           AccuracyExact,
		TableKind:          TableState,
		TableGroup:         tableGroup,
		StateTable:         stateTable,
		OrgCol:             "org",
		Expr:               expr,
		Additive:           additive,
	}
}

// builtinCatalog builds the 115 governed metric definitions across all 14 Hanzo domains,
// mapped onto the existing shared analytics.* and hanzo.* tables.
func builtinCatalog() []MetricDef {
	return []MetricDef{
		// ── 1. Commerce (18 metrics) ─────────────────────────────────────────
		commerceDailyMetric("commerce.revenue", "commerce",
			"Recognized cash revenue collected from paid invoices in minor currency units (USD cents).",
			"usd_cents", "sumIf(amount_minor, kind = 'invoice_paid')", true),
		commerceDailyMetric("commerce.spend", "commerce",
			"Total metered customer spend across cash and credit ledgers in minor currency units (USD cents).",
			"usd_cents", "sumIf(amount_minor, kind = 'usage')", true),
		commerceDailyMetric("commerce.invoices_paid", "commerce",
			"Count of settled customer invoices within the queried window.",
			"count", "sumIf(entries, kind = 'invoice_paid')", true),
		commerceDailyMetric("commerce.deposits", "commerce",
			"Total prepaid cash deposits credited to customer wallets in USD cents.",
			"usd_cents", "sumIf(amount_minor, kind = 'deposit')", true),
		commerceDailyMetric("commerce.refunds", "commerce",
			"Net signed refund adjustments posted against customer ledgers in USD cents.",
			"usd_cents", "sumIf(amount_minor, kind = 'refund')", true),
		commerceDailyMetric("commerce.grants", "commerce",
			"Total promotional and enterprise credit grants issued in USD cents.",
			"usd_cents", "sumIf(amount_minor, kind = 'grant')", true),
		commerceDailyMetric("commerce.chargebacks", "commerce",
			"Net signed processor clawbacks and dispute debits in USD cents.",
			"usd_cents", "sumIf(amount_minor, kind = 'chargeback')", true),
		commerceDailyMetric("commerce.net_cash_flow", "commerce",
			"Net signed cash movement across deposits, invoice settlements, refunds, and clawbacks in USD cents.",
			"usd_cents", "sum(cash_minor)", true),
		commerceDailyMetric("commerce.ledger_entries", "commerce",
			"Total net commerce ledger flow entries recorded in the queried window.",
			"count", "sum(entries)", true),
		stateMetric("commerce.mrr", "commerce",
			"Current run-rate Monthly Recurring Revenue from active non-test subscriptions in USD cents.",
			"usd_cents", "commerce", "state_subscriptions", "analytics.state_subscriptions",
			[]string{"org", "plan", "interval", "provider", "currency"}, 60,
			"sumIf(mrr_minor, status = 'active' AND deleted = 0 AND test = 0)", true),
		stateMetric("commerce.arr", "commerce",
			"Current run-rate Annual Recurring Revenue (MRR * 12) from active non-test subscriptions in USD cents.",
			"usd_cents", "commerce", "state_subscriptions", "analytics.state_subscriptions",
			[]string{"org", "plan", "interval", "provider", "currency"}, 60,
			"sumIf(mrr_minor, status = 'active' AND deleted = 0 AND test = 0) * 12", true),
		stateMetric("commerce.active_subscriptions", "commerce",
			"Current count of non-ended customer subscriptions (active, trialing, and past_due).",
			"count", "commerce", "state_subscriptions", "analytics.state_subscriptions",
			[]string{"org", "plan", "status", "provider", "currency"}, 60,
			"countIf(status IN ('active', 'trialing', 'past_due') AND deleted = 0 AND test = 0)", true),
		stateMetric("commerce.seats", "commerce",
			"Current total active and trialing seats across non-test subscriptions.",
			"seats", "commerce", "state_subscriptions", "analytics.state_subscriptions",
			[]string{"org", "plan", "status", "provider"}, 60,
			"sumIf(seats, status IN ('active', 'trialing') AND deleted = 0 AND test = 0)", true),
		stateMetric("commerce.cash_held", "commerce",
			"Current unspent prepaid customer cash balance held across organization wallets in USD cents.",
			"usd_cents", "commerce", "state_org_balances", "analytics.state_org_balances",
			[]string{"org", "currency"}, 30, "sum(cash_minor)", true),
		stateMetric("commerce.credit_outstanding", "commerce",
			"Current unspent granted or promotional credit balance across organization wallets in USD cents.",
			"usd_cents", "commerce", "state_org_balances", "analytics.state_org_balances",
			[]string{"org", "currency"}, 30, "sum(credit_minor)", true),
		stateMetric("commerce.owed_balance", "commerce",
			"Current outstanding post-paid receivable balance owed by organizations in USD cents.",
			"usd_cents", "commerce", "state_org_balances", "analytics.state_org_balances",
			[]string{"org", "currency"}, 30, "sum(owed_minor)", true),
		stateMetric("commerce.paying_orgs", "commerce",
			"Count of distinct organizations holding active commerce books.",
			"count", "commerce", "state_org_balances", "analytics.state_org_balances",
			[]string{"currency"}, 30, "countIf(books = 1)", true),
		stateMetric("commerce.open_invoices_amount", "commerce",
			"Total unpaid amount across open and past-due customer invoices in USD cents.",
			"usd_cents", "commerce", "state_invoices", "analytics.state_invoices",
			[]string{"org", "currency", "status"}, 60,
			"sumIf(amount_minor - paid_minor, deleted = 0 AND status IN ('open', 'past_due'))", true),

		// ── 2. IAM (9 metrics) ───────────────────────────────────────────────
		stateMetric("iam.orgs", "iam",
			"Count of registered organizations in the IAM dimension table.",
			"count", "iam", "dim_orgs", "analytics.dim_orgs",
			[]string{"tier", "status", "is_internal"}, 60, "uniqExact(org)", false),
		stateMetric("iam.customer_orgs", "iam",
			"Count of non-internal customer organizations registered in IAM.",
			"count", "iam", "dim_orgs", "analytics.dim_orgs",
			[]string{"tier", "status"}, 60, "countIf(is_internal = 0)", true),
		stateMetric("iam.internal_orgs", "iam",
			"Count of reserved platform and internal organizations registered in IAM.",
			"count", "iam", "dim_orgs", "analytics.dim_orgs",
			[]string{"tier", "status"}, 60, "countIf(is_internal = 1)", true),
		stateMetric("iam.total_members", "iam",
			"Total organization memberships summed across the IAM org directory.",
			"count", "iam", "dim_orgs", "analytics.dim_orgs",
			[]string{"tier", "status", "is_internal"}, 60, "sum(members)", true),
		stateMetric("iam.users", "iam",
			"Count of real human user accounts across organizations.",
			"count", "iam", "dim_users", "analytics.dim_users",
			[]string{"org", "is_admin"}, 60, "uniqExact(user_id)", false),
		stateMetric("iam.admin_users", "iam",
			"Count of privileged organization administrator accounts in IAM.",
			"count", "iam", "dim_users", "analytics.dim_users",
			[]string{"org"}, 60, "countIf(is_admin = 1)", true),
		stateMetric("iam.forbidden_users", "iam",
			"Count of suspended or forbidden user accounts across organizations.",
			"count", "iam", "dim_users", "analytics.dim_users",
			[]string{"org"}, 60, "countIf(forbidden = 1)", true),
		stateMetric("iam.active_users_7d", "iam",
			"Users with a recorded IAM sign-in within the trailing 7 days.",
			"count", "iam", "dim_users", "analytics.dim_users",
			[]string{"org"}, 60, "countIf(last_signin_at >= now() - INTERVAL 7 DAY)", true),
		aiRollupMetric("iam.active_users", "iam",
			"Distinct authenticated users active within the queried time window.",
			"count", AccuracyApproximate, "uniqMerge(users)", false),

		// ── 3. AI (14 metrics — 1m/1h/1d tiers) ──────────────────────────────
		aiRollupMetric("ai.requests", "ai",
			"Total AI inference requests served through the Hanzo AI gateway.",
			"count", AccuracyExact, "countMerge(requests)", true),
		aiRollupMetric("ai.tokens", "ai",
			"Total prompt and completion tokens processed across AI models.",
			"tokens", AccuracyExact, "sumMerge(tokens)", true),
		aiRollupMetric("ai.prompt_tokens", "ai",
			"Total input prompt tokens processed across AI models.",
			"tokens", AccuracyExact, "sumMerge(prompt_tokens)", true),
		aiRollupMetric("ai.completion_tokens", "ai",
			"Total generated completion tokens produced across AI models.",
			"tokens", AccuracyExact, "sumMerge(completion_tokens)", true),
		aiRollupMetric("ai.cache_read_tokens", "ai",
			"Total prompt tokens served from provider prompt caches.",
			"tokens", AccuracyExact, "sumMerge(cache_read_tokens)", true),
		aiRollupMetric("ai.cost", "ai",
			"Upstream provider inference cost incurred by Hanzo in USD cents.",
			"usd_cents", AccuracyExact, "intDiv(sumMerge(cost_nano) + 5000000, 10000000)", true),
		aiRollupMetric("ai.billed", "ai",
			"Customer-billed AI inference charges in USD cents.",
			"usd_cents", AccuracyExact, "intDiv(sumMerge(billed_nano) + 5000000, 10000000)", true),
		aiRollupMetric("ai.margin", "ai",
			"Gross AI inference margin (billed minus upstream provider cost) in USD cents.",
			"usd_cents", AccuracyExact, "intDiv(sumMerge(margin_nano) + 5000000, 10000000)", true),
		aiRollupMetric("ai.cache_savings", "ai",
			"Estimated cost savings from prompt caching hits in USD cents.",
			"usd_cents", AccuracyEstimated, "intDiv(sumMerge(cache_saved_nano) + 5000000, 10000000)", true),
		aiRollupMetric("ai.errors", "ai",
			"Total failed AI inference requests returning status='error'.",
			"count", AccuracyExact, "sumMerge(errors)", true),
		aiRollupMetric("ai.error_rate", "ai",
			"Ratio of failed AI requests to total AI requests (0.0 to 1.0).",
			"ratio", AccuracyExact, "if(countMerge(requests) > 0, toFloat64(sumMerge(errors)) / toFloat64(countMerge(requests)), 0.0)", false),
		aiRollupMetric("ai.active_users", "ai",
			"Distinct users issuing AI inference requests within the window.",
			"count", AccuracyApproximate, "uniqMerge(users)", false),
		aiRollupMetric("ai.cache_hit_ratio", "ai",
			"Fraction of prompt tokens served via prompt cache reads (0.0 to 1.0).",
			"ratio", AccuracyExact, "if(sumMerge(prompt_tokens) > 0, toFloat64(sumMerge(cache_read_tokens)) / toFloat64(sumMerge(prompt_tokens)), 0.0)", false),
		aiRollupMetric("ai.margin_bps", "ai",
			"Gross AI inference margin expressed in basis points (10000 = 100%).",
			"bps", AccuracyExact, "if(sumMerge(billed_nano) > 0, intDiv(sumMerge(margin_nano) * 10000, sumMerge(billed_nano)), 0)", false),

		// ── 4. Enso (6 metrics) ──────────────────────────────────────────────
		productRollupMetric("enso.events", "enso",
			"Total Enso canvas and generation telemetry events.",
			"count", AccuracyExact, "countMergeIf(events, product = 'enso')", true),
		productRollupMetric("enso.active_users", "enso",
			"Distinct interactive Enso users merged from HyperLogLog states.",
			"count", AccuracyApproximate, "uniqMergeIf(users, product = 'enso')", false),
		productRollupMetric("enso.sessions", "enso",
			"Distinct interactive Enso workspace sessions.",
			"count", AccuracyApproximate, "uniqMergeIf(sessions, product = 'enso')", false),
		productRollupMetric("enso.bytes", "enso",
			"Total Enso payload and artifact bytes processed.",
			"bytes", AccuracyExact, "sumMergeIf(bytes, product = 'enso')", true),
		commerceDailyMetric("enso.spend", "enso",
			"Metered customer spend attributed to Enso in USD cents.",
			"usd_cents", "sumIf(amount_minor, product = 'enso' AND kind = 'usage')", true),
		commerceDailyMetric("enso.revenue", "enso",
			"Recognized invoice revenue attributed to Enso in USD cents.",
			"usd_cents", "sumIf(amount_minor, product = 'enso' AND kind = 'invoice_paid')", true),

		// ── 5. Kai (6 metrics) ───────────────────────────────────────────────
		productRollupMetric("kai.events", "kai",
			"Total Kai autonomous assistant telemetry events.",
			"count", AccuracyExact, "countMergeIf(events, product = 'kai')", true),
		productRollupMetric("kai.tasks_executed", "kai",
			"Total Kai task execution events recorded.",
			"count", AccuracyExact, "countMergeIf(events, product = 'kai' AND name = 'task_executed')", true),
		productRollupMetric("kai.active_users", "kai",
			"Distinct active users interacting with Kai.",
			"count", AccuracyApproximate, "uniqMergeIf(users, product = 'kai')", false),
		productRollupMetric("kai.sessions", "kai",
			"Distinct interactive Kai assistant sessions.",
			"count", AccuracyApproximate, "uniqMergeIf(sessions, product = 'kai')", false),
		productRollupMetric("kai.bytes", "kai",
			"Total Kai task payload bytes ingested.",
			"bytes", AccuracyExact, "sumMergeIf(bytes, product = 'kai')", true),
		commerceDailyMetric("kai.spend", "kai",
			"Metered customer spend attributed to Kai in USD cents.",
			"usd_cents", "sumIf(amount_minor, product = 'kai' AND kind = 'usage')", true),

		// ── 6. Compute (8 metrics) ───────────────────────────────────────────
		{
			ID:                 "compute.samples",
			Domain:             "compute",
			Description:        "Count of hourly compute telemetry rollups observed across fleet hosts and GPUs.",
			Unit:               "count",
			Dimensions:         []string{"org", "source", "kind", "day", "hour"},
			Source:             "compute",
			FreshnessTargetSec: 60,
			Accuracy:           AccuracyExact,
			TableKind:          TableRollup,
			TableGroup:         "rollup_compute",
			HourlyTable:        "hanzo.compute_samples_hourly",
			DailyTable:         "hanzo.compute_samples_hourly",
			HourlyTimeCol:      "hour",
			DailyTimeCol:       "hour",
			OrgCol:             "org",
			Expr:               "count()",
			Additive:           true,
		},
		{
			ID:                 "compute.cost",
			Domain:             "compute",
			Description:        "Maximum observed hourly compute infrastructure cost across units in USD cents.",
			Unit:               "usd_cents",
			Dimensions:         []string{"org", "source", "kind", "day", "hour"},
			Source:             "compute",
			FreshnessTargetSec: 60,
			Accuracy:           AccuracyEstimated,
			TableKind:          TableRollup,
			TableGroup:         "rollup_compute",
			HourlyTable:        "hanzo.compute_samples_hourly",
			DailyTable:         "hanzo.compute_samples_hourly",
			HourlyTimeCol:      "hour",
			DailyTimeCol:       "hour",
			OrgCol:             "org",
			Expr:               "maxMerge(cost_cents_max)",
			Additive:           false,
		},
		o11yRollupMetric("compute.spans", "compute",
			"Total traced compute and container/VM workload execution spans.",
			"count", AccuracyExact, "countMergeIf(spans, service IN ('compute', 'sandbox', 'paas', 'visor'))", true),
		o11yRollupMetric("compute.errors", "compute",
			"Total failed compute and sandbox execution spans.",
			"count", AccuracyExact, "sumMergeIf(errors, service IN ('compute', 'sandbox', 'paas', 'visor'))", true),
		o11yRollupMetric("compute.p50_ms", "compute",
			"Median (p50) compute workload span duration in milliseconds.",
			"ms", AccuracyApproximate, "quantileTDigestMergeIf(0.50)(p50_ns, service IN ('compute', 'sandbox', 'paas', 'visor')) / 1000000.0", false),
		o11yRollupMetric("compute.p95_ms", "compute",
			"95th percentile compute workload span duration in milliseconds.",
			"ms", AccuracyApproximate, "quantileTDigestMergeIf(0.95)(p95_ns, service IN ('compute', 'sandbox', 'paas', 'visor')) / 1000000.0", false),
		o11yRollupMetric("compute.p99_ms", "compute",
			"99th percentile compute workload span duration in milliseconds.",
			"ms", AccuracyApproximate, "quantileTDigestMergeIf(0.99)(p99_ns, service IN ('compute', 'sandbox', 'paas', 'visor')) / 1000000.0", false),
		commerceDailyMetric("compute.spend", "compute",
			"Metered customer spend attributed to Compute workloads in USD cents.",
			"usd_cents", "sumIf(amount_minor, product = 'compute' AND kind = 'usage')", true),

		// ── 7. Storage (6 metrics) ───────────────────────────────────────────
		productRollupMetric("storage.bytes_ingested", "storage",
			"Total telemetry and storage payload bytes ingested across products.",
			"bytes", AccuracyExact, "sumMerge(bytes)", true),
		productRollupMetric("storage.events", "storage",
			"Total storage and object-store telemetry events.",
			"count", AccuracyExact, "countMergeIf(events, product IN ('storage', 's3', 'datastore', 'kv'))", true),
		productRollupMetric("storage.active_users", "storage",
			"Distinct users interacting with Hanzo Storage services.",
			"count", AccuracyApproximate, "uniqMergeIf(users, product IN ('storage', 's3', 'datastore', 'kv'))", false),
		productRollupMetric("storage.sessions", "storage",
			"Distinct sessions accessing Hanzo Storage surfaces.",
			"count", AccuracyApproximate, "uniqMergeIf(sessions, product IN ('storage', 's3', 'datastore', 'kv'))", false),
		o11yRollupMetric("storage.spans", "storage",
			"Total observed RPC/HTTP spans served by storage backends.",
			"count", AccuracyExact, "countMergeIf(spans, service IN ('storage', 's3', 'datastore', 'kv', 'postgres'))", true),
		commerceDailyMetric("storage.spend", "storage",
			"Metered customer spend attributed to Storage in USD cents.",
			"usd_cents", "sumIf(amount_minor, product = 'storage' AND kind = 'usage')", true),

		// ── 8. Network (7 metrics) ───────────────────────────────────────────
		o11yRollupMetric("network.requests", "network",
			"Total gateway, ingress, proxy, and edge RPC spans observed.",
			"count", AccuracyExact, "countMergeIf(spans, service IN ('gateway', 'edge', 'ingress', 'proxy', 'dns', 'cdn'))", true),
		o11yRollupMetric("network.errors", "network",
			"Total failed gateway, ingress, proxy, and edge RPC spans.",
			"count", AccuracyExact, "sumMergeIf(errors, service IN ('gateway', 'edge', 'ingress', 'proxy', 'dns', 'cdn'))", true),
		o11yRollupMetric("network.error_rate", "network",
			"Ratio of failed network/gateway spans to total network spans (0.0 to 1.0).",
			"ratio", AccuracyExact, "if(countMergeIf(spans, service IN ('gateway', 'edge', 'ingress', 'proxy', 'dns', 'cdn')) > 0, toFloat64(sumMergeIf(errors, service IN ('gateway', 'edge', 'ingress', 'proxy', 'dns', 'cdn'))) / toFloat64(countMergeIf(spans, service IN ('gateway', 'edge', 'ingress', 'proxy', 'dns', 'cdn'))), 0.0)", false),
		o11yRollupMetric("network.p50_ms", "network",
			"Median (p50) network gateway span latency in milliseconds.",
			"ms", AccuracyApproximate, "quantileTDigestMergeIf(0.50)(p50_ns, service IN ('gateway', 'edge', 'ingress', 'proxy', 'dns', 'cdn')) / 1000000.0", false),
		o11yRollupMetric("network.p95_ms", "network",
			"95th percentile network gateway span latency in milliseconds.",
			"ms", AccuracyApproximate, "quantileTDigestMergeIf(0.95)(p95_ns, service IN ('gateway', 'edge', 'ingress', 'proxy', 'dns', 'cdn')) / 1000000.0", false),
		o11yRollupMetric("network.p99_ms", "network",
			"99th percentile network gateway span latency in milliseconds.",
			"ms", AccuracyApproximate, "quantileTDigestMergeIf(0.99)(p99_ns, service IN ('gateway', 'edge', 'ingress', 'proxy', 'dns', 'cdn')) / 1000000.0", false),
		productRollupMetric("network.bytes", "network",
			"Total network and edge telemetry payload bytes observed.",
			"bytes", AccuracyExact, "sumMergeIf(bytes, product IN ('network', 'edge', 'gateway', 'dns', 'cdn'))", true),

		// ── 9. O11y (8 metrics — 1m/1h tiers) ────────────────────────────────
		o11yRollupMetric("o11y.requests", "o11y",
			"Total HTTP and RPC spans observed by Hanzo observability pipelines.",
			"count", AccuracyExact, "countMerge(spans)", true),
		o11yRollupMetric("o11y.errors", "o11y",
			"Total error-status spans recorded across services.",
			"count", AccuracyExact, "sumMerge(errors)", true),
		o11yRollupMetric("o11y.error_rate", "o11y",
			"Ratio of error-status spans to total observed spans (0.0 to 1.0).",
			"ratio", AccuracyExact, "if(countMerge(spans) > 0, toFloat64(sumMerge(errors)) / toFloat64(countMerge(spans)), 0.0)", false),
		o11yRollupMetric("o11y.p50_ms", "o11y",
			"Median (p50) span latency in milliseconds merged from t-digest states.",
			"ms", AccuracyApproximate, "quantileTDigestMerge(0.50)(p50_ns) / 1000000.0", false),
		o11yRollupMetric("o11y.p95_ms", "o11y",
			"95th percentile (p95) span latency in milliseconds merged from t-digest states.",
			"ms", AccuracyApproximate, "quantileTDigestMerge(0.95)(p95_ns) / 1000000.0", false),
		o11yRollupMetric("o11y.p99_ms", "o11y",
			"99th percentile (p99) span latency in milliseconds merged from t-digest states.",
			"ms", AccuracyApproximate, "quantileTDigestMerge(0.99)(p99_ns) / 1000000.0", false),
		o11yRollupMetric("o11y.active_services", "o11y",
			"Count of distinct instrumented services emitting spans within the window.",
			"count", AccuracyExact, "uniqExact(service)", false),
		o11yRollupMetric("o11y.active_orgs", "o11y",
			"Count of distinct organizations emitting observability telemetry within the window.",
			"count", AccuracyExact, "uniqExact(org)", false),

		// ── 10. Product (7 metrics) ──────────────────────────────────────────
		productRollupMetric("product.events", "product",
			"Total product telemetry events ingested via /v1/event.",
			"count", AccuracyExact, "countMerge(events)", true),
		productRollupMetric("product.visitors", "product",
			"Distinct product visitors merged from HyperLogLog states.",
			"count", AccuracyApproximate, "uniqMerge(users)", false),
		productRollupMetric("product.sessions", "product",
			"Distinct product user sessions merged from HyperLogLog states.",
			"count", AccuracyApproximate, "uniqMerge(sessions)", false),
		productRollupMetric("product.bytes", "product",
			"Total product telemetry event payload bytes ingested.",
			"bytes", AccuracyExact, "sumMerge(bytes)", true),
		productRollupMetric("product.active_products", "product",
			"Distinct Hanzo products emitting engagement events in the window.",
			"count", AccuracyExact, "uniqExact(product)", false),
		productRollupMetric("product.active_orgs", "product",
			"Distinct organizations generating product telemetry events in the window.",
			"count", AccuracyExact, "uniqExact(org)", false),
		commerceDailyMetric("product.revenue", "product",
			"Total recognized invoice and usage revenue grouped by product in USD cents.",
			"usd_cents", "sumIf(amount_minor, kind IN ('usage', 'invoice_paid'))", true),

		// ── 11. Bots (6 metrics) ─────────────────────────────────────────────
		productRollupMetric("bots.events", "bots",
			"Total Hanzo Bot messages, webhooks, and automation trigger events.",
			"count", AccuracyExact, "countMergeIf(events, product = 'bots')", true),
		productRollupMetric("bots.active_users", "bots",
			"Distinct users interacting with Hanzo Bots.",
			"count", AccuracyApproximate, "uniqMergeIf(users, product = 'bots')", false),
		productRollupMetric("bots.sessions", "bots",
			"Distinct Hanzo Bot conversation and automation sessions.",
			"count", AccuracyApproximate, "uniqMergeIf(sessions, product = 'bots')", false),
		productRollupMetric("bots.bytes", "bots",
			"Total Hanzo Bot message and webhook payload bytes.",
			"bytes", AccuracyExact, "sumMergeIf(bytes, product = 'bots')", true),
		o11yRollupMetric("bots.spans", "bots",
			"Total traced Hanzo Bot execution spans.",
			"count", AccuracyExact, "countMergeIf(spans, service = 'bots')", true),
		commerceDailyMetric("bots.spend", "bots",
			"Metered customer spend attributed to Bots in USD cents.",
			"usd_cents", "sumIf(amount_minor, product = 'bots' AND kind = 'usage')", true),

		// ── 12. Science (6 metrics) ──────────────────────────────────────────
		productRollupMetric("science.events", "science",
			"Total Hanzo Science evaluation, simulation, and research pipeline events.",
			"count", AccuracyExact, "countMergeIf(events, product IN ('science', 'research'))", true),
		productRollupMetric("science.active_users", "science",
			"Distinct researchers and scientists active on Hanzo Science.",
			"count", AccuracyApproximate, "uniqMergeIf(users, product IN ('science', 'research'))", false),
		productRollupMetric("science.sessions", "science",
			"Distinct interactive experiment and notebook sessions on Hanzo Science.",
			"count", AccuracyApproximate, "uniqMergeIf(sessions, product IN ('science', 'research'))", false),
		productRollupMetric("science.bytes", "science",
			"Total dataset and evaluation telemetry payload bytes on Hanzo Science.",
			"bytes", AccuracyExact, "sumMergeIf(bytes, product IN ('science', 'research'))", true),
		o11yRollupMetric("science.spans", "science",
			"Total traced benchmark and scientific workflow execution spans.",
			"count", AccuracyExact, "countMergeIf(spans, service IN ('science', 'research', 'eval'))", true),
		commerceDailyMetric("science.spend", "science",
			"Metered customer spend attributed to Science workloads in USD cents.",
			"usd_cents", "sumIf(amount_minor, product IN ('science', 'research') AND kind = 'usage')", true),

		// ── 13. Dev (7 metrics) ──────────────────────────────────────────────
		productRollupMetric("dev.events", "dev",
			"Total Hanzo Dev CLI, IDE, SDK, and agent coding telemetry events.",
			"count", AccuracyExact, "countMergeIf(events, product IN ('dev', 'cli', 'sdk', 'code'))", true),
		productRollupMetric("dev.active_users", "dev",
			"Distinct developers active across Hanzo Dev CLI, IDE, and SDKs.",
			"count", AccuracyApproximate, "uniqMergeIf(users, product IN ('dev', 'cli', 'sdk', 'code'))", false),
		productRollupMetric("dev.sessions", "dev",
			"Distinct Hanzo Dev coding and build sessions.",
			"count", AccuracyApproximate, "uniqMergeIf(sessions, product IN ('dev', 'cli', 'sdk', 'code'))", false),
		productRollupMetric("dev.bytes", "dev",
			"Total Hanzo Dev telemetry and artifact payload bytes.",
			"bytes", AccuracyExact, "sumMergeIf(bytes, product IN ('dev', 'cli', 'sdk', 'code'))", true),
		aiRollupMetric("dev.api_requests", "dev",
			"Total developer API inference calls routed through the AI gateway.",
			"count", AccuracyExact, "countMerge(requests)", true),
		aiRollupMetric("dev.api_tokens", "dev",
			"Total developer API tokens consumed through the AI gateway.",
			"tokens", AccuracyExact, "sumMerge(tokens)", true),
		commerceDailyMetric("dev.spend", "dev",
			"Metered customer spend attributed to Hanzo Dev in USD cents.",
			"usd_cents", "sumIf(amount_minor, product IN ('dev', 'cli', 'sdk', 'code') AND kind = 'usage')", true),

		// ── 14. Cyber (7 metrics) ────────────────────────────────────────────
		stateMetric("cyber.forbidden_users", "cyber",
			"Count of blocked, suspended, or forbidden accounts across organizations.",
			"count", "iam", "dim_users", "analytics.dim_users",
			[]string{"org"}, 30, "countIf(forbidden = 1)", true),
		stateMetric("cyber.admin_users", "cyber",
			"Count of privileged admin identities audited for least-privilege posture.",
			"count", "iam", "dim_users", "analytics.dim_users",
			[]string{"org"}, 30, "countIf(is_admin = 1)", true),
		o11yRollupMetric("cyber.error_spans", "cyber",
			"Total security, auth, and KMS service error spans observed.",
			"count", AccuracyExact, "sumMergeIf(errors, service IN ('iam', 'kms', 'auth', 'cyber', 'security', 'waf'))", true),
		o11yRollupMetric("cyber.error_rate", "cyber",
			"Ratio of error spans across security, auth, and KMS services (0.0 to 1.0).",
			"ratio", AccuracyExact, "if(countMergeIf(spans, service IN ('iam', 'kms', 'auth', 'cyber', 'security', 'waf')) > 0, toFloat64(sumMergeIf(errors, service IN ('iam', 'kms', 'auth', 'cyber', 'security', 'waf'))) / toFloat64(countMergeIf(spans, service IN ('iam', 'kms', 'auth', 'cyber', 'security', 'waf'))), 0.0)", false),
		productRollupMetric("cyber.events", "cyber",
			"Total Hanzo Cyber security posture, scan, and audit telemetry events.",
			"count", AccuracyExact, "countMergeIf(events, product IN ('cyber', 'security', 'kms', 'audit'))", true),
		productRollupMetric("cyber.active_users", "cyber",
			"Distinct operators and identities generating Cyber security events.",
			"count", AccuracyApproximate, "uniqMergeIf(users, product IN ('cyber', 'security', 'kms', 'audit'))", false),
		productRollupMetric("cyber.audit_sessions", "cyber",
			"Distinct security audit and KMS access sessions recorded.",
			"count", AccuracyApproximate, "uniqMergeIf(sessions, product IN ('cyber', 'security', 'kms', 'audit'))", false),
	}
}

var (
	catalogMu      sync.RWMutex
	catalogList    []MetricDef
	catalogByIDMap map[string]MetricDef
)

func init() {
	defaults := builtinCatalog()
	catalogList = make([]MetricDef, 0, len(defaults))
	catalogByIDMap = make(map[string]MetricDef, len(defaults))
	for _, m := range defaults {
		MustRegister(m)
	}
}

func cloneMetricDef(m MetricDef) MetricDef {
	m.Dimensions = slices.Clone(m.Dimensions)
	m.Tables = slices.Clone(m.Tables)
	return m
}

// Register validates and registers a governed semantic metric in the global catalog.
// Safe for concurrent use. Rejects duplicate IDs, invalid domains/units/accuracy,
// unsafe SQL expressions, unapproved physical tables, or dimensions outside the closed
// allowlist.
func Register(def MetricDef) error {
	if err := validateMetricDef(&def); err != nil {
		return err
	}
	stored := cloneMetricDef(def)
	catalogMu.Lock()
	defer catalogMu.Unlock()
	if _, exists := catalogByIDMap[stored.ID]; exists {
		return fmt.Errorf("analytics: metric %q is already registered", stored.ID)
	}
	catalogList = append(catalogList, stored)
	catalogByIDMap[stored.ID] = stored
	return nil
}

// MustRegister registers def or panics if validation fails or the metric ID already exists.
func MustRegister(def MetricDef) {
	if err := Register(def); err != nil {
		panic(err)
	}
}

// CatalogMetrics returns a copy of all registered metrics, optionally filtered by domain.
func CatalogMetrics(domain string) []MetricDef {
	domain = strings.ToLower(strings.TrimSpace(domain))
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	out := make([]MetricDef, 0, len(catalogList))
	for _, m := range catalogList {
		if domain != "" && m.Domain != domain {
			continue
		}
		out = append(out, cloneMetricDef(m))
	}
	return out
}

// LookupMetric returns the registered MetricDef for id, or false if unregistered.
func LookupMetric(id string) (MetricDef, bool) {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	m, ok := catalogByIDMap[strings.TrimSpace(id)]
	if !ok {
		return MetricDef{}, false
	}
	return cloneMetricDef(m), true
}
