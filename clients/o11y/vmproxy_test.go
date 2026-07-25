package o11y

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// vmproxy_test.go — the SuperAdmin VM read proxy's ALLOWLIST + gate contract. This
// extends the o11y allowlist tests (scope_test.go covers the tenant-scoped reads;
// this covers the platform-sudo VM proxy). The security invariant under test: the
// proxy admits ONLY the server-owned fixed query set (never a raw/injected PromQL),
// is SuperAdmin-only, and validates range args as positive integers — fail-closed at
// every step (vmproxy.go's contract).

// vmApp builds the SuperAdmin VM read surface exactly as mountScope registers it, so
// the tests exercise the real handlers + allowlist.
func vmApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Get("/v1/o11y/vm/query", handleVMQuery)
	app.Get("/v1/o11y/vm/query_range", handleVMQueryRange)
	return app
}

// vmAdminReq carries the validated-SuperAdmin gateway claim (X-User-IsAdmin: true —
// what SanitizeIdentity sets ONLY for the reserved admin org; a client can't forge it
// past the gateway, but a bare unit app reads it straight, like scope_test's X-User-Id).
func vmAdminReq(method, path string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-User-IsAdmin", "true")
	return req
}

// theAllowlist is the EXACT set the proxy must admit — the 3 platform-health queries
// plus the 9 Lux Network board queries. This is the byte-for-byte contract the console
// (LUX_QUERIES in lib/api/lux-infra.ts) depends on: the board sends these strings and
// nothing else, so a drift on EITHER side is a caught regression.
var theAllowlist = []string{
	// Platform health.
	"up",
	"sum(up)",
	"count(up)",
	// Lux Network — node/pod resource.
	`100*(1 - sum by (node)(node_memory_MemAvailable_bytes{cluster="lux-k8s"}) / sum by (node)(node_memory_MemTotal_bytes{cluster="lux-k8s"}))`,
	`topk(12, sum by (namespace,pod)(container_memory_working_set_bytes{cluster="lux-k8s",pod!=""}))`,
	// Lux Network — named-service available/desired replicas.
	`kube_deployment_status_replicas_available{cluster="lux-k8s",deployment=~"lux-admin|lux-safe|lux-safe-cgw|lux-bitcoin|lux-coin|lux-finance|lux-invest|lux-market|lux-industries|lux-blog|iam|bootnode-web|bridge-server|bridge-ui|explorer"}`,
	`kube_deployment_spec_replicas{cluster="lux-k8s",deployment=~"lux-admin|lux-safe|lux-safe-cgw|lux-bitcoin|lux-coin|lux-finance|lux-invest|lux-market|lux-industries|lux-blog|iam|bootnode-web|bridge-server|bridge-ui|explorer"}`,
	`kube_statefulset_status_replicas_ready{cluster="lux-k8s",namespace="lux-kms-go",statefulset="kms"}`,
	`kube_statefulset_replicas{cluster="lux-k8s",namespace="lux-kms-go",statefulset="kms"}`,
	// Lux Network — validators.
	"lux_validator_up",
	"lux_validator_c_block_height",
	"lux_validator_peers",
	"lux_validator_bootstrapped",
	"lux_network_validators_up",
	"lux_network_validators_total",
}

// ── the allowlist SET is EXACTLY the declared contract (no more, no less) ───────

func TestVMProxyAllowlistIsExact(t *testing.T) {
	if len(vmProxyQueries) != len(theAllowlist) {
		t.Fatalf("allowlist size drift: map has %d, contract has %d", len(vmProxyQueries), len(theAllowlist))
	}
	for _, q := range theAllowlist {
		if _, ok := vmProxyQueries[q]; !ok {
			t.Fatalf("allowlist is missing the contract query %q", q)
		}
	}
	// And nothing UNDECLARED slipped into the map (the reverse inclusion).
	declared := make(map[string]struct{}, len(theAllowlist))
	for _, q := range theAllowlist {
		declared[q] = struct{}{}
	}
	for q := range vmProxyQueries {
		if _, ok := declared[q]; !ok {
			t.Fatalf("allowlist has an UNDECLARED query %q — the server-owned set must match the contract exactly", q)
		}
	}
}

// ── the gate fails closed on a non-SuperAdmin (every VM read is platform-sudo) ──

func TestVMProxyRequiresSuperAdmin(t *testing.T) {
	app := vmApp(t)
	for _, path := range []string{
		"/v1/o11y/vm/query?query=up",
		"/v1/o11y/vm/query_range?query=sum(up)&start=1&end=2&step=15",
	} {
		// No X-User-IsAdmin → not a validated SuperAdmin → 403 (before the allowlist).
		req := httptest.NewRequest("GET", path, nil)
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("Test %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: want 403 for non-admin, got %d", path, resp.StatusCode)
		}
	}
}

// ── NO raw passthrough: anything off the allowlist is a 400 (the exfil boundary) ─

func TestVMProxyRejectsRawQuery(t *testing.T) {
	app := vmApp(t)
	// Real injection/exfil attempts + near-misses of an allowlisted query. Every one
	// must be a 400 "query not permitted" — the proxy is not a generic PromQL endpoint.
	for _, bad := range []string{
		"",                                   // empty
		"node_load1",                         // a valid but UNLISTED metric
		"container_memory_working_set_bytes", // the raw series behind an allowlisted rollup
		`up{job="iam-health"}`,               // a filtered variant of an allowlisted name
		"up or vector(1)",                    // a compound-query exfil attempt
		"lux_validator_c_finalized_height",   // a real lux_* metric that is NOT on the list
		"sum by (node) (node_memory_MemTotal_bytes)",                                                          // an extra space vs the exact allowlisted string
		"topk(24, sum by (namespace,pod)(container_memory_working_set_bytes{cluster=\"lux-k8s\",pod!=\"\"}))", // a different k than the fixed topk(12,…)
	} {
		req := vmAdminReq("GET", "/v1/o11y/vm/query?query="+url.QueryEscape(bad))
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("Test %q: %v", bad, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("query=%q: want 400 (not permitted), got %d", bad, resp.StatusCode)
		}
	}
}

// ── every allowlisted query is ADMITTED past the allowlist gate ─────────────────
//
// With no O11Y_VM_URL wired in a unit run the forward fails at VM (502), but the point
// is that it gets PAST the allowlist (never a 400 "not permitted" and never a 403). So
// "admitted" ⟺ status is neither 400 nor 403 — robust whether or not a VM is reachable.

func TestVMProxyAdmitsEveryAllowlistedQuery(t *testing.T) {
	app := vmApp(t)
	for _, q := range theAllowlist {
		req := vmAdminReq("GET", "/v1/o11y/vm/query?query="+url.QueryEscape(q))
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("Test %q: %v", q, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusForbidden {
			t.Fatalf("allowlisted query %q was rejected (status %d) — it must pass the gate", q, resp.StatusCode)
		}
	}
}

// ── range args are validated as positive integers, AFTER the allowlist ──────────

func TestVMProxyRangeArgValidation(t *testing.T) {
	app := vmApp(t)
	// An allowlisted range query with malformed args → 400 (bad range args), even for
	// an admin. The allowlist gate runs first, so this proves the SECOND gate.
	for _, bad := range []string{
		"/v1/o11y/vm/query_range?query=sum(up)",                        // missing all
		"/v1/o11y/vm/query_range?query=sum(up)&start=0&end=2&step=15",  // start not positive
		"/v1/o11y/vm/query_range?query=sum(up)&start=1&end=-2&step=15", // end negative
		"/v1/o11y/vm/query_range?query=sum(up)&start=1&end=2&step=abc", // step non-numeric
	} {
		req := vmAdminReq("GET", bad)
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("Test %s: %v", bad, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: want 400 for bad range args, got %d", bad, resp.StatusCode)
		}
	}
	// Valid args on an allowlisted range query pass BOTH gates (→ forwarded; 502 with no
	// VM wired, never a 400).
	req := vmAdminReq("GET", "/v1/o11y/vm/query_range?query=sum(up)&start=1&end=2&step=15")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test valid range: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusBadRequest {
		t.Fatalf("valid range args must pass, got 400")
	}
}
