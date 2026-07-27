package o11y

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/datastore"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// The scoped, tenant-isolated o11y surface: /v1/o11y/{logs,metrics,status}. These
// are the ONLY code paths that serve product logs/metrics/status, and every one
// pins the org SERVER-SIDE (principal.Org) into the query — the client never
// supplies an org, a raw query, or a PromQL/SQL fragment. They are registered
// BEFORE the hanzoai/o11y wildcard (`app.All("/v1/o11y/*")`, order 70) so Fiber's
// in-order match gives these specific routes precedence; the wildcard proxy
// (o11y.go) is left to serve only the genuine o11y runtime UI/query API and is
// principal-gated, so there is no un-scoped path to product telemetry.
//
// This is the ONE owner of /v1/o11y/{logs,metrics,status}. It supersedes the former
// clients/observe surface (now deleted): the rich per-org RED metrics + LLM usage
// (metricsread.go) and the two-view logs (logs.go, admin infra + per-org request)
// were folded in here so nothing was lost, and the duplicate mount is gone.
//
// Ordering: mountScope runs inside the one order-69 `o11y` mount (mountO11y), so
// these static routes register before the hanzoai/o11y module installs its wildcard
// at order 70. serve.go's HIP-0106 health loop adds GET /v1/o11y/health (once, via
// the module's order-70 co-registration of the `o11y` name — see o11y.go).

// admin reports whether the caller is a validated platform SuperAdmin. After
// SanitizeIdentity, c.IsAdmin() (X-User-IsAdmin) is set to true ONLY for a verified
// SuperAdmin whose owner == the reserved admin org (middleware_identity.go) — the
// SAME predicate every cloud admin surface gates on. So the infra-log god-view and
// the whole-product metrics view use the platform-sudo scope, never a per-org
// isAdmin claim (which would be a privilege-escalation path to cross-tenant infra
// telemetry).
func admin(c *zip.Ctx) bool { return c.IsAdmin() }

// mountScope registers the cloud-native SPECIFIC /v1/o11y/* routes — the ONE place
// the whole specific-route table is declared, so the "before the wildcard" invariant
// is obvious and lives in a single spot. Called by mountO11y (o11y.go) inside the one
// order-69 mount, so every route here precedes the hanzoai/o11y wildcard (order 70).
// The public surface is FLAT and version-less (one /v1/, no nested /api/vN): the
// upstream engine version is an internal impl detail resolved inside the
// handlers, never leaked into the route.
func mountScope(a cloud.Router) {
	// Tenant-scoped, org-pinned reads — the ONE owner of these paths (handlers below).
	a.Get("/v1/o11y/logs", handleLogs)
	a.Get("/v1/o11y/metrics", handleMetrics)
	a.Get("/v1/o11y/status", handleStatus)
	// SuperAdmin-only VictoriaMetrics read proxy (vmproxy.go). Flat public paths;
	// the upstream `api/v1/*` nesting stays INSIDE the handler, never in our route.
	// Allowlisted to {up, sum(up), count(up)} only.
	a.Get("/v1/o11y/vm/query", handleVMQuery)
	a.Get("/v1/o11y/vm/query_range", handleVMQueryRange)
	// Flat builder query (query.go): the ONE canonical public path for the console's
	// composite list query; the upstream engine version (v3) is resolved INTERNALLY.
	a.Post("/v1/o11y/query", builderQueryHandler("query"))
	a.Post("/v1/o11y/query_range", builderQueryHandler("query_range"))
	// Flat, org-gated LLM-obs sessions list (sessions.go): pins the runtime's
	// /api/sessions route and refuses an org-less caller at the cloud boundary.
	a.Get("/v1/o11y/sessions", sessionsHandler)
}

// handleLogs serves org-scoped product logs. The org is the validated tenant; a
// non-admin sees ONLY its own request stream (org-tagged traces); a validated
// SuperAdmin sees the raw infra stdout stream. An unknown/unbacked product is
// honest-empty; a malformed product slug is a 400 at the boundary.
func handleLogs(c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	svc, resolved, err := requireService(c)
	if err != nil {
		return err
	}
	isAdmin := admin(c)
	if !resolved {
		// Well-formed but unbacked product → honest-empty (never an error, never
		// another product's data).
		return c.JSON(http.StatusOK, logsResponse{Product: svc.ID, View: viewFor(isAdmin), Lines: []logLine{}})
	}
	ctx, cancel := context.WithTimeout(c.Context(), logReadTimeout)
	defer cancel()
	resp, err := queryLogs(ctx, svc, org, isAdmin,
		boundSinceNs(c.Query("sinceNs")), boundWindowSec(c.Query("window")), boundLogLimit(c.Query("limit")))
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "o11y logs: %v", err)
	}
	return c.JSON(http.StatusOK, resp)
}

// handleMetrics serves org-scoped RED series + a per-org LLM usage rollup. RED
// selectors are pinned to the product and (non-admin) the org; usage is always the
// caller's own org. Unknown/unbacked product → honest-empty; malformed slug → 400.
func handleMetrics(c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	svc, resolved, err := requireService(c)
	if err != nil {
		return err
	}
	if !resolved {
		return c.JSON(http.StatusOK, emptyMetrics(svc.ID))
	}
	if !datastore.Ready() {
		return zip.Errorf(http.StatusServiceUnavailable, "o11y metrics: datastore not connected")
	}
	rangeSec := boundRangeSec(c.Query("range"))
	stepSec := stepFor(rangeSec, c.Query("stepSec"))

	ctx, cancel := context.WithTimeout(c.Context(), metricsTimeout)
	defer cancel()

	resp, err := queryMetrics(ctx, metricsQuery{svc: svc, org: org, admin: admin(c), rangeSec: rangeSec, stepSec: stepSec})
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "o11y metrics: %v", err)
	}
	if err := usageSeries(ctx, org, rangeSec, stepSec, &resp); err != nil {
		// Usage is a secondary signal (LLM ledger); a miss is logged upstream, never
		// fails the whole tab — the RED series still render.
		ensureSeries(&resp)
	}
	return c.JSON(http.StatusOK, resp)
}

// handleStatus serves the live health of a product's service. Principal-gated
// (infra health is not tenant-partitioned, but an unvalidated caller is refused).
// The probe target is only ever an allowlisted host (SSRF boundary); an
// unknown/unbacked product → honest down/unknown-service; malformed slug → 400.
func handleStatus(c *zip.Ctx) error {
	if !principal.Validated(c) {
		return zip.ErrForbidden("a validated principal is required")
	}
	svc, resolved, err := requireService(c)
	if err != nil {
		return err
	}
	if !resolved {
		return c.JSON(http.StatusOK, unknownStatus(svc.ID))
	}
	return c.JSON(http.StatusOK, probeStatus(c.Context(), svc))
}

// requireService reads + shape-validates the ?product query param and resolves it
// through the alias + allowlist table. A missing or malformed slug is a 400 at the
// boundary (never smuggles injection/SSRF); a well-formed but unbacked product
// returns resolved=false with svc.ID set, so the handler answers honest-empty.
func requireService(c *zip.Ctx) (svc service, resolved bool, err error) {
	raw := strings.TrimSpace(c.Query("product"))
	if raw == "" {
		return service{}, false, zip.ErrBadRequest("product query param is required")
	}
	if !validProduct(raw) {
		return service{}, false, zip.ErrBadRequest("product must be a slug (lowercase alnum + hyphen, ≤63)")
	}
	s, ok := resolveService(raw)
	if !ok {
		return service{ID: raw}, false, nil
	}
	return s, true, nil
}

// emptyMetrics is the honest metrics response for an unbacked product: empty series,
// usage zero, product echoed.
func emptyMetrics(product string) metricsResponse {
	var resp metricsResponse
	resp.Product = product
	ensureSeries(&resp)
	return resp
}
