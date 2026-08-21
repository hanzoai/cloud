package risk

// address_test.go pins the two claims a content-addressed model rests on: that the
// NAME is a pure function of the value, and that the name is NEVER an authority.
//
// The second is the whole isolation argument. The address deliberately does not
// carry the organisation (address.go's header says why: a name that has to be
// unguessable is obscurity, not isolation), so the ONLY thing keeping one
// organisation's model out of another's reach is the predicate — the shelf file is
// the organisation and the qualified key leads every statement inside it. A claim
// like that is worth nothing asserted, so it is stated here as the experiment that
// would refute it: hand organisation B the exact address organisation A published,
// and watch it resolve nothing.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"
)

// TestAddress_IsAPureFunctionOfTheValue: two calls over the same value agree
// without talking to each other, which is what makes publication idempotent and
// makes a rollback target nameable at all. No clock, no counter, no identity.
func TestAddress_IsAPureFunctionOfTheValue(t *testing.T) {
	snap := sampleSnapshot()
	warmed := time.Unix(1_700_000_000, 0).UTC()
	first := address(halfSpace, snap, warmed)
	if first == "" {
		t.Fatal("a value has no name")
	}
	if len(first) != addressBytes {
		t.Fatalf("an address is %d characters, want %d", len(first), addressBytes)
	}
	for range 4 {
		if again := address(halfSpace, snap, warmed); again != first {
			t.Fatalf("the same value named itself twice: %q then %q", first, again)
		}
	}
}

// TestAddress_CoversEveryTermThatChangesAScore: every field the header claims is in
// the name is in it. Each case is a MUTATION — flip one term, the name must move —
// so a term dropped from [address] fails here rather than silently making two
// different models one value.
//
// The organisation is the one field asserted NOT to move it, and that is the
// security decision under test, not an omission.
func TestAddress_CoversEveryTermThatChangesAScore(t *testing.T) {
	warmed := time.Unix(1_700_000_000, 0).UTC()
	base := address(halfSpace, sampleSnapshot(), warmed)

	moves := map[string]func(*anomaly.Snapshot){
		"shape":    func(s *anomaly.Snapshot) { s.Digest = "a different inventory" },
		"version":  func(s *anomaly.Snapshot) { s.Version = s.Version + 1 },
		"seed":     func(s *anomaly.Snapshot) { s.Seed ^= 0xdeadbeef },
		"learned":  func(s *anomaly.Snapshot) { s.Learned++ },
		"seen":     func(s *anomaly.Snapshot) { s.Seen++ },
		"cut":      func(s *anomaly.Snapshot) { s.Cut = s.Cut / 2 },
		"ref mass": func(s *anomaly.Snapshot) { s.Ref[0][0] += 1 },
		"cur mass": func(s *anomaly.Snapshot) { s.Cur[1][2] += 1 },
		"hist":     func(s *anomaly.Snapshot) { s.Hist[3] += 1 },
	}
	for term, move := range moves {
		s := sampleSnapshot()
		move(&s)
		if got := address(halfSpace, s, warmed); got == base {
			t.Fatalf("changing %s did not change the value's name, so two models that score "+
				"differently share one address", term)
		}
	}

	// The FOLD WATERMARK moves it too, and it is the one term whose absence would be
	// invisible: two models with identical masses reached by different routes hold
	// different beliefs about what is left to fold, and one of them re-teaches
	// history the other does not.
	if got := address(halfSpace, sampleSnapshot(), warmed.Add(24*time.Hour)); got == base {
		t.Fatal("the fold watermark is not in the address, so a model that will re-teach its " +
			"own history is named identically to one that will not")
	}

	// THE FAMILY MOVES IT, and it is the FIRST term for a reason: every term above is
	// a term of one family's arithmetic, so without this a value fitted under one
	// family could wear a name minted under another and be adopted through it.
	if got := address("transformer", sampleSnapshot(), warmed); got == base {
		t.Fatal("the model family is not in the address, so two families' masses that happen " +
			"to agree numerically are named as one value")
	}

	// THE ORGANISATION IS NOT IN IT. Two organisations whose models are literally
	// identical get one name, and that is a finding about a fold gone wrong rather
	// than a leak — a salted name would have hidden it. Isolation is the predicate
	// tested below, never the unguessability of this string.
	mine := sampleSnapshot()
	mine.OrgID = "brand/some-other-organisation"
	if got := address(halfSpace, mine, warmed); got != base {
		t.Fatal("the address is salted with the organisation, which makes isolation rest on a " +
			"name being unguessable instead of on the per-organisation store")
	}
}

// TestAddress_MassesAreNamedByTheirBitsAndNotTheirDigits: a decimal rendering is a
// lossy function of a float64, so a name taken over text could call two different
// masses one value. apps/datasets reached this first; this is the same reasoning
// applied to the same problem.
//
// Mutation proof: hash the masses with %v instead of Float64bits and this fails.
func TestAddress_MassesAreNamedByTheirBitsAndNotTheirDigits(t *testing.T) {
	warmed := time.Unix(1_700_000_000, 0).UTC()
	a := sampleSnapshot()
	b := sampleSnapshot()
	// Two float64s one ULP apart. Rendered with the precision a decimal encoder
	// reaches for they are the same string; they are not the same number, and a model
	// holding one does not score like a model holding the other.
	a.Ref[0][0] = 0.1
	b.Ref[0][0] = 0.1 + 5e-17
	if a.Ref[0][0] == b.Ref[0][0] {
		t.Skip("this platform cannot represent the two masses apart")
	}
	if address(halfSpace, a, warmed) == address(halfSpace, b, warmed) {
		t.Fatal("two distinct masses share one address, so the name is over their digits " +
			"rather than over their bits")
	}
}

// TestAddress_AForeignOrgResolvesNothing IS THE ISOLATION REQUIREMENT, stated as
// the experiment that would refute it.
//
// Organisation A publishes a value and gets its address. Organisation B is handed
// THAT EXACT ADDRESS — not a guess, the real one — and every door in this plane is
// tried with it: the record read, the masses read, and the adoption an operator
// would use to roll a model back. All three must answer nothing, and B's own model
// must be untouched afterwards.
//
// WHAT MUTATION BREAKS IT, MEASURED. Two drafts of this comment were wrong before
// the mutations were actually run, so what follows is the result and not the
// expectation.
//
// THE DEFENCE IS TWO LAYERS AND EITHER ONE ALONE HOLDS:
//
//	the FILE   [plane.for_] resolves a different store per organisation, so B's
//	           file has no row to leak whatever any statement says.
//	the ROW    `tenant = ?` leads every statement, so a row written under A's
//	           qualified key does not answer B even from one shared file.
//
// Removing EITHER leaves this test green, and that is the layering working rather
// than the test being weak — measured: point [plane.for_] at one namespace for every
// organisation and it still passes; drop `tenant = ?` from [plane.masses] and it
// still passes. It fails when BOTH go, which is the falsification condition this
// test actually has.
//
// So a single-layer regression is caught by
// [TestAddress_TwoBrandsShareAFileAndNotAValue] instead, and it can be: two brands'
// identically named organisations SHARE ONE FILE by design, so the file layer is
// already absent there and the row predicate is the only thing left. That is where
// dropping it leaks, and that test fails on that one mutation alone.
//
// The finding underneath, worth saying because it inverts the obvious reading: the
// per-organisation FILE is not what makes this isolated — the row predicate holds on
// its own. The file buys per-organisation encryption and a blast radius, and it is
// the layer whose absence is invisible.
func TestAddress_AForeignOrgResolvesNothing(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	a, b := key(t, brandA, orgA), key(t, brandA, orgB)

	teach(t, p, a, stream(300, time.Now().UTC().Add(-3*time.Hour)))
	published, minted, err := p.publish(a)
	if err != nil || !minted {
		t.Fatalf("publish(A): minted=%v err=%v", minted, err)
	}
	if published.Address == "" {
		t.Fatal("A published a value with no name")
	}

	// B has its own model, and it has learned its own events — so "B resolved
	// nothing" below cannot be an artifact of B having no residency at all.
	teach(t, p, b, stream(300, time.Now().UTC().Add(-3*time.Hour)))
	beforeB, _, err := p.state(b)
	if err != nil {
		t.Fatalf("state(B): %v", err)
	}
	if beforeB.Learned == 0 {
		t.Fatal("organisation B learned nothing, so this test proves nothing about B")
	}

	// DOOR 1 — the record.
	if v, ok, err := p.valueAt(b, published.Address); err != nil {
		t.Fatalf("valueAt(B): %v", err)
	} else if ok {
		t.Fatalf("organisation B read organisation A's published value by name: %+v", v)
	}

	// DOOR 2 — the model: the masses AND the shape they describe, which is the whole
	// of what an adoption installs.
	if m, ok, err := p.modelAt(b, published.Address); err != nil {
		t.Fatalf("modelAt(B): %v", err)
	} else if ok {
		t.Fatalf("organisation B read organisation A's model by name: learned=%d shape=%+v", m.Learned, m.Shape)
	}

	// DOOR 3 — adoption, which is what an operator rolling a model back actually
	// calls. This is the one that would install A's learned behaviour into B.
	if _, _, err := p.adopt(b, published.Address); err == nil {
		t.Fatal("organisation B adopted organisation A's model by naming its address")
	}

	// B's own model is untouched, so the refusal was a refusal and not a wipe.
	afterB, _, err := p.state(b)
	if err != nil {
		t.Fatalf("state(B) after: %v", err)
	}
	if afterB.Learned != beforeB.Learned {
		t.Fatalf("organisation B's model moved from %d to %d learned events while another "+
			"organisation's address was pushed at it", beforeB.Learned, afterB.Learned)
	}

	// A's own history is unchanged too: B's attempts published nothing into it.
	vs, _, err := p.values(a, 0)
	if err != nil {
		t.Fatalf("values(A): %v", err)
	}
	if len(vs) != 1 || vs[0].Address != published.Address {
		t.Fatalf("organisation A's history is %d values after B's attempts, want the one it published", len(vs))
	}
	// ...and B has none, rather than a copy of A's.
	if bs, _, err := p.values(b, 0); err != nil {
		t.Fatalf("values(B): %v", err)
	} else if len(bs) != 0 {
		t.Fatalf("organisation B holds %d published values it never published", len(bs))
	}
}

// TestPublish_IsIdempotentOnTheValue: publishing an unchanged model mints nothing
// and answers with the name it already has. That is what a content address BUYS —
// the same property [plane.enact] has for a regime, reached by content rather than
// by comparing fields — and it is why publishing at every boundary that matters is
// free rather than the cheapest way to fill a disk.
func TestPublish_IsIdempotentOnTheValue(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	teach(t, p, k, stream(300, time.Now().UTC().Add(-3*time.Hour)))

	first, minted, err := p.publish(k)
	if err != nil || !minted {
		t.Fatalf("first publish: minted=%v err=%v", minted, err)
	}
	for range 3 {
		again, minted, err := p.publish(k)
		if err != nil {
			t.Fatalf("republish: %v", err)
		}
		if minted {
			t.Fatal("an unchanged model minted a second value, so publishing on a boundary is " +
				"the cheapest way to fill a tenant's disk")
		}
		if again.Address != first.Address {
			t.Fatalf("an unchanged model published under two names: %q then %q", first.Address, again.Address)
		}
		if again.Seq != first.Seq {
			t.Fatalf("an unchanged model took a second place in the history: %d then %d", first.Seq, again.Seq)
		}
	}
	vs, _, err := p.values(k, 0)
	if err != nil {
		t.Fatalf("values: %v", err)
	}
	if len(vs) != 1 {
		t.Fatalf("the history holds %d values after four publications of one model, want 1", len(vs))
	}

	// Teach it something and it becomes a DIFFERENT value, with its own name and its
	// own place — the succession of states the identity is made of.
	fresh(t, p, k, "second", 120, time.Now().UTC().Add(-time.Hour))
	next, minted, err := p.publish(k)
	if err != nil || !minted {
		t.Fatalf("publish after learning: minted=%v err=%v", minted, err)
	}
	if next.Address == first.Address {
		t.Fatal("a model that learned more published under the same name")
	}
	if next.Seq != first.Seq+1 {
		t.Fatalf("the second value took sequence %d, want %d", next.Seq, first.Seq+1)
	}
	if next.Learned <= first.Learned {
		t.Fatalf("the second value is behind the first: %d then %d", first.Learned, next.Learned)
	}
}

// TestPublish_RefusesAModelThatLearnedNothing: planted is not learned. Every
// residency plants its geometry so an adoption has something to be checked against,
// so "the engine holds a model" does not mean "this organisation taught it
// anything" — and a value that reproduces nothing is not a value.
func TestPublish_RefusesAModelThatLearnedNothing(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	if _, _, err := p.state(k); err != nil { // plants the residency, teaches nothing
		t.Fatalf("state: %v", err)
	}
	v, minted, err := p.publish(k)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if minted || v.Address != "" {
		t.Fatalf("a model that learned nothing published %+v", v)
	}
}

// TestPublished_IsAppendOnly holds the difference between this table and the `model`
// cell beside it. The cell is overwritten every sweep and is meant to be; a
// published value is what a rollback names and what an adverse decision is
// reconstructed against, so a statement that rewrote one would rewrite history.
//
// It reads the SOURCE for the statements themselves, because that is the invariant:
// a test over behaviour can only catch the overwrite somebody already thought of.
// The one permitted DELETE is retention's.
func TestPublished_IsAppendOnly(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	const disposal = "DELETE FROM published WHERE tenant = ? AND seq <= ("
	var mutations, deletes []string
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				sql := strings.ToUpper(lit.Value)
				if !strings.Contains(sql, "PUBLISHED") {
					return true
				}
				where := fmt.Sprintf("%s:%d", name, fset.Position(lit.Pos()).Line)
				if strings.Contains(sql, "UPDATE PUBLISHED") || strings.Contains(sql, "ON CONFLICT") ||
					strings.Contains(sql, "REPLACE INTO PUBLISHED") {
					mutations = append(mutations, where)
				}
				if strings.Contains(sql, "DELETE FROM PUBLISHED") && !strings.Contains(lit.Value, disposal) {
					deletes = append(deletes, where)
				}
				return true
			})
		}
	}
	if len(mutations) > 0 {
		t.Errorf("statement(s) that MUTATE a published model value: %s\n"+
			"A value that can be edited is not a value, and every decision reconstructed "+
			"against it is reconstructed against whatever it was last edited to be. The "+
			"`model` cell beside it is the place that gets overwritten; this is not.",
			strings.Join(mutations, ", "))
	}
	if len(deletes) > 0 {
		t.Errorf("DELETE against published outside the retention disposal: %s\n"+
			"Retention's is the only one, so what was removed stays countable from the "+
			"lowest surviving sequence.", strings.Join(deletes, ", "))
	}
}

// TestPublished_RetentionBindsInBytesAndTheLossIsCountable: the budget is stated in
// BYTES because every organisation's shelf lives on one volume, so a history
// bounded only in rows is one organisation filling the disk another organisation's
// model is stored on. At the ceiling the OLDEST goes, and how many went is DERIVED
// from the lowest surviving sequence rather than counted — a figure that cannot
// drift from the thing it describes.
func TestPublished_RetentionBindsInBytesAndTheLossIsCountable(t *testing.T) {
	if modelValues < 2 {
		t.Fatalf("the byte budget retains %d values, which is not a history", modelValues)
	}
	if modelValues*maxModelRowBytes > modelBudget {
		t.Fatalf("%d values at %d bytes is %d, over the %d-byte budget",
			modelValues, maxModelRowBytes, modelValues*maxModelRowBytes, modelBudget)
	}
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	teach(t, p, k, stream(200, time.Now().UTC().Add(-5*time.Hour)))

	// One value past the ceiling. Each publication has to be a DIFFERENT value or it
	// mints nothing, so each one learns first.
	var names []string
	for i := 0; i <= modelValues; i++ {
		fresh(t, p, k, "r"+itoa(i), 20, time.Now().UTC().Add(-time.Duration(i+1)*time.Minute))
		v, minted, err := p.publish(k)
		if err != nil || !minted {
			t.Fatalf("publish %d: minted=%v err=%v", i, minted, err)
		}
		names = append(names, v.Address)
	}
	vs, disposed, err := p.values(k, 0)
	if err != nil {
		t.Fatalf("values: %v", err)
	}
	if len(vs) > modelValues {
		t.Fatalf("the history holds %d values, over the %d the budget allows", len(vs), modelValues)
	}
	if disposed == 0 {
		t.Fatal("retention bound the history and reported nothing disposed of, which is the " +
			"silence the derived count exists to prevent")
	}
	// The OLDEST went, not the newest: a retention that disposed of what was just
	// published would be a history with no present in it.
	newest := names[len(names)-1]
	var held bool
	for _, v := range vs {
		if v.Address == newest {
			held = true
		}
		if v.Address == names[0] {
			t.Fatal("retention kept the oldest value and disposed of a newer one")
		}
	}
	if !held {
		t.Fatal("retention disposed of the value that was just published")
	}
}

// TestValues_DescendsIsDerivedSoRollbackIsRightForFree: what the working model grew
// out of is answered from its mass count, never from a stored pointer. Adopting an
// OLDER value moves the count backward and the same query answers with that older
// value — where a head pointer would be a second fact to keep in step with the
// first, and rollback is exactly the moment it would fall out of step.
func TestValues_DescendsIsDerivedSoRollbackIsRightForFree(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)

	teach(t, p, k, stream(300, time.Now().UTC().Add(-4*time.Hour)))
	early, _, err := p.publish(k)
	if err != nil {
		t.Fatalf("publish early: %v", err)
	}
	fresh(t, p, k, "late", 200, time.Now().UTC().Add(-2*time.Hour))
	late, minted, err := p.publish(k)
	if err != nil || !minted {
		t.Fatalf("publish late: minted=%v err=%v", minted, err)
	}

	st, _, err := p.state(k)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	d, ok, err := p.descends(k, st.Learned)
	if err != nil || !ok {
		t.Fatalf("descends: ok=%v err=%v", ok, err)
	}
	if d.Address != late.Address {
		t.Fatalf("the working model descends from %q, want the newest value %q", d.Address, late.Address)
	}

	// ROLL BACK by naming the earlier value. Nothing but its name is supplied.
	if _, _, err := p.adopt(k, early.Address); err != nil {
		t.Fatalf("rolling back to a published value: %v", err)
	}
	rolled, _, err := p.state(k)
	if err != nil {
		t.Fatalf("state after rollback: %v", err)
	}
	if rolled.Learned != early.Learned {
		t.Fatalf("after adopting a value with %d learned events the model reports %d",
			early.Learned, rolled.Learned)
	}
	back, ok, err := p.descends(k, rolled.Learned)
	if err != nil || !ok {
		t.Fatalf("descends after rollback: ok=%v err=%v", ok, err)
	}
	if back.Address != early.Address {
		t.Fatalf("after rolling back the model descends from %q, want %q — a derived answer "+
			"is right in both directions and a stored pointer is what would not be",
			back.Address, early.Address)
	}
	// The newer value is STILL THERE. A rollback names a value; it does not destroy
	// the one it rolled back from, which is what makes it reversible.
	if _, ok, err := p.valueAt(k, late.Address); err != nil || !ok {
		t.Fatalf("rolling back disposed of the value it rolled back FROM: ok=%v err=%v", ok, err)
	}
}

// TestAddress_TwoBrandsShareAFileAndNotAValue is the half the test above cannot
// reach, and it is what makes the `tenant = ?` predicate on every statement in
// address.go load-bearing rather than decorative.
//
// A shelf file is the FLEET's per-org file: [plane.for_] resolves it from the BARE
// org slug, deliberately, so two brands' identically named organisations share ONE
// FILE. Inside it the QUALIFIED key is the only thing separating them — no second
// file, no directory boundary. So this is the one configuration where a published
// value could be read across a tenant boundary, and it is the configuration the
// deployment actually has.
//
// Mutation proof: drop `tenant = ?` from [plane.masses] (or [plane.valueAt], or
// [plane.values]) and one brand's organisation adopts the other's model.
func TestAddress_TwoBrandsShareAFileAndNotAValue(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	ha, za := key(t, brandA, orgA), key(t, brandB, orgA) // SAME org slug, two brands

	sha, err := p.for_(ha)
	if err != nil {
		t.Fatalf("shelf %s: %v", ha, err)
	}
	shz, err := p.for_(za)
	if err != nil {
		t.Fatalf("shelf %s: %v", za, err)
	}
	if sha != shz {
		t.Fatalf("the two brands do not share a file, so this test is not exercising the predicate " +
			"it exists to hold — the fixture has stopped modelling the deployment")
	}

	fresh(t, p, ha, "hanzo", 300, time.Now().UTC().Add(-3*time.Hour))
	mine, minted, err := p.publish(ha)
	if err != nil || !minted {
		t.Fatalf("publish %s: minted=%v err=%v", ha, minted, err)
	}

	// The other brand's organisation of the same name, in the same file, holding the
	// exact address.
	if v, ok, err := p.valueAt(za, mine.Address); err != nil {
		t.Fatalf("valueAt %s: %v", za, err)
	} else if ok {
		t.Fatalf("%s read %s's published value out of the file they share: %+v", za, ha, v)
	}
	if m, ok, err := p.modelAt(za, mine.Address); err != nil {
		t.Fatalf("modelAt %s: %v", za, err)
	} else if ok {
		t.Fatalf("%s read %s's model out of the file they share: learned=%d", za, ha, m.Learned)
	}
	if _, _, err := p.adopt(za, mine.Address); err == nil {
		t.Fatalf("%s adopted %s's model out of the file they share", za, ha)
	}
	// The listing is the third door, and it is the one an operator reads: it must not
	// even MENTION the other brand's value.
	vs, _, err := p.values(za, 0)
	if err != nil {
		t.Fatalf("values %s: %v", za, err)
	}
	for _, v := range vs {
		if v.Address == mine.Address {
			t.Fatalf("%s's history lists %s's value", za, ha)
		}
	}
	if len(vs) != 0 {
		t.Fatalf("%s holds %d published values it never published", za, len(vs))
	}
	// ...and the brand that DID publish still reads its own, so the separation is a
	// separation and not a blanket empty answer.
	if _, ok, err := p.valueAt(ha, mine.Address); err != nil || !ok {
		t.Fatalf("%s cannot read the value it published: ok=%v err=%v", ha, ok, err)
	}
}

// fresh teaches n events whose ids nobody has used, so the model actually MOVES.
//
// [stream] mints e_0..e_n-1 deterministically, which is exactly right for the tests
// it was written for — two organisations fed identical input — and wrong here: the
// observation record deduplicates on (tenant, id), so a second stream teaches
// nothing and the model publishes to the name it already has. That is [plane.publish]
// being correct, and it made the first draft of these tests fail. A value history
// needs successive DIFFERENT values, so the ids are namespaced per round.
func fresh(t *testing.T, p *plane, k tenant, round string, n int, at time.Time) {
	t.Helper()
	evs := make([]observation, 0, n)
	for i := range n {
		evs = append(evs, ob(t, round+"_"+itoa(i), kindAccount, "u_"+itoa(i%7),
			float64(100+(i*37)%900), at.Add(time.Duration(i)*time.Second)))
	}
	if _, err := p.learn(k, evs...); err != nil {
		t.Fatalf("learn %s: %v", round, err)
	}
}

// sampleSnapshot is a snapshot with the shape of a real one — the default topology,
// 25 trees of 511 nodes over two windows and a 256-bucket distribution — and masses
// that are distinct per position so a term dropped from [address] shows up.
func sampleSnapshot() anomaly.Snapshot {
	const trees, nodes = 25, 511
	s := anomaly.Snapshot{
		Version: 1,
		Digest:  "0f4a5c2e",
		OrgID:   "brand/organisation",
		Seed:    0x0123456789abcdef,
		Learned: 1_250_000,
		Seen:    128,
		Cut:     0.9731,
		Ref:     make([][]float64, trees),
		Cur:     make([][]float64, trees),
		Hist:    make([]float64, 256),
	}
	for i := range trees {
		s.Ref[i] = make([]float64, nodes)
		s.Cur[i] = make([]float64, nodes)
		for j := range nodes {
			s.Ref[i][j] = float64(i*nodes+j) * 1.5
			s.Cur[i][j] = float64(i*nodes+j) * 0.25
		}
	}
	for i := range s.Hist {
		s.Hist[i] = float64(i) * 3
	}
	return s
}
