package guide

import (
	"context"
	"errors"
	"testing"
)

// signalCurriculum: two signalled steps + one plain step, for reconcile tests.
var signalCurriculum = Curriculum{Version: "t", Steps: []Step{
	{ID: "a", Title: "A", Signal: "present"},
	{ID: "b", Title: "B", Signal: "absent"},
	{ID: "c", Title: "C"}, // no signal — never auto-detected
}}

// TestReconcileMarksPresentSignals: a step whose detector reports present auto-marks
// done; an absent one stays; a signal-less one is untouched.
func TestReconcileMarksPresentSignals(t *testing.T) {
	ctx := context.Background()
	dets := map[string]Detector{
		"present": func(context.Context, string, Step) (bool, error) { return true, nil },
		"absent":  func(context.Context, string, Step) (bool, error) { return false, nil },
	}
	states := map[string]State{}
	var marked []string
	mark := func(id string) error { marked = append(marked, id); return nil }
	if err := reconcile(ctx, "acme", signalCurriculum, states, dets, mark); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(marked) != 1 || marked[0] != "a" {
		t.Fatalf("only the present signal should mark; marked=%v", marked)
	}
	if states["a"] != StateDone {
		t.Fatalf("a should be done, got %q", states["a"])
	}
	if states["b"] == StateDone || states["c"] == StateDone {
		t.Fatalf("b/c must not be auto-done: b=%q c=%q", states["b"], states["c"])
	}
}

// TestReconcileNeverDowngradesTerminal: a step already skipped is not re-marked even
// if its detector reports present.
func TestReconcileNeverDowngradesTerminal(t *testing.T) {
	ctx := context.Background()
	dets := map[string]Detector{"present": func(context.Context, string, Step) (bool, error) { return true, nil }}
	states := map[string]State{"a": StateSkipped}
	var marked []string
	if err := reconcile(ctx, "acme", signalCurriculum, states, dets, func(id string) error { marked = append(marked, id); return nil }); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(marked) != 0 {
		t.Fatalf("a terminal step must not be re-marked; marked=%v", marked)
	}
	if states["a"] != StateSkipped {
		t.Fatalf("a must stay skipped, got %q", states["a"])
	}
}

// TestReconcileSwallowsDetectorErrors: a detector that errors (its data source is
// unreachable) leaves the step untouched — never a spurious done, never a failed
// request.
func TestReconcileSwallowsDetectorErrors(t *testing.T) {
	ctx := context.Background()
	dets := map[string]Detector{
		"present": func(context.Context, string, Step) (bool, error) { return false, errors.New("warehouse down") },
		"absent":  func(context.Context, string, Step) (bool, error) { return false, nil },
	}
	states := map[string]State{}
	err := reconcile(ctx, "acme", signalCurriculum, states, dets, func(string) error { return nil })
	if err != nil {
		t.Fatalf("reconcile must not surface a detector error, got %v", err)
	}
	if states["a"] == StateDone {
		t.Fatal("an erroring detector must not mark the step done")
	}
}

// TestReconcileUnknownSignalSkipped: a step naming a signal with no registered
// detector is simply skipped (a curriculum may reference a not-yet-shipped signal).
func TestReconcileUnknownSignalSkipped(t *testing.T) {
	ctx := context.Background()
	c := Curriculum{Steps: []Step{{ID: "a", Title: "A", Signal: "future"}}}
	states := map[string]State{}
	if err := reconcile(ctx, "acme", c, states, map[string]Detector{}, func(string) error { return nil }); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if states["a"] == StateDone {
		t.Fatal("an unregistered signal must not mark the step done")
	}
}

// TestActedDetector: the shipped "acted" signal reads the real action ledger — a
// step auto-completes once a "do it for me" call on its tool has succeeded.
func TestActedDetector(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	dets := newDetectors(func(context.Context, string) (*Store, error) { return st, nil })
	acted := dets["acted"]
	step := Step{ID: "positioning", Title: "P", Tool: "content_generate"}

	// Before any action: not present.
	if ok, err := acted(ctx, "acme", step); err != nil || ok {
		t.Fatalf("acted before action want (false,nil), got (%v,%v)", ok, err)
	}
	// A FAILED action does not count.
	if err := st.AddAction(ctx, ActionRecord{ID: "1", StepID: "positioning", Tool: "content_generate", OK: false, CreatedAt: 1}); err != nil {
		t.Fatalf("add failed action: %v", err)
	}
	if ok, _ := acted(ctx, "acme", step); ok {
		t.Fatal("a failed action must not satisfy the acted signal")
	}
	// A SUCCESSFUL action on the tool → present.
	if err := st.AddAction(ctx, ActionRecord{ID: "2", StepID: "positioning", Tool: "content_generate", OK: true, CreatedAt: 2}); err != nil {
		t.Fatalf("add ok action: %v", err)
	}
	if ok, err := acted(ctx, "acme", step); err != nil || !ok {
		t.Fatalf("acted after success want (true,nil), got (%v,%v)", ok, err)
	}
	// A step with no tool can never be "acted".
	if ok, _ := acted(ctx, "acme", Step{ID: "x", Title: "X"}); ok {
		t.Fatal("a tool-less step must not satisfy the acted signal")
	}
}

// TestActedEndToEndThroughReconcile: a successful action arms the acted signal so a
// subsequent reconcile flips the step to done — the auto-detect loop end to end
// over real store state.
func TestActedEndToEndThroughReconcile(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	dets := newDetectors(func(context.Context, string) (*Store, error) { return st, nil })
	c := Curriculum{Steps: []Step{{ID: "positioning", Title: "P", Signal: "acted", Tool: "content_generate"}}}

	states := map[string]State{}
	_ = reconcile(ctx, "acme", c, states, dets, func(id string) error { return st.SetState(ctx, id, StateDone, "auto", "", 1) })
	if states["positioning"] == StateDone {
		t.Fatal("positioning must not be done before any action")
	}
	if err := st.AddAction(ctx, ActionRecord{ID: "1", StepID: "positioning", Tool: "content_generate", OK: true, CreatedAt: 1}); err != nil {
		t.Fatalf("add action: %v", err)
	}
	states = map[string]State{}
	if err := reconcile(ctx, "acme", c, states, dets, func(id string) error { return st.SetState(ctx, id, StateDone, "auto", "", 2) }); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if states["positioning"] != StateDone {
		t.Fatalf("positioning must auto-complete after a successful action, got %q", states["positioning"])
	}
}

func TestFirstCount(t *testing.T) {
	cases := []struct {
		rows []map[string]any
		want int64
	}{
		{nil, 0},
		{[]map[string]any{{"n": uint64(3)}}, 3},
		{[]map[string]any{{"n": int64(5)}}, 5},
		{[]map[string]any{{"n": float64(7)}}, 7},
		{[]map[string]any{{"n": "9"}}, 9},
		{[]map[string]any{{"n": nil}}, 0},
	}
	for _, tc := range cases {
		if got := firstCount(tc.rows); got != tc.want {
			t.Fatalf("firstCount(%v) = %d, want %d", tc.rows, got, tc.want)
		}
	}
}
