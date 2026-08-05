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
	"os"
	"path/filepath"
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
//
// It answers a SETTLED RECEIPT — commerce's own TakePaymentOut field names — because
// reaching the handler is no longer the end of the door: an allow that clears a charge
// then credits the spendable ledger off exactly this answer (settle.go), so a fixture
// that answered anything else would be testing a door that refuses.
func charged(c *zip.Ctx) error {
	return c.JSON(http.StatusOK, map[string]any{
		"transactionId": settledReceipt,
		"balanceCents":  gateCents,
		"status":        "ok",
		"processorRef":  settledRef,
	})
}

// gateApp is the credit door reduced to the two middlewares that decide the
// outcome: the screen, and the thing it guards.
func gateApp(t *testing.T) *zip.App {
	t.Helper()
	// The scorer's socket is resolved under a directory that has none, so
	// "not deployed" is the state of the world unless a test installs a scorer.
	shortRuntimeDir(t)
	plane.Unbind()
	t.Cleanup(plane.Unbind)
	t.Cleanup(func() { cloud.SetRiskScorer(nil) })

	app := zip.New(zip.Config{Logger: luxlog.New("gatetest"), DisableStartupMessage: true})
	// The screen WRAPPING THE HANDLER, exactly as mount.go registers the browser door.
	// settling supplies the ledger a settled top-up now deposits into and the receipt
	// its amount is read off (settle_test.go) — the money half is not this file's
	// subject, but a door that cannot finish a settlement cannot answer 200 either.
	app.Post("/v1/billing/topup/token", settling(t).route(charged))
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
	shortRuntimeDir(t)
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

// shortRuntimeDir points the fleet's socket scheme at an anonymous, SHORT-named
// directory and returns nothing, because the address is all the caller needs.
//
// A unix socket address is capped near a hundred bytes (104 on darwin), and
// t.TempDir embeds the calling test's own name — so
// TestScoreOverPlane_AnUndeployedPeerIsAbsentRatherThanAnOutage produced a
// sockaddr over the cap and every dial answered `connect: invalid argument`.
// That reads exactly like the "peer is not deployed" case these tests assert,
// so the suite was red on any Mac while green in CI, and red for a reason that
// looked like the thing under test.
//
// Same fix, same reason, as servePeerLedger in ledger_peer_test.go. Tests that
// deliberately want an UNUSABLE address build one explicitly (see
// unusableRuntimeDir) — this is only for the ones that want a working socket.
func shortRuntimeDir(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("", "zip")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("ZIP_RUNTIME_DIR", dir)
}

// unusableRuntimeDir points the fleet's socket scheme at a directory whose socket
// paths cannot be DIALLED — the third fact [plane.Listening] separates, and the one
// the converse controls below turn on.
//
// The path is longer than sun_path (108 bytes on Linux, 104 on Darwin), so the
// address is refused while the sockaddr is built — before any syscall, in
// microseconds, and against a scorer that may be perfectly healthy. That is the
// shape of every real one: EMFILE once the process is out of descriptors, EACCES on
// a run directory it may not open, ENAMETOOLONG on an over-long one. None of them
// is ENOENT and none is ECONNREFUSED, so none of them is an ABSENCE — and none is
// slow enough for the 150ms budget to catch.
func unusableRuntimeDir(t *testing.T) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", filepath.Join(t.TempDir(), strings.Repeat("d", 200)))
	plane.Unbind()
	t.Cleanup(plane.Unbind)
}

// TestScoreOverPlane_AnUnusableSocketIsAnOutageRatherThanAnAbsence — THE MIRROR
// IMAGE of [TestScoreOverPlane_AnUndeployedPeerIsAbsentRatherThanAnOutage], and
// the control that side had no counterpart for.
//
// Absence and outage are the two facts the whole probe exists to tell apart, and
// the probe reports THREE: no listener, a listener, and a socket that is present
// and unusable. The third one is an outage. Folding it into the first — which is
// what `err == nil && up` does — hands the fail policy the one refusal it EXEMPTS,
// so a privileged grant at the credit door is waved through by a scorer nobody
// could reach. Reachable without touching the risk app at all: exhaust this
// process's descriptors and every probe answers "no scorer here".
//
// Mutation proof: restore `return err == nil && up` in scorerUp, or drop the error
// branch in scoreOverPlane, and BOTH assertions below fail while
// [TestScoreOverPlane_AnUndeployedPeerIsAbsentRatherThanAnOutage] still passes.
func TestScoreOverPlane_AnUnusableSocketIsAnOutageRatherThanAnAbsence(t *testing.T) {
	unusableRuntimeDir(t)
	t.Cleanup(func() { cloud.SetRiskScorer(nil) })

	q := cloud.RiskQuery{
		Stage:      cloud.StagePayment,
		Subject:    cloud.RiskSubject{Kind: plane.KindAccount, ID: "acme/u_412"},
		Privileged: true,
	}

	// THE PROBE ITSELF, first: a socket that cannot be dialled is not a socket with
	// nobody behind it, and the difference is the error.
	if up, err := scorerUp(); err == nil {
		t.Fatalf("an undiallable socket probed clean (up=%v) — the fixture proves nothing", up)
	}

	// THE SHAPE. An outage is an ERROR out of this seam. Answering it as a verdict
	// at all would be this client deciding the fail policy for itself, and the only
	// verdict it is entitled to state is the ABSENT one.
	v, err := scoreOverPlane(context.Background(), luxlog.New("gatetest"), gateOrg, q)
	if err == nil {
		t.Fatalf("an unusable socket answered %+v with no error — an unreachable-but-present "+
			"scorer is an outage, and only a NOT-DEPLOYED one may read as an absence", v)
	}
	if v.Refusal == cloud.RefusalAbsent {
		t.Errorf("refusal %q — an outage was laundered into the one refusal the fail policy exempts", v.Refusal)
	}

	// AND THE POLICY'S ANSWER TO IT, which is the fact that decides whether money
	// moves: the query is privileged, so the seam BLOCKS rather than allows.
	installRiskScorer(luxlog.New("gatetest"))
	switch got := cloud.Decide(context.Background(), gateOrg, q); {
	case got.Action == cloud.ActionAllow:
		t.Errorf("a privileged grant was ALLOWED against an unreachable scorer (%+v) — "+
			"this is the fail-open that mints spendable balance during a risk-plane outage", got)
	case got.Action != cloud.ActionBlock:
		t.Errorf("action %q, want %q", got.Action, cloud.ActionBlock)
	case got.Refusal != cloud.RefusalError:
		t.Errorf("refusal %q, want %q — the record must name the operational fact", got.Refusal, cloud.RefusalError)
	}
}

// TestRiskGate_AnUnusableScorerSocketMakesTheGrantWait is that same property at
// the WIRE, where the money is: the door answers 503 and the charge does not
// settle.
//
// Mutation proof: restore `return err == nil && up` in scorerUp and this answers
// 200 — the top-up settles, unscored, during a risk-plane failure.
func TestRiskGate_AnUnusableScorerSocketMakesTheGrantWait(t *testing.T) {
	app := gateApp(t)
	unusableRuntimeDir(t)
	installRiskScorer(luxlog.New("gatetest"))

	code, body := topup(t, app)
	if code == http.StatusOK {
		t.Fatalf("the top-up SETTLED against an unreachable scorer: %d %s — a stolen card clears "+
			"whenever this process runs out of descriptors", code, body)
	}
	if code != http.StatusServiceUnavailable {
		t.Fatalf("%d %s, want 503 — the judge's door is there and unusable, so a privileged grant waits", code, body)
	}
	if !strings.Contains(body, cloud.RefusalError) {
		t.Errorf("the refusal does not name the operational fact a customer can act on: %s", body)
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

// TestRiskGate_ADeterminationIsNotAnOutage — a DECIDED verdict that also carries a
// refusal is the screen's answer, never the fail policy's.
//
// Action and Refusal became INDEPENDENT when the rule and the model were fused:
// the severest of the two judgements stands, and the model's own refusal is
// recorded beside it rather than replaced by it. So an armed organisation whose
// model is still warming, on a payment a stated rule froze, answers
// {restrict, "warming"} — a determination over stated facts, with the model merely
// having had nothing to add.
//
// Read off the REFUSAL ALONE that is a 503 "try again in a moment": the door
// invites the retry that settles the payment it just froze, and reports a control
// working exactly as designed as an outage. The discriminator is the pair.
//
// Mutation proof: change the branch back to `if v.Refusal != ""` and the first two
// cases answer 503; drop the `v.Refusal != ""` conjunct and the third answers 403,
// which loses the one operational fact a customer can act on.
func TestRiskGate_ADeterminationIsNotAnOutage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		action  string
		refusal string
		want    int
	}{
		{"a rule froze it while the model was warming", cloud.ActionRestrict, "warming", http.StatusForbidden},
		{"a rule froze it while the model was unusable", cloud.ActionRestrict, "unusable", http.StatusForbidden},
		// The fuse keeps REVIEW proceeding whatever the model had to say, so a
		// determination that only summons a person still serves the customer.
		{"a rule examined it while the model was warming", cloud.ActionReview, "warming", http.StatusOK},
		// The ONE shape the fail policy produces, and the only one that is a 503.
		// BLOCK is what makes it identifiable: the scorer's own vocabulary tops out
		// at restrict, held closed by
		// [TestActions_TheScorerNeverBlocks] in apps/risk.
		{"the judge is here and did not answer", cloud.ActionBlock, cloud.RefusalError, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := gateApp(t)
			cloud.SetRiskScorer(func(context.Context, string, cloud.RiskQuery) (cloud.RiskVerdict, error) {
				return cloud.RiskVerdict{Action: tc.action, Refusal: tc.refusal, Score: 0.9}, nil
			})
			code, body := topup(t, app)
			if code != tc.want {
				t.Fatalf("{%s, %q}: %d %s, want %d", tc.action, tc.refusal, code, body, tc.want)
			}
			if tc.want == http.StatusForbidden && strings.Contains(body, "try again") {
				t.Errorf("a determination was answered as a retryable outage: %s", body)
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
	// THE PAYER, AND NOT THE ACCOUNT, and the kind is what makes that structural.
	// The kind namespaces the subject, so it selects which POPULATION's aggregates
	// answer — and an account's are its metered inference spend. Judged as an
	// account, a top-up is scored against a distribution of money spent OUT, and the
	// windowed value bounds the aggregate rule reads (a PAYMENTS appetite) accrue on
	// that same key: a customer with a large inference bill is examined for it, and
	// no restatement of the number can fix a population.
	if asked.Subject.Kind != plane.KindPayer {
		t.Errorf("kind %q, want %q — a top-up is the PAYER moving money in, and an account's "+
			"aggregates are its inference spend; one key for both is one appetite over two populations",
			asked.Subject.Kind, plane.KindPayer)
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

// TestPaymentSignals_AnAmountThatIsNotUSDIsNotStated. Nano is nano-USD, so a minor
// unit in another currency read as cents is a different amount of money. Absent
// beats wrong: the value features read blind, and blind is counted.
func TestPaymentSignals_AnAmountThatIsNotUSDIsNotStated(t *testing.T) {
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
			// The RAW door's whole read: the wire parse and the signal rule it feeds,
			// which is the pair mount.go's top-up runs through [screen.route].
			app.Post("/probe", func(c *zip.Ctx) error {
				cents, currency := wireAmount(c)
				got = cloud.Facts(paymentSignals(c, cents, currency))
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
