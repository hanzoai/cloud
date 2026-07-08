package o11y

import (
	"fmt"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// The scoped, tenant-isolated o11y surface: /v1/o11y/{logs,metrics,status}. These
// are the ONLY code paths that serve product logs/metrics/status, and every one
// pins the org SERVER-SIDE (principal.Tenant) into the query — the client never
// supplies an org, a raw query, or a PromQL/SQL fragment. They are registered
// BEFORE the hanzoai/o11y wildcard (`app.All("/v1/o11y/*")`, order 70) so Fiber's
// in-order match gives these specific routes precedence; the wildcard proxy
// (o11y.go) is left to serve only the genuine staff-admin runtime UI and is
// ADMIN-gated, so there is no un-scoped path to product telemetry.
//
// Ordering: registered at order 69 (< 70) so MountAll mounts this subsystem — and
// thus registers these static routes — before the o11y module installs its
// wildcard. serve.go's HIP-0106 health loop adds GET /v1/o11yscope/health.

func init() {
	cloud.Register("o11yscope", 69, func(app any, _ cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("o11yscope.Mount: app is %T, want *zip.App", app)
		}
		a.Get("/v1/o11y/logs", handleLogs)
		a.Get("/v1/o11y/metrics", handleMetrics)
		a.Get("/v1/o11y/status", handleStatus)
		return nil
	})
}

// handleLogs serves org-scoped product logs. The org is the validated tenant; a
// non-admin sees ONLY rows carrying its own org attribution (empty for unattributed
// history — never another tenant's stream); a validated global admin sees the full
// stream. An unknown/unbacked product is honest-empty, not an error.
func handleLogs(c *zip.Ctx) error {
	org, ok := principal.Tenant(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	svc, known := resolveService(c.Query("product"))
	if !known {
		return c.JSON(http.StatusOK, map[string]any{"lines": []logLine{}, "cursor": ""})
	}
	q := logQuery{
		app:        svc.App,
		org:        org,
		admin:      c.IsAdmin(),
		sinceHours: boundSinceHours(c.Query("since")),
		afterNano:  parseCursor(c.Query("after")),
		limit:      boundLogLimit(c.Query("limit")),
	}
	lines, cursor, err := queryLogs(c.Context(), q)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "logs: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"lines": lines, "cursor": cursor})
}

// handleMetrics serves org-scoped RED series + a per-org usage rollup. RED
// selectors are pinned to the product and (non-admin) the org; usage is bound to
// the caller's own org. Unknown/unbacked product → honest-empty result.
func handleMetrics(c *zip.Ctx) error {
	org, ok := principal.Tenant(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	svc, known := resolveService(c.Query("product"))
	if !known {
		return c.JSON(http.StatusOK, emptyMetrics(c.Query("product")))
	}
	res, err := queryMetrics(c.Context(), metricsQuery{
		svc:     svc,
		org:     org,
		admin:   c.IsAdmin(),
		minutes: boundRangeMinutes(c.Query("minutes")),
	})
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "metrics: %v", err)
	}
	return c.JSON(http.StatusOK, res)
}

// handleStatus serves the live health of a product's service. Principal-gated
// (infra health is not tenant-partitioned, but an unvalidated caller is refused).
// The probe target is only ever an allowlisted host (SSRF boundary); an
// unknown/unbacked product → honest down/unknown-service, never a probe.
func handleStatus(c *zip.Ctx) error {
	if !principal.Validated(c) {
		return zip.ErrForbidden("a validated principal is required")
	}
	product := c.Query("product")
	svc, known := resolveService(product)
	if !known {
		return c.JSON(http.StatusOK, unknownStatus(product))
	}
	return c.JSON(http.StatusOK, probeStatus(c.Context(), svc))
}

// emptyMetrics is the honest metrics response for an unbacked product: no series,
// usage unavailable, source names the honest state.
func emptyMetrics(product string) metricsResult {
	return metricsResult{
		Product:     product,
		Up:          []metricPoint{},
		RequestRate: []metricPoint{},
		ErrorRate:   []metricPoint{},
		P50:         []metricPoint{},
		P95:         []metricPoint{},
		Usage:       usageRollup{Available: false},
		Source:      "unknown-service",
	}
}
