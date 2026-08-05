package commerce

// risk_test.go — the credit door's screen, held to the one property that decides
// whether shipping it is safe: WHICH WAY IT FAILS.
//
// The two failures are not the same failure and must not have the same answer:
//
//	the scorer is NOT DEPLOYED — the top-up proceeds. A control that is not
//	installed must not be able to take the product down, and this is the
//	NEGATIVE CONTROL of the whole change: if this test can be made to fail by
//	the same code that makes the next one pass, the gate is an outage.
//
//	the scorer IS here and cannot answer — the top-up waits. That is what
//	Privileged selects, and it is the reason the bit is stated at this gate.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/account"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

const (
	gateOrg  = "acme"
	gateUser = "u_412"
	// A $42.00 top-up, in the wire's own minor unit.
	gateBody = `{"sourceId":"cnon:card-nonce-ok","amountCents":4200,"currency":"USD"}`
)

// charged is the handler the gate stands in front of. Reaching it IS the allow.
func charged(c *zip.Ctx) error {
	return c.JSON(http.StatusOK, map[string]string{"status": "charged"})
}

// gateApp is the credit door reduced to the two middlewares that decide the
// outcome: the screen, and the thing it guards.
func gateApp(t *testing.T) *zip.App {
	t.Helper()
	// The scorer's socket is resolved under a directory that has none, so
	// "not deployed" is the state of the world unless a test installs a scorer.
	t.Setenv("ZIP_RUNTIME_DIR", t.TempDir())
	plane.Unbind()
	t.Cleanup(plane.Unbind)
	t.Cleanup(func() { cloud.SetRiskScorer(nil) })

	app := zip.New(zip.Config{Logger: luxlog.New("gatetest"), DisableStartupMessage: true})
	app.Post("/v1/billing/topup/token", riskGate(luxlog.New("gatetest")), charged)
	return app
}

// topup posts the credit door's own body as a validated customer.
func topup(t *testing.T, app *zip.App) (int, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/billing/topup/token", strings.NewReader(gateBody))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Org-Id", gateOrg)
	r.Header.Set("X-User-Id", gateUser)
	resp, err := app.Test(r)
	if err != nil {
		t.Fatalf("topup: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestRiskGate_ANotDeployedScorerDoesNotCloseTheCreditDoor — THE NEGATIVE
// CONTROL.
//
// A fleet that does not run the risk app must still take money. cloud's fail
// policy exempts exactly this case ("refusing to mint a credential because a
// component is not deployed is not security, it is a product that cannot be
// operated") and the plane client is what preserves it across a process boundary:
// over a socket, "not deployed" would otherwise arrive as a failed call and take
// the fail-CLOSED branch.
//
// Mutation proof: return an error instead of the absent verdict in
// scoreOverPlane, or drop the scorerUp probe, and this fails while
// [TestRiskGate_APresentScorerThatCannotAnswerMakesTheGrantWait] still passes.
func TestRiskGate_ANotDeployedScorerDoesNotCloseTheCreditDoor(t *testing.T) {
	app := gateApp(t)

	// No producer at all — the state of every process before this change.
	cloud.SetRiskScorer(nil)
	if code, body := topup(t, app); code != http.StatusOK {
		t.Fatalf("no scorer installed: %d %s, want 200 — an uninstalled control must not refuse a payment", code, body)
	}

	// The producer this change installs, against a fleet with no risk app: the
	// socket has no listener, so the answer is ABSENT and the door stays open.
	installRiskScorer(luxlog.New("gatetest"))
	if code, body := topup(t, app); code != http.StatusOK {
		t.Fatalf("scorer installed, peer not deployed: %d %s, want 200 — an unreachable model is absent, not a denial", code, body)
	}
}

// TestScoreOverPlane_AnUndeployedPeerIsAbsentRatherThanAnOutage is the same
// property one layer down, where the two facts are actually told apart — and it
// asserts the SHAPE of the answer, which the status code above cannot see.
func TestScoreOverPlane_AnUndeployedPeerIsAbsentRatherThanAnOutage(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", t.TempDir())
	plane.Unbind()
	t.Cleanup(plane.Unbind)

	q := cloud.RiskQuery{
		Stage:      cloud.StagePayment,
		Subject:    cloud.RiskSubject{Kind: plane.KindAccount, ID: "acme/u_412"},
		Privileged: true,
	}
	v, err := scoreOverPlane(context.Background(), luxlog.New("gatetest"), gateOrg, q)
	if err != nil {
		t.Fatalf("an undeployed peer reported an error: %v — an error is an outage, and this is an absence", err)
	}
	if v.Refusal != cloud.RefusalAbsent {
		t.Errorf("refusal %q, want %q — the answer must name why it is not a scored one", v.Refusal, cloud.RefusalAbsent)
	}
	if v.Action != cloud.ActionAllow {
		t.Errorf("action %q, want %q even though the query is privileged", v.Action, cloud.ActionAllow)
	}
	if v.Scored() {
		t.Error("an absent scorer produced a SCORED verdict — an unscored allow must never read as a clean one")
	}
}

// TestRiskGate_APresentScorerThatCannotAnswerMakesTheGrantWait is the other half
// of the fail policy, and the reason Privileged is stated at this gate at all:
// cloud.Privileged() does not match this route, so without the explicit bit a
// scorer outage would mint balance.
//
// Mutation proof: drop `Privileged: true` from the query and this returns 200.
func TestRiskGate_APresentScorerThatCannotAnswerMakesTheGrantWait(t *testing.T) {
	app := gateApp(t)
	cloud.SetRiskScorer(func(context.Context, string, cloud.RiskQuery) (cloud.RiskVerdict, error) {
		return cloud.RiskVerdict{}, errors.New("the model plane is unavailable")
	})

	code, body := topup(t, app)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("%d %s, want 503 — a judge that is here and did not answer makes a privileged grant wait", code, body)
	}
	if code == http.StatusPaymentRequired {
		t.Error("the refusal was a 402 — that means out of funds, which is the one thing this door exists to fix")
	}
	if !strings.Contains(body, cloud.RefusalError) {
		t.Errorf("the refusal does not name the operational fact a customer can act on: %s", body)
	}
}

// TestRiskGate_AVerdictDecidesTheOutcome — the whole action vocabulary at this
// door, including the two it has no way to honour.
func TestRiskGate_AVerdictDecidesTheOutcome(t *testing.T) {
	for _, tc := range []struct {
		action string
		want   int
	}{
		{cloud.ActionAllow, http.StatusOK},
		// Review PROCEEDS. It summons a person; it does not stop traffic.
		{cloud.ActionReview, http.StatusOK},
		{cloud.ActionBlock, http.StatusForbidden},
		// This door has no challenge to present and no reduced ceiling to fall
		// back to, so anything short of "proceed" is a refusal — never a quiet
		// proceed.
		{cloud.ActionChallenge, http.StatusForbidden},
		{cloud.ActionRestrict, http.StatusForbidden},
	} {
		t.Run(tc.action, func(t *testing.T) {
			app := gateApp(t)
			cloud.SetRiskScorer(func(context.Context, string, cloud.RiskQuery) (cloud.RiskVerdict, error) {
				return cloud.RiskVerdict{Action: tc.action, Score: 0.9, Cause: "above the cut"}, nil
			})
			if code, body := topup(t, app); code != tc.want {
				t.Errorf("%s: %d %s, want %d", tc.action, code, body, tc.want)
			}
		})
	}
}

// TestRiskGate_JudgesThePayerThatWillBeCredited pins the QUESTION rather than the
// answer: the moment, the subject, the amount, and the bit that decides which way
// silence falls.
func TestRiskGate_JudgesThePayerThatWillBeCredited(t *testing.T) {
	app := gateApp(t)
	var asked cloud.RiskQuery
	var forOrg string
	cloud.SetRiskScorer(func(_ context.Context, org string, q cloud.RiskQuery) (cloud.RiskVerdict, error) {
		forOrg, asked = org, q
		return cloud.RiskVerdict{Action: cloud.ActionAllow}, nil
	})
	if code, body := topup(t, app); code != http.StatusOK {
		t.Fatalf("%d %s, want 200", code, body)
	}

	if forOrg != gateOrg {
		t.Errorf("the model asked was %q's, want %q's — the tenant is the org whose ledger is credited", forOrg, gateOrg)
	}
	if asked.Stage != cloud.StagePayment {
		t.Errorf("stage %q, want %q", asked.Stage, cloud.StagePayment)
	}
	if asked.Subject.Kind != plane.KindAccount {
		t.Errorf("kind %q, want %q — a top-up moves an ACCOUNT's spend, and the kind namespaces the subject",
			asked.Subject.Kind, plane.KindAccount)
	}
	// The ONE subject rule, stated here independently of the gate: the wallet key
	// the balance read, the spend gate and the credit all address.
	want := account.Payer(account.Credential{Owner: gateOrg, Name: gateUser}).Subject()
	if asked.Subject.ID != want {
		t.Errorf("subject %q, want %q — the screen must judge the subject the charge credits", asked.Subject.ID, want)
	}
	if !asked.Privileged {
		t.Error("the query is not privileged — a scorer outage would mint spendable balance, " +
			"and cloud.Privileged() does not match this route")
	}
	if got := asked.Signals[plane.SignalNano]; got != "42000000000" {
		t.Errorf("nano %q, want %q — $42.00 is 4200 cents is 42e9 nano", got, "42000000000")
	}
	if got := asked.Signals["currency"]; got != "usd" {
		t.Errorf("currency %q, want %q", got, "usd")
	}
	// EVERY STATED SIGNAL IS A FACT. cloud.Facts drops the empties, because an
	// empty string is a VALUE: a scorer keying velocity on an address it never
	// received would group every such caller into one very busy one. The address is
	// absent HERE for exactly that reason and correctly — app.Test has no socket
	// peer, and 0.0.0.0 is not a client address — so what this asserts is the rule
	// rather than the fixture.
	for name, value := range asked.Signals {
		if value == "" {
			t.Errorf("signal %q travelled empty — a fact we do not have must be ABSENT, not the empty string", name)
		}
	}
}

// TestTopupSignals_AnAmountThatIsNotUSDIsNotStated. Nano is nano-USD, so a minor
// unit in another currency read as cents is a different amount of money. Absent
// beats wrong: the value features read blind, and blind is counted.
func TestTopupSignals_AnAmountThatIsNotUSDIsNotStated(t *testing.T) {
	for _, tc := range []struct{ name, body, nano string }{
		{"usd", `{"amountCents":4200,"currency":"USD"}`, "42000000000"},
		{"no currency is usd", `{"amountCents":500}`, "5000000000"},
		{"eur is not converted", `{"amountCents":4200,"currency":"eur"}`, ""},
		{"zero is not an amount", `{"amountCents":0,"currency":"usd"}`, ""},
		{"a negative is not an amount", `{"amountCents":-4200,"currency":"usd"}`, ""},
		{"an unreadable body observes nothing", `not json at all`, ""},
		{"no body observes nothing", ``, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := zip.New(zip.Config{Logger: luxlog.New("gatetest"), DisableStartupMessage: true})
			var got map[string]string
			app.Post("/probe", func(c *zip.Ctx) error {
				got = cloud.Facts(topupSignals(c))
				return c.JSON(http.StatusOK, "ok")
			})
			r := httptest.NewRequest(http.MethodPost, "/probe", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			if _, err := app.Test(r); err != nil {
				t.Fatalf("probe: %v", err)
			}
			if got[plane.SignalNano] != tc.nano {
				t.Errorf("nano %q, want %q", got[plane.SignalNano], tc.nano)
			}
		})
	}
}

// TestSignalsOf_CrossesAsASortedList. A map cannot cross this plane at all —
// zapenc refuses one at encode, which is a failure INSIDE the call and not a
// rejected field — and an unordered wire is one that cannot be compared with
// itself.
func TestSignalsOf_CrossesAsASortedList(t *testing.T) {
	got := signalsOf(map[string]string{"ip": "203.0.113.7", "currency": "usd", plane.SignalNano: "1"})
	if len(got) != 3 {
		t.Fatalf("%d signals, want 3", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Name >= got[i].Name {
			t.Errorf("signal %d (%q) is not after %q — the wire is unordered", i, got[i].Name, got[i-1].Name)
		}
	}
	if signalsOf(nil) != nil {
		t.Error("no facts must travel as no signals, never as an empty list")
	}
}

// TestPayerOrg_ResolvesOnlyTheTwoLanesThatReachThisGate. Everything else is "",
// which the scorer refuses to mint a tenant for — so a request that reaches the
// credit door naming no organisation is denied by the privileged branch rather
// than screened against nobody.
func TestPayerOrg_ResolvesOnlyTheTwoLanesThatReachThisGate(t *testing.T) {
	for _, tc := range []struct{ name, org, user, want string }{
		{"a validated customer pays from its own ledger", gateOrg, gateUser, gateOrg},
		{"an org header with no validated user is the forged case", gateOrg, "", ""},
		{"no identity at all", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := zip.New(zip.Config{Logger: luxlog.New("gatetest"), DisableStartupMessage: true})
			var got string
			app.Get("/probe", func(c *zip.Ctx) error {
				got = payerOrg(c)
				return c.JSON(http.StatusOK, "ok")
			})
			r := httptest.NewRequest(http.MethodGet, "/probe", nil)
			if tc.org != "" {
				r.Header.Set("X-Org-Id", tc.org)
			}
			if tc.user != "" {
				r.Header.Set("X-User-Id", tc.user)
			}
			if _, err := app.Test(r); err != nil {
				t.Fatalf("probe: %v", err)
			}
			if got != tc.want {
				t.Errorf("payerOrg = %q, want %q", got, tc.want)
			}
		})
	}
}
