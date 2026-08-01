package risk

// quality_test.go proves the four claims the scoring-quality layer makes, and
// each test was written by reintroducing the defect and checking that THIS test
// goes red:
//
//	a decision cites reason codes from a closed vocabulary
//	a score reads as a probability, and refuses to when it cannot
//	the thresholds are a per-organisation, versioned, audited record
//	a replay is reproducible, so it can be evidence for a change
//
// plus the boundary that holds all four: none of it crosses a tenant.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/luxfi/aml/pkg/calibrate"
	"github.com/luxfi/aml/pkg/reason"
	"github.com/luxfi/aml/pkg/types"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// ── the boundary ────────────────────────────────────────────────────────────

// TestQualityOpsRefuseAnUnvalidatedPrincipal pins the tenant gate on the ops
// registered in quality.go.
//
// It is not a duplicate of the same test on the other ops, and the reason is
// structural: these ops are declared on GROUPS RE-CREATED in quality.go, after
// mount installed cloud.Bridge on the original ones. fiber matches middleware by
// path prefix in registration order, so the gate does reach them — but that is a
// property of fiber's dispatch and not of anything visible in this package, and
// a surface that silently registered ungated would look exactly like this one.
func TestQualityOpsRefuseAnUnvalidatedPrincipal(t *testing.T) {
	app, _ := wireApp(t)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/risk/reasons", ""},
		{http.MethodGet, "/v1/risk/policy", ""},
		{http.MethodPut, "/v1/risk/policy", `{"stage":"payment","floor":"allow","reason":"x","bands":[{"at":0.9,"action":"block"}]}`},
		{http.MethodGet, "/v1/risk/policy/versions?stage=payment", ""},
		{http.MethodPost, "/v1/ml/calibrate", `{"horizon":0}`},
		{http.MethodGet, "/v1/ml/calibration", ""},
		{http.MethodPost, "/v1/ml/evaluate", `{"horizon":0}`},
		{http.MethodGet, "/v1/ml/learning", ""},
		{http.MethodPost, "/v1/ml/replay", `{"stage":"payment","horizon":0}`},
		{http.MethodGet, "/v1/ml/replays/replay_abc", ""},
	} {
		// An X-Org-Id with no X-User-Id is the forged-header case: the header
		// survived the edge but no credential minted it.
		code, body := req(t, app, tc.method, tc.path, "acme", "", tc.body)
		if code != http.StatusForbidden {
			t.Errorf("%s %s = %d %s, want 403 for an unvalidated principal", tc.method, tc.path, code, body)
		}
	}
}

// TestQualityIsTenantIsolated is the load-bearing boundary test for this layer.
//
// A policy, a calibration and a replay report are each something one
// organisation's thresholds and one organisation's customers are visible in. B
// must see none of A's — and a foreign replay id must answer 404 rather than
// 403, because a 403 is an oracle that distinguishes "exists and is not yours"
// from "does not exist".
func TestQualityIsTenantIsolated(t *testing.T) {
	app, s := wireApp(t)

	// A sets bands and fits a calibration off its own judged history.
	code, body := req(t, app, http.MethodPut, "/v1/risk/policy", "acme", "u_acme",
		`{"stage":"payment","floor":"allow","reason":"the dispute rate doubled",
		  "bands":[{"at":0.3,"action":"challenge"},{"at":0.8,"action":"block"}],
		  "cost":{"miss":40000000000,"alarm":2000000000}}`)
	if code != http.StatusOK {
		t.Fatalf("A set policy = %d %s", code, body)
	}
	seedJudged(t, s, Tenant("hanzo/acme"), 120)
	code, body = req(t, app, http.MethodPost, "/v1/ml/calibrate", "acme", "u_acme", `{"horizon":0}`)
	if code != http.StatusCreated {
		t.Fatalf("A calibrate = %d %s", code, body)
	}
	code, body = req(t, app, http.MethodPost, "/v1/ml/replay", "acme", "u_acme",
		`{"stage":"payment","horizon":0,"bands":[{"at":0.2,"action":"block"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("A replay = %d %s", code, body)
	}
	var mine mlReplayReport
	if err := json.Unmarshal(body, &mine); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}

	// B sees no bands.
	code, body = req(t, app, http.MethodGet, "/v1/risk/policy", "beta", "u_beta", "")
	if code != http.StatusOK {
		t.Fatalf("B policy = %d %s", code, body)
	}
	var theirs riskPolicyBook
	_ = json.Unmarshal(body, &theirs)
	if len(theirs.Items) != 0 {
		t.Errorf("B reads %d of A's policies", len(theirs.Items))
	}

	// B has no calibration, and the answer says so rather than borrowing one.
	code, body = req(t, app, http.MethodGet, "/v1/ml/calibration", "beta", "u_beta", "")
	if code != http.StatusOK {
		t.Fatalf("B calibration = %d %s", code, body)
	}
	var view mlCalibrationView
	_ = json.Unmarshal(body, &view)
	if view.Fitted {
		t.Error("B reads A's calibration — one organisation's customers describe another's probabilities")
	}
	if view.Refusal == "" {
		t.Error("B has no calibration and the answer did not say so")
	}

	// B's measurement sees none of A's decisions.
	code, body = req(t, app, http.MethodPost, "/v1/ml/evaluate", "beta", "u_beta", `{"horizon":0}`)
	if code != http.StatusOK {
		t.Fatalf("B evaluate = %d %s", code, body)
	}
	var m mlMeasurement
	_ = json.Unmarshal(body, &m)
	if m.Metrics.Rows != 0 || m.Metrics.Judged != 0 {
		t.Errorf("B measures %d rows / %d judged of A's history", m.Metrics.Rows, m.Metrics.Judged)
	}

	// A's replay is not readable by B, and the refusal is a 404.
	code, body = req(t, app, http.MethodGet, "/v1/ml/replays/"+mine.ID, "beta", "u_beta", "")
	if code != http.StatusNotFound {
		t.Errorf("B reads A's replay = %d %s, want 404", code, body)
	}
	// And A can still read its own, so the 404 is isolation and not breakage.
	code, _ = req(t, app, http.MethodGet, "/v1/ml/replays/"+mine.ID, "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Errorf("A cannot read its own replay = %d", code)
	}
}

// ── reason codes ────────────────────────────────────────────────────────────

// TestDecisionCitesReasonCodes is THE reason-code test on the wire: a decision
// that a rule fired on carries codes from the published vocabulary, strongest
// first, capped, each with a sentence and the specific instrument named.
func TestDecisionCitesReasonCodes(t *testing.T) {
	app, _ := wireApp(t)

	// The starter rule set includes "payment from a denied address", which reads
	// the ip-deny list. Putting an address on it makes a decision fire for a
	// reason that is this organisation's own explicit instruction.
	code, body := req(t, app, http.MethodPost, "/v1/risk/lists/ip-deny/entries", "acme", "u_acme",
		`{"values":["203.0.113.7"]}`)
	if code != http.StatusOK {
		t.Fatalf("add list entry = %d %s", code, body)
	}
	code, body = req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
		`{"stage":"payment","subject":{"kind":"transaction","id":"tx-9"},
		  "amount":{"nano":9000000000,"currency":"USD","direction":"in"},
		  "signals":{"ip":"203.0.113.7","device":"dev-x"}}`)
	if code != http.StatusOK {
		t.Fatalf("decide = %d %s", code, body)
	}
	var d riskDecision
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if len(d.Reasons) == 0 {
		t.Fatal("a decision a rule fired on cites no reason — the adverse-action artefact is missing")
	}
	if len(d.Reasons) > topReasons {
		t.Errorf("%d reasons cited, at most %d is an explanation", len(d.Reasons), topReasons)
	}
	for i, r := range d.Reasons {
		if !reason.Known(r.Code) {
			t.Errorf("reason %d cites %q, which the vocabulary does not publish", i, r.Code)
		}
		if r.Says == "" {
			t.Errorf("reason %d (%s) has no sentence to show anyone", i, r.Code)
		}
		if i > 0 && d.Reasons[i-1].Weight < r.Weight {
			t.Errorf("reasons are not strongest first: %+v", d.Reasons)
		}
	}
	// A rule reason names WHICH rule, or nobody can go and look at it.
	var named bool
	for _, r := range d.Reasons {
		if r.Code == reason.Rule {
			named = r.Source != ""
		}
	}
	if !named {
		t.Errorf("no rule reason names the rule that fired: %+v", d.Reasons)
	}

	// The vocabulary the decision cites from is published, and every code in it
	// is one a decision could cite.
	code, body = req(t, app, http.MethodGet, "/v1/risk/reasons", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("reasons = %d %s", code, body)
	}
	var book riskReasonBook
	_ = json.Unmarshal(body, &book)
	if len(book.Items) == 0 || book.Model == "" {
		t.Fatalf("the published vocabulary is empty or unpinned: %+v", book)
	}
	published := map[string]bool{}
	for _, c := range book.Items {
		published[c.Code] = true
	}
	for _, r := range d.Reasons {
		if !published[r.Code] {
			t.Errorf("the decision cites %q and /v1/risk/reasons does not publish it", r.Code)
		}
	}
}

// A suppressed hit is recorded, is visible, and contributed nothing to the
// action — so citing it as a reason FOR the action would be false.
func TestSuppressedEvidenceIsNotAReason(t *testing.T) {
	out := outcome{
		hits: []hit{
			{Rule: "loud", Name: "a rule that fired", Action: ActionBlock, Weight: 0.9, Suppressed: true},
			{Rule: "quiet", Name: "a rule that also fired", Action: ActionReview, Weight: 0.3},
		},
	}
	rs := reasonsOf(out)
	for _, r := range rs {
		if r.Source == "loud" {
			t.Fatalf("a muted rule was cited as a reason for the action: %+v", rs)
		}
	}
	if len(rs) != 1 || rs[0].Source != "quiet" {
		t.Fatalf("reasons = %+v, want only the rule that actually contributed", rs)
	}
}

// The model's own hit must not be cited beside its features: its contribution is
// already expressed feature by feature, so counting both counts one piece of
// evidence twice.
func TestTheModelIsNotCitedTwice(t *testing.T) {
	out := outcome{
		causes: []types.Cause{{Feature: "amount", Observed: 900, Baseline: 100, Share: 0.8}},
		hits:   []hit{{Rule: modelRuleID, Name: "Unusual for this customer", Weight: 0.5}},
	}
	rs := reasonsOf(out)
	if len(rs) != 1 || rs[0].Code != "amount.above" {
		t.Fatalf("reasons = %+v, want the feature attribution alone", rs)
	}
}

// ── calibration ─────────────────────────────────────────────────────────────

// TestCalibrationRefusesThenAnswers is THE calibration test: with no evidence
// the plane refuses to say what a score means, and with evidence a decision
// carries a probability.
func TestCalibrationRefusesThenAnswers(t *testing.T) {
	app, s := wireApp(t)
	tn := Tenant("hanzo/acme")

	// Nothing judged: a fit is refused rather than produced, and the refusal
	// names what is missing.
	code, body := req(t, app, http.MethodPost, "/v1/ml/calibrate", "acme", "u_acme", `{"horizon":0}`)
	if code != http.StatusCreated {
		t.Fatalf("calibrate = %d %s", code, body)
	}
	var view mlCalibrationView
	_ = json.Unmarshal(body, &view)
	if view.Fitted {
		t.Fatal("a calibration was fitted with nothing judged — every probability it produced would be invented")
	}
	if view.Refusal == "" {
		t.Fatal("the refusal is silent, so an absent calibration looks like a calibrated plane")
	}

	// A decision now carries no probability, and says why.
	d := decideOnce(t, app)
	if d.Probability != nil {
		t.Errorf("a probability was reported with no calibration: %v", *d.Probability)
	}
	if d.Refusal == "" {
		t.Error("a decision with no probability did not say why")
	}

	// With judged history the fit lands and reads back.
	seedJudged(t, s, tn, 120)
	code, body = req(t, app, http.MethodPost, "/v1/ml/calibrate", "acme", "u_acme", `{"horizon":0}`)
	if code != http.StatusCreated {
		t.Fatalf("calibrate = %d %s", code, body)
	}
	_ = json.Unmarshal(body, &view)
	if !view.Fitted {
		t.Fatalf("no fit over 120 judged decisions: %s", view.Refusal)
	}
	if view.Version != 1 || view.Digest == "" || view.Shape == "" {
		t.Fatalf("the fit is not identifiable: %+v", view)
	}
	if !view.Current {
		t.Fatal("a fit taken under the shape in force reports as stale")
	}
	if view.Productive < calibrate.MinClass || view.Unproductive < calibrate.MinClass {
		t.Fatalf("the fitting sample is degenerate: %+v", view)
	}
	// An empty reliability bin reports absence, never a point at zero.
	for _, b := range view.Reliability {
		if b.Rows == 0 && (b.Predicted != nil || b.Observed != nil) {
			t.Errorf("empty bin [%g,%g) reports numbers — a point drawn there claims perfect confidence in innocence", b.From, b.To)
		}
		if b.Rows > 0 && (b.Predicted == nil || b.Observed == nil) {
			t.Errorf("bin [%g,%g) has %d rows and no numbers", b.From, b.To, b.Rows)
		}
	}

	// And now a decision carries one, in [0,1].
	d = decideOnce(t, app)
	if d.Probability == nil {
		t.Fatalf("no probability after a fit: refusal=%q", d.Refusal)
	}
	if *d.Probability < 0 || *d.Probability > 1 {
		t.Fatalf("probability %g is not a probability", *d.Probability)
	}
	if d.Calibration == "" {
		t.Error("the decision does not name the map that produced its probability")
	}
}

// TestCalibrationRefusesAfterTheScoringShapeMoves is THE training-serving skew
// test.
//
// The features and rules used at score time must be the ones the map was fitted
// under. Write a rule and the score distribution moves; the map fitted before it
// then describes coordinates that no longer mean what they meant. Silently
// applying it is invisible — every number still looks like a probability — so
// the map must REFUSE, and every decision after must report its probability as
// absent rather than wrong.
func TestCalibrationRefusesAfterTheScoringShapeMoves(t *testing.T) {
	app, s := wireApp(t)
	seedJudged(t, s, Tenant("hanzo/acme"), 120)

	code, body := req(t, app, http.MethodPost, "/v1/ml/calibrate", "acme", "u_acme", `{"horizon":0}`)
	if code != http.StatusCreated {
		t.Fatalf("calibrate = %d %s", code, body)
	}
	var before mlCalibrationView
	_ = json.Unmarshal(body, &before)
	if !before.Fitted || !before.Current {
		t.Fatalf("no usable fit to invalidate: %+v", before)
	}
	if d := decideOnce(t, app); d.Probability == nil {
		t.Fatal("no probability before the shape moved; the test would prove nothing")
	}

	// Write a rule. This is a governed change to what the score MEANS.
	code, body = req(t, app, http.MethodPost, "/v1/risk/rules", "acme", "u_acme",
		`{"rule":{"id":"a-new-detection","name":"A new detection","stage":"payment","action":"review",
		  "weight":0.5,"severity":"high","enabled":true,
		  "all":[{"field":"amount.nano","op":"gte","number":1}]}}`)
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("create rule = %d %s", code, body)
	}

	code, body = req(t, app, http.MethodGet, "/v1/ml/calibration", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("calibration = %d %s", code, body)
	}
	var after mlCalibrationView
	_ = json.Unmarshal(body, &after)
	if !after.Fitted {
		t.Fatal("the fit vanished; it should still be on file and merely refuse")
	}
	if after.Current {
		t.Fatal("the scoring shape moved and the map still claims to apply — this is training-serving skew, undetected")
	}
	if after.Refusal == "" {
		t.Error("a stale map did not say why it is stale")
	}

	d := decideOnce(t, app)
	if d.Probability != nil {
		t.Fatalf("a probability was reported under a moved shape: %g", *d.Probability)
	}
	if d.Refusal == "" {
		t.Error("a decision with a suppressed probability did not say why")
	}
	// The decision still happened: losing the calibration costs the policy's
	// escalation and nothing else, so this degrades to the rule-driven plane
	// rather than failing open OR failing shut.
	if d.Action == "" {
		t.Error("a decision produced no action at all")
	}
}

// TestTheProbabilityIsTheRecordedScoreMapped is the other half of the
// training-serving skew control, and it is about the VALUE rather than the
// shape.
//
// A calibration is fitted on the scores in the decision log and applied to the
// score a live decision produced. Those have to be one number. If the serving
// path mapped a score it computed differently from the one it wrote down — a
// rounding, a re-score, a blend applied on one side only — every probability
// would describe a decision that was never recorded, and nothing would say so:
// the fit would look healthy and the shape gate would stay green, because the
// shape has not moved.
//
// So: fit, decide, read the decision back out of the record plane, and check
// that mapping the RECORDED score through the map on file reproduces the
// probability the wire returned, exactly.
func TestTheProbabilityIsTheRecordedScoreMapped(t *testing.T) {
	app, s := wireApp(t)
	tn := Tenant("hanzo/acme")
	seedJudged(t, s, tn, 160)
	if code, body := req(t, app, http.MethodPost, "/v1/ml/calibrate", "acme", "u_acme", `{"horizon":0}`); code != http.StatusCreated {
		t.Fatalf("calibrate = %d %s", code, body)
	}

	live := decideOnce(t, app)
	if live.Probability == nil {
		t.Fatalf("no probability to check: refusal=%q", live.Refusal)
	}

	db, err := s.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var recordedScore float64
	if err := db.QueryRow(`SELECT score FROM decision WHERE id = ?`, live.ID).Scan(&recordedScore); err != nil {
		t.Fatalf("read back the recorded score: %v", err)
	}
	if recordedScore != live.Score {
		t.Fatalf("the wire returned score %g and the record holds %g — the plane is answering about a decision it did not write",
			live.Score, recordedScore)
	}
	cal, _, _, ok, err := currentCalibration(db)
	if err != nil || !ok {
		t.Fatalf("read the calibration: %v (found=%t)", err, ok)
	}
	shape, err := scoringShape(db, s.State.model.Digest())
	if err != nil {
		t.Fatalf("shape: %v", err)
	}
	want, err := cal.P(recordedScore, shape)
	if err != nil {
		t.Fatalf("the map refuses the shape the decision was taken under: %v", err)
	}
	if want != *live.Probability {
		t.Fatalf("the recorded score %g maps to %g, and the decision reported %g — the served probability is not the recorded one",
			recordedScore, want, *live.Probability)
	}

	// And the DISPUTE PACKET carries the same answer, because it is READ and not
	// recomputed. That surface is the one an investigator or a regulator opens
	// months later, when nobody still has the body of the original call.
	code, body := req(t, app, http.MethodGet, "/v1/risk/decisions/"+live.ID, "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("read decision = %d %s", code, body)
	}
	var packet riskDecisionView
	if err := json.Unmarshal(body, &packet); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if packet.Probability == nil || *packet.Probability != *live.Probability {
		t.Fatalf("the dispute packet reports %v and the wire reported %g", packet.Probability, *live.Probability)
	}
	if packet.Calibration != live.Calibration {
		t.Errorf("the packet names map %q and the wire named %q", packet.Calibration, live.Calibration)
	}
	if len(packet.Reasons) != len(live.Reasons) {
		t.Fatalf("the packet cites %d reasons and the decision cited %d", len(packet.Reasons), len(live.Reasons))
	}
	for i := range live.Reasons {
		if packet.Reasons[i] != live.Reasons[i] {
			t.Errorf("reason %d differs between the decision and its packet: %+v vs %+v",
				i, live.Reasons[i], packet.Reasons[i])
		}
	}
}

// TestTheRecordsSurviveARestart is the durability proof, and cloud is the
// deployment that makes it load-bearing: `strategy: Recreate` at one replica, so
// every rollout is a hard stop that drops the in-memory forests, the calibration
// the decide path held and the counters behind them.
//
// A verdict, a policy version and a replay report are each something a regulator
// or a declined customer can ask about, so none of them may be state that a
// deploy clears. They land in the tenant's own cek-encrypted file beside the
// decision they belong to, and this drives a SECOND process over the SAME data
// directory to prove it: the first one is closed, everything in memory is gone,
// and the questions are asked again.
func TestTheRecordsSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	first, s1 := wireAt(t, dir)
	seedJudged(t, s1, Tenant("hanzo/acme"), 120)

	code, body := req(t, first, http.MethodPut, "/v1/risk/policy", "acme", "u_acme",
		`{"stage":"payment","floor":"allow","reason":"the dispute rate on new cards doubled",
		  "bands":[{"at":0.35,"action":"challenge"},{"at":0.85,"action":"block"}],
		  "cost":{"miss":40000000000,"alarm":2000000000}}`)
	if code != http.StatusOK {
		t.Fatalf("set policy = %d %s", code, body)
	}
	var setBands riskPolicyBands
	_ = json.Unmarshal(body, &setBands)

	if code, body = req(t, first, http.MethodPost, "/v1/ml/calibrate", "acme", "u_acme", `{"horizon":0}`); code != http.StatusCreated {
		t.Fatalf("calibrate = %d %s", code, body)
	}
	var fit mlCalibrationView
	_ = json.Unmarshal(body, &fit)
	if !fit.Fitted {
		t.Fatalf("nothing to survive: %s", fit.Refusal)
	}

	code, body = req(t, first, http.MethodPost, "/v1/ml/replay", "acme", "u_acme",
		`{"stage":"payment","horizon":0,"bands":[{"at":0.2,"action":"block"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("replay = %d %s", code, body)
	}
	var rep mlReplayReport
	_ = json.Unmarshal(body, &rep)

	decision := decideOnce(t, first)

	// The process ends. Everything held in memory ends with it.
	s1.State.shelf.close()

	second, _ := wireAt(t, dir)

	code, body = req(t, second, http.MethodGet, "/v1/risk/policy?stage=payment", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("policy after restart = %d %s", code, body)
	}
	var book riskPolicyBook
	_ = json.Unmarshal(body, &book)
	if len(book.Items) != 1 || book.Items[0].Digest != setBands.Digest || book.Items[0].Version != 1 {
		t.Fatalf("the bands in force did not survive: %+v", book.Items)
	}
	if book.Items[0].Reason != "the dispute rate on new cards doubled" || book.Items[0].By == "" {
		t.Errorf("the record lost who changed the thresholds and why: %+v", book.Items[0])
	}

	code, body = req(t, second, http.MethodGet, "/v1/ml/calibration", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("calibration after restart = %d %s", code, body)
	}
	var back mlCalibrationView
	_ = json.Unmarshal(body, &back)
	if !back.Fitted || back.Digest != fit.Digest || back.Version != fit.Version {
		t.Fatalf("the calibration did not survive: %+v", back)
	}
	if !back.Current {
		t.Error("the restored fit reports as stale — the shape is a function of the record and must be reproduced exactly")
	}

	code, body = req(t, second, http.MethodGet, "/v1/ml/replays/"+rep.ID, "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("replay after restart = %d %s", code, body)
	}
	var storedRep mlReplayReport
	_ = json.Unmarshal(body, &storedRep)
	if storedRep.Digest != rep.Digest || storedRep.Changed != rep.Changed {
		t.Fatalf("the replay evidence did not survive: %+v", storedRep)
	}

	// And the decision still says why it went the way it did.
	code, body = req(t, second, http.MethodGet, "/v1/risk/decisions/"+decision.ID, "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("decision after restart = %d %s", code, body)
	}
	var packet riskDecisionView
	_ = json.Unmarshal(body, &packet)
	if len(packet.Reasons) != len(decision.Reasons) {
		t.Fatalf("the reasons did not survive: %+v", packet.Reasons)
	}
	if (packet.Probability == nil) != (decision.Probability == nil) {
		t.Fatalf("the probability did not survive: %v vs %v", packet.Probability, decision.Probability)
	}
	if packet.Probability != nil && *packet.Probability != *decision.Probability {
		t.Fatalf("the probability came back as %g, was %g", *packet.Probability, *decision.Probability)
	}
	if packet.Calibration != decision.Calibration {
		t.Errorf("the decision no longer names the map that graded it")
	}
}

// wireAt mounts a second surface over an EXISTING data directory, which is what
// a restart is. wireApp cannot be reused: it mints a fresh t.TempDir, and a
// durability test over a directory nothing was written to would pass by
// answering nothing.
func wireAt(t *testing.T, dir string) (*zip.App, *stateService) {
	t.Helper()
	s, err := build(cloud.Deps{Logger: luxlog.New("risktest"), DataDir: dir, Brand: "hanzo"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	app := zip.New(zip.Config{Logger: luxlog.New("risktest"), DisableStartupMessage: true})
	mount(s, app)
	t.Cleanup(s.State.shelf.close)
	return app, s
}

// ── the governed thresholds ─────────────────────────────────────────────────

// TestPolicyIsVersionedAndAudited: a change of thresholds is a recorded decision
// with an author, a reason and a version, and nothing is edited in place.
func TestPolicyIsVersionedAndAudited(t *testing.T) {
	app, _ := wireApp(t)

	put := func(reason string, at float64) riskPolicyBands {
		t.Helper()
		body := fmt.Sprintf(`{"stage":"payment","floor":"allow","reason":%q,
		  "bands":[{"at":%g,"action":"challenge"},{"at":0.9,"action":"block"}],
		  "cost":{"miss":40000000000,"alarm":2000000000}}`, reason, at)
		code, out := req(t, app, http.MethodPut, "/v1/risk/policy", "acme", "u_acme", body)
		if code != http.StatusOK {
			t.Fatalf("set policy = %d %s", code, out)
		}
		var p riskPolicyBands
		if err := json.Unmarshal(out, &p); err != nil {
			t.Fatalf("unmarshal %s: %v", out, err)
		}
		return p
	}

	first := put("the dispute rate on new cards doubled", 0.30)
	second := put("the challenge step was costing more than it saved", 0.45)
	if first.Version != 1 || second.Version != 2 {
		t.Fatalf("versions are %d then %d, want 1 then 2", first.Version, second.Version)
	}
	if first.Digest == second.Digest {
		t.Error("moving a threshold left the identity unchanged")
	}
	if second.By == "" || second.At == "" {
		t.Error("a threshold changed and the record does not say who or when")
	}

	code, body := req(t, app, http.MethodGet, "/v1/risk/policy/versions?stage=payment&limit=10", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("versions = %d %s", code, body)
	}
	var history riskPolicyBook
	_ = json.Unmarshal(body, &history)
	if len(history.Items) != 2 {
		t.Fatalf("%d versions in the audit trail, want 2 — a version was edited in place", len(history.Items))
	}
	if history.Items[0].Version != 2 || history.Items[1].Version != 1 {
		t.Errorf("the trail is not newest-first: %d then %d", history.Items[0].Version, history.Items[1].Version)
	}
	if history.Items[1].Reason != "the dispute rate on new cards doubled" {
		t.Errorf("the earlier version's stated reason was lost: %q", history.Items[1].Reason)
	}
	if history.Items[1].Bands[0].At != 0.30 {
		t.Errorf("the earlier version's threshold moved to %g — it must be immutable", history.Items[1].Bands[0].At)
	}
}

// Each refusal below is a real way a ladder silently stops being a risk policy.
func TestPolicyRefusesALadderThatCannotBePutInForce(t *testing.T) {
	app, _ := wireApp(t)
	for _, tc := range []struct{ name, body string }{
		{"no reason", `{"stage":"payment","floor":"allow","bands":[{"at":0.9,"action":"block"}]}`},
		{"not a stage", `{"stage":"vibes","floor":"allow","reason":"x","bands":[{"at":0.9,"action":"block"}]}`},
		{"not an action", `{"stage":"payment","floor":"allow","reason":"x","bands":[{"at":0.9,"action":"delete"}]}`},
		{"no floor", `{"stage":"payment","reason":"x","bands":[{"at":0.9,"action":"block"}]}`},
		{"no bands", `{"stage":"payment","floor":"allow","reason":"x","bands":[]}`},
		{"above one", `{"stage":"payment","floor":"allow","reason":"x","bands":[{"at":1.5,"action":"block"}]}`},
		{"descending", `{"stage":"payment","floor":"allow","reason":"x","bands":[{"at":0.8,"action":"review"},{"at":0.2,"action":"block"}]}`},
		{"duplicate", `{"stage":"payment","floor":"allow","reason":"x","bands":[{"at":0.5,"action":"review"},{"at":0.5,"action":"block"}]}`},
		{"dead band", `{"stage":"payment","floor":"allow","reason":"x","bands":[{"at":0.5,"action":"allow"}]}`},
		{"negative price", `{"stage":"payment","floor":"allow","reason":"x","bands":[{"at":0.9,"action":"block"}],"cost":{"miss":-1}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := req(t, app, http.MethodPut, "/v1/risk/policy", "acme", "u_acme", tc.body)
			if code != http.StatusBadRequest {
				t.Fatalf("accepted = %d %s", code, body)
			}
		})
	}
}

// The evidence and the policy are two authorities and the decision is one
// answer. The stronger wins: an explicit deny-list rule is not talked down by a
// low probability, and a high probability is not talked down by quiet evidence.
func TestEscalationTakesTheStrongerAuthority(t *testing.T) {
	for _, c := range []struct{ evidence, policy, want string }{
		{ActionAllow, ActionBlock, ActionBlock},
		{ActionBlock, ActionAllow, ActionBlock},
		{ActionBlock, "", ActionBlock},
		{ActionChallenge, ActionReview, ActionReview},
		{ActionReview, ActionChallenge, ActionReview},
		{ActionAllow, "", ActionAllow},
	} {
		if got := escalate(c.evidence, c.policy); got != c.want {
			t.Errorf("escalate(%q, %q) = %q, want %q", c.evidence, c.policy, got, c.want)
		}
	}
}

// A horizon is a caller-supplied integer that reaches a date computation and a
// scan. Backwards is nonsense; beyond the plane's own window it walks the clock
// out of the range a stored timestamp expresses, and the comparison then still
// runs and still returns rows — a report rather than an error, which is the
// worse failure.
func TestTheHorizonIsBounded(t *testing.T) {
	app, _ := wireApp(t)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/ml/calibrate", `{"horizon":-1}`},
		{http.MethodPost, "/v1/ml/calibrate", `{"horizon":100000000}`},
		{http.MethodGet, "/v1/ml/calibration?horizon=100000000", ""},
		{http.MethodPost, "/v1/ml/evaluate", `{"horizon":-1}`},
		{http.MethodPost, "/v1/ml/evaluate", `{"horizon":2147483647}`},
		{http.MethodGet, "/v1/ml/learning?horizon=2147483647", ""},
		{http.MethodPost, "/v1/ml/replay", `{"stage":"payment","horizon":2147483647}`},
	} {
		code, body := req(t, app, tc.method, tc.path, "acme", "u_acme", tc.body)
		if code != http.StatusBadRequest {
			t.Errorf("%s %s %s = %d %s, want 400", tc.method, tc.path, tc.body, code, body)
		}
	}
	// And the bound itself is usable — a payment lane's 120 days must go through.
	if code, body := req(t, app, http.MethodPost, "/v1/ml/evaluate", "acme", "u_acme", `{"horizon":120}`); code != http.StatusOK {
		t.Errorf("a 120-day horizon was refused: %d %s", code, body)
	}
}

// ── the replay ──────────────────────────────────────────────────────────────

// TestReplayIsDeterministic is THE backtest determinism test on the wire. A
// replay justifies a threshold change, so re-running it has to reproduce it —
// including across processes, where Go randomises map iteration.
func TestReplayIsDeterministic(t *testing.T) {
	app, s := wireApp(t)
	seedJudged(t, s, Tenant("hanzo/acme"), 200)
	code, body := req(t, app, http.MethodPost, "/v1/ml/calibrate", "acme", "u_acme", `{"horizon":0}`)
	if code != http.StatusCreated {
		t.Fatalf("calibrate = %d %s", code, body)
	}

	candidate := `{"stage":"payment","horizon":0,"floor":"allow",
	  "bands":[{"at":0.25,"action":"challenge"},{"at":0.7,"action":"block"}],
	  "cost":{"miss":40000000000,"alarm":2000000000}}`

	run := func() mlReplayReport {
		t.Helper()
		code, body := req(t, app, http.MethodPost, "/v1/ml/replay", "acme", "u_acme", candidate)
		if code != http.StatusCreated {
			t.Fatalf("replay = %d %s", code, body)
		}
		var r mlReplayReport
		if err := json.Unmarshal(body, &r); err != nil {
			t.Fatalf("unmarshal %s: %v", body, err)
		}
		return r
	}

	first := run()
	if first.Refusal != "" {
		t.Fatalf("the replay refused: %s", first.Refusal)
	}
	if first.Digest == "" {
		t.Fatal("a report with no identity cannot be evidence")
	}
	if first.Rows == 0 || first.Changed == 0 {
		t.Fatalf("the candidate changed nothing over %d rows; the test would prove nothing", first.Rows)
	}
	for i := range 5 {
		again := run()
		if again.Digest != first.Digest {
			t.Fatalf("run %d produced a different report: %s vs %s", i, again.Digest[:12], first.Digest[:12])
		}
		if again.Changed != first.Changed || len(again.Would) != len(first.Would) {
			t.Fatalf("run %d moved the counts", i)
		}
	}
	// The stored report is the SAME report, or the record is not the evidence.
	code, body = req(t, app, http.MethodGet, "/v1/ml/replays/"+first.ID, "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("read back = %d %s", code, body)
	}
	var stored mlReplayReport
	_ = json.Unmarshal(body, &stored)
	if stored.Digest != first.Digest || stored.Changed != first.Changed {
		t.Fatalf("the stored report differs from the one returned: %+v", stored)
	}

	// The digest must discriminate, or determinism is satisfied by a constant.
	code, body = req(t, app, http.MethodPost, "/v1/ml/replay", "acme", "u_acme",
		`{"stage":"payment","horizon":0,"floor":"allow","bands":[{"at":0.05,"action":"block"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("second candidate = %d %s", code, body)
	}
	var other mlReplayReport
	_ = json.Unmarshal(body, &other)
	if other.Digest == first.Digest {
		t.Fatal("two different candidates produced one report")
	}
	if other.Tightened <= first.Tightened {
		t.Errorf("a far lower band did not act harder: %d then %d", first.Tightened, other.Tightened)
	}
}

// ── the measurement ─────────────────────────────────────────────────────────

// The metrics have to suit imbalanced data and have to be absent rather than
// zero when nothing supports them — on a plane where most decisions are unjudged
// that is the default state, and a rate reported as 0.0 reads as a perfect
// model.
func TestMeasurementIsAbsentRatherThanZero(t *testing.T) {
	app, s := wireApp(t)

	// Nothing judged.
	code, body := req(t, app, http.MethodPost, "/v1/ml/evaluate", "acme", "u_acme", `{"horizon":0}`)
	if code != http.StatusOK {
		t.Fatalf("evaluate = %d %s", code, body)
	}
	var m mlMeasurement
	_ = json.Unmarshal(body, &m)
	if m.Metrics.ROC != nil || m.Metrics.PR != nil || m.Metrics.Prevalence != nil {
		t.Errorf("rates were reported over nothing: %+v", m.Metrics)
	}
	if m.Metrics.Refusal == "" {
		t.Error("an unmeasurable report did not say so")
	}

	// With judged history and stated prices, everything appears — including the
	// cost-weighted number, which is the one with a unit anybody outside the team
	// cares about.
	seedJudged(t, s, Tenant("hanzo/acme"), 200)
	code, body = req(t, app, http.MethodPut, "/v1/risk/policy", "acme", "u_acme",
		`{"stage":"payment","floor":"allow","reason":"initial","bands":[{"at":0.5,"action":"review"}],
		  "cost":{"miss":40000000000,"alarm":2000000000}}`)
	if code != http.StatusOK {
		t.Fatalf("set policy = %d %s", code, body)
	}
	code, body = req(t, app, http.MethodPost, "/v1/ml/calibrate", "acme", "u_acme", `{"horizon":0}`)
	if code != http.StatusCreated {
		t.Fatalf("calibrate = %d %s", code, body)
	}
	code, body = req(t, app, http.MethodPost, "/v1/ml/evaluate", "acme", "u_acme",
		`{"stage":"payment","horizon":0}`)
	if code != http.StatusOK {
		t.Fatalf("evaluate = %d %s", code, body)
	}
	_ = json.Unmarshal(body, &m)
	for name, v := range map[string]*float64{
		"prevalence": m.Metrics.Prevalence, "roc": m.Metrics.ROC, "pr": m.Metrics.PR,
		"brier": m.Metrics.Brier,
	} {
		if v == nil {
			t.Errorf("%s is absent over %d judged decisions", name, m.Metrics.Judged)
		}
	}
	if m.Metrics.CostNano == nil || m.Metrics.BestNano == nil || m.Metrics.BestThreshold == nil {
		t.Errorf("prices were stated and no cost was computed: %+v", m.Metrics)
	}
	if m.Metrics.BestNano != nil && m.Metrics.CostNano != nil && *m.Metrics.BestNano > *m.Metrics.CostNano {
		t.Errorf("the reported optimum %d is worse than the operating point %d", *m.Metrics.BestNano, *m.Metrics.CostNano)
	}
	if len(m.Curve) == 0 {
		t.Error("no curve to render")
	}
	// A metric with no stated price must not invent one.
	code, body = req(t, app, http.MethodPost, "/v1/ml/evaluate", "acme", "u_acme", `{"horizon":0}`)
	if code != http.StatusOK {
		t.Fatalf("evaluate = %d %s", code, body)
	}
	var free mlMeasurement
	_ = json.Unmarshal(body, &free)
	if free.Metrics.CostNano != nil {
		t.Errorf("a cost was computed with no price stated: %d", *free.Metrics.CostNano)
	}
}

// The learning curve is what a console renders. Two arms, a fixed later window
// so two steps are comparable, and a training prefix that never reaches into it.
func TestLearningCurveHasTwoArmsOverAFixedWindow(t *testing.T) {
	app, s := wireApp(t)
	seedJudged(t, s, Tenant("hanzo/acme"), 400)

	code, body := req(t, app, http.MethodGet, "/v1/ml/learning?horizon=0&steps=5", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("learning = %d %s", code, body)
	}
	var c mlLearningCurve
	if err := json.Unmarshal(body, &c); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if len(c.Steps) != 5 {
		t.Fatalf("%d steps, want 5: %s", len(c.Steps), c.Refusal)
	}
	held := c.Steps[0].Held
	if held == 0 {
		t.Fatal("nothing was held out, so every point is measured in-sample")
	}
	for i, s := range c.Steps {
		if s.Held != held {
			t.Errorf("step %d moved the held window: %d vs %d — two steps are then not comparable", i, s.Held, held)
		}
		if i > 0 && s.Rows <= c.Steps[i-1].Rows {
			t.Errorf("step %d did not grow the training prefix: %d after %d", i, s.Rows, c.Steps[i-1].Rows)
		}
		if s.Ahead.Rows != held {
			t.Errorf("step %d measured %d ahead rows against a window of %d", i, s.Ahead.Rows, held)
		}
	}
	// A monotone calibration cannot reorder, so separation is the DETECTOR
	// improving and Brier is the CALIBRATION improving. Both must be present at
	// the far end or the curve says nothing.
	last := c.Steps[len(c.Steps)-1]
	if last.Ahead.ROC == nil || last.Ahead.Brier == nil {
		t.Errorf("the final step measures neither separation nor calibration out of sample: %+v", last.Ahead)
	}
}

// ── an idempotent repeat carries the SAME reasons ───────────────────────────

// An idempotency key promises one decision. It must promise one EXPLANATION too:
// recomputing the grading on a retry would grade the same decision against
// whatever calibration and bands are in force now.
func TestIdempotentRepeatReturnsTheSameReasons(t *testing.T) {
	app, _ := wireApp(t)
	code, body := req(t, app, http.MethodPost, "/v1/risk/lists/ip-deny/entries", "acme", "u_acme",
		`{"values":["203.0.113.8"]}`)
	if code != http.StatusOK {
		t.Fatalf("add list entry = %d %s", code, body)
	}
	call := `{"stage":"payment","subject":{"kind":"transaction","id":"tx-idem"},"idem":"key-1",
	  "amount":{"nano":9000000000,"currency":"USD","direction":"in"},
	  "signals":{"ip":"203.0.113.8"}}`

	var first, second riskDecision
	code, body = req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme", call)
	if code != http.StatusOK {
		t.Fatalf("first decide = %d %s", code, body)
	}
	_ = json.Unmarshal(body, &first)
	code, body = req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme", call)
	if code != http.StatusOK {
		t.Fatalf("second decide = %d %s", code, body)
	}
	_ = json.Unmarshal(body, &second)

	if first.ID != second.ID {
		t.Fatalf("the idempotency key produced two decisions: %s and %s", first.ID, second.ID)
	}
	if len(first.Reasons) == 0 {
		t.Fatal("the first decision cited no reason; the test would prove nothing")
	}
	if len(first.Reasons) != len(second.Reasons) {
		t.Fatalf("the repeat cited %d reasons, the original %d", len(second.Reasons), len(first.Reasons))
	}
	for i := range first.Reasons {
		if first.Reasons[i] != second.Reasons[i] {
			t.Errorf("reason %d changed on a repeat: %+v then %+v", i, first.Reasons[i], second.Reasons[i])
		}
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

// decideOnce drives one payment decision through the live wire and returns it.
//
// Each call names a fresh subject and carries no idempotency key, so two calls
// in one test are two decisions rather than one replayed — which matters because
// the tests that use it compare a decision taken BEFORE a change to one taken
// after, and an idempotent repeat would return the first one's stored grading
// and prove nothing.
func decideOnce(t *testing.T, app *zip.App) riskDecision {
	t.Helper()
	n := decideSeq.Add(1)
	code, body := req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
		fmt.Sprintf(`{"stage":"payment","subject":{"kind":"transaction","id":"tx-once-%d"},
		  "amount":{"nano":%d,"currency":"USD","direction":"in"},
		  "signals":{"ip":"198.51.100.%d","device":"dev-once"}}`, n, 1_000_000_000+n, n%250))
	if code != http.StatusOK {
		t.Fatalf("decide = %d %s", code, body)
	}
	var d riskDecision
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	return d
}

// decideSeq keeps two decisions in one process from colliding on a subject.
var decideSeq atomic.Int64

// seedJudged writes n decisions straight into the tenant's record plane and
// judges them, with the productive ones scoring higher than the rest.
//
// Written through the store rather than through /v1/risk/decide on purpose: the
// scores have to SEPARATE for a calibration to be fittable at all, and a fresh
// model refuses to score for its whole warm period, so decisions driven through
// the live path would all carry the same score and prove nothing about a map
// fitted on them.
func seedJudged(t *testing.T, s *stateService, tn Tenant, n int) {
	t.Helper()
	db, err := s.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open %s: %v", tn, err)
	}
	base := time.Now().UTC().Add(-time.Duration(n+1) * time.Hour)
	for i := range n {
		// A rising score with the productive ones concentrated at the top, plus a
		// deliberate overlap so the problem is not separable and the metrics have
		// something to measure.
		score := float64(i) / float64(n)
		productive := i%7 == 0 && score > 0.3
		verdict := "legitimate"
		if productive {
			verdict = "fraud"
		}
		o := observation{
			id: fmt.Sprintf("dec_seed_%04d", i), at: base.Add(time.Duration(i) * time.Minute),
			stage: StagePayment, kind: "transaction", subject: fmt.Sprintf("tx-%d", i),
			agency: AgencyUnknown, amount: 1_000_000_000, currency: "USD", direction: "in",
			signals: map[string]string{},
		}
		action := ActionAllow
		if score > 0.9 {
			action = ActionReview
		}
		out := outcome{id: o.id, action: action, score: score, agency: o.agency}
		if err := putDecision(db, o, out, "seed-digest", ""); err != nil {
			t.Fatalf("seed decision %d: %v", i, err)
		}
		if err := label(db, o.id, verdict, "u_seed"); err != nil {
			t.Fatalf("seed label %d: %v", i, err)
		}
	}
	assertSeeded(t, db, n)
}

func assertSeeded(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	var rows, judged int
	if err := db.QueryRow(`SELECT COUNT(*), SUM(CASE WHEN label != '' THEN 1 ELSE 0 END) FROM decision`).
		Scan(&rows, &judged); err != nil {
		t.Fatalf("count seeded: %v", err)
	}
	if rows < n || judged < n {
		t.Fatalf("seeded %d rows / %d judged, want %d of each", rows, judged, n)
	}
}
