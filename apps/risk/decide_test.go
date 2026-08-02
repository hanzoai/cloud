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
	"github.com/luxfi/aml/pkg/velocity"
)

// TestRecordHappensBeforeScoring pins step 1. Everything after reads the rings,
// so the numbers quoted in a decision must be the ones an investigator sees when
// they look at the subject — including THIS observation.
func TestRecordHappensBeforeScoring(t *testing.T) {
	vel := velocity.New(velocity.Config{})
	model, err := anomaly.New(anomaly.Config{Shadow: true}, vel)
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
	out := decide(context.Background(), vel, model, tn, o, rules, nil, nil, false)
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
	vel := velocity.New(velocity.Config{})
	model, _ := anomaly.New(anomaly.Config{Shadow: true}, vel)
	rules := []rule{{
		ID: "block-all", Name: "would block", Stage: StagePayment, Action: ActionBlock,
		Weight: 1, Enabled: true,
		All: []term{{Field: "subject.kind", Op: OpEq, Value: "transaction"}},
	}}
	o := observation{
		id: "d1", at: time.Now(), stage: StagePayment, kind: "transaction", subject: "tx1",
		signals: map[string]string{},
	}
	out := decide(context.Background(), vel, model, Tenant("hanzo/acme"), o, rules, nil, nil, true)
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
	out = decide(context.Background(), vel, model, Tenant("hanzo/acme"), o, rules, nil, nil, false)
	if out.action != ActionBlock {
		t.Fatalf("live produced %q, want block — the shadow assertion proves nothing if the rule cannot fire", out.action)
	}
}

// TestSuppressedEvidenceIsRecordedNotDropped pins the doctrine: a muted control
// that leaves no trace is indistinguishable from one that was never running.
func TestSuppressedEvidenceIsRecordedNotDropped(t *testing.T) {
	vel := velocity.New(velocity.Config{})
	model, _ := anomaly.New(anomaly.Config{Shadow: true}, vel)
	rules := []rule{{
		ID: "r1", Name: "noisy", Stage: StageSignup, Action: ActionBlock, Weight: 1, Enabled: true,
		All: []term{{Field: "subject.kind", Op: OpEq, Value: "account"}},
	}}
	o := observation{id: "d1", at: time.Now(), stage: StageSignup, kind: "account", subject: "a1", signals: map[string]string{}}

	out := decide(context.Background(), vel, model, Tenant("hanzo/acme"), o, rules, nil,
		func(h hit, _ observation) bool { return h.Rule == "r1" }, false)

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

// TestAgencyIsDerivedNotDeclared pins the differentiator. The classification
// turns on facts we hold — the credential class and this org's own agent
// registry — and never on a user-agent string, which is a claim.
func TestAgencyIsDerivedNotDeclared(t *testing.T) {
	for _, tc := range []struct {
		name                                          string
		credentialed, publishable, declared, humanSes bool
		want                                          string
	}{
		{"declared agent on a secret key", true, false, true, false, AgencyAgent},
		{"declared agent on a publishable key", true, true, true, false, AgencyUnknown},
		{"anonymous script", false, false, false, false, AgencyBot},
		{"publishable-key script", true, true, false, false, AgencyBot},
		{"browser session", true, false, false, true, AgencyHuman},
		{"credentialed but undeclared", true, false, false, false, AgencyUnknown},
	} {
		if got := classify(tc.credentialed, tc.publishable, tc.declared, tc.humanSes); got != tc.want {
			t.Errorf("%s: agency = %q, want %q", tc.name, got, tc.want)
		}
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
	code, got := req(t, app, http.MethodPost, "/v1/ml/train", "acme", "u_acme", body)
	if code != http.StatusOK {
		t.Fatalf("train = %d %s", code, got)
	}
	var out mlTrainOut
	_ = json.Unmarshal(got, &out)
	if out.Learned != 20 {
		t.Fatalf("learned %d of 20", out.Learned)
	}
	if mustShipped(t, s, Tenant("hanzo/acme")).State("hanzo/acme").Learned == 0 {
		t.Fatal("the caller's model learned nothing")
	}
	if mustShipped(t, s, Tenant("hanzo/beta")).State("hanzo/beta").Learned != 0 {
		t.Fatal("another tenant's model learned from this tenant's data")
	}
	// And the tenant key really is qualified, not bare. A bare org is not even a
	// key this stable can be asked for, so the assertion is made against the
	// caller's OWN store: the only one its traffic can have reached.
	if mustShipped(t, s, Tenant("hanzo/acme")).State("acme").Learned != 0 {
		t.Fatal("the model is indexed on the BARE org — two brands' same-named orgs would share it")
	}
}

// TestADecisionNamesTheModelVersionThatMadeIt is the compliance bar this plane
// was scoped against, and the record could not meet it.
//
// A decline is an adverse action. The question an auditor asks about one is
// "which model version declined this customer" — and the decision carried only a
// DIGEST, which names a geometry that any number of versions can share, and it
// was the SHIPPED model's digest even when a promoted champion of another
// geometry had decided. The champion was computed on the line above and passed
// only into the trial table.
func TestADecisionNamesTheModelVersionThatMadeIt(t *testing.T) {
	dir := t.TempDir()
	app, s := wireAt(t, dir)
	tn, _ := qualify("hanzo", "acme")
	db, err := s.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	champ := seedFit(t, s, tn, db, roleChampion, otherShape)

	code, body := req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
		`{"stage":"payment","subject":{"kind":"account","id":"acct-1"},
		  "amount":{"nano":5000000000,"currency":"USD","direction":"in"},
		  "signals":{"ip":"203.0.113.9","device":"dev-a"}}`)
	if code != http.StatusOK {
		t.Fatalf("decide = %d %s", code, body)
	}
	var d riskDecision
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.Fit != champ {
		t.Fatalf("the decision names version %q, want %q — an auditor cannot answer which model declined "+
			"this customer", d.Fit, champ)
	}
	// And the digest is the DECIDING model's, not the shipped one's. Two versions
	// of one shape share a digest, which is why the version is the answer and the
	// digest is only corroboration — but a digest naming a model that did not
	// decide is worse than none.
	want := mustStore(t, s, tn, champ, otherShape).Digest()
	if d.Model != want {
		t.Fatalf("the decision records digest %q, want the champion's %q", d.Model, want)
	}
	if d.Model == s.State.digest {
		t.Fatal("the decision records the SHIPPED model's digest while a promoted champion decided")
	}

	// It is on the RECORD and not merely in the reply: the row, the detail view
	// and the log page all carry it, because the dispute packet is read months
	// later from the file.
	var stored string
	if err := db.QueryRow(`SELECT fit FROM decision WHERE id = ?`, d.ID).Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored != champ {
		t.Fatalf("the stored decision names version %q, want %q", stored, champ)
	}
	code, body = req(t, app, http.MethodGet, "/v1/risk/decisions/"+d.ID, "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("decision detail = %d %s", code, body)
	}
	var view riskDecisionView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if view.Decision.Fit != champ || view.Model != want {
		t.Fatalf("the dispute packet says version %q digest %q, want %q / %q",
			view.Decision.Fit, view.Model, champ, want)
	}

	// A tenant running the shipped model records an EMPTY version, which is
	// itself the answer, and the shipped digest beside it.
	other, _ := qualify("hanzo", "beta")
	odb, err := s.State.shelf.open(other)
	if err != nil {
		t.Fatalf("open beta: %v", err)
	}
	code, body = req(t, app, http.MethodPost, "/v1/risk/decide", "beta", "u_beta",
		`{"stage":"payment","subject":{"kind":"account","id":"acct-1"},
		  "amount":{"nano":5000000000,"currency":"USD","direction":"in"}}`)
	if code != http.StatusOK {
		t.Fatalf("decide (beta) = %d %s", code, body)
	}
	var plain riskDecision
	if err := json.Unmarshal(body, &plain); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if plain.Fit != "" {
		t.Fatalf("a tenant with no promoted version recorded %q", plain.Fit)
	}
	if plain.Model != s.State.digest {
		t.Fatalf("the shipped model's decision records digest %q, want %q", plain.Model, s.State.digest)
	}
	if err := odb.QueryRow(`SELECT fit FROM decision WHERE id = ?`, plain.ID).Scan(&stored); err != nil {
		t.Fatalf("read back beta: %v", err)
	}
	if stored != "" {
		t.Fatalf("beta's decision names version %q", stored)
	}
}

// TestAModelThatCannotBeHousedRefusesRatherThanPanics is the fail-secure edge of
// "which model decides".
//
// champion() answers nil when a promoted geometry cannot be stood up, and the
// docstring has always said the decision is then made on rules alone with the
// model refusing. It was not: the nil went straight into Inspect, which is a
// panic on the authorisation path — a 500 for a control that was supposed to
// degrade to its rules.
func TestAModelThatCannotBeHousedRefusesRatherThanPanics(t *testing.T) {
	vel := velocity.New(velocity.Config{})
	tn := Tenant("hanzo/acme")
	o := observation{
		id: newID("dec"), at: time.Now(), stage: StagePayment, kind: "account",
		subject: "acct-1", amount: 5_000_000_000, currency: "USD", direction: "in",
	}
	out := decide(context.Background(), vel, nil, tn, o, nil,
		func(string, string) bool { return false }, nil, false)
	if out.refusal != RefusalWarming {
		t.Fatalf("a decision with no model says refusal %q, want %q", out.refusal, RefusalWarming)
	}
	if out.action != ActionAllow {
		t.Fatalf("a decision with no model and no rules acted %q", out.action)
	}

	// The rules still run and can still act — the model's absence removes the
	// model's evidence, not the control.
	r := rule{
		ID: "r-1", Name: "any payment", Stage: StagePayment, Action: ActionReview,
		Weight: 1, Severity: "medium", Enabled: true,
		All: []term{{Field: "amount.nano", Op: OpGt, Number: 1}},
	}
	out = decide(context.Background(), vel, nil, tn, o, []rule{r},
		func(string, string) bool { return false }, nil, false)
	if out.action != ActionReview {
		t.Fatalf("with no model the rules did not act: %q", out.action)
	}
	if out.refusal != RefusalWarming {
		t.Fatalf("refusal %q, want %q — silence must never read as a clean result", out.refusal, RefusalWarming)
	}
}

// TestAnExistingFileGrowsTheColumn is the half of "a fresh file and an existing
// one converge" that CREATE TABLE IF NOT EXISTS does not cover.
//
// Every tenant deciding today has a decision table with no `fit` column. The
// statement is a no-op against a table that exists, so without a widening pass
// the first decision after this ships fails to insert and the tenant stops being
// able to score at all — and the rows already written must still read back,
// answering the honest "the shipped model decided this" rather than failing.
func TestAnExistingFileGrowsTheColumn(t *testing.T) {
	_, s := wireAt(t, t.TempDir())
	tn, _ := qualify("hanzo", "acme")
	db, err := s.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Rebuild the decision plane as it was BEFORE the column existed, with a row
	// in it.
	for _, stmt := range []string{
		`DROP TABLE decision`,
		`CREATE TABLE decision (
			id TEXT PRIMARY KEY, at TEXT NOT NULL, stage TEXT NOT NULL, kind TEXT NOT NULL,
			subject TEXT NOT NULL, action TEXT NOT NULL, score REAL NOT NULL, agency TEXT NOT NULL,
			shadow INTEGER NOT NULL, refusal TEXT NOT NULL DEFAULT '', amount INTEGER NOT NULL DEFAULT 0,
			currency TEXT NOT NULL DEFAULT '', direction TEXT NOT NULL DEFAULT '',
			idem TEXT NOT NULL DEFAULT '', hits TEXT NOT NULL DEFAULT '[]',
			causes TEXT NOT NULL DEFAULT '[]', signals TEXT NOT NULL DEFAULT '{}',
			digest TEXT NOT NULL DEFAULT '', label TEXT NOT NULL DEFAULT '',
			label_by TEXT NOT NULL DEFAULT '', label_at TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO decision (id, at, stage, kind, subject, action, score, agency, shadow)
			VALUES ('dec-old', '2026-01-01T00:00:00Z', 'payment', 'account', 'acct-1', 'allow', 0, 'unknown', 1)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	if err := widen(db); err != nil {
		t.Fatalf("widen: %v", err)
	}
	// Twice, because open() runs it on every touch and a migration that is not
	// idempotent takes the tenant's whole record plane down on the second one.
	if err := widen(db); err != nil {
		t.Fatalf("widen is not idempotent: %v", err)
	}

	// The old row reads back, naming no version — which is the true answer.
	rows, err := decisionsPage(db, "", "", "", "", 10)
	if err != nil {
		t.Fatalf("decisionsPage: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "dec-old" || rows[0].Fit != "" {
		t.Fatalf("the pre-existing decision reads back as %+v", rows)
	}
	// And a new one can be written.
	o := observation{id: "dec-new", at: time.Now(), stage: StagePayment, kind: "account", subject: "acct-1"}
	if err := putDecision(db, o, outcome{id: o.id, action: ActionAllow}, "digest", "fit-1", ""); err != nil {
		t.Fatalf("putDecision after widen: %v", err)
	}
	var got string
	if err := db.QueryRow(`SELECT fit FROM decision WHERE id = 'dec-new'`).Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got != "fit-1" {
		t.Fatalf("the widened column holds %q", got)
	}
}

// TestActivityCountsWhatTheDecisionsRecorded covers the live activity view,
// which had no test of its output at all — only that the route exists and is
// gated. It reads its rows in ONE statement now; it used to read a page and
// then re-read every row of that page for its hits, five hundred sequential
// round trips on the tenant's ONE connection, which every decision for that
// tenant queues behind.
func TestActivityCountsWhatTheDecisionsRecorded(t *testing.T) {
	app, _ := wireApp(t)
	mustOK(t, app, http.MethodPost, "/v1/risk/rules", "acme", "u_acme",
		`{"rule":{"name":"probe","stage":"signup","action":"review","weight":0.9,"enabled":true,
		  "all":[{"field":"signal.ip","op":"eq","value":"192.0.2.10"}]}}`, http.StatusCreated)

	// Two that fire the rule, one that does not.
	for _, ip := range []string{"192.0.2.10", "192.0.2.10", "198.51.100.7"} {
		mustOK(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
			`{"stage":"signup","subject":{"kind":"account","id":"a-1"},"signals":{"ip":"`+ip+`"}}`,
			http.StatusOK)
	}

	code, body := req(t, app, http.MethodGet, "/v1/risk/activity", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("activity = %d %s", code, body)
	}
	var view riskActivityView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if view.Sampled != 3 {
		t.Fatalf("sampled = %d, want the 3 recorded decisions", view.Sampled)
	}
	if n := view.Actions[ActionAllow] + view.Actions[ActionReview]; n != 3 {
		t.Fatalf("the action tally covers %d of 3 decisions: %v", n, view.Actions)
	}
	if total := sum(view.Agency); total != 3 {
		t.Fatalf("the agency tally covers %d of 3 decisions: %v", total, view.Agency)
	}
	if !view.Shadow {
		t.Fatal("a tenant that never went live is reported as acting")
	}
	var probe *riskActivityRule
	for i := range view.Rules {
		if view.Rules[i].Name == "probe" {
			probe = &view.Rules[i]
		}
	}
	if probe == nil {
		t.Fatalf("the rule that fired twice is absent from the activation report: %+v", view.Rules)
	}
	if probe.Activations != 2 {
		t.Fatalf("the rule activated %d times, want 2 — the hits on the row were not read",
			probe.Activations)
	}
}

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}
