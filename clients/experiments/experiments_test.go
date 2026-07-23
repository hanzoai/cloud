package experiments

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/flags"
	"github.com/hanzoai/cloud/clients/research"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mountStack mounts the three composed planes — flags (assignment), research
// (evidence), experiments (the composition) — on one app sharing one DataDir, so the
// e2e proof exercises the REAL seams: real deterministic flags assignment, real
// research evidence writes, the real /v1/experiments surface.
func mountStack(t *testing.T) *zip.App {
	t.Helper()
	dir := t.TempDir()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	deps := cloud.Deps{Logger: luxlog.New("test"), DataDir: dir}
	if err := flags.Mount(app, deps); err != nil {
		t.Fatalf("flags mount: %v", err)
	}
	if err := research.Mount(app, deps); err != nil {
		t.Fatalf("research mount: %v", err)
	}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("experiments mount: %v", err)
	}
	t.Cleanup(func() {
		_ = flags.Shutdown(context.Background())
		_ = research.Shutdown()
		_ = Shutdown()
	})
	return app
}

// requireEngine skips the assignment-dependent proof when the native flags evaluator
// is not linked (cgo off): assignment is a native-engine evaluation.
func requireEngine(t *testing.T) {
	t.Helper()
	def := json.RawMessage(`{"key":"p","active":true,"filters":{"groups":[{"properties":[],"rollout_percentage":100}],"multivariate":{"variants":[{"key":"a","rollout_percentage":100}]}}}`)
	if err := flags.PutDef("probe", "", "p", def, "t"); err != nil {
		t.Fatalf("probe putdef: %v", err)
	}
	a, err := flags.Assign("probe", "", "p", "s1", nil)
	if err != nil {
		t.Skipf("native flags engine unavailable: %v", err)
	}
	if a.Variant != "a" {
		t.Skipf("flags engine not evaluating variants (got %q)", a.Variant)
	}
}

// do issues an HTTP request with a validated principal (the X-Org-Id + X-User-Id pair
// the gateway sets only from a verified credential); org "" exercises the
// no-principal path.
func do(t *testing.T, app *zip.App, method, path, org, project string, body any) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	if project != "" {
		req.Header.Set("X-Project-Id", project)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m
}

type fakeMetric struct{ outcomes []MetricOutcome }

func (f *fakeMetric) Outcomes(_ context.Context, _, _, _ string, _, _ time.Time) ([]MetricOutcome, error) {
	return f.outcomes, nil
}

func featureExperimentBody() map[string]any {
	return map[string]any{
		"id":            "checkout_cta",
		"name":          "Checkout CTA",
		"metricEvent":   "order_completed",
		"exposureEvent": "checkout_viewed",
		"variants": []map[string]any{
			{"key": "control", "control": true, "weight": 50, "payload": map[string]any{"cta": "Buy now"}},
			{"key": "treatment", "weight": 50, "payload": map[string]any{"cta": "Get started"}},
		},
	}
}

// TestExperiment_FeatureFlagProof is the full end-to-end proof for a FEATURE
// experiment: two variants of an enablement flag -> subjects deterministically
// assigned via flags -> outcomes (faked, keyed by the real assignment) -> analyze
// yields lift + significance -> decide promotes the winner and the flag flips to
// 100% for it.
func TestExperiment_FeatureFlagProof(t *testing.T) {
	app := mountStack(t)
	requireEngine(t)
	org, project := "acme", ""
	const flagKey = "exp_checkout_cta"

	// 1. CREATE — writes the multivariate assignment flag + the registry row.
	code, body := do(t, app, http.MethodPost, "/v1/experiments", org, project, featureExperimentBody())
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	if body["flagKey"] != flagKey || body["status"] != "running" {
		t.Fatalf("create response wrong: %v", body)
	}

	// 2. DETERMINISTIC ASSIGNMENT — subject -> variant is stable and split ~50/50.
	const N = 2000
	variant := map[string]string{}
	counts := map[string]int{}
	for i := 0; i < N; i++ {
		subj := fmt.Sprintf("user-%d", i)
		v := assignVariant(t, org, project, flagKey, subj)
		if v != "control" && v != "treatment" {
			t.Fatalf("subject %s got unexpected variant %q", subj, v)
		}
		variant[subj] = v
		counts[v]++
	}
	// Stickiness: re-evaluating the same subjects yields the identical variant.
	for i := 0; i < 100; i++ {
		subj := fmt.Sprintf("user-%d", i)
		if got := assignVariant(t, org, project, flagKey, subj); got != variant[subj] {
			t.Fatalf("assignment NOT deterministic for %s: %q != %q", subj, got, variant[subj])
		}
	}
	// The HTTP assign route uses the SAME engine (same variant for the same subject).
	code, ab := do(t, app, http.MethodGet, "/v1/experiments/checkout_cta/assign?subject=user-1", org, project, nil)
	if code != http.StatusOK || ab["variant"] != variant["user-1"] {
		t.Fatalf("http assign mismatch: %v vs %s", ab, variant["user-1"])
	}
	if counts["control"] < N/4 || counts["treatment"] < N/4 {
		t.Fatalf("rollout split degenerate: %v", counts)
	}

	// 3. OUTCOMES flow into analytics (faked here): treatment converts ~20%, control
	//    ~10%, keyed by the REAL assignment so the analyze fold re-derives the buckets.
	fake := &fakeMetric{}
	mounted.metric = fake
	for subj, v := range variant {
		n := subjNum(t, subj)
		converted := n%10 < 1 // control ~10%
		if v == "treatment" {
			converted = n%10 < 2 // treatment ~20%
		}
		fake.outcomes = append(fake.outcomes, MetricOutcome{Subject: subj, Exposed: true, Converted: converted})
	}

	// 4. ANALYZE — per-variant lift + significance (the in-process seam campaign uses).
	a, err := Analyze(context.Background(), org, project, "checkout_cta", time.Now().Add(-time.Hour), time.Now(), 0.05)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	ctrl, treat := resultFor(t, a, "control"), resultFor(t, a, "treatment")
	if treat.Rate <= ctrl.Rate {
		t.Fatalf("treatment must beat control: treat=%+v ctrl=%+v", treat, ctrl)
	}
	if treat.Lift <= 0 {
		t.Fatalf("treatment lift must be positive, got %v", treat.Lift)
	}
	if !treat.Significant {
		t.Fatalf("treatment must be significant: p=%v exposed=%d", treat.PValue, treat.Exposed)
	}
	if a.Winner != "treatment" {
		t.Fatalf("winner = %q want treatment", a.Winner)
	}

	// analyze over HTTP too (shared *state uses the same fake) — persists evidence.
	code, _ = do(t, app, http.MethodPost, "/v1/experiments/checkout_cta/analyze?days=1", org, project, nil)
	if code != http.StatusOK {
		t.Fatalf("http analyze: %d", code)
	}

	// 5. EVIDENCE — per-variant samples landed in the research plane (kind "ab").
	rows, err := research.List(context.Background(), org, project, "ab")
	if err != nil {
		t.Fatalf("research list: %v", err)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.ID] = true
	}
	if !seen["checkout_cta:control"] || !seen["checkout_cta:treatment"] {
		t.Fatalf("research evidence missing per-variant rows: %v", seen)
	}

	// 6. DECIDE — promote treatment; the assignment flag flips to 100% treatment.
	code, dec := do(t, app, http.MethodPost, "/v1/experiments/checkout_cta/decide", org, project, map[string]any{"winner": "treatment"})
	if code != http.StatusOK || dec["status"] != "decided" || dec["winner"] != "treatment" {
		t.Fatalf("decide: %d %v", code, dec)
	}
	// EVERY subject now resolves to the winner (the flag rollout is 100% treatment).
	for i := 0; i < 300; i++ {
		subj := fmt.Sprintf("user-%d", i)
		if v := assignVariant(t, org, project, flagKey, subj); v != "treatment" {
			t.Fatalf("after decide, %s = %q, want treatment (flag not flipped)", subj, v)
		}
	}
}

// TestExperiment_CrossTenantDenied proves the org boundary holds on every verb: a
// second org can neither read, assign, analyze, nor decide the first org's
// experiment, and an unvalidated request is refused.
func TestExperiment_CrossTenantDenied(t *testing.T) {
	app := mountStack(t)
	requireEngine(t)

	code, _ := do(t, app, http.MethodPost, "/v1/experiments", "acme", "", featureExperimentBody())
	if code != http.StatusCreated {
		t.Fatalf("create in acme: %d", code)
	}

	// globex sees nothing of acme's experiment on any verb.
	for _, tc := range []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodGet, "/v1/experiments/checkout_cta", nil, http.StatusNotFound},
		{http.MethodGet, "/v1/experiments/checkout_cta/assign?subject=u1", nil, http.StatusNotFound},
		{http.MethodPost, "/v1/experiments/checkout_cta/analyze", nil, http.StatusNotFound},
		{http.MethodPost, "/v1/experiments/checkout_cta/decide", map[string]any{"winner": "treatment"}, http.StatusNotFound},
	} {
		if code, _ := do(t, app, tc.method, tc.path, "globex", "", tc.body); code != tc.want {
			t.Fatalf("globex %s %s = %d, want %d", tc.method, tc.path, code, tc.want)
		}
	}

	// globex's list is empty (physical store isolation).
	if code, m := do(t, app, http.MethodGet, "/v1/experiments", "globex", "", nil); code != http.StatusOK || m["total"].(float64) != 0 {
		t.Fatalf("globex list must be empty: %d %v", code, m)
	}

	// An unvalidated request (no principal) is refused.
	if code, _ := do(t, app, http.MethodGet, "/v1/experiments/checkout_cta", "", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-principal read = %d, want 403", code)
	}

	// Assignment is org-scoped at the flags plane too: globex evaluating acme's flag
	// key gets NOT-enrolled (its own flag store has no such def), never acme's split.
	if a, _ := flags.Assign("globex", "", "exp_checkout_cta", "user-1", nil); a.On || a.Variant != "" {
		t.Fatalf("globex must not evaluate acme's flag: %+v", a)
	}
}

// ── helpers ────────────────────────────────────────────────────────────────────

func assignVariant(t *testing.T, org, project, flagKey, subject string) string {
	t.Helper()
	a, err := flags.Assign(org, project, flagKey, subject, nil)
	if err != nil {
		t.Fatalf("assign %s: %v", subject, err)
	}
	return a.Variant
}

func subjNum(t *testing.T, subj string) int {
	t.Helper()
	n, err := strconv.Atoi(strings.TrimPrefix(subj, "user-"))
	if err != nil {
		t.Fatalf("subjNum %s: %v", subj, err)
	}
	return n
}

func resultFor(t *testing.T, a Analysis, variant string) Result {
	t.Helper()
	for _, r := range a.Results {
		if r.Variant == variant {
			return r
		}
	}
	t.Fatalf("no result for variant %q in %+v", variant, a.Results)
	return Result{}
}
