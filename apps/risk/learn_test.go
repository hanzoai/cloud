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
	"sync"
	"testing"
	"time"
)

// ob is the tests' own way to build an observation, and it goes through the ONE
// constructor exactly like production does. A test that could build one another
// way would be a test of a type the package does not have — and the source test
// [TestObservation_HasOneConstructor] scans these files too.
func ob(t *testing.T, id, kind, subject string, usd float64, at time.Time, axes ...string) observation {
	t.Helper()
	a := actor{Kind: kind, Subject: subject}
	if len(axes) > 0 {
		a.Peer = axes[0]
	}
	if len(axes) > 1 {
		a.Device = axes[1]
	}
	o, err := observe(id, a, usd, at)
	if err != nil {
		t.Fatalf("observe(%q): %v", id, err)
	}
	return o
}

// stream is a fixed sequence of events, so two organisations can be given
// IDENTICAL input and any difference in the result is a difference in the model
// rather than in the data.
func stream(n int, at time.Time) []observation {
	out := make([]observation, 0, n)
	for i := 0; i < n; i++ {
		o, err := observe("e_"+itoa(i), actor{
			Kind:    kindAccount,
			Subject: "u_" + itoa(i%7),
			Device:  "d_" + itoa(i%3),
		}, float64(100+(i*37)%900), at.Add(time.Duration(i)*time.Second))
		if err != nil {
			panic(err)
		}
		out = append(out, o)
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
	if _, err := p.learn(k, evs...); err != nil {
		t.Fatalf("learn: %v", err)
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
	if _, _, err := p.state(b); err != nil {
		t.Fatalf("state(B): %v", err)
	}
	teach(t, p, a, stream(400, time.Now().UTC().Add(-4*time.Hour)))

	sa, _, _ := p.state(a)
	sb, _, _ := p.state(b)
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
	if _, _, err := p.adopt(b, snap); err == nil {
		t.Fatal("organisation B adopted organisation A's learned state")
	}
	// ...and A can still adopt its own, so the refusal above is about WHOSE state
	// it is and not about the state being unusable.
	if _, _, err := p.adopt(a, snap); err != nil {
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
	before, _, _ := first.state(k)
	if before.Learned == 0 {
		t.Fatal("nothing was learned — the test proves nothing")
	}
	if err := first.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := planeAt(t, dir)
	defer func() { _ = second.close() }()
	after, _, err := second.state(k)
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

	st, _, err := p.state(a)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if !st.Config.Shadow {
		t.Fatal("a brand-new model is LIVE — shadow must be the default, or a model nobody reviewed can refuse a payment")
	}

	if _, _, _, err := p.appetite(a, 0.05, 0.01, true, "u_"+orgA); err != nil {
		t.Fatalf("appetite: %v", err)
	}
	sa, _, _ := p.state(a)
	sb, _, _ := p.state(b)
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
	before, _, _ := p.state(k)

	after, _, _, err := p.appetite(k, 0.02, 0.001, false, "u_"+orgA)
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
	before, _, _ := p.state(k)

	rep := p.search(context.Background(), k, "srch_test", hist)
	if len(rep.Trials) == 0 {
		t.Fatal("the search tried no candidate at all")
	}
	if rep.Winner == nil {
		t.Fatal("the search ranked no winner")
	}
	after, _, _ := p.state(k)
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
	if _, err := p.begin(context.Background(), k, 24*time.Hour, nil, nil); err == nil {
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
	for i := 0; i < 50; i++ {
		probe.hold(string(k), map[string]any{
			"subject_kind": kindAccount, "subject": "u_" + itoa(i%5),
			"bucket": surfaceAt(i + 1),
			"events": uint32(3), "spend_nano": int64(250_000_000 + i),
		})
	}
	run, err := p.begin(context.Background(), k, 24*time.Hour, nil, nil)
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
	holdFolds(t, p) // this test drives the fold; the plane must not also be folding
	a, b := key(t, brandA, orgA), key(t, brandA, orgB)
	for i := 0; i < 30; i++ {
		probe.hold(string(a), map[string]any{
			"subject_kind": kindAccount, "subject": "u_a",
			"bucket": surfaceAt(i + 1),
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
	holdFolds(t, p)
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

// ── the residency, and the three ways it used to go quiet ────────────────────

// TestResident_IsBuiltOnceHoweverManyAskAtOnce: building a residency replays up
// to [recordRows] of a tenant's own record and allocates a whole ring set and
// model. Unserialised, N concurrent first touches each did the whole thing and
// N−1 were thrown away at the end — N × 8 MiB of rings allocated to discard, for
// free, and every rollout is exactly when every tenant touches at once.
//
// Mutation proof: remove the p.opening single flight from plane.resident and
// `built` below rises with the concurrency.
func TestResident_IsBuiltOnceHoweverManyAskAtOnce(t *testing.T) {
	probe.reset(true)
	dir := t.TempDir()
	k := key(t, brandA, orgA)

	first := planeAt(t, dir)
	holdFolds(t, first)
	teach(t, first, k, stream(2_000, time.Now().UTC().Add(-6*time.Hour)))
	if err := first.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	p := planeAt(t, dir)
	defer func() { _ = p.close() }()
	holdFolds(t, p)
	const callers = 32
	var wg sync.WaitGroup
	seen := make([]*resident, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := p.resident(k)
			if err != nil {
				t.Errorf("resident: %v", err)
				return
			}
			seen[i] = r
		}(i)
	}
	wg.Wait()
	_, built, _, _ := p.residents()
	if built != 1 {
		t.Fatalf("%d concurrent first touches of ONE organisation built %d residencies — each one "+
			"replays that tenant's whole record and allocates its own %d MiB of rings, and %d of them "+
			"are discarded", callers, built, residentRingBudget>>20, built-1)
	}
	for i, r := range seen {
		if r != seen[0] {
			t.Fatalf("caller %d was handed a different model from caller 0 — two models for one "+
				"organisation answer one question two ways", i)
		}
	}
}

// TestFold_AGapIsRetried: the fold is the moat, and it used to be given exactly
// one chance per residency. A warehouse blip on a tenant's first touch wrote the
// sentinel with a gap in it, and foldSoon returned early on ANY sentinel — so
// that organisation's own history was never folded again for the life of its
// residency, with the reason sitting on its state and nothing acting on it.
//
// Mutation proof: set f.again unconditionally false and the second fold below
// never runs.
func TestFold_AGapIsRetried(t *testing.T) {
	probe.reset(false) // the warehouse is down on this tenant's first touch
	p := newTestPlane(t)
	holdFolds(t, p) // this test drives every fold; the plane must start none of its own
	k := key(t, brandA, orgA)

	r, err := p.resident(k)
	if err != nil {
		t.Fatalf("resident: %v", err)
	}
	if f := letFold(t, p, r); f.Gap == "" {
		t.Fatalf("the fold reported no gap with the warehouse down: %+v", f)
	}

	// The warehouse comes back and the tenant has history waiting.
	probe.reset(true)
	for i := 0; i < 10; i++ {
		probe.hold(string(k), map[string]any{
			"subject_kind": kindAccount, "subject": "u_" + itoa(i),
			"bucket": surfaceAt(i + 1),
			"events": uint32(2), "spend_nano": int64(120_000_000),
		})
	}
	// Ordinary traffic: the next touch is what re-arms it.
	got := letFold(t, p, r)
	if got.Folded == 0 {
		t.Fatalf("the fold never ran again after a warehouse blip: %+v — the moat silently "+
			"never applies to that organisation", got)
	}
	if got.Gap != "" {
		t.Fatalf("the retry still reports a gap: %+v", got)
	}

	// ...and a fold that SUCCEEDED is not re-armed, which is the other half of the
	// rule and the reason a plain "always retry" would not do: without it every
	// touch of every resident re-folds, and the ticket pool is the warehouse's only
	// protection from that.
	p.mu.Lock()
	again := p.folded[k].again
	p.mu.Unlock()
	if again {
		t.Fatal("a clean fold re-armed itself — every touch would re-fold this organisation")
	}
}

// TestFoldMark_SurvivesTheWire: a watermark is only a watermark if the READ can
// express it.
//
// [bucketMark] names a bucket in order to EXCLUDE it, and the exclusion happens
// in the warehouse: the read is `bucket >= ?` with the bound rendered by
// [tsLiteral], whose format is second-grained. A mark one nanosecond past the
// bucket is therefore truncated back ONTO the bucket it meant to exclude the
// moment it is bound, and the fold re-reads a window it has already applied on
// every retry. The mark and the transport have to agree about resolution or the
// mark is decoration.
//
// This is measured as the round trip rather than through the fold, deliberately:
// [replayable] applies the window a SECOND time in memory, where the nanosecond
// still exists, so the re-read row is silently dropped downstream and no
// behavioural test upstream of it can see the defect. A property that is only
// visible at one seam is asserted at that seam.
//
// Mutation proof: step [bucketMark] by a nanosecond and this fails.
func TestFoldMark_SurvivesTheWire(t *testing.T) {
	bucket := horizon(time.Now().UTC())
	mark := bucketMark(bucket, time.Time{})
	if !mark.After(bucket) {
		t.Fatalf("the mark %s does not exclude the bucket %s it names", mark, bucket)
	}
	// ...and it still excludes it after a round trip through the wire, which is
	// where the exclusion actually happens.
	onWire, err := time.Parse("2006-01-02 15:04:05", tsLiteral(mark))
	if err != nil {
		t.Fatalf("the mark does not render as a bound: %v", err)
	}
	if !bucket.Before(onWire.UTC()) {
		t.Fatalf("the mark %s binds as %q, which does NOT exclude the bucket %s it names — every "+
			"retry re-reads and re-applies a bucket the fold already folded",
			mark.Format(time.RFC3339Nano), tsLiteral(mark), bucket)
	}
	// And it cannot skip the NEXT bucket: surface buckets are a whole grain apart,
	// so a mark that excluded one would lose that history for good.
	if next := bucket.Add(featureBucket); next.Before(onWire.UTC()) {
		t.Fatalf("the mark %s binds as %q, which also excludes the next bucket %s — the fold "+
			"would skip it for good", mark, tsLiteral(mark), next)
	}
}

// TestFold_MarksOnlyWhatTheSurfaceCanHold: the fold watermark may name only what
// the surface is known to CONTAIN.
//
// The rollup deliberately stops [rollLag] behind the present, because the source
// planes are written asynchronously and the newest minutes are not complete. The
// fold used to read and mark to `now` regardless — 10 to 15 minutes past anything
// the rollup had written. The watermark then said "folded through now" about
// buckets that did not exist yet; the next rollup wrote them; and because the
// mark was already past them the fold never came back. Every fold cycle dropped
// that organisation's most recent history, permanently, while the model went on
// reporting itself warm — which is the silent-quiet failure this app exists to
// refuse, on a schedule.
//
// Mutation proof: in [plane.warm] set `end := now` instead of `end := horizon(now)`
// and the late bucket below is never folded.
func TestFold_MarksOnlyWhatTheSurfaceCanHold(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)

	base := time.Now().UTC().Truncate(featureBucket)
	clock := base
	p.now = func() time.Time { return clock }

	// A fold over an EMPTY surface. It still moves the mark — the question this
	// test asks is HOW FAR.
	if n, err := p.warm(context.Background(), k); err != nil || n != 0 {
		t.Fatalf("warm over an empty surface read %d, err=%v", n, err)
	}
	r, err := p.resident(k)
	if err != nil {
		t.Fatalf("resident: %v", err)
	}
	r.mu.Lock()
	warmed := r.warmed
	r.mu.Unlock()
	if warmed.After(horizon(base)) {
		t.Fatalf("the fold marked %s, past the surface's horizon of %s — it has claimed buckets the rollup is not allowed to have written",
			warmed, horizon(base))
	}

	// The rollup now writes the bucket that was still filling when that fold ran.
	// It is in the PAST relative to the fold that already happened, which is the
	// ordinary case and not an exotic one.
	late := horizon(base)
	probe.hold(string(k), map[string]any{
		"subject_kind": kindAccount, "subject": "u_late", "bucket": late,
		"events": uint32(3), "spend_nano": int64(100_000_000),
	})
	clock = base.Add(2 * rollLag) // ...and time moves on, so that bucket is now inside the horizon

	n, err := p.warm(context.Background(), k)
	if err != nil {
		t.Fatalf("warm: %v", err)
	}
	if n != 1 {
		t.Fatalf("the fold read %d buckets, want 1 — the watermark was advanced past a bucket the "+
			"rollup had not written yet, so that organisation's history is gone and nothing says so", n)
	}
}

// letFold releases ONE fold ticket, arms this tenant's fold the way a request
// does, waits for the report, and takes the ticket back.
//
// Every other ticket stays held by [holdFolds], so the fold this drives is the
// only one in the process. Driving it through [plane.foldSoon] rather than
// calling [plane.fold] directly is the point: the ARMING RULE is what is under
// test, and calling the fold by hand would step over it.
func letFold(t *testing.T, p *plane, r *resident) fold {
	t.Helper()
	select {
	case <-p.folds:
	default:
		t.Fatal("no fold ticket to release")
	}
	p.mu.Lock()
	p.foldSoon(r)
	armed := p.folded[r.key]
	p.mu.Unlock()
	if armed.Gap != foldRunning {
		t.Fatalf("the fold was not armed; foldSoon declined to retry: %+v", armed)
	}
	f := awaitFold(t, p, r.key, func(f fold) bool { return f.Gap != foldRunning })
	// Back to "every ticket held" — which is also how this waits for the goroutine
	// to have finished with it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case p.folds <- struct{}{}:
			return f
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the fold never released its ticket")
		}
		time.Sleep(time.Millisecond)
	}
}

// awaitFold waits for the plane's own background fold to reach a state. The
// fold is a goroutine on the plane's waitgroup, and that group also carries the
// scheduler and the snapshot sweeper — which run for the life of the plane — so
// a test cannot simply Wait on it.
func awaitFold(t *testing.T, p *plane, k tenant, done func(fold) bool) fold {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f := p.surface(k)
		if done(f) {
			return f
		}
		if time.Now().After(deadline) {
			t.Fatalf("the fold never reached the expected state: %+v", f)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestFold_APartialFoldDoesNotReTeach: a fold reads up to maxRows under a
// two-minute deadline, so stopping partway is the ORDINARY case. The watermark
// used to move only after the LAST row, so every retry re-taught everything the
// last attempt had already applied — and the masses stopped describing the
// traffic and started describing how often the fold was interrupted.
//
// Mutation proof: move the r.warmed advance back out of the loop (set it only
// after the last row) and the model below learns the early buckets twice.
func TestFold_APartialFoldDoesNotReTeach(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	// Enough buckets that the cancellation below lands MID-LOOP with three orders
	// of magnitude of margin: the watcher reacts in tens of microseconds and the
	// remaining buckets are milliseconds of work.
	const buckets = 5_000
	for i := 0; i < buckets; i++ {
		probe.hold(string(k), map[string]any{
			"subject_kind": kindAccount, "subject": "u_" + itoa(i%4),
			"bucket": surfaceAt(i + 1),
			"events": uint32(1), "spend_nano": int64(1_000_000),
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopAfter(t, p, k, cancel, 100)
	applied, err := p.warm(ctx, k)
	if err == nil {
		t.Fatalf("the fold ran to completion (%d of %d) — this test proves nothing unless the fold "+
			"is interrupted", applied, buckets)
	}
	if applied == 0 {
		t.Fatal("the fold was interrupted before it applied anything — this test proves nothing " +
			"unless some buckets were applied and some were not")
	}
	// The retry, unbounded this time.
	rest, err := p.warm(context.Background(), k)
	if err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
	st, _, err := p.state(k)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if st.Learned > int64(buckets) {
		t.Fatalf("a %d-bucket surface taught the model %d times (%d applied, then %d) — a partial fold "+
			"re-teaches everything it already applied, and its masses count the interruptions",
			buckets, st.Learned, applied, rest)
	}
	if st.Learned < int64(buckets) {
		t.Fatalf("a %d-bucket surface taught the model only %d times — the partial fold lost the "+
			"buckets it had not reached", buckets, st.Learned)
	}
}

// stopAfter cancels once the tenant's model has learned n events, which is how a
// deadline lands mid-fold.
func stopAfter(t *testing.T, p *plane, k tenant, cancel context.CancelFunc, n int) {
	t.Helper()
	r, err := p.resident(k)
	if err != nil {
		t.Fatalf("resident: %v", err)
	}
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			r.mu.Lock()
			learned := r.mod.State(string(k)).Learned
			r.mu.Unlock()
			if learned >= int64(n) {
				cancel()
				return
			}
			time.Sleep(50 * time.Microsecond)
		}
	}()
}

// TestMasses_SurviveAnUngracefulStop: the learned state used to be written down
// only at shutdown — and a shutdown hook runs on the graceful path and on no
// other. An OOM kill, a lost node or a forced delete threw away everything a
// long-lived resident had learned, and a model that comes back empty REFUSES to
// score, which reads as clean to anything that does not check the refusal.
//
// This test never calls close: the second plane reads what the first wrote while
// it was running.
//
// Mutation proof: delete the saveEvery watermark in plane.learn and the second
// plane comes back with nothing.
func TestMasses_SurviveAnUngracefulStop(t *testing.T) {
	probe.reset(true)
	dir := t.TempDir()
	k := key(t, brandA, orgA)

	p := planeAt(t, dir)
	holdFolds(t, p)
	teach(t, p, k, stream(saveEvery+50, time.Now().UTC().Add(-3*time.Hour)))
	before, _, err := p.state(k)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if before.Learned == 0 {
		t.Fatal("the first plane learned nothing — the test proves nothing")
	}
	// NO close(). The pod was killed: the context is cancelled and the files are
	// released, and nothing writes a shutdown snapshot.
	p.stop()
	if err := p.shelf.CloseAll(); err != nil {
		t.Fatalf("release the files: %v", err)
	}

	second := planeAt(t, dir)
	defer func() { _ = second.close() }()
	holdFolds(t, second)
	after, _, err := second.state(k)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if after.Learned == 0 {
		t.Fatalf("an ungraceful stop lost every one of %d learned events — the model comes back "+
			"refusing to score, which reads as clean", before.Learned)
	}
	if after.Learned < int64(saveEvery) {
		t.Fatalf("an ungraceful stop kept %d of %d learned events, which is more than the %d-event "+
			"watermark allows to be at risk", after.Learned, before.Learned, saveEvery)
	}
}

// TestMasses_AreWrittenDownOnAnIntervalToo: the OTHER half of surviving an
// ungraceful stop, and the half a count watermark cannot cover.
//
// [saveEvery] bounds the loss of a model learning in a LOOP. A model that learned
// a little and then went quiet never reaches it, so without the interval its last
// few events sit in memory indefinitely and an OOM kill takes them — and the
// tenant comes back with a model that refuses to score, which reads as clean.
// Two triggers because there are two ways to be at risk, and neither covers the
// other.
//
// Mutation proof: delete the save inside [plane.sweep] and the second plane below
// comes back having learned nothing.
func TestMasses_AreWrittenDownOnAnIntervalToo(t *testing.T) {
	probe.reset(true)
	dir := t.TempDir()
	k := key(t, brandA, orgA)

	p := planeAt(t, dir)
	holdFolds(t, p)
	// FEWER than the watermark, so the count trigger cannot fire and only the
	// interval can. A test that taught saveEvery events would pass either way.
	teach(t, p, k, stream(saveEvery/10, time.Now().UTC().Add(-3*time.Hour)))
	before, _, err := p.state(k)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if before.Learned == 0 || before.Learned >= int64(saveEvery) {
		t.Fatalf("the first plane learned %d events; this test needs some, and fewer than the "+
			"%d-event watermark, or it proves nothing", before.Learned, saveEvery)
	}

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go p.sweep(ctx, time.Millisecond)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if snap, _, _, err := p.load(k); err == nil && snap != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the sweeper never wrote a resident down — a model that learns a little and then " +
				"goes quiet is lost by any ungraceful stop, and comes back refusing to score")
		}
		time.Sleep(time.Millisecond)
	}
	stop()

	// The pod is killed: no close(), so nothing writes a shutdown snapshot.
	p.stop()
	if err := p.shelf.CloseAll(); err != nil {
		t.Fatalf("release the files: %v", err)
	}
	second := planeAt(t, dir)
	defer func() { _ = second.close() }()
	holdFolds(t, second)
	after, _, err := second.state(k)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if after.Learned != before.Learned {
		t.Fatalf("an ungraceful stop kept %d of %d learned events — the interval trigger is what "+
			"covers a model below the %d-event watermark", after.Learned, before.Learned, saveEvery)
	}
}

// TestRestore_CannotChooseTheGeometry: the engine regenerates a model's trees
// from the snapshot's SEED, so a caller that supplies the seed chooses WHERE THE
// REGIONS ARE — the one thing a snapshot is supposed not to disclose, arriving
// through the other door. The geometry is this plane's, minted here and carried
// with the tenant's own state.
//
// Mutation proof: delete the seed comparison in plane.install and the tampered
// snapshot below is adopted.
func TestRestore_CannotChooseTheGeometry(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	teach(t, p, k, stream(120, time.Now().UTC().Add(-2*time.Hour)))
	snap, ok, err := p.pin(k)
	if err != nil || !ok {
		t.Fatalf("pin: %v (ok=%v)", err, ok)
	}
	// Its own state, unchanged, is adoptable.
	if _, _, err := p.adopt(k, snap); err != nil {
		t.Fatalf("an organisation could not adopt its own snapshot: %v", err)
	}
	// The same masses under a geometry the caller picked is not.
	chosen := snap
	chosen.Seed = snap.Seed ^ 0xdeadbeef
	if _, _, err := p.adopt(k, chosen); err == nil {
		t.Fatal("a caller chose its model's tree geometry — which is choosing where the dense " +
			"regions are, and therefore where activity can be hidden")
	}
}
