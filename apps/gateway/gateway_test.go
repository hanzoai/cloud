// SPDX-License-Identifier: Apache-2.0 OR MIT

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

type testTopology struct {
	domain        EIP712Domain
	hanzoDir      *DirectoryPeerServer
	communityDirA *DirectoryPeerServer
	communityDirB *DirectoryPeerServer
	workers       []*WorkerNode
	hanzoGW       *Gateway
	communityGW   *Gateway
	hanzoSrv      *httptest.Server
	communitySrv  *httptest.Server
}

func (top *testTopology) close() {
	if top.hanzoSrv != nil {
		top.hanzoSrv.Close()
	}
	if top.communitySrv != nil {
		top.communitySrv.Close()
	}
	for _, w := range top.workers {
		w.Close()
	}
	if top.hanzoDir != nil {
		top.hanzoDir.Close()
	}
	if top.communityDirA != nil {
		top.communityDirA.Close()
	}
	if top.communityDirB != nil {
		top.communityDirB.Close()
	}
}

func setupDualGatewayTopology(t *testing.T) *testTopology {
	t.Helper()
	domain := DefaultDomain()
	models := []ModelSpec{
		{
			ModelID:          "zen-7b-instruct",
			ModelDigest:      "sha256:" + SHA256Hex([]byte("zen-7b-instruct-open-weights-v1")),
			MaxContextTokens: 32768,
			License:          "Apache-2.0",
		},
		{
			ModelID:          "zen-32b-coder",
			ModelDigest:      "sha256:" + SHA256Hex([]byte("zen-32b-coder-open-weights-v1")),
			MaxContextTokens: 65536,
			License:          "Apache-2.0",
		},
	}

	hanzoDirKP, err := KeyPairFromSeed([]byte("test-hanzo-dir-peer"))
	if err != nil {
		t.Fatal(err)
	}
	commDirAKP, err := KeyPairFromSeed([]byte("test-comm-dir-peer-a"))
	if err != nil {
		t.Fatal(err)
	}
	commDirBKP, err := KeyPairFromSeed([]byte("test-comm-dir-peer-b"))
	if err != nil {
		t.Fatal(err)
	}

	hanzoDir, err := NewDirectoryPeerServer("dir-hanzo-convenience", "Hanzo Convenience Directory", domain, hanzoDirKP)
	if err != nil {
		t.Fatal(err)
	}
	commDirA, err := NewDirectoryPeerServer("dir-community-alpha", "Community Peer Alpha", domain, commDirAKP)
	if err != nil {
		t.Fatal(err)
	}
	commDirB, err := NewDirectoryPeerServer("dir-community-beta", "Community Peer Beta", domain, commDirBKP)
	if err != nil {
		t.Fatal(err)
	}

	workerSpecs := []struct {
		id     string
		region string
		price  uint64
		ttft   int64
	}{
		{"worker-alpha-lowcost", "us-east", 280_000, 35},
		{"worker-beta-lowttft", "eu-west", 440_000, 12},
		{"worker-gamma-balanced", "ap-northeast", 340_000, 18},
	}

	workers := make([]*WorkerNode, 0, len(workerSpecs))
	for _, spec := range workerSpecs {
		kp, err := KeyPairFromSeed([]byte("test-worker-" + spec.id))
		if err != nil {
			t.Fatal(err)
		}
		w, err := NewWorkerNode(WorkerConfig{
			ProviderID:         spec.id,
			Region:             spec.region,
			Models:             models,
			PriceMicroUSDPer1M: spec.price,
			AdvertisedTTFTMs:   spec.ttft,
			Domain:             domain,
			KeyPair:            kp,
		})
		if err != nil {
			t.Fatal(err)
		}
		workers = append(workers, w)
		rec, err := w.SignedRecord(time.Now().Unix(), 3600)
		if err != nil {
			t.Fatal(err)
		}
		// Workers publish directly to community peer A (not Hanzo-locked).
		if err := commDirA.RegisterWorker(rec); err != nil {
			t.Fatal(err)
		}
	}

	// Open gossip propagates worker records to Community Peer B and Hanzo Directory.
	ctx := context.Background()
	if err := commDirA.GossipToPeer(ctx, commDirB.URL(), nil); err != nil {
		t.Fatalf("gossip A->B: %v", err)
	}
	if err := commDirB.GossipToPeer(ctx, hanzoDir.URL(), nil); err != nil {
		t.Fatalf("gossip B->Hanzo: %v", err)
	}

	hanzoGWKP, err := KeyPairFromSeed([]byte("test-hanzo-gateway"))
	if err != nil {
		t.Fatal(err)
	}
	commGWKP, err := KeyPairFromSeed([]byte("test-community-gateway"))
	if err != nil {
		t.Fatal(err)
	}

	hanzoGW, err := NewGateway(GatewayConfig{
		GatewayID:       "hanzo-enso-gateway-1",
		OperatorName:    "Hanzo Convenience Gateway",
		OperatorType:    OperatorTypeHanzo,
		Strategy:        StrategyWeightedSLOScore,
		GatewayFeeBps:   25,
		Domain:          domain,
		KeyPair:         hanzoGWKP,
		DirectoryURLs:   []string{hanzoDir.URL(), commDirA.URL()},
		ProviderTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	commGW, err := NewGateway(GatewayConfig{
		GatewayID:       "community-enso-gateway-1",
		OperatorName:    "Independent Community Gateway",
		OperatorType:    OperatorTypeCommunity,
		Strategy:        StrategyWeightedSLOScore,
		GatewayFeeBps:   10,
		Domain:          domain,
		KeyPair:         commGWKP,
		DirectoryURLs:   []string{commDirA.URL(), commDirB.URL()},
		ProviderTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := hanzoGW.SyncDirectory(ctx); err != nil {
		t.Fatalf("sync hanzo gateway: %v", err)
	}
	if _, err := commGW.SyncDirectory(ctx); err != nil {
		t.Fatalf("sync community gateway: %v", err)
	}

	top := &testTopology{
		domain:        domain,
		hanzoDir:      hanzoDir,
		communityDirA: commDirA,
		communityDirB: commDirB,
		workers:       workers,
		hanzoGW:       hanzoGW,
		communityGW:   commGW,
		hanzoSrv:      StartLoopbackServer(hanzoGW),
		communitySrv:  StartLoopbackServer(commGW),
	}
	t.Cleanup(top.close)
	return top
}

func TestProtocolQuoteAndReceiptCrypto(t *testing.T) {
	domain := DefaultDomain()
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	req := ChatCompletionRequest{
		Model:     "zen-7b-instruct",
		Messages:  []ChatMessage{{Role: "user", Content: "Verify EIP-712 quote and receipt"}},
		MaxTokens: 64,
	}
	reqHash := CanonicalRequestHash(req)
	quote := InferenceQuote{
		QuoteID:            "qt-crypto-001",
		ProtocolVersion:    ProtocolVersion,
		ProviderID:         "worker-crypto",
		ModelID:            "zen-7b-instruct",
		ModelDigest:        "sha256:" + SHA256Hex([]byte("weights")),
		PriceMicroUSDPer1M: 320_000,
		MaxTTFTMs:          15,
		MaxTokens:          64,
		IssuedAt:           now,
		ExpiresAt:          now + 60,
		Nonce:              "nonce-crypto-001",
		RequestHash:        reqHash,
	}
	if err := SignQuote(domain, kp, &quote); err != nil {
		t.Fatalf("SignQuote: %v", err)
	}
	if err := VerifyQuote(domain, quote, now); err != nil {
		t.Fatalf("VerifyQuote valid quote failed: %v", err)
	}

	// Verify EIP-2 high-S malleated quote is rejected fail-closed.
	highSQuote := quote
	highSQuote.Signature, err = MalleateSignatureHighS(quote.Signature)
	if err != nil {
		t.Fatalf("MalleateSignatureHighS: %v", err)
	}
	if err := VerifyQuote(domain, highSQuote, now); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature for high-S quote, got %v", err)
	}

	// Sign and verify ExecutionReceipt offline.
	qh, err := QuoteHash(domain, quote)
	if err != nil {
		t.Fatal(err)
	}
	content := "deterministic verifiable inference output"
	rcpt := ExecutionReceipt{
		ReceiptID:         "rcpt-crypto-001",
		ProtocolVersion:   ProtocolVersion,
		VerificationClass: ReceiptVerificationClass,
		GatewayID:         "community-gw",
		GatewayOperator:   "Community Operator",
		ProviderID:        quote.ProviderID,
		ModelID:           quote.ModelID,
		ModelDigest:       quote.ModelDigest,
		QuoteID:           quote.QuoteID,
		QuoteHash:         qh,
		QuoteNonce:        quote.Nonce,
		RequestHash:       reqHash,
		OutputHash:        ComputeOutputHash(content),
		PromptTokens:      12,
		CompletionTokens:  24,
		TotalTokens:       36,
		TTFTMs:            11.5,
		TotalLatencyMs:    18.2,
		CompletedAt:       now,
	}
	if err := SignReceipt(domain, kp, &rcpt); err != nil {
		t.Fatalf("SignReceipt: %v", err)
	}
	if err := VerifyReceipt(domain, rcpt, &quote, reqHash, content); err != nil {
		t.Fatalf("VerifyReceipt valid receipt failed: %v", err)
	}
	if err := VerifyReceipt(domain, rcpt, &quote, reqHash, content+" tampered"); !errors.Is(err, ErrOutputHashMismatch) {
		t.Fatalf("expected ErrOutputHashMismatch on modified content, got %v", err)
	}
}

func TestTwoIndependentGatewaysIdenticalOpenAIRequests(t *testing.T) {
	top := setupDualGatewayTopology(t)
	ctx := context.Background()

	req := ChatCompletionRequest{
		Model:     "zen-7b-instruct",
		Messages:  []ChatMessage{{Role: "user", Content: "Compare Hanzo and Community Enso gateways"}},
		MaxTokens: 48,
	}
	reqHash := CanonicalRequestHash(req)

	// 1. Live TCP loopback HTTP via HanzoGateway and CommunityGateway.
	hanzoClient := NewMultiGatewayClient(top.domain, []GatewayEndpoint{
		{GatewayID: top.hanzoGW.GatewayID(), OperatorType: OperatorTypeHanzo, BaseURL: top.hanzoSrv.URL, Enabled: true},
	}, nil)
	commClient := NewMultiGatewayClient(top.domain, []GatewayEndpoint{
		{GatewayID: top.communityGW.GatewayID(), OperatorType: OperatorTypeCommunity, BaseURL: top.communitySrv.URL, Enabled: true},
	}, nil)

	hanzoModels, err := hanzoClient.ListModels(ctx)
	if err != nil {
		t.Fatalf("Hanzo /v1/models: %v", err)
	}
	commModels, err := commClient.ListModels(ctx)
	if err != nil {
		t.Fatalf("Community /v1/models: %v", err)
	}
	if len(hanzoModels.Data) != 2 || len(commModels.Data) != 2 {
		t.Fatalf("expected identical 2 models on both gateways, got hanzo=%d comm=%d", len(hanzoModels.Data), len(commModels.Data))
	}

	hanzoResp, err := hanzoClient.ChatCompletions(ctx, req)
	if err != nil {
		t.Fatalf("Hanzo /v1/chat/completions: %v", err)
	}
	commResp, err := commClient.ChatCompletions(ctx, req)
	if err != nil {
		t.Fatalf("Community /v1/chat/completions: %v", err)
	}

	if hanzoResp.Object != "chat.completion" || commResp.Object != "chat.completion" {
		t.Fatalf("unexpected OpenAI object type: hanzo=%q comm=%q", hanzoResp.Object, commResp.Object)
	}
	if hanzoResp.Choices[0].Message.Content != commResp.Choices[0].Message.Content {
		t.Fatalf("expected identical OpenAI completion content across independent gateways:\nhanzo=%q\ncomm =%q",
			hanzoResp.Choices[0].Message.Content, commResp.Choices[0].Message.Content)
	}
	if err := VerifyReceipt(top.domain, *hanzoResp.EnsoReceipt, hanzoResp.EnsoQuote, reqHash, hanzoResp.Choices[0].Message.Content); err != nil {
		t.Fatalf("Hanzo receipt offline verify failed: %v", err)
	}
	if err := VerifyReceipt(top.domain, *commResp.EnsoReceipt, commResp.EnsoQuote, reqHash, commResp.Choices[0].Message.Content); err != nil {
		t.Fatalf("Community receipt offline verify failed: %v", err)
	}
	if hanzoResp.EnsoRoute.GatewayID == commResp.EnsoRoute.GatewayID {
		t.Fatalf("expected two distinct gateway IDs, both were %q", hanzoResp.EnsoRoute.GatewayID)
	}
	// Community gateway competes on lower routing fee (10 bps vs 25 bps).
	if commResp.EnsoRoute.EffectivePriceMicroUSDPer1M >= hanzoResp.EnsoRoute.EffectivePriceMicroUSDPer1M {
		t.Fatalf("expected community gateway fee competition to yield lower effective price: comm=%d hanzo=%d",
			commResp.EnsoRoute.EffectivePriceMicroUSDPer1M, hanzoResp.EnsoRoute.EffectivePriceMicroUSDPer1M)
	}
}

func TestKillSwitchHanzoDisabledAcceptance(t *testing.T) {
	top := setupDualGatewayTopology(t)
	ctx := context.Background()

	client := NewMultiGatewayClient(top.domain, []GatewayEndpoint{
		{GatewayID: top.hanzoGW.GatewayID(), OperatorType: OperatorTypeHanzo, BaseURL: top.hanzoSrv.URL, Enabled: true},
		{GatewayID: top.communityGW.GatewayID(), OperatorType: OperatorTypeCommunity, BaseURL: top.communitySrv.URL, Enabled: true},
	}, nil)

	// Engage full Hanzo Kill Switch: disable AND close Hanzo gateway + Hanzo directory,
	// and disable all Hanzo endpoints in the client routing table.
	top.hanzoGW.Disable()
	top.hanzoSrv.Close()
	top.hanzoDir.Disable()
	top.hanzoDir.Close()
	top.hanzoGW.ResetCounters()
	client.DisableOperatorType(OperatorTypeHanzo)
	client.ResetCounters()

	const totalRequests = 25
	completed := 0
	for i := 0; i < totalRequests; i++ {
		req := ChatCompletionRequest{
			Model:     "zen-7b-instruct",
			Messages:  []ChatMessage{{Role: "user", Content: fmt.Sprintf("post-killswitch request %d", i)}},
			MaxTokens: 32,
		}
		resp, err := client.ChatCompletions(ctx, req)
		if err != nil {
			t.Fatalf("request %d failed with Hanzo disabled: %v", i, err)
		}
		if resp.EnsoRoute.OperatorType != OperatorTypeCommunity {
			t.Fatalf("expected community operator, got %q", resp.EnsoRoute.OperatorType)
		}
		if err := VerifyReceipt(top.domain, *resp.EnsoReceipt, resp.EnsoQuote, CanonicalRequestHash(req), resp.Choices[0].Message.Content); err != nil {
			t.Fatalf("offline receipt verification failed on request %d: %v", i, err)
		}
		completed++
	}

	if completed != totalRequests {
		t.Fatalf("expected 100%% completion (%d/%d), got %d", totalRequests, totalRequests, completed)
	}
	if client.HanzoCalls() != 0 {
		t.Fatalf("expected client HanzoGatewayCalls == 0, got %d", client.HanzoCalls())
	}
	if top.hanzoGW.Calls() != 0 {
		t.Fatalf("expected server HanzoGateway.Calls() == 0, got %d", top.hanzoGW.Calls())
	}
	if client.CommunityCalls() != totalRequests {
		t.Fatalf("expected %d community gateway calls, got %d", totalRequests, client.CommunityCalls())
	}
}

func TestSchedulerStrategiesAndProviderFailover(t *testing.T) {
	top := setupDualGatewayTopology(t)
	ctx := context.Background()

	// 1. lowest_cost picks worker-alpha-lowcost (280k microUSD/1M).
	respCost, err := top.communityGW.ChatCompletions(ctx, ChatCompletionRequest{
		Model:     "zen-7b-instruct",
		Messages:  []ChatMessage{{Role: "user", Content: "strategy lowest cost"}},
		MaxTokens: 32,
		Strategy:  string(StrategyLowestCost),
	})
	if err != nil {
		t.Fatalf("lowest_cost failed: %v", err)
	}
	if respCost.EnsoRoute.ProviderID != "worker-alpha-lowcost" {
		t.Fatalf("lowest_cost chose %q, want worker-alpha-lowcost", respCost.EnsoRoute.ProviderID)
	}

	// 2. lowest_ttft picks worker-beta-lowttft (12ms TTFT).
	respTTFT, err := top.communityGW.ChatCompletions(ctx, ChatCompletionRequest{
		Model:     "zen-7b-instruct",
		Messages:  []ChatMessage{{Role: "user", Content: "strategy lowest ttft"}},
		MaxTokens: 32,
		Strategy:  string(StrategyLowestTTFT),
	})
	if err != nil {
		t.Fatalf("lowest_ttft failed: %v", err)
	}
	if respTTFT.EnsoRoute.ProviderID != "worker-beta-lowttft" {
		t.Fatalf("lowest_ttft chose %q, want worker-beta-lowttft", respTTFT.EnsoRoute.ProviderID)
	}

	// 3. Mid-flight HTTP 500 crash on primary cheapest worker -> automatic failover to peer.
	top.workers[0].SetFaultMode(FaultCrash500)
	respCrashFailover, err := top.communityGW.ChatCompletions(ctx, ChatCompletionRequest{
		Model:     "zen-7b-instruct",
		Messages:  []ChatMessage{{Role: "user", Content: "midflight crash failover"}},
		MaxTokens: 32,
		Strategy:  string(StrategyLowestCost),
	})
	if err != nil {
		t.Fatalf("crash failover failed: %v", err)
	}
	if !respCrashFailover.EnsoRoute.FailoverOccurred || respCrashFailover.EnsoRoute.ProviderID == "worker-alpha-lowcost" {
		t.Fatalf("expected failover away from crashed worker-alpha-lowcost, got route=%+v", respCrashFailover.EnsoRoute)
	}

	// 4. Mid-flight timeout on primary lowest-TTFT worker -> automatic failover to peer.
	top.workers[1].SetFaultMode(FaultTimeout)
	top.workers[1].SetTimeoutDelay(400 * time.Millisecond)

	shortTimeoutGW, err := NewGateway(GatewayConfig{
		GatewayID:       "gw-timeout-failover",
		OperatorName:    "Timeout Failover Tester",
		OperatorType:    OperatorTypeCommunity,
		Strategy:        StrategyLowestTTFT,
		Domain:          top.domain,
		DirectoryURLs:   []string{top.communityDirA.URL()},
		ProviderTimeout: 80 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := shortTimeoutGW.SyncDirectory(ctx); err != nil {
		t.Fatal(err)
	}
	respTimeoutFailover, err := shortTimeoutGW.ChatCompletions(ctx, ChatCompletionRequest{
		Model:     "zen-7b-instruct",
		Messages:  []ChatMessage{{Role: "user", Content: "timeout failover"}},
		MaxTokens: 32,
		Strategy:  string(StrategyFailoverResilient),
	})
	if err != nil {
		t.Fatalf("timeout failover failed: %v", err)
	}
	if respTimeoutFailover.EnsoRoute.ProviderID != "worker-gamma-balanced" {
		t.Fatalf("expected failover to healthy worker-gamma-balanced, got %q", respTimeoutFailover.EnsoRoute.ProviderID)
	}
}

func TestAdversarialFailClosedSuite(t *testing.T) {
	domain := DefaultDomain()
	ctx := context.Background()

	newSingleWorkerGW := func(t *testing.T, id string, mode FaultMode) (*WorkerNode, *Gateway) {
		t.Helper()
		kp, err := KeyPairFromSeed([]byte("adv-" + id))
		if err != nil {
			t.Fatal(err)
		}
		w, err := NewWorkerNode(WorkerConfig{
			ProviderID:         id,
			Region:             "eu-central",
			Models:             defaultTestModels(),
			PriceMicroUSDPer1M: 300_000,
			AdvertisedTTFTMs:   10,
			Domain:             domain,
			KeyPair:            kp,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(w.Close)
		w.SetFaultMode(mode)
		rec, err := w.SignedRecord(time.Now().Unix(), 3600)
		if err != nil {
			t.Fatal(err)
		}
		gw, err := NewGateway(GatewayConfig{
			GatewayID:       "gw-adv-" + id,
			OperatorName:    "Adversarial Verifier",
			OperatorType:    OperatorTypeCommunity,
			Strategy:        StrategyLowestCost,
			Domain:          domain,
			StaticWorkers:   []WorkerRecord{rec},
			FailoverEnabled: BoolPtr(false),
			VerifyReceipts:  BoolPtr(true),
		})
		if err != nil {
			t.Fatal(err)
		}
		return w, gw
	}

	req := ChatCompletionRequest{
		Model:     "zen-7b-instruct",
		Messages:  []ChatMessage{{Role: "user", Content: "adversarial security probe"}},
		MaxTokens: 32,
	}

	t.Run("forged_quote_signature_rejected", func(t *testing.T) {
		_, gw := newSingleWorkerGW(t, "forged-quote", FaultForgedQuoteSig)
		_, err := gw.ChatCompletions(ctx, req)
		if !errors.Is(err, ErrInvalidSignature) && !errors.Is(err, ErrSignerMismatch) {
			t.Fatalf("expected forged quote signature error, got %v", err)
		}
	})

	t.Run("high_s_malleated_quote_rejected", func(t *testing.T) {
		_, gw := newSingleWorkerGW(t, "high-s-quote", FaultHighSSignature)
		_, err := gw.ChatCompletions(ctx, req)
		if !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("expected ErrInvalidSignature for high-S quote, got %v", err)
		}
	})

	t.Run("expired_quote_rejected", func(t *testing.T) {
		_, gw := newSingleWorkerGW(t, "expired-quote", FaultExpiredQuote)
		_, err := gw.ChatCompletions(ctx, req)
		if !errors.Is(err, ErrQuoteExpired) {
			t.Fatalf("expected ErrQuoteExpired, got %v", err)
		}
	})

	t.Run("mismatched_output_hash_receipt_rejected", func(t *testing.T) {
		_, gw := newSingleWorkerGW(t, "mismatched-hash", FaultMismatchedReceiptHash)
		_, err := gw.ChatCompletions(ctx, req)
		if !errors.Is(err, ErrOutputHashMismatch) {
			t.Fatalf("expected ErrOutputHashMismatch, got %v", err)
		}
	})

	t.Run("corrupted_output_content_rejected", func(t *testing.T) {
		_, gw := newSingleWorkerGW(t, "corrupt-output", FaultCorruptOutput)
		_, err := gw.ChatCompletions(ctx, req)
		if !errors.Is(err, ErrOutputHashMismatch) {
			t.Fatalf("expected ErrOutputHashMismatch, got %v", err)
		}
	})

	t.Run("replay_nonce_rejected", func(t *testing.T) {
		_, gw := newSingleWorkerGW(t, "replay-nonce", FaultReplayNonce)
		if _, err := gw.ChatCompletions(ctx, req); err != nil {
			t.Fatalf("first execution with static nonce should succeed, got %v", err)
		}
		if _, err := gw.ChatCompletions(ctx, req); !errors.Is(err, ErrReplayNonce) {
			t.Fatalf("expected ErrReplayNonce on second execution with reused nonce, got %v", err)
		}
	})

	t.Run("tampered_worker_record_rejected", func(t *testing.T) {
		w, _ := newSingleWorkerGW(t, "tampered-record", FaultNone)
		rec, err := w.SignedRecord(time.Now().Unix(), 3600)
		if err != nil {
			t.Fatal(err)
		}
		rec.PriceMicroUSDPer1M = 1 // tamper price after signing
		dir := NewPeerDirectory(domain)
		if err := dir.RegisterWorker(rec, time.Now().Unix()); err == nil {
			t.Fatalf("expected tampered worker record to be rejected")
		}
	})
}

func TestConcurrent16ClientsRaceStress(t *testing.T) {
	top := setupDualGatewayTopology(t)
	const clients = 16
	const reqsPerClient = 6

	strategies := []Strategy{
		StrategyLowestCost,
		StrategyLowestTTFT,
		StrategyWeightedSLOScore,
		StrategyFailoverResilient,
	}

	var wg sync.WaitGroup
	errCh := make(chan error, clients*reqsPerClient)

	for c := 0; c < clients; c++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()
			ctx := context.Background()
			ep := GatewayEndpoint{
				GatewayID:    top.communityGW.GatewayID(),
				OperatorType: OperatorTypeCommunity,
				BaseURL:      top.communitySrv.URL,
				Enabled:      true,
			}
			if clientID%2 == 0 {
				ep = GatewayEndpoint{
					GatewayID:    top.hanzoGW.GatewayID(),
					OperatorType: OperatorTypeHanzo,
					BaseURL:      top.hanzoSrv.URL,
					Enabled:      true,
				}
			}
			cli := NewMultiGatewayClient(top.domain, []GatewayEndpoint{ep}, nil)
			for r := 0; r < reqsPerClient; r++ {
				req := ChatCompletionRequest{
					Model:     "zen-7b-instruct",
					Messages:  []ChatMessage{{Role: "user", Content: fmt.Sprintf("client-%d-req-%d", clientID, r)}},
					MaxTokens: 32,
					Strategy:  string(strategies[(clientID+r)%len(strategies)]),
				}
				resp, err := cli.ChatCompletions(ctx, req)
				if err != nil {
					errCh <- fmt.Errorf("client %d req %d: %w", clientID, r, err)
					return
				}
				if err := VerifyReceipt(top.domain, *resp.EnsoReceipt, resp.EnsoQuote, CanonicalRequestHash(req), resp.Choices[0].Message.Content); err != nil {
					errCh <- fmt.Errorf("client %d req %d receipt verify: %w", clientID, r, err)
					return
				}
			}
		}(c)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

func TestSovereigntyConfigDirectoryExportAndFork(t *testing.T) {
	top := setupDualGatewayTopology(t)
	ctx := context.Background()

	// Export stateless config + verified worker directory from CommunityGateway.
	bundle, err := top.communityGW.ExportBundle()
	if err != nil {
		t.Fatalf("ExportBundle: %v", err)
	}
	if err := VerifyExportBundle(top.domain, *bundle, time.Now().Unix()); err != nil {
		t.Fatalf("VerifyExportBundle: %v", err)
	}

	// Tampered bundle must fail verification fail-closed.
	tampered := *bundle
	tampered.Config.GatewayFeeBps = 9999
	if err := VerifyExportBundle(top.domain, tampered, time.Now().Unix()); !errors.Is(err, ErrDirectoryCorrupted) {
		t.Fatalf("expected ErrDirectoryCorrupted for tampered bundle, got %v", err)
	}

	// Shut down ALL Hanzo infrastructure before booting forked third-party gateway.
	top.hanzoGW.Disable()
	top.hanzoSrv.Close()
	top.hanzoDir.Disable()
	top.hanzoDir.Close()
	top.hanzoGW.ResetCounters()

	forkKP, err := KeyPairFromSeed([]byte("operator-c-forked-gateway"))
	if err != nil {
		t.Fatal(err)
	}
	forkGW, err := NewGatewayFromBundle(*bundle, "operator-c-fork-gw", "Operator C Fork", OperatorTypeCommunity, 0, forkKP, nil)
	if err != nil {
		t.Fatalf("NewGatewayFromBundle: %v", err)
	}
	forkSrv := StartLoopbackServer(forkGW)
	defer forkSrv.Close()

	forkClient := NewMultiGatewayClient(top.domain, []GatewayEndpoint{
		{GatewayID: forkGW.GatewayID(), OperatorType: OperatorTypeCommunity, BaseURL: forkSrv.URL, Enabled: true},
	}, nil)

	req := ChatCompletionRequest{
		Model:     "zen-32b-coder",
		Messages:  []ChatMessage{{Role: "user", Content: "forked gateway inference test"}},
		MaxTokens: 32,
	}
	resp, err := forkClient.ChatCompletions(ctx, req)
	if err != nil {
		t.Fatalf("forked gateway ChatCompletions failed: %v", err)
	}
	if resp.EnsoRoute.GatewayID != "operator-c-fork-gw" || resp.EnsoRoute.GatewayFeeBps != 0 {
		t.Fatalf("unexpected forked gateway route: %+v", resp.EnsoRoute)
	}
	if top.hanzoGW.Calls() != 0 {
		t.Fatalf("expected 0 Hanzo calls during forked gateway execution, got %d", top.hanzoGW.Calls())
	}
}

func TestThreeNegativeAndBoundaryRegimes(t *testing.T) {
	res, err := RunNegativeAndBoundaryRegimes(DefaultDomain(), 20)
	if err != nil {
		t.Fatalf("RunNegativeAndBoundaryRegimes: %v", err)
	}
	if len(res) != 3 {
		t.Fatalf("expected 3 regimes, got %d", len(res))
	}
	wantNames := []string{
		"unverified_receipt_negative",
		"single_provider_no_failover_negative",
		"stale_quote_storm_boundary",
	}
	for i, r := range res {
		if r.Name != wantNames[i] {
			t.Errorf("regime[%d].Name = %q, want %q", i, r.Name, wantNames[i])
		}
		if !r.Validated {
			t.Errorf("regime %s failed validation: %+v", r.Name, r)
		}
	}
}

func TestZIPAppSurfaceEndToEnd(t *testing.T) {
	top := setupDualGatewayTopology(t)
	app := zip.New(zip.Config{})
	if err := Mount(app, luxlog.New("gateway-test"), top.communityGW); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	callZIP := func(method, path string, body any) (*http.Response, []byte) {
		t.Helper()
		var r io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			r = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, r)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := app.Test(req, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("%s %s failed: %v", method, path, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp, raw
	}

	// GET /v1/gateway/health
	resp, raw := callZIP(http.MethodGet, "/v1/gateway/health", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/v1/gateway/health status=%d body=%s", resp.StatusCode, string(raw))
	}

	// GET /v1/models
	resp, raw = callZIP(http.MethodGet, "/v1/models", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/v1/models status=%d body=%s", resp.StatusCode, string(raw))
	}
	var models ModelsListResponse
	if err := json.Unmarshal(raw, &models); err != nil || len(models.Data) != 2 {
		t.Fatalf("invalid /v1/models response: err=%v count=%d", err, len(models.Data))
	}

	// GET /v1/gateway/providers
	resp, raw = callZIP(http.MethodGet, "/v1/gateway/providers", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/v1/gateway/providers status=%d body=%s", resp.StatusCode, string(raw))
	}

	// POST /v1/gateway/quotes
	resp, raw = callZIP(http.MethodPost, "/v1/gateway/quotes", QuotesQuery{Model: "zen-7b-instruct", MaxTokens: 32, Strategy: string(StrategyLowestCost)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/v1/gateway/quotes status=%d body=%s", resp.StatusCode, string(raw))
	}

	// POST /v1/chat/completions
	chatReq := ChatCompletionRequest{
		Model:     "zen-7b-instruct",
		Messages:  []ChatMessage{{Role: "user", Content: "zip mounted OpenAI completion"}},
		MaxTokens: 32,
	}
	resp, raw = callZIP(http.MethodPost, "/v1/chat/completions", chatReq)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/v1/chat/completions status=%d body=%s", resp.StatusCode, string(raw))
	}
	var chatResp ChatCompletionResponse
	if err := json.Unmarshal(raw, &chatResp); err != nil || chatResp.EnsoReceipt == nil {
		t.Fatalf("invalid chat response: err=%v body=%s", err, string(raw))
	}

	// POST /v1/gateway/receipts/verify
	resp, raw = callZIP(http.MethodPost, "/v1/gateway/receipts/verify", VerifyReceiptRequest{
		Receipt:         *chatResp.EnsoReceipt,
		Quote:           chatResp.EnsoQuote,
		RequestHash:     CanonicalRequestHash(chatReq),
		ResponseContent: StringPtr(chatResp.Choices[0].Message.Content),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/v1/gateway/receipts/verify status=%d body=%s", resp.StatusCode, string(raw))
	}

	// GET /v1/gateway/export
	resp, raw = callZIP(http.MethodGet, "/v1/gateway/export", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/v1/gateway/export status=%d body=%s", resp.StatusCode, string(raw))
	}
	var bundle GatewayExportBundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatalf("unmarshal export bundle: %v", err)
	}
	if err := VerifyExportBundle(top.domain, bundle, time.Now().Unix()); err != nil {
		t.Fatalf("VerifyExportBundle from zip export failed: %v", err)
	}

	// Second independent zip.App mounting HanzoGateway serves identical completion content.
	hanzoApp := zip.New(zip.Config{})
	if err := Mount(hanzoApp, luxlog.New("hanzo-gateway-test"), top.hanzoGW); err != nil {
		t.Fatalf("Mount HanzoGateway: %v", err)
	}
	bodyBytes, err := json.Marshal(chatReq)
	if err != nil {
		t.Fatal(err)
	}
	hReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(bodyBytes))
	hReq.Header.Set("Content-Type", "application/json")
	hResp, err := hanzoApp.Test(hReq, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("hanzo zip.App /v1/chat/completions: %v", err)
	}
	defer hResp.Body.Close()
	hRaw, _ := io.ReadAll(hResp.Body)
	if hResp.StatusCode != http.StatusOK {
		t.Fatalf("hanzo zip.App status=%d body=%s", hResp.StatusCode, string(hRaw))
	}
	var hanzoChat ChatCompletionResponse
	if err := json.Unmarshal(hRaw, &hanzoChat); err != nil {
		t.Fatalf("unmarshal hanzo chat: %v", err)
	}
	if hanzoChat.Choices[0].Message.Content != chatResp.Choices[0].Message.Content {
		t.Fatalf("dual zip.App gateways diverged: hanzo=%q comm=%q",
			hanzoChat.Choices[0].Message.Content, chatResp.Choices[0].Message.Content)
	}
}

func TestPeerDirectoryGossipAndSnapshot(t *testing.T) {
	top := setupDualGatewayTopology(t)
	ctx := context.Background()

	// Fresh community directory peer C syncs exclusively from Community Peer B
	// after Hanzo Directory is closed, proving zero Hanzo lock-in.
	top.hanzoDir.Disable()
	top.hanzoDir.Close()

	peerCKP, err := KeyPairFromSeed([]byte("test-comm-dir-peer-c"))
	if err != nil {
		t.Fatal(err)
	}
	peerC, err := NewDirectoryPeerServer("dir-community-gamma", "Community Peer Gamma", top.domain, peerCKP)
	if err != nil {
		t.Fatal(err)
	}
	defer peerC.Close()

	accepted, rejected, err := peerC.SyncFromPeer(ctx, top.communityDirB.URL(), nil)
	if err != nil {
		t.Fatalf("SyncFromPeer: %v", err)
	}
	if accepted != 3 || rejected != 0 {
		t.Fatalf("expected 3 accepted and 0 rejected workers, got accepted=%d rejected=%d", accepted, rejected)
	}

	// Signed snapshot export + offline verification + tamper rejection.
	snap, err := peerC.Directory().ExportSnapshot(peerCKP, peerC.PeerID, time.Now().Unix())
	if err != nil {
		t.Fatalf("ExportSnapshot: %v", err)
	}
	if err := VerifyDirectorySnapshot(top.domain, snap, time.Now().Unix()); err != nil {
		t.Fatalf("VerifyDirectorySnapshot: %v", err)
	}
	tampered := snap
	tampered.PeerID = "evil-directory"
	if err := VerifyDirectorySnapshot(top.domain, tampered, time.Now().Unix()); !errors.Is(err, ErrDirectoryCorrupted) {
		t.Fatalf("expected ErrDirectoryCorrupted on tampered snapshot, got %v", err)
	}
}

// --- Targeted Regression Tests for PR Review Findings ---

func TestPeerDirectoryPreservesProviderOwnership(t *testing.T) {
	domain := DefaultDomain()
	now := time.Now().Unix()
	ownerKP, err := KeyPairFromSeed([]byte("ownership-legitimate-owner"))
	if err != nil {
		t.Fatal(err)
	}
	attackerKP, err := KeyPairFromSeed([]byte("ownership-attacker"))
	if err != nil {
		t.Fatal(err)
	}

	legit := WorkerRecord{
		ProtocolVersion:    ProtocolVersion,
		ProviderID:         "worker-owned-1",
		EndpointURL:        "http://127.0.0.1:19001",
		Region:             "us-east",
		Models:             defaultTestModels(),
		PriceMicroUSDPer1M: 300_000,
		AdvertisedTTFTMs:   15,
		CapacityRPM:        500,
		IssuedAt:           now,
		ExpiresAt:          now + 3600,
	}
	if err := SignWorkerRecord(domain, ownerKP, &legit); err != nil {
		t.Fatal(err)
	}

	dir := NewPeerDirectory(domain)
	if err := dir.RegisterWorker(legit, now); err != nil {
		t.Fatalf("RegisterWorker legitimate record: %v", err)
	}

	// Attacker signs a replacement record for the same ProviderID with equal and later IssuedAt.
	for _, issuedAt := range []int64{now, now + 100} {
		hijack := WorkerRecord{
			ProtocolVersion:    ProtocolVersion,
			ProviderID:         "worker-owned-1",
			EndpointURL:        "http://127.0.0.1:6666",
			Region:             "us-east",
			Models:             defaultTestModels(),
			PriceMicroUSDPer1M: 100_000,
			AdvertisedTTFTMs:   5,
			CapacityRPM:        500,
			IssuedAt:           issuedAt,
			ExpiresAt:          issuedAt + 3600,
		}
		if err := SignWorkerRecord(domain, attackerKP, &hijack); err != nil {
			t.Fatal(err)
		}
		if err := dir.RegisterWorker(hijack, now); !errors.Is(err, ErrSignerMismatch) {
			t.Fatalf("expected ErrSignerMismatch when different operator replaces ProviderID (issuedAt=%d), got %v", issuedAt, err)
		}
		accepted, rejected := dir.MergeRecords([]WorkerRecord{hijack}, now)
		if accepted != 0 || rejected != 1 {
			t.Fatalf("expected gossip merge to reject hijacked record, got accepted=%d rejected=%d", accepted, rejected)
		}
	}

	workers := dir.ListWorkers(now)
	if len(workers) != 1 || !SameAddress(workers[0].OperatorAddress, ownerKP.Address()) || workers[0].EndpointURL != legit.EndpointURL {
		t.Fatalf("legitimate worker ownership was altered: %+v", workers)
	}

	// The legitimate owner CAN refresh its own record with a later IssuedAt.
	updated := legit
	updated.EndpointURL = "http://127.0.0.1:19002"
	updated.IssuedAt = now + 10
	updated.ExpiresAt = now + 7200
	if err := SignWorkerRecord(domain, ownerKP, &updated); err != nil {
		t.Fatal(err)
	}
	if err := dir.RegisterWorker(updated, now); err != nil {
		t.Fatalf("legitimate owner refresh failed: %v", err)
	}
	workers = dir.ListWorkers(now)
	if len(workers) != 1 || workers[0].EndpointURL != updated.EndpointURL {
		t.Fatalf("expected legitimate owner update to apply, got %+v", workers)
	}

	// VerifyDirectorySnapshot also rejects conflicting operators for the same ProviderID.
	dirKP, err := KeyPairFromSeed([]byte("ownership-snapshot-signer"))
	if err != nil {
		t.Fatal(err)
	}
	hijackForSnap := updated
	hijackForSnap.EndpointURL = "http://127.0.0.1:6666"
	if err := SignWorkerRecord(domain, attackerKP, &hijackForSnap); err != nil {
		t.Fatal(err)
	}
	snap := DirectorySnapshot{
		ProtocolVersion: ProtocolVersion,
		PeerID:          "peer-conflict",
		OperatorAddress: dirKP.Address(),
		ExportedAt:      now,
		Workers:         []WorkerRecord{updated, hijackForSnap},
	}
	digest, err := computeSnapshotSHA256(snap)
	if err != nil {
		t.Fatal(err)
	}
	snap.SnapshotSHA256 = digest
	sh, err := snapshotStructHash(snap)
	if err != nil {
		t.Fatal(err)
	}
	snap.Signature, err = dirKP.SignDigest(domain.TypedDataDigest(sh))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyDirectorySnapshot(domain, snap, now); !errors.Is(err, ErrDirectoryCorrupted) {
		t.Fatalf("expected ErrDirectoryCorrupted for conflicting operators in snapshot, got %v", err)
	}
}

func TestWorkerRecordModelInventoryUnambiguousEncoding(t *testing.T) {
	domain := DefaultDomain()
	now := time.Now().Unix()
	kp, err := KeyPairFromSeed([]byte("model-encoding-provider"))
	if err != nil {
		t.Fatal(err)
	}

	modelA := ModelSpec{
		ModelID:          "model-a",
		ModelDigest:      "sha256:" + SHA256Hex([]byte("weights-a")),
		MaxContextTokens: 8192,
		License:          "Apache-2.0",
	}
	modelB := ModelSpec{
		ModelID:          "model-b",
		ModelDigest:      "sha256:" + SHA256Hex([]byte("weights-b")),
		MaxContextTokens: 16384,
		License:          "MIT",
	}

	// Order independence: [A, B] and [B, A] produce identical digests.
	if canonicalModelsDigest([]ModelSpec{modelA, modelB}) != canonicalModelsDigest([]ModelSpec{modelB, modelA}) {
		t.Fatalf("canonicalModelsDigest should be order-independent")
	}

	rec := WorkerRecord{
		ProtocolVersion:    ProtocolVersion,
		ProviderID:         "worker-two-models",
		EndpointURL:        "http://127.0.0.1:19100",
		Region:             "eu-west",
		Models:             []ModelSpec{modelA, modelB},
		PriceMicroUSDPer1M: 320_000,
		AdvertisedTTFTMs:   12,
		CapacityRPM:        600,
		IssuedAt:           now,
		ExpiresAt:          now + 3600,
	}
	if err := SignWorkerRecord(domain, kp, &rec); err != nil {
		t.Fatal(err)
	}
	if err := VerifyWorkerRecord(domain, rec, now); err != nil {
		t.Fatalf("VerifyWorkerRecord valid 2-model record failed: %v", err)
	}

	// Delimiter-smuggling attack: drop modelB and append "|model-b:..." to modelA.License.
	smuggled := rec
	smuggledA := modelA
	smuggledA.License = fmt.Sprintf("%s|%s:%s:%d:%s", modelA.License, modelB.ModelID, modelB.ModelDigest, modelB.MaxContextTokens, modelB.License)
	smuggled.Models = []ModelSpec{smuggledA}

	if canonicalModelsDigest(smuggled.Models) == canonicalModelsDigest(rec.Models) {
		t.Fatalf("delimiter-smuggled model inventory collided with original canonicalModelsDigest")
	}
	if err := VerifyWorkerRecord(domain, smuggled, now); !errors.Is(err, ErrSignerMismatch) {
		t.Fatalf("expected ErrSignerMismatch for delimiter-smuggled model inventory, got %v", err)
	}
}

func TestVerifyReceiptRejectsEmptyTruncatedCompletion(t *testing.T) {
	domain := DefaultDomain()
	now := time.Now().Unix()
	kp, err := KeyPairFromSeed([]byte("empty-completion-provider"))
	if err != nil {
		t.Fatal(err)
	}
	req := ChatCompletionRequest{
		Model:     "zen-7b-instruct",
		Messages:  []ChatMessage{{Role: "user", Content: "check empty completion truncation"}},
		MaxTokens: 32,
	}
	reqHash := CanonicalRequestHash(req)
	quote := InferenceQuote{
		QuoteID:            "qt-empty-check-1",
		ProtocolVersion:    ProtocolVersion,
		ProviderID:         "worker-empty-check",
		ModelID:            req.Model,
		ModelDigest:        "sha256:" + SHA256Hex([]byte("weights")),
		PriceMicroUSDPer1M: 300_000,
		MaxTTFTMs:          10,
		MaxTokens:          32,
		IssuedAt:           now,
		ExpiresAt:          now + 60,
		Nonce:              "nonce-empty-check-1",
		RequestHash:        reqHash,
	}
	if err := SignQuote(domain, kp, &quote); err != nil {
		t.Fatal(err)
	}
	qh, err := QuoteHash(domain, quote)
	if err != nil {
		t.Fatal(err)
	}

	honestContent := "honest non-empty completion"
	rcpt := ExecutionReceipt{
		ReceiptID:         "rcpt-empty-check-1",
		ProtocolVersion:   ProtocolVersion,
		VerificationClass: ReceiptVerificationClass,
		GatewayID:         "gw-empty-check",
		GatewayOperator:   "Test Gateway",
		ProviderID:        quote.ProviderID,
		ModelID:           quote.ModelID,
		ModelDigest:       quote.ModelDigest,
		QuoteID:           quote.QuoteID,
		QuoteHash:         qh,
		QuoteNonce:        quote.Nonce,
		RequestHash:       reqHash,
		OutputHash:        ComputeOutputHash(honestContent),
		PromptTokens:      10,
		CompletionTokens:  12,
		TotalTokens:       22,
		TTFTMs:            5.0,
		TotalLatencyMs:    12.0,
		CompletedAt:       now,
	}
	if err := SignReceipt(domain, kp, &rcpt); err != nil {
		t.Fatal(err)
	}

	// Empty completion string MUST be hashed and rejected against non-empty receipt.
	if err := VerifyReceipt(domain, rcpt, &quote, reqHash, ""); !errors.Is(err, ErrOutputHashMismatch) {
		t.Fatalf("expected ErrOutputHashMismatch when verifying empty completion against non-empty receipt, got %v", err)
	}
	// Separate content-omitted API succeeds when caller explicitly does not supply content.
	if err := VerifyReceiptWithoutContent(domain, rcpt, &quote, reqHash); err != nil {
		t.Fatalf("VerifyReceiptWithoutContent failed unexpectedly: %v", err)
	}

	// Gateway.VerifyReceipt distinguishes nil (omitted) from StringPtr("") (empty text).
	gw, err := NewGateway(GatewayConfig{
		GatewayID:    "gw-empty-verify-api",
		OperatorName: "Empty Verify API",
		OperatorType: OperatorTypeCommunity,
		Domain:       domain,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gw.VerifyReceipt(VerifyReceiptRequest{
		Receipt:         rcpt,
		Quote:           &quote,
		RequestHash:     reqHash,
		ResponseContent: nil,
	}); err != nil {
		t.Fatalf("VerifyReceipt with omitted ResponseContent failed: %v", err)
	}
	if _, err := gw.VerifyReceipt(VerifyReceiptRequest{
		Receipt:         rcpt,
		Quote:           &quote,
		RequestHash:     reqHash,
		ResponseContent: StringPtr(""),
	}); !errors.Is(err, ErrOutputHashMismatch) {
		t.Fatalf("VerifyReceipt with explicit empty ResponseContent expected ErrOutputHashMismatch, got %v", err)
	}

	// Genuinely empty completion whose OutputHash commits to "" passes VerifyReceipt(..., "").
	emptyRcpt := rcpt
	emptyRcpt.ReceiptID = "rcpt-genuinely-empty"
	emptyRcpt.OutputHash = ComputeOutputHash("")
	if err := SignReceipt(domain, kp, &emptyRcpt); err != nil {
		t.Fatal(err)
	}
	if err := VerifyReceipt(domain, emptyRcpt, &quote, reqHash, ""); err != nil {
		t.Fatalf("VerifyReceipt for genuinely empty output failed: %v", err)
	}

	// End-to-end: worker truncating content to "" is rejected by Scheduler and triggers fail-closed error.
	realWorker, err := NewWorkerNode(WorkerConfig{
		ProviderID:         "worker-truncating",
		Models:             defaultTestModels(),
		PriceMicroUSDPer1M: 300_000,
		AdvertisedTTFTMs:   10,
		Domain:             domain,
		KeyPair:            kp,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer realWorker.Close()

	truncProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upReq, _ := http.NewRequestWithContext(r.Context(), r.Method, realWorker.URL()+r.URL.Path, bytes.NewReader(body))
		upReq.Header.Set("Content-Type", "application/json")
		upResp, err := http.DefaultClient.Do(upReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer upResp.Body.Close()
		raw, _ := io.ReadAll(upResp.Body)
		if r.URL.Path == "/worker/v1/execute" && upResp.StatusCode == http.StatusOK {
			var er ExecuteResponse
			if err := json.Unmarshal(raw, &er); err == nil {
				er.Content = "" // strip completion text while preserving signed receipt
				writeJSON(w, http.StatusOK, er)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(upResp.StatusCode)
		_, _ = w.Write(raw)
	}))
	defer truncProxy.Close()

	rec, err := realWorker.SignedRecord(now, 3600)
	if err != nil {
		t.Fatal(err)
	}
	rec.EndpointURL = truncProxy.URL
	if err := SignWorkerRecord(domain, kp, &rec); err != nil {
		t.Fatal(err)
	}
	gwStrict, err := NewGateway(GatewayConfig{
		GatewayID:       "gw-trunc-reject",
		OperatorName:    "Trunc Reject",
		OperatorType:    OperatorTypeCommunity,
		Domain:          domain,
		StaticWorkers:   []WorkerRecord{rec},
		FailoverEnabled: BoolPtr(false),
		VerifyReceipts:  BoolPtr(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gwStrict.ChatCompletions(context.Background(), req); !errors.Is(err, ErrOutputHashMismatch) {
		t.Fatalf("expected Scheduler to reject truncated empty content with ErrOutputHashMismatch, got %v", err)
	}
}

func TestReceiptAndWorkerEnforceQuoteRequestHashBinding(t *testing.T) {
	domain := DefaultDomain()
	now := time.Now().Unix()
	kp, err := KeyPairFromSeed([]byte("quote-binding-worker"))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorkerNode(WorkerConfig{
		ProviderID:         "worker-quote-bind",
		Models:             defaultTestModels(),
		PriceMicroUSDPer1M: 300_000,
		AdvertisedTTFTMs:   10,
		Domain:             domain,
		KeyPair:            kp,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()

	reqA := ChatCompletionRequest{
		Model:     "zen-7b-instruct",
		Messages:  []ChatMessage{{Role: "user", Content: "prompt A"}},
		MaxTokens: 32,
	}
	reqB := ChatCompletionRequest{
		Model:     "zen-7b-instruct",
		Messages:  []ChatMessage{{Role: "user", Content: "prompt B"}},
		MaxTokens: 32,
	}
	hashA := CanonicalRequestHash(reqA)
	hashB := CanonicalRequestHash(reqB)
	if hashA == hashB {
		t.Fatalf("expected distinct request hashes")
	}

	// Obtain a valid signed quote bound to request A from the worker.
	qBody, _ := json.Marshal(QuoteRequest{
		ModelID:     reqA.Model,
		MaxTokens:   reqA.MaxTokens,
		RequestHash: hashA,
		GatewayID:   "gw-bind-test",
	})
	qResp, err := http.Post(worker.URL()+"/worker/v1/quote", "application/json", bytes.NewReader(qBody))
	if err != nil {
		t.Fatalf("quote request failed: %v", err)
	}
	defer qResp.Body.Close()
	if qResp.StatusCode != http.StatusOK {
		t.Fatalf("quote status=%d", qResp.StatusCode)
	}
	var quoteA InferenceQuote
	if err := json.NewDecoder(qResp.Body).Decode(&quoteA); err != nil {
		t.Fatal(err)
	}

	// 1. Worker dispatch MUST reject executing request B under quote bound to request A.
	execBody, _ := json.Marshal(ExecuteRequest{
		Quote:           quoteA,
		Request:         reqB,
		GatewayID:       "gw-bind-test",
		GatewayOperator: "Bind Tester",
	})
	execResp, err := http.Post(worker.URL()+"/worker/v1/execute", "application/json", bytes.NewReader(execBody))
	if err != nil {
		t.Fatalf("execute request failed: %v", err)
	}
	defer execResp.Body.Close()
	if execResp.StatusCode != http.StatusBadRequest {
		raw, _ := io.ReadAll(execResp.Body)
		t.Fatalf("expected HTTP 400 when executing prompt B with quote bound to prompt A, got %d: %s", execResp.StatusCode, string(raw))
	}

	// 2. VerifyReceipt and VerifyReceiptWithoutContent MUST reject a receipt for request B referencing quoteA.
	qh, err := QuoteHash(domain, quoteA)
	if err != nil {
		t.Fatal(err)
	}
	contentB := "output for prompt B"
	rcptB := ExecutionReceipt{
		ReceiptID:         "rcpt-mismatched-req",
		ProtocolVersion:   ProtocolVersion,
		VerificationClass: ReceiptVerificationClass,
		GatewayID:         "gw-bind-test",
		GatewayOperator:   "Bind Tester",
		ProviderID:        quoteA.ProviderID,
		ModelID:           quoteA.ModelID,
		ModelDigest:       quoteA.ModelDigest,
		QuoteID:           quoteA.QuoteID,
		QuoteHash:         qh,
		QuoteNonce:        quoteA.Nonce,
		RequestHash:       hashB,
		OutputHash:        ComputeOutputHash(contentB),
		PromptTokens:      10,
		CompletionTokens:  10,
		TotalTokens:       20,
		TTFTMs:            5.0,
		TotalLatencyMs:    10.0,
		CompletedAt:       now,
	}
	if err := SignReceipt(domain, kp, &rcptB); err != nil {
		t.Fatal(err)
	}
	if err := VerifyReceipt(domain, rcptB, &quoteA, hashB, contentB); !errors.Is(err, ErrInvalidReceipt) {
		t.Fatalf("expected ErrInvalidReceipt when receipt requestHash differs from bound quote, got %v", err)
	}
	if err := VerifyReceiptWithoutContent(domain, rcptB, &quoteA, hashB); !errors.Is(err, ErrInvalidReceipt) {
		t.Fatalf("expected ErrInvalidReceipt from VerifyReceiptWithoutContent on quote/receipt requestHash mismatch, got %v", err)
	}
}

func TestStandaloneHTTPHandlerUsesTypedOperationRegistry(t *testing.T) {
	top := setupDualGatewayTopology(t)

	// 1. Standalone loopback server exposes OpenAPI 3.1 projection from Mount's typed ops.
	resp, err := http.Get(top.communitySrv.URL + "/.well-known/openapi.json")
	if err != nil {
		t.Fatalf("GET /.well-known/openapi.json: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected HTTP 200 for /.well-known/openapi.json, got %d: %s", resp.StatusCode, string(raw))
	}
	var spec struct {
		OpenAPI string                 `json:"openapi"`
		Paths   map[string]interface{} `json:"paths"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&spec); err != nil {
		t.Fatalf("decode openapi spec: %v", err)
	}
	if spec.OpenAPI == "" {
		t.Fatalf("expected non-empty openapi version")
	}
	for _, path := range []string{
		"/v1/models",
		"/v1/chat/completions",
		"/v1/gateway/health",
		"/v1/gateway/providers",
		"/v1/gateway/quotes",
		"/v1/gateway/receipts/verify",
		"/v1/gateway/export",
	} {
		if _, ok := spec.Paths[path]; !ok {
			t.Errorf("standalone OpenAPI spec missing typed operation path %s", path)
		}
	}

	// 2. Standalone loopback server exposes MCP JSON-RPC projection from the same typed ops.
	mcpReq := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	mcpResp, err := http.Post(top.communitySrv.URL+"/mcp", "application/json", bytes.NewBufferString(mcpReq))
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer mcpResp.Body.Close()
	mcpRaw, _ := io.ReadAll(mcpResp.Body)
	if mcpResp.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200 for POST /mcp, got %d: %s", mcpResp.StatusCode, string(mcpRaw))
	}
	var mcpOut struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(mcpRaw, &mcpOut); err != nil {
		t.Fatalf("decode /mcp response: %v (raw=%s)", err, string(mcpRaw))
	}
	if len(mcpOut.Result.Tools) < 7 {
		t.Fatalf("expected at least 7 projected MCP tools from typed ops, got %d", len(mcpOut.Result.Tools))
	}

	// 3. Standalone /v1/gateway/receipts/verify distinguishes omitted vs explicit empty response_content.
	chatReq := ChatCompletionRequest{
		Model:     "zen-7b-instruct",
		Messages:  []ChatMessage{{Role: "user", Content: "standalone verify probe"}},
		MaxTokens: 32,
	}
	chatResp, err := top.communityGW.ChatCompletions(context.Background(), chatReq)
	if err != nil {
		t.Fatal(err)
	}
	verifyOmitted, _ := json.Marshal(VerifyReceiptRequest{
		Receipt:     *chatResp.EnsoReceipt,
		Quote:       chatResp.EnsoQuote,
		RequestHash: CanonicalRequestHash(chatReq),
	})
	vResp1, err := http.Post(top.communitySrv.URL+"/v1/gateway/receipts/verify", "application/json", bytes.NewReader(verifyOmitted))
	if err != nil {
		t.Fatal(err)
	}
	_ = vResp1.Body.Close()
	if vResp1.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for omitted response_content, got %d", vResp1.StatusCode)
	}

	verifyEmpty, _ := json.Marshal(VerifyReceiptRequest{
		Receipt:         *chatResp.EnsoReceipt,
		Quote:           chatResp.EnsoQuote,
		RequestHash:     CanonicalRequestHash(chatReq),
		ResponseContent: StringPtr(""),
	})
	vResp2, err := http.Post(top.communitySrv.URL+"/v1/gateway/receipts/verify", "application/json", bytes.NewReader(verifyEmpty))
	if err != nil {
		t.Fatal(err)
	}
	_ = vResp2.Body.Close()
	if vResp2.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for explicit empty response_content against non-empty receipt, got %d", vResp2.StatusCode)
	}
}

func TestTamperedTokenMetadataRejectedAndUsageFromSignedReceipt(t *testing.T) {
	domain := DefaultDomain()
	now := time.Now().Unix()
	kp, err := KeyPairFromSeed([]byte("usage-tamper-worker"))
	if err != nil {
		t.Fatal(err)
	}
	realWorker, err := NewWorkerNode(WorkerConfig{
		ProviderID:         "worker-usage-tamper",
		Models:             defaultTestModels(),
		PriceMicroUSDPer1M: 300_000,
		AdvertisedTTFTMs:   10,
		Domain:             domain,
		KeyPair:            kp,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer realWorker.Close()

	// Proxy worker that inflates unsigned envelope token counts while leaving signed Receipt intact.
	tamperWorkerProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upReq, _ := http.NewRequestWithContext(r.Context(), r.Method, realWorker.URL()+r.URL.Path, bytes.NewReader(body))
		upReq.Header.Set("Content-Type", "application/json")
		upResp, err := http.DefaultClient.Do(upReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer upResp.Body.Close()
		raw, _ := io.ReadAll(upResp.Body)
		if r.URL.Path == "/worker/v1/execute" && upResp.StatusCode == http.StatusOK {
			var er ExecuteResponse
			if err := json.Unmarshal(raw, &er); err == nil {
				er.PromptTokens += 5000
				er.CompletionTokens += 5000
				writeJSON(w, http.StatusOK, er)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(upResp.StatusCode)
		_, _ = w.Write(raw)
	}))
	defer tamperWorkerProxy.Close()

	rec, err := realWorker.SignedRecord(now, 3600)
	if err != nil {
		t.Fatal(err)
	}
	rec.EndpointURL = tamperWorkerProxy.URL
	if err := SignWorkerRecord(domain, kp, &rec); err != nil {
		t.Fatal(err)
	}

	req := ChatCompletionRequest{
		Model:     "zen-7b-instruct",
		Messages:  []ChatMessage{{Role: "user", Content: "check signed usage accounting"}},
		MaxTokens: 32,
	}

	// 1. With VerifyReceipts enabled, tampered execute envelope token metadata is rejected fail-closed.
	gwVerified, err := NewGateway(GatewayConfig{
		GatewayID:       "gw-usage-verified",
		OperatorName:    "Usage Verified",
		OperatorType:    OperatorTypeCommunity,
		Domain:          domain,
		StaticWorkers:   []WorkerRecord{rec},
		FailoverEnabled: BoolPtr(false),
		VerifyReceipts:  BoolPtr(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gwVerified.ChatCompletions(context.Background(), req); !errors.Is(err, ErrInvalidReceipt) {
		t.Fatalf("expected ErrInvalidReceipt on tampered worker envelope token counts, got %v", err)
	}

	// 2. Even if VerifyReceipts were disabled, ChatCompletionResponse.Usage is populated from signed Receipt.
	gwUnverified, err := NewGateway(GatewayConfig{
		GatewayID:       "gw-usage-from-receipt",
		OperatorName:    "Usage From Receipt",
		OperatorType:    OperatorTypeCommunity,
		Domain:          domain,
		StaticWorkers:   []WorkerRecord{rec},
		FailoverEnabled: BoolPtr(false),
		VerifyReceipts:  BoolPtr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := gwUnverified.ChatCompletions(context.Background(), req)
	if err != nil {
		t.Fatalf("unverified ChatCompletions failed: %v", err)
	}
	if resp.Usage.PromptTokens != resp.EnsoReceipt.PromptTokens ||
		resp.Usage.CompletionTokens != resp.EnsoReceipt.CompletionTokens ||
		resp.Usage.TotalTokens != resp.EnsoReceipt.TotalTokens {
		t.Fatalf("expected Usage to match signed receipt (%d/%d/%d), got %+v",
			resp.EnsoReceipt.PromptTokens, resp.EnsoReceipt.CompletionTokens, resp.EnsoReceipt.TotalTokens, resp.Usage)
	}

	// 3. MultiGatewayClient rejects a gateway response whose Usage disagrees with EnsoReceipt.
	honestRec, err := realWorker.SignedRecord(now, 3600)
	if err != nil {
		t.Fatal(err)
	}
	honestGW, err := NewGateway(GatewayConfig{
		GatewayID:     "gw-honest-for-usage-proxy",
		OperatorName:  "Honest GW",
		OperatorType:  OperatorTypeCommunity,
		Domain:        domain,
		StaticWorkers: []WorkerRecord{honestRec},
	})
	if err != nil {
		t.Fatal(err)
	}
	honestSrv := StartLoopbackServer(honestGW)
	defer honestSrv.Close()

	tamperGWProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upReq, _ := http.NewRequestWithContext(r.Context(), r.Method, honestSrv.URL+r.URL.Path, bytes.NewReader(body))
		upReq.Header.Set("Content-Type", "application/json")
		upResp, err := http.DefaultClient.Do(upReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer upResp.Body.Close()
		raw, _ := io.ReadAll(upResp.Body)
		var out ChatCompletionResponse
		if err := json.Unmarshal(raw, &out); err == nil {
			out.Usage.TotalTokens += 9999
			writeJSON(w, http.StatusOK, out)
			return
		}
		w.WriteHeader(upResp.StatusCode)
		_, _ = w.Write(raw)
	}))
	defer tamperGWProxy.Close()

	cli := NewMultiGatewayClient(domain, []GatewayEndpoint{
		{GatewayID: "tamper-gw", OperatorType: OperatorTypeCommunity, BaseURL: tamperGWProxy.URL, Enabled: true},
	}, nil)
	if _, err := cli.ChatCompletions(context.Background(), req); !errors.Is(err, ErrInvalidReceipt) {
		t.Fatalf("expected MultiGatewayClient to reject tampered Usage with ErrInvalidReceipt, got %v", err)
	}
}

func writeJSON(rw http.ResponseWriter, status int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(v)
}

func TestApplyGatewayFeeOverflowRejected(t *testing.T) {
	const overflowPrice = uint64(18444901242133756455)
	if _, err := ApplyGatewayFeeChecked(overflowPrice, 10); !errors.Is(err, ErrInvalidQuote) {
		t.Fatalf("expected ErrInvalidQuote for ApplyGatewayFeeChecked(%d, 10), got %v", overflowPrice, err)
	}
	if _, err := ApplyGatewayFeeChecked(^uint64(0), 1); !errors.Is(err, ErrInvalidQuote) {
		t.Fatalf("expected ErrInvalidQuote for MaxUint64 with 1 bps fee, got %v", err)
	}
	if _, err := ApplyGatewayFeeChecked(^uint64(0), ^uint64(0)); !errors.Is(err, ErrInvalidQuote) {
		t.Fatalf("expected ErrInvalidQuote for MaxUint64 * MaxUint64 without panic, got %v", err)
	}
	if got := ApplyGatewayFee(overflowPrice, 10); got == 0 {
		t.Fatalf("ApplyGatewayFee wrapped to 0 on overflow")
	}

	// End-to-end scheduler check: provider quoting overflowPrice must be rejected
	// rather than ranked as free under lowest_cost.
	domain := DefaultDomain()
	now := time.Now().Unix()
	overflowWorker, err := NewWorkerNode(WorkerConfig{
		ProviderID:         "worker-overflow",
		Models:             defaultTestModels(),
		PriceMicroUSDPer1M: overflowPrice,
		AdvertisedTTFTMs:   5,
		Domain:             domain,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer overflowWorker.Close()

	honestWorker, err := NewWorkerNode(WorkerConfig{
		ProviderID:         "worker-honest",
		Models:             defaultTestModels(),
		PriceMicroUSDPer1M: 400_000,
		AdvertisedTTFTMs:   15,
		Domain:             domain,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer honestWorker.Close()

	recOverflow, err := overflowWorker.SignedRecord(now, 3600)
	if err != nil {
		t.Fatal(err)
	}
	recHonest, err := honestWorker.SignedRecord(now, 3600)
	if err != nil {
		t.Fatal(err)
	}

	gw, err := NewGateway(GatewayConfig{
		GatewayID:     "gw-fee-overflow",
		OperatorName:  "Fee Overflow Guard",
		OperatorType:  OperatorTypeCommunity,
		Domain:        domain,
		Strategy:      StrategyLowestCost,
		GatewayFeeBps: 10,
		StaticWorkers: []WorkerRecord{recOverflow, recHonest},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := gw.ChatCompletions(context.Background(), ChatCompletionRequest{
		Model:     "zen-7b-instruct",
		Messages:  []ChatMessage{{Role: "user", Content: "check overflow guard"}},
		MaxTokens: 32,
		Strategy:  string(StrategyLowestCost),
	})
	if err != nil {
		t.Fatalf("ChatCompletions failed: %v", err)
	}
	if resp.EnsoRoute.ProviderID != "worker-honest" {
		t.Fatalf("expected honest worker to win, got %s (effectivePrice=%d)",
			resp.EnsoRoute.ProviderID, resp.EnsoRoute.EffectivePriceMicroUSDPer1M)
	}
	if resp.EnsoRoute.EffectivePriceMicroUSDPer1M <= 400_000 {
		t.Fatalf("expected fee-inclusive price > 400000, got %d", resp.EnsoRoute.EffectivePriceMicroUSDPer1M)
	}
}

func TestWorkerAndDirectoryExposeTypedOpenAPIAndMCP(t *testing.T) {
	domain := DefaultDomain()
	worker, err := NewWorkerNode(WorkerConfig{
		ProviderID:         "worker-typed-ops",
		Models:             defaultTestModels(),
		PriceMicroUSDPer1M: 250_000,
		AdvertisedTTFTMs:   10,
		Domain:             domain,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()

	dirSrv, err := NewDirectoryPeerServer("dir-typed-ops", "Typed Ops Directory", domain, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dirSrv.Close()

	checkSurface := func(name, baseURL string, wantPaths []string, minTools int) {
		t.Helper()
		resp, err := http.Get(baseURL + "/.well-known/openapi.json")
		if err != nil {
			t.Fatalf("%s GET /.well-known/openapi.json: %v", name, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s expected 200 for openapi.json, got %d: %s", name, resp.StatusCode, string(raw))
		}
		var spec struct {
			OpenAPI string                 `json:"openapi"`
			Paths   map[string]interface{} `json:"paths"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&spec); err != nil {
			t.Fatalf("%s decode openapi: %v", name, err)
		}
		for _, p := range wantPaths {
			if _, ok := spec.Paths[p]; !ok {
				t.Errorf("%s OpenAPI missing typed path %s", name, p)
			}
		}

		mcpReq := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
		mcpResp, err := http.Post(baseURL+"/mcp", "application/json", bytes.NewBufferString(mcpReq))
		if err != nil {
			t.Fatalf("%s POST /mcp: %v", name, err)
		}
		defer mcpResp.Body.Close()
		raw, _ := io.ReadAll(mcpResp.Body)
		if mcpResp.StatusCode != http.StatusOK {
			t.Fatalf("%s expected 200 for POST /mcp, got %d: %s", name, mcpResp.StatusCode, string(raw))
		}
		var mcpOut struct {
			Result struct {
				Tools []map[string]any `json:"tools"`
			} `json:"result"`
		}
		if err := json.Unmarshal(raw, &mcpOut); err != nil {
			t.Fatalf("%s decode /mcp: %v (raw=%s)", name, err, string(raw))
		}
		if len(mcpOut.Result.Tools) < minTools {
			t.Fatalf("%s expected at least %d MCP tools, got %d", name, minTools, len(mcpOut.Result.Tools))
		}
	}

	checkSurface("worker", worker.URL(), []string{
		"/worker/v1/record",
		"/worker/v1/quote",
		"/worker/v1/execute",
	}, 3)

	checkSurface("directory", dirSrv.URL(), []string{
		"/directory/v1/workers",
		"/directory/v1/gossip",
		"/directory/v1/snapshot",
	}, 4)
}

func TestFailoverRefreshesExpiredBackupQuotes(t *testing.T) {
	domain := DefaultDomain()
	now := time.Now().Unix()

	primary, err := NewWorkerNode(WorkerConfig{
		ProviderID:         "worker-slow-primary",
		Models:             defaultTestModels(),
		PriceMicroUSDPer1M: 100_000, // cheaper so selected first under lowest_cost
		AdvertisedTTFTMs:   5,
		QuoteTTL:           1 * time.Second,
		Domain:             domain,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close()
	primary.SetFaultMode(FaultTimeout)
	primary.SetTimeoutDelay(2500 * time.Millisecond)

	backup, err := NewWorkerNode(WorkerConfig{
		ProviderID:         "worker-healthy-backup",
		Models:             defaultTestModels(),
		PriceMicroUSDPer1M: 200_000,
		AdvertisedTTFTMs:   10,
		QuoteTTL:           1 * time.Second, // initial quote expires while primary times out
		Domain:             domain,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()

	recPrimary, err := primary.SignedRecord(now, 3600)
	if err != nil {
		t.Fatal(err)
	}
	recBackup, err := backup.SignedRecord(now, 3600)
	if err != nil {
		t.Fatal(err)
	}

	gw, err := NewGateway(GatewayConfig{
		GatewayID:         "gw-expired-backup-failover",
		OperatorName:      "Failover Refresh",
		OperatorType:      OperatorTypeCommunity,
		Domain:            domain,
		Strategy:          StrategyLowestCost,
		QuoteCacheEnabled: false,
		FailoverEnabled:   BoolPtr(true),
		ProviderTimeout:   1500 * time.Millisecond, // > 1s QuoteTTL so backup quote expires during attempt 1
		StaticWorkers:     []WorkerRecord{recPrimary, recBackup},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := gw.ChatCompletions(context.Background(), ChatCompletionRequest{
		Model:     "zen-7b-instruct",
		Messages:  []ChatMessage{{Role: "user", Content: "failover with short quote ttl"}},
		MaxTokens: 32,
		Strategy:  string(StrategyLowestCost),
	})
	if err != nil {
		t.Fatalf("expected failover to refresh expired backup quote and succeed, got %v", err)
	}
	if resp.EnsoRoute.ProviderID != "worker-healthy-backup" || !resp.EnsoRoute.FailoverOccurred {
		t.Fatalf("expected failover to worker-healthy-backup, got route=%+v", resp.EnsoRoute)
	}
	if backup.QuoteCalls() < 2 {
		t.Fatalf("expected backup worker to receive refreshed quote call, got %d quote calls", backup.QuoteCalls())
	}
}

func TestWorkerAndReceiptEnforceQuoteMaxTokensCap(t *testing.T) {
	domain := DefaultDomain()
	now := time.Now().Unix()
	kp, err := KeyPairFromSeed([]byte("quote-maxtokens-worker"))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorkerNode(WorkerConfig{
		ProviderID:         "worker-maxtokens",
		Models:             defaultTestModels(),
		PriceMicroUSDPer1M: 300_000,
		AdvertisedTTFTMs:   10,
		Domain:             domain,
		KeyPair:            kp,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()

	largeReq := ChatCompletionRequest{
		Model:     "zen-7b-instruct",
		Messages:  []ChatMessage{{Role: "user", Content: "produce 512 tokens under 1-token quote"}},
		MaxTokens: 512,
	}
	largeReqHash := CanonicalRequestHash(largeReq)

	// Obtain a quote with MaxTokens=1 bound to largeReq's hash.
	qBody, _ := json.Marshal(QuoteRequest{
		ModelID:     largeReq.Model,
		MaxTokens:   1,
		RequestHash: largeReqHash,
		GatewayID:   "gw-cap-test",
	})
	qResp, err := http.Post(worker.URL()+"/worker/v1/quote", "application/json", bytes.NewReader(qBody))
	if err != nil {
		t.Fatal(err)
	}
	defer qResp.Body.Close()
	if qResp.StatusCode != http.StatusOK {
		t.Fatalf("quote status=%d", qResp.StatusCode)
	}
	var oneTokenQuote InferenceQuote
	if err := json.NewDecoder(qResp.Body).Decode(&oneTokenQuote); err != nil {
		t.Fatal(err)
	}

	// 1. Worker execute MUST reject 512-token request under 1-token quote.
	execBody, _ := json.Marshal(ExecuteRequest{
		Quote:           oneTokenQuote,
		Request:         largeReq,
		GatewayID:       "gw-cap-test",
		GatewayOperator: "Cap Tester",
	})
	execResp, err := http.Post(worker.URL()+"/worker/v1/execute", "application/json", bytes.NewReader(execBody))
	if err != nil {
		t.Fatal(err)
	}
	defer execResp.Body.Close()
	if execResp.StatusCode != http.StatusBadRequest {
		raw, _ := io.ReadAll(execResp.Body)
		t.Fatalf("expected HTTP 400 when executing 512-token request under 1-token quote, got %d: %s", execResp.StatusCode, string(raw))
	}

	// 2. VerifyReceipt and VerifyReceiptWithoutContent MUST reject receipts exceeding quote MaxTokens.
	qh, err := QuoteHash(domain, oneTokenQuote)
	if err != nil {
		t.Fatal(err)
	}
	content := "over-cap output"
	rcpt := ExecutionReceipt{
		ReceiptID:         "rcpt-over-cap",
		ProtocolVersion:   ProtocolVersion,
		VerificationClass: ReceiptVerificationClass,
		GatewayID:         "gw-cap-test",
		GatewayOperator:   "Cap Tester",
		ProviderID:        oneTokenQuote.ProviderID,
		ModelID:           oneTokenQuote.ModelID,
		ModelDigest:       oneTokenQuote.ModelDigest,
		QuoteID:           oneTokenQuote.QuoteID,
		QuoteHash:         qh,
		QuoteNonce:        oneTokenQuote.Nonce,
		RequestHash:       largeReqHash,
		OutputHash:        ComputeOutputHash(content),
		PromptTokens:      12,
		CompletionTokens:  512,
		TotalTokens:       524,
		TTFTMs:            5.0,
		TotalLatencyMs:    10.0,
		CompletedAt:       now,
	}
	if err := SignReceipt(domain, kp, &rcpt); err != nil {
		t.Fatal(err)
	}
	if err := VerifyReceipt(domain, rcpt, &oneTokenQuote, largeReqHash, content); !errors.Is(err, ErrInvalidReceipt) {
		t.Fatalf("expected ErrInvalidReceipt for completionTokens > quote.MaxTokens, got %v", err)
	}
	if err := VerifyReceiptWithoutContent(domain, rcpt, &oneTokenQuote, largeReqHash); !errors.Is(err, ErrInvalidReceipt) {
		t.Fatalf("expected ErrInvalidReceipt from VerifyReceiptWithoutContent for completionTokens > quote.MaxTokens, got %v", err)
	}
}

func TestGatewayRefreshesDiscoveryOnModelSpecificMiss(t *testing.T) {
	domain := DefaultDomain()
	now := time.Now().Unix()

	modelA := ModelSpec{ModelID: "model-alpha", ModelDigest: SHA256Hex([]byte("alpha-v1")), License: "Apache-2.0"}
	modelB := ModelSpec{ModelID: "model-beta", ModelDigest: SHA256Hex([]byte("beta-v1")), License: "Apache-2.0"}

	workerA, err := NewWorkerNode(WorkerConfig{
		ProviderID:         "worker-static-alpha",
		Models:             []ModelSpec{modelA},
		PriceMicroUSDPer1M: 200_000,
		AdvertisedTTFTMs:   10,
		Domain:             domain,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer workerA.Close()

	workerB, err := NewWorkerNode(WorkerConfig{
		ProviderID:         "worker-dir-beta",
		Models:             []ModelSpec{modelB},
		PriceMicroUSDPer1M: 250_000,
		AdvertisedTTFTMs:   10,
		Domain:             domain,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer workerB.Close()

	dirSrv, err := NewDirectoryPeerServer("dir-model-miss", "Model Miss Dir", domain, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dirSrv.Close()

	recA, err := workerA.SignedRecord(now, 3600)
	if err != nil {
		t.Fatal(err)
	}
	recB, err := workerB.SignedRecord(now, 3600)
	if err != nil {
		t.Fatal(err)
	}
	if err := dirSrv.RegisterWorker(recB); err != nil {
		t.Fatal(err)
	}

	// Gateway starts with static workerA (so local directory is non-empty) and
	// directory peer holding workerB for model-beta.
	gw, err := NewGateway(GatewayConfig{
		GatewayID:     "gw-model-miss-refresh",
		OperatorName:  "Model Miss Guard",
		OperatorType:  OperatorTypeCommunity,
		Domain:        domain,
		StaticWorkers: []WorkerRecord{recA},
		DirectoryURLs: []string{dirSrv.URL()},
	})
	if err != nil {
		t.Fatal(err)
	}

	// FetchQuotes for model-beta must refresh discovery despite workerA being cached.
	qResp, err := gw.FetchQuotes(context.Background(), QuotesQuery{Model: "model-beta", MaxTokens: 32})
	if err != nil {
		t.Fatalf("FetchQuotes for model-beta failed despite directory peer holding it: %v", err)
	}
	if len(qResp.Candidates) != 1 || qResp.Candidates[0].Worker.ProviderID != "worker-dir-beta" {
		t.Fatalf("unexpected quotes response: %+v", qResp)
	}

	// Simulate workerB expiring locally while workerA remains valid, and directory
	// holding a renewed record for workerB.
	gw.Directory().RemoveWorker("worker-dir-beta")
	chatResp, err := gw.ChatCompletions(context.Background(), ChatCompletionRequest{
		Model:     "model-beta",
		Messages:  []ChatMessage{{Role: "user", Content: "discover renewed model-beta"}},
		MaxTokens: 32,
	})
	if err != nil {
		t.Fatalf("ChatCompletions for model-beta failed on model-specific miss: %v", err)
	}
	if chatResp.EnsoRoute.ProviderID != "worker-dir-beta" {
		t.Fatalf("expected worker-dir-beta, got %s", chatResp.EnsoRoute.ProviderID)
	}
}

