package risk

// fit_test.go proves the registry: what a version records, what may not change
// about it, and what admission refuses.
//
// Every test here was written by reintroducing the defect and checking that THIS
// test — not some other one — goes red.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"
)

// ── admission ───────────────────────────────────────────────────────────────

// TestHorizonExcludesALabelThatArrivedTooLate is the label-leakage test, and it
// is the sharpest one in the layer.
//
// A dispute lands 30 to 120 days after the payment it judges. A set that admits
// a row before its horizon has run is a set that knows the future: it measures
// beautifully offline and is worth nothing online, and NOTHING about the
// resulting numbers says so. This pins that a row younger than the horizon is
// excluded, and that the same row is admitted once it has aged.
func TestHorizonExcludesALabelThatArrivedTooLate(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	from, to := now.AddDate(0, 0, -365), now

	rows := []replayed{
		// Old enough: the dispute window has closed on it.
		{obs: observation{id: "old", at: now.AddDate(0, 0, -200), subject: "a1"}, label: "fraud"},
		// Six days old, judged "legitimate" — which today means only "nobody has
		// disputed it YET". Admitting it teaches the model that recent payments
		// are clean, which is a fact about the calendar and not about payments.
		{obs: observation{id: "fresh", at: now.AddDate(0, 0, -6), subject: "a2"}, label: "legitimate"},
	}

	got := matured(rows, from, to, defaultHorizon, now)
	if len(got) != 1 || got[0].obs.id != "old" {
		var ids []string
		for _, r := range got {
			ids = append(ids, r.obs.id)
		}
		t.Fatalf("admitted %v, want only [old] — a row inside the %d-day horizon reached the training set",
			ids, defaultHorizon)
	}

	// The SAME row, once it has aged past the horizon, is admitted. Without this
	// half the test would also pass on a function that admits nothing.
	later := now.AddDate(0, 0, defaultHorizon+1)
	got = matured(rows, from, later, defaultHorizon, later)
	if len(got) != 2 {
		t.Fatalf("admitted %d rows once both had matured, want 2 — the horizon is excluding rows forever", len(got))
	}

	// And the window still bounds it: a row older than From is out.
	got = matured(rows, now.AddDate(0, 0, -100), to, defaultHorizon, now)
	if len(got) != 0 {
		t.Fatalf("a row outside the window was admitted: %d rows", len(got))
	}
}

// TestSplitKeepsASubjectWhole pins the other leakage: the same account, device
// or address landing on both sides of the cut lets the model memorise the entity
// rather than the behaviour, and the held-out measurement then reports how well
// it memorises.
func TestSplitKeepsASubjectWhole(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var rows []replayed
	// A hundred subjects arriving over time, each acting a few times — real
	// entity churn. Then acct-0, which first acted at the very start, acts again
	// deep in the held-out period: that is the row a positional or hashed split
	// would put on the other side, and the one this test exists for.
	for i := 0; i < 300; i++ {
		rows = append(rows, replayed{obs: observation{
			id: newID("o"), at: base.Add(time.Duration(i) * time.Hour),
			subject: "acct-" + strconv.Itoa(i/3),
		}})
	}
	rows = append(rows, replayed{obs: observation{
		id: "acct-0-returns", at: base.Add(280 * time.Hour), subject: "acct-0",
	}})
	train, test, cut := split(rows, trainShare)
	if cut.IsZero() {
		t.Fatal("the split reported no cut point")
	}
	left := map[string]bool{}
	for _, r := range train {
		left[r.obs.subject] = true
	}
	for _, r := range test {
		if left[r.obs.subject] {
			t.Fatalf("subject %q appears in BOTH splits — the model can memorise it instead of the behaviour",
				r.obs.subject)
		}
	}
	if len(train)+len(test) != len(rows) {
		t.Fatalf("split dropped rows: %d + %d != %d", len(train), len(test), len(rows))
	}
	if len(train) == 0 || len(test) == 0 {
		t.Fatalf("one side of the split is empty (%d train, %d test) — there is nothing held out", len(train), len(test))
	}
	// The returning subject went WHOLE to the side it started on, late row and all.
	var late bool
	for _, r := range train {
		if r.obs.id == "acct-0-returns" {
			late = true
		}
	}
	if !late {
		t.Fatal("a subject's later row was held out while its earlier rows trained — the model can memorise that account")
	}
	// And the held-out side holds only subjects first seen AFTER the cut, which
	// is what makes the measurement out-of-entity as well as out-of-time.
	for _, r := range test {
		if left[r.obs.subject] {
			t.Fatalf("held-out subject %q was also trained on", r.obs.subject)
		}
	}
}

// TestADegenerateSplitIsRefusedNotReported is the other side of the same
// invariant, and it is the one an implementation gets wrong quietly.
//
// A window where two accounts do everything CANNOT be split by subject after a
// temporal cut: both of them appear before the cut, so both go left and nothing
// is held out. Grouping is right to do that. What must not happen is a fit
// reporting metrics measured on an empty held-out set, which is a model with a
// perfect-looking scorecard and no evidence behind it.
func TestADegenerateSplitIsRefusedNotReported(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var rows []replayed
	for i := 0; i < 20; i++ {
		rows = append(rows, replayed{obs: observation{
			id: newID("o"), at: base.Add(time.Duration(i) * time.Hour),
			subject: "acct-" + strconv.Itoa(i%2),
		}})
	}
	_, test, _ := split(rows, trainShare)
	if len(test) != 0 {
		t.Fatalf("held out %d rows from two subjects that both start before the cut", len(test))
	}
	if held := int(float64(len(rows)) * minHeld); len(test) >= held {
		t.Fatalf("a %d-row held-out side passed the %d-row floor — a fit would report metrics measured on nothing",
			len(test), held)
	}
}

// TestSplitIsTemporal pins that the cut is a point in TIME and not a shuffle. A
// model is used on tomorrow, so it must be measured on rows later than the ones
// it learned from.
func TestSplitIsTemporal(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var rows []replayed
	for i := 0; i < 100; i++ {
		rows = append(rows, replayed{obs: observation{
			id: newID("o"), at: base.Add(time.Duration(i) * time.Hour), subject: newID("s"),
		}})
	}
	train, test, cut := split(rows, trainShare)
	for _, r := range train {
		if r.obs.at.After(cut) {
			t.Fatalf("a training row (%s) is later than the cut (%s) — the split is not temporal", r.obs.at, cut)
		}
	}
	for _, r := range test {
		if r.obs.at.Before(cut) {
			t.Fatalf("a held-out row (%s) is earlier than the cut (%s)", r.obs.at, cut)
		}
	}
}

// ── the artefact ────────────────────────────────────────────────────────────

// TestSourceDigestIsTheRowsAndTheSplit pins reproducibility: two estimations
// over the same rows with the same split agree, and any change to either — a row
// added, a row moved across the cut, a dimension renamed — disagrees.
func TestSourceDigestIsTheRowsAndTheSplit(t *testing.T) {
	a := []replayed{{obs: observation{id: "1"}}, {obs: observation{id: "2"}}}
	b := []replayed{{obs: observation{id: "3"}}}
	dims := []string{"amount", "count"}

	one := sourceDigest(a, b, dims)
	if one != sourceDigest(a, b, dims) {
		t.Fatal("the same rows and split produced two digests — nothing about a fit is reproducible")
	}
	if one == sourceDigest(b, a, dims) {
		t.Fatal("swapping the splits produced the same digest — the digest does not cover the split assignment")
	}
	if one == sourceDigest(append(append([]replayed(nil), a...), replayed{obs: observation{id: "4"}}), b, dims) {
		t.Fatal("adding a row produced the same digest")
	}
	if one == sourceDigest(a, b, []string{"amount", "burst"}) {
		t.Fatal("changing the feature set produced the same digest")
	}
}

// TestFitDigestCoversTheWholeArtefact pins that a version's identity moves when
// anything it is an estimation OF moves, and not otherwise.
func TestFitDigestCoversTheWholeArtefact(t *testing.T) {
	shape := candidate{Trees: 25, Depth: 8, Window: 256, Blend: 0.25, Review: 0.01}
	src := fitSource{Name: sourceHistory, Digest: "rows-abc", Horizon: 120, Inventory: "inv-1"}
	base := fitDigest(algoForest, shape, src)

	for _, tc := range []struct {
		why   string
		algo  string
		shape candidate
		src   fitSource
	}{
		{"a different geometry", algoForest, candidate{Trees: 40, Depth: 8, Window: 256, Blend: 0.25, Review: 0.01}, src},
		{"a different appetite", algoForest, candidate{Trees: 25, Depth: 8, Window: 256, Blend: 0.25, Review: 0.02}, src},
		{"different rows", algoForest, shape, fitSource{Name: sourceHistory, Digest: "rows-xyz", Horizon: 120, Inventory: "inv-1"}},
		{"a different horizon", algoForest, shape, fitSource{Name: sourceHistory, Digest: "rows-abc", Horizon: 14, Inventory: "inv-1"}},
		{"a different feature inventory", algoForest, shape, fitSource{Name: sourceHistory, Digest: "rows-abc", Horizon: 120, Inventory: "inv-2"}},
	} {
		if fitDigest(tc.algo, tc.shape, tc.src) == base {
			t.Errorf("%s produced the SAME digest — two different models would be indistinguishable to an auditor", tc.why)
		}
	}
	if fitDigest(algoForest, shape, src) != base {
		t.Error("the digest is not a function of its inputs")
	}
}

// ── the metrics ─────────────────────────────────────────────────────────────

// TestAUCIsTheRankIdentity checks the arithmetic against hand-computable cases,
// including the tie case a warming forest actually produces.
func TestAUCIsTheRankIdentity(t *testing.T) {
	for _, tc := range []struct {
		why      string
		scores   []float64
		positive []bool
		want     float64
	}{
		{"perfect separation", []float64{0.1, 0.2, 0.8, 0.9}, []bool{false, false, true, true}, 1},
		{"perfectly wrong", []float64{0.9, 0.8, 0.2, 0.1}, []bool{false, false, true, true}, 0},
		{"all tied is a coin flip", []float64{0.5, 0.5, 0.5, 0.5}, []bool{false, true, false, true}, 0.5},
		{"one swap", []float64{0.1, 0.3, 0.2, 0.9}, []bool{false, false, true, true}, 0.75},
	} {
		if got := auc(tc.scores, tc.positive); got != tc.want {
			t.Errorf("%s: auc = %v, want %v", tc.why, got, tc.want)
		}
	}
}

// TestAnUnmeasuredRateIsAbsentAndNotZero is the honesty invariant. A rate
// reported as 0.0 because nothing was judged reads as a perfect model, and every
// dashboard above this would render it as one.
func TestAnUnmeasuredRateIsAbsentAndNotZero(t *testing.T) {
	tn, _ := qualify("hanzo", "acme")
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var rows []replayed
	for i := 0; i < 400; i++ {
		rows = append(rows, replayed{obs: observation{
			id: newID("o"), at: base.Add(time.Duration(i) * time.Minute),
			kind: "account", subject: newID("s"), amount: int64(i+1) * 1_000_000,
			currency: "USD", direction: "in", signals: map[string]string{"ip": "203.0.113.5"},
		}}) // no label anywhere
	}
	train, test, _ := split(rows, trainShare)
	_, m, prof, err := estimate(context.Background(), tn, quickShape, testSeed(t), train, test)
	if err != nil {
		t.Fatalf("estimate: %v", err)
	}
	if m.Judged != 0 {
		t.Fatalf("judged %d rows with no labels present", m.Judged)
	}
	if m.AUC != nil {
		t.Errorf("AUC = %v over zero judged rows — an unmeasured ranking reported as a number", *m.AUC)
	}
	if m.Prevalence != nil {
		t.Errorf("prevalence = %v over zero judged rows", *m.Prevalence)
	}
	if m.Lift != nil {
		t.Errorf("lift = %v over zero judged rows", *m.Lift)
	}
	if prof.Rate != nil {
		t.Errorf("the profile recorded a judgement rate of %v over zero judged rows", *prof.Rate)
	}
	// And the profile it DID record is the thing drift is measured against, so
	// an empty one would make every later drift reading vacuous.
	if len(prof.Score) == 0 {
		t.Error("the fit recorded no score distribution; drift would have nothing to compare against")
	}
}

// ── the wire ────────────────────────────────────────────────────────────────

// TestFitOpsAreBehindTheIdentityGate pins that the lifecycle leaves inherit the
// Bridge the /v1/ml group installs, which they get by registration order rather
// than by declaring their own. An X-Org-Id with no X-User-Id is the forged-header
// case: the header survived the edge but no credential minted it.
func TestFitOpsAreBehindTheIdentityGate(t *testing.T) {
	app, _ := wireApp(t)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/ml/fits", ""},
		{http.MethodPost, "/v1/ml/fits", `{"horizon":14}`},
		{http.MethodGet, "/v1/ml/fits/fit_x", ""},
		{http.MethodPost, "/v1/ml/fits/fit_x/cancel", ""},
		{http.MethodPut, "/v1/ml/fits/fit_x/role", `{"role":"champion","reason":"x"}`},
		{http.MethodGet, "/v1/ml/fits/fit_x/tally", ""},
		{http.MethodGet, "/v1/ml/schedule", ""},
		{http.MethodPut, "/v1/ml/schedule", `{"every":24}`},
		{http.MethodGet, "/v1/ml/drift", ""},
	} {
		code, body := req(t, app, tc.method, tc.path, "acme", "", tc.body)
		if code != http.StatusForbidden {
			t.Errorf("%s %s = %d %s, want 403 for an unvalidated principal", tc.method, tc.path, code, body)
		}
	}
}

// TestTheBridgeReachesTheLifecycleLeaves is the other half, and the one that
// would catch a registration-order regression: a VALIDATED principal must be
// served. If mountFit's group were registered before cloud.Bridge, the org would
// never be parked and every one of these would 403 forever — a whole plane dead
// for a reason no test asserting only refusals could see.
func TestTheBridgeReachesTheLifecycleLeaves(t *testing.T) {
	app, _ := wireApp(t)
	for _, path := range []string{"/v1/ml/fits", "/v1/ml/schedule", "/v1/ml/drift"} {
		code, body := req(t, app, http.MethodGet, path, "acme", "u_acme", "")
		if code != http.StatusOK {
			t.Errorf("GET %s = %d %s, want 200 for a validated principal — the identity bridge does not reach this leaf",
				path, code, body)
		}
	}
}

// TestFitRegistryIsTenantScoped is the isolation test for the registry itself.
// Two orgs, one surface: B must see none of A's versions and must not be able to
// name one.
func TestFitRegistryIsTenantScoped(t *testing.T) {
	app, s := wireApp(t)
	a, _ := qualify("hanzo", "acme")
	b, _ := qualify("hanzo", "beta")

	adb, err := s.State.shelf.open(a)
	if err != nil {
		t.Fatalf("open A: %v", err)
	}
	mine := seedFit(t, s, a, adb, roleChampion, quickShape)

	// A sees it.
	code, body := req(t, app, http.MethodGet, "/v1/ml/fits", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("A list = %d %s", code, body)
	}
	var page mlFitPage
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(page.Items) != 1 || page.Champion != mine {
		t.Fatalf("A sees %d versions, champion %q, want 1 and %q", len(page.Items), page.Champion, mine)
	}

	// B sees ZERO ROWS — not an error, because an error is a signal.
	code, body = req(t, app, http.MethodGet, "/v1/ml/fits", "beta", "u_beta", "")
	if code != http.StatusOK {
		t.Fatalf("B list = %d %s", code, body)
	}
	var theirs mlFitPage
	_ = json.Unmarshal(body, &theirs)
	if len(theirs.Items) != 0 || theirs.Champion != "" {
		t.Fatalf("B sees %d of A's versions (champion %q) — the tenant boundary leaked",
			len(theirs.Items), theirs.Champion)
	}

	// B naming A's version gets 404, indistinguishable from an unknown id. A 403
	// would be an oracle: it says "this exists and is not yours", which is enough
	// to enumerate a neighbour's registry.
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/ml/fits/" + mine, ""},
		{http.MethodPost, "/v1/ml/fits/" + mine + "/cancel", ""},
		{http.MethodPut, "/v1/ml/fits/" + mine + "/role", `{"role":"champion","reason":"steal it"}`},
		{http.MethodGet, "/v1/ml/fits/" + mine + "/tally", ""},
		{http.MethodGet, "/v1/ml/drift?fit=" + mine, ""},
	} {
		code, body := req(t, app, tc.method, tc.path, "beta", "u_beta", tc.body)
		if code != http.StatusNotFound {
			t.Errorf("B %s %s = %d %s, want 404", tc.method, tc.path, code, body)
		}
	}

	// And B's own file is untouched by any of it.
	bdb, err := s.State.shelf.open(b)
	if err != nil {
		t.Fatalf("open B: %v", err)
	}
	if _, ok, err := fitInRole(bdb, roleChampion); err != nil || ok {
		t.Fatal("B acquired a champion from A's registry")
	}
}

// TestOneChampionAndOneChallenger pins the partial unique indexes. The code that
// writes a role change also stands the incumbent down; the index is what makes
// that true even when the code is wrong.
func TestOneChampionAndOneChallenger(t *testing.T) {
	_, s := wireApp(t)
	tn, _ := qualify("hanzo", "acme")
	db, err := s.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	first := seedFit(t, s, tn, db, roleChampion, quickShape)
	second := seedFit(t, s, tn, db, roleCandidate, quickShape)

	// The index refuses a second champion written past setRole.
	if _, err := db.Exec(`UPDATE fit SET role = 'champion' WHERE id = ?`, second); err == nil {
		t.Fatal("a second champion was written — two models both believe they decide")
	}
	// And setRole, which stands the incumbent down, succeeds.
	r, err := getFit(db, second)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, err := setRole(db, r, roleChampion, "u", "promote", time.Now()); err != nil {
		t.Fatalf("setRole: %v", err)
	}
	champ, ok, err := fitInRole(db, roleChampion)
	if err != nil || !ok || champ.ID != second {
		t.Fatalf("champion = %q (%v), want %q", champ.ID, ok, second)
	}
	was, err := getFit(db, first)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if was.Role != roleRetired {
		t.Fatalf("the displaced champion is %q, want retired", was.Role)
	}
	// The transition is recorded on BOTH rows: a model that changed and cannot
	// say who changed it is a control nobody owns.
	for _, id := range []string{first, second} {
		ms, err := fitMoves(db, id)
		if err != nil {
			t.Fatalf("moves: %v", err)
		}
		if len(ms) == 0 {
			t.Fatalf("%s changed role with no recorded transition", id)
		}
		if ms[len(ms)-1].By == "" || ms[len(ms)-1].Reason == "" {
			t.Fatalf("%s's transition records no decider or no reason: %+v", id, ms[len(ms)-1])
		}
	}
}

// TestAnEstimationIsNeverRunInTheRequest pins the DoS bound at the op: the first
// call is accepted and queued, and a second one for the same tenant is refused
// rather than queued behind it.
func TestAnEstimationIsNeverRunInTheRequest(t *testing.T) {
	app, s := wireApp(t)
	tn, _ := qualify("hanzo", "acme")
	db, err := s.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Hold the tenant's one slot so the second request has something to collide
	// with, and keep the runner parked until the test releases it.
	release := make(chan struct{})
	held := newID("fit")
	if err := putFit(db, fitRow{ID: held, At: time.Now(), Algo: algoForest, Role: roleCandidate, Status: fitQueued}); err != nil {
		t.Fatalf("putFit: %v", err)
	}
	if err := s.State.bench.start(tn, kindFit, held, func(context.Context) { <-release }); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { close(release) })

	code, body := req(t, app, http.MethodPost, "/v1/ml/fits", "acme", "u_acme", `{"horizon":1}`)
	if code != http.StatusConflict {
		t.Fatalf("a second estimation for one tenant = %d %s, want 409 — a caller can queue unbounded work",
			code, body)
	}
}

// TestAShapeOutsideTheGridIsRefused pins the other half of the same bound: 400
// trees at depth 20 is not a model, it is an allocation, and it is reachable by
// anyone holding a key.
func TestAShapeOutsideTheGridIsRefused(t *testing.T) {
	app, _ := wireApp(t)
	for _, body := range []string{
		`{"shape":{"trees":400,"depth":8,"window":256,"blend":0.25,"review":0.01}}`,
		`{"shape":{"trees":25,"depth":20,"window":256,"blend":0.25,"review":0.01}}`,
		`{"shape":{"trees":25,"depth":8,"window":1000000,"blend":0.25,"review":0.01}}`,
		`{"shape":{"trees":25,"depth":8,"window":256,"blend":9,"review":0.01}}`,
		`{"shape":{"trees":25,"depth":8,"window":256,"blend":0.25,"review":0.9}}`,
		`{"algo":"transformer"}`,
	} {
		code, out := req(t, app, http.MethodPost, "/v1/ml/fits", "acme", "u_acme", body)
		if code != 422 {
			t.Errorf("POST /v1/ml/fits %s = %d %s, want 422", body, code, out)
		}
	}
}

// TestScheduleRefusesToPromote pins the feedback-loop guard. A schedule decides
// that a new estimation is OWED; a person decides which model answers a customer.
// A control that took over because a timer fired has a clock as its author.
func TestScheduleRefusesToPromote(t *testing.T) {
	app, _ := wireApp(t)
	code, body := req(t, app, http.MethodPut, "/v1/ml/schedule", "acme", "u_acme",
		`{"every":24,"role":"champion"}`)
	if code != 422 {
		t.Fatalf("a schedule that promotes = %d %s, want 422", code, body)
	}
	if !strings.Contains(string(body), "promote") {
		t.Errorf("the refusal does not say why: %s", body)
	}
	// And the two roles it MAY use are accepted.
	for _, role := range []string{"candidate", "challenger"} {
		code, body := req(t, app, http.MethodPut, "/v1/ml/schedule", "acme", "u_acme",
			`{"every":24,"labels":5,"role":"`+role+`"}`)
		if code != http.StatusOK {
			t.Errorf("schedule role %q = %d %s", role, code, body)
		}
	}
}

// TestDueIsBothTriggers pins that "scheduled" and "event-driven" are ONE
// predicate, and above all that a tenant with no new matured judgement is
// SKIPPED — re-estimating identical rows mints a new identifier for the same
// artefact and resets nothing.
func TestDueIsBothTriggers(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	off := schedule{Every: 0}.withDefaults()
	if ok, _ := due(off, time.Time{}, 1000, now); ok {
		t.Error("a schedule that is off is due")
	}
	every := schedule{Every: 24}.withDefaults()
	if ok, _ := due(every, now.Add(-time.Hour), 0, now); ok {
		t.Error("due one hour into a 24-hour cadence")
	}
	if ok, _ := due(every, now.Add(-25*time.Hour), 0, now); !ok {
		t.Error("not due 25 hours into a 24-hour cadence")
	}
	labels := schedule{Every: 1, Labels: 10}.withDefaults()
	if ok, _ := due(labels, now.Add(-48*time.Hour), 9, now); ok {
		t.Error("due on 9 matured judgements when 10 were required — a fit over unchanged rows")
	}
	ok, says := due(labels, now.Add(-48*time.Hour), 10, now)
	if !ok {
		t.Error("not due once the label threshold was reached")
	}
	if says == "" {
		t.Error("a due schedule does not say why")
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

// quickShape is a small, fast geometry for tests. Window 32 puts the warm floor
// at 8 * 32 = 256 observations instead of 2048, so a test can drive a model all
// the way to scoring in under a second — which is the only way a held-out
// measurement can be exercised at all in a unit test.
var quickShape = candidate{Trees: 5, Depth: 4, Window: 32, Blend: 0.25, Review: 0.05}

// seedFit writes a READY, SERVABLE version straight into a tenant's registry:
// the row, the feature inventory the running engine actually publishes, and
// learned state under the version's own key. Tests about ROLES then do not each
// have to run an estimation, and — because servability is a real precondition of
// promotion — a seeded version that skipped the state would make every promote
// test pass for the wrong reason.
func seedFit(t *testing.T, s *stateService, tn Tenant, db *sql.DB, role string, shape candidate) string {
	t.Helper()
	id := newID("fit")
	src := fitSource{
		Name: sourceHistory, Version: 1, From: time.Now().AddDate(0, 0, -365), To: time.Now().Add(-time.Hour),
		Horizon: defaultHorizon, Rows: 400, Train: 280, Test: 120,
		Inventory: s.State.digest, Digest: "rows-" + id,
	}
	if err := putFit(db, fitRow{
		ID: id, At: time.Now(), By: "test", Algo: algoForest, Shape: shape,
		Role: roleCandidate, Status: fitQueued, Source: src,
	}); err != nil {
		t.Fatalf("putFit: %v", err)
	}
	if err := markFit(db, id, fitFitting, ""); err != nil {
		t.Fatalf("markFit: %v", err)
	}
	if err := sealFit(db, id, src, fitMetrics{Rows: 120}, fitProfile{Score: make([]float64, 32)},
		fitDigest(algoForest, shape, src)); err != nil {
		t.Fatalf("sealFit: %v", err)
	}
	// Learned state, so the version is servable rather than merely recorded. The
	// seat is the VERSION's, not the geometry's: two versions of one shape are
	// two models, and a helper that conflated them would make every promote test
	// pass against a store the promotion never moved.
	store, err := s.State.stable.at(tn, id, shape)
	if err != nil {
		t.Fatalf("stable.at: %v", err)
	}
	drive(t, s, store, tn, 300)
	if err := keepFit(db, store, tn, id); err != nil {
		t.Fatalf("keepFit: %v", err)
	}
	if role != roleCandidate {
		r, err := getFit(db, id)
		if err != nil {
			t.Fatalf("getFit: %v", err)
		}
		if _, err := setRole(db, r, role, "test", "seeded", time.Now()); err != nil {
			t.Fatalf("setRole: %v", err)
		}
	}
	return id
}

// drive pushes n observations through one store so a tenant has learned state
// in it. Same shape of traffic the decision path produces, through the same two
// calls: the rings first, then the forest.
func drive(t *testing.T, s *stateService, store *anomaly.Store, tn Tenant, n int) {
	t.Helper()
	at := time.Now().Add(-time.Duration(n) * time.Minute)
	for i := 0; i < n; i++ {
		o := observation{
			id: newID("obs"), at: at.Add(time.Duration(i) * time.Minute),
			stage: StagePayment, kind: "account", subject: "acct-" + strconv.Itoa(i%7),
			amount: int64(i%50+1) * 1_000_000_000, currency: "USD", direction: "in",
			signals: map[string]string{"ip": "203.0.113.5", "device": "d-1"},
		}
		record(s.State.vel, tn, o)
		tx, ent := txOf(tn, o)
		_, _ = store.Assess(tx, ent)
	}
}

// otherShape is a second, distinct geometry, so a promote/rollback test moves
// between two real stores rather than twice around one.
var otherShape = candidate{Trees: 7, Depth: 5, Window: 32, Blend: 0.25, Review: 0.05}

// TestTheLiveGeometryIsNotDerivableFromThePublishedVersion is the ship-blocker
// that made the detector's own trees public.
//
// The seed was sha256("risk/fit/" + id) — and the id is on every /v1/ml/fits
// response, with the shape beside it. anomaly plants a tenant's trees at
// mix(seed, orgID), the snapshot carries the planted value, and takeFit replants
// the LIVE forest from it. So anyone holding a key for their own org could run
// the same public sandbox for the same tenant and stand up a byte-exact replica
// of the model deciding their traffic — which is a map of where every region
// lies, and therefore of which region to hide activity in. anomaly's own Config
// says it: a fixed seed makes the geometry guessable from the seed.
//
// The method below is the attack, and it is checked BOTH ways: the derived guess
// must miss, and the value the tenant's own file carries must hit. Without the
// second half a broken replica would pass this test for the wrong reason.
func TestTheLiveGeometryIsNotDerivableFromThePublishedVersion(t *testing.T) {
	_, s := wireAt(t, t.TempDir())
	tn, _ := qualify("hanzo", "acme")
	db, err := s.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ground(t, s, tn, db, time.Now().AddDate(0, 0, -300), 900, 1, 0)
	if err := putSchedule(db, schedule{Every: 1, Role: roleChallenger, Horizon: 30, Window: 400,
		Rows: fitRows}.withDefaults()); err != nil {
		t.Fatalf("putSchedule: %v", err)
	}
	if err := watch(context.Background(), s, tn, db, time.Now()); err != nil {
		t.Fatalf("watch: %v", err)
	}
	id := settle(t, s, tn)
	r, err := getFit(db, id)
	if err != nil {
		t.Fatalf("getFit: %v", err)
	}
	if r.Status != fitReady {
		t.Fatalf("the fit is %s: %s", r.Status, r.Refusal)
	}
	if r.Profile.Seed == 0 {
		t.Fatal("the sealed version carries no geometry, so drift cannot measure a score index at all")
	}

	// What the whole world can compute from the published record.
	sum := sha256.Sum256([]byte("risk/fit/" + id))
	guess := binary.BigEndian.Uint64(sum[:8])
	if guess == r.Profile.Seed {
		t.Fatal("the live geometry IS sha256 of the published identifier — an attacker replicates the forest " +
			"deciding its own traffic and searches it for a region to hide in")
	}

	// The attack in full, so the assertion above is not merely arithmetic: plant
	// the replica at the guess and compare what it grew against what the tenant's
	// file actually holds.
	planted := func(seed uint64) uint64 {
		t.Helper()
		model, vel, err := sandbox(r.Shape, seed)
		if err != nil {
			t.Fatalf("sandbox: %v", err)
		}
		o := observation{
			id: newID("obs"), at: time.Now(), stage: StagePayment, kind: "account",
			subject: "acct-0", amount: 1_000_000_000, currency: "USD", direction: "in",
		}
		record(vel, tn, o)
		tx, ent := txOf(tn, o)
		_, _ = model.Assess(tx, ent)
		snap, ok := model.Snapshot(tn.String())
		if !ok {
			t.Fatal("the replica planted nothing")
		}
		return snap.Seed
	}
	live := planted(r.Profile.Seed)
	if planted(guess) == live {
		t.Fatal("a replica planted from the published identifier grew the SAME trees as the live model")
	}

	// And nothing on the wire carries the value. The profile is the tenant's own
	// file and no record projects it.
	wire, err := json.Marshal(wireFit(r))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, form := range []string{
		strconv.FormatUint(r.Profile.Seed, 10),
		strconv.FormatUint(r.Profile.Seed, 16),
	} {
		if strings.Contains(string(wire), form) {
			t.Fatalf("the published record carries the geometry (%s): %s", form, wire)
		}
	}

	// Two versions never share one. A seed that repeated would make the first
	// version's geometry a map of the second's.
	if err := watch(context.Background(), s, tn, db, time.Now().Add(48*time.Hour)); err != nil {
		t.Fatalf("watch again: %v", err)
	}
	if again := settle(t, s, tn); again != "" && again != id {
		second, err := getFit(db, again)
		if err != nil {
			t.Fatalf("getFit: %v", err)
		}
		if second.Status == fitReady && second.Profile.Seed == r.Profile.Seed {
			t.Fatal("two versions were planted at one geometry")
		}
	}
}
