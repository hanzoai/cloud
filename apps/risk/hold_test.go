package risk

// hold_test.go closes the CROSS-TENANT RECLAIM, which was the one part of this
// app's bounds that no test measured.
//
// The resident bound is a COUNT of tenants ([maxResident]) with an LRU over it,
// so one organisation's arrival does drop another organisation's residency. That
// is a capacity decision and not the defect class, but ONLY because of two
// properties, and both were prose:
//
//	LOSSLESS  the victim's learned state is written to its own shelf BEFORE the
//	          residency goes, so its next request rebuilds what it had.
//	LOUD      the reclaim is counted where an operator reads it (`evicted` on
//	          /v1/risk/health), so a node running on cross-tenant reclamation
//	          does not read like one with headroom.
//
// Each was removed and the whole suite stayed GREEN. Losing the save is worse
// than losing the state: the same line releases the slot the eviction took in
// p.opening, so the victim is also locked out for the life of the process. That
// is one tenant's traffic silently unlearning and then locking out another's —
// measured, on this branch, with nothing failing.
//
// WHY NEW TESTS AND NOT EDITS. [TestRings_SurviveAnEviction] names the lossless
// property but REIMPLEMENTS it: it deletes the map entry, bumps the counter and
// calls save itself, so it measures its own three lines rather than the plane's.
// A test that performs the work it checks cannot fail when that work is removed.
// The two eviction tests below drive the real path — plane.resident past
// [maxResident] — and read only what an operator can read.
//
// The two tests above them are NOT gap closures and are not claimed as any: main
// already refuses an over-long field ([TestField_IsRefusedAtTheEndpointAndNotTruncated])
// and already notices a census that stops counting
// ([TestRings_TheCeilingIsMeasuredNotAsserted]). They are kept because they state
// the same two bounds from the direction an operator reads them — the published
// ceiling's own arithmetic, and the magnitude of a degradation rather than its
// existence — and both are cheap. Each is mutation-proven against its own defect.

import (
	"strings"
	"testing"
	"time"
)

// ── A: the count is a byte bound only because the value is capped ───────────

// TestObserve_RefusesAValueThePublishedCeilingCannotPrice.
//
// Every ceiling this app publishes — [ringKeys], [ringKeyCeiling], [recordRows],
// [planeRingCeiling] — is a COUNT derived by dividing a byte budget by
// [perSubjectBytes], and [perSubjectBytes] prices the caller's strings at
// [maxKeyText]. So the count is a byte bound if and only if no longer value can
// reach the rings: disable the refusal and 8 MiB of per-tenant budget buys a
// tenant as many megabytes as it likes, while the probe still reports the same
// count.
//
// [TestField_IsRefusedAtTheEndpointAndNotTruncated] already covers that refusal at
// the wire. This states it from the other end — the arithmetic the ceiling is
// derived from — and at the CONSTRUCTOR rather than at an endpoint, because the
// fold from a tenant's own feature surface and the replay from its own record
// both reach these rings without passing one.
func TestObserve_RefusesAValueThePublishedCeilingCannotPrice(t *testing.T) {
	long := strings.Repeat("x", maxField+1)
	at := time.Now().UTC()

	for _, c := range []struct {
		field string
		a     actor
		id    string
	}{
		{"subject", actor{Kind: kindAccount, Subject: long}, "e_1"},
		{"device", actor{Kind: kindAccount, Subject: "u_1", Device: long}, "e_1"},
		{"peer", actor{Kind: kindAccount, Subject: "u_1", Peer: long}, "e_1"},
		{"id", actor{Kind: kindAccount, Subject: "u_1"}, long},
	} {
		o, err := observe(c.id, c.a, 100, at)
		if err == nil {
			t.Errorf("%s of %d bytes was admitted — at %d bytes a key costs more than the %d B "+
				"perSubjectBytes prices it at, so the %d-subject ceiling is not an %d B bound",
				c.field, maxField+1, maxField, perSubjectBytes, ringKeyCeiling, residentRingBudget)
			continue
		}
		// The refusal has to NAME the field and the bound, or an operator reading
		// a 413 cannot tell which of four values was too long.
		if !strings.Contains(err.Error(), c.field) {
			t.Errorf("%s was refused without being named: %v", c.field, err)
		}
		_ = o
	}

	// And the cap admits what the app is for: the longest LEGAL value.
	if _, err := observe(strings.Repeat("e", maxField), actor{
		Kind: kindAccount, Subject: strings.Repeat("u", maxField),
		Device: strings.Repeat("d", maxField), Peer: strings.Repeat("p", maxField),
	}, 100, at); err != nil {
		t.Fatalf("a value of exactly maxField was refused, so the ceiling is priced against a value the app will not take: %v", err)
	}
}

// ── silent degradation: the magnitude, not only the state ───────────────────

// TestStrain_CountsTheSubjectsItForgot.
//
// [strain.Saturated] says a tenant's aggregates are at their bound;
// [strain.Forgotten] says HOW MANY of that tenant's own subjects have been
// dropped to stay inside it. Only the first had a test, and the two are not
// interchangeable: Saturated is true the moment the census is full and stays
// true, so it cannot distinguish a tenant sitting exactly at its ceiling from
// one that has silently discarded ten thousand subjects. Forgotten is the number
// an operator acts on, and this is the assertion that it is maintained —
// [TestRings_TheCeilingIsMeasuredNotAsserted] covers the same increment from the
// ceiling's side.
//
// A forgotten subject reads as "has done nothing", scores as unremarkable and
// raises nothing — which is what a clean bill of health looks like.
func TestStrain_CountsTheSubjectsItForgot(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)

	// Fresh: nothing forgotten, and that has to be the starting point or the
	// assertion below proves nothing.
	if _, s, err := p.state(k); err != nil {
		t.Fatalf("state: %v", err)
	} else if s.Forgotten != 0 {
		t.Fatalf("a tenant that has been taught nothing reports %d forgotten subjects", s.Forgotten)
	}

	// Past its OWN ceiling, one distinct subject at a time. The bound is the
	// tenant's own and degrades only itself.
	over := ringKeyCeiling + ringKeyCeiling/4
	at := time.Now().UTC().Add(-time.Hour)
	obs := make([]observation, 0, over)
	for i := range over {
		o, err := observe("e_"+itoa(i), actor{Kind: kindAccount, Subject: "u_" + itoa(i)}, 100, at.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatalf("observe %d: %v", i, err)
		}
		obs = append(obs, o)
	}
	teach(t, p, k, obs)

	_, s, err := p.state(k)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if !s.Saturated {
		t.Fatalf("%d distinct subjects went into a %d-subject ceiling and the tenant does not report saturated: %+v", over, ringKeyCeiling, s)
	}
	if s.Forgotten == 0 {
		t.Fatalf("%d distinct subjects went into a %d-subject ceiling: at least %d of this tenant's own "+
			"subjects now read as having done nothing, and the count it publishes is 0 — the magnitude of "+
			"the degradation is unreadable: %+v", over, ringKeyCeiling, over-ringKeyCeiling, s)
	}
	if s.Bound != ringKeyCeiling {
		t.Fatalf("the tenant publishes a bound of %d against a ceiling of %d", s.Bound, ringKeyCeiling)
	}
}

// ── B: a reclaim across tenants is lossless, and it is counted ──────────────

// fill makes n tenants resident by resolving each one, and returns their keys in
// the order they arrived. It drives plane.resident, which is the only path that
// evicts — a test that deletes from p.res itself is testing its own delete.
func fill(t *testing.T, p *plane, n int) []tenant {
	t.Helper()
	out := make([]tenant, 0, n)
	for i := range n {
		k := key(t, brandA, "org"+itoa(i))
		if _, err := p.resident(k); err != nil {
			t.Fatalf("resident %d: %v", i, err)
		}
		out = append(out, k)
	}
	return out
}

// TestEviction_IsCountedOnTheProbe.
//
// The resident bound is a COUNT of tenants ([maxResident]) with an LRU over it,
// so one tenant's arrival does reclaim another tenant's residency. That is the
// shape of defect class B, and what makes it a capacity decision rather than the
// defect is that it is COUNTED where an operator reads it — /v1/risk/health
// carries `evicted`. Deleting the increment left every test passing, because the
// only test that read the counter bumped it itself first.
func TestEviction_IsCountedOnTheProbe(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)

	fill(t, p, maxResident)
	if held, _, evicted, _ := p.residents(); held != maxResident || evicted != 0 {
		t.Fatalf("with exactly %d tenants resident the plane reports held=%d evicted=%d — nothing has been reclaimed yet", maxResident, held, evicted)
	}

	// The one that does not fit.
	if _, err := p.resident(key(t, brandA, "org"+itoa(maxResident))); err != nil {
		t.Fatalf("resident past the bound: %v", err)
	}

	held, _, evicted, _ := p.residents()
	if held > maxResident {
		t.Fatalf("%d tenants are resident against a bound of %d", held, maxResident)
	}
	if evicted == 0 {
		t.Fatalf("a tenant's residency was reclaimed to admit another and the plane reports evicted=0 — "+
			"a node running on cross-tenant reclamation reads identically to one with headroom (held=%d)", held)
	}
}

// TestEviction_WritesTheVictimsStateDownFirst.
//
// This is the whole justification for a cross-tenant reclaim. A tenant evicted by
// somebody else's arrival must lose NOTHING it learned — its state is snapshotted
// to its own shelf before the residency is dropped, so its next request rebuilds
// what it had. Remove that save and the victim silently returns to whatever its
// last save held: red measured exactly this shape elsewhere as learned=40 → 0
// from two other tenants' traffic, with no error and no alert.
//
// The victim learns AFTER the save that its own admission performed, so a shelf
// written only at admission is not enough to pass.
//
// The re-resolve is on a deadline. The save also releases the slot the eviction
// took in p.opening, so a reclaim that skips it does not merely lose the state —
// it leaves that tenant's slot held and every later request for it waiting
// forever. Without the deadline that is a hung suite rather than a failing test.
func TestEviction_WritesTheVictimsStateDownFirst(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)

	victim := key(t, brandA, "victim")
	if _, err := p.resident(victim); err != nil {
		t.Fatalf("resident: %v", err)
	}
	// Taught while resident, which is the state a shelf written at admission does
	// not have.
	teach(t, p, victim, stream(120, time.Now().UTC().Add(-3*time.Hour)))
	before, _, err := p.state(victim)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if before.Learned == 0 {
		t.Fatal("the victim learned nothing, so losing it would prove nothing")
	}

	// Other tenants arrive until the victim is the least recently used and the
	// bound reclaims it. It was resolved first, so it is the first to go.
	fill(t, p, maxResident+2)

	p.mu.Lock()
	_, stillResident := p.res[victim]
	p.mu.Unlock()
	if stillResident {
		t.Skip("the victim is still resident; this test needs the bound to have reclaimed it")
	}

	// Its next request must come back with what it learned, and must come back.
	done := make(chan struct{})
	var after int64
	var rerr error
	go func() {
		defer close(done)
		s, _, e := p.state(victim)
		after, rerr = s.Learned, e
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the reclaimed tenant's next request never returned — its residency slot was taken by the " +
			"eviction and never released, so that organisation is locked out for the life of the process")
	}
	if rerr != nil {
		t.Fatalf("re-resolving a reclaimed tenant: %v", rerr)
	}
	if after != before.Learned {
		t.Fatalf("a tenant reclaimed to make room for another came back having learned %d of the %d events "+
			"it had — %d were lost to other tenants' traffic, with no error and nothing on the probe",
			after, before.Learned, before.Learned-after)
	}
}
