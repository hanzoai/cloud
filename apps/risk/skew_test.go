package risk

// skew_test.go proves the training–serving skew control actually controls
// something, and that the record behind a decision is one record.
//
// Every test here was written against the defect it names: the fix was reverted,
// the test was run, and it went red. A control whose test passes with the control
// removed is a comment.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// A REFIT CANNOT RE-BLESS STALE COORDINATES.
//
// The shape gate refuses a map fitted under coordinates that have moved, and the
// refusal's own remedy is "fit again". If a refit reads the same rows — every one
// of them scored under the OLD shape — and stamps the NEW shape on them, then the
// gate is a door that opens from the inside: one POST clears the control with no
// new evidence at all, and the plane resumes reporting probabilities derived from
// a coordinate system that no longer exists.
//
// A fit reads ONE coordinate system. After a governed change the evidence under
// the new shape is empty, so the fit is refused and says how much evidence it had
// to leave behind and why.
func TestARefitCannotReblessCoordinatesThatMoved(t *testing.T) {
	app, s := wireApp(t)
	tn := Tenant("hanzo/acme")
	seedJudged(t, s, tn, 200)

	code, body := req(t, app, http.MethodPost, "/v1/ml/calibrate", "acme", "u_acme", `{"horizon":0}`)
	if code != http.StatusCreated {
		t.Fatalf("calibrate = %d %s", code, body)
	}
	var first mlCalibrationView
	if err := json.Unmarshal(body, &first); err != nil {
		t.Fatal(err)
	}
	if !first.Current || first.Rows != 200 {
		t.Fatalf("the first fit is not the premise this test needs: current=%v rows=%d", first.Current, first.Rows)
	}

	// A governed change. Every one of the 200 rows was scored under the old shape.
	code, body = req(t, app, http.MethodPost, "/v1/risk/rules", "acme", "u_acme",
		`{"rule":{"id":"skew-new","name":"new","stage":"payment","action":"review",
		  "weight":0.5,"severity":"high","enabled":true,
		  "all":[{"field":"amount.nano","op":"gte","number":1}]}}`)
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("rule = %d %s", code, body)
	}

	// The documented remedy, run against evidence that predates the change.
	code, body = req(t, app, http.MethodPost, "/v1/ml/calibrate", "acme", "u_acme", `{"horizon":0}`)
	if code != http.StatusCreated {
		t.Fatalf("refit = %d %s", code, body)
	}
	var second mlCalibrationView
	if err := json.Unmarshal(body, &second); err != nil {
		t.Fatal(err)
	}
	if second.Fitted {
		t.Errorf("the refit produced a map (shape %.12s, %d rows) out of history scored entirely under "+
			"shape %.12s. The skew control was cleared by one POST with no new observation.",
			second.Shape, second.Rows, first.Shape)
	}
	if second.Refusal == "" {
		t.Error("the refit refused and did not say why")
	}
	if second.Superseded != 200 {
		t.Errorf("superseded = %d, want 200 — the answer does not say how much evidence the shape "+
			"boundary put out of reach, so an operator cannot tell a thin tenant from a moved one",
			second.Superseded)
	}

	// And the map in force still refuses, because nothing about the world changed.
	code, body = req(t, app, http.MethodGet, "/v1/ml/calibration", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("calibration = %d %s", code, body)
	}
	var now mlCalibrationView
	if err := json.Unmarshal(body, &now); err != nil {
		t.Fatal(err)
	}
	if now.Current {
		t.Error("the map in force reports current after a shape change it was not fitted under")
	}
}

// EVERY OP READS THE MAP THROUGH THE SHAPE IN FORCE.
//
// The gate used to be Map.P(score, shape), and every internal caller passed the
// map's OWN shape — so the comparison was between a value and itself and the
// control was inert everywhere except the decide path. The consequence was not
// subtle: with the live plane refusing to state any probability, /v1/ml/evaluate
// still reported a Brier, /v1/ml/replay still moved rows onto rungs nothing could
// reach, and the reliability chart drew the map the same response refused to use.
//
// Binding at the boundary makes all three answer the same way the decide path
// does. This test moves the shape and then asks all three.
func TestMeasurementRefusesTheMapTheDecidePathRefuses(t *testing.T) {
	app, s := wireApp(t)
	tn := Tenant("hanzo/acme")
	seedJudged(t, s, tn, 200)

	if code, body := req(t, app, http.MethodPost, "/v1/ml/calibrate", "acme", "u_acme", `{"horizon":0}`); code != http.StatusCreated {
		t.Fatalf("calibrate = %d %s", code, body)
	}
	if code, body := req(t, app, http.MethodPut, "/v1/risk/policy", "acme", "u_acme",
		`{"stage":"payment","floor":"allow","reason":"under test","bands":[{"at":0.2,"action":"review"},{"at":0.6,"action":"block"}]}`); code != http.StatusOK {
		t.Fatalf("policy = %d %s", code, body)
	}
	if code, body := req(t, app, http.MethodPost, "/v1/risk/rules", "acme", "u_acme",
		`{"rule":{"id":"skew-two","name":"new","stage":"payment","action":"review",
		  "weight":0.5,"severity":"high","enabled":true,
		  "all":[{"field":"amount.nano","op":"gte","number":1}]}}`); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("rule = %d %s", code, body)
	}

	code, body := req(t, app, http.MethodGet, "/v1/ml/calibration", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("calibration = %d %s", code, body)
	}
	var view mlCalibrationView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if view.Current {
		t.Fatal("the shape did not move; the test proves nothing")
	}
	if len(view.Reliability) > 0 {
		t.Errorf("the response that says the map refuses to answer shipped %d reliability bins, "+
			"computed by applying that very map", len(view.Reliability))
	}

	code, body = req(t, app, http.MethodPost, "/v1/ml/evaluate", "acme", "u_acme", `{"stage":"payment","horizon":0}`)
	if code != http.StatusOK {
		t.Fatalf("evaluate = %d %s", code, body)
	}
	var meas mlMeasurement
	if err := json.Unmarshal(body, &meas); err != nil {
		t.Fatal(err)
	}
	if meas.Metrics.Brier != nil {
		t.Errorf("evaluate reported Brier=%.6f under a calibration the decide path refuses", *meas.Metrics.Brier)
	}
	if meas.Calibrated {
		t.Error("evaluate says it was calibrated while the map in force refuses to answer")
	}
	if meas.Refusal == "" {
		t.Error("evaluate measured without a probability and did not say so")
	}

	code, body = req(t, app, http.MethodPost, "/v1/ml/replay", "acme", "u_acme",
		`{"stage":"payment","horizon":0,"bands":[{"at":0.2,"action":"review"},{"at":0.6,"action":"block"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("replay = %d %s", code, body)
	}
	var rep mlReplayReport
	if err := json.Unmarshal(body, &rep); err != nil {
		t.Fatal(err)
	}
	acted := 0
	for _, w := range rep.Would {
		if w.Action != ActionAllow {
			acted += w.Count
		}
	}
	if acted > 0 {
		t.Errorf("the replay says the candidate would act on %d decisions. Under the shape in force "+
			"there is no probability, so no rung is reachable and the candidate acts on none — and "+
			"this report was written down as the evidence for a threshold change.", acted)
	}
	if rep.Refusal == "" {
		t.Error("the replay reached no rung and did not say why")
	}
}

// THE BOUNDED READ TAKES THE MOST RECENT DECISIONS.
//
// Ascending order plus LIMIT takes the OLDEST rows. Past the bound that freezes
// the calibration, the evaluation, the learning curve and every replay on the
// tenant's first maxHistory decisions forever, with nothing on any response
// saying the read was cut.
func TestTheBoundedReadTakesTheNewestDecisionsAndSaysWhenItCut(t *testing.T) {
	_, s := wireApp(t)
	tn := Tenant("hanzo/acme")
	db, err := s.State.shelf.open(tn)
	if err != nil {
		t.Fatal(err)
	}
	shape, err := scoringShape(db, s.State.model.Digest())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-1000 * time.Hour)
	const n = 300
	for i := range n {
		id := fmt.Sprintf("dec_w_%05d", i)
		o := observation{
			id: id, at: base.Add(time.Duration(i) * time.Minute),
			stage: StagePayment, kind: "transaction", subject: "tx", agency: AgencyUnknown,
			amount: 1_000_000_000, currency: "USD", direction: "in", signals: map[string]string{},
		}
		out := outcome{id: id, action: ActionAllow, score: float64(i) / n, agency: o.agency}
		if err := putDecision(db, o, out, "d", shape, "", verdict{}); err != nil {
			t.Fatal(err)
		}
		if err := label(db, id, "legitimate", "u"); err != nil {
			t.Fatal(err)
		}
	}
	// The bound is maxHistory in production; the statement is identical and only
	// the number differs, so a smaller one exercises the same path in a test.
	w, err := recorded(context.Background(), db, 0, shape, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.history) != 100 {
		t.Fatalf("read %d rows, want 100", len(w.history))
	}
	first, last := w.history[0].ID, w.history[len(w.history)-1].ID
	if last != fmt.Sprintf("dec_w_%05d", n-1) {
		t.Errorf("the bounded read returned %s..%s — the OLDEST rows, not the most recent. Past "+
			"maxHistory=%d decisions every measurement is pinned to the dawn of the log.", first, last, maxHistory)
	}
	if first != fmt.Sprintf("dec_w_%05d", n-100) {
		t.Errorf("the window starts at %s, want %s: the rows must be the last 100 IN TIME ORDER",
			first, fmt.Sprintf("dec_w_%05d", n-100))
	}
	if !w.truncated {
		t.Error("the read was cut and no field says so, so a report over a truncated window reads as complete")
	}
	// And an unbounded read reports no truncation.
	all, err := recorded(context.Background(), db, 0, shape, n)
	if err != nil {
		t.Fatal(err)
	}
	if all.truncated {
		t.Error("a read that saw everything reported itself truncated")
	}
}

// A MUTE MOVES THE SCORE DISTRIBUTION, SO IT MOVES THE SHAPE.
//
// combine() sums only the hits that were not suppressed, so muting a rule for
// every subject moves every score that rule touched exactly as retiring the rule
// would. Muting is the day-to-day tuning knob and retiring is the rare act, so a
// control blind to the mute is blind to the common change.
//
// A mute that names a SUBJECT is a statement about that subject — operational
// data, like a list entry — and must not invalidate the tenant's calibration.
func TestARuleWideMuteMovesTheShapeAndASubjectMuteDoesNot(t *testing.T) {
	app, s := wireApp(t)
	db, err := s.State.shelf.open(Tenant("hanzo/acme"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := scoringShape(db, s.State.model.Digest())
	if err != nil {
		t.Fatal(err)
	}

	code, body := req(t, app, http.MethodPost, "/v1/risk/suppressions", "acme", "u_acme",
		`{"rule":"payment-card-testing","reason":"too noisy this week"}`)
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("suppress = %d %s", code, body)
	}
	muted, err := scoringShape(db, s.State.model.Digest())
	if err != nil {
		t.Fatal(err)
	}
	if muted == before {
		t.Errorf("muting a weight-bearing rule for every subject left the shape at %.12s. Every score "+
			"that rule touched has moved and the calibration still reports itself current.", muted)
	}

	code, body = req(t, app, http.MethodPost, "/v1/risk/suppressions", "acme", "u_acme",
		`{"rule":"payment-velocity-burst","subject":"tx-one-merchant","reason":"known good"}`)
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("suppress subject = %d %s", code, body)
	}
	after, err := scoringShape(db, s.State.model.Digest())
	if err != nil {
		t.Fatal(err)
	}
	if after != muted {
		t.Errorf("excusing ONE subject moved the shape from %.12s to %.12s. Operational data about "+
			"one row would expire the tenant's calibration, and it would never have one.", muted, after)
	}
}

// THE LADDER CANNOT DECLINE ON THE MODEL'S EVIDENCE ALONE.
//
// decide caps the model's own hit at modelCeiling because an unexplainable
// refusal is not a decision anybody can defend to the customer or to a chargeback
// network. The policy ladder reads the calibrated probability, and that
// probability is a pure function of the SAME score — so an uncapped escalation
// voids the cap by arithmetic, and the invariant holds only until somebody sets a
// band.
func TestTheLadderCannotDeclineOnTheModelAlone(t *testing.T) {
	modelOnly := outcome{hits: []hit{{Rule: modelRuleID, Action: ActionReview, Weight: 0.9}}}
	nothing := outcome{}
	muted := outcome{hits: []hit{{Rule: "a-rule", Action: ActionBlock, Weight: 0.7, Suppressed: true}}}
	explained := outcome{hits: []hit{{Rule: "a-rule", Action: ActionBlock, Weight: 0.7}}}

	for _, c := range []struct {
		name             string
		out              outcome
		evidence, policy string
		want             string
	}{
		{"model alone cannot block", modelOnly, ActionReview, ActionBlock, ActionReview},
		{"model alone cannot restrict", modelOnly, ActionAllow, ActionRestrict, ActionReview},
		{"model alone can still review", modelOnly, ActionAllow, ActionReview, ActionReview},
		{"no evidence at all cannot block", nothing, ActionAllow, ActionBlock, ActionReview},
		{"a muted hit is not evidence", muted, ActionAllow, ActionBlock, ActionReview},
		{"a rule the org wrote can block", explained, ActionAllow, ActionBlock, ActionBlock},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := escalate(c.evidence, c.policy, c.out); got != c.want {
				t.Errorf("escalate(%q, %q) = %q, want %q — %s",
					c.evidence, c.policy, got, c.want,
					"a decline the model alone is behind has no reason a person can read")
			}
		})
	}
}

// THE DECISION AND ITS GRADING ARE ONE RECORD.
//
// Two independent statements can land one and not the other, and the half that
// lands is the half that acts: a BLOCK on the dispute-packet surface with no
// probability, no principal reason and no refusal saying why. This proves both
// halves — that a failed grading takes the decision down with it, and that a
// decision found without one says so instead of reading as ungraded.
func TestTheDecisionAndItsGradingAreOneRecord(t *testing.T) {
	app, s := wireApp(t)
	db, err := s.State.shelf.open(Tenant("hanzo/acme"))
	if err != nil {
		t.Fatal(err)
	}
	o := observation{
		id: "dec_atomic", at: time.Now().UTC(), stage: StagePayment, kind: "transaction",
		subject: "tx", agency: AgencyUnknown, amount: 1_000_000_000, currency: "USD",
		direction: "in", signals: map[string]string{},
	}
	out := outcome{id: o.id, action: ActionBlock, score: 0.97, agency: o.agency}

	// Make the SECOND write fail. Whatever the cause in production — a disk, a
	// lock, a crash between two Execs — the property under test is that the first
	// write does not survive it.
	if _, err := db.Exec(`ALTER TABLE verdict RENAME TO verdict_away`); err != nil {
		t.Fatal(err)
	}
	if err := putDecision(db, o, out, "d", "shape", "", verdict{}); err == nil {
		t.Fatal("the grading could not be written and the write reported success")
	}
	if _, err := db.Exec(`ALTER TABLE verdict_away RENAME TO verdict`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM decision WHERE id = ?`, o.id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("the decision survived a grading that did not: a BLOCK exists with no probability, "+
			"no principal reason and nothing this plane can defend it with (%d row(s))", n)
	}

	// The read path must never present an ungraded decision as a graded one with
	// nothing to say.
	orphan := o
	orphan.id = "dec_orphan"
	if _, err := db.Exec(`INSERT INTO decision (id, at, stage, kind, subject, action, score, agency, shadow)
		VALUES (?,?,?,?,?,?,?,?,0)`,
		orphan.id, stamp(orphan.at), orphan.stage, orphan.kind, orphan.subject,
		ActionBlock, 0.97, AgencyUnknown); err != nil {
		t.Fatal(err)
	}
	code, body := req(t, app, http.MethodGet, "/v1/risk/decisions/dec_orphan", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("detail = %d %s", code, body)
	}
	var packet riskDecisionView
	if err := json.Unmarshal(body, &packet); err != nil {
		t.Fatal(err)
	}
	if len(packet.Reasons) == 0 && packet.Refusal == "" {
		t.Error("a BLOCK is served on the dispute-packet surface with no reasons, no probability and " +
			"no refusal, so an ungraded decision is indistinguishable from one with nothing to say")
	}
}

// A GOVERNANCE RECORD NAMES THE PERSON.
//
// "The org changed the thresholds" is not an answer to who changed them, and the
// helper that resolves the validated principal already exists and is already used
// by the suppression and control records in this same package.
func TestGovernanceRecordsNameThePersonAndNotTheOrg(t *testing.T) {
	app, s := wireApp(t)
	tn := Tenant("hanzo/acme")
	seedJudged(t, s, tn, 200)
	db, err := s.State.shelf.open(tn)
	if err != nil {
		t.Fatal(err)
	}

	if code, body := req(t, app, http.MethodPut, "/v1/risk/policy", "acme", "u_acme",
		`{"stage":"payment","floor":"allow","reason":"under test","bands":[{"at":0.6,"action":"review"}]}`); code != http.StatusOK {
		t.Fatalf("policy = %d %s", code, body)
	}
	if code, body := req(t, app, http.MethodPost, "/v1/ml/calibrate", "acme", "u_acme", `{"horizon":0}`); code != http.StatusCreated {
		t.Fatalf("calibrate = %d %s", code, body)
	}
	if code, body := req(t, app, http.MethodPost, "/v1/ml/replay", "acme", "u_acme",
		`{"stage":"payment","horizon":0}`); code != http.StatusCreated {
		t.Fatalf("replay = %d %s", code, body)
	}

	for _, q := range []struct{ what, stmt string }{
		{"policy", `SELECT by FROM policy ORDER BY version DESC LIMIT 1`},
		{"calibration", `SELECT by FROM calibration ORDER BY version DESC LIMIT 1`},
		{"replay", `SELECT by FROM replay ORDER BY at DESC LIMIT 1`},
	} {
		var who string
		if err := db.QueryRow(q.stmt).Scan(&who); err != nil {
			t.Fatalf("%s: %v", q.what, err)
		}
		if who != "u_acme" {
			t.Errorf("the %s record names %q as its author; the validated principal is %q",
				q.what, who, "u_acme")
		}
	}
}

// MEASUREMENT IS BOUNDED PER TENANT, AND ONLY PER TENANT.
//
// These ops read the tenant's single-writer file, so a second concurrent
// measurement for one tenant queues behind the first holding the connection that
// tenant's own decisions need. The bound is per tenant by construction: there is
// no fleet-wide number, so a caller that loops this surface degrades itself and
// nobody else.
func TestMeasurementIsBoundedPerTenantAndOnlyPerTenant(t *testing.T) {
	f := newInflight()
	release, err := f.claim(Tenant("hanzo/acme"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.claim(Tenant("hanzo/acme")); err == nil {
		t.Error("a tenant took two measurement slots at once, so it can hold every connection its " +
			"own decide path needs")
	}
	// A NEIGHBOUR IS UNAFFECTED. This is the property a fleet-wide cap does not
	// have, and the whole reason the bound is keyed on the tenant.
	other, err := f.claim(Tenant("hanzo/other"))
	if err != nil {
		t.Fatalf("one tenant's measurement refused another tenant's: %v", err)
	}
	other()
	release()
	again, err := f.claim(Tenant("hanzo/acme"))
	if err != nil {
		t.Fatalf("the slot was not released: %v", err)
	}
	again()
	// Release is idempotent: an op that returns through two paths must not free a
	// slot a later request already took.
	release()
	if _, err := f.claim(Tenant("hanzo/acme")); err != nil {
		t.Fatalf("a double release corrupted the bound: %v", err)
	}
}

// A MEASUREMENT IS PRICED BY THE WORK IT DOES.
//
// A flat price over a scan whose cost is linear in the rows it reads is a lie
// about the cheap call: a fit over forty judged decisions and a twenty-step
// learning curve over fifty thousand cost the same, and the second is the one a
// caller loops.
func TestAMeasurementIsPricedByTheRowsItReads(t *testing.T) {
	small, large := measureCents(40), measureCents(maxHistory)
	if small != qualityCents {
		t.Errorf("a read of 40 rows costs %d, want the floor %d", small, qualityCents)
	}
	if large <= small {
		t.Errorf("a read of %d rows costs %d and a read of 40 costs %d — the price does not move "+
			"with the work", maxHistory, large, small)
	}
	if measureCents(-1) != qualityCents {
		t.Error("a negative row count priced below the floor")
	}
	// And the gate is taken on the CEILING a request could reach, so the ledger is
	// debited before the work rather than after it.
	if bounded(0) != maxHistory || bounded(999999) != maxHistory || bounded(10) != 10 {
		t.Errorf("bounded(0)=%d bounded(999999)=%d bounded(10)=%d", bounded(0), bounded(999999), bounded(10))
	}
}

// A SCORE MEANS A PROBABILITY ON THE SCORES THIS PLANE ACTUALLY PRODUCES.
//
// The whole track is sold on that sentence, and the score distribution it has to
// hold for is not a continuum. combine() returns 1 - prod(1-weight) over a FIXED
// set of rule weights and decide rounds it to four places, so thousands of
// decisions land on a handful of atoms and the modal atom is exactly zero. An
// isotonic fit that does not pool ties collapses on exactly that input: every
// plateau holding a positive is reported as CERTAINTY, at the top of the range,
// where declines happen and where the number lands on an adverse-action record.
//
// End to end through the op, because the unit test in the engine cannot see the
// alphabet cloud feeds it.
func TestAScoreMeansAProbabilityOnThePlateausThisPlaneProduces(t *testing.T) {
	app, s := wireApp(t)
	tn := Tenant("hanzo/acme")
	db, err := s.State.shelf.open(tn)
	if err != nil {
		t.Fatal(err)
	}
	shape, err := scoringShape(db, s.State.model.Digest())
	if err != nil {
		t.Fatal(err)
	}
	atoms := []struct {
		score        float64
		clean, fraud int
	}{
		{0.00, 900, 2},
		{0.35, 190, 10},
		{0.50, 85, 15},
		{0.70, 45, 55},
		{0.90, 10, 40},
	}
	base := time.Now().UTC().Add(-500 * time.Hour)
	n := 0
	for _, a := range atoms {
		for i := range a.clean + a.fraud {
			judgement := "legitimate"
			if i >= a.clean {
				judgement = "fraud"
			}
			id := fmt.Sprintf("dec_atom_%05d", n)
			o := observation{
				id: id, at: base.Add(time.Duration(n) * time.Minute),
				stage: StagePayment, kind: "transaction", subject: fmt.Sprintf("tx-%d", n),
				agency: AgencyUnknown, amount: 1_000_000_000, currency: "USD", direction: "in",
				signals: map[string]string{},
			}
			out := outcome{id: id, action: ActionAllow, score: a.score, agency: o.agency}
			if err := putDecision(db, o, out, "seed-digest", shape, "", verdict{}); err != nil {
				t.Fatal(err)
			}
			if err := label(db, id, judgement, "u_seed"); err != nil {
				t.Fatal(err)
			}
			n++
		}
	}

	code, body := req(t, app, http.MethodPost, "/v1/ml/calibrate", "acme", "u_acme", `{"horizon":0}`)
	if code != http.StatusCreated {
		t.Fatalf("calibrate = %d %s", code, body)
	}
	cal, _, _, ok, err := currentCalibration(db)
	if err != nil || !ok {
		t.Fatalf("read back: %v ok=%v", err, ok)
	}
	read, err := cal.Under(shape)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range atoms {
		want := float64(a.fraud) / float64(a.clean+a.fraud)
		got := read.P(a.score)
		if d := got - want; d > 0.05 || d < -0.05 {
			t.Errorf("score %.2f reports probability %.6f; %d of %d at that score turned out productive, "+
				"so the truth is %.4f (off by %+.4f)", a.score, got, a.fraud, a.clean+a.fraud, want, d)
		}
	}
	if p := read.P(0.90); p >= 1 {
		t.Errorf("the top plateau reports certainty (%.6f). That number goes on an adverse-action record.", p)
	}
}
