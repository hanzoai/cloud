package o11y

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/zap-proto/zip"
)

// vmproxy.go — the SuperAdmin-only VictoriaMetrics read proxy that backs the console's
// platform-infrastructure-health board (the /metrics and /status pages, gated by
// useIsSuperAdmin → the PlatformInfraHealth view).
//
// Why it exists: the console ships as a static export go:embedded into the cloud binary
// (output:'export'), which STRIPS every Next.js server route — so the console's former
// same-origin telemetry proxy (`app/telemetry/[...path]/route.ts`) is gone and a browser
// call to `/telemetry/api/v1/query` 404s (task #71, the last console error). This surface
// serves the SAME three admin queries the board issues, same-origin, over the cloud `/v1`
// API instead:
//
//	GET /v1/o11y/vm/api/v1/query?query=up
//	GET /v1/o11y/vm/api/v1/query_range?query=sum(up)&start&end&step
//	GET /v1/o11y/vm/api/v1/query_range?query=count(up)&start&end&step
//
// Security (mirrors scope.go's contract — "the client never supplies a raw query or a
// PromQL/SQL fragment"), fail-closed at every step:
//
//  1. SuperAdmin-only — admin(c) (X-User-IsAdmin, the reserved admin org). A non-admin
//     caller (every customer) is 403; the board is role-gated in the UI too, so a customer
//     never even reaches here. This is NOT tenant data — it is the whole platform's `up{}`
//     inventory, so it takes the platform-sudo scope, the same predicate the infra-log
//     god-view and whole-product metrics view use.
//  2. NO raw passthrough — the ?query param is ALLOWLISTED to the EXACT three the board
//     sends ({up, sum(up), count(up)}); anything else is a 400. So this can never become a
//     generic PromQL exfiltration/DoS endpoint. Range args (start/end/step) are validated
//     as positive integers before they reach VM.
//  3. Verbatim envelope — VM's native Prometheus JSON is returned UNCHANGED so the
//     console's parseInstant/parseRange work as-is. VM has no per-request auth (it is an
//     internal ClusterIP), so THIS handler is the access boundary.
//
// Registered in scope.go's init (order 69) BEFORE the o11y wildcard (order 70), so the
// specific route wins Fiber's in-order match — exactly like /v1/o11y/{logs,metrics,status}.

// vmProxyQueries is the EXACT allowlist of PromQL the infra-health board issues: the
// instant `up` inventory and the two range trends `sum(up)` / `count(up)`. The proxy
// admits ONLY these — anything else is a 400 — so the client picks from a fixed,
// server-owned set and never supplies a raw query (scope.go's contract).
var vmProxyQueries = map[string]struct{}{
	"up":        {},
	"sum(up)":   {},
	"count(up)": {},
}

// handleVMQuery proxies the board's instant query: GET /v1/o11y/vm/api/v1/query?query=up.
func handleVMQuery(c *zip.Ctx) error { return vmProxy(c, "api/v1/query", false) }

// handleVMQueryRange proxies the board's range queries:
// GET /v1/o11y/vm/api/v1/query_range?query=sum(up)|count(up)&start&end&step.
func handleVMQueryRange(c *zip.Ctx) error { return vmProxy(c, "api/v1/query_range", true) }

// vmProxy is the shared SuperAdmin VM read: gate → allowlist the query (and, for a range,
// validate start/end/step as positive integers) → forward to VM → return the raw
// Prometheus envelope verbatim. VM has no per-request auth (internal ClusterIP), so THIS
// is the access boundary — fail-closed at every step.
func vmProxy(c *zip.Ctx, apiPath string, isRange bool) error {
	if !admin(c) {
		return zip.ErrForbidden("platform infrastructure health is restricted to platform administrators")
	}
	query := strings.TrimSpace(c.Query("query"))
	if _, ok := vmProxyQueries[query]; !ok {
		return zip.ErrBadRequest("query not permitted")
	}
	params := url.Values{}
	params.Set("query", query)
	if isRange {
		start, ok1 := vmRangeArg(c.Query("start"))
		end, ok2 := vmRangeArg(c.Query("end"))
		step, ok3 := vmRangeArg(c.Query("step"))
		if !ok1 || !ok2 || !ok3 {
			return zip.ErrBadRequest("start, end and step must be positive integers")
		}
		params.Set("start", start)
		params.Set("end", end)
		params.Set("step", step)
	}
	ctx, cancel := context.WithTimeout(c.Context(), vmQueryTimeout)
	defer cancel()
	status, body, err := newVMClient().queryRaw(ctx, apiPath, params)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "o11y vm: %v", err)
	}
	c.SetHeader("Content-Type", "application/json")
	return c.Bytes(status, body)
}

// vmRangeArg validates a VM range arg (start/end unix seconds, step seconds) as a
// positive integer and returns its canonical string form. Anything non-numeric or
// non-positive is rejected at the boundary, so only well-formed range args reach VM.
func vmRangeArg(raw string) (string, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || n <= 0 {
		return "", false
	}
	return strconv.FormatInt(n, 10), true
}
