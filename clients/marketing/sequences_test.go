package marketing

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud"
)

// seedActiveSequence creates an active sequence with the given step bodies (delay
// per step in seconds) and returns its id.
func seedActiveSequence(t *testing.T, s *cloud.Service[state], org string, delays []int64) string {
	t.Helper()
	ctx := context.Background()
	seq, err := s.State.store.CreateSequence(ctx, Sequence{ID: "seq_1", Org: org, Name: "Onboarding", Status: "active", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatalf("create sequence: %v", err)
	}
	for i, d := range delays {
		if _, err := s.State.store.AddStep(ctx, Step{
			ID: "step_" + string(rune('a'+i)), Org: org, SequenceID: seq.ID,
			DelaySeconds: d, Subject: "S" + string(rune('0'+i)), Body: "B" + string(rune('0'+i)), CreatedAt: 1,
		}); err != nil {
			t.Fatalf("add step %d: %v", i, err)
		}
	}
	return seq.ID
}

// enrollAt inserts an active enrollment whose first step is due at dueAt.
func enrollAt(t *testing.T, s *cloud.Service[state], org, seqID, addr string, dueAt int64) Enrollment {
	t.Helper()
	e, err := s.State.store.Enroll(context.Background(), Enrollment{
		ID: "enr_1", Org: org, SequenceID: seqID, Address: addr, Channel: "email",
		CurrentStep: 0, Status: enrollActive, NextRunAt: dueAt, EnrolledAt: 1, UpdatedAt: 1,
	})
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	return e
}

// TestDripSchedulingAndIdempotence is the load-bearing drip test: a step fires
// only once it is due, fires EXACTLY once even across redelivery, then the walk
// advances to the next step's due time and finally completes.
func TestDripSchedulingAndIdempotence(t *testing.T) {
	ctx := context.Background()
	s := testService(t)
	got, restore := captureSends(t)
	defer restore()

	seqID := seedActiveSequence(t, s, "hanzo", []int64{0, 100}) // step0 immediate, step1 +100s
	e := enrollAt(t, s, "hanzo", seqID, "u@x.com", 1000)

	// SCHEDULING: not due yet (now < next_run_at) → nothing sent.
	if n, err := processDue(ctx, s, 999, 100); err != nil || n != 0 {
		t.Fatalf("before due: want (0,nil), got (%d,%v)", n, err)
	}
	if len(*got) != 0 {
		t.Fatalf("nothing should send before due, got %+v", *got)
	}

	// DUE: step0 fires once; enrollment advances to step1 due at 1000+100.
	if n, err := processDue(ctx, s, 1000, 100); err != nil || n != 1 {
		t.Fatalf("at due: want (1,nil), got (%d,%v)", n, err)
	}
	if len(*got) != 1 || (*got)[0].body != "B0" {
		t.Fatalf("step0 must send once, got %+v", *got)
	}
	adv, _ := s.State.store.GetEnrollment(ctx, "hanzo", e.ID)
	if adv.CurrentStep != 1 || adv.NextRunAt != 1100 {
		t.Fatalf("after step0 want step1@1100, got step%d@%d", adv.CurrentStep, adv.NextRunAt)
	}

	// IDEMPOTENCE (redelivery): simulate a crash between send and advance by
	// resetting the walk to step0@1000, then re-run. The claim is already taken,
	// so step0 does NOT send again — but the walk still advances.
	if err := s.State.store.AdvanceEnrollment(ctx, e.ID, 0, 1000, 1); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if n, err := processDue(ctx, s, 1000, 100); err != nil || n != 1 {
		t.Fatalf("redelivery: want (1,nil), got (%d,%v)", n, err)
	}
	if len(*got) != 1 {
		t.Fatalf("redelivered step0 must NOT send twice, got %+v", *got)
	}

	// step1 fires when due; then the walk completes (no step 2).
	if n, err := processDue(ctx, s, 1100, 100); err != nil || n != 1 {
		t.Fatalf("step1 due: want (1,nil), got (%d,%v)", n, err)
	}
	if len(*got) != 2 || (*got)[1].body != "B1" {
		t.Fatalf("step1 must send once, got %+v", *got)
	}
	done, _ := s.State.store.GetEnrollment(ctx, "hanzo", e.ID)
	if done.Status != enrollCompleted {
		t.Fatalf("walk should complete, got status %q", done.Status)
	}

	// Nothing left to do.
	if n, _ := processDue(ctx, s, 9999, 100); n != 0 {
		t.Fatalf("completed enrollment must not re-process, advanced %d", n)
	}
	if len(*got) != 2 {
		t.Fatalf("total sends must stay 2, got %d", len(*got))
	}
}

// TestDripSuppressionGate: a drip step for a suppressed contact is skipped at the
// ONE gate (never reaches the rail) yet the walk still advances — an opt-out
// silences delivery without wedging the sequence.
func TestDripSuppressionGate(t *testing.T) {
	ctx := context.Background()
	s := testService(t)
	got, restore := captureSends(t)
	defer restore()

	seqID := seedActiveSequence(t, s, "hanzo", []int64{0})
	if err := s.State.store.Suppress(ctx, Suppression{Org: "hanzo", Channel: "email", Address: "u@x.com", CreatedAt: 1}); err != nil {
		t.Fatalf("suppress: %v", err)
	}
	e := enrollAt(t, s, "hanzo", seqID, "u@x.com", 1000)

	if n, err := processDue(ctx, s, 1000, 100); err != nil || n != 1 {
		t.Fatalf("want (1,nil), got (%d,%v)", n, err)
	}
	if len(*got) != 0 {
		t.Fatalf("suppressed drip must not reach the rail, got %+v", *got)
	}
	done, _ := s.State.store.GetEnrollment(ctx, "hanzo", e.ID)
	if done.Status != enrollCompleted {
		t.Fatalf("suppressed single-step walk should complete, got %q", done.Status)
	}
}

// TestEnrollIdempotent: the same address cannot be double-enrolled in one
// sequence.
func TestEnrollIdempotent(t *testing.T) {
	ctx := context.Background()
	s := testService(t)
	seqID := seedActiveSequence(t, s, "hanzo", []int64{0})
	enrollAt(t, s, "hanzo", seqID, "u@x.com", 1000)

	_, err := s.State.store.Enroll(ctx, Enrollment{
		ID: "enr_2", Org: "hanzo", SequenceID: seqID, Address: "u@x.com", Channel: "email",
		CurrentStep: 0, Status: enrollActive, NextRunAt: 1000, EnrolledAt: 2, UpdatedAt: 2,
	})
	if !errors.Is(err, errConflict) {
		t.Fatalf("double enroll want errConflict, got %v", err)
	}
}

// TestDripTenantIsolation: a sweep processes each org's enrollments with that
// org's steps — a due enrollment in org A never sends org B's content, and the
// gate keys suppression per org.
func TestDripTenantIsolation(t *testing.T) {
	ctx := context.Background()
	s := testService(t)
	got, restore := captureSends(t)
	defer restore()

	// Two orgs, two independent sequences (globally-unique ids, as prod mints).
	if _, err := s.State.store.CreateSequence(ctx, Sequence{ID: "sa", Org: "a", Name: "A", Status: "active", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.State.store.AddStep(ctx, Step{ID: "sta", Org: "a", SequenceID: "sa", Subject: "SA", Body: "BA", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.State.store.CreateSequence(ctx, Sequence{ID: "sb", Org: "b", Name: "B", Status: "active", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.State.store.AddStep(ctx, Step{ID: "stb", Org: "b", SequenceID: "sb", Subject: "SB", Body: "BB", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.State.store.Enroll(ctx, Enrollment{ID: "ea", Org: "a", SequenceID: "sa", Address: "a@x.com", Channel: "email", Status: enrollActive, NextRunAt: 1000, EnrolledAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.State.store.Enroll(ctx, Enrollment{ID: "eb", Org: "b", SequenceID: "sb", Address: "b@x.com", Channel: "email", Status: enrollActive, NextRunAt: 1000, EnrolledAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}

	if n, err := processDue(ctx, s, 1000, 100); err != nil || n != 2 {
		t.Fatalf("want (2,nil), got (%d,%v)", n, err)
	}
	seen := map[string]string{}
	for _, m := range *got {
		seen[m.org] = m.body
	}
	if seen["a"] != "BA" || seen["b"] != "BB" {
		t.Fatalf("each org must get its OWN content, got %+v", seen)
	}
}
