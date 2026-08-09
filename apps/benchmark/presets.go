package benchmark

// Presets: "design your own router blend." A preset is a named arm-set + rank — the
// same shape the enso tiers take in the family catalog, but user-authored and served
// as enso-<name>. The catalog is already family-as-data; a preset is a scoped catalog
// entry a caller composes from the arena's measured leaderboard (pick the arms that
// win YOUR tasks). What a Hanzo-served tier is composed of stays ours; a preset
// publishes only what its author put in it.
//
// This surface stores/serves preset DEFINITIONS (the blend); the zen/enso serving layer
// resolves enso-<name> against them. Provenance-first: a preset records WHICH measured
// evidence justified each arm (leaderboard row at authoring time), so a blend is
// auditable, not vibes.

import (
	"context"
	"net/http"
	"strings"

	"github.com/zap-proto/zip"
)

// Preset is one user-authored router blend. Arms are catalog model ids (or BYO); rank
// is the escalation order (probe = rank[0], panel = rank[:panel]); panel bounds fan-out.
type Preset struct {
	Name  string   `json:"name"`           // served as enso-<name>
	Owner string   `json:"owner"`          // scoping org (never cross-tenant)
	Arms  []string `json:"arms"`           // the blend — model ids from the arena
	Rank  []string `json:"rank"`           // escalation order over arms
	Panel int      `json:"panel"`          // fan-out width (>=1)
	Note  string   `json:"note,omitempty"` // why this blend (audit)
}

// presetList is the answer to a preset read.
type presetList struct {
	// Data is the blends available to compose from.
	Data []Preset `json:"data"`
}

// presetAccepted is the answer to a composed blend: what was checked, and what it
// would be served as.
type presetAccepted struct {
	// Status is "accepted": the blend is well-formed, not that it is now served.
	Status string `json:"status"`
	// ServedAs is the model id the serving layer would resolve this blend under.
	ServedAs string `json:"served_as"`
	// Preset is the blend with its defaults filled in.
	Preset Preset `json:"preset"`
	// Note explains what acceptance does and does not promise.
	Note string `json:"note"`
}

// presets are the router blends available to compose from — a named set of model
// arms, the rank they escalate through and the panel width that bounds fan-out —
// each served by the model layer as enso-<name>.
//
// Today it answers exactly one row, the reference blend: a worked example written
// in models we name, published as an example of the FORM. It is deliberately not
// the composition of a Hanzo-served tier — the tier name exists to abstract that —
// so fork it and swap arms by what the leaderboard measures on your own tasks
// rather than reading it as a disclosure.
func (o ops) presets(ctx context.Context, _ *noIn) (*presetList, error) {
	// v1: presets are org-scoped catalog entries; the store lands next to attempts.
	// Returns the built-in enso-ultra blend as the reference preset until user presets persist.
	return &presetList{Data: []Preset{referenceBlend()}}, nil
}

// compose validates a router blend — its name, its arms, the rank they escalate
// through and the panel fan-out width — and answers 202 with the preset and the
// enso-<name> it would be served as.
//
// It VALIDATES AND ECHOES: the definition is not persisted yet, so a preset
// accepted here is not one the model layer will resolve. Treat the response as a
// check on the blend, not a promise to serve it.
//
// Defaults fill the shape rather than refusing it: an omitted rank becomes the arms
// in declared order and a panel below 1 becomes 1. The one real invariant is that
// rank may only name arms the blend declares — the same rule the model catalog
// enforces — and a rank naming anything else is a 422 listing exactly which entries
// were undeclared. A blend with no name or no arms is a 400.
func (o ops) compose(ctx context.Context, in *Preset) (*presetAccepted, error) {
	p := *in
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || len(p.Arms) == 0 {
		return nil, zip.ErrBadRequest("preset needs a name and at least one arm")
	}
	if p.Panel < 1 {
		p.Panel = 1
	}
	if len(p.Rank) == 0 {
		p.Rank = p.Arms
	}
	// Validate rank references resolve to declared arms (same invariant the catalog enforces).
	arms := map[string]bool{}
	for _, a := range p.Arms {
		arms[a] = true
	}
	var bad []string
	for _, r := range p.Rank {
		if !arms[r] {
			bad = append(bad, r)
		}
	}
	if len(bad) > 0 {
		return nil, zip.Errorf(http.StatusUnprocessableEntity,
			"rank references undeclared arms: %s", strings.Join(bad, ", "))
	}
	return &presetAccepted{
		Status: "accepted", ServedAs: "enso-" + p.Name, Preset: p,
		Note: "resolved by the enso serving layer; author from the leaderboard's measured winners for your tasks.",
	}, nil
}

// referenceBlend is the worked example a user forks — a blend written in models
// we name: Hanzo's own, plus the frontier models resold under their own brand.
//
// It is deliberately NOT the composition of enso-ultra. Publishing which bases
// serve an enso tier is the Zen mapping, and the enso name exists precisely to
// abstract it; a shipped preset is an example of the FORM, not a disclosure.
func referenceBlend() Preset {
	return Preset{
		Name:  "ultra",
		Owner: "hanzo",
		Arms:  []string{"enso-ultra", "zen5-pro", "x-ai/grok-4.5", "anthropic-claude-opus-4.8", "google/gemini-3.1-pro", "openai-gpt-5.6-sol"},
		Rank:  []string{"enso-ultra", "x-ai/grok-4.5", "anthropic-claude-opus-4.8"},
		Panel: 3,
		Note:  "example blend; fork it and swap arms by your measured leaderboard.",
	}
}
