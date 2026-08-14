package guide

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// strategies.go is the FILTER concern: the tactics corpus as an org-scoped, filterable
// read — the library the future recommendation-surface engine consumes. It resolves the
// caller's active blueprint (org override → brand → fixture), takes its ENABLED
// strategies, and narrows them by the explicit category/workload query filters AND by
// the org's OBSERVED growth profile through the tag join.
//
// The join (reconciliation b) maps the corpus's authored tag vocabulary onto the
// observe layer's ACTUAL signal vocabulary (signals.go): a `stage:<name>` tag is a
// readiness floor satisfied once the org has REACHED that stage (monotone over the
// ladder); a `has:<capability>` tag is satisfied when the mapped observe signal is
// present. A strategy surfaces when EVERY one of its tags is satisfied — its tags are
// preconditions. The CONTENT is shared platform data (a tactic carries no org's
// records), so this leaks nothing across tenants; only the WHICH-surfaces decision uses
// the caller's own profile.

// stageAliasRank maps a stage token — from a strategy tag or the ?stage= query — to its
// rung on the observe ladder. It absorbs the ONE vocabulary reconciliation: the historic
// playbook's lowest stage `research` is the observe layer's `formed` (signals.go has no
// `research` rung), so both map to rank 0.
var stageAliasRank = map[string]int{
	"research":             0, // playbook 'research' == observe 'formed'
	string(StageFormed):    0,
	string(StageLaunched):  1,
	string(StageActivated): 2,
	string(StageScaling):   3,
}

// hasSignal maps a `has:<capability>` tag to the observe layer's signal key
// (reconciliation b). analytics/revenue are direct signals; a live website reads as a
// live deployment; the ad/email/sms capabilities read as the connected provider the
// corresponding Guide step wires (connect:facebook / google-ads / mailchimp / sms);
// leads read as funnel signups. A capability with no mapping is fail-closed
// not-satisfied (the strategy waits until the capability is observable), so a novel
// admin-added tag never spuriously surfaces a tactic.
var hasSignal = map[string]string{
	"analytics": SignalAnalytics,
	"revenue":   SignalRevenue,
	"website":   SignalDeployed,
	"facebook":  kindConnected + ":facebook",
	"adwords":   kindConnected + ":google-ads",
	"email":     kindConnected + ":mailchimp",
	"sms":       kindConnected + ":sms",
	"leads":     SignalFunnelSignups,
}

// stageRank ranks an observed Stage on the growth ladder (formed < launched < activated
// < scaling). Unknown reads as the floor.
func stageRank(s Stage) int {
	switch s {
	case StageLaunched:
		return 1
	case StageActivated:
		return 2
	case StageScaling:
		return 3
	default:
		return 0
	}
}

// tagSatisfied reports whether ONE strategy tag is satisfied by the org's profile. An
// un-kinded tag (no ":") is a free label, not a gate. A `stage:X` tag is a readiness
// floor (org rank >= X rank). A `has:X` tag requires its mapped observe signal present;
// an unmapped capability is not satisfiable (fail-closed).
func tagSatisfied(tag string, stage Stage, signals SignalSet) bool {
	kind, param, ok := strings.Cut(tag, ":")
	if !ok {
		return true
	}
	switch kind {
	case "stage":
		want, known := stageAliasRank[strings.ToLower(strings.TrimSpace(param))]
		if !known {
			return false
		}
		return stageRank(stage) >= want
	case "has":
		sig, known := hasSignal[strings.ToLower(strings.TrimSpace(param))]
		if !known {
			return false
		}
		return signals[sig]
	default:
		return true // an unknown kind is a free label, not a gate
	}
}

// strategyMatches reports whether every one of a strategy's tags is satisfied by the
// org's profile — a strategy's tags are ALL preconditions, so an untagged strategy is
// universally applicable (vacuously true).
func strategyMatches(st Strategy, stage Stage, signals SignalSet) bool {
	for _, tag := range st.Tags {
		if !tagSatisfied(tag, stage, signals) {
			return false
		}
	}
	return true
}

// strategyView is the corpus projection returned to a caller.
type strategyView struct {
	ID       string   `json:"id"`
	Category string   `json:"category"`
	Workload string   `json:"workload,omitempty"`
	Action   string   `json:"action"`
	Tags     []string `json:"tags,omitempty"`
}

// strategiesQuery narrows the corpus. Every field is optional and binds from the
// query string; an omitted filter matches everything.
type strategiesQuery struct {
	// Category filters to tactics in exactly this category.
	Category string `json:"category"`
	// Stage previews the corpus at a chosen growth stage
	// (research|formed|launched|activated|scaling), overriding the org's observed
	// one. An unknown value is ignored and the observed stage stands.
	Stage string `json:"stage"`
	// Workload filters to tactics with exactly this workload.
	Workload string `json:"workload"`
}

// corpusView is the filtered tactics corpus plus the stage it was filtered at.
type corpusView struct {
	// Stage is the growth stage the tag join ran at — the org's observed stage, or
	// the one ?stage= previewed.
	Stage Stage `json:"stage"`
	// Count is how many tactics survived every filter.
	Count int `json:"count"`
	// Strategies are the surviving tactics, in corpus authoring order.
	Strategies []strategyView `json:"strategies"`
}

// Strategies returns the ENABLED tactics corpus for the caller's org: the tactics
// library narrowed by the explicit category/workload filters AND by the org's
// OBSERVED growth stage and capability signals (a tactic's tags are
// preconditions, so it surfaces only once the org can act on it). Passing stage
// PREVIEWS the corpus at that stage instead of the observed one. The content is
// shared platform data — no org's records — and the read is never a billable
// effect.
//
// Example: {"category": "viral-coefficient", "stage": "scaling"}
func (o ops) strategies(ctx context.Context, in *strategiesQuery) (*corpusView, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(o.s.State.stores, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	bp, _ := o.s.State.resolveBlueprint(ctx, store)

	// The org's OBSERVED profile drives the tag join. An explicit stage previews a
	// chosen stage; the signals always reflect the org's real capabilities.
	funnel := analyticsFunnel(ctx, org)
	signals, _ := observe(ctx, org, o.s.State.signals, funnel)
	stage := classifyStage(signals)
	if q := strings.ToLower(strings.TrimSpace(in.Stage)); q != "" {
		if rank, known := stageAliasRank[q]; known {
			stage = ladderStage(rank)
		}
	}

	catFilter := strings.TrimSpace(in.Category)
	workloadFilter := strings.TrimSpace(in.Workload)

	out := make([]strategyView, 0, 32)
	for _, st := range bp.enabledStrategies() {
		if catFilter != "" && st.Category != catFilter {
			continue
		}
		if workloadFilter != "" && st.Workload != workloadFilter {
			continue
		}
		if !strategyMatches(st, stage, signals) {
			continue
		}
		out = append(out, strategyView{ID: st.ID, Category: st.Category, Workload: st.Workload, Action: st.Action, Tags: st.Tags})
	}
	return &corpusView{Stage: stage, Count: len(out), Strategies: out}, nil
}

// ladderStage is the inverse of stageRank — the Stage at a ladder rank (for the ?stage=
// preview override).
func ladderStage(rank int) Stage {
	switch rank {
	case 1:
		return StageLaunched
	case 2:
		return StageActivated
	case 3:
		return StageScaling
	default:
		return StageFormed
	}
}
