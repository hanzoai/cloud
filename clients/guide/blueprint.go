package guide

import (
	_ "embed"
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"
)

// blueprint.go is the full Guide™ playbook SCHEMA — the superset the checklist engine
// runs on. A Blueprint is the authored DATA: ordered phase SECTIONS, flat DAG-linked
// STEPS, a filterable STRATEGIES corpus, and reusable TEMPLATES — every item carrying
// an `enabled` lever an admin flips. It decomposes into four decomplected concerns,
// each its own file:
//
//	seed     data      → the embedded fixture below, seeded into the DB (blueprint_store.go)
//	resolve  override→brand→fixture → resolve() in guide.go
//	author   SuperAdmin CRUD        → admin.go
//	filter   corpus by profile      → strategies.go
//
// This file owns the schema + two pure operations: Parse (fail-closed decode+validate)
// and Curriculum() (the projection that drops disabled items and hands the engine a
// clean ENABLED-only journey).

// Section is one ordered phase of the journey. Steps group under a section by id; a
// DISABLED section drops all of its steps from the journey (the precedence rule).
type Section struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Detail  string `json:"detail,omitempty"`
	Order   int    `json:"order,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
}

// Strategy is one tactic in the recommendation corpus (strategies.go filters it by
// category/workload and by the org's observed profile via the tag join). `tags` are
// signal predicates — `stage:<research|launched|activated|scaling>` and
// `has:<capability>` — that the corpus join maps onto the observe layer's vocabulary.
type Strategy struct {
	ID       string   `json:"id"`
	Category string   `json:"category"`
	Workload string   `json:"workload,omitempty"`
	Action   string   `json:"action"`
	Tags     []string `json:"tags,omitempty"`
	Enabled  *bool    `json:"enabled,omitempty"`
}

// Template is a reusable prompt/snippet a step references. `body` may carry
// {placeholder} tokens for client-specific bits ({client_name}, {domain}, {product}).
type Template struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	Enabled *bool  `json:"enabled,omitempty"`
}

// Blueprint is the full authored playbook. `brand` is the white-label key (""==the
// global default); `enabled` at the root is the whole-blueprint lever (a disabled
// blueprint is skipped by resolution, falling through to the next tier). The four
// arrays are the content. The engine runs on Curriculum() — the enabled projection.
type Blueprint struct {
	Version    string     `json:"version"`
	Brand      string     `json:"brand,omitempty"`
	Title      string     `json:"title,omitempty"`
	Enabled    *bool      `json:"enabled,omitempty"`
	Sections   []Section  `json:"sections,omitempty"`
	Steps      []Step     `json:"steps"`
	Strategies []Strategy `json:"strategies,omitempty"`
	Templates  []Template `json:"templates,omitempty"`
}

// Bounds on the corpus/collections so an org-custom or admin-authored blueprint can't
// amplify the store or a response. maxSteps/maxDeps live in curriculum.go (the engine).
const (
	maxSections   = 128
	maxStrategies = 2048
	maxTemplates  = 512
	maxBlueprint  = 4 << 20 // 4 MiB — a full blueprint PUT/seed body cap
)

// on reports whether an enable pointer reads as ON. Absence (nil) is ON: a legacy or
// org curriculum that omits `enabled` keeps every item, and only an explicit false
// (an admin disable) turns it off. This is the ONE place the default-true rule lives.
func on(p *bool) bool { return p == nil || *p }

// boolPtr returns a *bool for setting an explicit enable state (admin enable/disable).
func boolPtr(b bool) *bool { return &b }

// baseBlueprintYAML is the embedded seed — the historic Guide™ playbook (12 sections ·
// 67 steps · 114 strategies · 6 templates). It is a BYTE-IDENTICAL synced copy of the
// GitOps origin at ~/work/hanzo/universe/cloud/fixtures/guide/base.yaml (the two stay
// in sync; edit the origin, copy it here — no transform). At runtime the DB is
// authoritative; this embed is only the seed-if-absent source + the ultimate fail-safe
// when the DB is unreachable.
//
//go:embed base.yaml
var baseBlueprintYAML []byte

// defaultBlueprint is the embedded base blueprint, parsed once at init. A malformed or
// invalid embed is a BUILD-TIME fault (panic at package init), never a runtime
// surprise — the same discipline clients/automations uses for its catalog.
var defaultBlueprint = mustBlueprint(baseBlueprintYAML)

func mustBlueprint(raw []byte) Blueprint {
	b, err := Parse(raw)
	if err != nil {
		panic("guide: embedded base.yaml invalid: " + err.Error())
	}
	return b
}

// Parse decodes a blueprint from YAML or JSON (sigs.k8s.io/yaml accepts both) and
// validates it. A parse or validation failure returns an error; the caller keeps the
// previous blueprint (fail-closed — a bad PUT or seed never corrupts the active one).
func Parse(raw []byte) (Blueprint, error) {
	var b Blueprint
	if err := yaml.Unmarshal(raw, &b); err != nil {
		return Blueprint{}, fmt.Errorf("parse blueprint: %w", err)
	}
	if err := b.Validate(); err != nil {
		return Blueprint{}, err
	}
	return b, nil
}

// Validate enforces the blueprint invariants, fail-closed:
//
//   - the AUTHORED step graph is a valid engine curriculum (>=1 step, unique ids, no
//     self/dangling deps, acyclic, bounded) — reuses Validate(Curriculum) over ALL
//     steps, so a dep may reference a disabled step (it exists) but never a missing one;
//   - sections/strategies/templates have unique non-empty ids, bounded counts, and the
//     required content fields; every step's section (if named) refers to a real section;
//   - the projected ENABLED journey is ALSO a valid engine curriculum — DAG-acyclic +
//     no-dangling over the enabled steps (the explicit requirement). Because the
//     projection filters edges to enabled steps, this can never dangle; the check makes
//     the guarantee real and would catch a projection regression. An all-disabled
//     blueprint projects to an empty journey, which is allowed (resolution skips it).
func (b Blueprint) Validate() error {
	// 1. The authored step graph (all steps, enabled or not).
	if err := Validate(Curriculum{Version: b.Version, Steps: b.Steps}); err != nil {
		return err
	}
	// 2. Sections: unique ids, bounds.
	if len(b.Sections) > maxSections {
		return fmt.Errorf("blueprint has %d sections, max %d", len(b.Sections), maxSections)
	}
	sectionIDs := make(map[string]bool, len(b.Sections))
	for _, sec := range b.Sections {
		id := strings.TrimSpace(sec.ID)
		if id == "" {
			return fmt.Errorf("section has an empty id")
		}
		if sectionIDs[id] {
			return fmt.Errorf("duplicate section id %q", id)
		}
		if strings.TrimSpace(sec.Title) == "" {
			return fmt.Errorf("section %q has an empty title", id)
		}
		sectionIDs[id] = true
	}
	// Every step that names a section must reference one that exists.
	for _, s := range b.Steps {
		if s.Section != "" && !sectionIDs[s.Section] {
			return fmt.Errorf("step %q references unknown section %q", s.ID, s.Section)
		}
	}
	// 3. Strategies: unique ids, required action, bounds.
	if len(b.Strategies) > maxStrategies {
		return fmt.Errorf("blueprint has %d strategies, max %d", len(b.Strategies), maxStrategies)
	}
	stratIDs := make(map[string]bool, len(b.Strategies))
	for _, st := range b.Strategies {
		id := strings.TrimSpace(st.ID)
		if id == "" {
			return fmt.Errorf("strategy has an empty id")
		}
		if stratIDs[id] {
			return fmt.Errorf("duplicate strategy id %q", id)
		}
		if strings.TrimSpace(st.Category) == "" {
			return fmt.Errorf("strategy %q has an empty category", id)
		}
		if strings.TrimSpace(st.Action) == "" {
			return fmt.Errorf("strategy %q has an empty action", id)
		}
		stratIDs[id] = true
	}
	// 4. Templates: unique ids, required body, bounds.
	if len(b.Templates) > maxTemplates {
		return fmt.Errorf("blueprint has %d templates, max %d", len(b.Templates), maxTemplates)
	}
	tmplIDs := make(map[string]bool, len(b.Templates))
	for _, t := range b.Templates {
		id := strings.TrimSpace(t.ID)
		if id == "" {
			return fmt.Errorf("template has an empty id")
		}
		if tmplIDs[id] {
			return fmt.Errorf("duplicate template id %q", id)
		}
		if strings.TrimSpace(t.Body) == "" {
			return fmt.Errorf("template %q has an empty body", id)
		}
		tmplIDs[id] = true
	}
	// 5. The projected ENABLED journey (DAG-acyclic + no-dangling over enabled steps).
	if eff := b.Curriculum(); len(eff.Steps) > 0 {
		if err := Validate(eff); err != nil {
			return fmt.Errorf("enabled journey invalid: %w", err)
		}
	}
	return nil
}

// Curriculum projects the blueprint onto the engine's Curriculum: the ENABLED journey.
// The precedence rule (reconciliation c): a DISABLED section drops ALL its steps; a
// DISABLED step drops out; and every surviving step's dependencies are filtered to
// enabled steps only — a dependency on a disabled/absent step is DROPPED, not dangling
// (the disabled step is treated as resolved, so it never blocks). The result is
// acyclic and dangling-free by construction, so the engine runs it unchanged.
func (b Blueprint) Curriculum() Curriculum {
	disabledSection := make(map[string]bool, len(b.Sections))
	for _, sec := range b.Sections {
		if !on(sec.Enabled) {
			disabledSection[sec.ID] = true
		}
	}
	enabled := make(map[string]bool, len(b.Steps))
	for _, s := range b.Steps {
		if on(s.Enabled) && !(s.Section != "" && disabledSection[s.Section]) {
			enabled[s.ID] = true
		}
	}
	steps := make([]Step, 0, len(b.Steps))
	for _, s := range b.Steps {
		if !enabled[s.ID] {
			continue
		}
		cp := s
		if len(s.Dependencies) > 0 {
			deps := make([]string, 0, len(s.Dependencies))
			for _, d := range s.Dependencies {
				if enabled[d] {
					deps = append(deps, d)
				}
			}
			cp.Dependencies = deps
		}
		steps = append(steps, cp)
	}
	return Curriculum{Version: b.Version, Title: b.Title, Steps: steps}
}

// enabledStrategies returns the corpus's ENABLED strategies (the filterable library —
// strategies.go narrows this by category/workload and the org's profile).
func (b Blueprint) enabledStrategies() []Strategy {
	out := make([]Strategy, 0, len(b.Strategies))
	for _, st := range b.Strategies {
		if on(st.Enabled) {
			out = append(out, st)
		}
	}
	return out
}
