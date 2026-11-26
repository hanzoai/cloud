// SPDX-License-Identifier: Apache-2.0 OR MIT

package x402

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestAgentClientRejectsMismatchedAcceptsDespiteValidSignedQuote(t *testing.T) {
	h := newTestProviderHarness(t, StrategyFailover)
	agentKey := ephemeralKey(t)

	// Untrusted proxy keeps the provider's authentic SignedPriceQuote intact,
	// but mutates pr.Accepts to inflate the amount and divert PayTo.
	attackerAddr := strings.ToLower(crypto.PubkeyToAddress(ephemeralKey(t).PublicKey).Hex())
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		upReq, _ := http.NewRequestWithContext(r.Context(), r.Method, h.providerSrv.URL+r.URL.RequestURI(), bytes.NewReader(body))
		upReq.Header = r.Header.Clone()
		upResp, err := h.providerSrv.Client().Do(upReq)
		if err != nil {
			t.Errorf("upstream proxy call: %v", err)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer upResp.Body.Close()
		upBody, _ := io.ReadAll(upResp.Body)
		if upResp.StatusCode == http.StatusPaymentRequired {
			var pr protocol.PaymentRequired
			if err := protocol.DecodeHeader(upResp.Header.Get(protocol.HeaderPaymentRequired), &pr); err == nil && len(pr.Accepts) > 0 {
				pr.Accepts[0].Amount = "999999999"
				pr.Accepts[0].PayTo = attackerAddr
				if enc, err := protocol.EncodeHeader(pr); err == nil {
					upResp.Header.Set(protocol.HeaderPaymentRequired, enc)
				}
			}
		}
		copyHeaders(w.Header(), upResp.Header)
		w.WriteHeader(upResp.StatusCode)
		_, _ = w.Write(upBody)
	}))
	defer proxy.Close()

	agent := &AgentClient{
		HTTPClient:       proxy.Client(),
		Key:              agentKey,
		ExpectedProvider: h.providerAdr,
		Now:              h.guard.Now,
	}
	reqBody, _ := json.Marshal(InferenceRequest{Model: "zen-7b-instruct", Prompt: "hello", MaxTokens: 32})
	_, _, pay, err := agent.DoPaid(context.Background(), http.MethodPost, proxy.URL+"/v1/infer", reqBody, nil)
	if err == nil {
		t.Fatal("expected AgentClient.DoPaid to reject mutated Accepts before signing")
	}
	if pay != nil {
		t.Fatalf("AgentClient must not sign an authorization when Accepts diverges from SignedPriceQuote: %+v", pay)
	}
	if h.inferCalls.Load() != 0 {
		t.Fatalf("expected 0 inference calls, got %d", h.inferCalls.Load())
	}
}

func TestIdempotencyIsolatedByOrgCallerMethodAndURL(t *testing.T) {
	providerKey := ephemeralKey(t)
	providerAddr := strings.ToLower(crypto.PubkeyToAddress(providerKey.PublicKey).Hex())
	token := "0x036CbD53842c5426634e7929541eC2318f3dCF7e"

	fac := NewHTTPFacilitatorServer("fac-iso", "independent")
	facSrv := httptest.NewServer(fac)
	defer facSrv.Close()

	table, err := protocol.NewTable(protocol.Asset{
		Network:           "eip155:84532",
		Address:           token,
		Name:              "USD Coin",
		Version:           "2",
		Decimals:          6,
		PayTo:             providerAddr,
		MaxTimeoutSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := table.Set("GET /v1/kv", protocol.Price{Amount: "0.01", Description: "kv read"}); err != nil {
		t.Fatal(err)
	}
	if err := table.Set("POST /v1/kv", protocol.Price{Amount: "0.01", Description: "kv write"}); err != nil {
		t.Fatal(err)
	}

	ledger := NewMemoryReceiptLedger(providerKey)
	nonceStore := NewMemoryNonceStore()
	cfg := Config{
		Assets: []protocol.Asset{{
			Network:           "eip155:84532",
			Address:           token,
			Name:              "USD Coin",
			Version:           "2",
			Decimals:          6,
			PayTo:             providerAddr,
			MaxTimeoutSeconds: 60,
		}},
		Prices: map[string]protocol.Price{
			"GET /v1/kv":  {Amount: "0.01", Description: "kv read"},
			"POST /v1/kv": {Amount: "0.01", Description: "kv write"},
		},
		Facilitator: protocol.Remote{URL: facSrv.URL},
		NonceStore:  nonceStore,
		Ledger:      ledger,
	}

	app := zip.New(zip.Config{})
	if err := Mount(app, luxlog.New("test"), cfg); err != nil {
		t.Fatal(err)
	}
	var handlerCalls atomic.Int64
	app.Raw(http.MethodGet, "/v1/kv", func(c *zip.Ctx) error {
		handlerCalls.Add(1)
		return c.JSON(http.StatusOK, map[string]string{
			"org":   c.Org(),
			"key":   c.Query("k"),
			"value": "secret-for-" + c.Org() + "-" + c.Query("k"),
		})
	})
	app.Raw(http.MethodPost, "/v1/kv", func(c *zip.Ctx) error {
		handlerCalls.Add(1)
		return c.JSON(http.StatusOK, map[string]string{"org": c.Org(), "method": "POST"})
	})

	accepts, _, _ := table.Quote("GET /v1/kv")
	signPay := func(k *ecdsa.PrivateKey) string {
		var nonce [32]byte
		_, _ = rand.Read(nonce[:])
		p, err := protocol.Sign(accepts[0], k, nonce, 0, uint64(time.Now().Add(5*time.Minute).Unix()))
		if err != nil {
			t.Fatal(err)
		}
		enc, err := protocol.EncodeHeader(p)
		if err != nil {
			t.Fatal(err)
		}
		return enc
	}

	orgAKey := ephemeralKey(t)
	orgBKey := ephemeralKey(t)
	sigA := signPay(orgAKey)

	doReq := func(method, target, org, idem, sig string) (*http.Response, string) {
		req := httptest.NewRequest(method, target, nil)
		if org != "" {
			req.Header.Set("X-Org-Id", org)
		}
		if idem != "" {
			req.Header.Set(HeaderIdempotencyKey, idem)
		}
		if sig != "" {
			req.Header.Set(protocol.HeaderPaymentSignature, sig)
		}
		resp, err := app.Test(req, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp, string(b)
	}

	// 1. Org A pays GET /v1/kv?k=alpha with Idempotency-Key.
	r1, b1 := doReq(http.MethodGet, "/v1/kv?k=alpha", "org-a", "shared-idem-key", sigA)
	if r1.StatusCode != http.StatusOK || !strings.Contains(b1, "secret-for-org-a-alpha") {
		t.Fatalf("org-a initial call failed: %d %s", r1.StatusCode, b1)
	}

	// 2. Unauthenticated caller supplying the same Idempotency-Key without PAYMENT-SIGNATURE must get 402.
	rNoSig, bNoSig := doReq(http.MethodGet, "/v1/kv?k=alpha", "org-b", "shared-idem-key", "")
	if rNoSig.StatusCode != http.StatusPaymentRequired || strings.Contains(bNoSig, "secret-for-org-a") {
		t.Fatalf("unauthenticated caller must get 402 without org-a data: %d %s", rNoSig.StatusCode, bNoSig)
	}

	// 3. Org B with its own payment and the same Idempotency-Key must NEVER receive Org A's cached response.
	sigB := signPay(orgBKey)
	rOrgB, bOrgB := doReq(http.MethodGet, "/v1/kv?k=alpha", "org-b", "shared-idem-key", sigB)
	if rOrgB.StatusCode != http.StatusOK || !strings.Contains(bOrgB, "secret-for-org-b-alpha") || strings.Contains(bOrgB, "secret-for-org-a") {
		t.Fatalf("org-b cross-tenant isolation failed: %d %s", rOrgB.StatusCode, bOrgB)
	}

	// 4. Org B replaying Org A's stolen PAYMENT-SIGNATURE + Idempotency-Key must be rejected (402), never leaking Org A's cache.
	rStolen, bStolen := doReq(http.MethodGet, "/v1/kv?k=alpha", "org-b", "shared-idem-key", sigA)
	if rStolen.StatusCode != http.StatusPaymentRequired || strings.Contains(bStolen, "secret-for-org-a") {
		t.Fatalf("org-b with stolen sig must be rejected with 402: %d %s", rStolen.StatusCode, bStolen)
	}

	// 5. Org A reusing Idempotency-Key on a different query URL must return 409 Conflict.
	rDiffQuery, _ := doReq(http.MethodGet, "/v1/kv?k=beta", "org-a", "shared-idem-key", sigA)
	if rDiffQuery.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 on different query URL, got %d", rDiffQuery.StatusCode)
	}

	// 6. Org A reusing Idempotency-Key on a different HTTP method must return 409 Conflict.
	rDiffMethod, _ := doReq(http.MethodPost, "/v1/kv?k=alpha", "org-a", "shared-idem-key", sigA)
	if rDiffMethod.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 on different HTTP method, got %d", rDiffMethod.StatusCode)
	}

	// 7. Exact retry by Org A replays cached response without re-running handler.
	callsBefore := handlerCalls.Load()
	rReplay, bReplay := doReq(http.MethodGet, "/v1/kv?k=alpha", "org-a", "shared-idem-key", sigA)
	if rReplay.StatusCode != http.StatusOK || rReplay.Header.Get(HeaderIdempotentReplay) != "true" || bReplay != b1 {
		t.Fatalf("exact org-a retry failed: status=%d replay=%q body=%s", rReplay.StatusCode, rReplay.Header.Get(HeaderIdempotentReplay), bReplay)
	}
	if handlerCalls.Load() != callsBefore {
		t.Fatalf("handler re-executed on exact replay: before=%d after=%d", callsBefore, handlerCalls.Load())
	}

	// 8. Public /v1/x402/receipts redacts IdempotencyKey for non-admin callers.
	rRcpt, bRcpt := doReq(http.MethodGet, "/v1/x402/receipts", "org-b", "", "")
	if rRcpt.StatusCode != http.StatusOK || strings.Contains(bRcpt, "shared-idem-key") {
		t.Fatalf("public /receipts must not expose idempotency keys: %d %s", rRcpt.StatusCode, bRcpt)
	}
}

func TestReconcileReversalRequiresAuthAndReversalTx(t *testing.T) {
	providerKey := ephemeralKey(t)
	facKey := ephemeralKey(t)
	facAddr := strings.ToLower(crypto.PubkeyToAddress(facKey.PublicKey).Hex())
	wrongKey := ephemeralKey(t)
	token := "0x036CbD53842c5426634e7929541eC2318f3dCF7e"

	ledger := NewMemoryReceiptLedger(providerKey)
	settled, err := ledger.Record(context.Background(), Receipt{
		Resource:      "POST /v1/infer",
		FacilitatorID: "fac-auth",
		Payer:         "0x1111111111111111111111111111111111111111",
		PayTo:         strings.ToLower(crypto.PubkeyToAddress(providerKey.PublicKey).Hex()),
		Network:       "eip155:84532",
		Asset:         token,
		AmountAtomic:  "10000",
		ChargedAtomic: "10000",
		Nonce:         "0x01",
		Transaction:   "0xtxsettled001",
		Status:        StatusSettled,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Ledger directly rejects empty ReversalTx.
	if _, err := ledger.ReconcileReversal(context.Background(), ReversalEvent{
		ReceiptID:     settled.ReceiptID,
		FacilitatorID: "fac-auth",
		Transaction:   "0xtxsettled001",
		Reason:        "forged_without_tx",
	}); err == nil {
		t.Fatal("expected ReconcileReversal to reject empty reversalTx")
	}

	// 2. Mount with trusted facilitator signer rejects unauthenticated / unsigned / wrong-signer POST /v1/x402/reconcile.
	facSrv := httptest.NewServer(NewHTTPFacilitatorServer("fac-auth", "independent"))
	defer facSrv.Close()
	app := zip.New(zip.Config{})
	err = Mount(app, luxlog.New("test"), Config{
		Assets: []protocol.Asset{{
			Network:           "eip155:84532",
			Address:           token,
			Name:              "USD Coin",
			Version:           "2",
			Decimals:          6,
			PayTo:             settled.PayTo,
			MaxTimeoutSeconds: 60,
		}},
		Prices: map[string]protocol.Price{"POST /v1/infer": {Amount: "0.01"}},
		Facilitator: protocol.Remote{URL: facSrv.URL},
		Ledger: ledger,
		FacilitatorSigners: map[string]string{
			"fac-auth": facAddr,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	postReconcile := func(ev ReversalEvent, isAdmin bool) *http.Response {
		b, _ := json.Marshal(ev)
		req := httptest.NewRequest(http.MethodPost, "/v1/x402/reconcile", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if isAdmin {
			req.Header.Set("X-User-IsAdmin", "true")
		}
		resp, err := app.Test(req, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp
	}

	// Unsigned event rejected with 403.
	ev := ReversalEvent{
		ReceiptID:     settled.ReceiptID,
		FacilitatorID: "fac-auth",
		Transaction:   settled.Transaction,
		ReversalTx:    "0xrevtx001",
		Reason:        "reorg",
		RequireRefund: true,
	}
	if resp := postReconcile(ev, false); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for unsigned reversal event, got %d", resp.StatusCode)
	}
	// Wrong-signer event rejected with 403.
	wrongEv := ev
	if err := SignReversalEvent(wrongKey, &wrongEv); err != nil {
		t.Fatal(err)
	}
	if resp := postReconcile(wrongEv, false); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for untrusted signer reversal event, got %d", resp.StatusCode)
	}
	// Receipt must still be SETTLED.
	cur, _ := ledger.Get(context.Background(), settled.ReceiptID)
	if cur.Status != StatusSettled || cur.ChargedAtomic != "10000" {
		t.Fatalf("receipt mutated by rejected reconcile attempts: %+v", cur)
	}

	// Authentic facilitator-signed event succeeds!
	signedEv := ev
	if err := SignReversalEvent(facKey, &signedEv); err != nil {
		t.Fatal(err)
	}
	if resp := postReconcile(signedEv, false); resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for facilitator-signed reversal event, got %d", resp.StatusCode)
	}
	cur, _ = ledger.Get(context.Background(), settled.ReceiptID)
	if cur.Status != StatusRefundRequired || cur.ChargedAtomic != "0" {
		t.Fatalf("expected REFUND_REQUIRED with chargedAtomic=0, got %+v", cur)
	}

	// 3. Default mount without FacilitatorSigners/ReversalVerifier rejects non-admin caller with 403.
	appDefault := zip.New(zip.Config{})
	ledger2 := NewMemoryReceiptLedger(providerKey)
	s2, _ := ledger2.Record(context.Background(), Receipt{
		Resource:      "POST /v1/infer",
		FacilitatorID: "fac-auth",
		Payer:         "0x1111111111111111111111111111111111111111",
		PayTo:         settled.PayTo,
		Network:       "eip155:84532",
		Asset:         token,
		AmountAtomic:  "10000",
		ChargedAtomic: "10000",
		Nonce:         "0x02",
		Transaction:   "0xtxsettled002",
		Status:        StatusSettled,
	})
	if err := Mount(appDefault, luxlog.New("test"), Config{
		Assets: []protocol.Asset{{
			Network:           "eip155:84532",
			Address:           token,
			Name:              "USD Coin",
			Version:           "2",
			Decimals:          6,
			PayTo:             settled.PayTo,
			MaxTimeoutSeconds: 60,
		}},
		Prices:      map[string]protocol.Price{"POST /v1/infer": {Amount: "0.01"}},
		Facilitator: protocol.Remote{URL: facSrv.URL},
		Ledger:      ledger2,
	}); err != nil {
		t.Fatal(err)
	}
	b2, _ := json.Marshal(ReversalEvent{
		ReceiptID:     s2.ReceiptID,
		FacilitatorID: "fac-auth",
		Transaction:   s2.Transaction,
		ReversalTx:    "0xrevtx002",
		Reason:        "spoof_attempt",
	})
	reqUnauth := httptest.NewRequest(http.MethodPost, "/v1/x402/reconcile", bytes.NewReader(b2))
	reqUnauth.Header.Set("Content-Type", "application/json")
	respUnauth, err := appDefault.Test(reqUnauth, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = respUnauth.Body.Close()
	if respUnauth.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for unauthenticated caller on default /reconcile, got %d", respUnauth.StatusCode)
	}
}

func TestEnhancedModeAlwaysGuardsInFlightNoncesWhenNonceStoreNil(t *testing.T) {
	providerKey := ephemeralKey(t)
	providerAddr := strings.ToLower(crypto.PubkeyToAddress(providerKey.PublicKey).Hex())
	token := "0x036CbD53842c5426634e7929541eC2318f3dCF7e"

	facA := NewHTTPFacilitatorServer("fac-a", "independent")
	facA.VerifyDelay = 40 * time.Millisecond
	srvA := httptest.NewServer(facA)
	defer srvA.Close()

	facB := NewHTTPFacilitatorServer("fac-b", "independent")
	facB.VerifyDelay = 40 * time.Millisecond
	srvB := httptest.NewServer(facB)
	defer srvB.Close()

	// Load multi-facilitator JSON config -> enhanced mode, cfg.NonceStore == nil!
	cfgJSON := `{
		"routingStrategy": "failover",
		"facilitators": [
			{"id": "fac-a", "operator": "community-a", "url": "` + srvA.URL + `"},
			{"id": "fac-b", "operator": "community-b", "url": "` + srvB.URL + `"}
		],
		"assets": [{"network":"eip155:84532","address":"` + token + `","name":"USD Coin","version":"2","decimals":6,"payTo":"` + providerAddr + `","maxTimeoutSeconds":60}],
		"prices": {"POST /v1/infer": {"amount": "0.01", "description": "inference"}}
	}`
	cfg, err := Load(strings.NewReader(cfgJSON))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NonceStore != nil {
		t.Fatal("expected Load to leave cfg.NonceStore nil")
	}

	app := zip.New(zip.Config{})
	if err := Mount(app, luxlog.New("test"), cfg); err != nil {
		t.Fatal(err)
	}
	var handlerExecs atomic.Int64
	app.Raw(http.MethodPost, "/v1/infer", func(c *zip.Ctx) error {
		handlerExecs.Add(1)
		time.Sleep(25 * time.Millisecond)
		return c.JSON(http.StatusOK, map[string]bool{"ok": true})
	})

	agentKey := ephemeralKey(t)
	var nonce [32]byte
	_, _ = rand.Read(nonce[:])
	pay, err := protocol.Sign(protocol.PaymentRequirements{
		Scheme:            protocol.SchemeExact,
		Network:           "eip155:84532",
		Amount:            "10000",
		Asset:             token,
		PayTo:             providerAddr,
		MaxTimeoutSeconds: 60,
		Extra:             &protocol.Extra{Name: "USD Coin", Version: "2"},
	}, agentKey, nonce, 0, uint64(time.Now().Add(5*time.Minute).Unix()))
	if err != nil {
		t.Fatal(err)
	}
	sigHeader, err := protocol.EncodeHeader(pay)
	if err != nil {
		t.Fatal(err)
	}

	const workers = 12
	var okCount, blockedCount atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req := httptest.NewRequest(http.MethodPost, "/v1/infer", bytes.NewReader([]byte(`{"prompt":"race"}`)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(protocol.HeaderPaymentSignature, sigHeader)
			resp, err := app.Test(req, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
			if err != nil {
				t.Errorf("app.Test: %v", err)
				return
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				okCount.Add(1)
			} else if resp.StatusCode == http.StatusPaymentRequired {
				blockedCount.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if okCount.Load() != 1 || handlerExecs.Load() != 1 || blockedCount.Load() != workers-1 {
		t.Fatalf("expected exactly 1 execution and %d blocked, got ok=%d execs=%d blocked=%d",
			workers-1, okCount.Load(), handlerExecs.Load(), blockedCount.Load())
	}
}

func TestVerifySignedQuoteRejectsOverflowUint256WithoutPanic(t *testing.T) {
	providerAddr := strings.ToLower(crypto.PubkeyToAddress(ephemeralKey(t).PublicKey).Hex())
	two256 := new(big.Int).Lsh(big.NewInt(1), 256).String()
	now := time.Now().UTC()
	q := SignedPriceQuote{
		QuoteID:         "q_overflow",
		Version:         QuoteVersion,
		Resource:        "POST /v1/infer",
		RequestBodyHash: HashRequestBody([]byte(`{"prompt":"hi"}`)),
		Network:         "eip155:84532",
		Asset:           "0x036CbD53842c5426634e7929541eC2318f3dCF7e",
		AmountAtomic:    two256,
		PayTo:           providerAddr,
		IssuedAt:        now.Add(-time.Second).Unix(),
		ExpiresAt:       now.Add(time.Minute).Unix(),
		ProviderAddress: providerAddr,
		Signature:       "0x" + strings.Repeat("11", 65),
	}
	if _, err := q.Digest(); err == nil {
		t.Fatal("expected Digest to reject amountAtomic=2^256")
	}
	if err := VerifySignedQuoteStandalone(q, providerAddr, q.RequestBodyHash, now); err == nil {
		t.Fatal("expected VerifySignedQuoteStandalone to reject amountAtomic=2^256 without panicking")
	}
}

type failingCommitNonceStore struct {
	NonceReplayStore
	failCommit atomic.Bool
	commits    atomic.Int64
}

func (f *failingCommitNonceStore) Commit(ctx context.Context, claim NonceClaim, idemKey string, cached *CachedResponse) error {
	f.commits.Add(1)
	if f.failCommit.Load() {
		return errors.New("simulated journal disk full")
	}
	return f.NonceReplayStore.Commit(ctx, claim, idemKey, cached)
}

func TestCommitFailureDoesNotReportSuccessOrLeakUnpersistedCache(t *testing.T) {
	// 1. Unit test MemoryNonceStore journal failure retains pending entry without marking Committed.
	dir := t.TempDir()
	// Point journalPath to a directory path so OpenFile fails deterministically.
	badPath := filepath.Join(dir, "blocked-journal")
	if err := os.Mkdir(badPath, 0o700); err != nil {
		t.Fatal(err)
	}
	ns, err := NewFileNonceStore(badPath)
	if err == nil {
		// Opening a directory succeeds on Linux until ReadBytes fails with EISDIR;
		// test via a non-existent file inside a read-only directory instead.
		t.Log("unexpectedly opened dir as file")
	}
	roDir := filepath.Join(dir, "ro")
	if err := os.Mkdir(roDir, 0o700); err != nil {
		t.Fatal(err)
	}
	jPath := filepath.Join(roDir, "nonces.jsonl")
	ns, err = NewFileNonceStore(jPath)
	if err != nil {
		t.Fatal(err)
	}
	claim := NonceClaim{
		Network: "eip155:84532",
		Asset:   "0x036cbd53842c5426634e7929541ec2318f3dcf7e",
		From:    "0x1111111111111111111111111111111111111111",
		Nonce:   "0x01",
	}
	res, err := ns.Reserve(context.Background(), claim, "idem-fail-1", "req:abc")
	if err != nil || res.Outcome != ReserveAdmitted {
		t.Fatalf("reserve: %+v err=%v", res, err)
	}
	// Make directory read-only so journal creation fails.
	if err := os.Chmod(roDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(roDir, 0o700) }()

	if err := ns.Commit(context.Background(), claim, "idem-fail-1", &CachedResponse{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"ok":true}`),
	}); err == nil {
		t.Fatal("expected Commit to fail when journal directory is read-only")
	}
	if !ns.IsSpent(context.Background(), claim) {
		t.Fatal("settled nonce must remain marked spent in memory even when persistence fails")
	}
	if ns.PendingCount() != 1 {
		t.Fatalf("expected 1 pending journal entry, got %d", ns.PendingCount())
	}
	rec, ok := ns.LookupIdempotency(context.Background(), "idem-fail-1")
	if !ok || rec.Committed || !rec.PendingPersist {
		t.Fatalf("expected unpersisted record with Committed=false PendingPersist=true, got %+v", rec)
	}
	if _, err := ns.Reserve(context.Background(), claim, "idem-fail-1", "req:abc"); err == nil {
		t.Fatal("expected Reserve on unpersisted record to fail while disk is still read-only")
	}

	// Restore writability -> FlushPending recovers durably and promotes Committed=true.
	if err := os.Chmod(roDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ns.FlushPending(); err != nil {
		t.Fatalf("FlushPending failed after storage recovery: %v", err)
	}
	if ns.PendingCount() != 0 {
		t.Fatalf("expected 0 pending entries after flush, got %d", ns.PendingCount())
	}
	nsReopen, err := NewFileNonceStore(jPath)
	if err != nil {
		t.Fatal(err)
	}
	if !nsReopen.IsSpent(context.Background(), claim) {
		t.Fatal("expected flushed nonce to survive restart")
	}

	// 2. Middleware test: Commit failure must return HTTP 500 with receipt/payment headers instead of 200 OK.
	h := newTestProviderHarness(t, StrategyFailover)
	failStore := &failingCommitNonceStore{NonceReplayStore: h.nonceStore}
	failStore.failCommit.Store(true)
	h.guard.NonceStore = failStore

	agent := &AgentClient{
		HTTPClient:       h.providerSrv.Client(),
		Key:              ephemeralKey(t),
		ExpectedProvider: h.providerAdr,
		Now:              h.guard.Now,
	}
	reqBody, _ := json.Marshal(InferenceRequest{Model: "zen-7b-instruct", Prompt: "commit-fail", MaxTokens: 16})
	resp, body, _, err := agent.DoPaid(context.Background(), http.MethodPost, h.providerSrv.URL+"/v1/infer", reqBody, map[string]string{
		HeaderIdempotencyKey: "idem-commit-fail",
	})
	if err != nil {
		t.Fatalf("DoPaid transport error: %v", err)
	}
	if resp.StatusCode != http.StatusInternalServerError || !strings.Contains(string(body), "settlement_commit_failed") {
		t.Fatalf("expected 500 settlement_commit_failed, got %d %s", resp.StatusCode, string(body))
	}
	if resp.Header.Get(protocol.HeaderPaymentResponse) == "" || resp.Header.Get(HeaderReceiptID) == "" {
		t.Fatalf("expected settlement headers preserved on commit failure for reconciliation")
	}
	if failStore.commits.Load() != 3 {
		t.Fatalf("expected 3 commit retry attempts, got %d", failStore.commits.Load())
	}
}

func TestReceiptLedgerSequenceDoesNotAdvanceOnFailedAppend(t *testing.T) {
	dir := t.TempDir()
	roDir := filepath.Join(dir, "ledger-dir")
	if err := os.Mkdir(roDir, 0o700); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(roDir, "receipts.jsonl")
	key := ephemeralKey(t)
	addr := strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())

	l, err := NewFileReceiptLedger(ledgerPath, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(roDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(roDir, 0o700) }()

	// Failed first append must NOT consume sequence 1.
	if _, err := l.Record(context.Background(), Receipt{
		Resource:      "POST /v1/infer",
		FacilitatorID: "fac-a",
		Payer:         addr,
		PayTo:         addr,
		Network:       "eip155:84532",
		Asset:         "0x036CbD53842c5426634e7929541eC2318f3dCF7e",
		AmountAtomic:  "10000",
		Status:        StatusSettled,
	}); err == nil {
		t.Fatal("expected Record to fail while ledger dir is read-only")
	}

	// Restore permissions: first successful receipt MUST be seq=1, second seq=2.
	if err := os.Chmod(roDir, 0o700); err != nil {
		t.Fatal(err)
	}
	r1, err := l.Record(context.Background(), Receipt{
		Resource:      "POST /v1/infer",
		FacilitatorID: "fac-a",
		Payer:         addr,
		PayTo:         addr,
		Network:       "eip155:84532",
		Asset:         "0x036CbD53842c5426634e7929541eC2318f3dCF7e",
		AmountAtomic:  "10000",
		Status:        StatusSettled,
	})
	if err != nil {
		t.Fatalf("first successful Record failed: %v", err)
	}
	if r1.Seq != 1 {
		t.Fatalf("expected seq=1 after prior failed append, got %d", r1.Seq)
	}
	r2, err := l.Record(context.Background(), Receipt{
		Resource:      "POST /v1/infer",
		FacilitatorID: "fac-a",
		Payer:         addr,
		PayTo:         addr,
		Network:       "eip155:84532",
		Asset:         "0x036CbD53842c5426634e7929541eC2318f3dCF7e",
		AmountAtomic:  "10000",
		Status:        StatusSettled,
	})
	if err != nil {
		t.Fatalf("second Record failed: %v", err)
	}
	if r2.Seq != 2 {
		t.Fatalf("expected seq=2, got %d", r2.Seq)
	}

	var buf bytes.Buffer
	if err := l.ExportJSONL(&buf); err != nil {
		t.Fatal(err)
	}
	ver, err := VerifyExportJSONL(bytes.NewReader(buf.Bytes()), addr)
	if err != nil {
		t.Fatalf("VerifyExportJSONL failed after recovered append: %v", err)
	}
	if !ver.ChainIntact || !ver.SignaturesIntact || ver.TotalEntries != 2 {
		t.Fatalf("unexpected export verification: %+v", ver)
	}
}

func TestFileNonceStoreReadsLargeCachedResponseOver4MiB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large-nonces.jsonl")
	ns1, err := NewFileNonceStore(path)
	if err != nil {
		t.Fatal(err)
	}
	claim := NonceClaim{
		Network: "eip155:84532",
		Asset:   "0x036cbd53842c5426634e7929541ec2318f3dcf7e",
		From:    "0x2222222222222222222222222222222222222222",
		Nonce:   "0x99",
	}
	largeBody := bytes.Repeat([]byte("A"), 4*1024*1024+128*1024) // >4 MiB raw -> >5.5 MiB base64 JSONL line
	res, err := ns1.Reserve(context.Background(), claim, "idem-large-1", "req:large")
	if err != nil || res.Outcome != ReserveAdmitted {
		t.Fatalf("reserve: %+v err=%v", res, err)
	}
	if err := ns1.Commit(context.Background(), claim, "idem-large-1", &CachedResponse{
		StatusCode:  http.StatusOK,
		ContentType: "application/octet-stream",
		Body:        largeBody,
		ReceiptID:   "rcpt-large",
	}); err != nil {
		t.Fatalf("commit large response: %v", err)
	}

	// Reopening must succeed without bufio.Scanner token-too-long error.
	ns2, err := NewFileNonceStore(path)
	if err != nil {
		t.Fatalf("reopen large nonce journal failed: %v", err)
	}
	if !ns2.IsSpent(context.Background(), claim) {
		t.Fatal("expected spent claim after reopening large journal")
	}
	hit, err := ns2.Reserve(context.Background(), claim, "idem-large-1", "req:large")
	if err != nil || hit.Outcome != ReserveIdempotentHit || hit.Cached == nil {
		t.Fatalf("expected idempotent hit after reopen, got %+v err=%v", hit, err)
	}
	if !bytes.Equal(hit.Cached.Body, largeBody) {
		t.Fatalf("cached body length mismatch: got %d want %d", len(hit.Cached.Body), len(largeBody))
	}
}
