package guide

import (
	"fmt"
	"sort"
	"strings"
)

// JourneyStep is one checklist item — one quest of the launch journey. The struct
// tags are JSON, and sigs.k8s.io/yaml decodes YAML through them — so one tag set is
// the ONE contract for both the embedded YAML blueprint and a JSON PUT body (DRY: no
// parallel yaml tags).
//
// The qualifier is load-bearing, not decoration: the type NAME is the schema name in
// the published document, and that document is ONE flat namespace shared by every app
// in the fleet. Plain `Step` is already claimed there by apps/marketing (a step of a
// drip sequence — a different shape entirely), and openapi.Compose refuses one name with
// two shapes because a generated SDK would bind whichever it read last. Do not
// "simplify" this back to Step; that is the collision, not a tidier name. The wire is
// unaffected either way — the JSON keys live on the fields.
type JourneyStep struct {
	// ID is the stable slug the whole plane addresses this step by — the value in
	// `deps`, in `next`, in the progress rows, and in the URL of every step route.
	// Renaming it orphans an org's recorded progress for this step.
	ID string `json:"id"`
	// Section is the id of the phase this step groups under. A disabled section
	// takes its steps out of the journey with it.
	Section string `json:"section,omitempty"`
	// Title is the one-line quest as a person reads it in the checklist.
	Title string `json:"title"`
	// Detail is the juncture — what the Guide explains, or asks for, at this step.
	Detail string `json:"detail,omitempty"`

	// Dependencies are step ids that must be done/skipped before this step is
	// available. The wire key is `deps` (the blueprint contract); the Go field keeps
	// its descriptive name.
	Dependencies []string `json:"deps,omitempty"`

	// Enabled is the admin on/off lever. A NIL pointer reads as ENABLED (absence ==
	// on): a legacy/org curriculum that omits the field keeps every step, and only an
	// explicit `enabled: false` (an admin disable) drops a step from the journey. See
	// on() in blueprint.go and the Blueprint.Curriculum() projection.
	Enabled *bool `json:"enabled,omitempty"`

	// Signal, when set, names a machine detector (detect.go). When the detector
	// reports the org's real state present, the step auto-marks done.
	Signal string `json:"signal,omitempty"`

	// Tool, when set, names the MCP tool the Business AI runs for "do it for me".
	// A step with no tool can only be completed by a person; it is the field the
	// `automatable` flag on every projection of this step is derived from.
	Tool string `json:"tool,omitempty"`
	// Args are the tool's default arguments, merged under whatever the caller
	// passes at run time, so a step ships with the arguments that make it work.
	Args map[string]any `json:"args,omitempty"`
	// Draft, when set, is the prompt the embedded AI answers first; its output is
	// folded into one of Args before the tool runs, so the model writes the
	// content and the tool only delivers it.
	Draft string `json:"draft,omitempty"`
	// DraftInto names the argument the drafted text lands in. Empty means "brief".
	DraftInto string `json:"draftInto,omitempty"`
}

// Curriculum is the ENGINE's view: an ordered set of steps plus metadata. It only ever
// holds ENABLED steps — it is the projection of a Blueprint (Blueprint.Curriculum()),
// so the next-step/gating logic never has to reason about enablement. Order is
// authoring order and is the tiebreak the next-step logic walks.
type Curriculum struct {
	// Version identifies the authored playbook this journey was projected from, so
	// two orgs on different playbooks can be told apart. It is the blueprint's own
	// `version` string, not the store's numeric revision.
	Version string `json:"version"`
	// Title is the playbook's name as it heads the checklist.
	Title string `json:"title,omitempty"`
	// Steps are the enabled steps in authoring order. Order is the tiebreak the
	// next-step logic walks, so it is part of the contract rather than cosmetic.
	Steps []JourneyStep `json:"steps"`
}

// State is a step's per-org lifecycle state.
type State string

const (
	StateTodo       State = "todo"
	StateInProgress State = "in_progress"
	StateDone       State = "done"
	StateSkipped    State = "skipped"
)

// Bounds keep an org-custom curriculum / blueprint from amplifying the store or a
// response.
const (
	maxSteps       = 256
	maxDeps        = 32
	maxCurriculum  = 256 * 1024 // bytes of a per-org curriculum PUT body
	maxDraftOutput = 8 * 1024   // chars of AI-drafted content folded into a tool arg
)

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

// Validate enforces the schema invariants the engine relies on: at least one step;
// unique non-empty ids; dependencies that reference existing ids (no self-edge); a
// bounded shape; and — the load-bearing one — an ACYCLIC dependency graph, so
// next-step selection always terminates and can never deadlock on a cycle. Blueprint
// validation (blueprint.go) reuses this over BOTH the authored step graph and the
// projected ENABLED journey.
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

// stepByID returns the step with id, or (JourneyStep{}, false).
func (c Curriculum) stepByID(id string) (JourneyStep, bool) {
	for _, s := range c.Steps {
		if s.ID == id {
			return s, true
		}
	}
	return JourneyStep{}, false
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
