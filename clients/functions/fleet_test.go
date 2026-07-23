package functions

import (
	"context"
	"testing"

	tasksengine "github.com/hanzoai/tasks/pkg/tasks"
)

type fakeView struct {
	started *tasksengine.StandaloneActivity
	states  []tasksengine.StandaloneActivity
	i       int
}

func (f *fakeView) RegisterNamespace(tasksengine.Namespace) error { return nil }
func (f *fakeView) StartActivity(_, id, _ string, _ tasksengine.TypeRef, _ string, _ any, _ *tasksengine.RetryPolicy, _, _, _, _ string, _, _ string) (*tasksengine.StandaloneActivity, error) {
	f.started = &tasksengine.StandaloneActivity{}
	return f.started, nil
}
func (f *fakeView) DescribeActivity(_, _, _ string) (*tasksengine.StandaloneActivity, bool, error) {
	if f.i >= len(f.states) {
		f.i = len(f.states) - 1
	}
	a := f.states[f.i]
	f.i++
	return &a, true, nil
}

// TestFleetRunCompletes — a completed job maps output + exit code onto the
// sandbox execResult contract (Ok, 200) so downstream recording is identical.
func TestFleetRunCompletes(t *testing.T) {
	fv := &fakeView{states: []tasksengine.StandaloneActivity{
		{Status: "ACTIVITY_TASK_STATE_STARTED"},
		{Status: "ACTIVITY_TASK_STATE_COMPLETED", Result: map[string]any{"output": "hi\n", "exitCode": 0}},
	}}
	orig := engineView
	engineView = func(string) (fleetView, error) { return fv, nil }
	defer func() { engineView = orig }()
	res, err := fleetRun(context.Background(), "acme", Function{Name: "train", Code: "print(1)", TimeoutSec: 30}, "")
	if err != nil || !res.Ok || res.Output != "hi\n" || res.StatusCode != 200 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

// TestFleetRunFailure — a failed job surfaces the traceback as Errout with a
// 500, never a fabricated success.
func TestFleetRunFailure(t *testing.T) {
	fv := &fakeView{states: []tasksengine.StandaloneActivity{
		{Status: "ACTIVITY_TASK_STATE_FAILED", FailureCause: "fn.run: exit status 1\nTraceback ..."},
	}}
	orig := engineView
	engineView = func(string) (fleetView, error) { return fv, nil }
	defer func() { engineView = orig }()
	res, err := fleetRun(context.Background(), "acme", Function{Name: "train", Code: "boom", TimeoutSec: 30}, "")
	if err != nil || res.Ok || res.StatusCode != 500 || res.Errout == "" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

// TestFleetUnconfigured — no engine, no execution, honest error (fail-closed).
func TestFleetUnconfigured(t *testing.T) {
	orig := engineView
	engineView = func(string) (fleetView, error) { return nil, errFleetUnconfigured }
	defer func() { engineView = orig }()
	if _, err := fleetRun(context.Background(), "acme", Function{Name: "x", TimeoutSec: 5}, ""); err == nil {
		t.Fatal("want fail-closed error")
	}
}
