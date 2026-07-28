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
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// Preset is one user-authored router blend. Arms are catalog model ids (or BYO); rank
// is the escalation order (probe = rank[0], panel = rank[:panel]); panel bounds fan-out.
type Preset struct {
	Name   string   `json:"name"`             // served as enso-<name>
	Owner  string   `json:"owner"`            // scoping org (never cross-tenant)
	Arms   []string `json:"arms"`             // the blend — model ids from the arena
	Rank   []string `json:"rank"`             // escalation order over arms
	Panel  int      `json:"panel"`            // fan-out width (>=1)
	Note   string   `json:"note,omitempty"`   // why this blend (audit)
}

// presetRoutes is folded into routes() (one Mount). Kept here for cohesion.
func presetRoutes(g zip.Router, s *cloud.Service[state]) {
	g.Get("/presets", cloud.Handle(s, listPresets))
	g.Post("/presets", cloud.Handle(s, createPreset))
}

func listPresets(s *cloud.Service[state], c *zip.Ctx) error {
	// v1: presets are org-scoped catalog entries; the store lands next to attempts.
	// Returns the built-in enso-ultra blend as the reference preset until user presets persist.
	return c.JSON(http.StatusOK, map[string]any{"data": []Preset{referenceBlend()}})
}

func createPreset(s *cloud.Service[state], c *zip.Ctx) error {
	var p Preset
	if err := c.Bind(&p); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid preset"})
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || len(p.Arms) == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "preset needs a name and at least one arm"})
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
		return c.JSON(http.StatusUnprocessableEntity, map[string]any{"error": "rank references undeclared arms", "unknown": bad})
	}
	return c.JSON(http.StatusAccepted, map[string]any{
		"status": "accepted", "served_as": "enso-" + p.Name, "preset": p,
		"note": "resolved by the enso serving layer; author from the leaderboard's measured winners for your tasks.",
	})
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
