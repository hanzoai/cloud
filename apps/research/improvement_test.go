package research

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

func improvementFixture() (Study, Run, Run) {
	s := Study{Project: proj, ID: "harness", Improvement: &ImprovementPolicy{
		Baseline: "base", Target: "hanzoai/dev", MinQuestions: 10,
		Metrics: []ImprovementMetric{{Category: "all", Metric: "accuracy", Direction: "higher", MinGain: 1}, {Category: "all", Metric: "latency_ms", Direction: "lower", MaxRegression: 5}},
	}}
	a := Run{Project: proj, ID: "base", Study: s.ID, Benchmark: "held-out", Split: "test", System: "dev", Version: "v1", Commit: "base-revision", DatasetDigest: "frozen-data", Questions: new(20), Answered: new(20), Ended: "2026-10-24T01:00:00Z", Measures: []Measure{{Category: "all", Metric: "accuracy", Value: 70}, {Category: "all", Metric: "latency_ms", Value: 100}}}
	b := a
	b.ID = "candidate"
	b.Version = "v2"
	b.Commit = "candidate-revision"
	b.Measures = []Measure{{Category: "all", Metric: "accuracy", Value: 75}, {Category: "all", Metric: "latency_ms", Value: 90}}
	return s, a, b
}

func TestImprovementRequiresCompleteComparableEvidenceAndGuardrails(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*Study, *Run)
	}{
		{"gain", "improved", func(_ *Study, _ *Run) {}},
		{"partial metric", "blocked", func(_ *Study, b *Run) { b.Measures[0].N = new(10); b.Measures[0].Of = new(20) }},
		{"partial", "blocked", func(_ *Study, b *Run) { b.Answered = new(10) }},
		{"no dataset", "blocked", func(_ *Study, b *Run) { b.DatasetDigest = "" }},
		{"other dataset", "blocked", func(_ *Study, b *Run) { b.DatasetDigest = "other" }},
		{"other project", "blocked", func(_ *Study, b *Run) { b.Project = "other" }},
		{"other study", "blocked", func(_ *Study, b *Run) { b.Study = "other" }},
		{"missing provenance", "blocked", func(_ *Study, b *Run) { b.Commit = "" }},
		{"missing metric", "blocked", func(_ *Study, b *Run) { b.Measures = b.Measures[:1] }},
		{"different size", "blocked", func(_ *Study, b *Run) { b.Questions = new(21); b.Answered = new(21) }},
		{"guardrail regression", "regressed", func(_ *Study, b *Run) { b.Measures[1].Value = 106 }},
		{"equal", "inconclusive", func(_ *Study, b *Run) { b.Measures[0].Value = 70 }},
		{"below gain", "inconclusive", func(_ *Study, b *Run) { b.Measures[0].Value = 70.5 }},
		{"interval required", "blocked", func(s *Study, _ *Run) { s.Improvement.RequireIntervals = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, a, b := improvementFixture()
			tc.change(&s, &b)
			if got := reviewImprovement(s, a, b); got.Status != tc.want {
				t.Fatalf("%+v, want %s", got, tc.want)
			}
		})
	}
}

func TestImprovementIntervalsCannotTurnNoiseIntoGain(t *testing.T) {
	s, a, b := improvementFixture()
	s.Improvement.RequireIntervals = true
	a.Measures[0].Lo = new(65.0)
	a.Measures[0].Hi = new(78.0)
	b.Measures[0].Lo = new(72.0)
	b.Measures[0].Hi = new(80.0)
	if got := reviewImprovement(s, a, b); got.Status != "inconclusive" {
		t.Fatalf("overlap: %+v", got)
	}
	a.Measures[0].Hi = new(71.0)
	if got := reviewImprovement(s, a, b); got.Status != "improved" {
		t.Fatalf("separated: %+v", got)
	}
	// A malformed interval is missing evidence, never a significance claim.
	b.Measures[0].Lo = new(90.0)
	if got := reviewImprovement(s, a, b); got.Status != "blocked" {
		t.Fatalf("invalid: %+v", got)
	}
}

func TestImprovementRecordsOnceRetriesDeliveryAndIsolatesTenants(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountResearch(t)
		original := sendImprovementEvent
		t.Cleanup(func() { sendImprovementEvent = original })
		fail := true
		calls := 0
		keys := []string{}
		sendImprovementEvent = func(ctx context.Context, ev *client.ProductEvent) error {
			calls++
			keys = append(keys, ev.Key)
			if got := zip.CallerOf(ctx).Org; got != "acme" {
				t.Fatalf("wrong org %q", got)
			}
			if ev.Source != "research" || ev.Name != "improvement.reviewed" {
				t.Fatalf("wrong event: %+v", ev)
			}
			if fail {
				return errors.New("engine temporarily unavailable")
			}
			return nil
		}
		s, a, b := improvementFixture()
		if code, out := do(t, app, http.MethodPost, "/v1/research/studies", "acme", proj, studyBatch{Studies: []Study{s}}); code != 200 {
			t.Fatalf("study %d %+v", code, out)
		}
		body := runBatch{Runs: []Run{a, b}}
		if code, _ := do(t, app, http.MethodPost, "/v1/research/runs", "acme", proj, body); code != 503 {
			t.Fatalf("delivery failed: %d", code)
		}
		_, out := do(t, app, http.MethodGet, "/v1/research/actions", "acme", proj, nil)
		if out["total"] != float64(1) {
			t.Fatalf("review must survive delivery failure: %+v", out)
		}
		fail = false
		if code, out := do(t, app, http.MethodPost, "/v1/research/runs", "acme", proj, body); code != 200 {
			t.Fatalf("retry %d %+v", code, out)
		}
		if code, _ := do(t, app, http.MethodPost, "/v1/research/runs", "acme", proj, body); code != 200 {
			t.Fatalf("duplicate %d", code)
		}
		if calls != 2 || keys[0] != keys[1] {
			t.Fatalf("delivery is not stable: %d %v", calls, keys)
		}
		_, out = do(t, app, http.MethodGet, "/v1/research/actions", "globex", proj, nil)
		if out["total"] != float64(0) {
			t.Fatalf("other tenant saw review: %+v", out)
		}
		// New evidence must get a new identity; it cannot inherit the earlier review.
		b.Measures[0].Value = 60
		b.Revision = "corrected"
		if code, out := do(t, app, http.MethodPost, "/v1/research/runs", "acme", proj, runBatch{Runs: []Run{b}}); code != 200 {
			t.Fatalf("correction %d %+v", code, out)
		}
		_, out = do(t, app, http.MethodGet, "/v1/research/actions", "acme", proj, nil)
		if out["total"] != float64(2) || calls != 3 || keys[2] == keys[1] {
			t.Fatalf("correction did not retain and supersede evidence: %+v keys=%v", out, keys)
		}

	})
}

func TestImprovementPolicyIsVersionedAndValidated(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, ctx := newStore(t)
		s, _, _ := improvementFixture()
		if err := validateImprovement(s.Improvement); err != nil {
			t.Fatal(err)
		}
		_, err := st.putStudies(ctx, proj, []Study{s})
		if err != nil {
			t.Fatal(err)
		}
		got, err := st.studies(ctx, proj, s.ID)
		if err != nil || len(got) != 1 || got[0].Improvement == nil {
			t.Fatalf("policy lost: %+v %v", got, err)
		}
		h := hashStudy(s)
		s.Improvement.Metrics[0].Direction = "lower"
		if h == hashStudy(s) {
			t.Fatal("policy correction must change study identity")
		}
		s.Improvement.Metrics[0].Direction = "unknown"
		if validateImprovement(s.Improvement) == nil {
			t.Fatal("unknown direction accepted")
		}
		s.Improvement.Metrics[0].Direction = "higher"
		s.Improvement.Metrics = append(s.Improvement.Metrics, s.Improvement.Metrics[0])
		if validateImprovement(s.Improvement) == nil {
			t.Fatal("duplicate metric accepted")
		}
		// Wire remains ordinary JSON, suitable for generated SDKs and MCP.
		if _, err := json.Marshal(got); err != nil {
			t.Fatal(err)
		}

	})
}
