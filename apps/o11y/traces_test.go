package o11y

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// tracesApp mounts the trace list the way mountScope does — on the ROOT app, at
// its full path — with cloud.Bridge in front, standing in for the composer. So
// these tests read the registration the document and the MCP tool are generated
// from rather than a reconstruction of it.
func tracesApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	zip.Get(app, o11yPrefix+"/traces", handleTraces)
	return app
}

// ── the tenant pin, on the wire ───────────────────────────────────────────────

// TestTraceListRefusesACallerWithNoTenant is the cross-tenant gate. A trace list
// is the caller's own records one row at a time, so the org is resolved from the
// validated principal and the read does not start without one. Both ways to
// arrive without a tenant are refused, and the third case is the control: a
// validated caller gets PAST the gate and is stopped by the missing warehouse
// instead, which is what proves the 403s above are the gate and not the wiring.
func TestTraceListRefusesACallerWithNoTenant(t *testing.T) {
	const path = "/v1/o11y/traces"

	ask := func(t *testing.T, app *zip.App, hdr map[string]string) (int, string) {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("Test GET %s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		b := make([]byte, 512)
		n, _ := resp.Body.Read(b)
		return resp.StatusCode, string(b[:n])
	}

	// No Bridge in front — o11y as its own binary, and MCP's tools/call, which
	// invokes an op directly so no route middleware runs. The tenant reaches the
	// handler anyway: principal.OrgFrom falls back to zip's caller, which crosses
	// every path. So a caller the host already validated is SERVED, landing on the
	// same missing warehouse as the control below rather than on the gate. This
	// wanted 403, which was a total outage of the surface wherever the Bridge is
	// not in front — not a tenant check doing its job.
	//
	// The tenant is not weakened: SanitizeIdentity deletes every client-sent
	// authority header at cloud's boundary, so an X-Org-Id a cloud handler reads
	// was minted from the validated membership claim. The header a caller CAN set
	// for itself is the next case, and it is still refused.
	bare := zip.New(zip.Config{Logger: luxlog.New("test")})
	zip.Get(bare, o11yPrefix+"/traces", handleTraces)
	if code, body := ask(t, bare, map[string]string{"X-Org-Id": "acme", "X-User-Id": "u_acme"}); code == http.StatusForbidden {
		t.Fatalf("without cloud.Bridge: a validated caller was refused — the principal should ride zip's caller, got %d %s", code, body)
	}

	// Forged: X-Org-Id present, no validated user behind it. principal.OrgFrom
	// does not park an org for an unvalidated caller, so tenantOf refuses — this
	// is the header a bearer-less request could set for itself.
	if code, body := ask(t, tracesApp(t), map[string]string{"X-Org-Id": "victim"}); code != http.StatusForbidden {
		t.Fatalf("forged org (no X-User-Id): want 403, got %d %s", code, body)
	}

	// The control. A validated caller passes the tenant gate and is stopped by the
	// warehouse the test process has not got — 503, with this op's own sentence.
	// A 403 here would mean the gate refuses everyone and the two above prove
	// nothing about the tenant.
	code, body := ask(t, tracesApp(t), map[string]string{"X-Org-Id": "acme", "X-User-Id": "u_acme"})
	if code == http.StatusForbidden {
		t.Fatalf("a validated caller was refused: the gate is not measuring the tenant, %d %s", code, body)
	}
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "o11y traces: datastore not connected") {
		t.Fatalf("validated caller with no warehouse = %d %s, want 503 and this op's own refusal", code, body)
	}
}

// TestTraceListPinsTheOrgAsAParameter is the same gate one layer down, where the
// leak would actually happen: the org has to reach the statement as a BOUND
// argument and never as text, and it has to be the first predicate — an org
// spliced into SQL is a tenant boundary a quote mark can cross.
func TestTraceListPinsTheOrgAsAParameter(t *testing.T) {
	const evil = `acme' OR 1=1 --`
	sql, args := traceListSQL(traceListQuery{org: evil, rangeSec: 3600, limit: 50})

	if strings.Contains(sql, evil) || strings.Contains(sql, "acme") {
		t.Fatalf("the org is interpolated into the statement: %s", sql)
	}
	if !strings.Contains(sql, "WHERE org = ?") {
		t.Fatalf("the org is not the first bound predicate: %s", sql)
	}
	if len(args) == 0 || args[0] != any(evil) {
		t.Fatalf("args[0] = %#v, want the org bound first", args)
	}
	// Aggregated on read: the table holds one partial per write batch, so a query
	// without the fold reports a batch as a trace.
	for _, want := range []string{"min(start)", "max(`end`)", "sum(num_spans)", "GROUP BY trace_id"} {
		if !strings.Contains(sql, want) {
			t.Errorf("statement does not %s — an AggregatingMergeTree read that skips the fold "+
				"returns partials, not traces: %s", want, sql)
		}
	}
	if !strings.Contains(sql, "ORDER BY ended DESC") {
		t.Errorf("the page is not newest-first: %s", sql)
	}
}

// ── the clamps, at their bounds ───────────────────────────────────────────────

// TestTraceListClampsItsBounds pins both caller-supplied numbers at the edges.
// Neither is a suggestion: the limit is what stops one request from asking for
// the whole table, and the window is what keeps the scan inside a few partitions.
func TestTraceListClampsItsBounds(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, defaultTraceLimit},         // absent
		{-1, defaultTraceLimit},        // unparseable arrives as <= 0
		{1, 1},                         // the floor is honoured, not rounded up
		{defaultTraceLimit, 50},        // the default asked for explicitly
		{maxTraceLimit, maxTraceLimit}, // exactly the cap
		{maxTraceLimit + 1, maxTraceLimit},
		{1 << 30, maxTraceLimit}, // "give me everything"
	} {
		if got := boundTraceLimit(tc.in); got != tc.want {
			t.Errorf("boundTraceLimit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
	// The window is the package's one clamp, shared with the RED read, so this
	// pins that this op uses it rather than a second one of its own.
	for _, tc := range []struct{ in, want int }{
		{0, defaultRangeSec},
		{-1, defaultRangeSec},
		{1, 1},
		{maxRangeSec, maxRangeSec},
		{maxRangeSec + 1, maxRangeSec},
	} {
		if got := boundRangeSec(tc.in); got != tc.want {
			t.Errorf("boundRangeSec(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
	// And the clamped values are what reach the statement — a clamp the query
	// does not read is decoration.
	_, args := traceListSQL(traceListQuery{org: "acme", rangeSec: boundRangeSec(0), limit: boundTraceLimit(1 << 30)})
	if len(args) != 3 || args[1] != any(defaultRangeSec) || args[2] != any(uint64(maxTraceLimit)) {
		t.Fatalf("args = %#v, want [org, %d, uint64(%d)]", args, defaultRangeSec, maxTraceLimit)
	}
}

// TestTraceListDurationFloorIsOptional pins the one filter this table can answer
// exactly. It is post-aggregation because the duration is: a partial row carries
// part of a trace, so no single row knows how long it ran. Absent, it is not in
// the statement at all — an unfiltered read stays the simplest query.
func TestTraceListDurationFloorIsOptional(t *testing.T) {
	for _, in := range []int{0, -5} {
		sql, args := traceListSQL(traceListQuery{org: "acme", rangeSec: 3600, limit: 50, minDurationMs: in})
		if strings.Contains(sql, "HAVING") {
			t.Errorf("minDurationMs=%d added a filter: %s", in, sql)
		}
		if len(args) != 3 {
			t.Errorf("minDurationMs=%d bound %d args, want 3", in, len(args))
		}
	}
	sql, args := traceListSQL(traceListQuery{org: "acme", rangeSec: 3600, limit: 50, minDurationMs: 250})
	if !strings.Contains(sql, "HAVING") {
		t.Fatalf("minDurationMs=250 did not filter: %s", sql)
	}
	if i := strings.Index(sql, "HAVING"); i < strings.Index(sql, "GROUP BY") {
		t.Fatalf("the floor is applied before the fold: %s", sql)
	}
	// Bound, and bound BEFORE the limit — positional arguments are the contract.
	if len(args) != 4 || args[2] != any(int64(250)) || args[3] != any(uint64(50)) {
		t.Fatalf("args = %#v, want [org, range, int64(250), uint64(50)]", args)
	}
}

// ── the collection, and nothing under it ──────────────────────────────────────

// TestTraceListClaimsTheCollectionAndNotTheDetail is the compose gate. The bare
// address was a 404 while hanzoai/o11y served everything beneath it, so this
// route exists to fill exactly one hole. Declaring anything under /traces/ would
// be a second declaration at an address the module already owns — the failure
// scope.go's header is written about, and one that takes the whole mount down
// rather than answering wrongly. A sentinel stands in for the module here.
func TestTraceListClaimsTheCollectionAndNotTheDetail(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	zip.Get(app, o11yPrefix+"/traces", handleTraces)
	app.All("/v1/o11y/*", func(c *zip.Ctx) error { return c.String(599, "FELL-THROUGH") })

	fellThrough := func(t *testing.T, path string) bool {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-Org-Id", "acme")
		req.Header.Set("X-User-Id", "u_acme")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("Test GET %s: %v", path, err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode == 599
	}

	if fellThrough(t, "/v1/o11y/traces") {
		t.Error("/v1/o11y/traces fell through — the collection is the one address this op owns")
	}
	// Everything below it belongs to the module: the detail read, its field
	// catalog, and the per-trace projections. Answering at any of them here means
	// a route was added that the real binary would refuse to compose with.
	for _, path := range []string{
		"/v1/o11y/traces/4bf92f3577b34da6a3ce929d0e0e4736",
		"/v1/o11y/traces/fields",
	} {
		if !fellThrough(t, path) {
			t.Errorf("%s is answered by the trace LIST — that address is hanzoai/o11y's "+
				"(SearchTraces / traceFields), and declaring it again refuses to compose", path)
		}
	}
}

// TestTraceFamilyComposesOnTheRealMount is the same fact measured on the router
// the document is generated from, where a second declaration would have PANICKED
// the mount rather than answered wrongly. Both halves have to be present at once:
// the collection typed and ours, the detail and its projections still the
// module's. Reading the composed document rather than a reconstruction is what
// makes this evidence — the hole this route fills was a 404 at exactly one
// address while every address under it answered.
func TestTraceFamilyComposesOnTheRealMount(t *testing.T) {
	served, typed := o11yOps(t)

	const list = "GET /v1/o11y/traces"
	if !served[list] {
		t.Fatalf("%s is not served by the composed router — the collection is still a 404", list)
	}
	if _, ok := typed[list]; !ok {
		t.Errorf("%s carries no typed registry entry, so it has no schema, no MCP tool, "+
			"no CLI command and no SDK method", list)
	}
	// The module's, beside it and untouched. A missing one means this declaration
	// suppressed a read that was already working.
	for _, addr := range []string{
		"GET /v1/o11y/traces/{traceId}",
		"GET /v1/o11y/traces/fields",
		"POST /v1/o11y/traces/fields",
		"POST /v1/o11y/traces/{traceId}/waterfall",
		"POST /v1/o11y/traces/{traceId}/aggregations",
		"POST /v1/o11y/traces/{traceId}/flamegraph",
	} {
		if !served[addr] {
			t.Errorf("%s is no longer served — the trace LIST displaced hanzoai/o11y's own read", addr)
		}
	}
}
