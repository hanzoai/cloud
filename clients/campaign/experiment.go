package campaign

import (
	"context"
	"strings"
)

// experiment.go composes the platform EXPERIMENT primitive for creative A/B — it
// does NOT reinvent assignment or evidence. A campaign creative A/B is exactly an
// experiment whose variant = a creative (Content[i], tagged as utm_content) and
// whose metric = the campaign result read from the ONE analytics plane
// (metrics.go). The experiment primitive itself (flags-based assignment +
// research-backed evidence, metric-source = analytics) is owned elsewhere; this
// file is the seam campaign calls, wired at the composition root.
//
// Both seams are nil-safe: with no experiment primitive wired, a campaign runs a
// SINGLE creative (Content[0]) and reads whole-campaign metrics — the honest
// degrade, the same fail-soft the marketing drip uses while it waits for the
// tasks engine. Nothing here fabricates a variant or an evidence write.

// AssignFunc resolves the creative variant a launch runs. It composes the
// experiment/flags assignment: given the experiment key and a stable subject, it
// returns the variant id (which campaign maps to a creative and tags as
// utm_content). An error or empty result means "no assignment" → single creative.
type AssignFunc func(ctx context.Context, org, experimentKey, subject string) (variant string, err error)

// EvidenceFunc records a per-variant metric read (from analytics) as experiment
// evidence — the input the primitive uses to pick and promote a winner (via
// flags). Campaign supplies the numbers; the primitive owns the decision.
type EvidenceFunc func(ctx context.Context, org, experimentKey string, ev Evidence) error

// Evidence is one variant's measured result, sourced entirely from the analytics
// plane + the channel connector's reported spend (metrics.go). It is what
// campaign hands the experiment primitive; the primitive never re-queries.
type Evidence struct {
	Variant     string
	Impressions int64
	Clicks      int64
	Conversions int64
	Revenue     float64
	SpendCents  int64
}

var (
	assignSeam   AssignFunc
	evidenceSeam EvidenceFunc
)

// SetExperiment wires the experiment primitive into the campaign plane. Called
// once at the composition root (apps/wire_seams.go) with the flags-assignment +
// evidence adapters. Passing nils clears the seam (single-creative mode).
func SetExperiment(assign AssignFunc, evidence EvidenceFunc) {
	assignSeam = assign
	evidenceSeam = evidence
}

// ExperimentKey is the stable experiment identity for a campaign's creative A/B.
func ExperimentKey(campaignID string) string { return "campaign:" + campaignID }

// assignVariant returns the creative variant this launch should run. It composes
// the experiment primitive: with more than one creative AND an assignment seam
// wired, it asks the primitive (subject = the campaign, so a campaign's variant
// is stable until the primitive re-weights toward the winner). Otherwise "" —
// the campaign runs Content[0], the honest single-creative default.
func assignVariant(ctx context.Context, org string, camp Campaign) string {
	if assignSeam == nil || len(camp.Content) <= 1 {
		return ""
	}
	variant, err := assignSeam(ctx, org, ExperimentKey(camp.ID), camp.ID)
	if err != nil {
		return "" // fail-soft: an assignment failure never blocks the launch
	}
	return strings.TrimSpace(variant)
}

// recordEvidence feeds a variant's measured result back to the experiment
// primitive. Nil-safe: with no primitive wired it is a no-op (the metrics read
// still returns whole-campaign numbers). Best-effort — an evidence write failure
// never fails a metrics read.
func recordEvidence(ctx context.Context, org, campaignID string, ev Evidence) {
	if evidenceSeam == nil || ev.Variant == "" {
		return
	}
	_ = evidenceSeam(ctx, org, ExperimentKey(campaignID), ev)
}
