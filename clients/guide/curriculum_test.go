package guide

import (
	"strings"
	"testing"
)

// TestDefaultCurriculumValid proves the embedded default parses and satisfies every
// schema invariant — the same check package init enforces, made explicit.
func TestDefaultCurriculumValid(t *testing.T) {
	c := defaultCurriculum
	if len(c.Steps) < 5 || len(c.Steps) > 8 {
		t.Fatalf("default should have 5-8 steps, got %d", len(c.Steps))
	}
	if err := Validate(c); err != nil {
		t.Fatalf("default invalid: %v", err)
	}
	// The launch capstone must depend on the funnel steps.
	launch, ok := c.stepByID("launch")
	if !ok {
		t.Fatal("default missing launch step")
	}
	if len(launch.Dependencies) == 0 {
		t.Fatal("launch must depend on earlier steps")
	}
}

func TestValidateRejectsBadCurricula(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"no steps", `version: v1`, "no steps"},
		{"empty id", "version: v1\nsteps:\n- title: X", "empty id"},
		{"dup id", "version: v1\nsteps:\n- {id: a, title: A}\n- {id: a, title: A2}", "duplicate step id"},
		{"unknown dep", "version: v1\nsteps:\n- {id: a, title: A, dependencies: [ghost]}", "unknown step"},
		{"self dep", "version: v1\nsteps:\n- {id: a, title: A, dependencies: [a]}", "depends on itself"},
		{"cycle", "version: v1\nsteps:\n- {id: a, title: A, dependencies: [b]}\n- {id: b, title: B, dependencies: [a]}", "cycle"},
		{"empty title", "version: v1\nsteps:\n- {id: a}", "empty title"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

// TestParseAcceptsJSON proves the ONE contract: a JSON body parses identically to
// YAML (sigs.k8s.io/yaml through the json tags).
func TestParseAcceptsJSON(t *testing.T) {
	c, err := Parse([]byte(`{"version":"v1","steps":[{"id":"a","title":"A"},{"id":"b","title":"B","dependencies":["a"]}]}`))
	if err != nil {
		t.Fatalf("parse json: %v", err)
	}
	if len(c.Steps) != 2 || c.Steps[1].Dependencies[0] != "a" {
		t.Fatalf("json not parsed into the same shape: %+v", c)
	}
}

// linear is a 3-step chain a → b → c for the transition/gating tests.
var linear = Curriculum{Version: "t", Steps: []Step{
	{ID: "a", Title: "A"},
	{ID: "b", Title: "B", Dependencies: []string{"a"}},
	{ID: "c", Title: "C", Dependencies: []string{"b"}},
}}

// TestNextHonoursDependencies: Next walks authoring order and returns the first
// step whose dependencies are satisfied, gating downstream steps behind blockers.
func TestNextHonoursDependencies(t *testing.T) {
	states := map[string]State{}
	if got := linear.Next(states); got != "a" {
		t.Fatalf("fresh: next want a, got %q", got)
	}
	states["a"] = StateDone
	if got := linear.Next(states); got != "b" {
		t.Fatalf("after a done: next want b, got %q", got)
	}
	// b in progress is NOT terminal → still the next actionable step.
	states["b"] = StateInProgress
	if got := linear.Next(states); got != "b" {
		t.Fatalf("b in-progress: next want b, got %q", got)
	}
	states["b"] = StateSkipped // skip satisfies the dependency
	if got := linear.Next(states); got != "c" {
		t.Fatalf("after b skipped: next want c, got %q", got)
	}
	states["c"] = StateDone
	if got := linear.Next(states); got != "" {
		t.Fatalf("all resolved: next want empty, got %q", got)
	}
}

// TestDependencyGating: a step is available only when every dependency is
// done/skipped, and BlockedBy names exactly the unmet ones.
func TestDependencyGating(t *testing.T) {
	states := map[string]State{}
	if linear.Available(states, "b") {
		t.Fatal("b must be blocked while a is todo")
	}
	if blocked := linear.BlockedBy(states, "b"); len(blocked) != 1 || blocked[0] != "a" {
		t.Fatalf("b blockedBy want [a], got %v", blocked)
	}
	if !linear.Available(states, "a") {
		t.Fatal("a has no deps and must be available")
	}
	states["a"] = StateDone
	if !linear.Available(states, "b") {
		t.Fatal("b must be available once a is done")
	}
	// c is still blocked by b (in progress does NOT satisfy).
	states["b"] = StateInProgress
	if linear.Available(states, "c") {
		t.Fatal("c must stay blocked while b is only in-progress")
	}
	if blocked := linear.BlockedBy(states, "c"); len(blocked) != 1 || blocked[0] != "b" {
		t.Fatalf("c blockedBy want [b], got %v", blocked)
	}
}

// TestMultiDependencyBlockedByAll: a step with several deps lists every unmet one,
// sorted, and clears them as they resolve.
func TestMultiDependencyBlockedByAll(t *testing.T) {
	c := Curriculum{Steps: []Step{
		{ID: "x", Title: "X"},
		{ID: "y", Title: "Y"},
		{ID: "z", Title: "Z", Dependencies: []string{"y", "x"}},
	}}
	states := map[string]State{}
	if blocked := c.BlockedBy(states, "z"); len(blocked) != 2 || blocked[0] != "x" || blocked[1] != "y" {
		t.Fatalf("z blockedBy want sorted [x y], got %v", blocked)
	}
	states["x"] = StateDone
	if blocked := c.BlockedBy(states, "z"); len(blocked) != 1 || blocked[0] != "y" {
		t.Fatalf("z blockedBy after x done want [y], got %v", blocked)
	}
	states["y"] = StateDone
	if !c.Available(states, "z") {
		t.Fatal("z must be available once x and y are done")
	}
}

// TestCounts reflects done+skipped as resolved and computes percent.
func TestCounts(t *testing.T) {
	states := map[string]State{"a": StateDone, "b": StateSkipped}
	done, total, percent := linear.Counts(states)
	if done != 2 || total != 3 || percent != 66 {
		t.Fatalf("counts want {2,3,66}, got {%d,%d,%d}", done, total, percent)
	}
}
