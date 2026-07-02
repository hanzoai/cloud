package prompt

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// catalog.json is the Hanzo starter prompt library (source of truth:
// hanzoai/prompts), vendored here so the unified `cloud` binary ships it with
// no external dependency. An org gets these seeded the first time it opens
// Prompts and has none of its own — a blank registry is honest but unhelpful,
// a pre-seeded one lets users build from real starters immediately.
//
//go:embed catalog.json
var catalogJSON []byte

// seedPrompt is one catalog entry — the stored subset of a hanzoai/prompts file.
type seedPrompt struct {
	Name   string          `json:"name"`
	Type   string          `json:"type"`
	Prompt string          `json:"prompt"`
	Tags   []string        `json:"tags"`
	Labels []string        `json:"labels"`
	Config json.RawMessage `json:"config,omitempty"`
}

// catalog decodes the embedded starter library once.
func catalog() ([]seedPrompt, error) {
	var out []seedPrompt
	if err := json.Unmarshal(catalogJSON, &out); err != nil {
		return nil, fmt.Errorf("prompts: decode embedded catalog: %w", err)
	}
	return out, nil
}

// SeedIfEmpty inserts the starter catalog into org as version 1 of each prompt,
// but only when the org has zero prompts. Idempotent: a second call is a no-op,
// and once a user has created anything the catalog is never re-imposed. Returns
// the number of prompts seeded.
func (s *Store) SeedIfEmpty(org string) (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM prompts WHERE org = ?`, org).Scan(&n); err != nil {
		return 0, err
	}
	if n > 0 {
		return 0, nil
	}
	cat, err := catalog()
	if err != nil {
		return 0, err
	}
	seeded := 0
	for _, p := range cat {
		if p.Name == "" {
			continue
		}
		if _, err := s.Create(org, PromptVersion{
			Name:   p.Name,
			Type:   p.Type,
			Prompt: p.Prompt,
			Tags:   p.Tags,
			Labels: p.Labels,
			Config: p.Config,
		}); err != nil {
			return seeded, fmt.Errorf("prompts: seed %q: %w", p.Name, err)
		}
		seeded++
	}
	return seeded, nil
}
