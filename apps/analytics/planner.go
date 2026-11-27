// Copyright © 2026 Hanzo AI. MIT License.

package analytics

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

const (
	// MaxGroupBy caps the number of group_by dimensions per query to bound cardinality.
	MaxGroupBy = 2
	// MaxMetricsPerQuery caps the number of metrics requested in a single query.
	MaxMetricsPerQuery = 16
	// DefaultLimit is applied when a query omits limit.
	DefaultLimit = 100
	// MaxLimit is the hard ceiling for returned rows per query.
	MaxLimit = 500
	// DefaultWindow is used when a query omits window.
	DefaultWindow = "24h"
	// MaxWindow is the hard ceiling on lookback duration (365 days).
	MaxWindow = 365 * 24 * time.Hour
	// MinuteAutoThreshold is the maximum window where auto granularity selects 1-minute real-time rollups.
	MinuteAutoThreshold = 2 * time.Hour
	// MaxMinuteWindow is the maximum window permitted when explicitly requesting minute granularity (48h TTL).
	MaxMinuteWindow = 48 * time.Hour
	// HourlyAutoThreshold is the maximum window where auto granularity selects hourly rollups.
	HourlyAutoThreshold = 7 * 24 * time.Hour
	// MaxHourlyWindow is the maximum window permitted when explicitly requesting hourly granularity.
	MaxHourlyWindow = 30 * 24 * time.Hour
)

// PlanError classifies planner rejections with an HTTP status code (400/401/403).
type PlanError struct {
	Code    int
	Message string
}

func (e *PlanError) Error() string { return e.Message }

func badRequest(format string, args ...any) error {
	return &PlanError{Code: 400, Message: fmt.Sprintf(format, args...)}
}

func forbidden(format string, args ...any) error {
	return &PlanError{Code: 403, Message: fmt.Sprintf(format, args...)}
}

// Scope represents the authenticated caller's validated tenant isolation boundary.
// Never construct Scope from unverified client headers or request fields.
type Scope struct {
	// Super is true only for platform SuperAdmins (principal.Super).
	Super bool
	// Org is the caller's validated organization slug when Super is false,
	// or an optional target org selected by a SuperAdmin.
	Org string
}

// Key returns the canonical cache and SSE hub scope identifier ("global" or "org:<slug>").
func (s Scope) Key() string {
	if s.Super && strings.TrimSpace(s.Org) == "" {
		return "global"
	}
	return "org:" + strings.TrimSpace(s.Org)
}

// IsGlobal reports whether this scope queries across the entire fleet.
func (s Scope) IsGlobal() bool {
	return s.Super && strings.TrimSpace(s.Org) == ""
}

// ResolveScope extracts the caller's validated IAM identity and tenant scope from ctx.
// The ctx accessors are the same facts ResolveScopeCtx reads off a *zip.Ctx — the
// bridge parks them for a routed HTTP op, and zip forwards them for a tool call —
// so both paths answer with one rule and neither needs the raw request. The raw
// SSE handler, which holds a *zip.Ctx and no op context, calls ResolveScopeCtx.
// Fails closed with 401 when unauthenticated, and 403 when an authenticated non-super
// caller has no validated organization.
func ResolveScope(ctx context.Context) (Scope, error) {
	if principal.SuperFrom(ctx) {
		return Scope{Super: true}, nil
	}
	if !principal.ValidatedFrom(ctx) {
		return Scope{}, zip.ErrUnauthorized("authentication required")
	}
	org, ok := principal.OrgFrom(ctx)
	if !ok || strings.TrimSpace(org) == "" {
		return Scope{}, zip.ErrForbidden("organization scope required")
	}
	return Scope{Super: false, Org: org}, nil
}

// ResolveScopeCtx extracts the validated tenant scope directly from a *zip.Ctx
// (used by both typed HTTP ops and the raw SSE stream endpoint).
func ResolveScopeCtx(c *zip.Ctx) (Scope, error) {
	if c == nil {
		return Scope{}, zip.ErrUnauthorized("authentication required")
	}
	if principal.Super(c) {
		return Scope{Super: true}, nil
	}
	if !principal.Validated(c) {
		return Scope{}, zip.ErrUnauthorized("authentication required")
	}
	org, ok := principal.Org(c)
	if !ok || strings.TrimSpace(org) == "" {
		return Scope{}, zip.ErrForbidden("organization scope required")
	}
	return Scope{Super: false, Org: org}, nil
}

// EnforceOrgParam validates an optional client-supplied org selector against the caller's Scope.
// SuperAdmin may pass "" (global) or any valid org slug.
// Non-super callers are HARD-scoped to their validated org and rejected 403 if they
// attempt to address "global", "*", or any other tenant's org.
func EnforceOrgParam(scope Scope, requestedOrg string) (Scope, error) {
	reqOrg := strings.TrimSpace(requestedOrg)
	if !scope.Super {
		if strings.TrimSpace(scope.Org) == "" {
			return Scope{}, forbidden("organization scope required")
		}
		if reqOrg != "" && (strings.EqualFold(reqOrg, "global") || reqOrg == "*" || reqOrg != scope.Org) {
			return Scope{}, forbidden("cross-tenant analytics access is forbidden")
		}
		return Scope{Super: false, Org: scope.Org}, nil
	}
	if reqOrg == "" || strings.EqualFold(reqOrg, "global") {
		return Scope{Super: true}, nil
	}
	if len(reqOrg) > principal.MaxOrgLen || principal.OrgHasUnsafeRune(reqOrg) {
		return Scope{}, badRequest("invalid org identifier")
	}
	return Scope{Super: true, Org: reqOrg}, nil
}

// QueryPlan is the compiled, injection-safe execution plan emitted by Plan.
type QueryPlan struct {
	// Table is the coarsest sufficient pre-aggregated analytics.* table selected.
	Table string
	// Window is the normalized lookback window string (e.g. "24h", "7d", "30d").
	Window string
	// Duration is the parsed time duration of Window.
	Duration time.Duration
	// Granularity is the selected aggregation granularity ("hour", "day", or "snapshot").
	Granularity string
	// EffectiveScope is the enforced scope ("global" or "org:<slug>").
	EffectiveScope Scope
	// Metrics are the validated, deduplicated catalog metric definitions.
	Metrics []MetricDef
	// GroupBy are the validated group_by dimension keys.
	GroupBy []string
	// Limit is the enforced row limit (1..500).
	Limit int
	// Source is the upstream data source of the planned metrics.
	Source string
	// Accuracy is the combined accuracy classification ("exact", "approximate", or "estimated").
	Accuracy string
	// SQL is the parameterized read-only ClickHouse SQL statement for rows.
	SQL string
	// Args are the positional ? arguments bound to SQL.
	Args []any
	// TotalsSQL is the optional parameterized summary SQL when GroupBy is non-empty.
	TotalsSQL string
	// TotalsArgs are the positional ? arguments bound to TotalsSQL.
	TotalsArgs []any
}

// Plan validates req against the canonical metric catalog, IAM tenant scope, and
// cardinality/cost budgets, then selects the coarsest sufficient pre-aggregated
// analytics.* table and emits a strictly parameterized SQL plan.
func Plan(req QueryIn, scope Scope) (QueryPlan, error) {
	return planAt(req, scope, time.Now().UTC())
}

func planAt(req QueryIn, scope Scope, now time.Time) (QueryPlan, error) {
	// 1. Enforce IAM tenant isolation first.
	if !scope.Super && strings.EqualFold(strings.TrimSpace(req.Org), "global") {
		return QueryPlan{}, forbidden("global analytics scope requires SuperAdmin")
	}
	effectiveScope, err := EnforceOrgParam(scope, req.Org)
	if err != nil {
		return QueryPlan{}, err
	}

	// 2. Validate metrics against canonical catalog.
	if len(req.Metrics) == 0 {
		return QueryPlan{}, badRequest("at least one metric is required")
	}
	if len(req.Metrics) > MaxMetricsPerQuery {
		return QueryPlan{}, badRequest("at most %d metrics may be queried at once", MaxMetricsPerQuery)
	}

	seenMetric := make(map[string]bool, len(req.Metrics))
	metrics := make([]MetricDef, 0, len(req.Metrics))
	var tableGroup string
	for _, rawID := range req.Metrics {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return QueryPlan{}, badRequest("metric id cannot be empty")
		}
		if seenMetric[id] {
			continue
		}
		def, ok := LookupMetric(id)
		if !ok {
			return QueryPlan{}, badRequest("unregistered metric %q", id)
		}
		if tableGroup == "" {
			tableGroup = def.TableGroup
		} else if def.TableGroup != tableGroup {
			return QueryPlan{}, badRequest("metric %q (%s) cannot be combined with %s in a single query", def.ID, def.TableGroup, tableGroup)
		}
		seenMetric[id] = true
		metrics = append(metrics, def)
	}

	// 3. Validate group_by cardinality budget and per-metric dimension allowlist.
	if len(req.GroupBy) > MaxGroupBy {
		return QueryPlan{}, badRequest("at most %d group_by dimensions are allowed", MaxGroupBy)
	}
	seenDim := make(map[string]bool, len(req.GroupBy))
	dims := make([]dimensionDef, 0, len(req.GroupBy))
	groupBy := make([]string, 0, len(req.GroupBy))
	needsMinute := false
	needsHour := false
	hasDay := false
	for _, rawDim := range req.GroupBy {
		key := strings.TrimSpace(rawDim)
		if key == "" {
			return QueryPlan{}, badRequest("group_by dimension cannot be empty")
		}
		if seenDim[key] {
			continue
		}
		ddef, ok := allowedDimensions[key]
		if !ok {
			return QueryPlan{}, badRequest("disallowed group_by dimension %q", key)
		}
		for _, m := range metrics {
			if !slices.Contains(m.Dimensions, key) {
				return QueryPlan{}, badRequest("dimension %q is not allowed for metric %q", key, m.ID)
			}
		}
		seenDim[key] = true
		dims = append(dims, ddef)
		groupBy = append(groupBy, key)
		if ddef.needsMinute {
			needsMinute = true
		}
		if ddef.needsHour {
			needsHour = true
		}
		if key == "day" {
			hasDay = true
		}
	}

	// 4. Validate limit budget (default 100, max 500).
	limit := req.Limit
	if limit == 0 {
		limit = DefaultLimit
	}
	if limit < 0 || limit > MaxLimit {
		return QueryPlan{}, badRequest("limit must be between 1 and %d", MaxLimit)
	}

	// 5. Parse and validate window budget (default 24h, max 365d).
	windowStr := strings.TrimSpace(req.Window)
	if windowStr == "" {
		windowStr = DefaultWindow
	}
	winDur, normWindow, err := ParseWindow(windowStr)
	if err != nil {
		return QueryPlan{}, err
	}

	// 6. Select coarsest sufficient aggregate table & granularity.
	lead := metrics[0]
	var table, granularity string
	if lead.TableKind == TableState {
		table = lead.StateTable
		granularity = "snapshot"
	} else {
		reqGran := strings.ToLower(strings.TrimSpace(req.Granularity))
		switch reqGran {
		case "", "auto":
			switch {
			case needsMinute:
				if lead.MinuteTable == "" {
					return QueryPlan{}, badRequest("minute grouping is not supported for metric %q", lead.ID)
				}
				if winDur > MaxMinuteWindow {
					return QueryPlan{}, badRequest("minute grouping is limited to windows <= 48h")
				}
				table = lead.MinuteTable
				granularity = "minute"
			case needsHour:
				if winDur > MaxHourlyWindow {
					return QueryPlan{}, badRequest("hourly grouping is limited to windows <= 30d")
				}
				table = lead.HourlyTable
				granularity = "hour"
			case hasDay:
				// Grouping by day only requires daily rollups regardless of window.
				table = lead.DailyTable
				granularity = "day"
			case winDur <= MinuteAutoThreshold && lead.MinuteTable != "":
				table = lead.MinuteTable
				granularity = "minute"
			case winDur <= HourlyAutoThreshold:
				table = lead.HourlyTable
				granularity = "hour"
			default:
				table = lead.DailyTable
				granularity = "day"
			}
		case "minute":
			if lead.MinuteTable == "" {
				return QueryPlan{}, badRequest("minute granularity is not supported for metric %q", lead.ID)
			}
			if winDur > MaxMinuteWindow {
				return QueryPlan{}, badRequest("minute granularity is limited to windows <= 48h")
			}
			table = lead.MinuteTable
			granularity = "minute"
		case "hour":
			if needsMinute {
				return QueryPlan{}, badRequest("cannot group by minute with hour granularity")
			}
			if winDur > MaxHourlyWindow {
				return QueryPlan{}, badRequest("hourly granularity is limited to windows <= 30d")
			}
			table = lead.HourlyTable
			granularity = "hour"
		case "day":
			if needsMinute {
				return QueryPlan{}, badRequest("cannot group by minute with day granularity")
			}
			if needsHour {
				return QueryPlan{}, badRequest("cannot group by hour with day granularity")
			}
			table = lead.DailyTable
			granularity = "day"
		default:
			return QueryPlan{}, badRequest("invalid granularity %q (expected auto, minute, hour, or day)", req.Granularity)
		}
	}

	// 7. Assemble strictly parameterized SQL (zero user-string interpolation).
	whereParts := make([]string, 0, 3)
	args := make([]any, 0, 3)

	timeCol := lead.HourlyTimeCol
	if granularity == "minute" && lead.MinuteTimeCol != "" {
		timeCol = lead.MinuteTimeCol
	} else if granularity == "day" && lead.DailyTimeCol != "" {
		timeCol = lead.DailyTimeCol
	}
	if lead.TableKind == TableRollup && timeCol != "" {
		since := now.Add(-winDur)
		if timeCol == "day" {
			whereParts = append(whereParts, "day >= toDate(?)", "day <= toDate(?)")
			args = append(args, since.UTC().Format("2006-01-02"), now.UTC().Format("2006-01-02"))
		} else {
			whereParts = append(whereParts, timeCol+" >= ?", timeCol+" <= ?")
			args = append(args, formatCHTime(since), formatCHTime(now))
		}
	}
	if !effectiveScope.IsGlobal() {
		whereParts = append(whereParts, lead.OrgCol+" = ?")
		args = append(args, effectiveScope.Org)
	}

	whereClause := ""
	if len(whereParts) > 0 {
		whereClause = " WHERE " + strings.Join(whereParts, " AND ")
	}

	metricExprs := make([]string, 0, len(metrics))
	for _, m := range metrics {
		metricExprs = append(metricExprs, m.Expr+" AS "+m.alias)
	}

	var rowsSQL, totalsSQL string
	var totalsArgs []any

	if len(dims) == 0 {
		rowsSQL = "SELECT " + strings.Join(metricExprs, ", ") + " FROM " + table + whereClause + " LIMIT 1"
	} else {
		dimSelect := make([]string, 0, len(dims))
		dimGroup := make([]string, 0, len(dims))
		orderCols := make([]string, 0, len(dims)+1)
		hasTimeDim := false
		for _, d := range dims {
			expr := d.sqlExpr
			if d.key == "day" && timeCol != "" {
				if timeCol == "day" {
					expr = "day"
				} else {
					expr = "toDate(" + timeCol + ")"
				}
			} else if d.key == "hour" && timeCol != "" {
				expr = "toStartOfHour(" + timeCol + ")"
			} else if d.key == "minute" && timeCol != "" {
				expr = "toStartOfMinute(" + timeCol + ")"
			}
			dimSelect = append(dimSelect, expr+" AS "+d.key)
			dimGroup = append(dimGroup, d.key)
			if d.isTimeDim {
				hasTimeDim = true
				orderCols = append(orderCols, d.key+" ASC")
			}
		}
		if !hasTimeDim {
			orderCols = append(orderCols, metrics[0].alias+" DESC")
		}
		selectCols := append(dimSelect, metricExprs...)
		rowsSQL = "SELECT " + strings.Join(selectCols, ", ") +
			" FROM " + table +
			whereClause +
			" GROUP BY " + strings.Join(dimGroup, ", ") +
			" ORDER BY " + strings.Join(orderCols, ", ") +
			" LIMIT " + strconv.Itoa(limit)

		totalsSQL = "SELECT " + strings.Join(metricExprs, ", ") + " FROM " + table + whereClause + " LIMIT 1"
		totalsArgs = slices.Clone(args)
	}

	accuracy := AccuracyExact
	for _, m := range metrics {
		if m.Accuracy == AccuracyEstimated {
			accuracy = AccuracyEstimated
			break
		}
		if m.Accuracy == AccuracyApproximate {
			accuracy = AccuracyApproximate
		}
	}

	return QueryPlan{
		Table:          table,
		Window:         normWindow,
		Duration:       winDur,
		Granularity:    granularity,
		EffectiveScope: effectiveScope,
		Metrics:        metrics,
		GroupBy:        groupBy,
		Limit:          limit,
		Source:         lead.Source,
		Accuracy:       accuracy,
		SQL:            rowsSQL,
		Args:           args,
		TotalsSQL:      totalsSQL,
		TotalsArgs:     totalsArgs,
	}, nil
}

// ParseWindow parses a lookback window string such as "24h", "7d", "30d", "90d", "365d"
// and enforces the 365d maximum budget.
func ParseWindow(raw string) (time.Duration, string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if len(s) < 2 {
		return 0, "", badRequest("invalid window %q (expected <n>h or <n>d)", raw)
	}
	unit := s[len(s)-1]
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return 0, "", badRequest("invalid window %q (expected positive integer with h or d suffix)", raw)
	}
	var dur time.Duration
	switch unit {
	case 'h':
		dur = time.Duration(n) * time.Hour
	case 'd':
		dur = time.Duration(n) * 24 * time.Hour
	default:
		return 0, "", badRequest("invalid window unit in %q (expected h or d)", raw)
	}
	if dur > MaxWindow {
		return 0, "", badRequest("window %q exceeds maximum 365d", raw)
	}
	return dur, strconv.Itoa(n) + string(unit), nil
}

// PlanErrorToZip converts a PlanError (or backend sentinel error) into the matching zip HTTP error.
func PlanErrorToZip(err error) error {
	if err == nil {
		return nil
	}
	var pe *PlanError
	if errors.As(err, &pe) {
		switch pe.Code {
		case 400:
			return zip.ErrBadRequest(pe.Message)
		case 401:
			return zip.ErrUnauthorized(pe.Message)
		case 403:
			return zip.ErrForbidden(pe.Message)
		default:
			return zip.Errorf(pe.Code, "%s", pe.Message)
		}
	}
	if errors.Is(err, ErrOverloaded) {
		return zip.Errorf(429, "%s", err.Error())
	}
	if errors.Is(err, ErrDatastoreUnavailable) {
		return zip.Errorf(503, "%s", err.Error())
	}
	return zip.Errorf(503, "analytics query failed: %v", err)
}
