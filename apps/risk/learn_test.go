package risk

// learn_test.go — the model plane's own boundary, which is a different boundary
// from the feature surface's.
//
// A read is scoped by a predicate. A MODEL is not: it is state, and the question
// is whether one organisation's state can end up inside another's model. Three
// ways it could, three tests:
//
//   - by sharing geometry, so that one organisation's regions describe another's
//     — refuted by [TestModelGeometry_IsPerOrg];
//   - by sharing counters, so that learning for one moves the other — refuted by
//     [TestModel_LearningIsNotShared];
//   - by restoring one organisation's snapshot into another — refuted by
//     [TestRestore_RefusesAnotherOrganisationsState].

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// stream is a fixed sequence of events, so two organisations can be given
// IDENTICAL input and any difference in the result is a difference in the model
// rather than in the data.
func stream(n int, at time.Time) []observation {
	out := make([]observation, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, observation{
			ID:      "e_" + itoa(i),
			Kind:    kindAccount,
			Subject: "u_" + itoa(i%7),
			USD:     float64(100 + (i*37)%900),
			Device:  "d_" + itoa(i%3),
			At:      at.Add(time.Duration(i) * time.Second),
		})
	}
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func teach(t *testing.T, p *plane, k tenant, evs []observation) {
	t.Helper()
	for _, o := range evs {
		if _, err := p.learn(k, o); err != nil {
			t.Fatalf("learn: %v", err)
		}
	}
}

// TestModelGeometry_IsPerOrg: two organisations fed IDENTICAL events end up with
// different trees and therefore different learned state.
//
// This is the property that makes the tenant boundary hold inside the model
// rather than only around it. Probing one organisation's model tells an adversary
// nothing about where another's regions lie, because they are not the same
// regions.
//
// Mutation proof: fix the geometry seed to a constant shared by every tenant
// (drop the per-tenant draw in plane.plant) and this fails.
func TestModelGeometry_IsPerOrg(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	a, b := key(t, brandA, orgA), key(t, brandA, orgB)
	evs := stream(600, time.Now().UTC().Add(-6*time.Hour))
	teach(t, p, a, evs)
	teach(t, p, b, evs)

	sa, ok := p.snapshotOf(t, a)
	if !ok {
		t.Fatal("organisation A learned nothing")
	}
	sb, ok := p.snapshotOf(t, b)
	if !ok {
		t.Fatal("organisation B learned nothing")
	}
	if sa.Seed == sb.Seed {
		t.Fatal("two organisations were planted from one seed — their trees are the same trees")
	}
	if reflect.DeepEqual(sa.Ref, sb.Ref) && reflect.DeepEqual(sa.Cur, sb.Cur) {
		t.Fatal("identical input produced identical masses in both models — the geometry is shared")
	}
	if sa.Learned != sb.Learned {
		t.Fatalf("the two models learned %d and %d events from the same stream", sa.Learned, sb.Learned)
	}
}

// TestModel_LearningIsNotShared: teaching one organisation moves nothing in
// another's model. It is the counters half of the same boundary.
func TestModel_LearningIsNotShared(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	a, b := key(t, brandA, orgA), key(t, brandB, orgA) // SAME org name, two brands
	if a == b {
		t.Fatal("two brands produced one tenant key")
	}
	// Touch B first so it exists, then teach only A.
	if _, err := p.state(b); err != nil {
		t.Fatalf("state(B): %v", err)
	}
	teach(t, p, a, stream(400, time.Now().UTC().Add(-4*time.Hour)))

	sa, _ := p.state(a)
	sb, _ := p.state(b)
	if sa.Learned == 0 {
		t.Fatal("organisation A learned nothing — the test proves nothing")
	}
	if sb.Learned != 0 {
		t.Fatalf("organisation B's model learned %d events it was never given", sb.Learned)
	}
	if sb.Scored != 0 {
		t.Fatalf("organisation B's model scored %d events it never saw", sb.Scored)
	}
}

// TestRestore_RefusesAnotherOrganisationsState: a snapshot is one organisation's
// learned behaviour, so installing it into another's model is a disclosure of the
// first organisation's activity. The engine checks the shape, the version and the
// mass invariant; whose state it is has to be checked here, because here the
// caller is a request rather than the tenant.
//
// Mutation proof: delete the key comparison in plane.install and this fails.
func TestRestore_RefusesAnotherOrganisationsState(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	a, b := key(t, brandA, orgA), key(t, brandA, orgB)
	teach(t, p, a, stream(300, time.Now().UTC().Add(-3*time.Hour)))

	snap, ok, err := p.pin(a)
	if err != nil || !ok {
		t.Fatalf("pin(A): ok=%v err=%v", ok, err)
	}
	if _, err := p.adopt(b, snap); err == nil {
		t.Fatal("organisation B adopted organisation A's learned state")
	}
	// ...and A can still adopt its own, so the refusal above is about WHOSE state
	// it is and not about the state being unusable.
	if _, err := p.adopt(a, snap); err != nil {
		t.Fatalf("an organisation could not adopt its own snapshot: %v", err)
	}
}

// TestSnapshot_SurvivesARestart is the reason Shutdown exists. The binary deploys
// one replica at a time with the old pod stopped first, so a model that is not
// written down is a model every rollout returns to warming — and a warming model
// REFUSES to score, which reads as a clean result to anything not checking the
// refusal.
func TestSnapshot_SurvivesARestart(t *testing.T) {
	probe.reset(true)
	dir := t.TempDir()
	k := key(t, brandA, orgA)
	evs := stream(500, time.Now().UTC().Add(-5*time.Hour))

	first := planeAt(t, dir)
	teach(t, first, k, evs)
	before, _ := first.state(k)
	if before.Learned == 0 {
		t.Fatal("nothing was learned — the test proves nothing")
	}
	if err := first.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := planeAt(t, dir)
	defer func() { _ = second.close() }()
	after, err := second.state(k)
	if err != nil {
		t.Fatalf("state after restart: %v", err)
	}
	if after.Learned != before.Learned {
		t.Fatalf("after a restart the model has learned %d events, had %d — a rollout returned the tenant to warming",
			after.Learned, before.Learned)
	}
	if after.Digest != before.Digest {
		t.Fatalf("the model's shape changed across a restart: %s -> %s", before.Digest, after.Digest)
	}
}

// TestAppetite_IsPerOrganisationAndShadowIsTheDefault: the risk appetite is the
// decision a model is not permitted to make for itself, so it is stated per
// organisation — and a model nobody has reviewed changes no outcome.
func TestAppetite_IsPerOrganisationAndShadowIsTheDefault(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	a, b := key(t, brandA, orgA), key(t, brandA, orgB)

	st, err := p.state(a)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if !st.Config.Shadow {
		t.Fatal("a brand-new model is LIVE — shadow must be the default, or a model nobody reviewed can refuse a payment")
	}

	if _, err := p.appetite(a, 0.05, 0.01, true); err != nil {
		t.Fatalf("appetite: %v", err)
	}
	sa, _ := p.state(a)
	sb, _ := p.state(b)
	if sa.Config.Appetite.Review != 0.05 || sa.Config.Shadow {
		t.Fatalf("organisation A's appetite did not take: %+v", sa.Config)
	}
	if sb.Config.Appetite.Review == 0.05 || !sb.Config.Shadow {
		t.Fatalf("organisation B inherited organisation A's appetite: %+v", sb.Config)
	}
}

// TestAppetite_KeepsWhatWasLearned: restating policy must not unlearn anything.
// The model's identity covers its SHAPE and not its appetite, so the learned
// state carries across exactly.
func TestAppetite_KeepsWhatWasLearned(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	k := key(t, brandA, orgA)
	teach(t, p, k, stream(400, time.Now().UTC().Add(-4*time.Hour)))
	before, _ := p.state(k)

	after, err := p.appetite(k, 0.02, 0.001, false)
	if err != nil {
		t.Fatalf("appetite: %v", err)
	}
	if after.Learned != before.Learned {
		t.Fatalf("restating the appetite unlearned %d events", before.Learned-after.Learned)
	}
}

// TestSearch_IsDry: an exhaustive search must not be able to move the live model.
// A sandbox that can mutate live state is not a sandbox.
func TestSearch_IsDry(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	k := key(t, brandA, orgA)
	hist := stream(400, time.Now().UTC().Add(-4*time.Hour))
	teach(t, p, k, hist)
	before, _ := p.state(k)

	rep := p.search(context.Background(), k, "srch_test", hist)
	if len(rep.Trials) == 0 {
		t.Fatal("the search tried no candidate at all")
	}
	if rep.Winner == nil {
		t.Fatal("the search ranked no winner")
	}
	after, _ := p.state(k)
	if after.Learned != before.Learned || after.Scored != before.Scored {
		t.Fatalf("the search moved the LIVE model: learned %d->%d, scored %d->%d",
			before.Learned, after.Learned, before.Scored, after.Scored)
	}
	// Every candidate is ranked and the ranking is stable.
	for i := 1; i < len(rep.Trials); i++ {
		if rep.Trials[i-1].Fit > rep.Trials[i].Fit {
			t.Fatalf("the trials are not ordered by fit at %d", i)
		}
	}
}

// TestSearch_RefusesAnEmptyHistory: "no alerts" is exactly what a quiet model
// looks like, so a replay over nothing is REFUSED rather than reported as a clean
// result. That refusal is the whole reason a sandbox exists.
func TestSearch_RefusesAnEmptyHistory(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	k := key(t, brandA, orgA)
	rep := p.search(context.Background(), k, "srch_empty", nil)
	if rep.Refusal == "" {
		t.Fatal("an empty history produced a report with no refusal")
	}
	if rep.Winner != nil {
		t.Fatal("an empty history produced a winning shape")
	}
	// And the accepting path refuses too, rather than starting a run that proves
	// nothing.
	if _, err := p.begin(context.Background(), k, 24*time.Hour); err == nil {
		t.Fatal("begin accepted a search over an empty surface")
	}
}

// TestSearch_ReadsOnlyItsOwnHistory: the run's history comes from the tenant's
// own feature surface through the one door, so the statements it issues carry the
// tenant like every other read.
func TestSearch_ReadsOnlyItsOwnHistory(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	k := key(t, brandA, orgA)
	now := time.Now().UTC()
	for i := 0; i < 50; i++ {
		probe.hold(string(k), map[string]any{
			"subject_kind": kindAccount, "subject": "u_" + itoa(i%5),
			"bucket": now.Add(-time.Duration(i) * time.Minute),
			"events": uint32(3), "spend_nano": int64(250_000_000 + i),
		})
	}
	run, err := p.begin(context.Background(), k, 24*time.Hour)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if run.Events == 0 {
		t.Fatal("the run replays nothing")
	}
	for _, s := range probe.reads() {
		if len(s.Args) == 0 || s.Args[0] != string(k) {
			t.Fatalf("the search read a statement that did not bind this tenant first: %v\n%s", s.Args, s.SQL)
		}
	}
}

// TestWarm_FoldsOnlyThisOrganisationsSurface: the automatic fold is the moat
// applied without being asked for, so it is exactly the place a mistake would
// pull another organisation's history into a model.
func TestWarm_FoldsOnlyThisOrganisationsSurface(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	a, b := key(t, brandA, orgA), key(t, brandA, orgB)
	now := time.Now().UTC()
	for i := 0; i < 30; i++ {
		probe.hold(string(a), map[string]any{
			"subject_kind": kindAccount, "subject": "u_a",
			"bucket": now.Add(-time.Duration(i) * time.Minute),
			"events": uint32(2), "spend_nano": int64(100_000_000),
		})
	}
	na, err := p.warm(context.Background(), a)
	if err != nil {
		t.Fatalf("warm(A): %v", err)
	}
	if na != 30 {
		t.Fatalf("A folded %d buckets, want 30", na)
	}
	nb, err := p.warm(context.Background(), b)
	if err != nil {
		t.Fatalf("warm(B): %v", err)
	}
	if nb != 0 {
		t.Fatalf("B folded %d buckets of a surface it does not own", nb)
	}
}

// TestWarm_ReportsTheGapRatherThanZero: an unreachable warehouse and an empty
// surface are different facts. A model must not report them as one, because the
// first is a control coming up and the second is a control that will not.
func TestWarm_ReportsTheGapRatherThanZero(t *testing.T) {
	probe.reset(false)
	defer probe.reset(true)
	p := newTestPlane(t)
	k := key(t, brandA, orgA)
	if _, err := p.warm(context.Background(), k); err == nil {
		t.Fatal("a fold against an unreachable warehouse reported success")
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func planeAt(t *testing.T, dir string) *plane {
	t.Helper()
	p, err := newPlane(baseAt(t, dir))
	if err != nil {
		t.Fatalf("newPlane: %v", err)
	}
	return p
}

// snapshotOf reads a resident's state without persisting it, for the tests that
// compare two models rather than exercise the shelf.
func (p *plane) snapshotOf(t *testing.T, k tenant) (snap snapshotView, ok bool) {
	t.Helper()
	r, err := p.resident(k)
	if err != nil {
		t.Fatalf("resident: %v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s, held := r.mod.Snapshot(string(k))
	if !held {
		return snapshotView{}, false
	}
	return snapshotView{Seed: s.Seed, Learned: s.Learned, Ref: s.Ref, Cur: s.Cur}, true
}

type snapshotView struct {
	Seed     uint64
	Learned  int64
	Ref, Cur [][]float64
}
