package campaign

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// experiment.go composes the platform EXPERIMENT primitive (clients/experiments)
// for creative A/B — it does NOT reinvent assignment or measurement. A campaign
// creative A/B is exactly an experiment whose variant = a creative (Content[i],
// tagged utm_content) and whose metric = the campaign result read from the ONE
// analytics plane. The experiments primitive owns the bucketing (experiments.Assign)
// and the pull-model analysis (experiments.Analyze reads analytics×flags×research
// ITSELF); campaign only COMPOSES those two seams, wired at the composition root.
//
// Both seams are nil-safe: with no experiment primitive wired, a campaign runs a
// SINGLE creative (Content[0]) and reports no A/B analysis — the honest degrade.
// Nothing here fabricates a variant or a result, and nothing re-measures what the
// experiments plane already measures (no duplicate evidence store).

// AssignFunc resolves the creative variant a launch runs — it composes
// experiments.Assign (the root supplies the project and extracts the variant key
// from the flags.Assignment). campaign maps the returned key to utm_content. An
// error or empty result means "no assignment" → single creative.
type AssignFunc func(ctx context.Context, org, experimentID, subject string) (variant string, err error)

// AnalyzeFunc returns an experiment's current analysis JSON — it composes
// experiments.Analyze (pull-model: it reads the metric from analytics itself). The
// result is opaque JSON so campaign stays decoupled from the experiments analysis
// type; the metrics endpoint embeds it verbatim.
type AnalyzeFunc func(ctx context.Context, org, experimentID string, start, end time.Time) (json.RawMessage, error)

var (
	assignSeam  AssignFunc
	analyzeSeam AnalyzeFunc
)

// SetExperiment wires the experiments primitive into the campaign plane. Called
// once at the composition root (apps/wire_seams.go) with the experiments.Assign +
// experiments.Analyze adapters. Passing nils clears the seam (single-creative mode).
func SetExperiment(assign AssignFunc, analyze AnalyzeFunc) {
	assignSeam = assign
	analyzeSeam = analyze
}

// ExperimentKey is the stable experiment identity for a campaign's creative A/B. A
// campaign opts into A/B by creating an experiment with THIS id (variants =
// creatives) on the experiments plane; if none exists, Assign fails and the
// campaign runs a single creative — no coupling, no auto-creation.
func ExperimentKey(campaignID string) string { return "campaign:" + campaignID }

// assignVariant returns the creative variant this launch should run. It composes
// experiments.Assign (subject = the campaign, so a campaign's variant is stable
// until the experiment decides a winner). With more than one creative AND an
// assignment seam wired it asks the primitive; otherwise "" — Content[0], the
// honest single-creative default. Fail-soft: an assignment error never blocks a
// launch (e.g. the org never set up the experiment).
func assignVariant(ctx context.Context, org string, camp Campaign) string {
	if assignSeam == nil || len(camp.Content) <= 1 {
		return ""
	}
	variant, err := assignSeam(ctx, org, ExperimentKey(camp.ID), camp.ID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(variant)
}

// analyzeExperiment returns the campaign's A/B analysis JSON, or nil when the
// campaign runs a single creative, no primitive is wired, or the experiment has no
// analysis yet. Best-effort — a metrics read never fails on the A/B lens.
func analyzeExperiment(ctx context.Context, org string, camp Campaign, start, end time.Time) json.RawMessage {
	if analyzeSeam == nil || len(camp.Content) <= 1 {
		return nil
	}
	raw, err := analyzeSeam(ctx, org, ExperimentKey(camp.ID), start, end)
	if err != nil {
		return nil
	}
	return raw
}
