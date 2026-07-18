// Package metrics is the fleet SaaS-operations god-view (/v1/admin/metrics) — the
// operator's business dashboard: MRR/ARR, net-new vs churned MRR, the plan/category
// mix, the top customers, and the recent subscription movements. SuperAdmin only
// (core.Guard).
//
// It OWNS no aggregation. The whole snapshot is computed IN commerce (the system of
// record for subscriptions + the usage ledger) by its cross-org SaaS-metrics engine
// (GET /v1/metrics/saas), which admin PROXIES with the SAME admin-scoped S2S service
// token finance uses for COGS. The engine is ALREADY fleet-wide — it walks every org
// namespace itself — so this is a SINGLE upstream read, no per-org fan-out, exactly as
// finance consumes commerce Costs. An unwired or unreachable commerce degrades to an
// honest empty snapshot (real zeros, `[]` not null) with a not-ok source, never a
// fabricated number.
package metrics

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/commerce"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/zap-proto/zip"
)

// errUnconfigured marks commerce not wired on this deployment — core.SrcOf reports it as
// a not-ok source so the console renders the honest not-configured state.
var errUnconfigured = errors.New("commerce metrics not configured")

// defaultLimit caps the top-customers list when the caller sends none (mirrors the
// commerce engine's own default so the proxy never asks for more than it returns).
const defaultLimit = 20

// MetricsData is the GET /v1/admin/metrics payload: the commerce SaaS snapshot, flat,
// plus the admin read time and the upstream freshness strip every god-view carries.
type MetricsData struct {
	commerce.SaaSMetrics
	GeneratedAt string              `json:"generatedAt"`
	Sources     []core.SourceStatus `json:"sources"`
}

// Metrics answers GET /v1/admin/metrics by proxying the commerce SaaS-metrics engine
// (already a fleet-wide cross-org aggregate). SuperAdmin only.
//
//	GET /v1/admin/metrics?window=30d&limit=20
func Metrics(s *cloud.Service[core.State], c *zip.Ctx) error {
	ctx := c.Context()
	now := time.Now().UTC().Format(time.RFC3339)
	window := strings.TrimSpace(c.Query("window"))
	limit := parseLimit(c.Query("limit"))

	if !s.State.Commerce.Ready() {
		return core.OK(c, empty(now, window, core.SrcOf("commerce-metrics", errUnconfigured, 0, now)))
	}
	m, err := s.State.Commerce.Metrics(ctx, window, limit)
	if err != nil {
		return core.OK(c, empty(now, window, core.SrcOf("commerce-metrics", err, 0, now)))
	}
	return core.OK(c, MetricsData{
		SaaSMetrics: normalize(m),
		GeneratedAt: now,
		Sources:     []core.SourceStatus{core.SrcOf("commerce-metrics", nil, m.Orgs, now)},
	})
}

// empty is the honest not-configured/unreachable snapshot: real zeros + empty slices
// (never null, never fabricated) plus the not-ok source.
func empty(now, window string, src core.SourceStatus) MetricsData {
	return MetricsData{
		SaaSMetrics: normalize(commerce.SaaSMetrics{AsOf: now, Currency: "usd", Window: window}),
		GeneratedAt: now,
		Sources:     []core.SourceStatus{src},
	}
}

// normalize replaces nil slices with empty ones so the JSON is honest arrays (`[]`, not
// null) and the console never has to guard a missing collection.
func normalize(m commerce.SaaSMetrics) commerce.SaaSMetrics {
	if m.Revenue.ByCategory == nil {
		m.Revenue.ByCategory = []commerce.SaaSCategory{}
	}
	if m.Subs.ByPlan == nil {
		m.Subs.ByPlan = []commerce.SaaSPlan{}
	}
	if m.Subs.Recent == nil {
		m.Subs.Recent = []commerce.SaaSEvent{}
	}
	if m.Customers == nil {
		m.Customers = []commerce.SaaSCustomer{}
	}
	if m.Gaps == nil {
		m.Gaps = []string{}
	}
	return m
}

// parseLimit clamps the top-N cap to [1,200], defaulting to defaultLimit — mirrors the
// commerce engine's clamp exactly.
func parseLimit(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return defaultLimit
	}
	if n > 200 {
		return 200
	}
	return n
}
