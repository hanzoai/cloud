package automations

import (
	"context"
	"errors"
	"testing"
)

// TestStoreOrgIsolation is the load-bearing tenant-isolation test: two orgs write
// flows/versions/runs; neither can list, get, or mutate the other's rows. The org is
// folded into every key, so a cross-tenant read is a physical miss, not a filtered one.
func TestStoreOrgIsolation(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	fa, err := s.CreateFlow(ctx, Flow{ID: "flow_a", Org: "acme", Status: FlowDisabled, Created: 1, Updated: 1})
	if err != nil {
		t.Fatalf("create acme flow: %v", err)
	}
	if _, err := s.CreateFlow(ctx, Flow{ID: "flow_b", Org: "globex", Status: FlowDisabled, Created: 1, Updated: 1}); err != nil {
		t.Fatalf("create globex flow: %v", err)
	}

	// acme lists exactly its own.
	list, err := s.ListFlows(ctx, "acme", 100)
	if err != nil {
		t.Fatalf("list acme: %v", err)
	}
	if len(list) != 1 || list[0].ID != "flow_a" {
		t.Fatalf("acme must see [flow_a], got %+v", list)
	}

	// globex cannot GET acme's flow (cross-tenant read → not found).
	if _, err := s.GetFlow(ctx, "globex", fa.ID); !errors.Is(err, errNotFound) {
		t.Fatalf("globex GET acme flow want errNotFound, got %v", err)
	}
	// globex cannot UPDATE acme's flow (cross-tenant write → not found, no mutation).
	if _, err := s.UpdateFlow(ctx, Flow{ID: fa.ID, Org: "globex", Status: FlowEnabled, Updated: 2}); !errors.Is(err, errNotFound) {
		t.Fatalf("globex UPDATE acme flow want errNotFound, got %v", err)
	}
	got, _ := s.GetFlow(ctx, "acme", fa.ID)
	if got.Status != FlowDisabled {
		t.Fatalf("acme flow must be unchanged, got status %q", got.Status)
	}
	// globex cannot DELETE acme's flow.
	deleted, err := s.DeleteFlow(ctx, "globex", fa.ID)
	if err != nil {
		t.Fatalf("globex delete: %v", err)
	}
	if deleted {
		t.Fatal("globex must not delete acme's flow")
	}
	if _, err := s.GetFlow(ctx, "acme", fa.ID); err != nil {
		t.Fatalf("acme flow must survive globex delete: %v", err)
	}

	// Versions + runs isolate identically.
	if _, err := s.CreateVersion(ctx, FlowVersion{ID: "ver_a", Org: "acme", FlowID: fa.ID, State: VersionDraft, Created: 1, Updated: 1}); err != nil {
		t.Fatalf("create acme version: %v", err)
	}
	if _, err := s.GetVersion(ctx, "globex", "ver_a"); !errors.Is(err, errNotFound) {
		t.Fatalf("globex GET acme version want errNotFound, got %v", err)
	}
	if _, err := s.CreateRun(ctx, FlowRun{ID: "run_a", Org: "acme", FlowID: fa.ID, WorkflowID: "run_a", Status: RunRunning, Created: 1, Updated: 1}); err != nil {
		t.Fatalf("create acme run: %v", err)
	}
	if _, err := s.GetRun(ctx, "globex", "run_a"); !errors.Is(err, errNotFound) {
		t.Fatalf("globex GET acme run want errNotFound, got %v", err)
	}
	if runs, _ := s.ListRuns(ctx, "globex", "", 100); len(runs) != 0 {
		t.Fatalf("globex must see zero runs, got %d", len(runs))
	}
}

// TestVersionCrossTenantRefRejected: a version's flow_id must resolve INSIDE the
// org; anchoring a version to another tenant's flow is errBadRef, never a link.
func TestVersionCrossTenantRefRejected(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	if _, err := s.CreateFlow(ctx, Flow{ID: "flow_acme", Org: "acme", Created: 1, Updated: 1}); err != nil {
		t.Fatalf("seed acme flow: %v", err)
	}
	// globex tries to anchor a version to acme's flow → errBadRef.
	if _, err := s.CreateVersion(ctx, FlowVersion{ID: "ver_x", Org: "globex", FlowID: "flow_acme", State: VersionDraft, Created: 1, Updated: 1}); !errors.Is(err, errBadRef) {
		t.Fatalf("cross-tenant version ref want errBadRef, got %v", err)
	}
	// A version anchored to a missing flow in-org is also errBadRef.
	if _, err := s.CreateVersion(ctx, FlowVersion{ID: "ver_y", Org: "acme", FlowID: "flow_ghost", State: VersionDraft, Created: 1, Updated: 1}); !errors.Is(err, errBadRef) {
		t.Fatalf("missing-flow version ref want errBadRef, got %v", err)
	}
}

// TestFlowLifecycleRoundTrip exercises the full flow+version+run lifecycle, including
// the trigger-tree JSON round-trip through SQLite.
func TestFlowLifecycleRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	if _, err := s.CreateFlow(ctx, Flow{ID: "f1", Org: "o", Status: FlowDisabled, Created: 10, Updated: 10}); err != nil {
		t.Fatalf("create flow: %v", err)
	}
	trig := &FlowTrigger{
		Name: "trigger", Type: TriggerTypePiece, DisplayName: "Start", Strategy: StrategyManual,
		Settings: StepSettings{PieceName: corePiece, TriggerName: "manual"},
		NextAction: &FlowAction{
			Name: "step1", Type: ActionTypeCode, DisplayName: "Transform",
			Settings: StepSettings{Input: map[string]any{"x": float64(1)}},
		},
	}
	v, err := s.CreateVersion(ctx, FlowVersion{ID: "v1", Org: "o", FlowID: "f1", DisplayName: "My Flow", Trigger: trig, Valid: true, State: VersionDraft, Created: 10, Updated: 10})
	if err != nil {
		t.Fatalf("create version: %v", err)
	}
	got, err := s.GetVersion(ctx, "o", v.ID)
	if err != nil {
		t.Fatalf("get version: %v", err)
	}
	if got.Trigger == nil || got.Trigger.NextAction == nil || got.Trigger.NextAction.Name != "step1" {
		t.Fatalf("trigger tree lost in round-trip: %+v", got.Trigger)
	}
	if got.Trigger.NextAction.Settings.Input["x"] != float64(1) {
		t.Fatalf("action input lost in round-trip: %+v", got.Trigger.NextAction.Settings)
	}

	// Run status transition.
	if _, err := s.CreateRun(ctx, FlowRun{ID: "r1", Org: "o", FlowID: "f1", FlowVersionID: "v1", WorkflowID: "r1", Status: RunRunning, StartTime: 11, Created: 11, Updated: 11}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := s.UpdateRunStatus(ctx, "o", "r1", RunSucceeded, 12, 12); err != nil {
		t.Fatalf("update run status: %v", err)
	}
	r, _ := s.GetRun(ctx, "o", "r1")
	if r.Status != RunSucceeded || r.FinishTime != 12 {
		t.Fatalf("run status transition lost: %+v", r)
	}

	// Deleting the flow cascades its versions + runs (within the org).
	if _, err := s.DeleteFlow(ctx, "o", "f1"); err != nil {
		t.Fatalf("delete flow: %v", err)
	}
	if _, err := s.GetVersion(ctx, "o", "v1"); !errors.Is(err, errNotFound) {
		t.Fatalf("version must be cascaded, got %v", err)
	}
	if _, err := s.GetRun(ctx, "o", "r1"); !errors.Is(err, errNotFound) {
		t.Fatalf("run must be cascaded, got %v", err)
	}
}
