package eval

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ── pure assemblers (no datastore) ───────────────────────────────────────────

func TestAssembleTotals(t *testing.T) {
	tot := assembleTotals(map[string]any{
		"generations": int64(100), "prompt_tokens": uint64(500),
		"completion_tokens": uint64(300), "total_tokens": uint64(800),
		"cost_cents": uint64(1234), "errors": int64(10),
		"models": uint64(4), "users": uint64(7),
	})
	if tot.Generations != 100 || tot.TotalTokens != 800 || tot.CostCents != 1234 {
		t.Fatalf("totals = %+v", tot)
	}
	if tot.PromptTokens != 500 || tot.CompletionTokens != 300 {
		t.Fatalf("token split = %d/%d", tot.PromptTokens, tot.CompletionTokens)
	}
	if tot.SuccessRate != 0.9 { // (100-10)/100
		t.Fatalf("successRate = %v, want 0.9", tot.SuccessRate)
	}
	if tot.Models != 4 || tot.Users != 7 {
		t.Fatalf("cardinality = %d/%d", tot.Models, tot.Users)
	}
}

func TestAssembleTotalsEmpty(t *testing.T) {
	tot := assembleTotals(map[string]any{}) // absent columns → zero, no divide-by-zero
	if tot.Generations != 0 || tot.SuccessRate != 0 {
		t.Fatalf("empty totals = %+v", tot)
	}
}

func TestAssembleSeriesGapFill(t *testing.T) {
	since := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	until := since.Add(3 * time.Hour)
	rows := []map[string]any{ // only the middle bucket has data
		{"bucket": since.Add(time.Hour), "generations": int64(5), "cost_cents": uint64(10), "total_tokens": uint64(50), "errors": int64(1)},
	}
	pts := assembleSeries(since, until, time.Hour, rows)
	if len(pts) != 3 {
		t.Fatalf("want 3 gap-filled buckets, got %d", len(pts))
	}
	if pts[0].Generations != 0 || pts[2].Generations != 0 {
		t.Fatalf("gaps not zero-filled: %+v", pts)
	}
	if pts[1].Generations != 5 || pts[1].CostCents != 10 || pts[1].TotalTokens != 50 || pts[1].Errors != 1 {
		t.Fatalf("data bucket = %+v", pts[1])
	}
	if pts[0].T != "2026-07-01T00:00:00Z" {
		t.Fatalf("bucket 0 T = %q", pts[0].T)
	}
}

func TestAssembleByModelFoldAndLatency(t *testing.T) {
	rows := []map[string]any{ // cost desc, as SQL returns
		{"model": "zen5", "provider": "do", "requests": int64(10), "prompt_tokens": uint64(100), "completion_tokens": uint64(40), "total_tokens": uint64(140), "cost_cents": uint64(80), "errors": int64(2)},
		{"model": "gpt", "provider": "openai", "requests": int64(6), "prompt_tokens": uint64(60), "completion_tokens": uint64(20), "total_tokens": uint64(80), "cost_cents": uint64(15), "errors": int64(0)},
		{"model": "haiku", "provider": "anthropic", "requests": int64(3), "prompt_tokens": uint64(30), "completion_tokens": uint64(10), "total_tokens": uint64(40), "cost_cents": uint64(5), "errors": int64(1)},
	}
	p95 := 123.0
	lat := map[string]latPercentiles{"zen5": {p50: fptr(50), p95: &p95, p99: fptr(200)}}
	head, other := assembleByModel(rows, lat, 2, 100)

	if len(head) != 2 {
		t.Fatalf("want 2 head rows, got %d", len(head))
	}
	if head[0].Model != "zen5" || head[0].ErrorRate != 0.2 || head[0].CostPct != 80 {
		t.Fatalf("head[0] = %+v", head[0])
	}
	if head[0].P95Ms == nil || *head[0].P95Ms != 123 {
		t.Fatalf("head[0] latency not merged: %v", head[0].P95Ms)
	}
	if head[1].P95Ms != nil {
		t.Fatalf("gpt has no latency, want nil, got %v", *head[1].P95Ms)
	}
	if other == nil {
		t.Fatal("want an 'other' fold for the 3rd model")
	}
	if other.ModelCount != 1 || other.Requests != 3 || other.CostCents != 5 || other.TotalTokens != 40 {
		t.Fatalf("other = %+v", other)
	}
	if want := round4(1.0 / 3.0); other.ErrorRate != want {
		t.Fatalf("other errorRate = %v, want %v", other.ErrorRate, want)
	}
}

func TestAssembleByModelNoFold(t *testing.T) {
	rows := []map[string]any{{"model": "a", "requests": int64(1), "cost_cents": uint64(1)}}
	head, other := assembleByModel(rows, nil, 8, 1)
	if len(head) != 1 || other != nil {
		t.Fatalf("no-fold expected: head=%d other=%v", len(head), other)
	}
}

func TestNsToMsPtr(t *testing.T) {
	if p := nsToMsPtr(nil); p != nil {
		t.Fatalf("nil → %v", *p)
	}
	if p := nsToMsPtr(float64(0)); p != nil {
		t.Fatalf("0 → %v", *p)
	}
	if p := nsToMsPtr(float64(1_500_000)); p == nil || *p != 1.5 { // 1.5ms
		t.Fatalf("1.5ms → %v", p)
	}
}

func TestPct100(t *testing.T) {
	if got := pct100(25, 100); got != 25 {
		t.Fatalf("pct100(25,100) = %v", got)
	}
	if got := pct100(1, 0); got != 0 { // no divide-by-zero
		t.Fatalf("pct100(1,0) = %v", got)
	}
}

// ── where clause: org isolation vs SuperAdmin all-orgs ─────────────────────────

func TestUsageWhereOrgScoped(t *testing.T) {
	f := MetricsFilter{Org: "hanzo", Since: time.Unix(0, 0).UTC(), Until: time.Unix(3600, 0).UTC()}
	w, args := usageWhere(f)
	if !strings.Contains(w, "organization = ?") {
		t.Fatalf("non-admin where must pin org: %q", w)
	}
	if len(args) != 3 || args[2] != "hanzo" {
		t.Fatalf("args = %v (want since,until,hanzo)", args)
	}
}

func TestUsageWhereAllOrgs(t *testing.T) {
	f := MetricsFilter{Org: "hanzo", AllOrgs: true, Since: time.Unix(0, 0).UTC(), Until: time.Unix(3600, 0).UTC()}
	w, args := usageWhere(f)
	if strings.Contains(w, "organization") {
		t.Fatalf("all-orgs where must NOT pin org: %q", w)
	}
	if len(args) != 2 {
		t.Fatalf("want 2 args (since,until), got %d", len(args))
	}
}

// ── in-memory store: honest-empty + org isolation ─────────────────────────────

func TestMemTelemetryMetrics(t *testing.T) {
	m := newMemTelemetry()
	if _, err := m.Metrics(context.Background(), MetricsFilter{}); err == nil {
		t.Fatal("want error on missing org (tenant isolation), got nil")
	}
	b, err := m.Metrics(context.Background(), MetricsFilter{
		Org: "hanzo", Interval: "hour",
		Since: time.Unix(0, 0).UTC(), Until: time.Unix(7200, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if b.Scope.Org != "hanzo" || len(b.ByModel) != 0 || b.Latency.Available {
		t.Fatalf("mem board not honest-empty: %+v", b)
	}
	if len(b.Series) != 2 { // two hourly buckets gap-filled
		t.Fatalf("want 2 gap-filled buckets, got %d", len(b.Series))
	}
}

// ── HTTP handler over the in-memory store ─────────────────────────────────────

func TestMetricsHandlerHonestEmpty(t *testing.T) {
	app, _ := mountApp(t)
	code, body := do(t, app, "GET", "/v1/evals/metrics?range=24h", "hanzo", nil)
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	var b Board
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, body)
	}
	if b.Scope.Org != "hanzo" {
		t.Fatalf("scope.org = %q", b.Scope.Org)
	}
	if b.Totals.Generations != 0 {
		t.Fatalf("want zero generations on empty store, got %d", b.Totals.Generations)
	}
	if b.ByModel == nil || len(b.ByModel) != 0 {
		t.Fatalf("byModel must be [] (not null, not populated): %v", b.ByModel)
	}
	if b.Latency.Available {
		t.Fatal("latency must be unavailable on empty store")
	}
	if len(b.Series) == 0 {
		t.Fatal("24h series must be gap-filled, got empty")
	}
	for _, p := range b.Series {
		if p.Generations != 0 || p.CostCents != 0 {
			t.Fatalf("empty-store series must be all-zero: %+v", p)
		}
	}
	if b.Range.Range != "24h" || b.Range.Interval != "hour" {
		t.Fatalf("range = %+v", b.Range)
	}
}

func TestMetricsHandlerRequiresPrincipal(t *testing.T) {
	app, _ := mountApp(t)
	code, _ := do(t, app, "GET", "/v1/evals/metrics", "", nil) // no principal
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (no validated principal)", code)
	}
}

func TestMetricsHandlerNonDefaultProjectEmpty(t *testing.T) {
	app, _ := mountApp(t)
	code, body := do(t, app, "GET", "/v1/evals/metrics?range=24h&project=alpha", "hanzo", nil)
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	var b Board
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if b.Scope.Project != "alpha" {
		t.Fatalf("scope.project = %q, want alpha", b.Scope.Project)
	}
	if len(b.ByModel) != 0 || b.Totals.Generations != 0 {
		t.Fatal("non-default project must be honest-empty (no ledger attribution)")
	}
}

func fptr(f float64) *float64 { return &f }
