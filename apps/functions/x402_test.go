package functions

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// fakeRail stands in for the payment rail the host reaches over the plane
// (internal/host x402fee.go), writing the headers the host writes. A payment
// checks out unless refused is set, and settles unless short is; steps is every
// ask, in order.
type fakeRail struct {
	refused, short bool
	steps          []cloud.FeeStep
}

func rail(t *testing.T) *fakeRail {
	t.Helper()
	r := &fakeRail{}
	cloud.SetHooks(cloud.Hooks{X402Fee: func(c *zip.Ctx, resource string, step cloud.FeeStep) (cloud.FeeAnswer, error) {
		if resource != client.FunctionInvoke.Resource() {
			t.Errorf("the invoke asked the rail about %q, want %q", resource, client.FunctionInvoke.Resource())
		}
		r.steps = append(r.steps, step)
		paying := c.Header(client.HeaderPaymentSignature) != ""
		switch {
		case step == cloud.FeeTerms:
			c.SetHeader(client.HeaderPaymentRequired, "terms")
			return cloud.FeeAnswer{}, nil
		case !paying:
			return cloud.FeeAnswer{}, nil
		case step == cloud.FeeCheck && r.refused:
			c.SetHeader(client.HeaderPaymentRequired, "terms")
			return cloud.FeeAnswer{Status: http.StatusPaymentRequired, Code: "nonce_replayed", Reason: "payment nonce already used"}, nil
		case step == cloud.FeeCheck:
			return cloud.FeeAnswer{OK: true}, nil
		case r.short:
			c.SetHeader(client.HeaderPaymentResponse, "failed")
			return cloud.FeeAnswer{Status: http.StatusPaymentRequired, Code: "insufficient_funds"}, nil
		}
		c.SetHeader(client.HeaderPaymentResponse, "receipt")
		return cloud.FeeAnswer{OK: true}, nil
	}})
	t.Cleanup(func() { cloud.SetHooks(cloud.Hooks{}) })
	return r
}

func (r *fakeRail) count(step cloud.FeeStep) int {
	n := 0
	for _, s := range r.steps {
		if s == step {
			n++
		}
	}
	return n
}

// invokeWith is fireInvoke with a PAYMENT-SIGNATURE on the request when payment
// is set.
func invokeWith(t *testing.T, s *cloud.Service[state], org, name, payment string) (int, []byte, http.Header) {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	g := app.Group("/v1/function")
	g.Use(cloud.DenyEnvelope())
	g.Post("/:name/invoke", ops{s: s}.invoke,
		zip.WithStatus(http.StatusOK, http.StatusBadGateway, http.StatusServiceUnavailable))
	req := httptest.NewRequest("POST", "/v1/function/"+name+"/invoke", bytes.NewReader([]byte(`{"input":"x"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "u_"+org)
	if payment != "" {
		req.Header.Set(client.HeaderPaymentSignature, payment)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test invoke: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

func okSandbox(t *testing.T) *planetest.Sandboxes {
	t.Helper()
	sb := planetest.ServeSandboxes(t)
	sb.Run = func(string, []string) (string, string, int, map[string][]byte) { return "ok", "", 0, nil }
	return sb
}

// An unfunded org's refusal is the balance gate's own 402, byte for byte, with the
// fee's x402 terms on PAYMENT-REQUIRED beside it. Nothing runs and nothing is
// charged either way. The payment is drawn from the same wallet the gate weighs, so
// a valid payment from that org is refused the same way, and nothing is settled.
func TestInvokeRefusalCarriesTheX402Challenge(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		bs := &billServer{available: 0}
		sb := planetest.ServeSandboxes(t)
		s := newBilledService(t, bs.start(t))
		seedFn(t, s, "acme", "resize")

		code0, body0, hdr0 := invokeWith(t, s, "acme", "resize", "")
		if code0 != http.StatusPaymentRequired || hdr0.Get(client.HeaderPaymentRequired) != "" {
			t.Fatalf("with no rail = %d %s, want a bare 402", code0, body0)
		}

		r := rail(t)
		code, body, hdr := invokeWith(t, s, "acme", "resize", "")
		if code != code0 || !bytes.Equal(body, body0) {
			t.Fatalf("the refusal changed with a rail:\n %d %s\nwas %d %s", code, body, code0, body0)
		}
		if !bytes.Contains(body, []byte(`"code":"insufficient_balance"`)) || !bytes.Contains(body, []byte(`"billingUrl"`)) {
			t.Fatalf("the refusal is not the money wire's: %s", body)
		}
		if hdr.Get(client.HeaderPaymentRequired) != "terms" || r.count(cloud.FeeTerms) != 1 {
			t.Fatalf("PAYMENT-REQUIRED = %q after steps %v, want the terms once", hdr.Get(client.HeaderPaymentRequired), r.steps)
		}

		code, body, hdr = invokeWith(t, s, "acme", "resize", "signed")
		if code != code0 || !bytes.Equal(body, body0) || hdr.Get(client.HeaderPaymentRequired) != "terms" || r.count(cloud.FeeSettle) != 0 {
			t.Fatalf("a payment from an empty wallet = %d %s after %v, want the same refusal and nothing settled", code, body, r.steps)
		}
		if sb.Ran() != 0 || bs.debits() != 0 {
			t.Fatalf("a refused invoke ran %d and crossed %d debits, want none", sb.Ran(), bs.debits())
		}
	})
}

// A funded org is served on its balance as before, and is not challenged.
func TestInvokeFundedIsServedWithoutAChallenge(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		bs := &billServer{available: 100000}
		okSandbox(t)
		s := newBilledService(t, bs.start(t))
		seedFn(t, s, "acme", "resize")
		r := rail(t)

		code, body, hdr := invokeWith(t, s, "acme", "resize", "")
		if code != http.StatusOK || hdr.Get(client.HeaderPaymentRequired) != "" || r.count(cloud.FeeTerms) != 0 {
			t.Fatalf("a funded invoke = %d %s, challenge %q", code, body, hdr.Get(client.HeaderPaymentRequired))
		}
		if !waitFor(func() bool { return bs.peer.Charged() == 1 }) {
			t.Fatalf("charged = %d, want the invoke fee once from the balance", bs.peer.Charged())
		}
	})
}

// A payment is the fee. It is checked before the invoke and settled after it, its
// receipt rides PAYMENT-RESPONSE, and the balance is not charged for the fee: the
// invoke is counted at $0.
func TestInvokePaidOverX402(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		bs := &billServer{available: 100000}
		sb := okSandbox(t)
		s := newBilledService(t, bs.start(t))
		seedFn(t, s, "acme", "resize")
		r := rail(t)

		code, body, hdr := invokeWith(t, s, "acme", "resize", "signed")
		if code != http.StatusOK || hdr.Get(client.HeaderPaymentResponse) != "receipt" {
			t.Fatalf("a paid invoke = %d %s, receipt %q", code, body, hdr.Get(client.HeaderPaymentResponse))
		}
		if len(r.steps) != 2 || r.steps[0] != cloud.FeeCheck || r.steps[1] != cloud.FeeSettle || sb.Ran() != 1 {
			t.Fatalf("steps %v with %d runs, want a check, the run, then a settle", r.steps, sb.Ran())
		}
		if !waitFor(func() bool { return bs.debits() == 2 }) {
			t.Fatalf("crossings = %d, want 2 (the invoke and its GB-seconds, counted)", bs.debits())
		}
		if n := bs.peer.Charged(); n != 0 {
			t.Fatalf("charged = %d, want 0: the payment was the fee", n)
		}
	})
}

// A payment that does not check out is refused before anything runs, in the
// rail's own code. One that checks out and then does not settle leaves the fee to
// the balance, so the invoke is never free.
func TestInvokeX402PaymentFailures(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		bs := &billServer{available: 100000}
		sb := okSandbox(t)
		s := newBilledService(t, bs.start(t))
		seedFn(t, s, "acme", "resize")
		r := rail(t)

		r.refused = true
		code, body, hdr := invokeWith(t, s, "acme", "resize", "signed")
		if code != http.StatusPaymentRequired || !bytes.Contains(body, []byte(`"code":"nonce_replayed"`)) || hdr.Get(client.HeaderPaymentRequired) != "terms" {
			t.Fatalf("a refused payment = %d %s, challenge %q", code, body, hdr.Get(client.HeaderPaymentRequired))
		}
		if sb.Ran() != 0 || bs.debits() != 0 {
			t.Fatalf("a refused payment ran %d and crossed %d, want none", sb.Ran(), bs.debits())
		}

		r.refused, r.short = false, true
		code, body, hdr = invokeWith(t, s, "acme", "resize", "signed")
		if code != http.StatusOK || hdr.Get(client.HeaderPaymentResponse) != "failed" {
			t.Fatalf("a payment that did not settle = %d %s, response %q", code, body, hdr.Get(client.HeaderPaymentResponse))
		}
		if !waitFor(func() bool { return bs.peer.Charged() == 1 }) {
			t.Fatalf("charged = %d, want the fee from the balance once", bs.peer.Charged())
		}
	})
}
