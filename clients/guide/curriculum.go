// Package guide mounts the Hanzo Cloud /v1/guide/* surface: the Business AI Guide,
// an interactive launch checklist every org completes on-site.
//
// It is three orthogonal things composed:
//
//   - a CHECKLIST ENGINE over a machine-readable curriculum (steps with id, title,
//     why, how-on-hanzo, done-criteria, dependencies). Per-org progress tracks a
//     state per step (todo|in_progress|done|skipped); next-step logic honours
//     dependencies; a step whose done-criterion maps to a real signal auto-marks
//     done when that signal is present (auto-detect).
//   - a BUSINESS AI AGENT: for a step bound to an MCP tool, "do it for me" drafts
//     the content with the embedded AI (deps.AI) and executes the tool through the
//     per-principal MCP plane (automations.InvokeTool) AS THE CALLER — so the
//     action can never exceed the caller's own authorization and is metered +
//     audited like any MCP call.
//   - a curriculum LOADER: a minimal built-in default (embedded default.yaml) so
//     /v1/guide works before the marketing repo's authored checklist.yaml lands; a
//     PUT of an org-custom or platform-updated curriculum replaces it cleanly.
//
// This file is the pure engine: the schema contract (Step / Curriculum), parsing,
// validation, and the dependency/next-step/auto-detect logic — all free functions
// over plain data, no I/O, so they are exhaustively unit-testable. Storage lives in
// store.go, detectors in detect.go, the agent in agent.go, and the HTTP surface in
// guide.go.
package guide

import (
	_ "embed"
	"fmt"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// Step is one checklist item. The struct tags are JSON, and sigs.k8s.io/yaml
// decodes YAML through them — so one tag set is the ONE contract for both the
// embedded YAML default and a JSON PUT body (DRY: no parallel yaml tags).
type Step struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Why          string   `json:"why"`
	How          string   `json:"how"`  // how-on-hanzo
	Done         string   `json:"done"` // human done-criteria
	Dependencies []string `json:"dependencies,omitempty"`

	// Signal, when set, names a machine detector (detect.go). When the detector
	// reports the org's real state present, the step auto-marks done.
	Signal string `json:"signal,omitempty"`

	// Tool, when set, is the "<connector>_<action>" MCP tool the Business AI runs
	// for "do it for me". Args are its default arguments; Draft is an optional AI
	// prompt whose output fills the DraftInto arg (default "brief").
	Tool      string         `json:"tool,omitempty"`
	Args      map[string]any `json:"args,omitempty"`
	Draft     string         `json:"draft,omitempty"`
	DraftInto string         `json:"draftInto,omitempty"`
}

// Curriculum is an ordered set of steps plus metadata. Order is authoring order and
// is the tiebreak the next-step logic walks.
type Curriculum struct {
	Version string `json:"version"`
	Title   string `json:"title,omitempty"`
	Steps   []Step `json:"steps"`
}

// State is a step's per-org lifecycle state.
type State string

const (
	StateTodo       State = "todo"
	StateInProgress State = "in_progress"
	StateDone       State = "done"
	StateSkipped    State = "skipped"
)

// Bounds keep an org-custom curriculum from amplifying the store or a response.
const (
	maxSteps       = 128
	maxDeps        = 32
	maxCurriculum  = 256 * 1024 // bytes of a PUT body
	maxDraftOutput = 8 * 1024   // chars of AI-drafted content folded into a tool arg
)

//go:embed default.yaml
var defaultYAML []byte

// defaultCurriculum is the built-in default, parsed once at init. A malformed or
// invalid embedded default is a BUILD-TIME fault (panic at package init), never a
// runtime surprise — the same discipline clients/automations uses for its catalog.
var defaultCurriculum = mustDefault()

func mustDefault() Curriculum {
	c, err := Parse(defaultYAML)
	if err != nil {
		panic("guide: built-in default.yaml invalid: " + err.Error())
	}
	return c
}

// validState reports whether s is a known step state.
func validState(s State) bool {
	switch s {
	case StateTodo, StateInProgress, StateDone, StateSkipped:
		return true
	default:
		return false
	}
}

// terminal reports whether a state satisfies a dependency and is excluded from
// next-step selection. done and skipped both satisfy a downstream dependency: a
// user who deliberately skips a step is telling the guide to treat it as resolved.
func terminal(s State) bool { return s == StateDone || s == StateSkipped }

// Parse decodes a curriculum from YAML or JSON (sigs.k8s.io/yaml accepts both) and
// validates it. A parse or validation failure returns an error; the caller keeps
// the previous curriculum (fail-closed — a bad PUT never corrupts the active one).
func Parse(raw []byte) (Curriculum, error) {
	var c Curriculum
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return Curriculum{}, fmt.Errorf("parse curriculum: %w", err)
	}
	if err := Validate(c); err != nil {
		return Curriculum{}, err
	}
	return c, nil
}

// Validate enforces the schema invariants the engine relies on: at least one step;
// unique non-empty ids; dependencies that reference existing ids (no self-edge); a
// bounded shape; and — the load-bearing one — an ACYCLIC dependency graph, so
// next-step selection always terminates and can never deadlock on a cycle.
func Validate(c Curriculum) error {
	if len(c.Steps) == 0 {
		return fmt.Errorf("curriculum has no steps")
	}
	if len(c.Steps) > maxSteps {
		return fmt.Errorf("curriculum has %d steps, max %d", len(c.Steps), maxSteps)
	}
	ids := make(map[string]bool, len(c.Steps))
	for _, s := range c.Steps {
		id := strings.TrimSpace(s.ID)
		if id == "" {
			return fmt.Errorf("step has an empty id")
		}
		if id != s.ID {
			return fmt.Errorf("step id %q has surrounding whitespace", s.ID)
		}
		if ids[id] {
			return fmt.Errorf("duplicate step id %q", id)
		}
		if strings.TrimSpace(s.Title) == "" {
			return fmt.Errorf("step %q has an empty title", id)
		}
		if len(s.Dependencies) > maxDeps {
			return fmt.Errorf("step %q has %d dependencies, max %d", id, len(s.Dependencies), maxDeps)
		}
		ids[id] = true
	}
	for _, s := range c.Steps {
		for _, d := range s.Dependencies {
			if d == s.ID {
				return fmt.Errorf("step %q depends on itself", s.ID)
			}
			if !ids[d] {
				return fmt.Errorf("step %q depends on unknown step %q", s.ID, d)
			}
		}
	}
	if cycle := findCycle(c); cycle != "" {
		return fmt.Errorf("dependency cycle involving step %q", cycle)
	}
	return nil
}

// findCycle returns the id of a step on a dependency cycle, or "" if the graph is
// acyclic. Depth-first three-colour walk.
func findCycle(c Curriculum) string {
	const (
		white = 0 // unvisited
		grey  = 1 // on the current stack
		black = 2 // done
	)
	deps := make(map[string][]string, len(c.Steps))
	for _, s := range c.Steps {
		deps[s.ID] = s.Dependencies
	}
	color := make(map[string]int, len(c.Steps))
	var visit func(id string) string
	visit = func(id string) string {
		color[id] = grey
		for _, d := range deps[id] {
			switch color[d] {
			case grey:
				return d
			case white:
				if bad := visit(d); bad != "" {
					return bad
				}
			}
		}
		color[id] = black
		return ""
	}
	for _, s := range c.Steps {
		if color[s.ID] == white {
			if bad := visit(s.ID); bad != "" {
				return bad
			}
		}
	}
	return ""
}

// stepByID returns the step with id, or (Step{}, false).
func (c Curriculum) stepByID(id string) (Step, bool) {
	for _, s := range c.Steps {
		if s.ID == id {
			return s, true
		}
	}
	return Step{}, false
}

// stateOf returns the recorded state for id, defaulting to todo.
func stateOf(states map[string]State, id string) State {
	if s, ok := states[id]; ok && validState(s) {
		return s
	}
	return StateTodo
}

// BlockedBy returns the step's dependencies that are NOT yet satisfied (not
// done/skipped). Empty slice means the step is available.
func (c Curriculum) BlockedBy(states map[string]State, id string) []string {
	s, ok := c.stepByID(id)
	if !ok {
		return nil
	}
	var blocked []string
	for _, d := range s.Dependencies {
		if !terminal(stateOf(states, d)) {
			blocked = append(blocked, d)
		}
	}
	sort.Strings(blocked)
	return blocked
}

// Available reports whether every dependency of id is satisfied.
func (c Curriculum) Available(states map[string]State, id string) bool {
	return len(c.BlockedBy(states, id)) == 0
}

// Next returns the id of the step the org should tackle next: the FIRST step in
// authoring order that is neither done nor skipped and whose dependencies are all
// satisfied. It returns "" when nothing is actionable (all steps terminal, or the
// only remaining steps are blocked — which, on an acyclic graph, means their
// blockers are themselves the actionable next steps and get picked first).
func (c Curriculum) Next(states map[string]State) string {
	for _, s := range c.Steps {
		if terminal(stateOf(states, s.ID)) {
			continue
		}
		if c.Available(states, s.ID) {
			return s.ID
		}
	}
	return ""
}

// Counts returns how many steps are done (skipped counts as resolved) and the
// total, plus an integer percent complete (0..100).
func (c Curriculum) Counts(states map[string]State) (done, total, percent int) {
	total = len(c.Steps)
	for _, s := range c.Steps {
		if terminal(stateOf(states, s.ID)) {
			done++
		}
	}
	if total > 0 {
		percent = done * 100 / total
	}
	return done, total, percent
}
