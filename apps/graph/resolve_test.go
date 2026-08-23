package graph

import (
	"testing"
	"time"
)

// withConfidence re-stamps a fact at a different confidence. Confidence is inside
// the digest, so the ID has to move with it or the store would treat two
// different assertions as redelivery of one.
func withConfidence(f Fact, c float64) Fact {
	f.Confidence = c
	f.ID = digest(f)
	return f
}

// TestNothingKnowableYetIsNotAnError pins the difference between "this plane holds
// nothing to say" and "this plane failed". A reader asking about an instant before
// anything became knowable gets ok=false and no winner — never a zero Fact dressed
// up as an answer.
func TestNothingKnowableYetIsNotAnError(t *testing.T) {
	f := mk(t, "svc:api", "owner", "team:core", false, "registry", t0)

	_, conflicts, contested, ok := Resolve([]Fact{f}, f.Knowable.Add(-time.Hour))
	if ok {
		t.Fatal("a fact not yet knowable must not be in force")
	}
	if conflicts != nil || contested {
		t.Errorf("silence carries no conflicts and contests nothing; got %d conflicts contested=%v", len(conflicts), contested)
	}

	if _, _, _, ok := Resolve(nil, t0); ok {
		t.Error("an empty set is in force about nothing")
	}
}

// TestACorrectionCannotRewriteWhatAPastDecisionKnew is the property the whole
// bitemporal model exists for. A source correcting itself files a NEW assertion; it
// wins from the moment it became knowable, and the original still wins for every
// observation instant before that. A decision made on Monday stays explainable on
// Friday even though Friday knows better.
func TestACorrectionCannotRewriteWhatAPastDecisionKnew(t *testing.T) {
	original := mk(t, "invoice:42", "total", "100.00", false, "billing", t0)
	correction := mk(t, "invoice:42", "total", "137.50", false, "billing", t0.Add(24*time.Hour))

	facts := []Fact{original, correction}

	// Read as of the correction: the correction is in force.
	winner, conflicts, contested, ok := Resolve(facts, correction.Knowable)
	if !ok || winner.Value != "137.50" {
		t.Fatalf("as of the correction, winner = %q ok=%v, want 137.50", winner.Value, ok)
	}
	if !contested {
		t.Error("two live values for one relation is contested, not resolved into silence")
	}

	// Read as of the moment BEFORE the correction was knowable: the original stands,
	// alone and uncontested. This is what makes an audit of a past decision honest.
	winner, conflicts, contested, ok = Resolve(facts, correction.Knowable.Add(-time.Nanosecond))
	if !ok || winner.Value != "100.00" {
		t.Fatalf("before the correction, winner = %q ok=%v, want 100.00", winner.Value, ok)
	}
	if contested || len(conflicts) != 0 {
		t.Errorf("an assertion not yet knowable cannot contest anything; got %d conflicts contested=%v", len(conflicts), contested)
	}
}

// TestAgreementIsNotContestation pins a distinction that reads backwards until you
// need it: conflicts is everything ELSE that was visible, agreeing or not, so a
// caller can show corroboration. contested is reserved for a genuine disagreement
// about the claim.
func TestAgreementIsNotContestation(t *testing.T) {
	a := mk(t, "svc:api", "owner", "team:core", false, "registry", t0)
	b := mk(t, "svc:api", "owner", "team:core", false, "handbook", t0)

	winner, conflicts, contested, ok := Resolve([]Fact{a, b}, t0.Add(time.Hour))
	if !ok {
		t.Fatal("two knowable assertions are in force")
	}
	if contested {
		t.Error("two sources saying the same thing corroborate; they do not contest")
	}
	if len(conflicts) != 1 {
		t.Fatalf("the corroborating source stays visible; got %d conflicts, want 1", len(conflicts))
	}
	if conflicts[0].Value != winner.Value {
		t.Errorf("corroboration carries the same value; got %q vs %q", conflicts[0].Value, winner.Value)
	}
}

// TestDisagreementKeepsBothSides proves a losing source is never resolved into
// silence — the contested set is the answer, not an error to be swallowed.
func TestDisagreementKeepsBothSides(t *testing.T) {
	registry := mk(t, "svc:api", "owner", "team:core", false, "registry", t0)
	handbook := mk(t, "svc:api", "owner", "team:platform", false, "handbook", t0)

	winner, conflicts, contested, ok := Resolve([]Fact{registry, handbook}, t0.Add(time.Hour))
	if !ok || !contested {
		t.Fatalf("disagreement must surface as contested; ok=%v contested=%v", ok, contested)
	}
	if len(conflicts) != 1 {
		t.Fatalf("the loser stays; got %d conflicts, want 1", len(conflicts))
	}
	if conflicts[0].Value == winner.Value {
		t.Error("the conflict must be the other claim, not a copy of the winner")
	}
	if conflicts[0].Source == winner.Source {
		t.Error("both sources must remain attributable after adjudication")
	}
}

// TestConfidenceBreaksAKnowableTie proves confidence is a tie-breaker within one
// instant and nothing more: it sorts equals, it does not lift a later claim over an
// earlier one that a source has already corrected.
func TestConfidenceBreaksAKnowableTie(t *testing.T) {
	base := mk(t, "doc:1", "language", "go", false, "guess", t0)
	weak := withConfidence(base, 0.2)

	other := mk(t, "doc:1", "language", "rust", false, "scanner", t0)
	strong := withConfidence(other, 0.9)

	winner, _, _, ok := Resolve([]Fact{weak, strong}, t0.Add(time.Hour))
	if !ok || winner.Value != "rust" {
		t.Fatalf("at equal knowable, higher confidence wins; got %q ok=%v", winner.Value, ok)
	}

	// Confidence must NOT outrank a later correction: a caller cannot buy its way
	// past a source that has since corrected itself by sending 1.0.
	late := withConfidence(mk(t, "doc:1", "language", "zig", false, "scanner", t0.Add(time.Hour)), 0.1)
	winner, _, _, ok = Resolve([]Fact{strong, late}, t0.Add(2*time.Hour))
	if !ok || winner.Value != "zig" {
		t.Fatalf("later knowable outranks higher confidence; got %q ok=%v", winner.Value, ok)
	}
}

// TestTheOrderIsTotalAndReproducible is the property every downstream comparison
// rests on: the same assertions resolve to the same answer regardless of the order
// the storage engine handed them back.
func TestTheOrderIsTotalAndReproducible(t *testing.T) {
	a := withConfidence(mk(t, "e", "r", "alpha", false, "s1", t0), 0.5)
	b := withConfidence(mk(t, "e", "r", "beta", false, "s2", t0), 0.5)
	c := withConfidence(mk(t, "e", "r", "gamma", false, "s3", t0), 0.5)

	forward, _, _, ok := Resolve([]Fact{a, b, c}, t0.Add(time.Hour))
	if !ok {
		t.Fatal("three knowable assertions are in force")
	}
	reverse, _, _, _ := Resolve([]Fact{c, b, a}, t0.Add(time.Hour))
	if forward.ID != reverse.ID {
		t.Errorf("input order changed the winner: %q vs %q — the order is not total", forward.Value, reverse.Value)
	}

	// Everything ties down to the digest here, so the winner is the lowest one.
	for _, f := range []Fact{a, b, c} {
		if f.ID < forward.ID {
			t.Errorf("a tie must break to the LOWEST digest; %q (%s) sorts under the winner (%s)", f.Value, f.ID, forward.ID)
		}
	}
}

// TestTheDigestIsTheAssertionAndNotItsHandling proves what content addressing means
// here: when the server happened to write it is not part of what was claimed, so
// redelivery hours later is the same row — while changing anything a caller
// actually asserted is a different assertion.
func TestTheDigestIsTheAssertionAndNotItsHandling(t *testing.T) {
	now := mk(t, "svc:api", "depends", "svc:db", true, "deploy", t0)
	later := mk(t, "svc:api", "depends", "svc:db", true, "deploy", t0.Add(6*time.Hour))

	if now.ID != later.ID {
		t.Error("the write clock is the plane's, not the claim's — redelivery must be one row")
	}
	if now.Knowable.Equal(later.Knowable) {
		t.Fatal("this test is only meaningful if the two differ in a field outside the digest")
	}

	// A litigation hold is a fact about the RECORD, so it cannot restate the claim.
	held := now
	held.Hold = true
	if digest(held) != now.ID {
		t.Error("a hold is about the record, not the world; it must stay outside the digest")
	}

	for name, changed := range map[string]Fact{
		"entity":     {Entity: "svc:web", Relation: now.Relation, Value: now.Value, Names: now.Names, At: now.At, Seen: now.Seen, Source: now.Source, Evidence: now.Evidence, By: now.By, Confidence: now.Confidence},
		"relation":   {Entity: now.Entity, Relation: "calls", Value: now.Value, Names: now.Names, At: now.At, Seen: now.Seen, Source: now.Source, Evidence: now.Evidence, By: now.By, Confidence: now.Confidence},
		"value":      {Entity: now.Entity, Relation: now.Relation, Value: "svc:cache", Names: now.Names, At: now.At, Seen: now.Seen, Source: now.Source, Evidence: now.Evidence, By: now.By, Confidence: now.Confidence},
		"source":     {Entity: now.Entity, Relation: now.Relation, Value: now.Value, Names: now.Names, At: now.At, Seen: now.Seen, Source: "handbook", Evidence: now.Evidence, By: now.By, Confidence: now.Confidence},
		"evidence":   {Entity: now.Entity, Relation: now.Relation, Value: now.Value, Names: now.Names, At: now.At, Seen: now.Seen, Source: now.Source, Evidence: "ev-2", By: now.By, Confidence: now.Confidence},
		"by":         {Entity: now.Entity, Relation: now.Relation, Value: now.Value, Names: now.Names, At: now.At, Seen: now.Seen, Source: now.Source, Evidence: now.Evidence, By: "someone-else", Confidence: now.Confidence},
		"confidence": {Entity: now.Entity, Relation: now.Relation, Value: now.Value, Names: now.Names, At: now.At, Seen: now.Seen, Source: now.Source, Evidence: now.Evidence, By: now.By, Confidence: 0.75},
		"at":         {Entity: now.Entity, Relation: now.Relation, Value: now.Value, Names: now.Names, At: now.At.Add(time.Hour), Seen: now.Seen.Add(time.Hour), Source: now.Source, Evidence: now.Evidence, By: now.By, Confidence: now.Confidence},
	} {
		if digest(changed) == now.ID {
			t.Errorf("changing %s must produce a different assertion, not redelivery of this one", name)
		}
	}
}

// TestAPropertyAndAnEdgeAreDifferentAssertions guards the one boolean a walk joins
// on. Same entity, relation and value — but one claims a link to another entity and
// the other claims a string. Collapsing them would make a walk follow a property.
func TestAPropertyAndAnEdgeAreDifferentAssertions(t *testing.T) {
	edge := mk(t, "svc:api", "depends", "svc:db", true, "deploy", t0)
	property := mk(t, "svc:api", "depends", "svc:db", false, "deploy", t0)

	if edge.ID == property.ID {
		t.Error("an edge and a property are different claims about the world")
	}
}

// TestTheDoorAnswersWhatIsInForceAndWhatDisagreed carries the adjudication above
// all the way out to the wire. Two sources say different things about one relation;
// the answer names a winner AND keeps the loser attributable, because a caller
// deciding on this needs to see that the question was contested.
func TestTheDoorAnswersWhatIsInForceAndWhatDisagreed(t *testing.T) {
	app := mountGraph(t)
	assertFact(t, app, "", "acme/svc/api", "owner", "acme/team/core", true)
	assertFact(t, app, "", "acme/svc/api", "owner", "acme/team/platform", true)

	env := ask(t, app, `{
		entity(key: "acme/svc/api") {
			key
			assertions { id entity relation value names at seen knowable source evidence by confidence }
			resolve(relation: "owner") {
				entity relation asOf known contested
				winner { id value source evidence by confidence }
				conflicts { id value source }
			}
		}
	}`)

	root := env["data"].(map[string]any)["entity"].(map[string]any)

	all, _ := root["assertions"].([]any)
	if len(all) != 2 {
		t.Fatalf("the endpoint returned %d assertions, want the 2 that were filed", len(all))
	}
	for _, a := range all {
		f := a.(map[string]any)
		for _, field := range []string{"id", "entity", "relation", "value", "at", "seen", "knowable", "source", "evidence"} {
			if s, _ := f[field].(string); s == "" {
				t.Errorf("assertion published an empty %s: %v", field, f)
			}
		}
	}

	r := root["resolve"].(map[string]any)
	if r["known"] != true {
		t.Fatal("two filed assertions must resolve to something known")
	}
	if r["contested"] != true {
		t.Error("two sources naming different owners is contested; reporting one owner alone would be a lie by omission")
	}
	if r["entity"] != "acme/svc/api" || r["relation"] != "owner" {
		t.Errorf("the resolution must restate what was asked: %v", r)
	}
	if s, _ := r["asOf"].(string); s == "" {
		t.Error("a resolution is only meaningful against an instant; asOf must be published")
	}

	winner, _ := r["winner"].(map[string]any)
	if winner == nil {
		t.Fatal("known with no winner is incoherent")
	}
	if s, _ := winner["evidence"].(string); s == "" {
		t.Error("the winner must carry the evidence it was defended by")
	}

	conflicts, _ := r["conflicts"].([]any)
	if len(conflicts) != 1 {
		t.Fatalf("the losing claim stays; got %d conflicts, want 1", len(conflicts))
	}
	loser := conflicts[0].(map[string]any)
	if loser["value"] == winner["value"] {
		t.Error("the conflict must be the claim that lost, not a second copy of the winner")
	}
	if loser["id"] == winner["id"] {
		t.Error("winner and conflict must be distinct assertions")
	}
}

// TestTheVocabularyPublishesTheRuleItAdjudicatesBy is what makes the resolution
// above auditable rather than magic: a caller can read the relations in use and the
// exact order that settled the tie, from the same endpoint that answered.
func TestTheVocabularyPublishesTheRuleItAdjudicatesBy(t *testing.T) {
	app := mountGraph(t)
	assertFact(t, app, "", "acme/svc/api", "owner", "acme/team/core", true)
	assertFact(t, app, "", "acme/svc/api", "runsOn", "acme/cluster/a", true)

	env := ask(t, app, `{ vocabulary { relations rule bound } }`)
	v := env["data"].(map[string]any)["vocabulary"].(map[string]any)

	relations, _ := v["relations"].([]any)
	seen := map[string]bool{}
	for _, r := range relations {
		seen[r.(string)] = true
	}
	for _, want := range []string{"owner", "runsOn"} {
		if !seen[want] {
			t.Errorf("a relation in use is missing from the vocabulary: %q not in %v", want, relations)
		}
	}

	rule, _ := v["rule"].([]any)
	if len(rule) == 0 {
		t.Fatal("a plane that adjudicates must say by what rule, or its answers cannot be checked")
	}
	if n, ok := v["bound"].(float64); !ok || n <= 0 {
		t.Errorf("bound = %v; a caller needs the ceiling to know when an answer was truncated", v["bound"])
	}
}
