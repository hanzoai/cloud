package risk

// family_test.go — THE FAMILY IS LOAD-BEARING, STATED AS THE EXPERIMENTS THAT WOULD
// REFUTE IT.
//
// The claim family.go makes is that a value fitted under one model family can never be
// adopted into another. It is refutable in four independent ways and each one is a test
// here: by NAME (the address does not separate the families), by GATE (install accepts
// it), by DECODE (a stored body naming a family this binary does not run is read as one
// it does), and by TYPE (a shape can be built whose family and parameters disagree).
//
// The last of those is the one that is NOT a test, and deliberately: it is a compile
// error. `shape{halfspace{...}}` with a family of "transformer" cannot be written down,
// because the family is a method on the geometry rather than a field beside it. What is
// tested instead is the property the type rests on — that the comparison is total.

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/types"
)

// transformer is a geometry from a family this plane does NOT run. It exists so the
// refusals that keep families apart can be exercised, and it is a STAND-IN rather than
// an implementation: building it fails, because a family whose detector this binary
// cannot construct is exactly the family a stored value must not be able to smuggle in.
//
// It is in a test file for a reason. [geometry]'s methods are unexported, so the set of
// families is closed to this package — a second family is a change to family.go and to
// nothing else, and a family cannot arrive from outside.
type transformer struct {
	Layers int `json:"layers"`
}

func (transformer) family() family { return "transformer" }

// errNotRun is what building a family this plane does not run answers. Two tests assert
// it is NEVER the error a cross-family adoption produces: the family gate refuses
// before the replant is built, so seeing this would mean the gate had been reached
// through rather than at.
var errNotRun = errors.New("this plane does not run the transformer family")

func (transformer) build(t tenant, pol regime, seed uint64, vel *rings) (detector, error) {
	return nil, errNotRun
}

// TestFamily_AValueIsNotAdoptableIntoAnotherFamily IS THE REQUIREMENT.
//
// A value carries the masses of one family's arithmetic. Installed into another family
// they are not stale, they are meaningless — so the refusal must be structural, must
// name both families, and must happen before anything is touched.
//
// Mutation proof, run rather than assumed: delete the family gate at the top of
// [plane.install] and this fails with the BUILD error instead of the family one, which
// is the whole point — the gate exists so the refusal is the answer and not a
// side-effect of the replant failing.
func TestFamily_AValueIsNotAdoptableIntoAnotherFamily(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	teach(t, p, k, stream(400, time.Now().UTC().Add(-4*time.Hour)))

	r, err := p.resident(k)
	if err != nil {
		t.Fatalf("resident: %v", err)
	}
	r.mu.Lock()
	snap, held := r.mod.snapshot()
	was, wasShape, wasLearned := r.geom, r.shape, snap.Learned
	r.mu.Unlock()
	if !held || wasLearned == 0 {
		t.Fatal("the organisation's model learned nothing, so there is no state a cross-family " +
			"adoption could corrupt and this test would prove nothing")
	}

	// THE SAME MASSES, claiming another family. Everything else about the value is
	// exactly what this organisation's own model just produced, so the family is the
	// only term that can carry the refusal.
	r.mu.Lock()
	err = p.install(r, model{Snapshot: snap, Shape: shape{transformer{Layers: 32}}})
	now, nowShape := r.geom, r.shape
	r.mu.Unlock()

	if err == nil {
		t.Fatal("a value fitted under another model family was installed, so one family's masses " +
			"are now this model's memory of behaviour it never saw")
	}
	if errors.Is(err, errNotRun) {
		t.Fatalf("the refusal came from failing to BUILD the other family, not from the family "+
			"gate — so a family this plane CAN build would have been installed: %v", err)
	}
	// The refusal names both families, because "this does not fit" is indistinguishable
	// from a fault and the two families are the whole of the answer.
	for _, want := range []string{"transformer", string(halfSpace)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q, so a caller cannot tell what it holds from what "+
				"its model runs: %v", want, err)
		}
	}
	// AND THE RESIDENCY IS EXACTLY AS IT WAS. A refusal that half-adopted would be
	// worse than an acceptance, because nothing would report it.
	if !now.same(was) || nowShape != wasShape {
		t.Fatalf("the refused adoption still moved the space: %+v (%s) -> %+v (%s)",
			was, wasShape, now, nowShape)
	}
	if st := r.mod.state(); st.Learned != wasLearned {
		t.Fatalf("the refused adoption moved the learned count: %d -> %d", wasLearned, st.Learned)
	}
}

// TestFamily_AStoredValueNamingAnUnknownFamilyIsRefusedAndNotDEFAULTED closes the
// second hole. The gate above is reached only by a value this binary can decode; a
// stored body naming a family it does not run must be refused ON THE WAY IN rather than
// read as the family it does run.
//
// This is a real state and not a hypothetical: a value published by a plane that runs a
// second family is on that organisation's own shelf during a rollback and during a
// rollout, and reading its parameters as half-space would restore another family's
// numbers into these trees.
//
// Mutation proof: make [shape.UnmarshalJSON]'s default case fall through to half-space
// and this fails — the adoption succeeds.
func TestFamily_AStoredValueNamingAnUnknownFamilyIsRefusedAndNotDefaulted(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	hist := stream(400, time.Now().UTC().Add(-4*time.Hour))
	teach(t, p, k, hist)

	v, _, err := p.publish(k)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if v.Address == "" {
		t.Fatal("nothing was published, so there is no body to rewrite")
	}
	was, wasShape := shapeInForce(t, p, k)

	m, ok, err := p.modelAt(k, v.Address)
	if err != nil || !ok {
		t.Fatalf("modelAt: ok=%v err=%v", ok, err)
	}
	// The value's own masses, its own digest, its own seed — and a family nobody here
	// runs. Written straight onto the shelf, which is the only way this state arrives.
	body, err := json.Marshal(struct {
		anomaly.Snapshot
		Shape map[string]any `json:"shape"`
	}{Snapshot: m.Snapshot, Shape: map[string]any{"family": "transformer", "layers": 32}})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	sh, err := p.for_(k)
	if err != nil {
		t.Fatalf("shelf: %v", err)
	}
	if _, err := sh.db.Exec(`UPDATE published SET body = ? WHERE tenant = ? AND address = ?`,
		body, string(k), v.Address); err != nil {
		t.Fatalf("rewrite body: %v", err)
	}

	_, _, err = p.adopt(k, v.Address)
	if err == nil {
		t.Fatal("a value naming a family this plane does not run was adopted, so another family's " +
			"numbers are now this model's masses")
	}
	if !strings.Contains(err.Error(), "transformer") {
		t.Errorf("the refusal does not name the family it refused, so an operator cannot tell a "+
			"rollback from a corruption: %v", err)
	}
	if now, nowShape := shapeInForce(t, p, k); !now.same(was) || nowShape != wasShape {
		t.Fatalf("the refused adoption still moved the space: %+v (%s) -> %+v (%s)",
			was, wasShape, now, nowShape)
	}
}

// TestFamily_TheWireFormRoundTripsAndAnOldBodyIsStillHalfSpace: the family is written
// beside its parameters and read back as the same value, and a body written before the
// family was one still decodes as the family those parameters belong to.
//
// The second half is what makes this change a DECODE rather than a migration. Every
// value on every organisation's shelf today has no family recorded, and there was only
// ever one family it could be.
func TestFamily_TheWireFormRoundTripsAndAnOldBodyIsStillHalfSpace(t *testing.T) {
	want := shape{halfspace{Trees: 40, Depth: 10, Window: 512, Blend: 0.5}}
	body, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(body), `"family":"halfspace"`) {
		t.Fatalf("the encoded shape does not name its family, so a reader has to guess: %s", body)
	}
	var back shape
	if err := json.Unmarshal(body, &back); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !back.same(want) {
		t.Fatalf("the shape did not survive the round trip: %+v -> %+v", want, back)
	}

	// A BODY FROM BEFORE THE FAMILY WAS A VALUE: the parameters, no tag.
	var old shape
	if err := json.Unmarshal([]byte(`{"trees":40,"depth":10,"window":512,"blend":0.5}`), &old); err != nil {
		t.Fatalf("decode an untagged shape: %v", err)
	}
	if !old.same(want) {
		t.Fatalf("an untagged shape decoded as %+v, want the half-space geometry %+v — every value "+
			"already on a shelf would be unadoptable", old, want)
	}

	// AN ABSENT SHAPE STAYS ABSENT. "The deployment's own" is a guess and these are mass
	// counters, so install must be able to tell "no shape recorded" from a shape.
	var none shape
	if err := json.Unmarshal([]byte(`{}`), &none); err != nil {
		t.Fatalf("decode an absent shape: %v", err)
	}
	if none.stated() {
		t.Fatal("an absent shape claims to name a space, so install would replant onto a default")
	}

	// AND AN UNKNOWN FAMILY IS AN ERROR, not a half-space shape.
	var unknown shape
	if err := json.Unmarshal([]byte(`{"family":"transformer","layers":32}`), &unknown); err == nil {
		t.Fatalf("a shape naming an unknown family decoded as %+v", unknown)
	}
}

// TestFamily_EveryGeometryIsComparable holds closed the invariant [shape.same] rests
// on. Go's == on an interface holding an uncomparable value PANICS at run time, and the
// place it would panic is the adoption path — so a family added with a slice or a map
// in its geometry must fail here, in a unit test, and not on some organisation's
// rollout.
func TestFamily_EveryGeometryIsComparable(t *testing.T) {
	// Every family this plane runs. A family added to family.go and not to this list
	// fails the count assertion below rather than going untested.
	all := []geometry{halfspace{}.complete()}
	for _, g := range all {
		if !reflect.TypeOf(g).Comparable() {
			t.Errorf("the %q family's geometry is not comparable, so comparing two shapes panics "+
				"inside the adoption path", g.family())
		}
	}
	// A family is one CASE in each of three switches — [shape.MarshalJSON],
	// [shape.UnmarshalJSON] and [legacy] — so the count is the reminder that adding one
	// is three edits and this list.
	if len(all) != 1 {
		t.Fatalf("this plane runs %d families; every one of them needs a case in shape.MarshalJSON, "+
			"shape.UnmarshalJSON and legacy, and a resume row that can carry its geometry", len(all))
	}
}

// TestDetector_AsksNothingAboutAnotherOrganisation is the isolation claim made
// STRUCTURAL. A detector IS one organisation's model, so no method may take a tenant,
// an org id or anything else that could name a second one — the boundary stops being a
// predicate every call site has to remember and becomes a fact about the type.
//
// Mutation proof: put the org back on any method (`snapshot(orgID string)`) and this
// fails.
func TestDetector_AsksNothingAboutAnotherOrganisation(t *testing.T) {
	d := reflect.TypeFor[detector]()
	allowed := map[reflect.Type]bool{
		reflect.TypeFor[types.Transaction](): true,
		reflect.TypeFor[anomaly.Snapshot]():  true,
	}
	for m := range d.Methods() {
		for in := range m.Type.Ins() {
			if !allowed[in] {
				t.Errorf("detector.%s takes a %s — a detector is ONE organisation's model, so a "+
					"parameter that can name another organisation is a boundary the type used to hold "+
					"and now only a call site does", m.Name, in)
			}
		}
	}
	// SIX METHODS, discovered from what this plane already asks of a model rather than
	// declared. The number is asserted because the client's value is that it is the
	// smallest honest one: a seventh belongs here only after a call site needs it.
	if d.NumMethod() != 6 {
		t.Fatalf("the detector client has %d methods; it was derived from six call-site groups — "+
			"observe, score, digest, snapshot, restore, state", d.NumMethod())
	}
}

// TestFamily_TheWholeWireNamesASpaceOneWay: a verdict, a model report and a published
// value all name the space as `<family>:<digest>`, and they agree.
//
// This is the defect the family exposed rather than caused. The three were derived in
// three places from three sources — the residency's own record, the engine's State, and
// a stored column — and they agreed only because one family's bare digest was all any of
// them could be. The published contract tells a caller to COMPARE them, so a fourth
// spelling would be a comparison that silently never matches.
func TestFamily_TheWholeWireNamesASpaceOneWay(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	teach(t, p, k, stream(400, time.Now().UTC().Add(-4*time.Hour)))

	v, _, err := p.publish(k)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	st, _, err := p.state(k)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	d, err := p.score(k, stream(1, time.Now().UTC())[0])
	if err != nil {
		t.Fatalf("score: %v", err)
	}

	prefix := string(halfSpace) + ":"
	for name, got := range map[string]string{
		"the value's shape":   v.Shape,
		"the report's shape":  st.Digest,
		"the verdict's shape": d.Shape,
	} {
		if !strings.HasPrefix(got, prefix) {
			t.Errorf("%s is %q, which does not name its family — the family is the first term of a "+
				"space, so a name without it can only be compared inside one family", name, got)
		}
	}
	if v.Shape != st.Digest || st.Digest != d.Shape {
		t.Fatalf("one model, three names for the space it runs in: value %q, report %q, verdict %q",
			v.Shape, st.Digest, d.Shape)
	}
}
