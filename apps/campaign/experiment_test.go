package campaign

import (
	"context"
	"errors"
	"testing"
)

// setExperimentForTest wires assign and restores the seams on cleanup.
func setExperimentForTest(t *testing.T, assign AssignFunc) {
	t.Helper()
	prevA, prevAn := assignSeam, analyzeSeam
	SetExperiment(assign, nil)
	t.Cleanup(func() { SetExperiment(prevA, prevAn) })
}

func multiCreative(specs ...ChannelSpec) Campaign {
	return Campaign{
		ID: "cmp_ab", Org: "acme", Name: "A/B", Status: StatusDraft,
		Content: []string{"hero-a", "hero-b"}, Channels: specs,
	}
}

// TestAB_AssignedVariantFlowsToExecutor: a multi-creative campaign composes the
// experiment primitive — the assigned variant is tagged onto the Plan the channel
// executor receives (utm_content), so analytics can attribute per creative.
func TestAB_AssignedVariantFlowsToExecutor(t *testing.T) {
	resetChannels()
	var gotExperimentID, gotSubject string
	setExperimentForTest(t, func(_ context.Context, _, experimentID, subject string) (string, error) {
		gotExperimentID, gotSubject = experimentID, subject
		return "hero-b", nil
	})
	rc := &recordingChannel{kind: KindPaid, ref: Ref{ExternalID: "ext_1", Status: chanLive}}
	RegisterChannel(rc)

	out := fanOut(context.Background(), "acme", multiCreative(ChannelSpec{Kind: KindPaid, Platform: "meta", Status: chanPending}))

	if rc.gotPlan.Variant != "hero-b" {
		t.Fatalf("assigned variant must flow to the executor plan, got %q", rc.gotPlan.Variant)
	}
	if gotExperimentID != ExperimentKey("cmp_ab") || gotSubject != "cmp_ab" {
		t.Fatalf("experiment composed with wrong key/subject: id=%q subject=%q", gotExperimentID, gotSubject)
	}
	if out.Status != StatusLive {
		t.Fatalf("campaign should be live, got %q", out.Status)
	}
}

// TestAB_SingleCreativeNeverAssigns: a single-creative campaign never calls the
// experiment primitive (no A/B) — the plan variant stays empty.
func TestAB_SingleCreativeNeverAssigns(t *testing.T) {
	resetChannels()
	called := false
	setExperimentForTest(t, func(_ context.Context, _, _, _ string) (string, error) {
		called = true
		return "hero-b", nil
	})
	rc := &recordingChannel{kind: KindPaid, ref: Ref{ExternalID: "ext_1", Status: chanLive}}
	RegisterChannel(rc)

	camp := draftWith(ChannelSpec{Kind: KindPaid, Platform: "meta", Status: chanPending}) // Content=["creative-a"]
	_ = fanOut(context.Background(), "acme", camp)

	if called {
		t.Fatalf("a single-creative campaign must not consult the experiment primitive")
	}
	if rc.gotPlan.Variant != "" {
		t.Fatalf("single-creative plan variant must be empty, got %q", rc.gotPlan.Variant)
	}
}

// TestAB_FailSoftOnAssignError: an assignment error never blocks a launch — the
// campaign runs the default creative (variant "").
func TestAB_FailSoftOnAssignError(t *testing.T) {
	resetChannels()
	setExperimentForTest(t, func(_ context.Context, _, _, _ string) (string, error) {
		return "", errors.New("experiments: unknown experiment")
	})
	rc := &recordingChannel{kind: KindPaid, ref: Ref{ExternalID: "ext_1", Status: chanLive}}
	RegisterChannel(rc)

	out := fanOut(context.Background(), "acme", multiCreative(ChannelSpec{Kind: KindPaid, Platform: "meta", Status: chanPending}))
	if rc.gotPlan.Variant != "" {
		t.Fatalf("assign error must fail soft to the default creative, got %q", rc.gotPlan.Variant)
	}
	if out.Status != StatusLive {
		t.Fatalf("launch must proceed despite an assign error, got %q", out.Status)
	}
}
