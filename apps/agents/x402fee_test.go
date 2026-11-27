package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// A run fee's payment pays for the one run that takes it. Everything the run
// starts inherits its context — an agent it calls as a tool, a group's members —
// and each of those finds the ticket taken and bills its own fee.
func TestAFeePaymentPaysForOneRun(t *testing.T) {
	if takeFeePayment(context.Background()) != nil {
		t.Fatal("a context with no ticket gave a payment")
	}
	pay := &cloud.FeePayment{}
	ctx := withFeePayment(context.Background(), pay)
	if takeFeePayment(ctx) != pay {
		t.Fatal("the run that holds the ticket could not take it")
	}
	nested, cancel := context.WithCancel(ctx)
	defer cancel()
	if takeFeePayment(nested) != nil || takeFeePayment(ctx) != nil {
		t.Fatal("a taken ticket paid for a second run")
	}
}

// runRail stands in for the payment rail the host reaches over the plane: a
// payment checks out and settles, and the terms are written when asked. steps is
// every ask, in order.
func runRail(t *testing.T) *[]cloud.FeeStep {
	t.Helper()
	var steps []cloud.FeeStep
	cloud.SetHooks(cloud.Hooks{X402Fee: func(c *zip.Ctx, resource string, step cloud.FeeStep) (cloud.FeeAnswer, error) {
		if resource != client.AgentRun.Resource() {
			t.Errorf("the run asked the rail about %q, want %q", resource, client.AgentRun.Resource())
		}
		steps = append(steps, step)
		switch {
		case step == cloud.FeeTerms:
			c.SetHeader(client.HeaderPaymentRequired, "terms")
			return cloud.FeeAnswer{}, nil
		case c.Header(client.HeaderPaymentSignature) == "":
			return cloud.FeeAnswer{}, nil
		case step == cloud.FeeSettle:
			c.SetHeader(client.HeaderPaymentResponse, "receipt")
		}
		return cloud.FeeAnswer{OK: true}, nil
	}})
	t.Cleanup(func() { cloud.SetHooks(cloud.Hooks{}) })
	return &steps
}

func runWith(t *testing.T, app *zip.App, payment string) (int, []byte, http.Header) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"input": "hi"})
	req := httptest.NewRequest(http.MethodPost, "/v1/agent/a/run", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", "acme")
	req.Header.Set("X-User-Id", "u-acme")
	if payment != "" {
		req.Header.Set(client.HeaderPaymentSignature, payment)
	}
	resp, err := app.Test(req, deadline)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body, resp.Header
}

func createAgent(t *testing.T, app *zip.App) {
	t.Helper()
	if code, body := do(t, app, http.MethodPost, "/v1/agent", "acme",
		map[string]any{"name": "a", "model": "m", "instructions": "x", "cap_micro_usd": 1000000, "max_task_micro_usd": 100000, "period": "month"}); code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
}

// An unfunded run is refused by the fee gate with its own 402, byte for byte, and
// the run fee's x402 terms on PAYMENT-REQUIRED beside it.
func TestRunRefusalCarriesTheX402Challenge(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		bs := &billServer{available: 0}
		app := mountBilled(t, bs.start(t), &fakeAI{content: "should not run"})
		createAgent(t, app)

		code0, body0, hdr0 := runWith(t, app, "")
		if code0 != http.StatusPaymentRequired || hdr0.Get(client.HeaderPaymentRequired) != "" {
			t.Fatalf("with no rail = %d %s, want a bare 402", code0, body0)
		}
		steps := runRail(t)
		code, body, hdr := runWith(t, app, "")
		if code != code0 || !bytes.Equal(body, body0) {
			t.Fatalf("the refusal changed with a rail:\n %d %s\nwas %d %s", code, body, code0, body0)
		}
		if hdr.Get(client.HeaderPaymentRequired) != "terms" || len(*steps) != 2 || (*steps)[1] != cloud.FeeTerms {
			t.Fatalf("PAYMENT-REQUIRED = %q after %v, want the terms once", hdr.Get(client.HeaderPaymentRequired), *steps)
		}
	})
}

// A paid run is settled once it has succeeded, its receipt rides PAYMENT-RESPONSE,
// and the fee is not taken from the balance again. A run the model fails is not
// billed either way: its payment is never settled.
func TestRunFeeOverX402(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		bs := &billServer{available: 100000}
		ai := &fakeAI{content: "the answer"}
		app := mountBilled(t, bs.start(t), ai)
		createAgent(t, app)
		steps := runRail(t)

		code, body, hdr := runWith(t, app, "signed")
		if code != http.StatusOK || hdr.Get(client.HeaderPaymentResponse) != "receipt" {
			t.Fatalf("a paid run = %d %s, receipt %q", code, body, hdr.Get(client.HeaderPaymentResponse))
		}
		if len(*steps) != 2 || (*steps)[0] != cloud.FeeCheck || (*steps)[1] != cloud.FeeSettle {
			t.Fatalf("steps %v, want a check then a settle", *steps)
		}
		if !waitForDebit(func() bool { return bs.debits() == 1 }) {
			t.Fatalf("the paid run was not counted")
		}
		if n := bs.peer.Charged(); n != 0 {
			t.Fatalf("charged = %d, want 0: the payment was the run fee", n)
		}

		*steps = nil
		ai.err = errTest
		code, body, hdr = runWith(t, app, "signed")
		if code != http.StatusBadGateway || hdr.Get(client.HeaderPaymentResponse) != "" {
			t.Fatalf("a failed run = %d %s, receipt %q, want 502 with nothing settled", code, body, hdr.Get(client.HeaderPaymentResponse))
		}
		if len(*steps) != 1 || (*steps)[0] != cloud.FeeCheck {
			t.Fatalf("steps %v for a failed run, want the check alone", *steps)
		}
	})
}
