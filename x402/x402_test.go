// SPDX-License-Identifier: Apache-2.0 OR MIT

package x402

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/zap-proto/zip"
)

func TestMain(m *testing.M) {
	_ = os.Setenv("CLOUD_DEV_UNENCRYPTED", "1")
	os.Exit(m.Run())
}

type testHarness struct {
	app        *zip.App
	gw         *ProviderGateway
	facDirect  *IndependentERC3009Facilitator
	facCircle  *IndependentERC3009Facilitator
	payerPriv  ed25519.PrivateKey
	payerAddr  string
	nowUnix    int64
	failNext   atomic.Bool
	inferCalls atomic.Int64
}

func newHarness(t *testing.T) *testHarness {
	t.Helper()
	_, privProv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen provider key: %v", err)
	}
	pubPayer, privPayer, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen payer key: %v", err)
	}

	h := &testHarness{
		payerPriv: privPayer,
		payerAddr: DeriveWalletAddress(pubPayer),
		nowUnix:   1_763_460_000,
	}
	nowFn := func() int64 { return h.nowUnix }
	h.facDirect = NewIndependentERC3009Facilitator("direct-erc3009", nowFn)
	h.facCircle = NewIndependentERC3009Facilitator("circle-facilitator-opt", nowFn)

	gw, err := OpenGateway(GatewayConfig{
		DataDir:      t.TempDir(),
		ProviderID:   "provider-node-alpha",
		PayToWallet:  "0x9999999999999999999999999999999999999999",
		Asset:        "USDC",
		ChainID:      "eip155:8453",
		QuoteTTLSec:  300,
		ProviderPriv: privProv,
		Facilitators: []Facilitator{h.facDirect, h.facCircle},
		PriceFn: func(_ string, _ []byte) (uint64, uint64) {
			return 2500, 1250 // $0.0025 USDC exact quote
		},
		NowUnix: nowFn,
	})
	if err != nil {
		t.Fatalf("OpenGateway: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close() })
	h.gw = gw

	app := zip.New(zip.Config{AppName: "x402-test", DisableStartupMessage: true})
	if err := gw.RegisterOps(app); err != nil {
		t.Fatalf("RegisterOps: %v", err)
	}
	app.Group("/v1/x402", gw.PaymentGate()).Post("/inference", func(c *zip.Ctx) error {
		n := h.inferCalls.Add(1)
		if h.failNext.Swap(false) {
			return zip.Errorf(500, "simulated model worker crash")
		}
		return c.JSON(200, map[string]any{
			"completion": "verified wallet-native answer",
			"call_seq":   n,
		})
	})
	h.app = app
	return h
}

func decodeReceiptHeader(t *testing.T, hdr string) Receipt {
	t.Helper()
	if hdr == "" {
		t.Fatalf("missing %s header", HeaderReceipt)
	}
	raw, err := base64.StdEncoding.DecodeString(hdr)
	if err != nil {
		t.Fatalf("decode receipt base64: %v", err)
	}
	var r Receipt
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("unmarshal receipt JSON: %v", err)
	}
	if err := VerifyReceiptSignature(r); err != nil {
		t.Fatalf("VerifyReceiptSignature: %v", err)
	}
	return r
}

func TestEndToEndTwoIndependentFacilitators(t *testing.T) {
	h := newHarness(t)
	bodyBytes := []byte(`{"model":"zen-coder","messages":[{"role":"user","content":"hello x402"}]}`)

	for idx, facID := range []string{"direct-erc3009", "circle-facilitator-opt"} {
		// Step 1: Unauthenticated agent calls inference endpoint -> receives HTTP 402 + signed Quote.
		req402 := httptest.NewRequest(http.MethodPost, "/v1/x402/inference", bytes.NewReader(bodyBytes))
		req402.Header.Set("Content-Type", "application/json")
		res402, err := h.app.Fiber().Test(req402)
		if err != nil {
			t.Fatalf("[%s] 402 challenge transport err: %v", facID, err)
		}
		b402, _ := io.ReadAll(res402.Body)
		_ = res402.Body.Close()

		if res402.StatusCode != http.StatusPaymentRequired {
			t.Fatalf("[%s] status = %d, want 402 (%s)", facID, res402.StatusCode, string(b402))
		}
		var challenge PaymentRequiredResponse
		if err := json.Unmarshal(b402, &challenge); err != nil {
			t.Fatalf("[%s] decode 402 JSON: %v", facID, err)
		}
		if err := VerifyQuoteSignature(challenge.Quote, nil); err != nil {
			t.Fatalf("[%s] quote signature invalid: %v", facID, err)
		}

		// Step 2: Agent signs ERC-3009 authorization for the chosen facilitator and gets inference.
		auth := PaymentAuthorization{
			Version:         ProtocolVersion,
			Quote:           challenge.Quote,
			FacilitatorID:   facID,
			From:            h.payerAddr,
			To:              challenge.Quote.PayTo,
			Asset:           challenge.Quote.Asset,
			ChainID:         challenge.Quote.ChainID,
			ValueAtomic:     challenge.Quote.ExactAmountAtomic,
			ValidAfterUnix:  h.nowUnix - 5,
			ValidBeforeUnix: h.nowUnix + 120,
			Nonce:           "0x0000000000000000000000000000000" + string(rune('1'+idx)),
			IdempotencyKey:  "idem-fac-" + facID,
			RequestBodyHash: HashRequestBody(bodyBytes),
		}
		SignAuthorization(&auth, h.payerPriv)
		hdr, err := EncodePaymentHeader(auth)
		if err != nil {
			t.Fatalf("EncodePaymentHeader: %v", err)
		}

		reqPay := httptest.NewRequest(http.MethodPost, "/v1/x402/inference", bytes.NewReader(bodyBytes))
		reqPay.Header.Set("Content-Type", "application/json")
		reqPay.Header.Set(HeaderPayment, hdr)
		resPay, err := h.app.Fiber().Test(reqPay)
		if err != nil {
			t.Fatalf("[%s] paid request err: %v", facID, err)
		}
		bPay, _ := io.ReadAll(resPay.Body)
		_ = resPay.Body.Close()

		if resPay.StatusCode != http.StatusOK {
			t.Fatalf("[%s] paid status = %d, want 200 (%s)", facID, resPay.StatusCode, string(bPay))
		}
		rcpt := decodeReceiptHeader(t, resPay.Header.Get(HeaderReceipt))
		if rcpt.FacilitatorID != facID || rcpt.Status != StatusSettled || rcpt.AmountAtomic != 2500 || rcpt.TxHash == "" {
			t.Fatalf("[%s] unexpected settled receipt: %+v", facID, rcpt)
		}
	}
}

func TestDuplicateIdempotencyAndReplayedSignatureRejection(t *testing.T) {
	h := newHarness(t)
	body := []byte(`{"model":"zen-coder","prompt":"idempotent call"}`)
	quote := h.gw.IssueQuote("/v1/x402/inference", body)

	auth := PaymentAuthorization{
		Version:         ProtocolVersion,
		Quote:           quote,
		FacilitatorID:   "direct-erc3009",
		From:            h.payerAddr,
		To:              quote.PayTo,
		Asset:           quote.Asset,
		ChainID:         quote.ChainID,
		ValueAtomic:     quote.ExactAmountAtomic,
		ValidAfterUnix:  h.nowUnix - 5,
		ValidBeforeUnix: h.nowUnix + 120,
		Nonce:           "0xfeedfacefeedfacefeedface00000001",
		IdempotencyKey:  "idem-dup-check-1",
		RequestBodyHash: HashRequestBody(body),
	}
	SignAuthorization(&auth, h.payerPriv)
	hdr, _ := EncodePaymentHeader(auth)

	// First request runs inference once and settles once.
	req1 := httptest.NewRequest(http.MethodPost, "/v1/x402/inference", bytes.NewReader(body))
	req1.Header.Set(HeaderPayment, hdr)
	res1, _ := h.app.Fiber().Test(req1)
	b1, _ := io.ReadAll(res1.Body)
	_ = res1.Body.Close()
	if res1.StatusCode != http.StatusOK {
		t.Fatalf("first call status = %d (%s)", res1.StatusCode, string(b1))
	}
	rcpt1 := decodeReceiptHeader(t, res1.Header.Get(HeaderReceipt))

	// Exact duplicate retry with same IdempotencyKey + Nonce returns identical body + receipt
	// WITHOUT invoking downstream inference a second time or double-charging.
	callsBeforeDup := h.inferCalls.Load()
	reqDup := httptest.NewRequest(http.MethodPost, "/v1/x402/inference", bytes.NewReader(body))
	reqDup.Header.Set(HeaderPayment, hdr)
	resDup, _ := h.app.Fiber().Test(reqDup)
	bDup, _ := io.ReadAll(resDup.Body)
	_ = resDup.Body.Close()
	if resDup.StatusCode != http.StatusOK || string(bDup) != string(b1) {
		t.Fatalf("duplicate response mismatch: status=%d got=%s want=%s", resDup.StatusCode, string(bDup), string(b1))
	}
	if h.inferCalls.Load() != callsBeforeDup {
		t.Fatalf("duplicate request re-executed inference handler!")
	}
	rcptDup := decodeReceiptHeader(t, resDup.Header.Get(HeaderReceipt))
	if rcptDup.TxHash != rcpt1.TxHash {
		t.Fatalf("duplicate tx_hash mismatch: %s vs %s", rcptDup.TxHash, rcpt1.TxHash)
	}

	// Replayed signature / reused ERC-3009 nonce on a different request or new IdempotencyKey
	// MUST be rejected with HTTP 409 Conflict before inference executes.
	body2 := []byte(`{"model":"zen-coder","prompt":"different prompt trying to reuse nonce"}`)
	quote2 := h.gw.IssueQuote("/v1/x402/inference", body2)
	replayAuth := auth
	replayAuth.Quote = quote2
	replayAuth.IdempotencyKey = "idem-different-request"
	replayAuth.RequestBodyHash = HashRequestBody(body2)
	SignAuthorization(&replayAuth, h.payerPriv)
	hdrReplay, _ := EncodePaymentHeader(replayAuth)

	callsBeforeReplay := h.inferCalls.Load()
	reqReplay := httptest.NewRequest(http.MethodPost, "/v1/x402/inference", bytes.NewReader(body2))
	reqReplay.Header.Set(HeaderPayment, hdrReplay)
	resReplay, _ := h.app.Fiber().Test(reqReplay)
	bReplay, _ := io.ReadAll(resReplay.Body)
	_ = resReplay.Body.Close()

	if resReplay.StatusCode != http.StatusConflict {
		t.Fatalf("replayed nonce status = %d, want 409 (%s)", resReplay.StatusCode, string(bReplay))
	}
	if h.inferCalls.Load() != callsBeforeReplay {
		t.Fatalf("replayed nonce executed downstream inference!")
	}
}

func TestFailedInferenceReleasesHoldAndSettlementReversalReconciles(t *testing.T) {
	h := newHarness(t)
	body := []byte(`{"model":"zen-coder","prompt":"handle failures and reversals"}`)

	// Part 1: Downstream inference fails (HTTP 500) -> facilitator hold is released,
	// customer is NOT settled, and ledger records status=inference_failed.
	h.failNext.Store(true)
	qFail := h.gw.IssueQuote("/v1/x402/inference", body)
	authFail := PaymentAuthorization{
		Version:         ProtocolVersion,
		Quote:           qFail,
		FacilitatorID:   "direct-erc3009",
		From:            h.payerAddr,
		To:              qFail.PayTo,
		Asset:           qFail.Asset,
		ChainID:         qFail.ChainID,
		ValueAtomic:     qFail.ExactAmountAtomic,
		ValidAfterUnix:  h.nowUnix - 5,
		ValidBeforeUnix: h.nowUnix + 120,
		Nonce:           "0xdeadbeefdeadbeefdeadbeef00000009",
		IdempotencyKey:  "idem-infer-fail",
		RequestBodyHash: HashRequestBody(body),
	}
	SignAuthorization(&authFail, h.payerPriv)
	hdrFail, _ := EncodePaymentHeader(authFail)

	reqFail := httptest.NewRequest(http.MethodPost, "/v1/x402/inference", bytes.NewReader(body))
	reqFail.Header.Set(HeaderPayment, hdrFail)
	resFail, _ := h.app.Fiber().Test(reqFail)
	_ = resFail.Body.Close()
	if resFail.StatusCode != http.StatusInternalServerError {
		t.Fatalf("failed inference status = %d, want 500", resFail.StatusCode)
	}
	if resFail.Header.Get(HeaderReceipt) != "" {
		t.Fatalf("failed inference must NOT emit a settled receipt header")
	}

	// Part 2: Succeed on a new request, then simulate an on-chain settlement reversal
	// and reconcile via typed POST /v1/x402/reconcile + export via GET /v1/x402/receipts.
	qOk := h.gw.IssueQuote("/v1/x402/inference", body)
	authOk := authFail
	authOk.Quote = qOk
	authOk.Nonce = "0x1234567890abcdef1234567890abcdef"
	authOk.IdempotencyKey = "idem-reversal-target"
	SignAuthorization(&authOk, h.payerPriv)
	hdrOk, _ := EncodePaymentHeader(authOk)

	reqOk := httptest.NewRequest(http.MethodPost, "/v1/x402/inference", bytes.NewReader(body))
	reqOk.Header.Set(HeaderPayment, hdrOk)
	resOk, _ := h.app.Fiber().Test(reqOk)
	_ = resOk.Body.Close()
	if resOk.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK before reversal, got %d", resOk.StatusCode)
	}
	settledRcpt := decodeReceiptHeader(t, resOk.Header.Get(HeaderReceipt))

	// Inject chain reorg / settlement reversal on facilitator and call typed POST /v1/x402/reconcile.
	h.facDirect.SimulateChainReversal(settledRcpt.TxHash, "chain_reorg_tx_reverted")

	recReq := httptest.NewRequest(http.MethodPost, "/v1/x402/reconcile", bytes.NewReader([]byte(`{}`)))
	recReq.Header.Set("Content-Type", "application/json")
	recRes, err := h.app.Fiber().Test(recReq)
	if err != nil {
		t.Fatalf("reconcile op err: %v", err)
	}
	recBytes, _ := io.ReadAll(recRes.Body)
	_ = recRes.Body.Close()
	if recRes.StatusCode != http.StatusOK {
		t.Fatalf("reconcile status = %d (%s)", recRes.StatusCode, string(recBytes))
	}
	var recOut ReconcileOut
	if err := json.Unmarshal(recBytes, &recOut); err != nil {
		t.Fatalf("unmarshal reconcile out: %v", err)
	}
	if recOut.CheckedCount != 1 || recOut.ReversedCount != 1 || recOut.Reversed[0].Status != StatusReversed {
		t.Fatalf("unexpected reconcile result: %+v", recOut)
	}

	// Verify sovereign receipt export via typed GET /v1/x402/receipts?status=reversed.
	expReq := httptest.NewRequest(http.MethodGet, "/v1/x402/receipts?status=reversed", nil)
	expRes, _ := h.app.Fiber().Test(expReq)
	expBytes, _ := io.ReadAll(expRes.Body)
	_ = expRes.Body.Close()
	var expOut ListReceiptsOut
	if err := json.Unmarshal(expBytes, &expOut); err != nil {
		t.Fatalf("unmarshal export: %v", err)
	}
	if len(expOut.Receipts) != 1 || expOut.Receipts[0].DisputeReason != "chain_reorg_tx_reverted" {
		t.Fatalf("unexpected exported reversed receipts: %+v", expOut)
	}
	if err := VerifyReceiptSignature(expOut.Receipts[0]); err != nil {
		t.Fatalf("exported reversed receipt failed standalone verification: %v", err)
	}
}

func TestHIP1350FiveAxisBenchmark(t *testing.T) {
	report, err := RunFiveAxisEvaluation(t.TempDir(), 120)
	if err != nil {
		t.Fatalf("RunFiveAxisEvaluation: %v", err)
	}
	t.Logf("HIP-1350 5-Axis Report: %+v", report)

	if report.ValidCompletionRate != 1.0 {
		t.Fatalf("valid completion rate = %v, want 1.0", report.ValidCompletionRate)
	}
	if report.FraudAndReplayDetectionRate != 1.0 {
		t.Fatalf("fraud/replay detection rate = %v, want 1.0", report.FraudAndReplayDetectionRate)
	}
	if report.HanzoIndependentCompletionShare != 1.0 {
		t.Fatalf("decentralization share = %v, want 1.0", report.HanzoIndependentCompletionShare)
	}
	if report.SovereignExportVerificationRatio != 1.0 {
		t.Fatalf("sovereign export verification = %v, want 1.0", report.SovereignExportVerificationRatio)
	}
}
