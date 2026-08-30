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
//	peer ledger (the deployed shape) — the crossing ships plane.AuthorizeIn{Subject:""}
//	   over the internal plane, commerce's own `validate:"required"` rejects it, and
//	   because that refusal is a 4xx the money wire PRESERVES it verbatim: 400
//	   `field "subject" is required`. The caller is told to send a field that
//	   appears nowhere in the operation's published request schema, so no caller can
//	   ever satisfy it. Measured against api.hanzo.ai on POST /v1/risk/score,
//	   /learn, /features, /state, /policy, /state/model (both verbs) and
//	   /search/{id} — eight of the ten declared operations.
//
// Both are the same defect and both close with the same guard, which is why the
// fix is an ordering rule and not a message.

import (
	"context"
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
//	       facts: 405 says "wrong verb, right route", 404 says "no route". Only one of
//	       them is true here.
//
// AND THIS IS WHERE THE 404 IN THE REPORT COMES FROM, because production does not
// answer 405. Measured on api.hanzo.ai: `GET /v1/risk/score` returns 404 with the
// plain-text body `not found` — byte-identical to what `GET /v1/risk/zzz-nope`
// returns, so at the deployed edge a wrong VERB on a real route is indistinguishable
// from a route that was never registered. That is what makes a declared route read
// as unanswered. This test pins the honest answer the app itself gives; the edge that
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
	{http.MethodPut, "/v1/risk/policy", `{"review":0.01,"sample":0.001}`},
	{http.MethodPost, "/v1/risk/state/model", ""},
	{http.MethodPut, "/v1/risk/state/model", `{"address":"not-a-published-address"}`},
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
// WHERE THE RULE LIVES, since this file's own note used to name the wrong place.
// It said "delete the empty-ledger guard from [ops.gate] and every op in this
// table reports 503", and that stopped being true: the same refusal was fixed
// FLEET-WIDE in [cloud.Meter.Authorize], above both of its branches, as
// [cloud.ErrNoLedger] — so deleting this app's copy left the status and the
// sentence unchanged and only changed the envelope. The copy is gone and the
// fleet's meter is the one answer; the envelope is asserted by the test below,
// which is the half no status assertion can see.
//
// Mutation proof: remove the `if org == ""` refusal from [cloud.Meter.Authorize]
// and every op in this table reports 503 instead of 403.
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
		// The SENTENCE matters as much as the status. There is exactly one refusal
		// for this condition (cloud.ErrNoLedger, which tenantOf's own wording matches
		// verbatim) and a second wording would be a second answer to one question.
		if !strings.Contains(string(body), "no validated principal") {
			t.Errorf("%s %s refused with %s — want the one sentence for this condition, %q",
				tc.method, tc.path, body, "no validated principal")
		}
	}
}

// TestPricedOps_RefuseAnUnidentifiedCallerInTheFleetsOwnEnvelope holds the SHAPE,
// which is the half a status-and-sentence assertion cannot see — and it is the
// half that was wrong.
//
// Two envelopes used to coexist on this one surface for one refusal, with the same
// status and the same words in them. Measured, on this package's priced ops:
//
//	this app's own copy of the rule   403 {"status":403,"error":"no validated principal"}
//	the fleet's meter                 403 {"error":{"code":"forbidden","message":"no validated principal"}}
//
// The nested one is canonical because it is what the edge gate and every other
// Hanzo surface emit, so a client that reads `error.code` reads it everywhere. The
// flat one was zip rendering a returned error, and on /v1/risk it meant
// `error.code` was absent from exactly one product's refusals.
//
// Asserted STRUCTURALLY and not by substring: both bodies contain the sentence, so
// a substring check passes on either and is therefore no assertion about the shape
// at all. It reads error.code, which only one of the two has.
//
// Mutation proof: put `if ledger == "" { return nil, zip.ErrForbidden("no
// validated principal") }` back at the top of [ops.gate] and every op here reports
// the flat envelope with no error.code.
func TestPricedOps_RefuseAnUnidentifiedCallerInTheFleetsOwnEnvelope(t *testing.T) {
	probe.reset(true)
	app := mountBilled(t, &ledger{available: 100_000_000})
	for _, tc := range pricedOps {
		code, body := req(t, app, tc.method, tc.path, orgA, "", tc.body)
		if code != http.StatusForbidden {
			t.Errorf("%s %s = %d %s, want 403", tc.method, tc.path, code, body)
			continue
		}
		var env struct {
			Error *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &env); err != nil || env.Error == nil {
			t.Errorf("%s %s refused with %s — want the fleet's nested {\"error\":{\"code\",\"message\"}}; "+
				"a caller reading error.code finds nothing on this product and something on every other",
				tc.method, tc.path, body)
			continue
		}
		if env.Error.Code != "forbidden" || env.Error.Message != "no validated principal" {
			t.Errorf("%s %s refused with code=%q message=%q, want forbidden / \"no validated principal\"",
				tc.method, tc.path, env.Error.Code, env.Error.Message)
		}
	}
}

// TestPricedOps_RefuseAnUnidentifiedCallerEvenWhenTheOperatorPricesThemAtZero is
// the hole the deletion above could have opened, closed.
//
// [cloud.Meter.Authorize] returns EARLY when the cost is zero — before its own
// empty-org refusal — and the price is an operator's row in the meter authority,
// where 0 is a legal value that makes the surface free. So "the meter refuses an unidentified caller" is true only while
// somebody is charged. The app's own copy of the rule used to cover that case by
// accident, because it ran before the price was computed.
//
// The refusal therefore cannot live only at the meter: it lives at [tenantOf]
// too, which every op reaches whatever it costs. Both together are why the surface
// cannot be made anonymous by setting a price to zero.
//
// Mutation proof: revert [tenantOf]'s no-principal branch to
// zip.ErrForbidden("no validated principal") and this reports the flat envelope on
// every op; make it `return "", nil` and it reports 200 — an unidentified caller
// served, for free, by one price set to zero.
func TestPricedOps_RefuseAnUnidentifiedCallerEvenWhenTheOperatorPricesThemAtZero(t *testing.T) {
	probe.reset(true)
	// Zero is a legal price. It used to be reachable through an env var; the
	// price is a row now, so the client is the resolver itself.
	saved := screenRate
	t.Cleanup(func() { screenRate = saved })
	screenRate = func(context.Context) int64 { return 0 }
	if screenMicros(context.Background(), 1) != 0 {
		t.Fatal("the price is not zero, so this test is not exercising the free path it exists for")
	}
	app := mountBilled(t, &ledger{available: 100_000_000})
	for _, tc := range pricedOps {
		code, body := req(t, app, tc.method, tc.path, orgA, "", tc.body)
		if code != http.StatusForbidden {
			t.Errorf("%s %s = %d %s at price zero, want 403 — a free surface is not an anonymous one",
				tc.method, tc.path, code, body)
			continue
		}
		var env struct {
			Error *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &env); err != nil || env.Error == nil ||
			env.Error.Code != "forbidden" || env.Error.Message != "no validated principal" {
			t.Errorf("%s %s at price zero refused with %s — want the same nested envelope it refuses "+
				"with when priced", tc.method, tc.path, body)
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
