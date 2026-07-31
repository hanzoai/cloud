package o11y

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/apps/datastore"
	"github.com/zap-proto/zip"
)

// The scoped, tenant-isolated o11y surface: /v1/o11y/{logs,metrics,status}. These
// are the ONLY code paths that serve product logs/metrics/status, and every one
// pins the org SERVER-SIDE (tenantOf, typed.go) into the query — the client never
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
// It takes the App rather than a Router because half of what it registers are
// TYPED ops, and those need the registry every projection reads: a Router that
// could not reach one would serve these reads to nothing but HTTP.
func mountScope(a *zip.App) {
	// Tenant-scoped, org-pinned reads — the ONE owner of these paths (handlers
	// below), declared as TYPED ops on the REGISTRY with their WHOLE path spelled
	// so every projection (the document, the MCP tool, the CLI command, the SDK
	// method) follows from this one registration. cmd/zipdoc files each doc
	// comment under the literal path it was handed, so spelling it whole here is
	// what lets the prose below reach the document and the tool list.
	zip.Get(a, o11yPrefix+"/logs", handleLogs)
	zip.Get(a, o11yPrefix+"/metrics", handleMetrics)
	zip.Get(a, o11yPrefix+"/status", handleStatus)
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

// ---- the typed reads (the published contract) ----

// logsIn selects one product's log window. Every field rides in the query
// string; the org is NEVER one of them — it is the validated tenant, resolved
// server-side.
type logsIn struct {
	// Product is the console product slug whose logs to read, e.g. "kms".
	// Required.
	Product string `json:"product"`
	// SinceNs is the nanosecond cursor from a previous response's nextCursor.
	// Absent (0) reads the last `window` seconds instead.
	SinceNs int64 `json:"sinceNs"`
	// Window is how many seconds back to read when there is no cursor.
	// Default 900, capped at 86400.
	Window int `json:"window"`
	// Limit caps the returned lines. Default 200, capped at 1000.
	Limit int `json:"limit"`
}

// metricsIn selects one product's RED window. The org is the validated tenant,
// never a field.
type metricsIn struct {
	// Product is the console product slug to read, e.g. "kms". Required.
	Product string `json:"product"`
	// Range is the window in seconds. Default 3600, capped at 604800 (7d).
	Range int `json:"range"`
	// StepSec is the bucket width in seconds, clamped to [30, 3600]. Absent
	// picks ~60 buckets across the range.
	StepSec int `json:"stepSec"`
}

// statusIn names the product to probe.
type statusIn struct {
	// Product is the console product slug to probe, e.g. "kms". Required.
	Product string `json:"product"`
}

// GetO11yLogs returns a page of one product's logs for the caller's org. A
// normal caller sees its OWN request stream, derived from org-tagged spans; a
// validated platform SuperAdmin sees the product's raw infra stdout stream
// instead. Poll for a live tail by passing the previous response's nextCursor
// back as sinceNs. A well-formed product with no backing workload answers an
// empty page rather than an error; a malformed slug is a 400.
//
// Example: {"product": "kms", "limit": 200}
func handleLogs(ctx context.Context, in *logsIn) (*logsResponse, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	svc, resolved, err := requireService(in.Product)
	if err != nil {
		return nil, err
	}
	isAdmin := callerIsAdmin(ctx)
	if !resolved {
		// Well-formed but unbacked product → honest-empty (never an error, never
		// another product's data).
		return &logsResponse{Product: svc.ID, View: viewFor(isAdmin), Lines: []logLine{}}, nil
	}
	rctx, cancel := context.WithTimeout(ctx, logReadTimeout)
	defer cancel()
	resp, err := queryLogs(rctx, svc, org, isAdmin,
		boundSinceNs(in.SinceNs), boundWindowSec(in.Window), boundLogLimit(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "o11y logs: %v", err)
	}
	return &resp, nil
}

// GetO11yMetrics returns one product's RED series — request rate, errors, p50
// and p95 latency — for the caller's org, plus that org's LLM usage rollup over
// the same window. The series come from org-tagged request spans, so a tenant
// only ever aggregates its own traffic; a validated platform SuperAdmin sees the
// whole product's RED, while usage stays the caller's own org either way. A
// well-formed product with no backing workload answers empty series; a malformed
// slug is a 400.
//
// Example: {"product": "kms", "range": 3600}
func handleMetrics(ctx context.Context, in *metricsIn) (*metricsResponse, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	svc, resolved, err := requireService(in.Product)
	if err != nil {
		return nil, err
	}
	if !resolved {
		empty := emptyMetrics(svc.ID)
		return &empty, nil
	}
	if !datastore.Ready() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "o11y metrics: datastore not connected")
	}
	rangeSec := boundRangeSec(in.Range)
	stepSec := stepFor(rangeSec, in.StepSec)

	rctx, cancel := context.WithTimeout(ctx, metricsTimeout)
	defer cancel()

	resp, err := queryMetrics(rctx, metricsQuery{svc: svc, org: org, admin: callerIsAdmin(ctx), rangeSec: rangeSec, stepSec: stepSec})
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "o11y metrics: %v", err)
	}
	if err := usageSeries(rctx, org, rangeSec, stepSec, &resp); err != nil {
		// Usage is a secondary signal (LLM ledger); a miss is logged upstream, never
		// fails the whole tab — the RED series still render.
		ensureSeries(&resp)
	}
	return &resp, nil
}

// GetO11yStatus reports whether a product's service is live: an in-cluster
// health probe with its measured latency, fused with the per-replica up
// inventory. Infra health is not tenant-partitioned — a service is up or down
// for everyone — so any validated caller is served, but an unvalidated one is
// refused. A product with no backing workload answers down/unknown-service
// without probing anything; a malformed slug is a 400.
//
// Example: {"product": "kms"}
func handleStatus(ctx context.Context, in *statusIn) (*statusResult, error) {
	if !callerValidated(ctx) {
		return nil, zip.ErrForbidden("a validated principal is required")
	}
	svc, resolved, err := requireService(in.Product)
	if err != nil {
		return nil, err
	}
	if !resolved {
		unknown := unknownStatus(svc.ID)
		return &unknown, nil
	}
	status := probeStatus(ctx, svc)
	return &status, nil
}

// requireService shape-validates the caller's product slug and resolves it
// through the alias + allowlist table. A missing or malformed slug is a 400 at the
// boundary (never smuggles injection/SSRF); a well-formed but unbacked product
// returns resolved=false with svc.ID set, so the handler answers honest-empty.
func requireService(raw string) (svc service, resolved bool, err error) {
	raw = strings.TrimSpace(raw)
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
