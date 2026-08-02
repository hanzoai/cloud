package risk

// decide_test.go pins the decision path's ORDER and its two hard limits: the
// model may never act alone, and shadow may never act at all.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"
)

// TestRecordHappensBeforeScoring pins step 1. Everything after reads the rings,
// so the numbers quoted in a decision must be the ones an investigator sees when
// they look at the subject — including THIS observation.
func TestRecordHappensBeforeScoring(t *testing.T) {
	vel := aggregates()
	model, err := forest(anomaly.Config{}, vel)
	if err != nil {
		t.Fatalf("anomaly.New: %v", err)
	}
	tn := Tenant("hanzo/acme")

	// A rule that can only hold if THIS observation is already in the ring.
	rules := []rule{{
		ID: "r1", Name: "counted", Stage: StagePayment, Action: ActionReview,
		Weight: 0.5, Enabled: true,
		All: []term{{Field: "velocity.ip.1h.count", Op: OpGte, Number: 1}},
	}}
	o := observation{
		id: "d1", at: time.Now(), stage: StagePayment, kind: "transaction", subject: "tx1",
		amount: 1_000_000_000, currency: "USD", direction: "in",
		signals: map[string]string{"ip": "203.0.113.1"},
	}
	out := decide(context.Background(), bench{t: tn, vel: vel, model: model, rules: rules}, o)
	if len(out.hits) != 1 {
		t.Fatalf("hits = %v; the rule did not see this observation in the ring, so the record ran after the read", out.hits)
	}
}

// TestModelEvidenceCannotExceedTheCeiling pins the structural backstop. A
// statistical judgement may summon a person; it may not decline a payment,
// because an unexplainable refusal is not a decision anybody can defend to a
// customer or a chargeback network.
func TestModelEvidenceCannotExceedTheCeiling(t *testing.T) {
	if actionRank(modelCeiling) != actionRank(ActionReview) {
		t.Fatalf("the model ceiling is %q, not review — statistical evidence can now act alone", modelCeiling)
	}
	// And the combine step's ordering must never promote a capped hit.
	hits := []hit{{Rule: "model", Action: modelCeiling, Weight: 1.0}}
	_, action := combine(hits)
	if actionRank(action) > actionRank(ActionReview) {
		t.Fatalf("a weight-1.0 model hit produced %q — the cap is not holding", action)
	}
}

// TestShadowActsOnNothing pins the default. In shadow every rule runs, the model
// scores and learns, every decision is recorded, and the action is always allow.
func TestShadowActsOnNothing(t *testing.T) {
	vel := aggregates()
	model, _ := forest(anomaly.Config{}, vel)
	rules := []rule{{
		ID: "block-all", Name: "would block", Stage: StagePayment, Action: ActionBlock,
		Weight: 1, Enabled: true,
		All: []term{{Field: "subject.kind", Op: OpEq, Value: "transaction"}},
	}}
	o := observation{
		id: "d1", at: time.Now(), stage: StagePayment, kind: "transaction", subject: "tx1",
		signals: map[string]string{},
	}
	out := decide(context.Background(), bench{t: Tenant("hanzo/acme"), vel: vel, model: model, rules: rules, shadow: true}, o)
	if out.action != ActionAllow {
		t.Fatalf("shadow produced action %q — a shadow tenant acted", out.action)
	}
	// The answer must SAY it was shadow, or silence reads as a clean result. The
	// refusal keeps the MODEL's more specific reason when it has one (a warming
	// model is a different fact from a shadow tenant, and hiding the first behind
	// the second is exactly the conflation this field exists to prevent) — so
	// `shadow` is the flag that must always hold, and `refusal` must always be
	// populated with something.
	if !out.shadow {
		t.Fatal("a shadow decision does not say it was shadow")
	}
	if out.refusal == "" {
		t.Fatal("a shadow decision names no refusal at all, so it is indistinguishable from a clean live one")
	}
	if out.refusal != RefusalWarming && out.refusal != RefusalShadow {
		t.Fatalf("refusal = %q, want the model's own reason or shadow", out.refusal)
	}
	if len(out.hits) != 1 {
		t.Fatalf("shadow computed %d hits, want 1 — shadow must compute everything and act on nothing", len(out.hits))
	}
	// Live, the same input blocks. Without this the test above would pass on a
	// rule that simply never fires.
	out = decide(context.Background(), bench{t: Tenant("hanzo/acme"), vel: vel, model: model, rules: rules}, o)
	if out.action != ActionBlock {
		t.Fatalf("live produced %q, want block — the shadow assertion proves nothing if the rule cannot fire", out.action)
	}
}

// TestADisarmedModelIsNamedEvenWhenSomethingElseIsAlsoShort.
//
// THE DEFECT: the model's refusal was graded AFTER every reason had been merged
// into the single word the wire carries. Grading only fired on the literal word
// `warming`, so a decision that was ALSO short of something else — an agency the
// registry could not confirm, a tenant in shadow — went out under that other
// reason's name and the disarmed model was never said out loud. A grader that
// stops working as soon as anything else goes wrong is a grader that stops
// working exactly when a reader most needs it.
//
// THE FIX IS ORDER: the model's own reason is graded where it is produced, before
// the merge, so the merge chooses between TRUE words.
func TestADisarmedModelIsNamedEvenWhenSomethingElseIsAlsoShort(t *testing.T) {
	vel := aggregates()
	model, err := forest(anomaly.Config{}, vel)
	if err != nil {
		t.Fatalf("forest: %v", err)
	}
	o := observation{
		id: "d1", at: time.Now(), stage: StageSignup, kind: "account", subject: "a1",
		signals: map[string]string{},
		// The agency claim could not be checked — a second, independently true
		// shortfall that outranks warming.
		refusal: RefusalUnverified,
	}
	// A grader standing in for a tenant whose learned state is gone.
	disarm := func(reason string) string {
		if reason == RefusalWarming {
			return RefusalDisarmed
		}
		return reason
	}

	out := decide(context.Background(), bench{
		t: Tenant("hanzo/acme"), vel: vel, model: model, grade: disarm,
	}, o)
	if out.refusal != RefusalDisarmed {
		t.Fatalf("refusal = %q, want %q — a control that is OFF was hidden behind another reason, which is the silent disarm in one word",
			out.refusal, RefusalDisarmed)
	}

	// The control: with no grader the same decision reports the reason the engine
	// actually gave, so the assertion above is about grading and not about rank.
	plain := decide(context.Background(), bench{
		t: Tenant("hanzo/acme"), vel: vel, model: model,
	}, o)
	if plain.refusal == RefusalDisarmed {
		t.Fatal("an ungraded decision reported disarmed, so the test above proves nothing")
	}
	if plain.refusal != RefusalUnverified {
		t.Fatalf("ungraded refusal = %q, want %q — an unchecked agency must outrank a model that is merely young", plain.refusal, RefusalUnverified)
	}
}

// TestSuppressedEvidenceIsRecordedNotDropped pins the doctrine: a muted control
// that leaves no trace is indistinguishable from one that was never running.
func TestSuppressedEvidenceIsRecordedNotDropped(t *testing.T) {
	vel := aggregates()
	model, _ := forest(anomaly.Config{}, vel)
	rules := []rule{{
		ID: "r1", Name: "noisy", Stage: StageSignup, Action: ActionBlock, Weight: 1, Enabled: true,
		All: []term{{Field: "subject.kind", Op: OpEq, Value: "account"}},
	}}
	o := observation{id: "d1", at: time.Now(), stage: StageSignup, kind: "account", subject: "a1", signals: map[string]string{}}

	out := decide(context.Background(), bench{
		t: Tenant("hanzo/acme"), vel: vel, model: model, rules: rules,
		mute: func(h hit, _ observation) bool { return h.Rule == "r1" },
	}, o)

	if len(out.hits) != 1 {
		t.Fatalf("the suppressed hit was DROPPED (%d hits) — the record no longer says the rule fired", len(out.hits))
	}
	if !out.hits[0].Suppressed {
		t.Fatal("the hit is recorded but not marked suppressed")
	}
	if out.action != ActionAllow {
		t.Fatalf("a suppressed hit still drove the action to %q", out.action)
	}
	if out.score != 0 {
		t.Fatalf("a suppressed hit contributed %v to the score", out.score)
	}
}

// TestRuleAdmissionRefusesWhatWouldReadAsWorking pins the admission gate. Each
// of these produces a rule that looks live and detects nothing, or one that
// detects everything.
func TestRuleAdmissionRefusesWhatWouldReadAsWorking(t *testing.T) {
	base := rule{Name: "r", Action: ActionBlock, Weight: 0.5, Enabled: true,
		All: []term{{Field: "signal.ip", Op: OpEq, Value: "x"}}}
	ok := base
	if err := admit(ok); err != nil {
		t.Fatalf("a well-formed rule was refused: %v", err)
	}
	for _, tc := range []struct {
		name string
		mut  func(*rule)
	}{
		{"no terms holds on everything", func(r *rule) { r.All = nil }},
		{"no name cannot be read back in an alert", func(r *rule) { r.Name = "" }},
		{"an unknown action", func(r *rule) { r.Action = "quarantine" }},
		{"a weight outside [0,1]", func(r *rule) { r.Weight = 2 }},
		{"an unknown stage", func(r *rule) { r.Stage = "checkout" }},
		{"a field the vocabulary does not carry", func(r *rule) { r.All = []term{{Field: "tx.amount", Op: OpEq}} }},
		{"an unknown operator", func(r *rule) { r.All = []term{{Field: "signal.ip", Op: "matches"}} }},
		{"an unknown velocity axis", func(r *rule) { r.All = []term{{Field: "velocity.wallet.1h.count", Op: OpGte}} }},
		{"an unknown velocity window", func(r *rule) { r.All = []term{{Field: "velocity.ip.90d.count", Op: OpGte}} }},
		{"an empty in-set holds on nothing", func(r *rule) { r.All = []term{{Field: "agency", Op: OpIn}} }},
		{"an inlist naming no list", func(r *rule) { r.All = []term{{Field: "signal.ip", Op: OpInList}} }},
	} {
		r := base
		r.All = append([]term(nil), base.All...)
		tc.mut(&r)
		if err := admit(r); err == nil {
			t.Errorf("admitted a rule with %s", tc.name)
		}
	}
}

// TestWeightOfEvidenceCompounds pins the score's shape. Two independent weak
// signals must compound and no single one may saturate — a summed score clamps
// at 1 and then loses every further signal.
func TestWeightOfEvidenceCompounds(t *testing.T) {
	one, _ := combine([]hit{{Weight: 0.4, Action: ActionAllow}})
	two, _ := combine([]hit{{Weight: 0.4, Action: ActionAllow}, {Weight: 0.4, Action: ActionAllow}})
	three, _ := combine([]hit{{Weight: 0.4}, {Weight: 0.4}, {Weight: 0.4}})
	four, _ := combine([]hit{{Weight: 0.4}, {Weight: 0.4}, {Weight: 0.4}, {Weight: 0.4}})
	if !(one < two && two < three && three < four) {
		t.Fatalf("scores %v %v %v %v do not compound — a fourth signal changed nothing", one, two, three, four)
	}
	if four >= 1 {
		t.Fatalf("four 0.4 signals saturated at %v; nothing after this could raise the score", four)
	}
}

// TestIdempotentDecideDoesNotScoreTwice pins the wire promise. A retried decide
// must return the SAME decision and must not move the counters again — a
// double-counted payment is a velocity rule firing on one transaction.
func TestIdempotentDecideDoesNotScoreTwice(t *testing.T) {
	app, _ := wireApp(t)
	body := `{"stage":"payment","subject":{"kind":"transaction","id":"tx-9"},
	          "amount":{"nano":1000000000,"currency":"USD","direction":"in"},
	          "signals":{"ip":"198.51.100.7"},"idem":"k-1"}`

	code, first := req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme", body)
	if code != http.StatusOK {
		t.Fatalf("first decide = %d %s", code, first)
	}
	code, second := req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme", body)
	if code != http.StatusOK {
		t.Fatalf("second decide = %d %s", code, second)
	}
	var a, b riskDecision
	_ = json.Unmarshal(first, &a)
	_ = json.Unmarshal(second, &b)
	if a.ID != b.ID {
		t.Fatalf("the same idempotency key produced two decisions (%s, %s)", a.ID, b.ID)
	}

	_, list := req(t, app, http.MethodGet, "/v1/risk/decisions", "acme", "u_acme", "")
	var page riskDecisionPage
	_ = json.Unmarshal(list, &page)
	if len(page.Items) != 1 {
		t.Fatalf("%d decisions recorded for one idempotency key", len(page.Items))
	}
}

// TestConcurrentRetriesUnderOneKeyProduceOneDecision pins the race the
// two-statement version had: insert the row, then claim the key, and two
// simultaneous retries both find no row, both insert, and the loser's claim
// violates the index — so a caller that retried CORRECTLY gets a 500 and the
// tenant's counters moved twice.
//
// Claiming the key inside the insert makes the loser lose at the index, before a
// second decision exists, and read the winner's answer back.
func TestConcurrentRetriesUnderOneKeyProduceOneDecision(t *testing.T) {
	app, _ := wireApp(t)
	body := `{"stage":"payment","subject":{"kind":"transaction","id":"tx-race"},
	          "amount":{"nano":2500000000,"currency":"USD","direction":"in"},
	          "signals":{"ip":"198.51.100.99"},"idem":"race-1"}`

	const n = 8
	type result struct {
		code int
		id   string
	}
	out := make(chan result, n)
	var start sync.WaitGroup
	start.Add(1)
	for i := 0; i < n; i++ {
		go func() {
			start.Wait()
			code, b := req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme", body)
			var d riskDecision
			_ = json.Unmarshal(b, &d)
			out <- result{code, d.ID}
		}()
	}
	start.Done()

	ids := map[string]int{}
	for i := 0; i < n; i++ {
		r := <-out
		if r.code != http.StatusOK {
			t.Errorf("a concurrent retry answered %d — a correct retry must never be an error", r.code)
			continue
		}
		ids[r.id]++
	}
	if len(ids) != 1 {
		t.Fatalf("%d concurrent requests under one key produced %d distinct decisions: %v", n, len(ids), ids)
	}
	// And exactly one row was written, so the counters moved once.
	_, list := req(t, app, http.MethodGet, "/v1/risk/decisions", "acme", "u_acme", "")
	var page riskDecisionPage
	_ = json.Unmarshal(list, &page)
	if len(page.Items) != 1 {
		t.Fatalf("%d decisions recorded under one idempotency key", len(page.Items))
	}
}

// TestDecideRefusesAnUnknownStageOrKind pins the two vocabularies at the door.
func TestDecideRefusesAnUnknownStageOrKind(t *testing.T) {
	app, _ := wireApp(t)
	for _, body := range []string{
		`{"stage":"checkout","subject":{"kind":"account","id":"a"}}`,
		`{"stage":"signup","subject":{"kind":"wallet","id":"a"}}`,
		`{"stage":"signup","subject":{"kind":"account","id":""}}`,
	} {
		code, got := req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme", body)
		if code != http.StatusBadRequest {
			t.Errorf("%s = %d %s, want 400", body, code, got)
		}
	}
}

// TestSimulateRefusesAnEmptyHistory pins the sandbox's whole reason for
// existing: "no alerts" is what a quiet rule and an unrun rule both look like.
func TestSimulateRefusesAnEmptyHistory(t *testing.T) {
	app, _ := wireApp(t)
	code, body := req(t, app, http.MethodPost, "/v1/risk/simulate", "acme", "u_acme",
		`{"candidate":{"name":"c","action":"review","weight":0.5,
		  "all":[{"field":"signal.ip","op":"eq","value":"1.1.1.1"}]}}`)
	if code != http.StatusOK {
		t.Fatalf("simulate = %d %s", code, body)
	}
	var rep riskSimulateReport
	_ = json.Unmarshal(body, &rep)
	if rep.Refusal == "" {
		t.Fatal("an empty history reported zero alerts instead of refusing")
	}
	if rep.Events != 0 {
		t.Fatalf("events = %d on an empty history", rep.Events)
	}
}

// TestSimulateMeasuresAgainstRealHistory drives a decision in, then replays a
// candidate over it, so the report is measured rather than asserted.
func TestSimulateMeasuresAgainstRealHistory(t *testing.T) {
	app, _ := wireApp(t)
	mustOK(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
		`{"stage":"signup","subject":{"kind":"account","id":"a-1"},"signals":{"ip":"192.0.2.10"}}`,
		http.StatusOK)

	code, body := req(t, app, http.MethodPost, "/v1/risk/simulate", "acme", "u_acme",
		`{"candidate":{"name":"c","stage":"signup","action":"review","weight":0.5,
		  "all":[{"field":"signal.ip","op":"eq","value":"192.0.2.10"}]}}`)
	if code != http.StatusOK {
		t.Fatalf("simulate = %d %s", code, body)
	}
	var rep riskSimulateReport
	_ = json.Unmarshal(body, &rep)
	if rep.Events != 1 || rep.Alerts != 1 {
		t.Fatalf("events=%d alerts=%d, want 1/1 — the replay did not read the recorded facts", rep.Events, rep.Alerts)
	}
	if len(rep.Added) != 1 {
		t.Fatalf("added=%v, want the one decision the candidate newly catches", rep.Added)
	}
}

// TestModeDefaultsToShadowAndSaysSo pins the default at the wire.
func TestModeDefaultsToShadowAndSaysSo(t *testing.T) {
	app, _ := wireApp(t)
	code, body := req(t, app, http.MethodGet, "/v1/risk/mode", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("mode = %d %s", code, body)
	}
	var m riskModeView
	_ = json.Unmarshal(body, &m)
	if m.Mode != "shadow" {
		t.Fatalf("a fresh tenant is %q, not shadow — it would act on its very first request", m.Mode)
	}

	// A decision in shadow must SAY it was shadow.
	_, body = req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
		`{"stage":"signup","subject":{"kind":"account","id":"a-1"}}`)
	var d riskDecision
	_ = json.Unmarshal(body, &d)
	if !d.Shadow {
		t.Fatal("a shadow tenant's decision does not say so")
	}
}

// TestHealthCarriesItsReport pins the reason the probe is untyped: the body IS
// the answer.
func TestHealthCarriesItsReport(t *testing.T) {
	app, _ := wireApp(t)
	code, body := req(t, app, http.MethodGet, "/v1/risk/health", "", "", "")
	if code != http.StatusOK {
		t.Fatalf("health = %d %s", code, body)
	}
	for _, k := range []string{"status", "model", "warehouse", "billing"} {
		if !strings.Contains(string(body), `"`+k+`"`) {
			t.Errorf("the probe body does not carry %q — a typed op would have dropped exactly this", k)
		}
	}
}

// TestTrainIsOnlineAndPerTenant drives training through the wire and proves the
// counters moved for the caller's tenant and nobody else's.
func TestTrainIsOnlineAndPerTenant(t *testing.T) {
	app, s := wireApp(t)
	obs := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		obs = append(obs, `{"subject":{"kind":"account","id":"a-1"},"amount":{"nano":1000000000,"currency":"USD","direction":"in"}}`)
	}
	body := `{"observations":[` + strings.Join(obs, ",") + `]}`
	code, got := req(t, app, http.MethodPost, "/v1/risk/train", "acme", "u_acme", body)
	if code != http.StatusOK {
		t.Fatalf("train = %d %s", code, got)
	}
	var out riskTrainOut
	_ = json.Unmarshal(got, &out)
	if out.Learned != 20 {
		t.Fatalf("learned %d of 20", out.Learned)
	}
	_, mine, _ := resOf(t, s, Tenant("hanzo/acme")).arms()
	if mine.State("hanzo/acme").Learned == 0 {
		t.Fatal("the caller's model learned nothing")
	}
	_, theirs, _ := resOf(t, s, Tenant("hanzo/beta")).arms()
	if theirs.State("hanzo/beta").Learned != 0 {
		t.Fatal("another tenant's model learned from this tenant's data")
	}
	// And the tenant key really is qualified, not bare.
	if mine.State("acme").Learned != 0 {
		t.Fatal("the model is indexed on the BARE org — two brands' same-named orgs would share it")
	}
}
