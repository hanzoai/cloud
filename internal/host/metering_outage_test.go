package host

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	cloud "github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/account"
	luxlog "github.com/luxfi/log"
)

// A METERING OUTAGE REFUSES THE BILLABLE CALL, AND NO SWITCH SERVES IT FREE.
//
// When the ledger cannot say whether the payer can pay, the call is not made. A
// person calling over HTTP is answered 503 with
// {"error":{"code":"balance_unavailable","message":"Billing temporarily unavailable"}}
// and the handler never runs; a program calling a model in this process gets the
// meter's error back and no model is asked. There used to be three ways to turn
// that into a free call — BILLING_FAIL_OPEN, METERING_FAIL_OPEN and
// METERING_DISABLED — and each is set here to prove none is read.
func TestAMeteringOutageRefusesTheBillableCall(t *testing.T) {
	for _, env := range []string{"BILLING_FAIL_OPEN", "METERING_FAIL_OPEN", "METERING_DISABLED"} {
		t.Setenv(env, "true")
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(down.Close)

	cfg := cloud.LoadConfig()
	cfg.CommerceHTTPURL, cfg.Enable = down.URL, []string{"ai"} // a ledger reached over HTTP, and down
	m := buildMeteringClient(cfg, luxlog.NewNoOpLogger())

	var ran atomic.Bool
	resp := doReq(t, newGateApp(t, m, &ran))
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), `"code":"balance_unavailable"`) {
		t.Fatalf("an outage answered %d %s, want 503 balance_unavailable", resp.StatusCode, body)
	}
	if ran.Load() {
		t.Fatal("the handler ran while the ledger could not weigh the call")
	}

	inner := &recordingAI{resp: &infer.ChatResponse{Content: "hi", TotalTokens: 10}}
	ai := cloud.MeteredAIClient(inner, cloud.Deps{Metering: m})
	if _, err := ai.ChatCompletion(context.Background(), &infer.ChatRequest{
		Model: "enso-flash", Prompt: "hello", Org: "acme", Payer: account.Org("acme"),
	}); err == nil {
		t.Fatal("an in-process inference was served while the ledger could not weigh it")
	}
	if inner.chatReq != nil {
		t.Fatal("the model was asked for a call the ledger could not weigh")
	}
}
