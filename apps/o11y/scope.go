package o11y

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/apps/datastore"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// The PRODUCT face of o11y: the tenant-isolated reads keyed by a console product
// slug. Every one pins the org SERVER-SIDE (tenantOf, typed.go) into the query —
// the client names a PRODUCT and a bounded range, never an org, a raw query or a
// PromQL/SQL fragment.
//
// THE PRODUCT DIMENSION IS THE NAME. hanzoai/o11y went native and now declares
// every one of its 367 routes by name, so there is no wildcard left for a host
// route to shadow: two declarations at one address is a refusal to compose. Three
// addresses collided, and each was a DIFFERENT QUESTION wearing the same name:
//
//   - GET /v1/o11y/metrics is the module's metric-NAME CATALOG (which metrics
//     exist in the store). Ours is one product's RED window. Two questions, so
//     ours moved to the address that says which one it answers —
//     /v1/o11y/product/metrics — and the module keeps the bare name.
//   - GET /v1/o11y/logs was ours and had NO caller (the console reaches logs
//     through the query engine); the module's is the real log-record read. Ours
//     is gone rather than renamed — an address nobody calls is not a contract.
//   - POST /v1/o11y/query_range pinned the runtime's v3 engine route, and the
//     runtime has no v3 route left to pin (no /api/* routes at all since it
//     dropped prefix-stripping, and queryRangeV3 has no caller). A route that
//     forwards to an address nothing serves is dead; the module's v5 querier
//     answers there now.
//
// So this file claims NOTHING back from the module (o11y.go mounts it with no
// Claimed option). A host that has to name the addresses it takes is a host that
// took addresses it did not own.
//
// /status and /availability keep their bare names because the module declares
// neither — nothing to disambiguate. If it ever does, /status joins /product/.
//
// Ordering: mountScope runs inside the one order-69 `o11y` mount (mountO11y).
// It no longer carries a "before the wildcard" invariant — the wildcard is gone,
// and every address here is one the module does not declare.

// admin reports whether the caller is a validated platform SuperAdmin. After
// SanitizeIdentity, c.IsAdmin() (X-User-IsAdmin) is set to true ONLY for a verified
// SuperAdmin whose owner == the reserved admin org (middleware_identity.go) — the
// SAME predicate every cloud admin surface gates on. So the infra-log god-view and
// the whole-product metrics view use the platform-sudo scope, never a per-org
// isAdmin claim (which would be a privilege-escalation path to cross-tenant infra
// telemetry).
func admin(c *zip.Ctx) bool { return c.IsAdmin() }

// mountScope registers the cloud-native /v1/o11y routes — the ONE place the whole
// table is declared. Called by mountO11y (o11y.go) inside the one order-69 mount.
// Every address here is one hanzoai/o11y does NOT declare, so registration order
// decides nothing and no claim is needed.
func mountScope(a *zip.App) {
	// Tenant-scoped, org-pinned reads, declared as TYPED ops at their FULL path so
	// every projection (the document, the MCP tool, the CLI command, the SDK method)
	// follows from this one registration — and so cmd/zipdoc, which resolves a path
	// statically, can read it. That is why these are not on a hand-rolled router
	// value: zipdoc cannot see through one, and the build gate refuses rather than
	// file the prose under a path it guessed.
	zip.Get(a, o11yPrefix+"/status", handleStatus)
	// Platform-sudo fleet availability (availability.go), read from the native
	// store. This is what remains of the VictoriaMetrics proxy that used to sit at
	// /v1/o11y/vm/{query,query_range}: the store is gone, so the route named after
	// it is gone, and the one question inside it we still MEASURE is asked here as
	// a typed op instead of as three allowlisted PromQL strings.
	zip.Get(a, o11yPrefix+"/availability", handleAvailability)
	// The per-product RED window (metricsread.go), under the dimension that says
	// which question it answers. The module's bare /v1/o11y/metrics is the metric
	// NAME CATALOG — a different question, so a different address.
	zip.Get(a, productPrefix+"/metrics", handleMetrics)
	// The org's trace LIST (traces.go), at the BARE collection address — the one
	// address in the trace family the module leaves open. It declares the DETAIL
	// (/traces/{traceId}), the field catalog and three per-trace projections, and
	// every one of them needs an id this read is where you get. Claiming the
	// collection and nothing under it is what keeps that a composition.
	zip.Get(a, o11yPrefix+"/traces", handleTraces)
	// Flat, org-gated LLM-obs sessions list (sessions.go): pins the runtime's
	// /api/sessions route and refuses an org-less caller at the cloud boundary.
	// Raw: it relays the runtime's own envelope byte-for-byte, and a relay has
	// no Go shape to declare.
	a.Get("/v1/o11y/sessions", sessionsHandler)
}

// ---- the typed reads (the published contract) ----

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

// GetO11yProductMetrics returns one product's RED series — request rate, errors, p50
// and p95 latency — for the caller's org, plus that org's LLM usage rollup over
// the same window. The series come from org-tagged request spans, so a tenant
// only ever aggregates its own traffic; a validated platform SuperAdmin sees the
// whole product's RED, while usage stays the caller's own org either way. A
// well-formed product with no backing workload answers empty series; a malformed
// slug is a 400.
//
// Example: {"product": "kms", "range": 3600}
func handleMetrics(ctx context.Context, in *metricsIn) (*metricsResponse, error) {
	org, err := principal.Acting(ctx)
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
