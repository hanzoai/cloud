package functions

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	db, err := cloud.TenantDB(t.TempDir(), "test", "", "functions")
	if err != nil {
		t.Fatalf("TenantDB: %v", err)
	}
	s, err := openStore(db)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mkFn(org, name string) Function {
	now := time.Now().Unix()
	return Function{
		ID: org + "-" + name + "-id", Org: org, Name: name, Namespace: "default",
		Runtime: "python", Code: "print('hi')", TimeoutSec: 30, MemoryLimit: "256Mi",
		EnvNames: []string{"API_KEY"}, Status: "ready", LastDeployAt: now,
	}
}

func TestUpsertCreateThenRedeploy(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	f, err := s.Upsert(ctx, mkFn("maxpower", "resize"))
	if err != nil || f.DeployVer != 1 {
		t.Fatalf("first deploy should be v1: %v v%d", err, f.DeployVer)
	}
	f2 := mkFn("maxpower", "resize")
	f2.Code = "print('v2')"
	f2, err = s.Upsert(ctx, f2)
	if err != nil || f2.DeployVer != 2 {
		t.Fatalf("redeploy should be v2: %v v%d", err, f2.DeployVer)
	}
	if f2.CreatedAt != f.CreatedAt {
		t.Fatalf("createdAt must survive redeploy: %d vs %d", f2.CreatedAt, f.CreatedAt)
	}
	got, _ := s.Get(ctx, "maxpower", "resize")
	if got.Code != "print('v2')" {
		t.Fatalf("redeploy must update code, got %q", got.Code)
	}
}

func TestTenantIsolation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Upsert(ctx, mkFn("maxpower", "shared")); err != nil {
		t.Fatalf("seed maxpower: %v", err)
	}
	if _, err := s.Upsert(ctx, mkFn("acme", "shared")); err != nil {
		t.Fatalf("seed acme: %v", err)
	}
	_ = s.InsertInvocation(ctx, Invocation{ID: "iv1", Org: "maxpower", FunctionName: "shared", Status: "ok", CreatedAt: time.Now().Unix()})

	// acme cannot see maxpower's invocations.
	acInv, _ := s.ListInvocations(ctx, "acme", "shared", 100)
	if len(acInv) != 0 {
		t.Fatalf("acme must not see maxpower invocations, got %d", len(acInv))
	}
	mpInv, _ := s.ListInvocations(ctx, "maxpower", "shared", 100)
	if len(mpInv) != 1 {
		t.Fatalf("maxpower should have 1 invocation, got %d", len(mpInv))
	}
	// acme delete must not touch maxpower's function or its invocation log.
	if _, err := s.Delete(ctx, "acme", "shared"); err != nil {
		t.Fatalf("acme delete: %v", err)
	}
	if _, err := s.Get(ctx, "maxpower", "shared"); err != nil {
		t.Fatalf("maxpower function must survive acme delete: %v", err)
	}
	if got, _ := s.ListInvocations(ctx, "maxpower", "shared", 100); len(got) != 1 {
		t.Fatalf("maxpower invocation log must survive acme delete, got %d", len(got))
	}
}

func TestStatsSinceDerivation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().Unix()
	_, _ = s.Upsert(ctx, mkFn("maxpower", "f"))
	seed := []Invocation{
		{ID: "a", Org: "maxpower", FunctionName: "f", Status: "ok", DurationMs: 100, CreatedAt: now - 10},
		{ID: "b", Org: "maxpower", FunctionName: "f", Status: "ok", DurationMs: 300, CreatedAt: now - 5},
		{ID: "c", Org: "maxpower", FunctionName: "f", Status: "error", DurationMs: 200, CreatedAt: now - 2},
	}
	for _, iv := range seed {
		if err := s.InsertInvocation(ctx, iv); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	st, err := s.StatsSince(ctx, "maxpower", "f", now-window7d)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.Count != 3 || st.Errors != 1 || st.SumDuration != 600 {
		t.Fatalf("derived stats wrong: %+v", st)
	}
	// toView folds these into REAL rollups; success rate = 2/3.
	v := (&svc{}).toView(Function{Name: "f", Namespace: "default"}, st)
	if v.Invocations7d == nil || *v.Invocations7d != 3 {
		t.Fatalf("invocations7d should be 3, got %v", v.Invocations7d)
	}
	if v.SuccessRate == nil || *v.SuccessRate < 0.66 || *v.SuccessRate > 0.67 {
		t.Fatalf("successRate should be ~0.667, got %v", v.SuccessRate)
	}
	if v.AvgDurationMs == nil || *v.AvgDurationMs != 200 {
		t.Fatalf("avgDurationMs should be 200, got %v", v.AvgDurationMs)
	}
}

// toView must OMIT metrics (nil → "—" in UI) when there are zero invocations —
// never fabricate a 0% success rate.
func TestToViewNoInvocationsOmitsMetrics(t *testing.T) {
	v := (&svc{}).toView(Function{Name: "f", Namespace: "default"}, InvStats{})
	if v.Invocations7d != nil || v.SuccessRate != nil || v.AvgDurationMs != nil || v.Errors7d != nil {
		t.Fatalf("metrics must be nil when no invocations, got %+v", v)
	}
	if v.Endpoint != "/v1/functions/f/invoke" {
		t.Fatalf("endpoint should be the invoke URL, got %q", v.Endpoint)
	}
}

func TestBuildMetricsBucketsRealRows(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	spec := rangeSpecs["24H"]
	invs := []Invocation{
		{FunctionName: "f", Status: "ok", CreatedAt: now.Add(-2 * time.Hour).Unix()},
		{FunctionName: "f", Status: "ok", CreatedAt: now.Add(-2 * time.Hour).Unix()},
		{FunctionName: "f", Status: "error", CreatedAt: now.Add(-1 * time.Hour).Unix()},
		{FunctionName: "g", Status: "timeout", CreatedAt: now.Add(-30 * time.Minute).Unix()},
		{FunctionName: "f", Status: "ok", CreatedAt: now.Add(-48 * time.Hour).Unix()}, // out of window
	}
	m := buildMetrics(invs, spec, now)
	if m.Status.Success != 2 || m.Status.Error != 1 || m.Status.Timeout != 1 {
		t.Fatalf("status donut wrong: %+v", m.Status)
	}
	if m.CostCents != nil {
		t.Fatalf("costCents must be null (no cost source), got %v", *m.CostCents)
	}
	if len(m.Series) != 2 {
		t.Fatalf("want 2 series (f,g), got %d", len(m.Series))
	}
	// Each series has exactly `buckets` points; total v across f = 3 (in-window).
	for _, s := range m.Series {
		if len(s.Points) != spec.buckets {
			t.Fatalf("series %s should have %d points, got %d", s.Key, spec.buckets, len(s.Points))
		}
		sum := 0
		for _, p := range s.Points {
			sum += p.V
		}
		if s.Key == "f" && sum != 3 {
			t.Fatalf("f in-window count should be 3, got %d", sum)
		}
	}
}

func TestParseExecBodyDefensive(t *testing.T) {
	cases := []struct {
		raw     string
		wantOut string
		wantErr string
	}{
		{`{"stdout":"hello","stderr":""}`, "hello", ""},
		{`{"run":{"stdout":"nested","stderr":"boom"}}`, "nested", "boom"},
		{`{"output":"alt"}`, "alt", ""},
		{`not json at all`, "not json at all", ""},
		{`{"error":"failed"}`, "", "failed"},
	}
	for _, c := range cases {
		out, errout := parseExecBody([]byte(c.raw))
		if out != c.wantOut || errout != c.wantErr {
			t.Fatalf("parseExecBody(%q) = (%q,%q), want (%q,%q)", c.raw, out, errout, c.wantOut, c.wantErr)
		}
	}
}

func TestExecClientFailClosed(t *testing.T) {
	e := &execClient{} // no upstream configured
	if e.configured() {
		t.Fatalf("empty exec client must be unconfigured")
	}
	_, err := e.run(context.Background(), mkFn("maxpower", "f"), "in", 30)
	if err != errExecUnconfigured {
		t.Fatalf("unconfigured run must fail closed, got %v", err)
	}
}
