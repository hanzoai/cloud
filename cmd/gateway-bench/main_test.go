// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/gateway"
)

func TestGatewayBenchmarkAndDossierGeneration(t *testing.T) {
	rep, err := runBenchmark(24, 15, 8)
	if err != nil {
		t.Fatalf("runBenchmark failed: %v", err)
	}
	if rep.Performance.CompletionRate != 1.0 {
		t.Fatalf("expected 100%% sequential completion, got %.4f", rep.Performance.CompletionRate)
	}
	if rep.Performance.TTFT.Samples != rep.Performance.SequentialCompleted ||
		rep.Performance.ProviderReportedTTFT.Samples != rep.Performance.SequentialCompleted {
		t.Fatalf("expected TTFT and ProviderReportedTTFT sample counts to match SequentialCompleted (%d), got ttft=%d provider=%d",
			rep.Performance.SequentialCompleted, rep.Performance.TTFT.Samples, rep.Performance.ProviderReportedTTFT.Samples)
	}
	if rep.Performance.TTFT.P50Ms > rep.Performance.EndToEndLatency.P50Ms+1.0 {
		t.Fatalf("expected client-boundary TTFT p50 (%.3f ms) <= E2E p50 (%.3f ms)",
			rep.Performance.TTFT.P50Ms, rep.Performance.EndToEndLatency.P50Ms)
	}
	if rep.Performance.FailoverCompletion != 1.0 {
		t.Fatalf("expected 100%% failover completion, got %.4f", rep.Performance.FailoverCompletion)
	}
	if rep.Trust.Passed != rep.Trust.Total || rep.Trust.DetectionRate != 1.0 {
		t.Fatalf("expected 100%% trust detection, got %d/%d", rep.Trust.Passed, rep.Trust.Total)
	}
	if !rep.Decentralization.DualGatewayOpenAIParity || !rep.Decentralization.ZIPDualGatewayParity {
		t.Fatalf("expected dual-gateway OpenAI and zip parity: %+v", rep.Decentralization)
	}
	if rep.Decentralization.IndependentWorkloadShare != 1.0 ||
		rep.Decentralization.HanzoClientCallsAfterKill != 0 ||
		rep.Decentralization.HanzoServerCallsAfterKill != 0 {
		t.Fatalf("expected 100%% kill-switch decentralization with 0 Hanzo calls, got %+v", rep.Decentralization)
	}
	if !rep.Sovereignty.TamperedBundleRejected || rep.Sovereignty.ForkedGatewayCompletion != 1.0 || rep.Sovereignty.ForkedGatewayHanzoCalls != 0 {
		t.Fatalf("sovereignty check failed: %+v", rep.Sovereignty)
	}
	if len(rep.NegativeRegimes) != 3 {
		t.Fatalf("expected 3 negative/boundary regimes, got %d", len(rep.NegativeRegimes))
	}
	for _, nr := range rep.NegativeRegimes {
		if !nr.Validated {
			t.Errorf("negative/boundary regime %s failed validation: %+v", nr.Name, nr)
		}
	}

	dir := t.TempDir()
	raw, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeDossier(dir, rep, raw); err != nil {
		t.Fatalf("writeDossier: %v", err)
	}
	for _, name := range []string{"raw_results.json", "score_table.md", "README.md"} {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil || st.Size() == 0 {
			t.Fatalf("expected non-empty dossier artifact %s, err=%v", name, err)
		}
	}
}

func TestClientBoundaryTTFTIncludesGenerationDelay(t *testing.T) {
	domain := gateway.DefaultDomain()
	kp, err := gateway.KeyPairFromSeed([]byte("bench-ttft-boundary-worker"))
	if err != nil {
		t.Fatal(err)
	}
	models := []gateway.ModelSpec{
		{
			ModelID:          "zen-7b-instruct",
			ModelDigest:      "sha256:" + gateway.SHA256Hex([]byte("zen-7b-instruct-open-weights-v1")),
			MaxContextTokens: 32768,
			License:          "Apache-2.0",
		},
	}
	worker, err := gateway.NewWorkerNode(gateway.WorkerConfig{
		ProviderID:         "worker-delayed-gen",
		Region:             "us-east",
		Models:             models,
		PriceMicroUSDPer1M: 300_000,
		AdvertisedTTFTMs:   5,
		SimulatedTTFT:      2 * time.Millisecond,
		SimulatedGenDelay:  65 * time.Millisecond,
		Domain:             domain,
		KeyPair:            kp,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()

	rec, err := worker.SignedRecord(time.Now().Unix(), 3600)
	if err != nil {
		t.Fatal(err)
	}
	gw, err := gateway.NewGateway(gateway.GatewayConfig{
		GatewayID:     "gw-ttft-boundary",
		OperatorName:  "TTFT Boundary Tester",
		OperatorType:  gateway.OperatorTypeCommunity,
		Domain:        domain,
		StaticWorkers: []gateway.WorkerRecord{rec},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := gateway.StartLoopbackServer(gw)
	defer srv.Close()

	client := gateway.NewMultiGatewayClient(domain, []gateway.GatewayEndpoint{
		{GatewayID: gw.GatewayID(), OperatorType: gateway.OperatorTypeCommunity, BaseURL: srv.URL, Enabled: true},
	}, nil)

	resp, clientTTFTMs, totalMs, err := callWithClientTTFT(context.Background(), client, gateway.ChatCompletionRequest{
		Model:     "zen-7b-instruct",
		Messages:  []gateway.ChatMessage{{Role: "user", Content: "measure client-boundary TTFT"}},
		MaxTokens: 32,
	})
	if err != nil {
		t.Fatalf("callWithClientTTFT failed: %v", err)
	}
	if clientTTFTMs < 55.0 {
		t.Fatalf("expected client-boundary TTFT to include 65ms non-streaming generation delay, got %.3f ms (receipt internal ttft=%.3f ms)",
			clientTTFTMs, resp.EnsoReceipt.TTFTMs)
	}
	if resp.EnsoReceipt.TTFTMs >= 45.0 {
		t.Fatalf("expected provider-internal receipt TTFT to reflect pre-generation timing (~2ms), got %.3f ms",
			resp.EnsoReceipt.TTFTMs)
	}
	if clientTTFTMs > totalMs {
		t.Fatalf("client TTFT (%.3f ms) must not exceed total latency (%.3f ms)", clientTTFTMs, totalMs)
	}
}
