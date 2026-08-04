package risk

// gate_order_test.go — the ONE ordering this surface cannot get wrong: the tenant
// is resolved BEFORE the money plane is asked anything.
//
// WHY THIS IS ITS OWN FILE. [TestTypedOpsRefuseAnUnvalidatedPrincipal] already
// asserts that every op refuses a forged header with a 403, and it PASSED
// throughout the defect this file catches — because it mounts through [mountApp],
// which builds deps with NO metering client. With no client the money gate is a
// no-op, so the op fell through to the tenant gate and answered the honest 403 the
// test asked for. A deployed binary HAS a money client, prices its ops, and
// therefore reaches the gate FIRST. The fixture made the property unobservable:
// the assertion was true of the test and false of production.
//
// So these tests mount through [mountBilled] — the same real metering client the
// priced-op tests use — which is the only fixture in which the ordering is
// observable at all.
//
// WHAT WENT WRONG, in one sentence: [ops.gate] re-derived the caller's ledger with
// principal.Ledger, which answers "" for exactly the requests the tenant gate
// refuses, and then asked the money plane to authorize a spend for a NAMELESS
// subject instead of refusing.
//
// The two shapes that empty ledger produced, both measured:
//
//	local ledger (this fixture) — metering refuses an empty org fail-closed, which
//	   is not a 4xx, so the money wire's fallback renders 503 "Billing temporarily
//	   unavailable". An unauthenticated caller is told the BILLER is broken.
//	peer ledger (the deployed shape) — gatePeer ships plane.AuthorizeIn{Subject:""}
//	   over the internal plane, commerce's own `validate:"required"` rejects it, and
//	   because that refusal is a 4xx the money wire PRESERVES it verbatim: 400
//	   `field "subject" is required`. The caller is told to send a field that
//	   appears nowhere in the operation's published request schema, so no caller can
//	   ever satisfy it. Measured against api.hanzo.ai on POST /v1/risk/score,
//	   /learn, /features, /state, /state/appetite, /state/snapshot, /state/restore
//	   and /search/{id} — eight of the ten declared paths.
//
// Both are the same defect and both close with the same guard, which is why the
// fix is an ordering rule and not a message.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestScore_AnswersTheMethodItDeclaresAndOnlyThat settles the report that started
// this file: "/v1/risk/score is in the published spec and returns 404".
//
// The declaration is sound and must NOT be withdrawn — the operation exists and it
// opens. The document declares POST and only POST, so:
//
//	POST — bound, and it answers 200 with a decodable verdict.
//	GET  — 405. The ROUTE exists and the VERB does not, and those are different
//	       facts: 405 says "wrong verb, right door", 404 says "no door". Only one of
//	       them is true here.
//
// AND THIS IS WHERE THE 404 IN THE REPORT COMES FROM, because production does not
// answer 405. Measured on api.hanzo.ai: `GET /v1/risk/score` returns 404 with the
// plain-text body `not found` — byte-identical to what `GET /v1/risk/zzz-nope`
// returns, so at the deployed edge a wrong VERB on a real route is indistinguishable
// from a route that was never registered. That is what makes a declared door read as
// unanswered. This test pins the honest answer the app itself gives; the edge that
// flattens it to 404 is named in the report rather than papered over here, because
// this suite mounts the app and cannot reach that layer.
//
// Mutation proof: change the route's method to GET and the POST half fails; delete
// the op and the GET half stops being a 405.
func TestScore_AnswersTheMethodItDeclaresAndOnlyThat(t *testing.T) {
	probe.reset(true)
	app := mountBilled(t, &ledger{available: 100_000_000})
	const body = `{"event":{"id":"tx_9","kind":"account","subject":"u_412","nano":420000000}}`

	code, out := req(t, app, http.MethodPost, "/v1/risk/score", orgA, "u_"+orgA, body)
	if code != http.StatusOK {
		t.Fatalf("POST /v1/risk/score = %d %s, want 200 — the declared operation must open", code, out)
	}
	var v riskScoreOut
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("the verdict does not decode as its published schema: %v (%s)", err, out)
	}
	// A brand-new model has learned nothing, so it must DECLINE with a reason
	// rather than answer a score of zero — an unscored event is not a clean one.
	if v.Scored {
		t.Error("a model that has learned nothing reported a scored verdict")
	}
	if v.Refusal == "" {
		t.Error("the model declined without naming the refusal — silence reads as innocence")
	}

	// The verb the document does not declare is refused as a VERB error, not as a
	// missing route. A 404 here would be the app itself telling a caller its own
	// published operation does not exist.
	if code, out := req(t, app, http.MethodGet, "/v1/risk/score", orgA, "u_"+orgA, ""); code != http.StatusMethodNotAllowed {
		t.Errorf("GET /v1/risk/score = %d %s, want 405 — the route is declared, the verb is not, "+
			"and a 404 would make those two facts one", code, out)
	}
}

// pricedOps is every operation that asks the money plane, with a body that is
// VALID for it — so a refusal can only come from the gate or the tenant, never
// from decoding. /v1/risk/health is absent because it is not priced and not
// gated; it is the one route that must answer without a principal.
var pricedOps = []struct{ method, path, body string }{
	{http.MethodPost, "/v1/risk/score", `{"event":{"kind":"account","subject":"u_1"}}`},
	{http.MethodPost, "/v1/risk/learn", `{"events":[{"kind":"account","subject":"u_1"}]}`},
	{http.MethodGet, "/v1/risk/state", ""},
	{http.MethodPut, "/v1/risk/state/appetite", `{"review":0.01,"sample":0.001}`},
	{http.MethodPost, "/v1/risk/state/snapshot", ""},
	{http.MethodPost, "/v1/risk/state/restore", `{"body":{"version":1}}`},
	{http.MethodGet, "/v1/risk/features", ""},
	{http.MethodPost, "/v1/risk/search", `{"days":7}`},
	{http.MethodGet, "/v1/risk/search/srch_1", ""},
}

// TestPricedOps_ResolveTheTenantBeforeTheyAskForMoney is the gate on the ordering.
//
// A request with an org header and no validated user is the forged-header case.
// Every priced op must answer 403 with the tenant gate's own sentence, over a
// MONEY-CONFIGURED mount. A 503 means the money plane was asked and could not
// price a nameless subject; a 400 means it was asked and its far end refused the
// empty field. Both are the money plane answering a question about identity.
//
// Mutation proof: delete the empty-ledger guard from [ops.gate] and every op in
// this table reports 503 instead of 403.
func TestPricedOps_ResolveTheTenantBeforeTheyAskForMoney(t *testing.T) {
	probe.reset(true)
	// A funded ledger, so a refusal can never be mistaken for poverty: the money is
	// there, and the answer must still be 403 because there is nobody to bill.
	app := mountBilled(t, &ledger{available: 100_000_000})
	for _, tc := range pricedOps {
		code, body := req(t, app, tc.method, tc.path, orgA, "", tc.body)
		if code != http.StatusForbidden {
			t.Errorf("%s %s = %d %s, want 403 — the tenant is resolved before the money plane is asked",
				tc.method, tc.path, code, body)
			continue
		}
		// The SENTENCE matters as much as the status. The app owns exactly one
		// refusal for this condition (tenantOf) and a second wording would be a
		// second answer to one question.
		if !strings.Contains(string(body), "no validated principal") {
			t.Errorf("%s %s refused with %s — want the tenant gate's own sentence, %q",
				tc.method, tc.path, body, "no validated principal")
		}
	}
}

// A NOTE ON A TEST THAT IS NOT HERE, because the absence is the finding.
//
// The obvious companion assertion is "an unvalidated request makes NO call to the
// money plane", counted on the ledger fake. It was written, and it PASSED against
// the defect — so it was deleted rather than shipped. The reason is worth keeping:
// with an empty org the metering client refuses inside AuthorizeVerdict ("metering:
// empty org") BEFORE it ever issues an HTTP request, so a counter on the fake's
// round-trips reads zero whether the gate was reached or not. The counter measured
// the transport, and the property is about the DECISION.
//
// In the deployed peer shape that same empty subject DOES cross the wire, which is
// why production answers 400 and this fixture answers 503. Observing that would
// mean standing up the internal plane's socket and a fake commerce peer, which is
// an integration fixture and not this file. The status assertion above is the
// falsifiable statement of the same rule: a money-wire refusal of ANY shape means
// the money plane was asked a question about identity.

// TestHealth_AnswersWithoutAPrincipal keeps the fix from over-reaching. The probe
// is the one route that must answer a kubelet, which carries no principal and no
// ledger, and a guard that refused it would turn a liveness check into a 403 and
// take the pod down.
func TestHealth_AnswersWithoutAPrincipal(t *testing.T) {
	probe.reset(true)
	app := mountBilled(t, &ledger{available: 100_000_000})
	code, body := req(t, app, http.MethodGet, "/v1/risk/health", "", "", "")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/risk/health = %d %s, want 200 without a principal", code, body)
	}
}

// TestPricedOps_StillReachTheMoneyPlaneForARealPrincipal is the POSITIVE half, and
// it is what keeps the guard above from being a way to make the priced-op tests
// green by never billing anyone. A validated caller's ledger is named, so the ask
// must happen.
//
// Mutation proof: widen the guard to refuse every caller (drop the `== ""`) and
// this fails with zero asks.
func TestPricedOps_StillReachTheMoneyPlaneForARealPrincipal(t *testing.T) {
	probe.reset(true)
	books := &ledger{available: 100_000_000}
	app := mountBilled(t, books)
	code, body := req(t, app, http.MethodGet, "/v1/risk/state", orgA, "u_"+orgA, "")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/risk/state = %d %s, want 200 for a validated principal", code, body)
	}
	if books.asks() == 0 {
		t.Fatal("a validated, priced read never reached the money plane — the guard refuses everyone, " +
			"which makes the surface free rather than gated")
	}
}
