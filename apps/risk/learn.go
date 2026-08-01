package risk

// learn.go is the model plane: train, exhaustive-search, score — on the AI
// cloud's own substrate, in process, per tenant.
//
// WHERE THE MODELS TRAIN, AND WHY NOT THE ALTERNATIVES.
//
//   - hanzoai/ai is the LLM router. It has no trainer.
//   - candle is Rust and cloud does not link it.
//   - Kubeflow (/v1/train/jobs, /v1/train/experiments) is real and stays the
//     escape hatch for a customer who wants a heavy supervised sweep — but
//     apps/ml fails closed on it today because no kserve/katib/trainer chart is
//     deployed. It is not a substrate this product can stand on.
//
// So: luxfi/aml's half-space trees, in this process. That is not a placeholder —
// it is the right shape for the problem, for five reasons that a neural
// alternative cannot offer:
//
//  1. NO TRAINING PASS AND NO RETAINED SAMPLE. The geometry is built BEFORE any
//     data arrives; the model IS a set of mass counters. Training is one online
//     increment per observation, so there is no job, no queue and no window in
//     which the tenant is protected by a stale model.
//  2. PER TENANT INCLUDING THE GEOMETRY. The seed is mix(cfg.Seed, orgID), so two
//     tenants do not merely hold different counters — they hold DIFFERENT TREES.
//     Probing one reveals nothing about where another's regions lie.
//  3. BOUNDED. 336 KB per tenant at the defaults, MaxOrgs 256, LRU eviction. A
//     tenant that goes idle costs nothing and one that comes back re-warms.
//  4. ATTRIBUTION IS A COUNTERFACTUAL ON THE MODEL THAT RAISED THE ALERT. Move
//     one coordinate to its neutral value, rescore, and the drop IS that
//     feature's contribution. No second explainer model, and therefore no second
//     thing that can be wrong. That is what makes a decline defensible.
//  5. IT CANNOT ACT ALONE. Evidence is capped at review; weight is non-negative;
//     NaN and Inf are refused.
//
// EXHAUSTIVE SEARCH WITHOUT KUBEFLOW. luxfi/aml's replay package already replays
// a candidate over real history through the engine's own evaluator, writing
// nothing — it reaches the evaluator through a one-method interface and history
// through another, and imports no store, so a dry run is STRUCTURALLY dry.
// mlSearch generalises that from one rule candidate to a grid over the model
// topology, replays each candidate over the tenant's own recent decisions, and
// returns the learning curve and the winning topology. Native Go, per tenant, no
// GPU, no CRD.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/types"
	"github.com/luxfi/aml/pkg/velocity"
)

// snapshotKey names the row a tenant's learned state is kept under. One row: a
// tenant has one model, and a second row would be a second answer to one
// question.
const snapshotKey = "anomaly"

// saveModel writes a tenant's learned state into its own encrypted file.
//
// This is what makes a rollout survivable. cloud is strategy Recreate at one
// replica, so every deploy drops the process — and with it every warming model
// and the appetite threshold it had computed. Without this, every deploy
// silently resets every tenant to warming, and a warming model REFUSES to score,
// which reads as "clean" to anything that does not check Refusal.
func saveModel(s *shelf, model *anomaly.Store, t Tenant) error {
	snap, ok := model.Snapshot(t.String())
	if !ok {
		return nil // nothing learned for this tenant; nothing to keep
	}
	body, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	db, err := s.open(t)
	if err != nil {
		return err
	}
	return putModel(db, snapshotKey, body)
}

// loadModel restores a tenant's learned state on first touch after a restart.
// A snapshot whose shape does not match the running inventory is REFUSED by the
// engine, not coerced: state the model would treat as its own memory has to have
// come from this algorithm over this feature set.
func loadModel(s *shelf, model *anomaly.Store, t Tenant) error {
	db, err := s.open(t)
	if err != nil {
		return err
	}
	body, err := getModel(db, snapshotKey)
	if err != nil {
		return nil // no snapshot is the normal first-run state, not a failure
	}
	var snap anomaly.Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return err
	}
	// The snapshot's tenant must be the tenant asking for it. The engine checks
	// this too; checking here as well means a restore of A's file into B's
	// request is refused at the boundary that knows who asked.
	if snap.OrgID != t.String() {
		return fmt.Errorf("risk: snapshot belongs to another tenant")
	}
	return model.Restore(snap)
}

// ── exhaustive search ───────────────────────────────────────────────────────

// axis is one dimension of the topology grid, with the values the search may
// try. Both are CLOSED: an unbounded grid is an unbounded amount of a shared
// pod's CPU, reachable by anyone with a key.
type axis struct {
	name   string
	values []float64
}

// grid is the search space. Trees x Depth x Window x Blend x Review is 3*3*3*3*3
// = 243 candidates at the widest, which is seconds of one core over a few
// thousand replayed decisions — bounded by construction rather than by a
// timeout.
func grid() []axis {
	return []axis{
		{"trees", []float64{15, 25, 40}},
		{"depth", []float64{6, 8, 10}},
		{"window", []float64{128, 256, 512}},
		{"blend", []float64{0.1, 0.25, 0.5}},
		{"review", []float64{0.005, 0.01, 0.02}},
	}
}

// candidate is one point in the grid.
type candidate struct {
	// Trees is how many half-space trees the model holds.
	Trees int `json:"trees"`
	// Depth is how deep each tree splits.
	Depth int `json:"depth"`
	// Window is how many observations make up one reference window.
	Window int `json:"window"`
	// Blend is how much of a closing window folds into the reference.
	Blend float64 `json:"blend"`
	// Review is the share of the stream this topology may send for examination.
	Review float64 `json:"review"`
}

// trial is what one candidate did over the replayed history.
type trial struct {
	// Candidate is the topology tried.
	Candidate candidate `json:"candidate"`
	// Scored is how many observations the model was able to score. A topology
	// that warms slowly scores fewer, which is a real cost and is reported.
	Scored int `json:"scored"`
	// Alerted is how many of those it would have alerted on.
	Alerted int `json:"alerted"`
	// Realised is Alerted/Scored — the share actually reached, against the
	// Review share intended. The gap between the two IS the governance report.
	Realised float64 `json:"realised"`
	// Separation is the mean score of the alerted set minus the mean score of
	// the rest. It is the ranking objective: a topology that separates the tail
	// from the body is doing the job whatever its absolute scores look like.
	Separation float64 `json:"separation"`
	// Warm is how many observations passed before the model would score at all.
	Warm int `json:"warm"`
}

// searchReport is the whole answer.
type searchReport struct {
	// Events is how many historical observations were replayed.
	Events int `json:"events"`
	// From and To are the period they span.
	From time.Time `json:"from,omitzero"`
	To   time.Time `json:"to,omitzero"`
	// Trials is every candidate tried, best first.
	Trials []trial `json:"trials"`
	// Winner is the best-separating topology that also honoured its stated
	// appetite. Absent when no candidate did both.
	Winner *candidate `json:"winner,omitempty"`
	// Curve is the learning curve of the winner: separation as a function of how
	// much of the history it had seen, in ten steps.
	Curve []float64 `json:"curve,omitempty"`
	// Refusal names why a report is empty when it is. An empty report and a
	// report of no alerts are opposite facts.
	Refusal string `json:"refusal,omitempty"`
}

// errNoHistory is what a search returns instead of a clean-looking zero. replay
// refuses an empty history for exactly this reason: "no alerts" is what a quiet
// rule and an unrun rule both look like, and the difference is the whole reason
// a sandbox exists.
var errNoHistory = fmt.Errorf("risk: no history to replay, so a result would be indistinguishable from a quiet model")

// searchRun replays every candidate over one tenant's own recorded observations.
//
// Nothing is written and nothing outside this function's own scratch models is
// touched: each candidate gets a FRESH anomaly.Store over a FRESH velocity store
// seeded from the tenant key, so a search can never move the live model's
// counters, and two searches for two tenants can never see each other's.
func searchRun(ctx context.Context, t Tenant, history []observation) (searchReport, error) {
	if len(history) == 0 {
		return searchReport{Refusal: errNoHistory.Error()}, errNoHistory
	}
	rep := searchReport{Events: len(history), From: history[0].at, To: history[len(history)-1].at}

	for _, c := range candidates() {
		select {
		case <-ctx.Done():
			return rep, ctx.Err()
		default:
		}
		tr, err := replayCandidate(t, c, history)
		if err != nil {
			continue // a topology the engine refuses is not a result, it is a non-candidate
		}
		rep.Trials = append(rep.Trials, tr)
	}
	sort.SliceStable(rep.Trials, func(i, j int) bool {
		return rep.Trials[i].Separation > rep.Trials[j].Separation
	})
	for i := range rep.Trials {
		// The winner must have SCORED something and must have honoured its stated
		// appetite within a factor of two. A topology that separates beautifully
		// while alerting on nothing is not a control.
		tr := rep.Trials[i]
		if tr.Scored == 0 || tr.Realised == 0 {
			continue
		}
		if tr.Realised > 2*tr.Candidate.Review {
			continue
		}
		w := tr.Candidate
		rep.Winner = &w
		rep.Curve = curve(t, w, history)
		break
	}
	if rep.Winner == nil {
		rep.Refusal = "no candidate both scored and honoured its stated appetite over this history"
	}
	return rep, nil
}

// candidates expands the grid. Written as an explicit product so the count is
// visible in the code rather than emergent from a recursion.
func candidates() []candidate {
	g := grid()
	var out []candidate
	for _, trees := range g[0].values {
		for _, depth := range g[1].values {
			for _, win := range g[2].values {
				for _, blend := range g[3].values {
					for _, review := range g[4].values {
						out = append(out, candidate{
							Trees: int(trees), Depth: int(depth), Window: int(win),
							Blend: blend, Review: review,
						})
					}
				}
			}
		}
	}
	return out
}

// replayCandidate runs one topology over the history in a sandbox.
func replayCandidate(t Tenant, c candidate, history []observation) (trial, error) {
	vel := velocity.New(velocity.Config{})
	model, err := anomaly.New(anomaly.Config{
		Trees: c.Trees, Depth: c.Depth, Window: c.Window, Blend: c.Blend,
		Appetite: anomaly.Appetite{Review: c.Review, Sample: 0.001},
		Shadow:   true, // a sandbox never alerts for real
	}, vel)
	if err != nil {
		return trial{}, err
	}

	tr := trial{Candidate: c}
	var alerted, rest []float64
	for _, o := range history {
		record(vel, t, o)
		tx := types.Transaction{
			ID: o.id, OrgID: t.String(), UserID: o.subject, AccountID: o.subject,
			Currency: o.currency, Direction: o.direction,
			IPAddress: o.signals["ip"], DeviceFingerprint: o.signals["device"],
			Timestamp: o.at, USD: nanoUSD(o.amount),
		}
		a := model.Inspect(tx, types.Entity{ID: o.subject, OrgID: t.String()})
		// Inspect does not learn, so a second pass through the learning path is
		// what advances the model. judge(learn=true) is Assess; in shadow it
		// returns no hit, and the counters still move — which is exactly the
		// replay we want.
		_, _ = model.Assess(tx, types.Entity{ID: o.subject, OrgID: t.String()})
		if !a.Scored {
			tr.Warm++
			continue
		}
		tr.Scored++
		if a.Score >= a.Cut {
			tr.Alerted++
			alerted = append(alerted, a.Score)
		} else {
			rest = append(rest, a.Score)
		}
	}
	if tr.Scored > 0 {
		tr.Realised = round4(float64(tr.Alerted) / float64(tr.Scored))
	}
	tr.Separation = round4(mean(alerted) - mean(rest))
	return tr, nil
}

// curve is the winner's separation as a function of how much history it had
// seen. Ten steps: enough to see whether the model is still improving, few
// enough that the answer is a chart and not a data set.
func curve(t Tenant, c candidate, history []observation) []float64 {
	out := make([]float64, 0, 10)
	for i := 1; i <= 10; i++ {
		n := len(history) * i / 10
		if n == 0 {
			out = append(out, 0)
			continue
		}
		tr, err := replayCandidate(t, c, history[:n])
		if err != nil {
			out = append(out, 0)
			continue
		}
		out = append(out, tr.Separation)
	}
	return out
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	v := s / float64(len(xs))
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}
