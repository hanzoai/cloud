package risk

// strain_test.go — the loud half of the isolation fix fires when the STORE starts
// forgetting, not only when the census does.
//
// THE DEFECT, MEASURED. velocity applies its cap PER SHARD, as MaxKeys/shards+1 —
// FIVE keys per shard against a published census ceiling of 320. Subjects hash
// unevenly, so a shard fills long before the total does and the store silently
// drops that shard's least-recently-used key. The census is a flat count and sees
// none of it:
//
//	subjects=200   storeKeys=198   censusLen=200   lost=0   SATURATED=false
//	subjects=320   storeKeys=290   censusLen=320   lost=0   SATURATED=true
//
// At 200 subjects, TWO of this organisation's own subjects are gone and the model
// reports itself healthy. Each one reads as "has done nothing", scores as
// unremarkable, and raises nothing — a clean bill of health for the wrong reason,
// which is the exact failure the strain report exists to prevent. At 320 the state
// is right by luck (the census is at its ceiling) and the COUNT an operator reads
// is still 0 while thirty subjects are gone.
//
// The evidence was already being collected and thrown away: reconcile reads the
// store's own key count into `live`, and Saturated consulted only the census.
//
// It also makes the published per-tenant bound honest. The 8 MiB budget buys 320
// subjects; the bound that actually binds starts biting at around 200. That is
// class A in a new spelling — a bound stated in one dimension and enforced in
// another — and the fix is for the REPORT to describe the bound that binds.

import (
	"strconv"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/types"
)

// ringsOf records n distinct subjects into one tenant's rings and reconciles. It is
// not named `fill` because [fill] is already this package's "make n tenants
// resident" helper, and one name for two fixtures is how a test reads as the other.
func ringsOf(n int) *rings {
	vel := newRings()
	feed(vel, "b/o", 0, n)
	vel.reconcile()
	return vel
}

// feed records subjects [from,to) for org into r.
func feed(r *rings, org string, from, to int) {
	at := time.Now().UTC()
	for i := from; i < to; i++ {
		r.record(types.Transaction{
			ID: strconv.Itoa(i), OrgID: org, AccountID: "account:u_" + strconv.Itoa(i),
			USD: 1, Timestamp: at,
		})
	}
}

// shedWindow drives r until its STORE holds fewer subjects than the census lists
// — the state per-shard eviction produces — while the census is still UNDER its
// own ceiling. That window is the whole defect, and which subject count reaches it
// depends on how the key text happens to hash, so it is FOUND rather than assumed:
// a fixture that hard-codes a count is a fixture that silently stops testing this
// when the hash, the axes or the shard count change.
//
// It returns how many subjects were introduced and how many the store kept.
func shedWindow(t *testing.T, r *rings, org string) (introduced, held int) {
	t.Helper()
	for introduced = 0; introduced < ringKeyCeiling; {
		feed(r, org, introduced, introduced+8)
		introduced += 8
		r.reconcile()
		if r.order.Len() >= ringKeyCeiling {
			break // the census reached its own ceiling first; not this window
		}
		if held = r.vel.Keys(); held < r.order.Len() {
			return introduced, held
		}
	}
	t.Fatalf("the store never shed a subject before the census filled (introduced=%d census=%d store=%d, per-shard cap=%d) — "+
		"this build cannot reach the window, so the test would be vacuous", introduced, r.order.Len(), r.vel.Keys(), ringKeys/shards+1)
	return 0, 0
}

// TestStrain_FiresWhenTheStoreForgetsBeforeTheCensusDoes is the blocker: strain
// must not read healthy while the store is dropping this tenant's subjects.
func TestStrain_FiresWhenTheStoreForgetsBeforeTheCensusDoes(t *testing.T) {
	// The window the old fixture jumped over: the store is shedding while the
	// census is still under its own ceiling.
	vel := newRings()
	subjects, held := shedWindow(t, vel, "b/o")
	if vel.order.Len() >= ringKeyCeiling {
		t.Fatalf("the census is at its ceiling (%d); this would prove the census, not the store", ringKeyCeiling)
	}
	if vel.lost != 0 {
		t.Fatalf("the census has itself forgotten %d — the window under test is the one where it has NOT", vel.lost)
	}

	st := vel.strain()
	if !st.Saturated {
		t.Fatalf("the store holds %d of this organisation's %d subjects and strain reports saturated=false — "+
			"%d subjects read as 'has done nothing' and nothing says so", held, subjects, subjects-held)
	}
	if st.Forgotten == 0 {
		t.Fatalf("%d of %d subjects are gone and the report says 0 forgotten — the count an operator acts on is wrong",
			subjects-held, subjects)
	}
	if int(st.Forgotten) < subjects-held {
		t.Fatalf("the report says %d forgotten, but %d are actually gone — under-reporting the bound that binds",
			st.Forgotten, subjects-held)
	}
}

// TestStrain_CountsTheStoresLossAtTheCensusCeilingToo: at the ceiling the STATE
// was right by luck and the COUNT was still zero. Both must be right.
func TestStrain_CountsTheStoresLossAtTheCensusCeilingToo(t *testing.T) {
	vel := ringsOf(ringKeyCeiling)
	held := vel.vel.Keys()
	if held >= ringKeyCeiling {
		t.Skip("no loss at the ceiling on this build; the window case above carries the proof")
	}
	st := vel.strain()
	if int(st.Forgotten) < ringKeyCeiling-held {
		t.Fatalf("%d subjects went into the ceiling, the store holds %d, and the report says %d forgotten — "+
			"want at least %d", ringKeyCeiling, held, st.Forgotten, ringKeyCeiling-held)
	}
}

// TestStrain_ForgettingIsNotUndone: the report is a HIGH-WATER MARK, not a gauge.
//
// A dropped subject is re-admitted the moment it is active again, which CLOSES the
// shortfall — but it does not un-blind the window in which that subject read as
// "has done nothing". A gauge would fall back to zero and report that a control
// which had switched itself off never did.
func TestStrain_ForgettingIsNotUndone(t *testing.T) {
	vel := newRings()
	feed(vel, "b/o", 0, 16) // small enough that the store keeps everything
	vel.reconcile()
	if got := vel.strain().Forgotten; got != 0 {
		t.Fatalf("the fixture already lost %d subjects; it must start clean", got)
	}
	// A loss that HAS happened.
	vel.mu.Lock()
	vel.shed = 7
	vel.mu.Unlock()
	// ...and a reconcile taken after the shortfall closed.
	vel.reconcile()

	st := vel.strain()
	if st.Forgotten < 7 {
		t.Fatalf("after the shortfall closed the report says %d forgotten, want at least 7 — it is a gauge that falls "+
			"back to zero, so a control that switched itself off reads as if it never did", st.Forgotten)
	}
	if !st.Saturated {
		t.Fatal("subjects were forgotten and the state reads healthy again — the bound stopped being readable once the shard refilled")
	}
}

// TestStrain_IsQuietWhenNothingIsForgotten keeps the named state honest in the
// other direction. Without this, "always saturated" would satisfy the tests
// above — and a control that is always on is not a control.
func TestStrain_IsQuietWhenNothingIsForgotten(t *testing.T) {
	const subjects = 20
	vel := ringsOf(subjects)
	if held := vel.vel.Keys(); held != subjects {
		t.Fatalf("the store dropped %d of %d subjects at this size; pick a smaller fixture", subjects-held, subjects)
	}
	st := vel.strain()
	if st.Saturated {
		t.Fatalf("%d subjects, none forgotten, and strain reports SATURATED — a control that is always on tells an operator nothing", subjects)
	}
	if st.Forgotten != 0 {
		t.Fatalf("%d subjects, none forgotten, and the report says %d forgotten", subjects, st.Forgotten)
	}
}

// TestStrain_SurvivesTheProbe: the process-level `strained` count on the health
// probe is derived from this state, so a resident whose store is shedding must be
// counted there too. Otherwise the fix is loud in one place and silent at the one
// surface an operator actually reads.
func TestStrain_SurvivesTheProbe(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	r, err := p.resident(k)
	if err != nil {
		t.Fatalf("resident: %v", err)
	}
	introduced, held := shedWindow(t, r.vel, string(k))
	if _, _, _, strained := p.residents(); strained != 1 {
		t.Fatalf("one resident holds %d of its own %d subjects and the probe reports strained=%d, want 1 — "+
			"the state is loud on the model and silent on the surface an operator reads", held, introduced, strained)
	}
}
