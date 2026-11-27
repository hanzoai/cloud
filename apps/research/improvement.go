package research

// Continuous improvement is a fold over the evidence we already retain. It
// needs neither a second queue nor a model call: recording runs or a study
// re-evaluates its candidates and files idempotent DRAFT actions in this store.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk/plane"
	ormdb "github.com/hanzoai/orm/db"
	"github.com/zap-proto/zip"
)

// ImprovementPolicy is a study's explicit acceptance contract. It enables
// automatic review, not execution, publication, training, merge or deployment.
type ImprovementPolicy struct {
	// Baseline is the reference run id, in this study's project and study.
	Baseline string `json:"baseline"`
	// Target names the harness, app, skill, connector or infrastructure under test.
	Target string `json:"target"`
	// Metrics are the objective followed by optional regression guardrails.
	Metrics []ImprovementMetric `json:"metrics"`
	// MinQuestions is the minimum completed evaluation size (at least one).
	MinQuestions int `json:"min_questions"`
	// RequireIntervals blocks a verdict unless both runs report non-overlapping
	// intervals for the primary metric. Intervals must use the same method.
	RequireIntervals bool `json:"require_intervals"`
}

// deliverImprovementEvents drains committed research actions through the existing
// durable automation engine. The action is the outbox; a tiny meta marker is its
// delivery receipt. A failed delivery leaves it pending, and the next write or
// retry resumes it. Auto's deterministic run ids make a crash after delivery and
// before the receipt safe. No model call or new scheduler is involved.
func (o ops) deliverImprovementEvents(ctx context.Context, st *store, org string) error {
	recs, err := latest[actionRecord](ctx, st, kindAction)
	if err != nil {
		return err
	}
	for _, rec := range recs {
		if rec.By != "research.improvement" || rec.Status != draft {
			continue
		}
		key := st.db.NewKey(kindMeta, "delivered:"+rec.ContentHash, 0, nil)
		var receipt metaRecord
		if err := st.db.Get(ctx, key, &receipt); err == nil {
			continue
		} else if !errors.Is(err, ormdb.ErrNoSuchEntity) {
			return err
		}
		// Only the draft's structured review is sent; no transcript, answer or
		// credential is present in this event.
		payload := rec.Action
		callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := sendImprovementEvent(plane.For(callCtx, org), &client.ProductEvent{Source: "research", Name: "improvement.reviewed", Key: rec.ContentHash, Payload: payload})
		cancel()
		if err != nil {
			return zip.Errorf(http.StatusServiceUnavailable, "research review is recorded but automation delivery is pending; retry the upload")
		}
		if _, err := st.db.CreateIfAbsent(ctx, key, &metaRecord{Seq: rec.Seq}); err != nil {
			return err
		}
		if err := o.shipDurable(org); err != nil {
			return err
		}
	}
	return nil
}

var sendImprovementEvent = func(ctx context.Context, event *client.ProductEvent) error {
	_, err := plane.Call[client.ProductEvent, client.ProductEventDelivered](ctx, manifest.Auto, client.AutoEventDeliver, event)
	return err
}

// ImprovementMetric declares the direction and acceptance threshold of a metric.
type ImprovementMetric struct {
	// Category is the measure's category, exactly as recorded in the run.
	Category string `json:"category"`
	// Metric is the measure's name, exactly as recorded in the run.
	Metric string `json:"metric"`
	// Direction is higher or lower; improvement is signed in this direction.
	Direction string `json:"direction"`
	// MinGain is the primary metric's required absolute gain in its own units.
	MinGain float64 `json:"min_gain"`
	// MaxRegression is the greatest permitted degradation for this metric.
	MaxRegression float64 `json:"max_regression"`
}

// ImprovementReview is a reproducible lesson backed by exact run content hashes.
// It is embedded in a private DRAFT action, never accepted as an authorization.
type ImprovementReview struct {
	// Project is the server-stamped project containing all evidence.
	Project string `json:"project"`
	// Study is the study whose declared policy was evaluated.
	Study string `json:"study"`
	// Target is the harness or product named in that policy.
	Target string `json:"target"`
	// Baseline is the reference run id.
	Baseline string `json:"baseline"`
	// Candidate is the candidate run id.
	Candidate string `json:"candidate"`
	// BaselineHash pins the exact reference evidence, including provenance.
	BaselineHash string `json:"baseline_hash"`
	// CandidateHash pins the exact candidate evidence, including provenance.
	CandidateHash string `json:"candidate_hash"`
	// Status is improved, regressed, inconclusive or blocked.
	Status string `json:"status"`
	// Blocks explain incomplete, missing or incompatible evidence.
	Blocks []string `json:"blocks"`
	// Gains are absolute changes signed in each declared metric's direction.
	Gains []float64 `json:"gains"`
	// Policy is the acceptance contract used, so later edits cannot change this lesson.
	Policy ImprovementPolicy `json:"policy"`
}

func validateImprovement(p *ImprovementPolicy) error {
	if p == nil {
		return nil
	}
	if strings.TrimSpace(p.Baseline) == "" || len(p.Baseline) > 256 || strings.TrimSpace(p.Target) == "" || len(p.Target) > 256 {
		return fmt.Errorf("improvement needs baseline and target (at most 256 bytes each)")
	}
	if p.MinQuestions < 1 || len(p.Metrics) < 1 || len(p.Metrics) > 16 {
		return fmt.Errorf("improvement needs min_questions >= 1 and 1 to 16 metrics")
	}
	seen := map[string]bool{}
	for _, m := range p.Metrics {
		key := m.Category + "\x00" + m.Metric
		if m.Metric == "" || len(m.Metric) > 128 || len(m.Category) > 128 || seen[key] || (m.Direction != "higher" && m.Direction != "lower") {
			return fmt.Errorf("improvement metrics need unique bounded names and direction higher or lower")
		}
		seen[key] = true
		if !finite(m.MinGain) || !finite(m.MaxRegression) || m.MinGain < 0 || m.MaxRegression < 0 {
			return fmt.Errorf("improvement thresholds must be finite and non-negative")
		}
	}
	return nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func reviewImprovement(study Study, a, b Run) ImprovementReview {
	p := *study.Improvement
	r := ImprovementReview{Project: study.Project, Study: study.ID, Target: p.Target,
		Baseline: a.ID, Candidate: b.ID, BaselineHash: hashRun(a), CandidateHash: hashRun(b),
		Status: "blocked", Blocks: []string{}, Gains: []float64{}, Policy: p}
	a.Completion = completionOf(a.Questions, a.Answered, a.Ended)
	b.Completion = completionOf(b.Questions, b.Answered, b.Ended)
	r.Blocks = append(r.Blocks, compare(a, b).Blocks...)
	for _, run := range []Run{a, b} {
		if run.Project != study.Project || run.Study != study.ID {
			r.Blocks = append(r.Blocks, "run is outside the study/project")
		}
		if run.DatasetDigest == "" || run.Commit == "" || run.Version == "" {
			r.Blocks = append(r.Blocks, "run needs dataset digest, commit and system version")
		}
		if run.Questions == nil || *run.Questions < p.MinQuestions {
			r.Blocks = append(r.Blocks, "run is below the declared minimum evaluation size")
		}
	}
	if a.Questions != nil && b.Questions != nil && *a.Questions != *b.Questions {
		r.Blocks = append(r.Blocks, "runs cover different evaluation sizes")
	}
	primary := false
	regressed := false
	for i, metric := range p.Metrics {
		find := func(run Run) (Measure, bool) {
			var found Measure
			n := 0
			for _, m := range run.Measures {
				if m.Category == metric.Category && m.Metric == metric.Metric {
					found = m
					n++
				}
			}
			return found, n == 1 && finite(found.Value)
		}
		x, okA := find(a)
		y, okB := find(b)
		if !okA || !okB {
			r.Blocks = append(r.Blocks, "metric missing, duplicated or non-finite: "+metric.Metric)
			continue
		}
		// Declared category coverage must be complete and comparable. Unknown
		// coverage is retained as unknown, never silently normalized.
		for _, m := range []Measure{x, y} {
			if (m.N != nil && *m.N < p.MinQuestions) || (m.Of != nil && (m.N == nil || *m.N != *m.Of)) {
				r.Blocks = append(r.Blocks, "metric coverage is incomplete: "+metric.Metric)
			}
		}
		if (x.N == nil) != (y.N == nil) || (x.N != nil && y.N != nil && *x.N != *y.N) {
			r.Blocks = append(r.Blocks, "metric coverage differs: "+metric.Metric)
		}
		gain := y.Value - x.Value
		if metric.Direction == "lower" {
			gain = -gain
		}
		r.Gains = append(r.Gains, gain)
		if gain < -metric.MaxRegression {
			regressed = true
		}
		if i == 0 {
			primary = gain > 0 && gain >= metric.MinGain
			if p.RequireIntervals {
				valid := func(m Measure) bool {
					return m.Lo != nil && m.Hi != nil && finite(*m.Lo) && finite(*m.Hi) && *m.Lo <= m.Value && m.Value <= *m.Hi
				}
				if !valid(x) || !valid(y) {
					r.Blocks = append(r.Blocks, "primary metric needs valid intervals")
				} else if overlap(x, y) != nil && *overlap(x, y) {
					primary = false
				}
			}
		}
	}
	if len(r.Blocks) != 0 {
		return r
	}
	r.Status = "inconclusive"
	if regressed {
		r.Status = "regressed"
	} else if primary {
		r.Status = "improved"
	}
	return r
}

// reviewImprovements runs before ship-before-ack. A crash or retry recomputes the
// same action identity from the exact policy and evidence, so it appends nothing
// twice and cannot carry an old authorization onto changed evidence.
func (o ops) reviewImprovements(ctx context.Context, st *store, org, project string) error {
	studies, err := st.studies(ctx, project, "")
	if err != nil {
		return err
	}
	runs, err := st.runs(ctx, runFilter{Project: project})
	if err != nil {
		return err
	}
	for _, study := range studies {
		if study.Improvement == nil {
			continue
		}
		var baseline Run
		for _, r := range runs {
			if r.Project == study.Project && r.Study == study.ID && r.ID == study.Improvement.Baseline {
				baseline = r
			}
		}
		if baseline.ID == "" {
			continue
		} // evidence arriving in either order is supported
		for _, run := range runs {
			if run.Project != study.Project || run.Study != study.ID || run.ID == baseline.ID {
				continue
			}
			review := reviewImprovement(study, baseline, run)
			identity := hashJoin(study.Project, study.ID, baseline.ID, run.ID, marshal(study.Improvement), review.BaselineHash, review.CandidateHash)
			act, _ := json.Marshal(map[string]any{
				"id": "improvement/" + identity, "status": draft, "verdict": review.Status,
				"rule": "research.improvement.v1", "description": fmt.Sprintf("%s: %s versus %s is %s; reproduce and review before proposing a shared change", study.Improvement.Target, run.ID, baseline.ID, review.Status),
				"review": review,
			})
			rec := actionRecord{ActionID: "improvement/" + identity, Action: act, Status: draft, Time: now(), By: "research.improvement"}
			if err := rec.sealed(); err != nil {
				return err
			}
			if _, err := putAll(ctx, st, kindAction, []actionRecord{rec}, func(r *actionRecord, seq int64) { r.Seq = seq }); err != nil {
				o.s.Log.Error("research improvement review failed", "org", org, "err", err)
				return zip.Errorf(http.StatusServiceUnavailable, "research improvement review was not recorded; retry the upload")
			}
		}
	}
	return nil
}
