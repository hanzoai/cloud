// Copyright © 2026 Hanzo AI. MIT License.

package analytics

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/spec"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// ── Test doubles ─────────────────────────────────────────────────────────────

type recordedQuery struct {
	SQL  string
	Args []any
}

type fakeBackend struct {
	mu      sync.Mutex
	ready   bool
	queries []recordedQuery
	handler func(ctx context.Context, sql string, args ...any) ([]map[string]any, error)
}

func newFakeBackend(fn func(ctx context.Context, sql string, args ...any) ([]map[string]any, error)) *fakeBackend {
	return &fakeBackend{ready: true, handler: fn}
}

func (f *fakeBackend) SetReady(r bool) {
	f.mu.Lock()
	f.ready = r
	f.mu.Unlock()
}

func (f *fakeBackend) Ready() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ready
}

func (f *fakeBackend) Query(ctx context.Context, sql string, args ...any) ([]map[string]any, error) {
	f.mu.Lock()
	ready := f.ready
	f.queries = append(f.queries, recordedQuery{SQL: sql, Args: append([]any(nil), args...)})
	fn := f.handler
	f.mu.Unlock()
	if !ready {
		return nil, ErrDatastoreUnavailable
	}
	if fn != nil {
		return fn(ctx, sql, args...)
	}
	return nil, nil
}

func (f *fakeBackend) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queries)
}

func (f *fakeBackend) Last() recordedQuery {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queries) == 0 {
		return recordedQuery{}
	}
	return f.queries[len(f.queries)-1]
}

type memKVEntry struct {
	val     []byte
	expires time.Time
}

type memKV struct {
	mu   sync.Mutex
	data map[string]memKVEntry
	down bool
}

func newMemKV() *memKV {
	return &memKV{data: make(map[string]memKVEntry)}
}

func (m *memKV) SetDown(down bool) {
	m.mu.Lock()
	m.down = down
	m.mu.Unlock()
}

func (m *memKV) ExpireFreshKeys() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.data {
		if !strings.HasPrefix(k, cacheStalePrefix) {
			delete(m.data, k)
		}
	}
}

func (m *memKV) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down {
		return nil, errors.New("kv connection refused")
	}
	e, ok := m.data[key]
	if !ok {
		return nil, errors.New("kv: nil")
	}
	if !e.expires.IsZero() && time.Now().After(e.expires) {
		delete(m.data, key)
		return nil, errors.New("kv: expired")
	}
	cp := make([]byte, len(e.val))
	copy(cp, e.val)
	return cp, nil
}

func (m *memKV) Set(_ context.Context, key string, val []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down {
		return errors.New("kv connection refused")
	}
	cp := make([]byte, len(val))
	copy(cp, val)
	var exp time.Time
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	m.data[key] = memKVEntry{val: cp, expires: exp}
	return nil
}

func (m *memKV) Ping(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down {
		return errors.New("kv connection refused")
	}
	return nil
}

// newTestApp mounts Service behind cloud.Bridge so zip typed ops see caller headers
// exactly as production requests do after SanitizeIdentity.
func newTestApp(t *testing.T, cfg Config) (*zip.App, *Service) {
	t.Helper()
	app := zip.New(zip.Config{
		Logger:                luxlog.New("analytics-test"),
		DisableStartupMessage: true,
		OpenAPI:               zip.OpenAPIConfig{Title: "analytics", Version: "v1"},
	})
	app.Use(cloud.Bridge())
	svc := NewService(cfg)
	svc.Mount(app)
	return app, svc
}

func doReq(t *testing.T, app *zip.App, method, path, body string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 5 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, raw
}

var (
	hdrAnon       = map[string]string{}
	hdrSuper      = map[string]string{"X-User-Id": "admin/z", "X-Org-Id": "admin", "X-User-IsAdmin": "true"}
	hdrAcmeMember = map[string]string{"X-User-Id": "acme/alice", "X-Org-Id": "acme"}
	hdrAcmeAdmin  = map[string]string{"X-User-Id": "acme/bob", "X-Org-Id": "acme", "X-User-IsOrgAdmin": "true"}
	hdrNoOrg      = map[string]string{"X-User-Id": "orphan/user"}
)

// ── 1. Metric Catalog Discovery ──────────────────────────────────────────────

func TestMetricCatalogDiscovery(t *testing.T) {
	app, _ := newTestApp(t, Config{Backend: newFakeBackend(nil)})

	// Unauthenticated must fail closed with 401.
	resp, _ := doReq(t, app, "GET", "/v1/analytics/metrics", "", hdrAnon)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anon /metrics status = %d, want 401", resp.StatusCode)
	}

	// Authenticated member can discover the entire catalog.
	resp, body := doReq(t, app, "GET", "/v1/analytics/metrics", "", hdrAcmeMember)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("member /metrics status = %d, body=%s", resp.StatusCode, body)
	}
	var out MetricsOut
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Status != statusOK || out.Data == nil || out.Data.Total < 20 {
		t.Fatalf("unexpected catalog: %+v", out)
	}

	wantRequired := []string{
		"commerce.revenue", "commerce.mrr", "commerce.arr", "commerce.cash_held",
		"commerce.credit_outstanding", "commerce.spend",
		"ai.requests", "ai.tokens", "ai.cost", "ai.billed", "ai.margin", "ai.cache_savings", "ai.error_rate",
		"iam.orgs", "iam.users", "iam.active_users",
		"o11y.requests", "o11y.errors", "o11y.p50_ms", "o11y.p95_ms", "o11y.p99_ms",
		"compute.samples", "product.events", "product.visitors", "product.sessions",
	}
	present := map[string]MetricDef{}
	for _, m := range out.Data.Metrics {
		present[m.ID] = m
	}
	for _, id := range wantRequired {
		m, ok := present[id]
		if !ok {
			t.Errorf("canonical metric %q missing from catalog", id)
			continue
		}
		if m.Unit == "" || m.Source == "" || m.Accuracy == "" || m.FreshnessTargetSec <= 0 || len(m.Tables) == 0 {
			t.Errorf("metric %q has incomplete metadata: %+v", id, m)
		}
	}

	// Domain filter returns only that domain.
	resp, body = doReq(t, app, "GET", "/v1/analytics/metrics?domain=ai", "", hdrSuper)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("domain filter status = %d", resp.StatusCode)
	}
	var aiOut MetricsOut
	if err := json.Unmarshal(body, &aiOut); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if aiOut.Data.Total == 0 {
		t.Fatal("expected ai metrics")
	}
	for _, m := range aiOut.Data.Metrics {
		if m.Domain != "ai" {
			t.Errorf("domain=ai returned %q from domain %q", m.ID, m.Domain)
		}
	}
}

// ── 2. Query Planner: Table Selection, Validation & SQL Injection Safety ─────

func TestQueryPlannerTableSelection(t *testing.T) {
	super := Scope{Super: true}
	fixedNow := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		req       QueryIn
		wantTable string
		wantGran  string
	}{
		{
			name:      "ai 24h auto -> hourly",
			req:       QueryIn{Metrics: []string{"ai.requests", "ai.tokens"}, Window: "24h"},
			wantTable: "analytics.rollup_ai_hourly",
			wantGran:  "hour",
		},
		{
			name:      "ai 7d auto -> hourly",
			req:       QueryIn{Metrics: []string{"ai.cost", "ai.margin"}, Window: "7d", GroupBy: []string{"model"}},
			wantTable: "analytics.rollup_ai_hourly",
			wantGran:  "hour",
		},
		{
			name:      "ai 30d auto -> daily",
			req:       QueryIn{Metrics: []string{"ai.tokens"}, Window: "30d"},
			wantTable: "analytics.rollup_ai_daily",
			wantGran:  "day",
		},
		{
			name:      "ai 90d auto -> daily",
			req:       QueryIn{Metrics: []string{"ai.billed"}, Window: "90d", GroupBy: []string{"provider"}},
			wantTable: "analytics.rollup_ai_daily",
			wantGran:  "day",
		},
		{
			name:      "ai 24h group_by day -> coarsest daily",
			req:       QueryIn{Metrics: []string{"ai.requests"}, Window: "24h", GroupBy: []string{"day"}},
			wantTable: "analytics.rollup_ai_daily",
			wantGran:  "day",
		},
		{
			name:      "commerce revenue 30d -> daily",
			req:       QueryIn{Metrics: []string{"commerce.revenue", "commerce.spend"}, Window: "30d"},
			wantTable: "analytics.rollup_commerce_daily",
			wantGran:  "day",
		},
		{
			name:      "o11y p99 24h -> hourly",
			req:       QueryIn{Metrics: []string{"o11y.p99_ms"}, Window: "24h", GroupBy: []string{"service"}},
			wantTable: "analytics.rollup_o11y_hourly",
			wantGran:  "hour",
		},
		{
			name:      "product visitors 7d -> hourly",
			req:       QueryIn{Metrics: []string{"product.events", "product.visitors"}, Window: "7d"},
			wantTable: "analytics.rollup_product_hourly",
			wantGran:  "hour",
		},
		{
			name:      "commerce mrr state -> state_subscriptions",
			req:       QueryIn{Metrics: []string{"commerce.mrr", "commerce.arr"}},
			wantTable: "analytics.state_subscriptions",
			wantGran:  "snapshot",
		},
		{
			name:      "commerce cash_held state -> state_org_balances",
			req:       QueryIn{Metrics: []string{"commerce.cash_held", "commerce.credit_outstanding"}},
			wantTable: "analytics.state_org_balances",
			wantGran:  "snapshot",
		},
		{
			name:      "iam orgs state -> dim_orgs",
			req:       QueryIn{Metrics: []string{"iam.orgs"}},
			wantTable: "analytics.dim_orgs",
			wantGran:  "snapshot",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := planAt(tc.req, super, fixedNow)
			if err != nil {
				t.Fatalf("planAt failed: %v", err)
			}
			if plan.Table != tc.wantTable {
				t.Errorf("table = %q, want %q", plan.Table, tc.wantTable)
			}
			if plan.Granularity != tc.wantGran {
				t.Errorf("granularity = %q, want %q", plan.Granularity, tc.wantGran)
			}
			if plan.Limit != DefaultLimit {
				t.Errorf("default limit = %d, want %d", plan.Limit, DefaultLimit)
			}
		})
	}
}

func TestQueryPlannerValidationAndSQLInjectionSafety(t *testing.T) {
	super := Scope{Super: true}

	rejectCases := []struct {
		name string
		req  QueryIn
	}{
		{name: "empty metrics", req: QueryIn{}},
		{name: "unregistered metric", req: QueryIn{Metrics: []string{"ai.nonexistent"}}},
		{name: "sql injection metric", req: QueryIn{Metrics: []string{"ai.tokens; DROP TABLE analytics.rollup_ai_daily--"}}},
		{name: "disallowed dimension", req: QueryIn{Metrics: []string{"commerce.revenue"}, GroupBy: []string{"model"}}},
		{name: "sql injection dimension", req: QueryIn{Metrics: []string{"ai.tokens"}, GroupBy: []string{"model; DROP TABLE x"}}},
		{name: "too many group_by (>2)", req: QueryIn{Metrics: []string{"ai.tokens"}, GroupBy: []string{"org", "model", "provider"}}},
		{name: "limit exceeds 500", req: QueryIn{Metrics: []string{"ai.tokens"}, Limit: 501}},
		{name: "negative limit", req: QueryIn{Metrics: []string{"ai.tokens"}, Limit: -1}},
		{name: "window exceeds 365d", req: QueryIn{Metrics: []string{"ai.tokens"}, Window: "366d"}},
		{name: "invalid window syntax", req: QueryIn{Metrics: []string{"ai.tokens"}, Window: "30m"}},
		{name: "incompatible table families", req: QueryIn{Metrics: []string{"ai.tokens", "o11y.p95_ms"}}},
		{name: "hourly over 90d rejected", req: QueryIn{Metrics: []string{"ai.tokens"}, Window: "90d", Granularity: "hour"}},
		{name: "invalid org rune for super", req: QueryIn{Metrics: []string{"ai.tokens"}, Org: "acme' OR '1'='1"}},
	}

	for _, tc := range rejectCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Plan(tc.req, super)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			var pe *PlanError
			if !errors.As(err, &pe) || pe.Code != 400 {
				t.Fatalf("expected 400 PlanError, got %v", err)
			}
		})
	}

	// Positional parameter safety: verify org and timestamps are NEVER interpolated into SQL.
	evilOrg := "acme'; DROP TABLE analytics.rollup_ai_daily; --"
	plan, err := Plan(QueryIn{Metrics: []string{"ai.tokens"}, Window: "7d", GroupBy: []string{"model"}}, Scope{Super: false, Org: evilOrg})
	if err != nil {
		t.Fatalf("plan failed: %v", err)
	}
	if strings.Contains(plan.SQL, evilOrg) || strings.Contains(plan.SQL, "DROP") || strings.Contains(plan.SQL, "acme") {
		t.Fatalf("user org value interpolated into SQL: %s", plan.SQL)
	}
	if strings.Count(plan.SQL, "?") != 3 || len(plan.Args) != 3 {
		t.Fatalf("expected 3 positional ? placeholders (since, until, org), got sql=%q args=%v", plan.SQL, plan.Args)
	}
	if plan.Args[2] != evilOrg {
		t.Fatalf("expected positional org arg %q, got %v", evilOrg, plan.Args[2])
	}
}

// ── 3. Strict IAM Tenant Isolation ───────────────────────────────────────────

func TestStrictTenantIsolation(t *testing.T) {
	backend := newFakeBackend(func(_ context.Context, sql string, args ...any) ([]map[string]any, error) {
		if strings.Contains(sql, "analytics.serving_global_overview") || strings.Contains(sql, "analytics.global_overview") {
			return []map[string]any{{"mrr_minor": int64(999999), "orgs_total": int64(250)}}, nil
		}
		if strings.Contains(sql, "analytics.serving_org_overview") || strings.Contains(sql, "analytics.org_overview") {
			return []map[string]any{{"mrr_minor": int64(12345), "members": int64(7)}}, nil
		}
		return []map[string]any{{"ai_tokens": float64(5000), "model": "zen-pro"}}, nil
	})
	app, _ := newTestApp(t, Config{Backend: backend})

	// 1. Unauthenticated caller -> 401 on every endpoint.
	for _, ep := range []struct{ method, path, body string }{
		{"GET", "/v1/analytics/metrics", ""},
		{"POST", "/v1/analytics/query", `{"metrics":["ai.tokens"]}`},
		{"GET", "/v1/analytics/overview", ""},
		{"GET", "/v1/analytics/health", ""},
		{"GET", "/v1/analytics/freshness", ""},
		{"GET", "/v1/analytics/stream", ""},
	} {
		resp, _ := doReq(t, app, ep.method, ep.path, ep.body, hdrAnon)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("anon %s %s = %d, want 401", ep.method, ep.path, resp.StatusCode)
		}
	}

	// 2. Authenticated user without org -> 403 fail closed.
	resp, _ := doReq(t, app, "POST", "/v1/analytics/query", `{"metrics":["ai.tokens"]}`, hdrNoOrg)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no-org user query = %d, want 403", resp.StatusCode)
	}

	// 3. Non-super caller CANNOT query another org or global scope -> 403.
	for _, body := range []string{
		`{"metrics":["ai.tokens"],"org":"globex"}`,
		`{"metrics":["ai.tokens"],"org":"global"}`,
		`{"metrics":["ai.tokens"],"org":"*"}`,
	} {
		resp, raw := doReq(t, app, "POST", "/v1/analytics/query", body, hdrAcmeAdmin)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("non-super cross-tenant query %s = %d (body=%s), want 403", body, resp.StatusCode, raw)
		}
	}
	resp, _ = doReq(t, app, "GET", "/v1/analytics/overview?org=globex", "", hdrAcmeMember)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-super cross-tenant overview = %d, want 403", resp.StatusCode)
	}
	resp, _ = doReq(t, app, "GET", "/v1/analytics/stream?org=globex", "", hdrAcmeMember)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-super cross-tenant stream = %d, want 403", resp.StatusCode)
	}
	resp, _ = doReq(t, app, "GET", "/v1/analytics/stream?scope=global", "", hdrAcmeAdmin)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-super global stream = %d, want 403", resp.StatusCode)
	}

	// 4. Non-super caller without org param is HARD-scoped to their own org ("acme").
	resp, raw := doReq(t, app, "POST", "/v1/analytics/query", `{"metrics":["ai.tokens"],"window":"7d"}`, hdrAcmeMember)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("member own-org query = %d (%s)", resp.StatusCode, raw)
	}
	var qOut QueryOut
	if err := json.Unmarshal(raw, &qOut); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if qOut.Data.Plan.Scope != "org:acme" {
		t.Fatalf("member scope = %q, want org:acme", qOut.Data.Plan.Scope)
	}
	last := backend.Last()
	if !strings.Contains(last.SQL, "org = ?") || len(last.Args) == 0 || last.Args[len(last.Args)-1] != "acme" {
		t.Fatalf("member SQL not pinned to org=acme: sql=%q args=%v", last.SQL, last.Args)
	}

	// 5. Non-super overview queries analytics.org_overview pinned to "acme" (0 per-org RPCs).
	resp, raw = doReq(t, app, "GET", "/v1/analytics/overview", "", hdrAcmeMember)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("member overview = %d (%s)", resp.StatusCode, raw)
	}
	var ovOut AnalyticsOverviewOut
	if err := json.Unmarshal(raw, &ovOut); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ovOut.Data.Scope != "org:acme" || ovOut.Data.KPIs.MRRCents != 12345 {
		t.Fatalf("unexpected member overview: %+v", ovOut.Data)
	}

	// 6. SuperAdmin CAN query global scope AND any specific org.
	resp, raw = doReq(t, app, "GET", "/v1/analytics/overview", "", hdrSuper)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("super global overview = %d (%s)", resp.StatusCode, raw)
	}
	var superOv AnalyticsOverviewOut
	_ = json.Unmarshal(raw, &superOv)
	if superOv.Data.Scope != "global" || superOv.Data.KPIs.MRRCents != 999999 || superOv.Data.KPIs.Orgs != 250 {
		t.Fatalf("unexpected super global overview: %+v", superOv.Data)
	}

	resp, raw = doReq(t, app, "POST", "/v1/analytics/query", `{"metrics":["ai.tokens"],"window":"30d","org":"globex"}`, hdrSuper)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("super org query = %d (%s)", resp.StatusCode, raw)
	}
	var superQ QueryOut
	_ = json.Unmarshal(raw, &superQ)
	if superQ.Data.Plan.Scope != "org:globex" || superQ.Data.Plan.Table != "analytics.rollup_ai_daily" {
		t.Fatalf("unexpected super org query plan: %+v", superQ.Data.Plan)
	}
}

// ── 4. Hanzo KV Acceleration, Cold KV Fallback & Stale Outage Degradation ────

func TestKVColdFallbackAndStaleOutageDegradation(t *testing.T) {
	var rev atomic.Int64
	rev.Store(420000)

	backend := newFakeBackend(func(_ context.Context, sql string, _ ...any) ([]map[string]any, error) {
		return []map[string]any{{"mrr_minor": rev.Load(), "cash_minor": int64(100000)}}, nil
	})
	kvStore := newMemKV()
	kvStore.SetDown(true) // Start with KV completely down!

	nowVal := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	var nowMu sync.Mutex
	nowFn := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return nowVal
	}

	app, _ := newTestApp(t, Config{
		Backend:  backend,
		KV:       kvStore,
		CacheTTL: 15 * time.Second,
		StaleTTL: 24 * time.Hour,
		Now:      nowFn,
	})

	// 1. KV is down -> must serve straight from Datastore (never fail!).
	resp, raw := doReq(t, app, "GET", "/v1/analytics/overview", "", hdrSuper)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cold/down KV overview = %d (%s)", resp.StatusCode, raw)
	}
	var out AnalyticsOverviewOut
	_ = json.Unmarshal(raw, &out)
	if out.Data.KPIs.MRRCents != 420000 || out.Data.Freshness.Stale {
		t.Fatalf("unexpected cold KV response: %+v", out.Data)
	}
	if backend.Count() != 1 {
		t.Fatalf("expected 1 datastore query, got %d", backend.Count())
	}

	// 2. Bring KV up and warm cache.
	kvStore.SetDown(false)
	rev.Store(550000)
	resp, raw = doReq(t, app, "GET", "/v1/analytics/overview", "", hdrSuper)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("warm KV overview = %d (%s)", resp.StatusCode, raw)
	}
	_ = json.Unmarshal(raw, &out)
	if out.Data.KPIs.MRRCents != 550000 || out.Data.Freshness.Stale {
		t.Fatalf("expected fresh 550000, got %+v", out.Data)
	}
	if backend.Count() != 2 {
		t.Fatalf("expected 2 datastore queries, got %d", backend.Count())
	}

	// 3. Subsequent request within TTL hits KV (0 additional Datastore queries).
	resp, raw = doReq(t, app, "GET", "/v1/analytics/overview", "", hdrSuper)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cached overview = %d", resp.StatusCode)
	}
	if backend.Count() != 2 {
		t.Fatalf("KV hit should not hit Datastore; count = %d", backend.Count())
	}

	// 4. Expire fresh KV key AND take Datastore offline -> MUST serve last-known KV snapshot with stale=true!
	kvStore.ExpireFreshKeys()
	backend.SetReady(false)
	nowMu.Lock()
	nowVal = nowVal.Add(45 * time.Second)
	nowMu.Unlock()

	resp, raw = doReq(t, app, "GET", "/v1/analytics/overview", "", hdrSuper)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("outage stale fallback status = %d (%s), want 200", resp.StatusCode, raw)
	}
	var staleOut AnalyticsOverviewOut
	_ = json.Unmarshal(raw, &staleOut)
	if !staleOut.Data.Freshness.Stale {
		t.Fatalf("expected freshness.stale=true on Datastore outage, got %+v", staleOut.Data.Freshness)
	}
	if staleOut.Data.KPIs.MRRCents != 550000 {
		t.Fatalf("expected last-known KV mrr 550000, got %d", staleOut.Data.KPIs.MRRCents)
	}
	if staleOut.Data.Freshness.LagMs < 45_000 {
		t.Fatalf("expected lagMs >= 45000, got %d", staleOut.Data.Freshness.LagMs)
	}

	// 5. Datastore offline with cold/empty KV -> fails cleanly with 503 (never unbounded RPC sweep).
	resp, _ = doReq(t, app, "GET", "/v1/analytics/overview?window=7d", "", hdrSuper)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("outage without snapshot status = %d, want 503", resp.StatusCode)
	}
}

func TestSingleflightCoalescesConcurrentCacheMisses(t *testing.T) {
	var calls atomic.Int32
	gate := make(chan struct{})
	backend := newFakeBackend(func(_ context.Context, _ string, _ ...any) ([]map[string]any, error) {
		calls.Add(1)
		<-gate
		return []map[string]any{{"revenue_cents": int64(777)}}, nil
	})
	svc := NewService(Config{Backend: backend, KV: newMemKV()})

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.fetchOverview(context.Background(), Scope{Super: true}, "30d")
			errs <- err
		}()
	}
	time.Sleep(30 * time.Millisecond)
	close(gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("fetchOverview failed: %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected singleflight to coalesce %d concurrent misses into 1 Datastore query, got %d", n, got)
	}
}

// ── 5. Bounded Query Semaphore Shedding ──────────────────────────────────────

func TestQuerySemaphoreBoundsConcurrency(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	backend := newFakeBackend(func(_ context.Context, _ string, _ ...any) ([]map[string]any, error) {
		started <- struct{}{}
		<-release
		return []map[string]any{{"ai_tokens": float64(100)}}, nil
	})
	app, svc := newTestApp(t, Config{
		Backend:              backend,
		MaxConcurrentQueries: 2,
		QueryTimeout:         2 * time.Second,
	})

	// Occupy both semaphore slots with distinct queries (so singleflight doesn't merge them).
	var wg sync.WaitGroup
	for _, w := range []string{"24h", "7d"} {
		wg.Add(1)
		go func(win string) {
			defer wg.Done()
			doReq(t, app, "POST", "/v1/analytics/query", `{"metrics":["ai.tokens"],"window":"`+win+`"}`, hdrSuper)
		}(w)
	}
	<-started
	<-started

	// 3rd concurrent query must be shed immediately with 429 (not hang or overload).
	resp, _ := doReq(t, app, "POST", "/v1/analytics/query", `{"metrics":["ai.tokens"],"window":"30d"}`, hdrSuper)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("excess query status = %d, want 429", resp.StatusCode)
	}
	if stats := svc.backend.SemaphoreStats(); stats.Rejected != 1 || stats.InFlight != 2 {
		t.Fatalf("unexpected semaphore stats: %+v", stats)
	}

	close(release)
	wg.Wait()
}

// ── 6. Health & Freshness Endpoints ──────────────────────────────────────────

func TestHealthAndFreshnessEndpoints(t *testing.T) {
	wmTime := time.Date(2026, 11, 25, 11, 59, 55, 0, time.UTC)
	backend := newFakeBackend(func(_ context.Context, sql string, _ ...any) ([]map[string]any, error) {
		switch {
		case strings.Contains(sql, "system.parts"):
			return []map[string]any{{"active_parts": int64(42), "max_parts_per_partition": int64(10)}}, nil
		case strings.Contains(sql, "analytics.watermarks"):
			return []map[string]any{
				{
					"pipeline":              "commerce",
					"watermark_ts":          wmTime,
					"lag_ms":                int64(5000),
					"rows_processed":        int64(100000),
					"reconcile_drift_minor": int64(0),
					"status":                "fresh",
					"updated_at":            wmTime,
				},
				{
					"pipeline":              "o11y",
					"watermark_ts":          wmTime.Add(-90 * time.Second),
					"lag_ms":                int64(95000),
					"rows_processed":        int64(5000000),
					"reconcile_drift_minor": int64(150),
					"status":                "lagging",
					"updated_at":            wmTime,
				},
			}, nil
		default:
			return nil, nil
		}
	})
	app, _ := newTestApp(t, Config{Backend: backend, KV: newMemKV()})

	// Health
	resp, raw := doReq(t, app, "GET", "/v1/analytics/health", "", hdrSuper)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d (%s)", resp.StatusCode, raw)
	}
	var hOut HealthOut
	if err := json.Unmarshal(raw, &hOut); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if hOut.Data.Status != "healthy" || !hOut.Data.Datastore.Reachable || hOut.Data.Datastore.ActiveParts != 42 || hOut.Data.Datastore.MergePressure != "low" {
		t.Fatalf("unexpected health: %+v", hOut.Data)
	}
	if !hOut.Data.KV.Configured || !hOut.Data.KV.Reachable || hOut.Data.Semaphore.Capacity != MaxConcurrentQueries {
		t.Fatalf("unexpected health subsystems: %+v", hOut.Data)
	}

	// Freshness
	resp, raw = doReq(t, app, "GET", "/v1/analytics/freshness", "", hdrAcmeMember)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("freshness status = %d (%s)", resp.StatusCode, raw)
	}
	var fOut FreshnessOut
	if err := json.Unmarshal(raw, &fOut); err != nil {
		t.Fatalf("decode freshness: %v", err)
	}
	if len(fOut.Data.Pipelines) != 2 || fOut.Data.MaxLagMs != 95000 || fOut.Data.StalePipelines != 1 || fOut.Data.TotalDriftCents != 150 {
		t.Fatalf("unexpected freshness: %+v", fOut.Data)
	}
}

// ── 7. Shared Coalesced Real-Time SSE Hub ────────────────────────────────────

func TestCoalescedSSEHub(t *testing.T) {
	var fetches atomic.Int64
	var revenue atomic.Int64
	revenue.Store(1000)

	hub := NewHub(func(_ context.Context, scope Scope) (AnalyticsOverviewData, error) {
		fetches.Add(1)
		// Small delay so all 20 concurrent Subscribe calls overlap and coalesce.
		time.Sleep(15 * time.Millisecond)
		return AnalyticsOverviewData{
			Scope:  scope.Key(),
			Org:    scope.Org,
			Window: "30d",
			KPIs:   OverviewKPIs{RevenueCents: revenue.Load()},
		}, nil
	}, 120*time.Millisecond, 250*time.Millisecond)

	// 1. 20 concurrent subscribers on `global` trigger ONLY 1 backend fetch!
	const numSubs = 20
	subs := make([]*subscription, numSubs)
	var wg sync.WaitGroup
	var errCount atomic.Int32
	for i := range numSubs {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sub, err := hub.Subscribe(context.Background(), Scope{Super: true}, 0)
			if err != nil {
				errCount.Add(1)
				return
			}
			subs[idx] = sub
		}(i)
	}
	wg.Wait()
	if errCount.Load() != 0 {
		t.Fatalf("expected 0 subscribe errors, got %d", errCount.Load())
	}
	if got := fetches.Load(); got != 1 {
		t.Fatalf("expected 20 concurrent subscribers to trigger 1 backend fetch, got %d", got)
	}
	if st := hub.Stats(); st.ActiveStreams != numSubs || st.ActiveScopes != 1 {
		t.Fatalf("unexpected hub stats: %+v", st)
	}
	for i, s := range subs {
		if s.initialVer != 1 || s.upToDate {
			t.Fatalf("sub %d initialVer=%d upToDate=%v, want 1/false", i, s.initialVer, s.upToDate)
		}
	}

	// 2. ?since_version=1 reconnect when current version is 1 -> sends lightweight `: up-to-date`!
	resyncSub, err := hub.Subscribe(context.Background(), Scope{Super: true}, 1)
	if err != nil {
		t.Fatalf("resync subscribe: %v", err)
	}
	if !resyncSub.upToDate || resyncSub.initialVer != 1 {
		t.Fatalf("expected upToDate=true at version 1, got %+v", resyncSub)
	}
	var resyncBuf bytes.Buffer
	resyncCtx, resyncCancel := context.WithCancel(context.Background())
	resyncDone := make(chan struct{})
	go func() {
		defer close(resyncDone)
		pumpSSE(resyncCtx, bufio.NewWriter(&resyncBuf), resyncSub, 5*time.Second)
	}()
	time.Sleep(15 * time.Millisecond)
	resyncCancel()
	<-resyncDone
	resyncSub.Unsubscribe()
	if !strings.Contains(resyncBuf.String(), ": up-to-date\n\n") || strings.Contains(resyncBuf.String(), "event: snapshot") {
		t.Fatalf("expected ': up-to-date' without snapshot, got %q", resyncBuf.String())
	}

	// 3. Advance backend data -> shared ticker broadcasts `event: delta` with monotonic version 2 to all 20 subscribers.
	revenue.Store(2500)
	for i, s := range subs {
		select {
		case frame := <-s.sub.ch:
			if frame.event != "delta" || frame.version != 2 {
				t.Fatalf("sub %d got event=%q ver=%d, want delta/2", i, frame.event, frame.version)
			}
			var payload StreamPayload
			if err := json.Unmarshal(frame.data, &payload); err != nil {
				t.Fatalf("decode delta: %v", err)
			}
			if payload.Version != 2 || payload.Overview.KPIs.RevenueCents != 2500 {
				t.Fatalf("unexpected delta payload: %+v", payload)
			}
		case <-time.After(1 * time.Second):
			t.Fatalf("sub %d timed out waiting for delta frame", i)
		}
	}

	// 4. Disconnect all subscribers -> ActiveStreams=0, ActiveScopes=0, and background ticks STOP!
	for _, s := range subs {
		s.Unsubscribe()
	}
	if st := hub.Stats(); st.ActiveStreams != 0 || st.ActiveScopes != 0 {
		t.Fatalf("expected 0 active streams/scopes after disconnect, got %+v", st)
	}
	// Let any in-flight tick exit, then confirm 0 further fetches occur across multiple tick intervals.
	time.Sleep(60 * time.Millisecond)
	afterDisconnect := fetches.Load()
	time.Sleep(150 * time.Millisecond)
	if got := fetches.Load(); got != afterDisconnect {
		t.Fatalf("background ticks kept running after all subscribers disconnected: before=%d after=%d", afterDisconnect, got)
	}
}

func TestSSEHubDropsSlowSubscriberWithoutBlocking(t *testing.T) {
	var rev atomic.Int64
	rev.Store(1)
	hub := NewHub(func(_ context.Context, scope Scope) (AnalyticsOverviewData, error) {
		return AnalyticsOverviewData{
			Scope:  scope.Key(),
			Window: "30d",
			KPIs:   OverviewKPIs{RevenueCents: rev.Load()},
		}, nil
	}, 10*time.Second, 10*time.Second)
	hub.bufSize = 1 // tiny buffer to deterministically trigger slow-subscriber drop

	slow, err := hub.Subscribe(context.Background(), Scope{Super: true}, 0)
	if err != nil {
		t.Fatalf("subscribe slow: %v", err)
	}
	fast, err := hub.Subscribe(context.Background(), Scope{Super: true}, 0)
	if err != nil {
		t.Fatalf("subscribe fast: %v", err)
	}
	defer fast.Unsubscribe()

	sh := hub.scopes["global"]

	// Publish two deltas while slow never drains its buffer of size 1.
	rev.Store(2)
	_ = hub.refreshScope(context.Background(), sh, true)
	<-fast.sub.ch // fast drains ver 2

	rev.Store(3)
	_ = hub.refreshScope(context.Background(), sh, true)
	f3 := <-fast.sub.ch // fast still receives ver 3 without being blocked by slow!
	if f3.version != 3 {
		t.Fatalf("fast got version %d, want 3", f3.version)
	}
	if !slow.sub.closed.Load() {
		t.Fatal("expected slow subscriber to be closed/dropped when its buffer overflowed")
	}
	if st := hub.Stats(); st.ActiveStreams != 1 {
		t.Fatalf("expected 1 active stream remaining, got %d", st.ActiveStreams)
	}
}

func TestPumpSSEHeartbeatAndSnapshot(t *testing.T) {
	sub := &subscription{
		initialVer: 7,
		initialRaw: []byte(`{"version":7,"scope":"global"}`),
		sub:        &subscriber{ch: make(chan streamFrame, 2)},
	}
	var buf bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		pumpSSE(ctx, bufio.NewWriter(&buf), sub, 25*time.Millisecond)
	}()
	time.Sleep(65 * time.Millisecond)
	cancel()
	<-done

	out := buf.String()
	if !strings.Contains(out, "id: 7\nevent: snapshot\ndata: {\"version\":7,\"scope\":\"global\"}\n\n") {
		t.Fatalf("missing snapshot frame: %q", out)
	}
	if !strings.Contains(out, ": ping\n\n") {
		t.Fatalf("missing heartbeat ping: %q", out)
	}
}

func TestGroupedQueryAndStaleFallback(t *testing.T) {
	backend := newFakeBackend(func(_ context.Context, sql string, _ ...any) ([]map[string]any, error) {
		if strings.Contains(sql, "GROUP BY model") {
			return []map[string]any{
				{"model": "zen-pro", "ai_requests": int64(300), "ai_tokens": int64(90000)},
				{"model": "zen-mini", "ai_requests": int64(120), "ai_tokens": int64(35000)},
			}, nil
		}
		return []map[string]any{
			{"ai_requests": int64(420), "ai_tokens": int64(125000)},
		}, nil
	})
	kvStore := newMemKV()
	app, _ := newTestApp(t, Config{Backend: backend, KV: kvStore})

	body := `{"metrics":["ai.requests","ai.tokens"],"group_by":["model"],"window":"7d","limit":10}`
	resp, raw := doReq(t, app, "POST", "/v1/analytics/query", body, hdrSuper)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("grouped query status = %d (%s)", resp.StatusCode, raw)
	}
	var out QueryOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Data.Rows) != 2 || out.Data.Rows[0].Dimensions["model"] != "zen-pro" || out.Data.Totals["ai.tokens"] != 125000 {
		t.Fatalf("unexpected grouped query output: %+v", out.Data)
	}
	if out.Data.Plan.Table != "analytics.rollup_ai_hourly" || out.Data.Plan.Limit != 10 || out.Data.Freshness.Stale {
		t.Fatalf("unexpected plan/freshness: %+v %+v", out.Data.Plan, out.Data.Freshness)
	}

	// Expire fresh KV + take Datastore down -> query serves stale KV snapshot with stale=true.
	kvStore.ExpireFreshKeys()
	backend.SetReady(false)
	resp, raw = doReq(t, app, "POST", "/v1/analytics/query", body, hdrSuper)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stale query status = %d (%s)", resp.StatusCode, raw)
	}
	var staleOut QueryOut
	_ = json.Unmarshal(raw, &staleOut)
	if !staleOut.Data.Freshness.Stale || staleOut.Data.Totals["ai.tokens"] != 125000 {
		t.Fatalf("expected stale=true with totals 125000, got %+v", staleOut.Data)
	}
}

func TestStreamEndpointValidationAndHeaders(t *testing.T) {
	backend := newFakeBackend(func(_ context.Context, _ string, _ ...any) ([]map[string]any, error) {
		return []map[string]any{{"revenue_cents": int64(500)}}, nil
	})
	app, _ := newTestApp(t, Config{Backend: backend})

	// Invalid since_version -> 400.
	resp, _ := doReq(t, app, "GET", "/v1/analytics/stream?since_version=notanumber", "", hdrSuper)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid since_version status = %d, want 400", resp.StatusCode)
	}

	// Datastore down + cold KV -> 503 fast fail instead of hanging open dead stream.
	backend.SetReady(false)
	resp, _ = doReq(t, app, "GET", "/v1/analytics/stream", "", hdrSuper)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unavailable stream status = %d, want 503", resp.StatusCode)
	}
}

// ── 8. OpenAPI / zipdoc Projection & Complete Field Prose ────────────────────

func TestOpenAPIProjectionAndFieldProse(t *testing.T) {
	app, _ := newTestApp(t, Config{Backend: newFakeBackend(nil)})

	doc, err := spec.Of(app, spec.Info{Title: "analytics", Version: "v1"})
	if err != nil {
		t.Fatalf("spec.Of: %v", err)
	}
	bare, err := spec.Bare(doc)
	if err != nil {
		t.Fatalf("spec.Bare: %v", err)
	}
	if len(bare) > 0 {
		t.Errorf("undescribed published schema properties: %s", strings.Join(bare, ", "))
	}

	raw, err := json.Marshal(app.OpenAPISpec())
	if err != nil {
		t.Fatalf("marshal openapi: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal openapi: %v", err)
	}
	paths := m["paths"].(map[string]any)
	wantOps := map[string]string{
		"GET /v1/analytics/metrics":   "analyticsMetrics",
		"POST /v1/analytics/query":    "analyticsQuery",
		"GET /v1/analytics/overview":  "analyticsOverview",
		"GET /v1/analytics/health":    "analyticsHealth",
		"GET /v1/analytics/freshness": "analyticsFreshness",
	}
	for route, wantID := range wantOps {
		parts := strings.SplitN(route, " ", 2)
		method, path := strings.ToLower(parts[0]), parts[1]
		item, ok := paths[path].(map[string]any)
		if !ok {
			t.Errorf("missing OpenAPI path %s", path)
			continue
		}
		op, ok := item[method].(map[string]any)
		if !ok {
			t.Errorf("missing OpenAPI method %s %s", strings.ToUpper(method), path)
			continue
		}
		if gotID, _ := op["operationId"].(string); gotID != wantID {
			t.Errorf("%s operationId = %q, want %q", route, gotID, wantID)
		}
		if desc, _ := op["description"].(string); strings.TrimSpace(desc) == "" {
			t.Errorf("%s has empty description", route)
		}
	}
}

// ── 9. L3 Materialized Serving Snapshots & Cold-Boot Fallback ────────────────

func TestL3SnapshotReadAndColdBootFallback(t *testing.T) {
	t.Run("L3 serving snapshots hit first with max_threads=1", func(t *testing.T) {
		backend := newFakeBackend(func(_ context.Context, sql string, args ...any) ([]map[string]any, error) {
			if strings.Contains(sql, "analytics.serving_global_overview") {
				return []map[string]any{{
					"orgs_total": int64(1000000), "members_total": int64(5000000),
					"cash_minor": int64(900000), "mrr_minor": int64(420000),
					"ai_requests_30d": int64(88000), "spans_24h": int64(123456),
				}}, nil
			}
			if strings.Contains(sql, "analytics.serving_org_overview") {
				return []map[string]any{{
					"org": "acme", "members": int64(42), "cash_minor": int64(75000), "mrr_minor": int64(29000),
				}}, nil
			}
			t.Errorf("unexpected fallback query when L3 snapshot populated: %s", sql)
			return nil, nil
		})
		app, _ := newTestApp(t, Config{Backend: backend})

		resp, raw := doReq(t, app, "GET", "/v1/analytics/overview", "", hdrSuper)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("global overview status = %d (%s)", resp.StatusCode, raw)
		}
		var gOut AnalyticsOverviewOut
		_ = json.Unmarshal(raw, &gOut)
		if gOut.Data.Freshness.Source != "analytics.serving_global_overview" || gOut.Data.KPIs.Orgs != 1000000 || gOut.Data.KPIs.MRRCents != 420000 {
			t.Fatalf("unexpected global L3 overview: %+v", gOut.Data)
		}
		q := backend.Last()
		if !strings.Contains(q.SQL, "ORDER BY refreshed_at DESC") || !strings.Contains(q.SQL, "SETTINGS max_threads = 1, optimize_read_in_order = 1") {
			t.Fatalf("global L3 query missing benchmarked settings: %s", q.SQL)
		}

		resp, raw = doReq(t, app, "GET", "/v1/analytics/overview", "", hdrAcmeMember)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("org overview status = %d (%s)", resp.StatusCode, raw)
		}
		var oOut AnalyticsOverviewOut
		_ = json.Unmarshal(raw, &oOut)
		if oOut.Data.Freshness.Source != "analytics.serving_org_overview" || oOut.Data.KPIs.Users != 42 || oOut.Data.KPIs.MRRCents != 29000 {
			t.Fatalf("unexpected org L3 overview: %+v", oOut.Data)
		}
		q = backend.Last()
		if !strings.Contains(q.SQL, "WHERE org = ?") || !strings.Contains(q.SQL, "SETTINGS max_threads = 1") {
			t.Fatalf("org L3 query missing org predicate or max_threads=1: %s", q.SQL)
		}
	})

	t.Run("cold boot empty L3 falls back transparently to L2 views", func(t *testing.T) {
		backend := newFakeBackend(func(_ context.Context, sql string, _ ...any) ([]map[string]any, error) {
			if strings.Contains(sql, "analytics.serving_global_overview") || strings.Contains(sql, "analytics.serving_org_overview") {
				return nil, nil // cold boot before first 5s refresh tick
			}
			if strings.Contains(sql, "analytics.global_overview") {
				return []map[string]any{{"orgs_total": int64(3100), "mrr_minor": int64(111000)}}, nil
			}
			if strings.Contains(sql, "analytics.org_overview") {
				return []map[string]any{{"org": "acme", "members": int64(9), "mrr_minor": int64(5000)}}, nil
			}
			return nil, nil
		})
		app, _ := newTestApp(t, Config{Backend: backend})

		resp, raw := doReq(t, app, "GET", "/v1/analytics/overview", "", hdrSuper)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("cold boot global overview = %d (%s)", resp.StatusCode, raw)
		}
		var gOut AnalyticsOverviewOut
		_ = json.Unmarshal(raw, &gOut)
		if gOut.Data.Freshness.Source != "analytics.global_overview" || gOut.Data.KPIs.Orgs != 3100 || gOut.Data.KPIs.MRRCents != 111000 {
			t.Fatalf("cold boot global fallback failed: %+v", gOut.Data)
		}

		resp, raw = doReq(t, app, "GET", "/v1/analytics/overview", "", hdrAcmeMember)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("cold boot org overview = %d (%s)", resp.StatusCode, raw)
		}
		var oOut AnalyticsOverviewOut
		_ = json.Unmarshal(raw, &oOut)
		if oOut.Data.Freshness.Source != "analytics.org_overview" || oOut.Data.KPIs.Users != 9 || oOut.Data.KPIs.MRRCents != 5000 {
			t.Fatalf("cold boot org fallback failed: %+v", oOut.Data)
		}
	})
}

// ── 10. Semantic Metric Registry & 14-Domain 100+ Catalog ────────────────────

func TestSemanticMetricRegistryAnd14Domains(t *testing.T) {
	all := CatalogMetrics("")
	if len(all) < 100 {
		t.Fatalf("governed metric catalog len = %d, want >= 100", len(all))
	}

	wantDomains := []string{
		"commerce", "iam", "ai", "enso", "kai", "compute", "storage",
		"network", "o11y", "product", "bots", "science", "dev", "cyber",
	}
	byDomain := make(map[string]int, len(wantDomains))
	for _, m := range all {
		byDomain[m.Domain]++
		if m.ID == "" || m.Unit == "" || m.Source == "" || m.Accuracy == "" || m.FreshnessTargetSec <= 0 || len(m.Tables) == 0 {
			t.Errorf("incomplete metadata on metric %q: %+v", m.ID, m)
		}
		for _, tbl := range m.Tables {
			if !allowedPhysicalTables[tbl] {
				t.Errorf("metric %q uses unapproved physical table %q", m.ID, tbl)
			}
		}
	}
	for _, d := range wantDomains {
		if byDomain[d] == 0 {
			t.Errorf("catalog missing governed metrics for domain %q", d)
		}
		filtered := CatalogMetrics(d)
		if len(filtered) != byDomain[d] {
			t.Errorf("CatalogMetrics(%q) = %d, want %d", d, len(filtered), byDomain[d])
		}
	}

	// Register validation rejects unsafe/invalid definitions.
	badCases := []struct {
		name string
		def  MetricDef
	}{
		{
			name: "duplicate existing ID",
			def: MetricDef{
				ID: "ai.requests", Domain: "ai", Description: "dup", Unit: "count", Source: "ai_ledger",
				FreshnessTargetSec: 10, Accuracy: AccuracyExact, HourlyTable: "analytics.rollup_ai_hourly",
				Expr: "countMerge(requests)", Dimensions: []string{"org"},
			},
		},
		{
			name: "sql injection in expr",
			def: MetricDef{
				ID: "custom.bad_sql", Domain: "ai", Description: "bad", Unit: "count", Source: "ai_ledger",
				FreshnessTargetSec: 10, Accuracy: AccuracyExact, HourlyTable: "analytics.rollup_ai_hourly",
				Expr: "count(); DROP TABLE analytics.rollup_ai_hourly--", Dimensions: []string{"org"},
			},
		},
		{
			name: "raw event table blocked",
			def: MetricDef{
				ID: "custom.raw_table", Domain: "o11y", Description: "bad", Unit: "count", Source: "o11y",
				FreshnessTargetSec: 10, Accuracy: AccuracyExact, HourlyTable: "event.span",
				Expr: "count()", Dimensions: []string{"org"},
			},
		},
		{
			name: "disallowed dimension",
			def: MetricDef{
				ID: "custom.bad_dim", Domain: "ai", Description: "bad", Unit: "count", Source: "ai_ledger",
				FreshnessTargetSec: 10, Accuracy: AccuracyExact, HourlyTable: "analytics.rollup_ai_hourly",
				Expr: "countMerge(requests)", Dimensions: []string{"org", "secret_col"},
			},
		},
		{
			name: "invalid accuracy",
			def: MetricDef{
				ID: "custom.bad_acc", Domain: "ai", Description: "bad", Unit: "count", Source: "ai_ledger",
				FreshnessTargetSec: 10, Accuracy: "guess", HourlyTable: "analytics.rollup_ai_hourly",
				Expr: "countMerge(requests)", Dimensions: []string{"org"},
			},
		},
	}
	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			if err := Register(tc.def); err == nil {
				t.Fatalf("Register(%s) succeeded, want error", tc.name)
			}
		})
	}

	// Register a valid dynamic metric and verify immediate query planning.
	customID := "ai.test_custom_registered_metric"
	if _, exists := LookupMetric(customID); !exists {
		err := Register(MetricDef{
			ID:                 customID,
			Domain:             "ai",
			Description:        "Custom dynamically registered AI metric for semantic registry test.",
			Unit:               "tokens",
			Dimensions:         []string{"org", "model", "minute", "hour", "day"},
			Source:             "ai_ledger",
			FreshnessTargetSec: 2,
			Accuracy:           AccuracyExact,
			TableKind:          TableRollup,
			TableGroup:         "rollup_ai",
			MinuteTable:        "analytics.rollup_ai_minute",
			HourlyTable:        "analytics.rollup_ai_hourly",
			DailyTable:         "analytics.rollup_ai_daily",
			Expr:               "sumMerge(tokens)",
			Additive:           true,
		})
		if err != nil {
			t.Fatalf("Register valid metric failed: %v", err)
		}
	}
	plan, err := Plan(QueryIn{Metrics: []string{customID}, Window: "1h", GroupBy: []string{"minute"}}, Scope{Super: true})
	if err != nil {
		t.Fatalf("Plan custom registered metric failed: %v", err)
	}
	if plan.Table != "analytics.rollup_ai_minute" || plan.Granularity != "minute" {
		t.Fatalf("custom metric plan = (%s, %s), want (analytics.rollup_ai_minute, minute)", plan.Table, plan.Granularity)
	}
}

// ── 11. 1-Minute Real-Time Rollup Query Planning ─────────────────────────────

func TestMinuteRollupQueryPlanning(t *testing.T) {
	super := Scope{Super: true}
	fixedNow := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		req       QueryIn
		wantTable string
		wantGran  string
		wantSQL   string
	}{
		{
			name:      "ai 1h auto -> minute rollup",
			req:       QueryIn{Metrics: []string{"ai.requests", "ai.tokens"}, Window: "1h"},
			wantTable: "analytics.rollup_ai_minute",
			wantGran:  "minute",
			wantSQL:   "minute >= ?",
		},
		{
			name:      "ai 2h auto -> minute rollup",
			req:       QueryIn{Metrics: []string{"ai.requests"}, Window: "2h", GroupBy: []string{"minute"}},
			wantTable: "analytics.rollup_ai_minute",
			wantGran:  "minute",
			wantSQL:   "toStartOfMinute(minute) AS minute",
		},
		{
			name:      "o11y 1h auto -> minute rollup",
			req:       QueryIn{Metrics: []string{"o11y.requests", "o11y.p95_ms"}, Window: "1h", GroupBy: []string{"service"}},
			wantTable: "analytics.rollup_o11y_minute",
			wantGran:  "minute",
			wantSQL:   "FROM analytics.rollup_o11y_minute",
		},
		{
			name:      "o11y explicit minute granularity 6h -> minute rollup",
			req:       QueryIn{Metrics: []string{"o11y.errors"}, Window: "6h", Granularity: "minute", GroupBy: []string{"minute"}},
			wantTable: "analytics.rollup_o11y_minute",
			wantGran:  "minute",
			wantSQL:   "toStartOfMinute(minute) AS minute",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := planAt(tc.req, super, fixedNow)
			if err != nil {
				t.Fatalf("planAt failed: %v", err)
			}
			if plan.Table != tc.wantTable || plan.Granularity != tc.wantGran {
				t.Fatalf("got (%s, %s), want (%s, %s)", plan.Table, plan.Granularity, tc.wantTable, tc.wantGran)
			}
			if !strings.Contains(plan.SQL, tc.wantSQL) {
				t.Fatalf("SQL %q missing %q", plan.SQL, tc.wantSQL)
			}
		})
	}

	// Minute granularity rejections: > 48h window, unsupported metric, incompatible granularity.
	for _, tc := range []struct {
		name string
		req  QueryIn
	}{
		{name: "minute over 48h rejected", req: QueryIn{Metrics: []string{"ai.requests"}, Window: "72h", Granularity: "minute"}},
		{name: "minute on commerce daily rejected", req: QueryIn{Metrics: []string{"commerce.revenue"}, Window: "2h", Granularity: "minute"}},
		{name: "group_by minute with day granularity rejected", req: QueryIn{Metrics: []string{"ai.requests"}, Window: "2h", Granularity: "day", GroupBy: []string{"minute"}}},
		{name: "group_by minute with hour granularity rejected", req: QueryIn{Metrics: []string{"ai.requests"}, Window: "2h", Granularity: "hour", GroupBy: []string{"minute"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := planAt(tc.req, super, fixedNow); err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
		})
	}
}
