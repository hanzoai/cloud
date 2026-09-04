package commerce

// risk_settlement_test.go — THE CREDIT ENDPOINT'S OTHER HALF: what it STATES, and
// what it TELLS the model afterwards.
//
// The screen was reading history that nothing was writing. [riskGate] asks the
// scorer about a payer, the scorer's aggregate rules answer from what that payer had
// already done — and at a self-serve credit endpoint the organisation IS the payer, so
// nothing it did taught its own model anything. Five payments of eleven thousand
// dollars each looked like five first payments, every one of them under every stated
// bound by construction.
//
// Two properties make that fixed, and both are here:
//
//	THE ENDPOINT STATES ITS AXES. A rule half over an identifier no gate states
//	cannot fire for any input, and reads exactly like a half that found nothing.
//
//	THE SETTLEMENT TEACHES. What the model learns comes from what this process
//	WATCHED SETTLE — never from the request body — is keyed on the settlement so a
//	retry converges, and can never fail the payment it describes.

import (
	"context"
	"errors"
	"github.com/hanzoai/cloud/internal/planetest"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/account"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
)

// The settlement fixture: a real [TakePaymentOut] shape, because what this endpoint
// keys an idempotent record on is read out of exactly that answer.
const (
	settledRef     = "sq_pay_9Xk2"
	settledReceipt = "txn_4412"
)

// settled answers the way the top-up handler answers a charge that cleared.
func settledBody(status int, body string) zip.Handler {
	return func(c *zip.Ctx) error {
		c.Fiber().Response().Header.Set("Content-Type", "application/json")
		return c.Bytes(status, []byte(body))
	}
}

// caught is one observation that LEFT this process, as the client saw it.
type caught struct {
	org string
	in  *client.RiskObserveIn
}

// watchTeaching substitutes the plane client and hands back what leaves. The client is a
// variable for exactly this: the endpoint's record can be asserted without standing up
// a risk child to receive it.
// The buffer holds a BURST rather than one payment, because the cross-endpoint proof
// (risk_payments_test.go) drives several payments before it reads any: teaching is
// detached, so a buffer smaller than the burst blocks the goroutines under test
// instead of recording them.
func watchTeaching(t *testing.T) <-chan caught {
	t.Helper()
	mute(t)
	seen := make(chan caught, 16)
	prior := teach
	teach = func(ctx context.Context, in *client.RiskObserveIn) (*client.RiskObserved, error) {
		seen <- caught{org: cloud.Who(ctx).Org, in: in}
		return &client.RiskObserved{Learned: 1}, nil
	}
	t.Cleanup(func() { teach = prior })
	return seen
}

// endpointApp is the credit endpoint with a handler that answers like the real one.
func endpointApp(t *testing.T, status int, body string) *zip.App {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t))
	client.Unbind()
	t.Cleanup(client.Unbind)
	t.Cleanup(func() { cloud.SetRiskScorer(nil) })
	cloud.SetRiskScorer(func(context.Context, string, cloud.RiskQuery) (cloud.RiskVerdict, error) {
		return cloud.RiskVerdict{Action: cloud.ActionAllow}, nil
	})
	app := zip.New(zip.Config{Logger: luxlog.New("endpointtest"), DisableStartupMessage: true})
	// settling supplies the ledger and the receipt read the settlement credit needs
	// (settle_test.go). What an endpoint TEACHES is this file's subject and what it
	// CREDITS is not, but the two run at the same point and the credit runs first, so an
	// endpoint with no ledger behind it never reaches the teaching at all.
	app.Post("/v1/billing/topup/token", settling(t).route(settledBody(status, body)))
	return app
}

func post(t *testing.T, app *zip.App) int {
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
	_, _ = io.ReadAll(resp.Body)
	return resp.StatusCode
}

// await takes the one observation the endpoint should have stated, or fails.
func await(t *testing.T, seen <-chan caught) caught {
	t.Helper()
	select {
	case c := <-seen:
		return c
	case <-time.After(3 * time.Second):
		t.Fatal("a settled top-up stated NOTHING to the risk model — the aggregate rules are " +
			"reading a history nothing writes, which is where this batch started")
		return caught{}
	}
}

// released reports whether every teaching slot has come back inside the deadline.
// It polls rather than sleeping a fixed time: the ceiling is released by a goroutine
// defer, so the only honest question is whether it happens at all.
func released(within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if inflight() == 0 {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return inflight() == 0
}

// inflight is how many detached hand-offs a settlement still has running. A
// settlement spawns TWO — the risk teaching and the event-plane statement — and a
// fixture that tears down while either is running is the race [quiet] exists to
// avoid, so both are counted here rather than one of them being watched and the
// other being hoped about.
func inflight() int { return len(teaching) + len(emitting) }

// none asserts the endpoint stated nothing, which is the correct answer for a top-up
// that did not settle.
func none(t *testing.T, seen <-chan caught) {
	t.Helper()
	select {
	case c := <-seen:
		t.Fatalf("the endpoint taught the model from a top-up that did not settle: %+v", c.in)
	case <-time.After(250 * time.Millisecond):
	}
}

// TestPaymentFacts_StatesThePeerThatArmsTheFanOut is HIGH-1's positive half.
//
// [risk.onFan] is the only half of the rule that can see account farming, because a
// farm is unremarkable from every account taken by itself — the pattern exists only
// in what the accounts SHARE. It reads two link identifiers, and this endpoint stated
// NEITHER: with ip, currency, country and nano the only facts, the counterparty and
// device axes were empty on every top-up and that half could not fire for any input.
//
// Mutation proof: drop the peer from [paymentFacts] and this fails; keep stating it as
// the empty string when the edge resolved nothing and the second half fails, because
// every unresolvable payer would pool into one very busy identifier and reach the
// fan-out bound on volume alone.
func TestPaymentFacts_StatesThePeerThatArmsTheFanOut(t *testing.T) {
	const address = "198.51.100.22"
	facts := paymentFacts(address, "US", 4200, "USD")
	if got := facts[client.SignalPeer]; got != address {
		t.Errorf("peer %q, want %q — an axis the endpoint does not state cannot be read, and the "+
			"fan-out half is unreachable without it", got, address)
	}
	// ABSENT, NEVER EMPTY. cloud.Facts drops an empty value, but the map itself must
	// not carry one: a key present with no value is a gate claiming to have looked.
	bare := paymentFacts("", "", 4200, "USD")
	if got, held := bare[client.SignalPeer]; held {
		t.Errorf("the endpoint stated a peer of %q when the edge resolved none — every payer whose "+
			"address we cannot resolve then shares ONE identifier, and the fan-out reports a "+
			"farm made of strangers", got)
	}
}

// TestPaymentAxes_MatchWhatTheEndpointActuallyStates holds the boot declaration to the
// endpoint.
//
// The unarmed axis is announced at boot precisely so it cannot read as a rule that
// found nothing — but a declaration that drifts from the endpoint is worse than none,
// because it is a claim an operator will believe. This is what keeps them one fact:
// every armed axis must be stateable, and every unarmed one must be unstated.
//
// Mutation proof: state a device in [paymentFacts] without moving it out of
// [paymentUnarmed] (or add an axis to [paymentAxes] the endpoint never states) and this
// fails.
func TestPaymentAxes_MatchWhatTheEndpointActuallyStates(t *testing.T) {
	// Everything the endpoint can observe, stated at once, so this is the endpoint's
	// whole reach rather than one sample of it.
	facts := paymentFacts("198.51.100.22", "US", 4200, "USD")
	for _, axis := range paymentAxes {
		if facts[axis] == "" {
			t.Errorf("the boot declaration claims %q is armed, and the endpoint states nothing for "+
				"it — an operator is being told a rule half can fire when it cannot", axis)
		}
	}
	for _, axis := range paymentUnarmed {
		if got, held := facts[axis]; held {
			t.Errorf("the boot declaration says %q is UNARMED and the endpoint states %q for it — "+
				"the announcement an operator reads is false", axis, got)
		}
	}
	// And the two lists are disjoint, or the declaration says both things at once.
	for _, armed := range paymentAxes {
		for _, unarmed := range paymentUnarmed {
			if armed == unarmed {
				t.Errorf("%q is declared both armed and unarmed", armed)
			}
		}
	}
}

// TestTeachSettlement_ASettledTopUpTeachesThePayer is HIGH-2 at the endpoint: what
// LEAVES this process when a charge clears.
//
// Mutation proof: remove the teachSettlement call from [riskGate] and this fails on
// the timeout — which is the state the batch began in, with the aggregate rules
// reading a history nothing wrote.
func TestTeachSettlement_ASettledTopUpTeachesThePayer(t *testing.T) {
	seen := watchTeaching(t)
	app := endpointApp(t, http.StatusOK,
		`{"transactionId":"`+settledReceipt+`","balanceCents":4200,"status":"ok","processorRef":"`+settledRef+`"}`)
	if code := post(t, app); code != http.StatusOK {
		t.Fatalf("topup: %d, want 200", code)
	}
	got := await(t, seen)

	// THE TENANT IS THE PAYER'S ORG, stated on the call rather than inherited — the
	// org that PAYS is not always the org that asked.
	if got.org != gateOrg {
		t.Errorf("the observation was filed under %q, want %q", got.org, gateOrg)
	}
	if got.in.Stage != cloud.StagePayment {
		t.Errorf("stage %q, want %q", got.in.Stage, cloud.StagePayment)
	}
	// THE SAME SUBJECT THE SCREEN JUDGED. A screen that read one subject while the
	// record taught another would leave the judged one's velocity empty forever,
	// however many payments settled.
	want := account.Payer(account.Credential{Owner: gateOrg, Name: gateUser}).Subject()
	if got.in.Kind != client.KindPayer || got.in.Subject != want {
		t.Errorf("taught %q/%q, want %q/%q — the record must name the subject the screen judged",
			got.in.Kind, got.in.Subject, client.KindPayer, want)
	}
	// THE SETTLEMENT IS THE KEY, so a retry converges instead of counting the money
	// again.
	if got.in.Settlement != settledRef {
		t.Errorf("settlement %q, want the processor's own reference %q", got.in.Settlement, settledRef)
	}
	// AND THE VALUE THAT SETTLED, on the axis the accrual is read over.
	var nano string
	for _, s := range got.in.Signals {
		if s.Name == client.SignalNano {
			nano = s.Value
		}
	}
	if nano != "42000000000" {
		t.Errorf("nano %q, want %q — an accrual with no value in it is a count wearing a "+
			"payments appetite", nano, "42000000000")
	}
}

// TestTeachSettlement_KeysOnTheProcessorReferenceFirst.
//
// Two paths can credit ONE payment — the synchronous endpoint and a replayed webhook —
// and each writes its own ledger row with its own receipt, while both carry the
// gateway's payment id. Keyed on the reference, two paths crediting one payment teach
// ONE observation; keyed on the receipt they teach two, and the accrual counts money
// that arrived once as having arrived twice.
//
// The receipt is the FALLBACK and not the default: a processor that states no
// reference still settled, and refusing to teach it would leave a real payment out.
func TestTeachSettlement_KeysOnTheProcessorReferenceFirst(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{
			"both stated — the gateway's reference wins",
			`{"transactionId":"` + settledReceipt + `","status":"ok","processorRef":"` + settledRef + `"}`,
			settledRef,
		},
		{
			"no reference — the ledger receipt carries it",
			`{"transactionId":"` + settledReceipt + `","status":"ok"}`,
			settledReceipt,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen := watchTeaching(t)
			if code := post(t, endpointApp(t, http.StatusOK, tc.body)); code != http.StatusOK {
				t.Fatalf("topup: %d", code)
			}
			if got := await(t, seen); got.in.Settlement != tc.want {
				t.Errorf("settlement %q, want %q", got.in.Settlement, tc.want)
			}
		})
	}
}

// TestTeachSettlement_TeachesNothingWithoutASettlement.
//
// This is the property that makes the accrual worth reading: it is driven by what
// SETTLED, not by what was asked for. A payer who could teach velocity by sending
// requests that fail — or by sending a body — would be filling the very history the
// bound is read from.
//
// Mutation proof: teach before [zip.Ctx.Next], or ignore the response status, and
// every row here becomes a taught observation for money that never moved.
func TestTeachSettlement_TeachesNothingWithoutASettlement(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"the card was declined", http.StatusPaymentRequired, `{"error":{"code":"declined"}}`},
		{"the gateway broke", http.StatusBadGateway, `{"error":{"code":"upstream"}}`},
		{"the request was malformed", http.StatusBadRequest, `{"error":{"code":"bad"}}`},
		// A 200 that names no settlement is the one case where the endpoint DID answer
		// success: it is still not taught, because an observation under an invented id
		// counts the same money again on the next retry.
		{"success naming no settlement", http.StatusOK, `{"status":"ok"}`},
		{"success with an unreadable answer", http.StatusOK, `not json at all`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen := watchTeaching(t)
			post(t, endpointApp(t, tc.status, tc.body))
			none(t, seen)
		})
	}
}

// TestTeachSettlement_CannotFailTheSettledPayment.
//
// The money has moved and the customer has been answered by the time the model is
// told anything. So a risk plane that is absent, refusing, panicking or slow must
// leave the endpoint's own answer untouched — a telemetry row is expendable and a
// settled payment is not.
//
// Mutation proof: return the teach error from [riskGate] and the first row here
// answers 500 for a charge that cleared.
func TestTeachSettlement_CannotFailTheSettledPayment(t *testing.T) {
	const body = `{"transactionId":"` + settledReceipt + `","status":"ok","processorRef":"` + settledRef + `"}`
	for _, tc := range []struct {
		name string
		call func(context.Context, *client.RiskObserveIn) (*client.RiskObserved, error)
	}{
		{"the plane refuses", func(context.Context, *client.RiskObserveIn) (*client.RiskObserved, error) {
			return nil, errors.New("no peer")
		}},
		{"the plane answers nothing", func(context.Context, *client.RiskObserveIn) (*client.RiskObserved, error) {
			return nil, nil
		}},
		{"the plane panics", func(context.Context, *client.RiskObserveIn) (*client.RiskObserved, error) {
			panic("the risk child died mid-call")
		}},
		{"the settlement was already recorded", func(context.Context, *client.RiskObserveIn) (*client.RiskObserved, error) {
			return &client.RiskObserved{Learned: 0}, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mute(t)
			done := make(chan struct{})
			prior := teach
			teach = func(ctx context.Context, in *client.RiskObserveIn) (*client.RiskObserved, error) {
				defer close(done)
				return tc.call(ctx, in)
			}
			t.Cleanup(func() { teach = prior })

			if code := post(t, endpointApp(t, http.StatusOK, body)); code != http.StatusOK {
				t.Fatalf("a settled top-up answered %d because the risk plane could not be told "+
					"about it", code)
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("the teaching call never ran")
			}
			// AND THE GOROUTINE SURVIVES ITS OWN FAILURE, so the ceiling slot it holds
			// comes back and the next payment is still taught. A leaked slot is the
			// endpoint quietly teaching less and less until it teaches nothing.
			//
			// It is WAITED FOR rather than read once. The slot is released by the
			// goroutine's own outermost defer, which by construction runs AFTER the call
			// returns — so `done` closing says the call finished, never that the goroutine
			// has. Reading the ceiling at that instant tests the scheduler.
			if !released(3 * time.Second) {
				t.Errorf("%d teaching slot(s) were never released — the ceiling leaks and the "+
					"endpoint eventually stops teaching anything", len(teaching))
			}
		})
	}
}
