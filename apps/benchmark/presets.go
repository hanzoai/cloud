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
	// Name is the blend's name; it is served as enso-<name>.
	Name string `json:"name"`
	// Owner is the scoping org (never cross-tenant).
	Owner string `json:"owner"`
	// Arms is the blend — model ids from the arena.
	Arms []string `json:"arms"`
	// Rank is the escalation order over arms; empty means the arms order.
	Rank []string `json:"rank"`
	// Panel is the fan-out width (>= 1); below 1 it is raised to 1.
	Panel int `json:"panel"`
	// Note records why this blend was authored (audit).
	Note string `json:"note,omitempty"`
}

// PresetList is the org's preset catalog.
type PresetList struct {
	// Data is every preset visible to the caller.
	Data []Preset `json:"data"`
}

// PresetAccepted acknowledges an admitted preset.
type PresetAccepted struct {
	// Status is "accepted".
	Status string `json:"status"`
	// ServedAs is the model id the enso serving layer resolves this blend under.
	ServedAs string `json:"served_as"`
	// Preset is the normalized preset (panel raised to >= 1, rank defaulted to arms).
	Preset Preset `json:"preset"`
	// Note states where the blend is resolved.
	Note string `json:"note"`
}

// presetRoutes is folded into routes() (one Mount). Kept here for cohesion.
func presetRoutes(z *zip.App, o ops) {
	zip.Get(z, "/v1/benchmark/presets", o.presets, opID("benchmarkPresets"))
	zip.Post(z, "/v1/benchmark/presets", o.createPreset, opID("benchmarkCreatePreset"), zip.WithStatus(http.StatusAccepted))
}

// presets lists the router blends available to the caller. Today that is the single
// built-in reference blend — user presets do not persist yet — so it is the worked
// example to fork, not an enumeration of anyone's saved blends.
//
// Response: {"data": [{"name": "ultra", "owner": "hanzo", "arms": ["enso-ultra", "zen5-pro"], "rank": ["enso-ultra"], "panel": 3, "note": "example blend; fork it and swap arms by your measured leaderboard."}]}
func (o ops) presets(ctx context.Context, _ *struct{}) (*PresetList, error) {
	// v1: presets are org-scoped catalog entries; the store lands next to attempts.
	// Returns the built-in enso-ultra blend as the reference preset until user presets persist.
	return &PresetList{Data: []Preset{referenceBlend()}}, nil
}

// createPreset validates a router blend and answers 202 with its enso-<name> id. Every
// rank entry must resolve to a declared arm; panel below 1 is raised to 1 and an empty
// rank defaults to the arms order. Nothing is persisted yet — the enso serving layer
// resolves the blend.
//
// Example: {"name": "mine", "arms": ["zen5-pro", "grok-4.5"], "rank": ["zen5-pro"], "panel": 2, "note": "my measured winners"}
func (o ops) createPreset(ctx context.Context, in *Preset) (*PresetAccepted, error) {
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
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "rank references undeclared arms: %s", strings.Join(bad, ", "))
	}
	return &PresetAccepted{
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
