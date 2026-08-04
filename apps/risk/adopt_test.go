package risk

// adopt_test.go holds the claim that /v1/risk/search is an OPERATION and not a
// leaflet: the shape a search finds is a shape the organisation that asked for it can
// actually run.
//
// The hole this closes was not cosmetic. A run answered with a winning topology and
// nothing could promote it, because the adoption path refused a shape change and a
// winner is a different shape BY DEFINITION — different trees, different depth,
// different window, different blend. So the plane produced advice nobody could take,
// on the very surface that replaced Katib.
//
// Every test below is an experiment that would refute one clause of that:
//
//	ADOPTABLE     a run's winner arrives as a published value, and adopting it moves
//	              the live model into the winner's space.
//	ISOLATED      the value belongs to ONE organisation. Another naming its address
//	              replants nothing, and its own space is untouched afterwards.
//	DURABLE       an adopted shape survives a rollout. Without this the model
//	              silently returns to the deployment's default shape on the next
//	              restart, which is the same class of defect as a control switching
//	              itself off.
//	STILL GATED   replanting did not become a way around the checks the refusal used
//	              to carry: the geometry seed and the digest still refuse.
//	AFFORDABLE    the widest shape the grid can win at fits the bound its own store
//	              enforces, MEASURED rather than asserted.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"
)

// otherShape is a point of the search grid that is NOT the deployment's default, so
// installing it is a real replant rather than a restore that happens to work. It is
// taken off the grid rather than invented: a shape no candidate can win at would make
// these tests prove something about a shape nobody can reach.
func otherShape(t *testing.T, from shape) shape {
	t.Helper()
	for _, c := range candidates() {
		if c.shape != from {
			return c.shape
		}
	}
	t.Fatal("every point of the search grid is the shape already in force, so no test here can " +
		"exercise a replant")
	return shape{}
}

// shapeInForce is the shape an organisation's model is actually running, read off the
// residency the next score would use.
func shapeInForce(t *testing.T, p *plane, k tenant) (shape, string) {
	t.Helper()
	r, err := p.resident(k)
	if err != nil {
		t.Fatalf("resident(%s): %v", k, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return shapeOf(r.cfg), r.shape
}

// fittedValue teaches an organisation, fits ONE named shape over that history and
// publishes it — the same two steps [plane.fitWinner] takes, driven directly so a test
// can choose the shape instead of waiting for the grid to rank sixty-four of them over
// a warehouse fixture.
//
// It goes through fitWinner itself rather than reproducing it, because a test that
// reimplemented the publication would be measuring the test.
func fittedValue(t *testing.T, p *plane, k tenant, s shape, hist []observation) value {
	t.Helper()
	v, err := p.fitWinner(k, topology{shape: s, Review: 0.01}, hist, time.Now().UTC())
	if err != nil {
		t.Fatalf("fitWinner(%s): %v", k, err)
	}
	if v.Address == "" {
		t.Fatalf("fitWinner(%s) published a value with no name", k)
	}
	return v
}

// TestAdopt_InstallsAShapeTheModelWasNotRunning is THE requirement.
//
// A winning shape is published as a value; adopting that value moves the live model
// into the winner's space. Both halves are asserted, because either one alone passes
// with the hole still open: a value that names a shape nobody can install is exactly
// what the plane produced before this, and a model that changes shape without a value
// to name is a change with no audit trail.
//
// Mutation proof, run rather than assumed: make [plane.install] refuse a shape change
// again (drop the replant branch) and this fails on the adopt call with "shape does not
// match the running inventory" — which is the error every search winner used to get.
func TestAdopt_InstallsAShapeTheModelWasNotRunning(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	hist := stream(600, time.Now().UTC().Add(-6*time.Hour))
	teach(t, p, k, hist)

	was, wasDigest := shapeInForce(t, p, k)
	want := otherShape(t, was)
	v := fittedValue(t, p, k, want, hist)

	// The VALUE records the winner's space, and it is a different space from the one
	// running — otherwise the adoption below would prove nothing.
	if v.Shape == wasDigest {
		t.Fatalf("the fitted value names the space already in force (%s), so this test cannot "+
			"exercise a replant", v.Shape)
	}
	m, ok, err := p.modelAt(k, v.Address)
	if err != nil || !ok {
		t.Fatalf("modelAt: ok=%v err=%v", ok, err)
	}
	if m.Shape != want {
		t.Fatalf("the published value records shape %+v, want the winner's %+v", m.Shape, want)
	}
	if m.Learned == 0 {
		t.Fatal("the published value has learned nothing, so adopting it would install a model that " +
			"decides nothing")
	}

	// ADOPT IT.
	st, _, err := p.adopt(k, v.Address)
	if err != nil {
		t.Fatalf("adopt: %v — a search winner is still not adoptable", err)
	}
	if st.Digest != v.Shape {
		t.Fatalf("after adopting, the model runs space %s and the value named %s", st.Digest, v.Shape)
	}
	if st.Learned != m.Learned {
		t.Fatalf("the adopted model has learned %d events, the value holds %d", st.Learned, m.Learned)
	}
	now, nowDigest := shapeInForce(t, p, k)
	if now != want {
		t.Fatalf("the residency runs shape %+v after adopting %+v", now, want)
	}
	if nowDigest != v.Shape {
		t.Fatalf("the residency cites space %s while running the value's %s — every score would cite "+
			"a shape it was not decided under", nowDigest, v.Shape)
	}

	// THE APPETITE IS UNTOUCHED. A shape is state and an appetite is policy; adopting
	// one must not restate the other, or every adoption is an unrecorded policy change.
	if st.Config.Appetite.Review != 0.01 || !st.Config.Shadow {
		t.Fatalf("adopting a shape moved the regime: review=%v shadow=%v, want the stated 0.01 and shadow",
			st.Config.Appetite.Review, st.Config.Shadow)
	}
}

// TestAdopt_AForeignOrgCannotReplantWithAnotherOrgsShape is the isolation requirement
// ON THE REPLANT PATH.
//
// [TestAddress_AForeignOrgResolvesNothing] already holds it for a restore into the
// space already running. Replanting is a NEW power — one call now rebuilds the store —
// so the same experiment is run against it: organisation A fits a shape and publishes
// it, organisation B is handed that exact address, and B must end up with its own space,
// its own masses and nothing of A's.
func TestAdopt_AForeignOrgCannotReplantWithAnotherOrgsShape(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	a, b := key(t, brandA, orgA), key(t, brandA, orgB)
	hist := stream(600, time.Now().UTC().Add(-6*time.Hour))
	teach(t, p, a, hist)
	teach(t, p, b, hist)

	wasA, _ := shapeInForce(t, p, a)
	want := otherShape(t, wasA)
	mine := fittedValue(t, p, a, want, hist)

	// B's own space and masses BEFORE, so "untouched" below is a measurement.
	wasB, wasBDigest := shapeInForce(t, p, b)
	beforeB, _, err := p.state(b)
	if err != nil {
		t.Fatalf("state(B): %v", err)
	}
	if beforeB.Learned == 0 {
		t.Fatal("organisation B learned nothing, so this test proves nothing about B")
	}
	if wasB == want {
		t.Fatal("organisation B is already running the shape A published, so a leak would be invisible")
	}

	// THE ADDRESS IS REAL AND IT IS A'S. B naming it must resolve nothing — not because
	// the name is unguessable, but because the name is not the authority.
	if _, _, err := p.adopt(b, mine.Address); err == nil {
		t.Fatal("organisation B replanted its model with organisation A's shape by naming its address")
	}
	if m, ok, err := p.modelAt(b, mine.Address); err != nil {
		t.Fatalf("modelAt(B): %v", err)
	} else if ok {
		t.Fatalf("organisation B read organisation A's model by name: shape=%+v", m.Shape)
	}

	// B'S SPACE IS ITS OWN AFTERWARDS, which is the half a refusal alone does not prove:
	// a replant that half ran would leave B in A's space with B's masses.
	nowB, nowBDigest := shapeInForce(t, p, b)
	if nowB != wasB || nowBDigest != wasBDigest {
		t.Fatalf("organisation B's space moved from %+v (%s) to %+v (%s) while another organisation's "+
			"address was pushed at it", wasB, wasBDigest, nowB, nowBDigest)
	}
	afterB, _, err := p.state(b)
	if err != nil {
		t.Fatalf("state(B) after: %v", err)
	}
	if afterB.Learned != beforeB.Learned {
		t.Fatalf("organisation B's model moved from %d to %d learned events", beforeB.Learned, afterB.Learned)
	}

	// And A's own adoption still works, so the refusal above is about WHOSE value it is
	// and not about replanting being broken.
	if _, _, err := p.adopt(a, mine.Address); err != nil {
		t.Fatalf("adopt(A) of A's own value: %v", err)
	}
}

// TestAdopt_TheFittedValueIsFitUnderTheOrganisationsOwnGeometry: the grid ranks under
// one fixed reference seed so a comparison is a comparison, and the value an
// organisation ADOPTS is fitted under that organisation's own seed — geometry an
// outsider cannot predict, and therefore cannot probe for a region to hide in.
//
// Two organisations given IDENTICAL events must publish DIFFERENT values for the same
// winning shape. Same masses under two partitions of the space are two different
// models, so one address for both would mean the plane had minted them under a
// constant.
func TestAdopt_TheFittedValueIsFitUnderTheOrganisationsOwnGeometry(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	a, b := key(t, brandA, orgA), key(t, brandA, orgB)
	hist := stream(600, time.Now().UTC().Add(-6*time.Hour))
	teach(t, p, a, hist)
	teach(t, p, b, hist)

	was, _ := shapeInForce(t, p, a)
	want := otherShape(t, was)
	mine, theirs := fittedValue(t, p, a, want, hist), fittedValue(t, p, b, want, hist)
	if mine.Address == theirs.Address {
		t.Fatal("two organisations fitted the same shape over identical events to ONE value, so the " +
			"fit ran under a shared geometry — the partition of the space is then predictable")
	}
	// The SHAPE is the same for both — that is the point of a shape — so it is the
	// geometry that differs.
	if mine.Shape != theirs.Shape {
		t.Fatalf("the same shape produced two spaces: %s and %s", mine.Shape, theirs.Shape)
	}
	ma, _, err := p.modelAt(a, mine.Address)
	if err != nil {
		t.Fatalf("modelAt(A): %v", err)
	}
	mb, _, err := p.modelAt(b, theirs.Address)
	if err != nil {
		t.Fatalf("modelAt(B): %v", err)
	}
	if ma.Seed == mb.Seed {
		t.Fatal("both fitted values carry one geometry seed, so the fit did not use each " +
			"organisation's own")
	}
	// ...and neither of them landed in the grid's REFERENCE partition, which is derived
	// from a constant and is therefore predictable from source. Measured, not assumed:
	// the reference partition is whatever ranking A actually plants in.
	ranked, _, err := replay(a, topology{shape: want, Review: 0.01}, nil, hist)
	if err != nil {
		t.Fatalf("rank under the reference partition: %v", err)
	}
	ref, held := ranked.Snapshot(string(a))
	if !held {
		t.Fatal("the reference sandbox fitted nothing")
	}
	if ma.Seed == ref.Seed {
		t.Fatalf("the fitted value landed in the grid's reference partition (%d), which is derived "+
			"from a constant — the geometry this organisation runs would be predictable from source",
			ref.Seed)
	}
}

// TestAdopt_AnAdoptedShapeSurvivesARollout is the defect a replant would otherwise
// introduce and nothing would report.
//
// A residency is planted at the deployment's own shape and then restored from its own
// resume row. An organisation that has adopted a searched shape holds masses that
// belong to a DIFFERENT space, so a restart that planted the default would find a
// shape mismatch, log a warning, and return that organisation to warming — on the
// default shape, indefinitely, once per rollout. A warming model declines to score,
// which reads as a clean result to anything not checking the refusal.
//
// So the resume row's shape is what the next process plants. Mutation proof: pass a
// zero shape at the [plane.open] install and this fails with the model back on its old
// space and its learned count reset.
func TestAdopt_AnAdoptedShapeSurvivesARollout(t *testing.T) {
	probe.reset(true)
	dir := t.TempDir()
	k := key(t, brandA, orgA)
	hist := stream(600, time.Now().UTC().Add(-6*time.Hour))

	first := planeAt(t, dir)
	teach(t, first, k, hist)
	was, _ := shapeInForce(t, first, k)
	want := otherShape(t, was)
	v := fittedValue(t, first, k, want, hist)
	before, _, err := first.adopt(k, v.Address)
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if before.Digest != v.Shape {
		t.Fatalf("the adoption did not take: running %s, adopted %s", before.Digest, v.Shape)
	}
	if err := first.close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := planeAt(t, dir)
	defer func() { _ = second.close(context.Background()) }()
	after, _, err := second.state(k)
	if err != nil {
		t.Fatalf("state after restart: %v", err)
	}
	if after.Digest != before.Digest {
		t.Fatalf("after a rollout the model runs space %s, adopted %s — the organisation was "+
			"silently returned to the deployment's default shape", after.Digest, before.Digest)
	}
	if after.Learned != before.Learned {
		t.Fatalf("after a rollout the model has learned %d events, had %d — the adopted shape's "+
			"masses were refused and the tenant returned to warming", after.Learned, before.Learned)
	}
	if now, _ := shapeInForce(t, second, k); now != want {
		t.Fatalf("after a rollout the residency runs shape %+v, adopted %+v", now, want)
	}
}

// TestAdopt_TheFoldWatermarkTravelsWithTheValue: the watermark is IN the address
// because two models with identical masses reached by different routes disagree about
// what is left to fold. So it has to come back with the value, and leaving it where it
// was is not a rounding error — a rollback would keep the newer mark, the fold between
// the two would never be read again, and the model would permanently lack history it
// believes it already has.
func TestAdopt_TheFoldWatermarkTravelsWithTheValue(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	hist := stream(400, time.Now().UTC().Add(-4*time.Hour))
	teach(t, p, k, hist)

	// Publish at an OLD watermark, then move the residency's watermark forward as a
	// fold would.
	old := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	was, _ := shapeInForce(t, p, k)
	v, err := p.fitWinner(k, topology{shape: otherShape(t, was), Review: 0.01}, hist, old)
	if err != nil {
		t.Fatalf("fitWinner: %v", err)
	}
	r, err := p.resident(k)
	if err != nil {
		t.Fatalf("resident: %v", err)
	}
	moved := time.Now().UTC().Truncate(time.Second)
	r.mu.Lock()
	r.warmed = moved
	r.mu.Unlock()

	if _, _, err := p.adopt(k, v.Address); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	r.mu.Lock()
	got := r.warmed
	r.mu.Unlock()
	if !got.Equal(old) {
		t.Fatalf("after adopting a value warmed to %s the residency's watermark is %s (it was moved to "+
			"%s) — the fold between them will never be read again", old, got, moved)
	}
}

// TestAdopt_StillRefusesAValueFromAnotherGeometry: replanting must not have become a
// way around the check the refusal used to carry.
//
// The seed is what says WHERE the regions are. Masses learned in one partition and
// scored in another are wrong in a way nothing reports, so a value whose seed is not
// the one this organisation's model holds is refused — whether or not it also changes
// the shape. The check runs against the model ALREADY RUNNING, before the replant
// builds a new one, which is the only order in which it means anything.
func TestAdopt_StillRefusesAValueFromAnotherGeometry(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	hist := stream(600, time.Now().UTC().Add(-6*time.Hour))
	teach(t, p, k, hist)

	was, wasDigest := shapeInForce(t, p, k)
	want := otherShape(t, was)
	// A value fitted under the GRID's reference partition rather than this
	// organisation's own — which is what a value minted under a shared constant, or
	// lifted from elsewhere, would be.
	mod, _, err := replay(k, topology{shape: want, Review: 0.01}, nil, hist)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	snap, held := mod.Snapshot(string(k))
	if !held {
		t.Fatal("the sandbox fitted nothing")
	}
	foreign, _, err := p.mint(k, model{Snapshot: snap, Shape: want}, time.Now().UTC())
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, _, err := p.adopt(k, foreign.Address); err == nil {
		t.Fatal("a value fitted under a different geometry was adopted, so the masses now describe " +
			"trees the model does not have")
	}
	if now, nowDigest := shapeInForce(t, p, k); now != was || nowDigest != wasDigest {
		t.Fatalf("the refused adoption still moved the space: %+v (%s) -> %+v (%s)",
			was, wasDigest, now, nowDigest)
	}
}

// TestAdopt_RefusesAValueWhoseShapeDoesNotProduceItsDigest: the engine's digest is a
// hash OVER the shape, so rebuilding the space from the recorded numbers and comparing
// digests PROVES the masses describe the space they are about to enter. The stored
// numbers are checked, never trusted.
//
// Two ways a value can fail that, and both must refuse:
//
//	NO SHAPE     a body that records no shape at all. Defaulting it to the
//	             deployment's own would be a guess about mass counters.
//	WRONG SHAPE  a body whose recorded shape hashes to something else — a row edited,
//	             a write half applied, an encoding that drifted.
func TestAdopt_RefusesAValueWhoseShapeDoesNotProduceItsDigest(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	hist := stream(600, time.Now().UTC().Add(-6*time.Hour))
	teach(t, p, k, hist)

	was, wasDigest := shapeInForce(t, p, k)
	want := otherShape(t, was)
	v := fittedValue(t, p, k, want, hist)
	m, ok, err := p.modelAt(k, v.Address)
	if err != nil || !ok {
		t.Fatalf("modelAt: ok=%v err=%v", ok, err)
	}

	for name, broken := range map[string]shape{
		"no shape":    {},
		"wrong trees": {Trees: want.Trees + 5, Depth: want.Depth, Window: want.Window, Blend: want.Blend},
		"wrong depth": {Trees: want.Trees, Depth: want.Depth + 1, Window: want.Window, Blend: want.Blend},
		"wrong blend": {Trees: want.Trees, Depth: want.Depth, Window: want.Window, Blend: want.Blend / 2},
	} {
		body, err := json.Marshal(model{Snapshot: m.Snapshot, Shape: broken})
		if err != nil {
			t.Fatalf("%s: encode: %v", name, err)
		}
		sh, err := p.for_(k)
		if err != nil {
			t.Fatalf("%s: shelf: %v", name, err)
		}
		if _, err := sh.db.Exec(`UPDATE published SET body = ? WHERE tenant = ? AND address = ?`,
			body, string(k), v.Address); err != nil {
			t.Fatalf("%s: rewrite body: %v", name, err)
		}
		if _, _, err := p.adopt(k, v.Address); err == nil {
			t.Fatalf("%s: a value whose recorded shape does not produce its space was adopted", name)
		}
		if now, nowDigest := shapeInForce(t, p, k); now != was || nowDigest != wasDigest {
			t.Fatalf("%s: the refused adoption still moved the space: %+v (%s) -> %+v (%s)",
				name, was, wasDigest, now, nowDigest)
		}
	}
}

// TestModel_TheWidestShapeFitsItsOwnBound MEASURES [maxModelBodyBytes] against the
// widest shape the grid DECLARES, which is what makes the constant a bound rather than
// a hope.
//
// It has to be measured because adopting a searched shape is what made a wide shape
// reachable at all: a published value used to only ever come from a residency running
// the deployment's own shape, so a bound covering that one shape covered every value
// that could exist. Now the space a search wins in is a space an organisation can
// publish, and a bound that did not cover it would turn up as a 413 on somebody's
// adoption instead of red here.
//
// EVERY REGION IS OCCUPIED AND EVERY MASS CARRIES A FULL MANTISSA, and that is the
// whole reason this is a construction and not a fitted sandbox. Measuring a real fit
// understates it by an order of magnitude: a deep tree over a short history leaves most
// of its regions at zero, and a zero encodes in two bytes where a blended mass encodes
// in twenty. A busy organisation's tree is the one this bound has to hold, so the worst
// case is built rather than hoped for. The invariant the engine checks on restore is
// deliberately not maintained here — this measures an ENCODING, and a body is refused on
// size before anything reads a mass.
//
// It measures the DECLARED grid and not what [candidates] currently returns, because
// those differ: maxTrials truncates 243 declared points to 64, and all 64 sit on the
// grid's narrowest tree count. Sizing the store on what the grid SAYS it searches means
// correcting that truncation is a change to the search and not also a change to storage.
func TestModel_TheWidestShapeFitsItsOwnBound(t *testing.T) {
	probe.reset(true)
	k := key(t, brandA, orgA)

	widest := shape{}
	for _, n := range grid.Trees {
		if n > widest.Trees {
			widest.Trees = n
		}
	}
	for _, d := range grid.Depth {
		if d > widest.Depth {
			widest.Depth = d
		}
	}
	for _, w := range grid.Window {
		if w > widest.Window {
			widest.Window = w
		}
	}
	for _, b := range grid.Blend {
		if b > widest.Blend {
			widest.Blend = b
		}
	}
	if widest.Trees == 0 || widest.Depth == 0 {
		t.Fatal("the search grid declares no shape, so there is nothing to size the store on")
	}
	// Every point the grid can actually hand out is inside that, so the bound below
	// covers the reachable space as well as the declared one.
	for _, c := range candidates() {
		if c.Trees > widest.Trees || c.Depth > widest.Depth || c.Window > widest.Window {
			t.Fatalf("candidate %+v is wider than the widest shape the grid declares (%+v)", c.shape, widest)
		}
	}

	// A real fit at that shape, purely to borrow the engine's own dimensions: the
	// snapshot format version and the width of the score distribution.
	mod, _, err := replay(k, topology{shape: widest, Review: 0.01}, nil, stream(64, time.Now().UTC().Add(-time.Hour)))
	if err != nil {
		t.Fatalf("fit the widest shape: %v", err)
	}
	snap, held := mod.Snapshot(string(k))
	if !held {
		t.Fatal("the widest shape fitted nothing")
	}
	sparse, err := json.Marshal(model{Snapshot: snap, Shape: widest})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	// OCCUPY IT. A third is a mass with a full mantissa in every position, which is what
	// a folded window holds.
	occupy := func(masses [][]float64) {
		for _, tree := range masses {
			for i := range tree {
				tree[i] = float64(i+1) / 3
			}
		}
	}
	occupy(snap.Ref)
	occupy(snap.Cur)
	for i := range snap.Hist {
		snap.Hist[i] = float64(i+1) / 7
	}
	snap.Cut = 1.0 / 3
	body, err := json.Marshal(model{Snapshot: snap, Shape: widest})
	if err != nil {
		t.Fatalf("encode occupied: %v", err)
	}
	t.Logf("the widest shape the grid declares (%d trees, depth %d = %d regions, window %d) encodes to "+
		"%d bytes fully occupied and %d sparse; the bound is %d and one row costs at most %d",
		widest.Trees, widest.Depth, 1<<(widest.Depth+1)-1, widest.Window,
		len(body), len(sparse), maxModelBodyBytes, maxModelRowBytes)
	if len(body) > maxModelBodyBytes {
		t.Fatalf("the widest shape the grid declares encodes to %d bytes fully occupied, over the "+
			"%d-byte bound every published value is refused past — so a busy organisation adopting that "+
			"shape cannot publish its own model, and the plane is back to producing advice nobody can take",
			len(body), maxModelBodyBytes)
	}
	// ...and the retention this bound implies is still a history worth having.
	if modelValues < 10 {
		t.Fatalf("the byte budget retains %d values at %d bytes a row, which is not a champion, a "+
			"challenger and eight points to roll back to", modelValues, maxModelRowBytes)
	}
}

// TestShape_IsTheHalfOfAConfigThatBelongsToTheState pins the split the whole adoption
// path rests on: a shape moves the SPACE and nothing else.
//
// Braided together in one config — as they were — installing a shape would restate an
// appetite nobody stated, and restating an appetite would look like a different model.
// Both directions are asserted, because each one alone is satisfied by a shape that
// does nothing.
func TestShape_IsTheHalfOfAConfigThatBelongsToTheState(t *testing.T) {
	base := anomaly.Config{
		Trees: 25, Depth: 8, Window: 256, Blend: 0.25,
		Appetite: anomaly.Appetite{Review: 0.02, Sample: 0.005},
		Shadow:   true, Seed: 0xfeedface, MaxOrgs: 1,
	}
	want := shape{Trees: 40, Depth: 10, Window: 512, Blend: 0.5}
	got := want.applyTo(base)
	if shapeOf(got) != want {
		t.Fatalf("applyTo left the space at %+v, want %+v", shapeOf(got), want)
	}
	if got.Appetite != base.Appetite || got.Shadow != base.Shadow || got.Seed != base.Seed {
		t.Fatalf("applying a shape moved the regime or the geometry: appetite %+v shadow %v seed %v",
			got.Appetite, got.Shadow, got.Seed)
	}
	// A shape that states nothing leaves the config alone, which is what lets a value
	// recorded before shapes were recorded come back exactly as it was rather than
	// being replanted onto a guess.
	if bare := (shape{}).applyTo(base); bare != base {
		t.Fatalf("a shape that states nothing rewrote the config: %+v", bare)
	}
	if (shape{}).stated() {
		t.Fatal("a zero shape claims to name a space, so install would replant onto a default")
	}
	if !want.stated() {
		t.Fatal("a real shape claims to name no space")
	}
}
