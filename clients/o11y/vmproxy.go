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
// API instead. The public path is FLAT and version-less (one /v1/, no nested /api/vN);
// the upstream VictoriaMetrics `api/v1/*` path stays INSIDE the handler (via queryRaw),
// never in our route:
//
//	GET /v1/o11y/vm/query?query=up
//	GET /v1/o11y/vm/query_range?query=sum(up)&start&end&step
//	GET /v1/o11y/vm/query_range?query=count(up)&start&end&step
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
// Registered by mountScope (scope.go) inside the one order-69 `o11y` mount — BEFORE the
// o11y wildcard (order 70) — so the specific route wins Fiber's in-order match, exactly
// like /v1/o11y/{logs,metrics,status}.

// vmProxyQueries is the EXACT allowlist of PromQL the infra-health boards issue. The
// proxy admits ONLY these — anything else is a 400 — so the client picks from a fixed,
// server-owned set and never supplies a raw query (scope.go's contract). Two groups:
//
//   - Platform health (the generic Hanzo infra board): the instant `up` inventory and
//     the two range trends `sum(up)` / `count(up)`.
//   - Lux Network (the console.lux.cloud SuperAdmin investor board, LuxNetworkModule):
//     a FIXED, parameter-free set over the lux-k8s series federated into this hub —
//     per-node memory %, top-pod memory, named-service available/desired replicas
//     (Deployments + the kms StatefulSet; the console dedupes multi-namespace workloads
//     client-side), and the luxd validator series (per-instance up/height/peers/
//     bootstrapped + the per-network up/total rollup). Every selector is pinned to
//     `cluster="lux-k8s"` (or the intrinsically Lux `lux_*` names), so this can never
//     read another cluster's series. These are the byte-for-byte twins of the console's
//     `LUX_QUERIES` (clients/o11y ⇄ lib/api/lux-infra.ts) — the query string the board
//     sends must equal a key here after TrimSpace, or it is rejected.
var vmProxyQueries = map[string]struct{}{
	// Platform health board.
	"up":        {},
	"sum(up)":   {},
	"count(up)": {},
	// Lux Network board — node/pod resource pressure (the OOM view + top consumers).
	`100*(1 - sum by (node)(node_memory_MemAvailable_bytes{cluster="lux-k8s"}) / sum by (node)(node_memory_MemTotal_bytes{cluster="lux-k8s"}))`: {},
	`topk(12, sum by (namespace,pod)(container_memory_working_set_bytes{cluster="lux-k8s",pod!=""}))`:                                           {},
	// Lux Network board — named-service AVAILABLE/DESIRED replicas. Deployments (the
	// regex is fully anchored in PromQL, so lux-safe never over-matches lux-safe-cgw;
	// the console dedupes multi-namespace workloads to a canonical namespace) + the kms
	// StatefulSet. This is the canonical available/desired signal the grid renders.
	`kube_deployment_status_replicas_available{cluster="lux-k8s",deployment=~"lux-admin|lux-safe|lux-safe-cgw|lux-bitcoin|lux-coin|lux-finance|lux-invest|lux-market|lux-industries|lux-blog|iam|bootnode-web|bridge-server|bridge-ui|explorer"}`: {},
	`kube_deployment_spec_replicas{cluster="lux-k8s",deployment=~"lux-admin|lux-safe|lux-safe-cgw|lux-bitcoin|lux-coin|lux-finance|lux-invest|lux-market|lux-industries|lux-blog|iam|bootnode-web|bridge-server|bridge-ui|explorer"}`:             {},
	`kube_statefulset_status_replicas_ready{cluster="lux-k8s",namespace="lux-kms-go",statefulset="kms"}`:                                                                                                                                          {},
	`kube_statefulset_replicas{cluster="lux-k8s",namespace="lux-kms-go",statefulset="kms"}`:                                                                                                                                                       {},
	// Lux Network board — validators. Per-instance liveness (all networks; the console
	// groups by the `network` label) + the per-network rollup.
	"lux_validator_up":             {},
	"lux_validator_c_block_height": {},
	"lux_validator_peers":          {},
	"lux_validator_bootstrapped":   {},
	"lux_network_validators_up":    {},
	"lux_network_validators_total": {},
}

// handleVMQuery proxies the board's instant query: GET /v1/o11y/vm/query?query=up.
// The flat public path resolves to VictoriaMetrics' `api/v1/query` INSIDE the handler.
func handleVMQuery(c *zip.Ctx) error { return vmProxy(c, "api/v1/query", false) }

// handleVMQueryRange proxies the board's range queries:
// GET /v1/o11y/vm/query_range?query=sum(up)|count(up)&start&end&step. The flat public
// path resolves to VictoriaMetrics' `api/v1/query_range` INSIDE the handler.
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
