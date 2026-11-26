// SPDX-License-Identifier: Apache-2.0 OR MIT

package x402

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	protocol "github.com/hanzoai/commerce/payment/x402"
	"github.com/luxfi/crypto"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func ephemeralKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate secp256k1 key: %v", err)
	}
	return k
}

func testAsset(payTo string) protocol.Asset {
	return protocol.Asset{
		Network:           "eip155:84532",
		Address:           "0x00000000000000000000000000000000000a11c3",
		Name:              "USD Coin",
		Version:           "2",
		Decimals:          6,
		PayTo:             payTo,
		MaxTimeoutSeconds: 120,
	}
}

func TestERC3009StandardTypeHashesAndEIP712RoundTrip(t *testing.T) {
	// Pin standard ERC-3009 TransferWithAuthorization and EIP-712 Domain typehashes.
	wantTransferTypeHash := "7c7c6cdb67a18743f49ec6fa9b35f50d52ed05cbed4cc592e13b44501c1a2267"
	gotTransferTypeHash := hex.EncodeToString(crypto.Keccak256([]byte(
		"TransferWithAuthorization(address from,address to,uint256 value,uint256 validAfter,uint256 validBefore,bytes32 nonce)",
	)))
	if gotTransferTypeHash != wantTransferTypeHash {
		t.Fatalf("ERC-3009 TransferWithAuthorization typehash = %s, want %s", gotTransferTypeHash, wantTransferTypeHash)
	}
	wantDomainTypeHash := "8b73c3c69bb8fe3d512ecc4cf759cc79239f7b179b0ffacaa9a75d522b39400f"
	gotDomainTypeHash := hex.EncodeToString(crypto.Keccak256([]byte(
		"EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)",
	)))
	if gotDomainTypeHash != wantDomainTypeHash {
		t.Fatalf("EIP712Domain typehash = %s, want %s", gotDomainTypeHash, wantDomainTypeHash)
	}

	payerKey := ephemeralKey(t)
	providerKey := ephemeralKey(t)
	payTo := strings.ToLower(crypto.PubkeyToAddress(providerKey.PublicKey).Hex())

	req := protocol.PaymentRequirements{
		Scheme:            protocol.SchemeExact,
		Network:           "eip155:8453",
		Amount:            "25000",
		Asset:             "0x00000000000000000000000000000000000a11c3",
		PayTo:             payTo,
		MaxTimeoutSeconds: 60,
		Extra: &protocol.Extra{
			AssetTransferMethod: protocol.TransferEIP3009,
			Name:                "USD Coin",
			Version:             "2",
		},
	}

	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1760000000, 0).UTC()
	pay, err := protocol.Sign(req, payerKey, nonce, uint64(now.Unix()-10), uint64(now.Unix()+120))
	if err != nil {
		t.Fatalf("protocol.Sign failed: %v", err)
	}
	if err := protocol.Verify(req, pay); err != nil {
		t.Fatalf("protocol.Verify failed on valid signature: %v", err)
	}
	if err := protocol.InWindow(pay.Payload.Authorization, now); err != nil {
		t.Fatalf("protocol.InWindow failed: %v", err)
	}

	// Mutating value, recipient, or expiry must fail verification.
	badVal := pay
	badVal.Payload.Authorization.Value = "25001"
	if err := protocol.Verify(req, badVal); err == nil {
		t.Fatal("expected value mismatch to fail Verify")
	}
	badTo := pay
	badTo.Payload.Authorization.To = "0x000000000000000000000000000000000000dead"
	if err := protocol.Verify(req, badTo); err == nil {
		t.Fatal("expected recipient mismatch to fail Verify")
	}
	if err := protocol.InWindow(pay.Payload.Authorization, now.Add(5*time.Minute)); err == nil {
		t.Fatal("expected expired authorization to fail InWindow")
	}
}

type testProviderHarness struct {
	providerKey *ecdsa.PrivateKey
	providerAdr string
	facA        *HTTPFacilitatorServer
	facB        *HTTPFacilitatorServer
	srvFacA     *httptest.Server
	srvFacB     *httptest.Server
	multi       *MultiFacilitator
	quoteSigner *ECDSAQuoteSigner
	nonceStore  *MemoryNonceStore
	ledger      *MemoryReceiptLedger
	guard       *SettlementGuard
	providerSrv *httptest.Server
	inferCalls  atomic.Int64
	failInfer   atomic.Bool
	clockMu     sync.Mutex
	clockNow    time.Time
}

func newTestProviderHarness(t *testing.T, strategy RoutingStrategy) *testProviderHarness {
	t.Helper()
	h := &testProviderHarness{
		providerKey: ephemeralKey(t),
		clockNow:    time.Unix(1760000100, 0).UTC(),
	}
	h.providerAdr = strings.ToLower(crypto.PubkeyToAddress(h.providerKey.PublicKey).Hex())
	nowFn := func() time.Time {
		h.clockMu.Lock()
		defer h.clockMu.Unlock()
		return h.clockNow
	}

	h.facA = NewHTTPFacilitatorServer("independent-facilitator-a", "community-a")
	h.facA.Now = nowFn
	h.srvFacA = httptest.NewServer(h.facA)
	t.Cleanup(h.srvFacA.Close)

	h.facB = NewHTTPFacilitatorServer("independent-facilitator-b", "community-b")
	h.facB.Now = nowFn
	h.srvFacB = httptest.NewServer(h.facB)
	t.Cleanup(h.srvFacB.Close)

	var err error
	h.multi, err = NewMultiFacilitator(strategy,
		NamedFacilitator{
			ID:          h.facA.ID,
			Operator:    h.facA.Operator,
			Endpoint:    h.srvFacA.URL,
			Facilitator: protocol.Remote{URL: h.srvFacA.URL, Client: h.srvFacA.Client()},
		},
		NamedFacilitator{
			ID:          h.facB.ID,
			Operator:    h.facB.Operator,
			Endpoint:    h.srvFacB.URL,
			Facilitator: protocol.Remote{URL: h.srvFacB.URL, Client: h.srvFacB.Client()},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	h.multi.now = nowFn

	h.quoteSigner, err = NewECDSAQuoteSigner(h.providerKey, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	h.quoteSigner.Now = nowFn

	h.nonceStore = NewMemoryNonceStore()
	h.nonceStore.now = nowFn

	h.ledger = NewMemoryReceiptLedger(h.providerKey)
	h.ledger.now = nowFn

	table, err := protocol.NewTable(testAsset(h.providerAdr))
	if err != nil {
		t.Fatal(err)
	}
	if err := table.Set("POST /v1/infer", protocol.Price{Amount: "0.01", Description: "inference call"}); err != nil {
		t.Fatal(err)
	}

	h.guard = &SettlementGuard{
		Table:       table,
		Facilitator: h.multi,
		QuoteSigner: h.quoteSigner,
		NonceStore:  h.nonceStore,
		Ledger:      h.ledger,
		Now:         nowFn,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/infer", func(w http.ResponseWriter, r *http.Request) {
		h.inferCalls.Add(1)
		if h.failInfer.Load() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"upstream inference failed"}`))
			return
		}
		var in InferenceRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Model == "" {
			in.Model = "zen-7b-instruct"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(InferenceResponse{
			Model:            in.Model,
			Output:           "inference-ok:" + in.Prompt,
			PromptTokens:     128,
			CompletionTokens: 256,
			TotalTokens:      384,
		})
	})

	h.providerSrv = httptest.NewServer(h.guard.HTTPMiddleware(pricedKey(table))(mux))
	t.Cleanup(h.providerSrv.Close)
	return h
}

func TestEndToEndTwoIndependentHTTPFacilitators(t *testing.T) {
	h := newTestProviderHarness(t, StrategyRoundRobin)
	agentKey := ephemeralKey(t)
	agent := &AgentClient{
		HTTPClient:       h.providerSrv.Client(),
		Key:              agentKey,
		ExpectedProvider: h.providerAdr,
		Now:              h.guard.Now,
	}

	for i := 0; i < 4; i++ {
		body, _ := json.Marshal(InferenceRequest{
			Model:     "zen-7b-instruct",
			Prompt:    "hello-x402",
			MaxTokens: 256,
		})
		resp, respBody, _, err := agent.DoPaid(context.Background(), http.MethodPost, h.providerSrv.URL+"/v1/infer", body, nil)
		if err != nil {
			t.Fatalf("call %d failed: %v", i, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("call %d status %d body %s", i, resp.StatusCode, string(respBody))
		}
		if resp.Header.Get(protocol.HeaderPaymentResponse) == "" {
			t.Fatalf("call %d missing PAYMENT-RESPONSE header", i)
		}
		if resp.Header.Get(HeaderReceiptID) == "" {
			t.Fatalf("call %d missing receipt id header", i)
		}
	}

	_, settledA := h.facA.Counts()
	_, settledB := h.facB.Counts()
	if settledA != 2 || settledB != 2 {
		t.Fatalf("expected 2 settlements on each independent facilitator, got A=%d B=%d", settledA, settledB)
	}

	receipts := h.ledger.List(context.Background(), ReceiptFilter{Status: StatusSettled})
	if len(receipts) != 4 {
		t.Fatalf("expected 4 settled receipts, got %d", len(receipts))
	}

	var exported bytes.Buffer
	if err := h.ledger.ExportJSONL(&exported); err != nil {
		t.Fatalf("export jsonl: %v", err)
	}
	ver, err := VerifyExportJSONL(bytes.NewReader(exported.Bytes()), h.providerAdr)
	if err != nil {
		t.Fatalf("verify export failed: %v", err)
	}
	if !ver.ChainIntact || !ver.SignaturesIntact || ver.TotalEntries != 4 || ver.SignedEntries != 4 {
		t.Fatalf("unexpected verification result: %+v", ver)
	}
}

func TestIdempotentDuplicateRequestNoDoubleCharge(t *testing.T) {
	h := newTestProviderHarness(t, StrategyFailover)
	agentKey := ephemeralKey(t)
	agent := &AgentClient{
		HTTPClient:       h.providerSrv.Client(),
		Key:              agentKey,
		ExpectedProvider: h.providerAdr,
		Now:              h.guard.Now,
	}

	body, _ := json.Marshal(InferenceRequest{Model: "zen-7b-instruct", Prompt: "idempotent-prompt", MaxTokens: 128})
	headers := map[string]string{HeaderIdempotencyKey: "idem-req-001"}

	resp1, body1, pay, err := agent.DoPaid(context.Background(), http.MethodPost, h.providerSrv.URL+"/v1/infer", body, headers)
	if err != nil || resp1.StatusCode != http.StatusOK {
		t.Fatalf("first call failed: status=%v err=%v", resp1, err)
	}
	encodedSig, err := protocol.EncodeHeader(pay)
	if err != nil {
		t.Fatal(err)
	}

	// Duplicate retry carrying identical Idempotency-Key, payload, and signature.
	req2, err := http.NewRequest(http.MethodPost, h.providerSrv.URL+"/v1/infer", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set(HeaderIdempotencyKey, "idem-req-001")
	req2.Header.Set(protocol.HeaderPaymentSignature, encodedSig)
	resp2, err := h.providerSrv.Client().Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("duplicate retry status=%d body=%s", resp2.StatusCode, string(body2))
	}
	if resp2.Header.Get(HeaderIdempotentReplay) != "true" {
		t.Fatalf("expected %s=true on duplicate retry", HeaderIdempotentReplay)
	}
	if string(body1) != string(body2) {
		t.Fatalf("cached body mismatch: %q vs %q", string(body1), string(body2))
	}
	if resp2.Header.Get(HeaderReceiptID) != resp1.Header.Get(HeaderReceiptID) {
		t.Fatalf("receipt id changed on idempotent replay")
	}
	if got := h.inferCalls.Load(); got != 1 {
		t.Fatalf("inference ran %d times, want 1", got)
	}
	_, settledA := h.facA.Counts()
	if settledA != 1 {
		t.Fatalf("facilitator settled %d times, want 1", settledA)
	}

	// Reusing the same Idempotency-Key with a mutated body must return 409 Conflict.
	diffBody, _ := json.Marshal(InferenceRequest{Model: "zen-7b-instruct", Prompt: "mutated-prompt", MaxTokens: 128})
	req3, _ := http.NewRequest(http.MethodPost, h.providerSrv.URL+"/v1/infer", bytes.NewReader(diffBody))
	req3.Header.Set("Content-Type", "application/json")
	req3.Header.Set(HeaderIdempotencyKey, "idem-req-001")
	req3.Header.Set(protocol.HeaderPaymentSignature, encodedSig)
	resp3, err := h.providerSrv.Client().Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp3.Body.Close()
	if resp3.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 Conflict on idempotency payload mismatch, got %d", resp3.StatusCode)
	}
}

func TestReplayedSignatureAndConcurrent16GoroutineRace(t *testing.T) {
	h := newTestProviderHarness(t, StrategyFailover)
	agentKey := ephemeralKey(t)
	agent := &AgentClient{
		HTTPClient:       h.providerSrv.Client(),
		Key:              agentKey,
		ExpectedProvider: h.providerAdr,
		Now:              h.guard.Now,
	}

	body, _ := json.Marshal(InferenceRequest{Model: "zen-7b-instruct", Prompt: "once-only", MaxTokens: 64})
	resp1, _, pay1, err := agent.DoPaid(context.Background(), http.MethodPost, h.providerSrv.URL+"/v1/infer", body, nil)
	if err != nil || resp1.StatusCode != http.StatusOK {
		t.Fatalf("initial paid call failed: %v", err)
	}
	encoded1, _ := protocol.EncodeHeader(pay1)

	// Replaying the already-settled signature without an idempotency hit must be rejected.
	replayReq, _ := http.NewRequest(http.MethodPost, h.providerSrv.URL+"/v1/infer", bytes.NewReader(body))
	replayReq.Header.Set("Content-Type", "application/json")
	replayReq.Header.Set(protocol.HeaderPaymentSignature, encoded1)
	replayResp, err := h.providerSrv.Client().Do(replayReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = replayResp.Body.Close()
	if replayResp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("expected 402 on replayed signature, got %d", replayResp.StatusCode)
	}

	// Concurrent 16-goroutine double-spend race with the exact same freshly signed nonce.
	challengeReq, _ := http.NewRequest(http.MethodPost, h.providerSrv.URL+"/v1/infer", bytes.NewReader(body))
	challengeResp, err := h.providerSrv.Client().Do(challengeReq)
	if err != nil {
		t.Fatal(err)
	}
	var pr protocol.PaymentRequired
	if err := protocol.DecodeHeader(challengeResp.Header.Get(protocol.HeaderPaymentRequired), &pr); err != nil {
		t.Fatalf("decode challenge: %v", err)
	}
	_ = challengeResp.Body.Close()

	signer := protocol.Signer{
		Key:   agentKey,
		Allow: func(protocol.ResourceInfo, protocol.PaymentRequirements) bool { return true },
		Now:   h.guard.Now,
	}
	racePay, err := signer.Pay(context.Background(), pr)
	if err != nil {
		t.Fatal(err)
	}
	raceSig, err := protocol.EncodeHeader(racePay)
	if err != nil {
		t.Fatal(err)
	}

	const workers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	var okCount atomic.Int64
	var rejectedCount atomic.Int64

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req, _ := http.NewRequest(http.MethodPost, h.providerSrv.URL+"/v1/infer", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(protocol.HeaderPaymentSignature, raceSig)
			resp, err := h.providerSrv.Client().Do(req)
			if err != nil {
				t.Errorf("worker request error: %v", err)
				return
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				okCount.Add(1)
			} else if resp.StatusCode == http.StatusPaymentRequired || resp.StatusCode == http.StatusConflict {
				rejectedCount.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if okCount.Load() != 1 || rejectedCount.Load() != workers-1 {
		t.Fatalf("concurrent race expected 1 success and %d rejections, got ok=%d rejected=%d",
			workers-1, okCount.Load(), rejectedCount.Load())
	}
	_, settledA := h.facA.Counts()
	if settledA != 2 { // 1 from initial call + 1 from concurrent race
		t.Fatalf("expected exactly 2 total settlements (0 double-spends), got %d", settledA)
	}
}

func TestFailedInferenceSkipsSettlementChargesZero(t *testing.T) {
	h := newTestProviderHarness(t, StrategyFailover)
	agentKey := ephemeralKey(t)
	agent := &AgentClient{
		HTTPClient:       h.providerSrv.Client(),
		Key:              agentKey,
		ExpectedProvider: h.providerAdr,
		Now:              h.guard.Now,
	}

	h.failInfer.Store(true)
	body, _ := json.Marshal(InferenceRequest{Model: "zen-7b-instruct", Prompt: "fail-first", MaxTokens: 128})
	resp, _, pay, err := agent.DoPaid(context.Background(), http.MethodPost, h.providerSrv.URL+"/v1/infer", body, nil)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 on failed inference, got %d", resp.StatusCode)
	}
	_, settledA := h.facA.Counts()
	if settledA != 0 {
		t.Fatalf("failed inference MUST NOT settle, got settled=%d", settledA)
	}
	voided := h.ledger.List(context.Background(), ReceiptFilter{Status: StatusVoided})
	if len(voided) != 1 || voided[0].ChargedAtomic != "0" {
		t.Fatalf("expected 1 VOIDED receipt with chargedAtomic=0, got %+v", voided)
	}

	// Because the authorization was never settled, retrying it once the provider recovers must succeed.
	h.failInfer.Store(false)
	encodedSig, _ := protocol.EncodeHeader(pay)
	retryReq, _ := http.NewRequest(http.MethodPost, h.providerSrv.URL+"/v1/infer", bytes.NewReader(body))
	retryReq.Header.Set("Content-Type", "application/json")
	retryReq.Header.Set(protocol.HeaderPaymentSignature, encodedSig)
	retryResp, err := h.providerSrv.Client().Do(retryReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = retryResp.Body.Close()
	if retryResp.StatusCode != http.StatusOK {
		t.Fatalf("expected unspent authorization retry to succeed, got %d", retryResp.StatusCode)
	}
	_, settledA = h.facA.Counts()
	if settledA != 1 {
		t.Fatalf("expected exactly 1 settlement after retry, got %d", settledA)
	}
}

func TestSettlementReversalAndDeterministicReconciliation(t *testing.T) {
	h := newTestProviderHarness(t, StrategyRoundRobin)
	agentKey := ephemeralKey(t)
	agent := &AgentClient{
		HTTPClient:       h.providerSrv.Client(),
		Key:              agentKey,
		ExpectedProvider: h.providerAdr,
		Now:              h.guard.Now,
	}

	// Issue two paid calls at the EXACT same frozen timestamp across Facilitator A and Facilitator B.
	for i := 0; i < 2; i++ {
		body, _ := json.Marshal(InferenceRequest{Model: "zen-7b-instruct", Prompt: "reconcile", MaxTokens: 64})
		resp, _, _, err := agent.DoPaid(context.Background(), http.MethodPost, h.providerSrv.URL+"/v1/infer", body, nil)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("call %d failed: %v", i, err)
		}
	}

	// Select receipt deterministically per facilitator (never assuming index 0 belongs to a specific facilitator).
	facAReceipts := h.ledger.List(context.Background(), ReceiptFilter{FacilitatorID: h.facA.ID})
	facBReceipts := h.ledger.List(context.Background(), ReceiptFilter{FacilitatorID: h.facB.ID})
	if len(facAReceipts) != 1 || len(facBReceipts) != 1 {
		t.Fatalf("expected 1 receipt per facilitator, got A=%d B=%d", len(facAReceipts), len(facBReceipts))
	}

	// Trigger reversal on Facilitator A.
	revEvent, err := h.facA.ReverseTransaction(facAReceipts[0].Transaction, "chain_reorg_revert", true)
	if err != nil {
		t.Fatalf("reverse tx: %v", err)
	}

	// Cross-facilitator spoofed reversal must be rejected.
	spoofed := revEvent
	spoofed.FacilitatorID = h.facB.ID
	if _, err := h.ledger.ReconcileReversal(context.Background(), spoofed); err == nil {
		t.Fatal("expected cross-facilitator reversal mismatch to be rejected")
	}

	// Valid reversal transitions receipt deterministically to REFUND_REQUIRED with chargedAtomic=0.
	reconciled, err := h.ledger.ReconcileReversal(context.Background(), revEvent)
	if err != nil {
		t.Fatalf("reconcile reversal: %v", err)
	}
	if reconciled.Status != StatusRefundRequired || reconciled.ChargedAtomic != "0" || reconciled.ReversalTx == "" {
		t.Fatalf("unexpected reconciled receipt: %+v", reconciled)
	}

	// Facilitator B's receipt must remain untouched SETTLED.
	recB, ok := h.ledger.Get(context.Background(), facBReceipts[0].ReceiptID)
	if !ok || recB.Status != StatusSettled {
		t.Fatalf("facilitator B receipt unexpectedly mutated: %+v", recB)
	}

	// Sovereign JSONL export verifies intact chain and catches single-byte tampering.
	var exported bytes.Buffer
	if err := h.ledger.ExportJSONL(&exported); err != nil {
		t.Fatal(err)
	}
	ver, err := VerifyExportJSONL(bytes.NewReader(exported.Bytes()), h.providerAdr)
	if err != nil {
		t.Fatalf("verify export: %v", err)
	}
	if !ver.ChainIntact || !ver.SignaturesIntact || ver.TotalEntries != 3 || ver.UniqueReceipts != 2 {
		t.Fatalf("unexpected export verification: %+v", ver)
	}

	tampered := strings.Replace(exported.String(), "chain_reorg_revert", "chain_reorg_fake00", 1)
	if _, err := VerifyExportJSONL(strings.NewReader(tampered), h.providerAdr); err == nil {
		t.Fatal("expected VerifyExportJSONL to detect tampered JSONL entry")
	}
}

func TestTamperedAndExpiredSignedQuoteRejected(t *testing.T) {
	h := newTestProviderHarness(t, StrategyFailover)
	agentKey := ephemeralKey(t)

	body, _ := json.Marshal(InferenceRequest{Model: "zen-7b-instruct", Prompt: "quote-check", MaxTokens: 128})
	chReq, _ := http.NewRequest(http.MethodPost, h.providerSrv.URL+"/v1/infer", bytes.NewReader(body))
	chResp, err := h.providerSrv.Client().Do(chReq)
	if err != nil {
		t.Fatal(err)
	}
	var pr protocol.PaymentRequired
	if err := protocol.DecodeHeader(chResp.Header.Get(protocol.HeaderPaymentRequired), &pr); err != nil {
		t.Fatal(err)
	}
	_ = chResp.Body.Close()

	origQuote, ok, err := ExtractQuoteExtension(pr.Extensions)
	if err != nil || !ok {
		t.Fatalf("expected signed quote in 402 challenge: ok=%v err=%v", ok, err)
	}

	signer := protocol.Signer{
		Key:   agentKey,
		Allow: func(protocol.ResourceInfo, protocol.PaymentRequirements) bool { return true },
		Now:   h.guard.Now,
	}

	sendWithQuote := func(reqBody []byte, mutate func(*SignedPriceQuote), dropQuote bool) int {
		prCopy := pr
		if dropQuote {
			prCopy.Extensions = nil
		} else {
			q := origQuote
			if mutate != nil {
				mutate(&q)
			}
			ext, err := AttachQuoteExtension(nil, q)
			if err != nil {
				t.Fatal(err)
			}
			prCopy.Extensions = ext
		}
		pay, err := signer.Pay(context.Background(), prCopy)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := protocol.EncodeHeader(pay)
		req, _ := http.NewRequest(http.MethodPost, h.providerSrv.URL+"/v1/infer", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(protocol.HeaderPaymentSignature, encoded)
		resp, err := h.providerSrv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	// 1. Tampered quote amount
	if code := sendWithQuote(body, func(q *SignedPriceQuote) { q.AmountAtomic = "1" }, false); code != http.StatusPaymentRequired {
		t.Fatalf("tampered amount status=%d, want 402", code)
	}
	// 2. Tampered quote model
	if code := sendWithQuote(body, func(q *SignedPriceQuote) { q.Model = "zen-max-900b" }, false); code != http.StatusPaymentRequired {
		t.Fatalf("tampered model status=%d, want 402", code)
	}
	// 3. Swapped request body (violates requestBodyHash binding)
	swappedBody, _ := json.Marshal(InferenceRequest{Model: "zen-7b-instruct", Prompt: "expensive-swapped-prompt", MaxTokens: 128})
	if code := sendWithQuote(swappedBody, nil, false); code != http.StatusPaymentRequired {
		t.Fatalf("swapped body status=%d, want 402", code)
	}
	// 4. Dropped quote extension
	if code := sendWithQuote(body, nil, true); code != http.StatusPaymentRequired {
		t.Fatalf("missing quote status=%d, want 402", code)
	}
	// 5. Expired quote
	h.clockMu.Lock()
	h.clockNow = h.clockNow.Add(10 * time.Minute)
	h.clockMu.Unlock()
	if code := sendWithQuote(body, nil, false); code != http.StatusPaymentRequired {
		t.Fatalf("expired quote status=%d, want 402", code)
	}
	if h.inferCalls.Load() != 0 {
		t.Fatalf("expected 0 inference executions across all rejected quotes, got %d", h.inferCalls.Load())
	}
}

func TestFacilitatorOutageFailoverAndZeroDowntimeMigration(t *testing.T) {
	h := newTestProviderHarness(t, StrategyFailover)
	agentKey := ephemeralKey(t)
	agent := &AgentClient{
		HTTPClient:       h.providerSrv.Client(),
		Key:              agentKey,
		ExpectedProvider: h.providerAdr,
		Now:              h.guard.Now,
	}

	body, _ := json.Marshal(InferenceRequest{Model: "zen-7b-instruct", Prompt: "failover-test", MaxTokens: 64})
	resp1, _, _, err := agent.DoPaid(context.Background(), http.MethodPost, h.providerSrv.URL+"/v1/infer", body, nil)
	if err != nil || resp1.StatusCode != http.StatusOK {
		t.Fatalf("pre-outage call failed: %v", err)
	}

	// Take Facilitator A offline mid-run.
	h.srvFacA.Close()

	resp2, _, _, err := agent.DoPaid(context.Background(), http.MethodPost, h.providerSrv.URL+"/v1/infer", body, nil)
	if err != nil || resp2.StatusCode != http.StatusOK {
		t.Fatalf("failover call failed: status=%v err=%v", resp2, err)
	}
	if h.multi.TotalFailovers() < 1 {
		t.Fatalf("expected at least 1 failover, got %d", h.multi.TotalFailovers())
	}
	_, settledB := h.facB.Counts()
	if settledB != 1 {
		t.Fatalf("expected Facilitator B to settle failover call, got %d", settledB)
	}

	// Zero-downtime live migration to a brand-new independent Facilitator C.
	facC := NewHTTPFacilitatorServer("independent-facilitator-c", "self-hosted-c")
	facC.Now = h.guard.Now
	srvC := httptest.NewServer(facC)
	defer srvC.Close()

	if err := h.multi.SwitchFacilitators([]NamedFacilitator{{
		ID:          facC.ID,
		Operator:    facC.Operator,
		Endpoint:    srvC.URL,
		Facilitator: protocol.Remote{URL: srvC.URL, Client: srvC.Client()},
	}}); err != nil {
		t.Fatalf("switch facilitators: %v", err)
	}
	resp3, _, _, err := agent.DoPaid(context.Background(), http.MethodPost, h.providerSrv.URL+"/v1/infer", body, nil)
	if err != nil || resp3.StatusCode != http.StatusOK {
		t.Fatalf("post-migration call failed: %v", err)
	}
	_, settledC := facC.Counts()
	if settledC != 1 || h.multi.Migrations() != 1 {
		t.Fatalf("expected 1 settlement on Facilitator C and 1 migration, got settled=%d migrations=%d", settledC, h.multi.Migrations())
	}
}

func TestPersistentFileNonceStoreAndReceiptLedgerRestart(t *testing.T) {
	dir := t.TempDir()
	noncePath := filepath.Join(dir, "nonces.jsonl")
	ledgerPath := filepath.Join(dir, "receipts.jsonl")
	providerKey := ephemeralKey(t)
	providerAddr := strings.ToLower(crypto.PubkeyToAddress(providerKey.PublicKey).Hex())

	ns1, err := NewFileNonceStore(noncePath)
	if err != nil {
		t.Fatal(err)
	}
	claim := NonceClaim{
		Network: "eip155:8453",
		Asset:   "0x00000000000000000000000000000000000a11c3",
		From:    providerAddr,
		Nonce:   "0x" + strings.Repeat("ab", 32),
	}
	bodyHash := HashRequestBody([]byte(`{"prompt":"persist"}`))
	res, err := ns1.Reserve(context.Background(), claim, "idem-persist-1", bodyHash)
	if err != nil || res.Outcome != ReserveAdmitted {
		t.Fatalf("reserve failed: %+v %v", res, err)
	}
	if err := ns1.Commit(context.Background(), claim, "idem-persist-1", &CachedResponse{
		StatusCode: 200,
		Body:       []byte(`{"ok":true}`),
		ReceiptID:  "rcpt_persist",
	}); err != nil {
		t.Fatal(err)
	}

	// Reopen from disk to simulate process restart.
	ns2, err := NewFileNonceStore(noncePath)
	if err != nil {
		t.Fatal(err)
	}
	if !ns2.IsSpent(context.Background(), claim) {
		t.Fatal("expected spent nonce to persist across restart")
	}
	replayRes, err := ns2.Reserve(context.Background(), claim, "", bodyHash)
	if err != nil || replayRes.Outcome != ReserveNonceReplay {
		t.Fatalf("expected nonce replay after restart, got %+v", replayRes)
	}
	idemRes, err := ns2.Reserve(context.Background(), claim, "idem-persist-1", bodyHash)
	if err != nil || idemRes.Outcome != ReserveIdempotentHit || string(idemRes.Cached.Body) != `{"ok":true}` {
		t.Fatalf("expected idempotent hit after restart, got %+v", idemRes)
	}

	l1, err := NewFileReceiptLedger(ledgerPath, providerKey)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := l1.Record(context.Background(), Receipt{
		Resource:      "POST /v1/infer",
		FacilitatorID: "independent-facilitator-a",
		Payer:         providerAddr,
		PayTo:         providerAddr,
		Network:       "eip155:8453",
		Asset:         claim.Asset,
		AmountAtomic:  "10000",
		Nonce:         claim.Nonce,
		Transaction:   "0x" + strings.Repeat("cd", 32),
		Status:        StatusSettled,
	})
	if err != nil {
		t.Fatal(err)
	}

	l2, err := NewFileReceiptLedger(ledgerPath, providerKey)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := l2.Get(context.Background(), saved.ReceiptID)
	if !ok || got.Transaction != saved.Transaction {
		t.Fatalf("receipt missing after restart: %+v", got)
	}
	var exported bytes.Buffer
	if err := l2.ExportJSONL(&exported); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyExportJSONL(bytes.NewReader(exported.Bytes()), providerAddr); err != nil {
		t.Fatalf("reopened ledger failed verification: %v", err)
	}
}

func TestZipTypedOpsAndMultiFacilitatorConfigLoad(t *testing.T) {
	providerKey := ephemeralKey(t)
	providerAddr := strings.ToLower(crypto.PubkeyToAddress(providerKey.PublicKey).Hex())

	facA := NewHTTPFacilitatorServer("fac-a", "independent")
	srvA := httptest.NewServer(facA)
	defer srvA.Close()

	facB := NewHTTPFacilitatorServer("fac-b", "independent")
	srvB := httptest.NewServer(facB)
	defer srvB.Close()

	// Verify JSON multi-facilitator config loading.
	cfgJSON := `{
		"routingStrategy": "round_robin",
		"facilitators": [
			{"id": "fac-a", "operator": "community-a", "url": "` + srvA.URL + `"},
			{"id": "fac-b", "operator": "community-b", "url": "` + srvB.URL + `"}
		],
		"assets": [{"network":"eip155:84532","address":"` + token + `","name":"USD Coin","version":"2","decimals":6,"payTo":"` + providerAddr + `","maxTimeoutSeconds":60}],
		"prices": {"POST /v1/infer": {"amount": "0.01", "description": "inference"}}
	}`
	cfg, err := Load(strings.NewReader(cfgJSON))
	if err != nil {
		t.Fatalf("Load multi-facilitator config failed: %v", err)
	}
	qs, err := NewECDSAQuoteSigner(providerKey, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cfg.QuoteSigner = qs
	cfg.NonceStore = NewMemoryNonceStore()
	cfg.Ledger = NewMemoryReceiptLedger(providerKey)

	app := zip.New(zip.Config{})
	if err := Mount(app, luxlog.New("test"), cfg); err != nil {
		t.Fatalf("Mount failed: %v", err)
	}
	app.Raw(http.MethodPost, "/v1/infer", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, InferenceResponse{Model: "zen-7b", Output: "zip-ok", TotalTokens: 128})
	})

	// 1. Check GET /v1/x402/facilitators typed op.
	resp, body := do(t, app, http.MethodGet, "/v1/x402/facilitators", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"fac-a"`) || !strings.Contains(body, `"fac-b"`) {
		t.Fatalf("facilitators op failed: %d %s", resp.StatusCode, body)
	}

	// 2. Check POST /v1/x402/quote typed op.
	inferBody := []byte(`{"model":"zen-7b","prompt":"hello","max_tokens":64}`)
	quoteReqBody, _ := json.Marshal(quoteIn{
		Resource:        "POST /v1/infer",
		Model:           "zen-7b",
		MaxTokens:       64,
		RequestBodyHash: HashRequestBody(inferBody),
	})
	qReq := httptest.NewRequest(http.MethodPost, "/v1/x402/quote", bytes.NewReader(quoteReqBody))
	qReq.Header.Set("Content-Type", "application/json")
	qResp, err := app.Test(qReq, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	qBytes, _ := io.ReadAll(qResp.Body)
	_ = qResp.Body.Close()
	if qResp.StatusCode != http.StatusOK {
		t.Fatalf("quote op failed: %d %s", qResp.StatusCode, string(qBytes))
	}
	var qOut quoteOut
	if err := json.Unmarshal(qBytes, &qOut); err != nil || qOut.Quote.Signature == "" {
		t.Fatalf("decode quote op: %v %s", err, string(qBytes))
	}
	if err := VerifySignedQuoteStandalone(qOut.Quote, providerAddr, HashRequestBody(inferBody), time.Now()); err != nil {
		t.Fatalf("standalone quote verify failed: %v", err)
	}

	// 3. Check optional CircleFacilitator adapter sends Bearer auth when configured.
	var gotAuth string
	circleSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		facA.ServeHTTP(w, r)
	}))
	defer circleSrv.Close()
	circleFac := NewCircleFacilitator(circleSrv.URL, "test-ephemeral-token", circleSrv.Client())
	if circleFac.Operator != "circle" {
		t.Fatalf("unexpected operator: %s", circleFac.Operator)
	}
	agentKey := ephemeralKey(t)
	var nonce [32]byte
	_, _ = rand.Read(nonce[:])
	req := protocol.PaymentRequirements{
		Scheme:            protocol.SchemeExact,
		Network:           "eip155:84532",
		Amount:            "10000",
		Asset:             token,
		PayTo:             providerAddr,
		MaxTimeoutSeconds: 60,
		Extra:             &protocol.Extra{Name: "USD Coin", Version: "2"},
	}
	pay, err := protocol.Sign(req, agentKey, nonce, 0, uint64(time.Now().Add(time.Minute).Unix()))
	if err != nil {
		t.Fatal(err)
	}
	vResp, err := circleFac.Facilitator.Verify(context.Background(), pay, req)
	if err != nil || !vResp.IsValid || gotAuth != "Bearer test-ephemeral-token" {
		t.Fatalf("circle adapter verify failed: resp=%+v err=%v auth=%q", vResp, err, gotAuth)
	}
}
