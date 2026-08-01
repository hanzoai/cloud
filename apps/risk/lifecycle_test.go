package risk

// lifecycle_test.go proves the three things that make a model plane a control
// rather than a demo: it survives the rollout, promotion is earned and rollback
// is instant, and degradation is noticed.
//
// cloud deploys strategy Recreate at ONE replica, so "survives the rollout" is
// not a nicety here — it is the difference between a control that is on and one
// that has been quietly off since the last deploy.

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/luxfi/aml/pkg/anomaly"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// wireAt mounts the surface over a NAMED data directory, so a second process can
// be stood up over the same tenant files. wireApp's t.TempDir() is per call and
// cannot express a restart.
func wireAt(t *testing.T, dir string) (*zip.App, *stateService) {
	t.Helper()
	deps := cloud.Deps{Logger: luxlog.New("risktest"), DataDir: dir, Brand: "hanzo"}
	s, err := build(deps)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	app := zip.New(zip.Config{Logger: luxlog.New("risktest"), DisableStartupMessage: true})
	mount(s, app)
	t.Cleanup(s.State.shelf.close)
	return app, s
}

// ── the rollout ─────────────────────────────────────────────────────────────

// TestAPromotedModelSurvivesARollout is the constraint this whole layer was
// shaped around.
//
// cloud is Recreate at one replica: every deploy drops the process and with it
// every in-memory forest. A champion that comes back with nothing learned
// declines to score for its whole warm period, and a control that is off for
// however long that takes is a control that was off — reported only if somebody
// reads Refusal, and silent if not.
//
// So: drive a tenant's promoted model to warm, tear the process down, stand a
// NEW one up over the same files, touch the tenant, and assert the model is back
// with the same memory and still scoring.
func TestAPromotedModelSurvivesARollout(t *testing.T) {
	dir := t.TempDir()
	tn, _ := qualify("hanzo", "acme")

	// ── the first process ──
	_, first := wireAt(t, dir)
	db, err := first.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	id := seedFit(t, first, tn, db, roleChampion, quickShape)

	store, err := first.State.stable.at(tn, quickShape)
	if err != nil {
		t.Fatalf("stable.at: %v", err)
	}
	before := store.State(tn.String())
	if !before.Warm {
		t.Fatalf("the seeded champion never warmed (%d learned, floor %d); the test cannot tell a restored "+
			"model from a fresh one", before.Learned, store.Config().Appetite.Warm)
	}

	// The rollout: shutdown snapshots every resident geometry, not only the
	// shipped one.
	if err := teardown(first); err != nil {
		t.Fatalf("teardown: %v", err)
	}

	// ── the second process, over the same files ──
	_, second := wireAt(t, dir)
	if s := mustStore(t, second, tn, quickShape).State(tn.String()); s.Learned != 0 {
		t.Fatalf("the new process started with %d learned observations before touching the tenant — "+
			"state is leaking across processes some other way", s.Learned)
	}

	// FIRST TOUCH is what hydrates. It is lazy rather than eager because the
	// process cannot know which tenants exist without walking every org's
	// directory, and that walk grows with the customer list.
	db2, err := second.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	hydrate(second, tn, db2)

	after := mustStore(t, second, tn, quickShape).State(tn.String())
	if after.Learned != before.Learned {
		t.Fatalf("learned %d after the rollout, %d before — the model came back with a different memory",
			after.Learned, before.Learned)
	}
	if after.Cut != before.Cut {
		t.Fatalf("the threshold came back as %v, was %v — the appetite would be honoured at a different line",
			after.Cut, before.Cut)
	}
	if !after.Warm {
		t.Fatal("the champion came back WARMING — it declines to score, which reads as a clean world")
	}
	champ, ok, err := fitInRole(db2, roleChampion)
	if err != nil || !ok || champ.ID != id {
		t.Fatalf("champion after the rollout = %q (%v), want %q", champ.ID, ok, id)
	}
}

// TestAChampionThatCannotBeRestoredRefusesToScore is the fail-secure half, and
// it is the one worth more than the happy path.
//
// The wrong answer to a lost snapshot is a fresh model: it has no reference
// window, so it answers "unremarkable" to everything, and a control answering
// unremarkable to everything is indistinguishable from a clean world. The right
// answer is warming — refuse to score, and say so.
func TestAChampionThatCannotBeRestoredRefusesToScore(t *testing.T) {
	dir := t.TempDir()
	tn, _ := qualify("hanzo", "acme")

	_, first := wireAt(t, dir)
	db, err := first.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	id := seedFit(t, first, tn, db, roleChampion, quickShape)
	if err := teardown(first); err != nil {
		t.Fatalf("teardown: %v", err)
	}

	_, second := wireAt(t, dir)
	db2, err := second.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// The snapshot is gone — a disk fault, a truncated write, a botched restore.
	// (teardown closed the first process's handles, so this is the new one's.)
	if _, err := db2.Exec(`DELETE FROM model WHERE id = ?`, fitKey(id)); err != nil {
		t.Fatalf("delete snapshot: %v", err)
	}
	hydrate(second, tn, db2)

	st := mustStore(t, second, tn, quickShape).State(tn.String())
	if st.Warm {
		t.Fatal("a champion with no restorable state came back WARM — it is scoring from memory it does not have")
	}
	if st.Learned != 0 {
		t.Fatalf("learned %d from a deleted snapshot", st.Learned)
	}
	// And promotion of such a version is refused at the precondition, so the
	// situation cannot be entered deliberately either.
	r, err := getFit(db2, id)
	if err != nil {
		t.Fatalf("getFit: %v", err)
	}
	if err := servable(second, tn, db2, r); err == nil {
		t.Fatal("a version with no learned state reports itself servable")
	}
}

func mustStore(t *testing.T, s *stateService, tn Tenant, shape candidate) *anomaly.Store {
	t.Helper()
	store, err := s.State.stable.at(tn, shape)
	if err != nil {
		t.Fatalf("stable.at: %v", err)
	}
	return store
}

// ── promote and roll back ───────────────────────────────────────────────────

// TestPromoteIsEarnedAndRollbackIsInstant is the champion-challenger discipline
// in one sequence, over the wire.
//
// The rule it pins: a version reaches the decision path only after it has scored
// the live stream beside the one it replaces — UNLESS it has decided real
// traffic before, which is what a rollback is. That exception is not a loophole,
// it is the point: the emergency where a rollback is refused by the same gate
// that should have stopped the promotion is the worst failure this plane has.
func TestPromoteIsEarnedAndRollbackIsInstant(t *testing.T) {
	dir := t.TempDir()
	app, s := wireAt(t, dir)
	tn, _ := qualify("hanzo", "acme")
	db, err := s.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	a := seedFit(t, s, tn, db, roleCandidate, quickShape)
	b := seedFit(t, s, tn, db, roleCandidate, otherShape)

	// 1. A is promoted with no incumbent to displace: allowed.
	rec := role(t, app, a, `{"role":"champion","reason":"first model"}`, http.StatusOK)
	if rec.Role != roleChampion || rec.Served == "" {
		t.Fatalf("A after promotion: role %q served %q, want champion with a served time", rec.Role, rec.Served)
	}

	// 2. B straight to champion is REFUSED. It has never scored beside A, so
	//    promoting it is putting an unmeasured model in front of customers.
	code, body := req(t, app, http.MethodPut, "/v1/ml/fits/"+b+"/role", "acme", "u_acme",
		`{"role":"champion","reason":"looks good"}`)
	if code != http.StatusConflict {
		t.Fatalf("an unproven version promoted straight to champion = %d %s, want 409", code, body)
	}

	// 3. A promotion with no stated reason is refused whatever the version.
	code, body = req(t, app, http.MethodPut, "/v1/ml/fits/"+b+"/role", "acme", "u_acme", `{"role":"challenger"}`)
	if code != 422 {
		t.Fatalf("a role change with no reason = %d %s, want 422", code, body)
	}

	// 4. B becomes the challenger, then champion. A is stood down to retired and
	//    KEEPS its learned state — which is the whole mechanism behind step 5.
	role(t, app, b, `{"role":"challenger","reason":"trial"}`, http.StatusOK)
	rec = role(t, app, b, `{"role":"champion","reason":"beat the incumbent over the trial"}`, http.StatusOK)
	if rec.Role != roleChampion {
		t.Fatalf("B after promotion: role %q", rec.Role)
	}
	was, err := getFit(db, a)
	if err != nil {
		t.Fatalf("getFit: %v", err)
	}
	if was.Role != roleRetired {
		t.Fatalf("the displaced champion is %q, want retired", was.Role)
	}
	if _, err := getModel(db, fitKey(a)); err != nil {
		t.Fatalf("the retired champion's learned state is gone (%v) — a rollback would be a re-warm", err)
	}

	// 5. ROLLBACK. A has served, so it goes back instantly with no trial and no
	//    ceremony, and it comes back WARM because its state was kept.
	rec = role(t, app, a, `{"role":"champion","reason":"rollback: B is declining good customers"}`, http.StatusOK)
	if rec.Role != roleChampion {
		t.Fatalf("rollback to A: role %q, want champion", rec.Role)
	}
	if st := mustStore(t, s, tn, quickShape).State(tn.String()); !st.Warm {
		t.Fatal("the rolled-back champion is warming — the rollback is a blind window, not a rollback")
	}
	if store, id := champion(s, tn, db); id != a || store == nil {
		t.Fatalf("the deciding model is %q, want %q", id, a)
	}

	// 6. Every transition is on the record, with who and why. A control that
	//    changed hands and cannot say who changed it is one nobody owns.
	moves, err := fitMoves(db, a)
	if err != nil {
		t.Fatalf("moves: %v", err)
	}
	if len(moves) < 3 {
		t.Fatalf("A records %d transitions, want at least 3 (promote, retire, rollback)", len(moves))
	}
	last := moves[len(moves)-1]
	if last.Now != roleChampion || last.By == "" || last.Reason == "" {
		t.Fatalf("the rollback record is incomplete: %+v", last)
	}
	if last.By != "u_acme" {
		t.Fatalf("the rollback was recorded against %q, want the validated caller u_acme", last.By)
	}

	// 7. And there is still exactly one champion.
	champ, ok, err := fitInRole(db, roleChampion)
	if err != nil || !ok || champ.ID != a {
		t.Fatalf("champion = %q (%v), want %q", champ.ID, ok, a)
	}
}

// role drives one role change over the wire and returns the record.
func role(t *testing.T, app *zip.App, id, body string, want int) mlFitRecord {
	t.Helper()
	code, out := req(t, app, http.MethodPut, "/v1/ml/fits/"+id+"/role", "acme", "u_acme", body)
	if code != want {
		t.Fatalf("PUT role %s %s = %d %s, want %d", id, body, code, out, want)
	}
	var rec mlFitRecord
	if err := json.Unmarshal(out, &rec); err != nil {
		t.Fatalf("unmarshal %s: %v", out, err)
	}
	return rec
}

// TestAChallengerScoresBesideTheChampionAndDecidesNothing pins the trial itself:
// both models see the same decision, only one of them answers, and the
// comparison is recorded.
func TestAChallengerScoresBesideTheChampionAndDecidesNothing(t *testing.T) {
	dir := t.TempDir()
	app, s := wireAt(t, dir)
	tn, _ := qualify("hanzo", "acme")
	db, err := s.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	champ := seedFit(t, s, tn, db, roleChampion, quickShape)
	chall := seedFit(t, s, tn, db, roleChallenger, otherShape)

	for i := 0; i < 20; i++ {
		code, body := req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
			`{"stage":"payment","subject":{"kind":"transaction","id":"tx-`+strconv.Itoa(i)+`"},
			  "amount":{"nano":5000000000,"currency":"USD","direction":"in"},
			  "signals":{"ip":"203.0.113.9","device":"dev-a"}}`)
		if code != http.StatusOK {
			t.Fatalf("decide = %d %s", code, body)
		}
		var d riskDecision
		if err := json.Unmarshal(body, &d); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		// The challenger contributes NOTHING to what came back. Every hit is a
		// rule or the champion's model, and the challenger's fit id appears nowhere.
		for _, h := range d.Hits {
			if h.Rule == chall {
				t.Fatal("the challenger's evidence reached the decision it was supposed to observe")
			}
		}
	}

	got, err := readTally(db, chall, 0)
	if err != nil {
		t.Fatalf("tally: %v", err)
	}
	if got.Rows != 20 {
		t.Fatalf("the challenger saw %d of 20 decisions — the trial is not over identical traffic", got.Rows)
	}
	if got.Champion != champ {
		t.Fatalf("the comparison names champion %q, want %q", got.Champion, champ)
	}
	// And it is readable over the wire.
	code, body := req(t, app, http.MethodGet, "/v1/ml/fits/"+chall+"/tally", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("tally = %d %s", code, body)
	}
	var out mlTallyOut
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Rows != 20 || out.Refusal != "" {
		t.Fatalf("tally over the wire: %d rows, refusal %q", out.Rows, out.Refusal)
	}
}

// TestTheStableIsBoundedPerTenant pins the memory bound AND the isolation that
// makes it safe.
//
// This is the fleet's recurring defect class stated as a test: one process-wide
// store shared by every tenant, with a global eviction, is a tenant-isolation
// failure and a cheap cross-tenant denial of service at once — one org fills it,
// another org's control goes quiet with no error and no alert. So the bound here
// is PER TENANT and the eviction can only ever reach the tenant that caused it.
//
// Two claims, both asserted: a tenant holds at most seatsPerTenant geometries
// and its own oldest goes when it asks for another; and a neighbour hammering
// geometries cannot displace a seat that is not its own.
func TestTheStableIsBoundedPerTenant(t *testing.T) {
	_, s := wireAt(t, t.TempDir())
	loud, _ := qualify("hanzo", "loud")
	quiet, _ := qualify("hanzo", "quiet")

	shape := func(i int) candidate {
		return candidate{Trees: 5 + i, Depth: 4, Window: 32, Blend: 0.25, Review: 0.05}
	}

	// The quiet tenant takes one seat and learns something in it, so the claim is
	// about a model with memory and not about an empty map entry.
	held, err := s.State.stable.at(quiet, shape(0))
	if err != nil {
		t.Fatalf("stable.at: %v", err)
	}
	drive(t, s, held, quiet, 300)
	if _, ok := held.Snapshot(quiet.String()); !ok {
		t.Fatal("the quiet tenant's seat holds no learned state, so the test cannot tell it was kept")
	}

	// The loud one asks for far more geometries than it may hold.
	for i := 0; i < seatsPerTenant+8; i++ {
		if _, err := s.State.stable.at(loud, shape(i)); err != nil {
			t.Fatalf("stable.at refused a geometry for its own tenant: %v", err)
		}
	}

	s.State.stable.mu.Lock()
	var mine, theirs int
	for k := range s.State.stable.held {
		switch k.tenant {
		case loud:
			mine++
		case quiet:
			theirs++
		}
	}
	s.State.stable.mu.Unlock()
	if mine > seatsPerTenant {
		t.Fatalf("one tenant holds %d resident geometries, bound is %d", mine, seatsPerTenant)
	}
	if theirs != 1 {
		t.Fatalf("the quiet tenant holds %d seats, want 1 — a neighbour's traffic displaced it", theirs)
	}
	// And the neighbour's MODEL is still there, not merely its map entry: an
	// anomaly.Store built for one tenant cannot be asked for another's key, which
	// is what makes the isolation structural rather than a bound someone chose.
	if _, ok := held.Snapshot(quiet.String()); !ok {
		t.Fatal("the quiet tenant's learned state was evicted by a neighbour's activity")
	}

	// The SHIPPED shape is always reachable, however busy the stable is: it is
	// the base store and is not one of the bounded set.
	if _, err := s.State.stable.at(loud, shapeOf(s.State.model.Config())); err != nil {
		t.Fatalf("the shipped geometry became unreachable: %v", err)
	}
}

// TestATrialDoesNotGrowWithoutBound is the disk-fill this design would otherwise
// have. A challenger writes one row per DECISION for as long as it runs, and the
// tally reads no further back than trialDepth — so everything older is storage
// nobody can read, in a per-tenant SQLite file on a pod with one disk.
func TestATrialDoesNotGrowWithoutBound(t *testing.T) {
	_, s := wireAt(t, t.TempDir())
	tn, _ := qualify("hanzo", "acme")
	db, err := s.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	at := time.Now().Add(-2 * time.Hour)
	over := trialDepth + 250
	for i := 0; i < over; i++ {
		if err := putChallenge(db, challengeRow{
			Decision: "dec-" + strconv.Itoa(i), At: at.Add(time.Duration(i) * time.Second),
			Champion: "champ", Fit: "chall", Score: 0.5, Cut: 0.4, Scored: true,
		}); err != nil {
			t.Fatalf("putChallenge: %v", err)
		}
	}
	if err := pruneTrials(db); err != nil {
		t.Fatalf("pruneTrials: %v", err)
	}
	var kept int
	if err := db.QueryRow(`SELECT COUNT(*) FROM challenge`).Scan(&kept); err != nil {
		t.Fatalf("count: %v", err)
	}
	if kept != trialDepth {
		t.Fatalf("the trial kept %d comparisons of %d written, bound is %d", kept, over, trialDepth)
	}
	// The MOST RECENT are the ones kept: a tally over the oldest thousand of a
	// running trial describes a model as it was, not as it is.
	var oldest string
	if err := db.QueryRow(`SELECT decision FROM challenge ORDER BY at ASC LIMIT 1`).Scan(&oldest); err != nil {
		t.Fatalf("oldest: %v", err)
	}
	if oldest != "dec-250" {
		t.Fatalf("the oldest kept comparison is %q, want dec-250 — the prune dropped the wrong end", oldest)
	}
}

// TestOneTenantsLearnedStateCannotEnterAnother is the isolation claim about the
// thing the registry actually persists: not rows, but a forest's mass counters.
//
// Those counters describe where a tenant's activity is dense. Loaded into
// another tenant's model they would move that tenant's threshold and its
// decisions, and the two files sit side by side under one data directory — so
// "the id is different" is not the control. The control is that the snapshot
// NAMES its tenant and a restore into anyone else is refused, at this package's
// boundary and again at the engine's.
func TestOneTenantsLearnedStateCannotEnterAnother(t *testing.T) {
	dir := t.TempDir()
	_, s := wireAt(t, dir)
	a, _ := qualify("hanzo", "acme")
	b, _ := qualify("hanzo", "beta")

	adb, err := s.State.shelf.open(a)
	if err != nil {
		t.Fatalf("open A: %v", err)
	}
	mine := seedFit(t, s, a, adb, roleChampion, quickShape)

	// B's own store, at the same geometry. A shared store would make this the
	// same object; a per-tenant one makes them two, which is the point.
	theirs, err := s.State.stable.at(b, quickShape)
	if err != nil {
		t.Fatalf("stable.at: %v", err)
	}
	if same, _ := s.State.stable.at(a, quickShape); same == theirs {
		t.Fatal("two tenants at one geometry share a store — one can evict the other's model")
	}

	// The direct attempt: A's kept state, B's store, B's key.
	if err := takeFit(adb, theirs, b, mine); err == nil {
		t.Fatal("A's learned state was loaded into B's model")
	}
	if st := theirs.State(b.String()); st.Learned != 0 {
		t.Fatalf("B's model absorbed %d observations it never saw", st.Learned)
	}

	// And B's own hydrate, over B's own file, brings back nothing of A's: B has
	// no version, so B runs the shipped shape and its seat stays empty.
	bdb, err := s.State.shelf.open(b)
	if err != nil {
		t.Fatalf("open B: %v", err)
	}
	hydrate(s, b, bdb)
	if v, ok := s.State.stable.serving(b); ok && (v.champion.id != "" || v.challenger.id != "") {
		t.Fatalf("B serves %q/%q — a neighbour's roles reached it", v.champion.id, v.challenger.id)
	}
	if st := theirs.State(b.String()); st.Learned != 0 {
		t.Fatalf("B's model holds %d observations after hydrate", st.Learned)
	}
}

// TestAnEvictedModelIsReloadedNotLostForever is the other half of the bound.
//
// A bound that evicts is only safe if what it evicted comes back. Restoring once
// per process is correct until something drops the state, and something does —
// so this drops a tenant's resident model the way an eviction does, touches the
// tenant, and asserts the model is BACK. Without on-demand reload the champion
// returns empty and scores nothing for the life of the process, which reads as a
// clean world.
func TestAnEvictedModelIsReloadedNotLostForever(t *testing.T) {
	dir := t.TempDir()
	app, s := wireAt(t, dir)
	tn, _ := qualify("hanzo", "acme")
	db, err := s.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	id := seedFit(t, s, tn, db, roleChampion, quickShape)
	// Touch the tenant once so the process is serving the promoted version.
	if code, body := req(t, app, http.MethodGet, "/v1/ml/fits", "acme", "u_acme", ""); code != http.StatusOK {
		t.Fatalf("fits = %d %s", code, body)
	}
	before := mustStore(t, s, tn, quickShape).State(tn.String())
	if before.Learned == 0 {
		t.Fatal("the champion holds nothing before the eviction; the test cannot tell a reload from a fresh model")
	}

	// The eviction, exactly as the engine performs it: the seat is dropped and
	// the next ask builds an empty store.
	s.State.stable.mu.Lock()
	for k := range s.State.stable.held {
		delete(s.State.stable.held, k)
		delete(s.State.stable.used, k)
	}
	s.State.stable.mu.Unlock()
	if st := mustStore(t, s, tn, quickShape).State(tn.String()); st.Learned != 0 {
		t.Fatal("the eviction did not actually drop the model")
	}

	// One request. That is all the reload may cost.
	if code, body := req(t, app, http.MethodGet, "/v1/ml/fits", "acme", "u_acme", ""); code != http.StatusOK {
		t.Fatalf("fits = %d %s", code, body)
	}
	after := mustStore(t, s, tn, quickShape).State(tn.String())
	if after.Learned != before.Learned {
		t.Fatalf("the evicted champion came back with %d learned, want %d — an eviction permanently silenced "+
			"a control (fit %s)", after.Learned, before.Learned, id)
	}
	if !after.Warm {
		t.Fatal("the reloaded champion is warming, so it declines to score")
	}
}

// ── the queue ───────────────────────────────────────────────────────────────

// TestAScheduledEstimationRunsSealsAndEnrols walks the automatic path end to
// end: the trigger fires, the estimation runs off the request path, the artefact
// is sealed once with a digest and its learned state, and the schedule's chosen
// landing role is applied — CANDIDATE or CHALLENGER, never champion.
func TestAScheduledEstimationRunsSealsAndEnrols(t *testing.T) {
	_, s := wireAt(t, t.TempDir())
	tn, _ := qualify("hanzo", "acme")
	db, err := s.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// A year of matured history — old enough to clear a 30-day horizon, with
	// enough entity churn for the group-temporal split to hold something out.
	ground(t, s, tn, db, time.Now().AddDate(0, 0, -300), 900, 1, 0)

	if err := putSchedule(db, schedule{Every: 1, Role: roleChallenger, Horizon: 30, Window: 400,
		Rows: fitRows}.withDefaults()); err != nil {
		t.Fatalf("putSchedule: %v", err)
	}
	if err := watch(s, tn, db, time.Now()); err != nil {
		t.Fatalf("watch: %v", err)
	}
	id := settle(t, s, tn)
	if id == "" {
		t.Fatal("the schedule was due and nothing was queued")
	}

	r, err := getFit(db, id)
	if err != nil {
		t.Fatalf("getFit: %v", err)
	}
	if r.Status != fitReady {
		t.Fatalf("the scheduled fit is %s: %s", r.Status, r.Refusal)
	}
	if r.Digest == "" || r.Source.Digest == "" || r.Source.Inventory == "" {
		t.Fatalf("a sealed fit is missing its lineage: %+v", r.Source)
	}
	if r.Source.Rows == 0 || r.Source.Train == 0 || r.Source.Test == 0 {
		t.Fatalf("a sealed fit records no split: %d rows, %d train, %d test",
			r.Source.Rows, r.Source.Train, r.Source.Test)
	}
	if r.Source.Version != 1 {
		t.Fatalf("the first version is %d, want 1", r.Source.Version)
	}
	if r.Role != roleChallenger {
		t.Fatalf("the scheduled fit landed as %q, want challenger", r.Role)
	}
	if _, err := getModel(db, fitKey(id)); err != nil {
		t.Fatalf("a ready fit has no learned state (%v) — it could be promoted and would not score", err)
	}

	// THE ARTEFACT IS IMMUTABLE. A second seal — a racing worker, a retry, a
	// cancelled fit completing late — matches zero rows and changes nothing.
	before := r.Digest
	err = sealFit(db, id, fitSource{Name: "forged", Digest: "rows-other"}, fitMetrics{Rows: 1},
		fitProfile{}, "forged-digest")
	if err == nil {
		t.Fatal("a sealed fit was sealed again — the artefact is not immutable")
	}
	again, err := getFit(db, id)
	if err != nil {
		t.Fatalf("getFit: %v", err)
	}
	if again.Digest != before || again.Source.Name != sourceHistory {
		t.Fatalf("the artefact moved after sealing: digest %q source %q", again.Digest, again.Source.Name)
	}

	// And a second tick over unchanged data does NOT re-estimate: the same rows
	// would mint a new identifier for the same artefact and reset nothing.
	if err := watch(s, tn, db, time.Now()); err != nil {
		t.Fatalf("watch again: %v", err)
	}
	rows, err := listFits(db, 50)
	if err != nil {
		t.Fatalf("listFits: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d versions after a second tick over unchanged data, want 1", len(rows))
	}
}

// TestACancelledEstimationSaysSo pins that cancelling leaves a record. A queue
// entry that vanishes leaves nobody able to tell a cancelled estimation from one
// that was never asked for.
func TestACancelledEstimationSaysSo(t *testing.T) {
	app, s := wireAt(t, t.TempDir())
	tn, _ := qualify("hanzo", "acme")
	db, err := s.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	id := newID("fit")
	if err := putFit(db, fitRow{ID: id, At: time.Now(), Algo: algoForest,
		Role: roleCandidate, Status: fitQueued}); err != nil {
		t.Fatalf("putFit: %v", err)
	}
	parked := make(chan struct{})
	done := make(chan struct{})
	if err := s.State.bench.start(tn, id, func(ctx context.Context) {
		close(parked)
		<-ctx.Done()
		close(done)
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	<-parked

	code, body := req(t, app, http.MethodPost, "/v1/ml/fits/"+id+"/cancel", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("cancel = %d %s", code, body)
	}
	var rec mlFitRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rec.Status != fitCancelled {
		t.Fatalf("a cancelled fit reads as %q", rec.Status)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the runner was not cancelled — a caller can pin a worker slot indefinitely")
	}
	// Cancelling something already finished is a 409, not a silent success.
	code, body = req(t, app, http.MethodPost, "/v1/ml/fits/"+id+"/cancel", "acme", "u_acme", "")
	if code != http.StatusConflict {
		t.Fatalf("cancelling a cancelled fit = %d %s, want 409", code, body)
	}
}

// settle waits for the tenant's queued estimation to finish and returns its id.
func settle(t *testing.T, s *stateService, tn Tenant) string {
	t.Helper()
	id, running := s.State.bench.running(tn)
	deadline := time.Now().Add(60 * time.Second)
	for running && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		var again string
		again, running = s.State.bench.running(tn)
		if again != "" {
			id = again
		}
	}
	if running {
		t.Fatal("an estimation did not finish inside its deadline")
	}
	return id
}
