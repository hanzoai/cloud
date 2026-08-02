package risk

// drift_test.go proves the plane notices when a model stops fitting the world it
// was estimated on — and, just as importantly, that it does NOT cry drift when
// nothing has moved. A detector that fires on stable data is a detector an
// operator turns off, after which nothing is being watched at all.

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// ── the index ───────────────────────────────────────────────────────────────

// TestPSIIsZeroOnStableAndLargeOnShifted pins the arithmetic against
// hand-checkable distributions, including the empty-bin case that a naive
// implementation either divides by or silently drops.
func TestPSIIsZeroOnStableAndLargeOnShifted(t *testing.T) {
	flat := []float64{0.25, 0.25, 0.25, 0.25}

	got, ok := psi(flat, flat)
	if !ok || got != 0 {
		t.Fatalf("psi(x, x) = %v (%v), want 0 — a stable distribution is being reported as drifted", got, ok)
	}

	// A modest shift: reported, but under the "different world" band.
	moved, ok := psi(flat, []float64{0.30, 0.25, 0.25, 0.20})
	if !ok {
		t.Fatal("psi refused a well-formed pair")
	}
	if moved <= 0 || moved >= psiShifted {
		t.Fatalf("a modest shift indexed %v, want between 0 and %v", moved, psiShifted)
	}

	// Everything moving into a bin the reference never filled is the case worth
	// catching, and the case a floor-free implementation reports as +Inf or
	// drops entirely.
	shifted, ok := psi([]float64{1, 0, 0, 0}, []float64{0, 0, 0, 1})
	if !ok {
		t.Fatal("psi refused a pair where the mass moved to an unseen bin")
	}
	if shifted <= psiShifted || math.IsInf(shifted, 0) || math.IsNaN(shifted) {
		t.Fatalf("a total shift indexed %v, want a finite number above %v", shifted, psiShifted)
	}

	// Mismatched shapes are refused rather than compared bin-for-bin by index,
	// which would silently compare a decile against a quartile.
	if _, ok := psi(flat, []float64{0.5, 0.5}); ok {
		t.Fatal("psi compared distributions of different widths")
	}
	if _, ok := psi(nil, nil); ok {
		t.Fatal("psi answered over an empty reference")
	}
}

// ── the reading ─────────────────────────────────────────────────────────────

// TestDriftIsSilentWhenTheWorldHasNotMoved is the mutation guard. Without it a
// detector that always returns true would pass every other test in this file.
func TestDriftIsSilentWhenTheWorldHasNotMoved(t *testing.T) {
	s, tn, db, id := driftBed(t, sameWorld)
	d, err := examine(context.Background(), s, tn, db, mustFit(t, db, id), 0, time.Now())
	if err != nil {
		t.Fatalf("examine: %v", err)
	}
	if d.Refusal != "" {
		t.Fatalf("the reading refused: %s", d.Refusal)
	}
	if d.Drifted {
		t.Fatalf("drift reported on unchanged traffic: %v", d.Says)
	}
	if len(d.Dims) == 0 {
		t.Fatal("no feature was measured at all — the reading is vacuous, not stable")
	}
}

// TestDriftIsSeenWhenTheInputDistributionMoves is the input side: the
// coordinates the model reads have moved away from the ones it was estimated on.
func TestDriftIsSeenWhenTheInputDistributionMoves(t *testing.T) {
	s, tn, db, id := driftBed(t, movedWorld)
	d, err := examine(context.Background(), s, tn, db, mustFit(t, db, id), 0, time.Now())
	if err != nil {
		t.Fatalf("examine: %v", err)
	}
	if d.Refusal != "" {
		t.Fatalf("the reading refused: %s", d.Refusal)
	}
	if !d.Drifted {
		t.Fatalf("no drift reported after the input distribution moved; indices: %+v score %v", d.Dims, d.Score)
	}
	if len(d.Says) == 0 {
		t.Fatal("drift was reported with nothing said — an operator cannot act on a boolean")
	}
	// The worst dimension is first, so the report is readable rather than a
	// bag of numbers.
	for i := 1; i < len(d.Dims); i++ {
		if d.Dims[i].Index > d.Dims[i-1].Index {
			t.Fatalf("the per-feature indices are not ordered worst-first: %+v", d.Dims)
		}
	}
}

// TestDriftRefusesOnTooLittleTraffic pins the floor. A population-stability
// index over forty rows is noise with a decimal point, and an alarm raised off
// it is one an operator learns to ignore — which disables the real ones too.
func TestDriftRefusesOnTooLittleTraffic(t *testing.T) {
	s, tn, db, id := driftBed(t, thinWorld)
	d, err := examine(context.Background(), s, tn, db, mustFit(t, db, id), 0, time.Now())
	if err != nil {
		t.Fatalf("examine: %v", err)
	}
	if d.Refusal == "" {
		t.Fatalf("a reading over %d rows answered instead of refusing", d.Rows)
	}
	if d.Drifted {
		t.Fatal("drift was reported off a refused reading")
	}
}

// TestADriftAlarmIsDurableAndRaisedOnce pins the alarm rather than the reading.
//
// A degradation is a RECORD: when it started is what makes the promotion that
// followed reviewable, and a reading that only ever says "true now" cannot
// answer that. It is also deduplicated — a model drifting for a month must not
// write one row every five minutes, because that is an alarm somebody mutes.
func TestADriftAlarmIsDurableAndRaisedOnce(t *testing.T) {
	s, tn, db, id := driftBed(t, movedWorld)
	now := time.Now()

	if err := alarm(context.Background(), s, tn, db, now); err != nil {
		t.Fatalf("alarm: %v", err)
	}
	first, err := openDrift(db, id)
	if err != nil {
		t.Fatalf("openDrift: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("a drifted champion raised no recorded alarm — the finding exists only in a log line")
	}

	// A second tick over the same finding writes nothing new.
	if err := alarm(context.Background(), s, tn, db, now.Add(5*time.Minute)); err != nil {
		t.Fatalf("alarm again: %v", err)
	}
	again, err := openDrift(db, id)
	if err != nil {
		t.Fatalf("openDrift: %v", err)
	}
	if len(again) != len(first) {
		t.Fatalf("a second tick raised %d alarms over the same finding, was %d", len(again), len(first))
	}

	// Standing the version down CLOSES the alarms — an alarm about a model that
	// no longer decides hides the next real one — and never deletes them.
	if err := clearDrift(db, id, now.Add(time.Hour)); err != nil {
		t.Fatalf("clearDrift: %v", err)
	}
	if open, err := openDrift(db, id); err != nil || len(open) != 0 {
		t.Fatalf("%d alarms still open after the version was stood down (%v)", len(open), err)
	}
	var kept int
	if err := db.QueryRow(`SELECT COUNT(*) FROM drift WHERE fit = ?`, id).Scan(&kept); err != nil {
		t.Fatalf("count: %v", err)
	}
	if kept != len(first) {
		t.Fatalf("%d drift records survive, want %d — closing an alarm deleted the history", kept, len(first))
	}
}

// TestDriftOverTheWire pins the op, including the honest refusal for a tenant
// that has never estimated anything.
func TestDriftOverTheWire(t *testing.T) {
	app, _ := wireApp(t)
	code, body := req(t, app, http.MethodGet, "/v1/ml/drift", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/ml/drift = %d %s", code, body)
	}
	var out mlDriftOut
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Refusal == "" {
		t.Fatal("a tenant with no estimated version got a drift reading rather than a refusal")
	}
	if out.Drifted {
		t.Fatal("drift reported for a tenant that has never estimated a model")
	}
}

// ── the bed ─────────────────────────────────────────────────────────────────

// world shapes the traffic a drift test writes after its fit was estimated.
type world int

const (
	// sameWorld replays the distribution the fit was estimated on.
	sameWorld world = iota
	// movedWorld replays a materially different one: amounts two orders of
	// magnitude larger, from new subjects, at a different pace.
	movedWorld
	// thinWorld replays too little of anything to measure.
	thinWorld
)

// driftBed stands up a tenant with a champion whose profile was estimated on one
// window, then writes a second window of decisions after it.
//
// The fit's reference profile is built by the REAL estimator over the first
// window, not hand-written, so the test exercises the same code path drift is
// compared against. A hand-written profile would let the two halves disagree
// about binning and the test would still pass.
func driftBed(t *testing.T, w world) (*stateService, Tenant, *sql.DB, string) {
	t.Helper()
	_, s := wireAt(t, t.TempDir())
	tn, _ := qualify("hanzo", "acme")
	db, err := s.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	base := time.Now().Add(-30 * 24 * time.Hour)
	train := ground(t, s, tn, db, base, 1100, 1, 0)
	cutAt := train[len(train)-1].at

	// The geometry is drawn ONCE and the profile carries it, because a drift
	// reading replays under the same one. Reading it back off the profile is the
	// only way to get it — it is never derivable from the identifier, which is
	// published.
	id := newID("fit")
	seed := testSeed(t)

	// Estimate the reference profile over the first window with the real
	// estimator, then seal it onto a champion.
	rows, err := replayHistory(db, fitRows)
	if err != nil {
		t.Fatalf("replayHistory: %v", err)
	}
	trainRows, testRows, _ := split(rows, trainShare)
	_, metrics, profile, err := estimate(context.Background(), tn, quickShape, seed, trainRows, testRows)
	if err != nil {
		t.Fatalf("estimate: %v", err)
	}

	src := fitSource{
		Name: sourceHistory, Version: 1, From: base.Add(-time.Hour), To: cutAt,
		Horizon: 0, Rows: len(rows), Train: len(trainRows), Test: len(testRows),
		Inventory: s.State.digest, Digest: "rows-" + id,
	}
	if err := putFit(db, fitRow{
		ID: id, At: time.Now(), By: "test", Algo: algoForest, Shape: quickShape,
		Role: roleCandidate, Status: fitQueued, Source: src,
	}); err != nil {
		t.Fatalf("putFit: %v", err)
	}
	if err := markFit(db, id, fitFitting, ""); err != nil {
		t.Fatalf("markFit: %v", err)
	}
	if err := sealFit(db, id, src, metrics, profile, fitDigest(algoForest, quickShape, src)); err != nil {
		t.Fatalf("sealFit: %v", err)
	}
	store, err := s.State.stable.at(tn, id, quickShape)
	if err != nil {
		t.Fatalf("stable.at: %v", err)
	}
	if err := keepFit(db, store, tn, id); err != nil {
		t.Fatalf("keepFit: %v", err)
	}
	r, err := getFit(db, id)
	if err != nil {
		t.Fatalf("getFit: %v", err)
	}
	if _, err := setRole(db, r, roleChampion, "test", "seeded", time.Now()); err != nil {
		t.Fatalf("setRole: %v", err)
	}

	// The window AFTER the fit — the one drift is measured over.
	after := cutAt.Add(time.Minute)
	switch w {
	case sameWorld:
		ground(t, s, tn, db, after, 1000, 1, 5000)
	case movedWorld:
		ground(t, s, tn, db, after, 1000, 400, 5000)
	case thinWorld:
		ground(t, s, tn, db, after, 20, 1, 5000)
	}
	return s, tn, db, id
}

// ground writes n decisions into a tenant's record plane and advances its rings,
// exactly as the decide path does. scale multiplies the amounts and offset moves
// the subject namespace, which is how the two worlds are made different.
func ground(t *testing.T, s *stateService, tn Tenant, db *sql.DB, from time.Time, n, scale, offset int) []observation {
	t.Helper()
	out := make([]observation, 0, n)
	for i := 0; i < n; i++ {
		o := observation{
			id:       newID("dec"),
			at:       from.Add(time.Duration(i) * time.Minute),
			stage:    StagePayment,
			kind:     "transaction",
			subject:  "acct-" + strconv.Itoa(offset+i/4),
			agency:   "human",
			amount:   int64((i%40+1)*scale) * 1_000_000_000,
			currency: "USD", direction: "in",
			signals: map[string]string{"ip": "203.0.113." + strconv.Itoa(i%200), "device": "d-" + strconv.Itoa(i%50)},
		}
		record(s.State.vel, tn, o)
		if err := putDecision(db, o, outcome{id: o.id, action: ActionAllow}, "test", "", ""); err != nil {
			t.Fatalf("putDecision: %v", err)
		}
		out = append(out, o)
	}
	return out
}

func mustFit(t *testing.T, db *sql.DB, id string) fitRow {
	t.Helper()
	r, err := getFit(db, id)
	if err != nil {
		t.Fatalf("getFit: %v", err)
	}
	return r
}

// TestOneProjectionServesBothSides is the training-serving-skew guard.
//
// A fit's reference distribution and a drift reading's live distribution are the
// two halves of every index this file computes. Built by two functions they
// would drift apart — in the binning, in the burn-in, in which rows count — and
// every index would then be measuring the difference between two
// implementations rather than a change in the world. Worse, it would look fine:
// small indices on stable data, for the wrong reason.
//
// So both go through project(), and this asserts the two call sites agree
// exactly on identical rows.
func TestOneProjectionServesBothSides(t *testing.T) {
	_, s := wireAt(t, t.TempDir())
	tn, _ := qualify("hanzo", "acme")
	db, err := s.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ground(t, s, tn, db, time.Now().Add(-40*24*time.Hour), 600, 1, 0)
	rows, err := replayHistory(db, fitRows)
	if err != nil {
		t.Fatalf("replayHistory: %v", err)
	}

	// ONE geometry across both sides, which is the whole point of the comparison:
	// two sandboxes at different seeds hold different trees and every index
	// between them is geometry noise.
	seed := testSeed(t)
	// The estimation side, over the whole set as its training split.
	_, fromFit, err := func() (any, fitProfile, error) {
		_, _, prof, err := estimate(context.Background(), tn, quickShape, seed, rows, nil)
		return nil, prof, err
	}()
	if err != nil {
		t.Fatalf("estimate: %v", err)
	}
	// The drift side, over the same rows.
	fromDrift, _, _, err := measure(context.Background(), tn, quickShape, seed, rows)
	if err != nil {
		t.Fatalf("measure: %v", err)
	}

	if fromFit.Scored != fromDrift.Scored {
		t.Fatalf("the two sides scored %d and %d of the same rows", fromFit.Scored, fromDrift.Scored)
	}
	if len(fromFit.Dims) == 0 {
		t.Fatal("the projection produced no feature distributions at all")
	}
	for dim, want := range fromFit.Dims {
		got, ok := fromDrift.Dims[dim]
		if !ok {
			t.Fatalf("the drift side did not produce %q at all", dim)
		}
		index, ok := psi(want, got)
		if !ok || index != 0 {
			t.Fatalf("%q differs between the estimation and drift projections (index %v) — "+
				"every drift reading is measuring the gap between two implementations", dim, index)
		}
	}
	if index, ok := psi(fromFit.Score, fromDrift.Score); !ok || index != 0 {
		t.Fatalf("the score distributions differ between the two projections (index %v)", index)
	}
}

// testSeed draws a version's geometry the way run() does. A test that wants two
// sandboxes to agree pins ONE draw and passes it to both — which is exactly the
// contract: the seed is a value the tenant's own file carries, never a function
// of anything published.
func testSeed(t *testing.T) uint64 {
	t.Helper()
	seed, err := newSeed()
	if err != nil {
		t.Fatalf("newSeed: %v", err)
	}
	return seed
}

// TestAReadingIsBoundedPerTenantAndAcrossTheFleet is the free-CPU hole.
//
// GET /v1/ml/drift did the metered estimator's work in the request: a fresh
// forest over up to fitRows replayed rows, measured at 54 ms of one core at the
// cap, with no gate, no meter, no slot and no deadline of its own — on the
// single replica that also answers authorisations for every product on
// api.hanzo.ai. `fit` does comparable work behind two fleet-wide slots, a
// ten-minute ceiling and a price.
//
// The bound is per tenant FIRST and fleet-wide second, and that order is the
// property: a tenant holds at most one of the fleet's slots, so no arrangement
// of one tenant's traffic can close the door on another's.
func TestAReadingIsBoundedPerTenantAndAcrossTheFleet(t *testing.T) {
	app, s := wireAt(t, t.TempDir())
	a, _ := qualify("hanzo", "acme")
	b, _ := qualify("hanzo", "beta")
	adb, err := s.State.shelf.open(a)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// A champion, so the op reaches the work rather than refusing for want of an
	// estimated version — otherwise the assertions below would pass on a route
	// that never measures anything.
	seedFit(t, s, a, adb, roleChampion, quickShape)

	// One per tenant. The second ask is refused, not queued.
	first, err := s.State.bench.probe(context.Background(), a)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if _, err := s.State.bench.probe(context.Background(), a); err == nil {
		t.Fatal("one tenant holds two in-request measurements at once")
	}

	// And the OP is behind that same bound, so a tenant looping it spends one
	// slot rather than one per request.
	code, body := req(t, app, http.MethodGet, "/v1/ml/drift", "acme", "u_acme", "")
	if code != http.StatusConflict {
		t.Fatalf("a drift reading while one is already running = %d %s, want 409", code, body)
	}

	// A NEIGHBOUR is unaffected: the per-tenant rule is what makes the fleet-wide
	// pair safe, because acme can never be holding both of them.
	second, err := s.State.bench.probe(context.Background(), b)
	if err != nil {
		t.Fatalf("a neighbour was refused a slot acme could not have been holding: %v", err)
	}

	// Both fleet slots are now held by two DISTINCT tenants, so a third waits on
	// its OWN deadline and gives up rather than running unbounded work.
	third, _ := qualify("hanzo", "gamma")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := s.State.bench.probe(ctx, third); err == nil {
		t.Fatal("a third tenant ran while both fleet slots were held — the pool is not a bound")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the refusal took %s; the wait is not the caller's own deadline", took)
	}

	first()
	second()
	// Released, the next ask succeeds — a bound that never reopens is an outage.
	again, err := s.State.bench.probe(context.Background(), a)
	if err != nil {
		t.Fatalf("the slot did not reopen: %v", err)
	}
	again()
}

// TestAReadingIsPricedLikeTheWorkItDoes pins the other half: the reading is
// gated on the caller's own ledger before it runs, and charged after.
func TestAReadingIsPricedLikeTheWorkItDoes(t *testing.T) {
	book := &book{deny: true}
	app, s := wireBilled(t, t.TempDir(), book)
	tn, _ := qualify("hanzo", "acme")
	db, err := s.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	seedFit(t, s, tn, db, roleChampion, quickShape)

	code, body := req(t, app, http.MethodGet, "/v1/ml/drift", "acme", "u_acme", "")
	if code != http.StatusPaymentRequired {
		t.Fatalf("a reading by an unfunded org = %d %s, want 402 — the work is free", code, body)
	}
	if book.asked("acme") == 0 {
		t.Fatal("the reading never asked the ledger")
	}
}
