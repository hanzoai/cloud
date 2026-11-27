package host

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	cloud "github.com/hanzoai/cloud/sdk"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/zap-proto/zip"
)

// fakeRail serves x402_settle as app "x402" on this test's plane socket and
// records what crossed: the org the caller acted for, and the settle input.
type fakeRail struct {
	mu      sync.Mutex
	answer  client.Settled
	checked client.Authorization
	calls   []client.SettleIn // a check is recorded as its resource and payment
	ops     []string
	orgs    []string
}

func serveRail(t *testing.T, answer client.Settled) *fakeRail {
	t.Helper()
	r := &fakeRail{answer: answer}
	app := zip.New(zip.Config{AppName: "x402"})
	app.Post("/x402/settle", func(ctx context.Context, in *client.SettleIn) (*client.Settled, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.calls = append(r.calls, *in)
		r.ops = append(r.ops, client.X402Settle)
		r.orgs = append(r.orgs, zip.CallerOf(ctx).Org)
		out := r.answer
		return &out, nil
	}, zip.WithOperationID(client.X402Settle))
	app.Post("/x402/authorize", func(ctx context.Context, in *client.AuthorizationIn) (*client.Authorization, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if in.Hold {
			t.Errorf("a metered fee's check held its payment")
		}
		r.calls = append(r.calls, client.SettleIn{Resource: in.Resource, Payment: in.Payment})
		r.ops = append(r.ops, client.X402Authorize)
		r.orgs = append(r.orgs, zip.CallerOf(ctx).Org)
		out := r.checked
		return &out, nil
	}, zip.WithOperationID(client.X402Authorize))
	planetest.Bind(t, "x402", app)
	return r
}

func (r *fakeRail) seen() ([]client.SettleIn, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]client.SettleIn(nil), r.calls...), append([]string(nil), r.orgs...)
}

func (r *fakeRail) asked() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ops...)
}

// askRail drives x402Fee on one request: org (non-empty) is the validated
// principal, payment (non-empty) the PAYMENT-SIGNATURE it carries.
func askRail(t *testing.T, org, payment string, step cloud.FeeStep) (cloud.FeeAnswer, http.Header) {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	app.Raw(http.MethodPost, "/act", func(c *zip.Ctx) error {
		out, err := x402Fee(c, client.FunctionInvoke.Resource(), step)
		if err != nil {
			return c.JSON(http.StatusBadGateway, map[string]any{"err": err.Error()})
		}
		return c.JSON(http.StatusOK, out)
	})
	req := httptest.NewRequest(http.MethodPost, "/act", nil)
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org)
	}
	if payment != "" {
		req.Header.Set(client.HeaderPaymentSignature, payment)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("x402Fee failed: %d %s", resp.StatusCode, b)
	}
	var out cloud.FeeAnswer
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return out, resp.Header
}

// A payment is checked before the act, holding nothing, and settled after it, for
// the caller's own books; what the rail answered rides back on the response: the
// challenge when the check refused, the settlement on PAYMENT-RESPONSE either way
// it went.
func TestX402FeeChecksThenSettlesForTheCallersBooks(t *testing.T) {
	r := serveRail(t, client.Settled{OK: true, Response: "receipt"})
	r.checked = client.Authorization{OK: true, Challenge: "terms"}
	out, hdr := askRail(t, "acme", "signed", cloud.FeeCheck)
	if !out.OK || hdr.Get(client.HeaderPaymentRequired) != "" || hdr.Get(client.HeaderPaymentResponse) != "" {
		t.Fatalf("a payment that checks out = %+v, headers %v, want OK and nothing written", out, hdr)
	}
	out, hdr = askRail(t, "acme", "signed", cloud.FeeSettle)
	if !out.OK || hdr.Get(client.HeaderPaymentResponse) != "receipt" || hdr.Get(client.HeaderPaymentRequired) != "" {
		t.Fatalf("a settled payment = %+v, headers %v", out, hdr)
	}
	calls, orgs := r.seen()
	if ops := r.asked(); len(ops) != 2 || ops[0] != client.X402Authorize || ops[1] != client.X402Settle {
		t.Fatalf("the rail was asked %v, want authorize then settle", ops)
	}
	for i, call := range calls {
		if call.Payment != "signed" || call.Resource != client.FunctionInvoke.Resource() || orgs[i] != "acme" {
			t.Fatalf("the rail saw %+v for %v, want the payment for acme", calls, orgs)
		}
	}

	r.checked = client.Authorization{Status: http.StatusPaymentRequired, Code: "nonce_replayed", Reason: "used", Challenge: "terms"}
	out, hdr = askRail(t, "acme", "signed", cloud.FeeCheck)
	if out.OK || out.Status != http.StatusPaymentRequired || out.Code != "nonce_replayed" || hdr.Get(client.HeaderPaymentRequired) != "terms" {
		t.Fatalf("a refused check = %+v, headers %v, want 402 with the challenge", out, hdr)
	}

	r.answer = client.Settled{Status: http.StatusPaymentRequired, Code: "insufficient_funds", Challenge: "terms", Response: "failed"}
	out, hdr = askRail(t, "acme", "signed", cloud.FeeSettle)
	if out.OK || out.Code != "insufficient_funds" || hdr.Get(client.HeaderPaymentResponse) != "failed" || hdr.Get(client.HeaderPaymentRequired) != "" {
		t.Fatalf("a settle that failed = %+v, headers %v, want the failed settlement and no challenge on a served act", out, hdr)
	}
}

// Asked for the terms alone, the rail's challenge rides the response and nothing
// else does: no payment was presented, so there is no settlement to report, and
// the answer is no answer, leaving the route's refusal as it was.
func TestX402FeeChallengeIsTheTermsAlone(t *testing.T) {
	r := serveRail(t, client.Settled{Status: http.StatusPaymentRequired, Code: "payment_required", Challenge: "terms", Response: "failed"})
	out, hdr := askRail(t, "acme", "", cloud.FeeTerms)
	if out != (cloud.FeeAnswer{}) || hdr.Get(client.HeaderPaymentRequired) != "terms" || hdr.Get(client.HeaderPaymentResponse) != "" {
		t.Fatalf("a challenge = %+v, headers %v, want the terms header alone", out, hdr)
	}
	if calls, _ := r.seen(); len(calls) != 1 || calls[0].Payment != "" {
		t.Fatalf("the rail saw %+v, want one ask with no payment", calls)
	}
}

// No answer, and the route bills as it always has: nothing to check or settle, a
// caller with no books, and a fee the rail does not sell — which the check answers
// 409 and the settle answers free.
func TestX402FeeNoAnswer(t *testing.T) {
	r := serveRail(t, client.Settled{OK: true, Free: true})
	r.checked = client.Authorization{Status: http.StatusConflict, Code: "unpriced"}
	if out, hdr := askRail(t, "acme", "signed", cloud.FeeCheck); out != (cloud.FeeAnswer{}) || hdr.Get(client.HeaderPaymentRequired) != "" {
		t.Fatalf("a check of a fee not sold = %+v %v, want no answer", out, hdr)
	}
	if out, hdr := askRail(t, "acme", "signed", cloud.FeeSettle); out != (cloud.FeeAnswer{}) || hdr.Get(client.HeaderPaymentResponse) != "" {
		t.Fatalf("a settle of a fee not sold = %+v %v, want no answer", out, hdr)
	}
	if out, _ := askRail(t, "acme", "", cloud.FeeCheck); out != (cloud.FeeAnswer{}) {
		t.Fatalf("no payment = %+v, want no answer", out)
	}
	if out, _ := askRail(t, "", "signed", cloud.FeeCheck); out != (cloud.FeeAnswer{}) {
		t.Fatalf("no books = %+v, want no answer", out)
	}
	if ops := r.asked(); len(ops) != 2 {
		t.Fatalf("the rail was asked %v, want only the two with books and a payment", ops)
	}
}

func TestX402FeeNoRailInThisFleet(t *testing.T) {
	// A plane with no x402 bound: plane.Call answers ErrNoPeer.
	planetest.Bind(t, "unrelated", zip.New(zip.Config{AppName: "unrelated"}))
	for _, step := range []cloud.FeeStep{cloud.FeeTerms, cloud.FeeCheck, cloud.FeeSettle} {
		if out, _ := askRail(t, "acme", "signed", step); out != (cloud.FeeAnswer{}) {
			t.Fatalf("step %d with no rail = %+v, want no answer", step, out)
		}
	}
}
