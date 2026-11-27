// SPDX-License-Identifier: Apache-2.0 OR MIT

// Command sovereignty-bench runs the HIP-1351 (hanzoai/cloud#20) full
// sovereignty and network exit acceptance flow, 5-dimension benchmark, and
// 3 honest negative/boundary regimes, writing raw JSON telemetry to disk.
package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/sovereignty"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

type LatencyStats struct {
	Count    int     `json:"count"`
	P50Us    float64 `json:"p50Us"`
	P95Us    float64 `json:"p95Us"`
	P99Us    float64 `json:"p99Us"`
	MeanUs   float64 `json:"meanUs"`
	OpsPerS  float64 `json:"opsPerSec"`
	TotalSec float64 `json:"totalSec"`
}

type CostBreakdown struct {
	TokensPerJob                 int     `json:"tokensPerJob"`
	MeasuredTokensPerJob         int     `json:"measuredTokensPerJob"`
	ETHPriceUSD                  float64 `json:"ethPriceUsd"`
	L1DirectUSDPer1MTokens       float64 `json:"l1DirectUsdPer1MTokens"`
	L1DirectUSDPerJob            float64 `json:"l1DirectUsdPerJob"`
	L2DirectUSDPer1MTokens       float64 `json:"l2DirectUsdPer1MTokens"`
	L2DirectUSDPerJob            float64 `json:"l2DirectUsdPerJob"`
	CommunityBatchedL2USDPer1M   float64 `json:"communityBatchedL2UsdPer1MTokens"`
	LocalVerifyAndDecryptUSD1M   float64 `json:"localVerifyAndDecryptUsdPer1MTokens"`
	TotalRecommendedUSDPer1MTok  float64 `json:"totalRecommendedUsdPer1MTokens"`
	VCPUHourlyRateUSD            float64 `json:"vcpuHourlyRateUsd"`
	MeasuredLocalCPUsPer1MTokens float64 `json:"measuredLocalCpuSecPer1MTokens"`
}

type TrustCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

type BenchmarkReport struct {
	ProtocolVersion  string                             `json:"protocolVersion"`
	GeneratedAt      time.Time                          `json:"generatedAt"`
	Iterations       int                                `json:"iterations"`
	RTODrillCount    int                                `json:"rtoDrillCount"`
	RegimeTrials     int                                `json:"regimeTrials"`
	ZIPE2EPassed     bool                               `json:"zipE2ePassed"`
	ZIPE2EDurationMs float64                            `json:"zipE2eDurationMs"`
	ExitDrill        *sovereignty.NetworkExitReport     `json:"exitDrill"`
	Cost             CostBreakdown                      `json:"cost"`
	Performance      PerformanceSection                 `json:"performance"`
	Trust            TrustSection                       `json:"trust"`
	Decentralization DecentralizationSection            `json:"decentralization"`
	Sovereignty      SovereigntySection                 `json:"sovereignty"`
	NegativeRegimes  []sovereignty.NegativeRegimeResult `json:"negativeRegimes"`
}

type PerformanceSection struct {
	BlackoutFailoverRTO    LatencyStats `json:"blackoutFailoverRto"`
	PeerAndModelDiscovery  LatencyStats `json:"peerAndModelDiscovery"`
	InferenceTTFTAndExec   LatencyStats `json:"inferenceTtftAndExec"`
	OfflineReceiptVerify   LatencyStats `json:"offlineReceiptVerify"`
	CommunityPaymentSettle LatencyStats `json:"communityPaymentSettle"`
	SovereignDataRecovery  LatencyStats `json:"sovereignDataRecovery"`
	FullPostBlackoutE2E    LatencyStats `json:"fullPostBlackoutE2e"`
	MeasuredTokensPerJob   int          `json:"measuredTokensPerJob"`
	TokensPerSec           float64      `json:"tokensPerSec"`
	CompletionRate         float64      `json:"completionRate"`
	HanzoCallsAfterKill    int64        `json:"hanzoCallsAfterKill"`
}

type TrustSection struct {
	Passed        int          `json:"passed"`
	Total         int          `json:"total"`
	DetectionRate float64      `json:"detectionRate"`
	Checks        []TrustCheck `json:"checks"`
}

type DecentralizationSection struct {
	CriticalFunctionsIndependent int      `json:"criticalFunctionsIndependent"`
	CriticalFunctionsTotal       int      `json:"criticalFunctionsTotal"`
	IndependentShare             float64  `json:"independentShare"`
	HanzoCallsAfterKill          int64    `json:"hanzoCallsAfterKill"`
	Functions                    []string `json:"functions"`
}

type SovereigntySection struct {
	UserDataRecoveryFidelity float64 `json:"userDataRecoveryFidelity"`
	UnauthorizedKeyRejected  bool    `json:"unauthorizedKeyRejected"`
	CustodialEscrowEnabled   bool    `json:"custodialEscrowEnabled"`
	GovernanceRatifiedNoHz   bool    `json:"governanceRatifiedWithoutHanzo"`
	ForkBundleVerified       bool    `json:"forkBundleVerified"`
	ForkBundleBytes          int     `json:"forkBundleBytes"`
	ForkHydratedProviders    int     `json:"forkHydratedProviders"`
	ForkHydratedVaultItems   int     `json:"forkHydratedVaultItems"`
	ConservationDrift        int64   `json:"conservationDrift"`
}

func main() {
	iters := flag.Int("iters", 200, "number of post-blackout E2E HTTP workflow iterations")
	rtoDrills := flag.Int("rto-drills", 20, "number of full 8-stage kill-switch failover drills for RTO measurement")
	regimeTrials := flag.Int("regime-trials", 50, "number of trials per negative/boundary regime")
	outPath := flag.String("out", "", "optional path to write raw JSON benchmark report")
	flag.Parse()

	report, err := runBenchmark(*iters, *rtoDrills, *regimeTrials)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sovereignty-bench failed: %v\n", err)
		os.Exit(1)
	}

	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal report: %v\n", err)
		os.Exit(1)
	}
	if *outPath != "" {
		if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "mkdir %s: %v\n", *outPath, err)
			os.Exit(1)
		}
		if err := os.WriteFile(*outPath, append(raw, '\n'), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write %s: %v\n", *outPath, err)
			os.Exit(1)
		}
	}

	fmt.Printf("HIP-1351 Full Sovereignty & Network Exit Benchmark (%s)\n", report.ProtocolVersion)
	fmt.Printf("  ZIP E2E Acceptance      : pass=%v (%.2f ms)\n", report.ZIPE2EPassed, report.ZIPE2EDurationMs)
	fmt.Printf("  1. Cost (recommended)   : $%.4f / 1M useful tokens (L2 batched + local verify/decrypt)\n", report.Cost.TotalRecommendedUSDPer1MTok)
	fmt.Printf("  2. Performance (RTO)    : p50=%.2f ms, p95=%.2f ms, p99=%.2f ms\n",
		report.Performance.BlackoutFailoverRTO.P50Us/1000.0,
		report.Performance.BlackoutFailoverRTO.P95Us/1000.0,
		report.Performance.BlackoutFailoverRTO.P99Us/1000.0)
	fmt.Printf("  2. Performance (E2E)    : p50=%.2f ms, p95=%.2f ms, p99=%.2f ms (%.1f ops/s, %.1f tok/s, %.1f%% completion)\n",
		report.Performance.FullPostBlackoutE2E.P50Us/1000.0,
		report.Performance.FullPostBlackoutE2E.P95Us/1000.0,
		report.Performance.FullPostBlackoutE2E.P99Us/1000.0,
		report.Performance.FullPostBlackoutE2E.OpsPerS,
		report.Performance.TokensPerSec,
		report.Performance.CompletionRate*100.0)
	fmt.Printf("  3. Trust                : %d / %d adversarial checks passed (%.1f%%)\n",
		report.Trust.Passed, report.Trust.Total, report.Trust.DetectionRate*100.0)
	fmt.Printf("  4. Decentralization     : %d / %d critical functions independent (%.1f%%, HanzoCallsAfterKill=%d)\n",
		report.Decentralization.CriticalFunctionsIndependent,
		report.Decentralization.CriticalFunctionsTotal,
		report.Decentralization.IndependentShare*100.0,
		report.Decentralization.HanzoCallsAfterKill)
	fmt.Printf("  5. Sovereignty          : %.1f%% recovery, forkVerified=%v (%dB), drift=%d\n",
		report.Sovereignty.UserDataRecoveryFidelity*100.0,
		report.Sovereignty.ForkBundleVerified,
		report.Sovereignty.ForkBundleBytes,
		report.Sovereignty.ConservationDrift)
	for _, r := range report.NegativeRegimes {
		fmt.Printf("  Regime %-38s : validated=%v completion=%.1f%% (%s)\n",
			r.Name, r.Validated, r.CompletionRate*100.0, r.ObservedBehavior)
	}
	if *outPath != "" {
		fmt.Printf("  Wrote raw results to %s\n", *outPath)
	}
}

func runBenchmark(iters, rtoDrills, regimeTrials int) (*BenchmarkReport, error) {
	if iters < 1 {
		iters = 100
	}
	if rtoDrills < 1 {
		rtoDrills = 10
	}
	if regimeTrials < 1 {
		regimeTrials = 25
	}
	domain := sovereignty.DefaultDomain()

	// 1. Live zip.App E2E acceptance flow.
	zipStart := time.Now()
	exitReport, forkBundle, err := runZIPAcceptance(domain)
	if err != nil {
		return nil, fmt.Errorf("zip acceptance: %w", err)
	}
	zipDurMs := float64(time.Since(zipStart).Microseconds()) / 1000.0

	// 2. Measure Blackout Failover RTO across rtoDrills independent kill-switch drills.
	rtoSamplesUs := make([]float64, 0, rtoDrills)
	rtoWallStart := time.Now()
	for i := 0; i < rtoDrills; i++ {
		rep, _, err := sovereignty.RunNetworkExitDrill(domain)
		if err != nil {
			return nil, fmt.Errorf("rto drill %d: %w", i, err)
		}
		if !rep.AllStagesPassed || rep.GuardMetrics.HanzoCallsAfterKill != 0 {
			return nil, fmt.Errorf("rto drill %d failed invariant: allPassed=%v hanzoAfterKill=%d",
				i, rep.AllStagesPassed, rep.GuardMetrics.HanzoCallsAfterKill)
		}
		rtoSamplesUs = append(rtoSamplesUs, rep.FailoverRTOMs*1000.0)
	}
	rtoStats := computeStats(rtoSamplesUs, time.Since(rtoWallStart))

	// 3. Measure post-blackout steady-state performance across iters real loopback HTTP workflows.
	perf, err := measurePostBlackoutPerformance(domain, iters, rtoStats)
	if err != nil {
		return nil, fmt.Errorf("post-blackout performance: %w", err)
	}

	// 4. Measure Trust (7 adversarial scenarios).
	trust, err := measureTrustMatrix(domain, forkBundle)
	if err != nil {
		return nil, fmt.Errorf("trust matrix: %w", err)
	}

	// 5. Measure 3 Honest Negative & Boundary Regimes.
	regimes, err := sovereignty.RunNegativeAndBoundaryRegimes(domain, regimeTrials)
	if err != nil {
		return nil, fmt.Errorf("negative regimes: %w", err)
	}

	// 6. Compute Cost model grounded in measured local verify/decrypt CPU times.
	cost := computeCostModel(perf)

	decentFuncs := []string{
		"new_provider_onboarding",
		"peer_discovery",
		"content_addressed_model_retrieval",
		"stateless_job_scheduling",
		"verifiable_inference_execution",
		"offline_receipt_verification",
		"independent_payment_settlement",
		"authorized_data_recovery_and_governance",
	}
	decentIndep := len(decentFuncs)
	if !exitReport.AllStagesPassed || exitReport.GuardMetrics.HanzoCallsAfterKill != 0 || perf.HanzoCallsAfterKill != 0 {
		decentIndep = 0
	}

	return &BenchmarkReport{
		ProtocolVersion:  sovereignty.ProtocolVersion,
		GeneratedAt:      time.Now().UTC(),
		Iterations:       iters,
		RTODrillCount:    rtoDrills,
		RegimeTrials:     regimeTrials,
		ZIPE2EPassed:     exitReport.AllStagesPassed && exitReport.GuardMetrics.HanzoCallsAfterKill == 0,
		ZIPE2EDurationMs: zipDurMs,
		ExitDrill:        exitReport,
		Cost:             cost,
		Performance:      perf,
		Trust:            trust,
		Decentralization: DecentralizationSection{
			CriticalFunctionsIndependent: decentIndep,
			CriticalFunctionsTotal:       len(decentFuncs),
			IndependentShare:             float64(decentIndep) / float64(len(decentFuncs)),
			HanzoCallsAfterKill:          exitReport.GuardMetrics.HanzoCallsAfterKill + perf.HanzoCallsAfterKill,
			Functions:                    decentFuncs,
		},
		Sovereignty: SovereigntySection{
			UserDataRecoveryFidelity: exitReport.SovereigntyRatio,
			UnauthorizedKeyRejected:  exitReport.UnauthorizedKeyBlocked,
			CustodialEscrowEnabled:   false,
			GovernanceRatifiedNoHz:   exitReport.GovernanceRatified,
			ForkBundleVerified:       exitReport.ForkBundleVerified,
			ForkBundleBytes:          exitReport.ForkBundleBytes,
			ForkHydratedProviders:    exitReport.ForkHydratedProviders,
			ForkHydratedVaultItems:   exitReport.ForkHydratedVaultItems,
			ConservationDrift:        exitReport.ConservationDrift,
		},
		NegativeRegimes: regimes,
	}, nil
}

func runZIPAcceptance(domain sovereignty.EIP712Domain) (*sovereignty.NetworkExitReport, *sovereignty.NetworkForkBundle, error) {
	app := zip.New(zip.Config{Logger: luxlog.New("sovereignty-bench")})
	coord := sovereignty.NewCoordinator(domain)
	if err := sovereignty.Mount(app, nil, coord); err != nil {
		return nil, nil, err
	}
	call := func(method, path string, body any) (int, []byte, error) {
		var r io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				return 0, nil, err
			}
			r = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, r)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			return 0, nil, err
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		return resp.StatusCode, raw, err
	}

	if st, _, err := call(http.MethodGet, "/v1/sovereignty/health", nil); err != nil || st != http.StatusOK {
		return nil, nil, fmt.Errorf("health status=%d err=%v", st, err)
	}
	st, raw, err := call(http.MethodPost, "/v1/sovereignty/drill/run", nil)
	if err != nil || st != http.StatusOK {
		return nil, nil, fmt.Errorf("drill/run status=%d err=%v", st, err)
	}
	var rep sovereignty.NetworkExitReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		return nil, nil, err
	}
	st, forkRaw, err := call(http.MethodGet, "/v1/sovereignty/fork/export", nil)
	if err != nil || st != http.StatusOK {
		return nil, nil, fmt.Errorf("fork/export status=%d err=%v", st, err)
	}
	var fork sovereignty.NetworkForkBundle
	if err := json.Unmarshal(forkRaw, &fork); err != nil {
		return nil, nil, err
	}
	if st, _, err := call(http.MethodPost, "/v1/sovereignty/fork/verify", fork); err != nil || st != http.StatusOK {
		return nil, nil, fmt.Errorf("fork/verify status=%d err=%v", st, err)
	}
	return &rep, &fork, nil
}

func measurePostBlackoutPerformance(domain sovereignty.EIP712Domain, iters int, rtoStats LatencyStats) (PerformanceSection, error) {
	guard := sovereignty.NewHostBlocker()
	defer guard.CloseIdleConnections()
	client := guard.NewHTTPClient(10 * time.Second)

	// Bring up Hanzo plane and kill it immediately so all N iterations run under total blackout.
	hanzo, err := sovereignty.NewHanzoControlPlane(domain, guard)
	if err != nil {
		return PerformanceSection{}, err
	}
	sovereignty.KillHanzoControlPlane(hanzo, guard)

	community, err := sovereignty.NewCommunityControlPlane("bench-community-op", domain, client, 2)
	if err != nil {
		return PerformanceSection{}, err
	}
	defer community.Close()

	pubKP, _ := sovereignty.GenerateKeyPair()
	provKP, _ := sovereignty.GenerateKeyPair()
	tenantKP, _ := sovereignty.GenerateKeyPair()
	userKey, _ := sovereignty.GenerateUserDataKey()

	const modelID = "zen-bench-open-7b-v1"
	const tokensPerIter = 128
	modelBlob := sovereignty.DeterministicModelWeights(modelID, 16384)
	modelDigest := sovereignty.SHA256Hex(modelBlob)
	if err := sovereignty.UploadModelBlob(client, community.Mirror.URL(), modelDigest, modelBlob); err != nil {
		return PerformanceSection{}, err
	}
	manifest := sovereignty.ModelManifest{
		ModelID:      modelID,
		Version:      "1.0.0",
		License:      "Apache-2.0",
		DigestSHA256: modelDigest,
		SizeBytes:    int64(len(modelBlob)),
		Mirrors:      []string{community.Mirror.URL()},
		IssuedAt:     time.Now().UTC().Unix(),
	}
	if err := sovereignty.SignModelManifest(domain, pubKP, &manifest); err != nil {
		return PerformanceSection{}, err
	}
	for _, b := range community.BootstrapNodes {
		if err := postJSON(client, b.URL()+"/v1/models", manifest, nil); err != nil {
			return PerformanceSection{}, err
		}
	}

	prov := sovereignty.NewProviderNodeServer("bench-provider-b", provKP, domain, client)
	defer prov.Close()
	if err := prov.FetchAndVerifyModel(community.Mirror.URL(), modelDigest); err != nil {
		return PerformanceSection{}, err
	}
	ad, err := prov.BuildSignedAdvertisement([]string{modelID}, 500, 5000, time.Now().UTC())
	if err != nil {
		return PerformanceSection{}, err
	}
	for _, b := range community.BootstrapNodes {
		if err := postJSON(client, b.URL()+"/v1/peers/register", ad, nil); err != nil {
			return PerformanceSection{}, err
		}
	}
	if err := postJSON(client, community.Facilitator.URL()+"/v1/payments/mint", map[string]any{
		"address": tenantKP.Address(),
		"amount":  uint64(iters) * 1000,
	}, nil); err != nil {
		return PerformanceSection{}, err
	}
	workspace := []byte(`{"workspace":"bench-sovereign-data","records":64,"policy":"non-custodial"}`)
	env, err := sovereignty.EncryptUserData(domain, tenantKP, userKey, "snap-bench-001", workspace, []string{community.Vault.URL()}, []string{community.OperatorID}, time.Now().UTC().Unix())
	if err != nil {
		return PerformanceSection{}, err
	}
	if err := postJSON(client, community.Vault.URL()+"/v1/vault/snapshots", env, nil); err != nil {
		return PerformanceSection{}, err
	}

	discUs := make([]float64, 0, iters)
	inferUs := make([]float64, 0, iters)
	verifyUs := make([]float64, 0, iters)
	settleUs := make([]float64, 0, iters)
	recoverUs := make([]float64, 0, iters)
	e2eUs := make([]float64, 0, iters)

	completed := 0
	totalTokens := 0
	wallStart := time.Now()
	bootURLs := community.BootstrapURLs()

	for i := 0; i < iters; i++ {
		iterStart := time.Now()

		// Step A: Decentralized peer & content-addressed model discovery
		t0 := time.Now()
		peers, err := sovereignty.DiscoverPeersFromBootstraps(client, domain, bootURLs, modelID)
		if err != nil || len(peers) == 0 {
			return PerformanceSection{}, fmt.Errorf("iter %d discover peers: %v", i, err)
		}
		m, err := sovereignty.DiscoverModelManifest(client, domain, bootURLs, modelID)
		if err != nil {
			return PerformanceSection{}, fmt.Errorf("iter %d discover manifest: %w", i, err)
		}
		blob, err := sovereignty.DownloadAndVerifyModelBlob(client, m.Mirrors[0], m.DigestSHA256)
		if err != nil {
			return PerformanceSection{}, fmt.Errorf("iter %d download model: %w", i, err)
		}
		discUs = append(discUs, float64(time.Since(t0).Nanoseconds())/1000.0)

		// Step B: Stateless community scheduling + inference
		t1 := time.Now()
		req := sovereignty.InferenceRequest{
			JobID:             fmt.Sprintf("bench-job-%d", i),
			TenantAddress:     tenantKP.Address(),
			ModelID:           modelID,
			ModelDigestSHA256: m.DigestSHA256,
			Prompt:            fmt.Sprintf("sovereign benchmark prompt #%d", i),
			Seed:              uint64(135100 + i),
			MaxTokens:         tokensPerIter,
			Nonce:             uint64(i + 1),
			Timestamp:         time.Now().UTC().Unix(),
		}
		var receipt sovereignty.InferenceExecutionReceipt
		if err := postJSON(client, community.Scheduler.URL()+"/v1/schedule", map[string]any{"request": req}, &receipt); err != nil {
			return PerformanceSection{}, fmt.Errorf("iter %d schedule: %w", i, err)
		}
		inferUs = append(inferUs, float64(time.Since(t1).Nanoseconds())/1000.0)

		// Step C: Independent offline verification
		t2 := time.Now()
		if err := sovereignty.VerifyExecutionReceiptOffline(domain, req, blob, receipt); err != nil {
			return PerformanceSection{}, fmt.Errorf("iter %d verify: %w", i, err)
		}
		verifyUs = append(verifyUs, float64(time.Since(t2).Nanoseconds())/1000.0)

		// Step D: Independent EIP-712 payment settlement
		t3 := time.Now()
		pay := sovereignty.PaymentAuthorization{
			PaymentID:    fmt.Sprintf("bench-pay-%d", i),
			JobID:        req.JobID,
			PayeeAddress: receipt.ProviderAddress,
			AmountAtomic: 500,
			Nonce:        uint64(i + 1),
			ValidBefore:  time.Now().UTC().Add(10 * time.Minute).Unix(),
			ChainID:      domain.ChainID,
		}
		if err := sovereignty.SignPaymentAuthorization(domain, tenantKP, &pay); err != nil {
			return PerformanceSection{}, err
		}
		var settle sovereignty.SettlementReceipt
		if err := postJSON(client, community.Facilitator.URL()+"/v1/payments/settle", pay, &settle); err != nil {
			return PerformanceSection{}, fmt.Errorf("iter %d settle: %w", i, err)
		}
		if err := sovereignty.VerifySettlementReceipt(domain, settle); err != nil {
			return PerformanceSection{}, fmt.Errorf("iter %d verify settle: %w", i, err)
		}
		settleUs = append(settleUs, float64(time.Since(t3).Nanoseconds())/1000.0)

		// Step E: Authorized encrypted user data recovery
		t4 := time.Now()
		var fetchedEnv sovereignty.EncryptedDataEnvelope
		if err := getJSON(client, community.Vault.URL()+"/v1/vault/snapshots/snap-bench-001", &fetchedEnv); err != nil {
			return PerformanceSection{}, fmt.Errorf("iter %d fetch vault: %w", i, err)
		}
		plain, err := sovereignty.DecryptUserData(domain, userKey, fetchedEnv)
		if err != nil || !bytes.Equal(plain, workspace) {
			return PerformanceSection{}, fmt.Errorf("iter %d decrypt: %v", i, err)
		}
		recoverUs = append(recoverUs, float64(time.Since(t4).Nanoseconds())/1000.0)

		e2eUs = append(e2eUs, float64(time.Since(iterStart).Nanoseconds())/1000.0)
		completed++
		totalTokens += receipt.TokensGenerated
	}
	wallDur := time.Since(wallStart)

	return PerformanceSection{
		BlackoutFailoverRTO:    rtoStats,
		PeerAndModelDiscovery:  computeStats(discUs, sumUsDuration(discUs)),
		InferenceTTFTAndExec:   computeStats(inferUs, sumUsDuration(inferUs)),
		OfflineReceiptVerify:   computeStats(verifyUs, sumUsDuration(verifyUs)),
		CommunityPaymentSettle: computeStats(settleUs, sumUsDuration(settleUs)),
		SovereignDataRecovery:  computeStats(recoverUs, sumUsDuration(recoverUs)),
		FullPostBlackoutE2E:    computeStats(e2eUs, wallDur),
		MeasuredTokensPerJob:   tokensPerIter,
		TokensPerSec:           float64(totalTokens) / wallDur.Seconds(),
		CompletionRate:         float64(completed) / float64(iters),
		HanzoCallsAfterKill:    guard.Metrics().HanzoCallsAfterKill,
	}, nil
}

func measureTrustMatrix(domain sovereignty.EIP712Domain, validFork *sovereignty.NetworkForkBundle) (TrustSection, error) {
	guard := sovereignty.NewHostBlocker()
	defer guard.CloseIdleConnections()
	guard.ActivateKillSwitch()
	client := guard.NewHTTPClient(5 * time.Second)

	community, err := sovereignty.NewCommunityControlPlane("trust-community-op", domain, client, 1)
	if err != nil {
		return TrustSection{}, err
	}
	defer community.Close()

	checks := make([]TrustCheck, 0, 7)

	// 1. Tampered model artifact rejected on SHA-256 mismatch.
	blob := sovereignty.DeterministicModelWeights("trust-model", 4096)
	digest := sovereignty.SHA256Hex(blob)
	_ = sovereignty.UploadModelBlob(client, community.Mirror.URL(), digest, blob)
	community.Mirror.InjectTamperedBlob(digest, true)
	_, err1 := sovereignty.DownloadAndVerifyModelBlob(client, community.Mirror.URL(), digest)
	checks = append(checks, TrustCheck{
		Name:   "tampered_model_artifact_sha256_rejected",
		Passed: errors.Is(err1, sovereignty.ErrModelDigestMismatch),
		Detail: "mirror bit-flip rejected fail-closed with ErrModelDigestMismatch",
	})
	community.Mirror.InjectTamperedBlob(digest, false)

	// 2. Forged/tampered execution receipt rejected offline.
	provKP, _ := sovereignty.GenerateKeyPair()
	tenantKP, _ := sovereignty.GenerateKeyPair()
	prov := sovereignty.NewProviderNodeServer("trust-prov", provKP, domain, client)
	defer prov.Close()
	_ = prov.FetchAndVerifyModel(community.Mirror.URL(), digest)
	prov.SetTamperOutput(true)
	req := sovereignty.InferenceRequest{
		JobID:             "trust-job-1",
		TenantAddress:     tenantKP.Address(),
		ModelID:           "trust-model",
		ModelDigestSHA256: digest,
		Prompt:            "trust verification check",
		Seed:              77,
		MaxTokens:         32,
		Nonce:             1,
		Timestamp:         time.Now().UTC().Unix(),
	}
	var badReceipt sovereignty.InferenceExecutionReceipt
	_ = postJSON(client, prov.URL()+"/v1/infer", req, &badReceipt)
	err2 := sovereignty.VerifyExecutionReceiptOffline(domain, req, blob, badReceipt)
	checks = append(checks, TrustCheck{
		Name:   "forged_or_tampered_execution_receipt_rejected",
		Passed: errors.Is(err2, sovereignty.ErrInvalidReceipt),
		Detail: "tampered inference output rejected offline via deterministic SHA-256 recomputation",
	})

	// 3. Payment replay double-spend rejected with HTTP 409 after a verified initial settlement.
	mintStatus, mintErr := postJSONStatus(client, community.Facilitator.URL()+"/v1/payments/mint", map[string]any{"address": tenantKP.Address(), "amount": uint64(5000)}, nil)
	pay := sovereignty.PaymentAuthorization{
		PaymentID:    "trust-pay-replay",
		JobID:        req.JobID,
		PayeeAddress: provKP.Address(),
		AmountAtomic: 500,
		Nonce:        1,
		ValidBefore:  time.Now().UTC().Add(5 * time.Minute).Unix(),
		ChainID:      domain.ChainID,
	}
	signErr := sovereignty.SignPaymentAuthorization(domain, tenantKP, &pay)
	var firstReceipt sovereignty.SettlementReceipt
	firstStatus, firstErr := postJSONStatus(client, community.Facilitator.URL()+"/v1/payments/settle", pay, &firstReceipt)
	replayStatus, replayErr := postJSONStatus(client, community.Facilitator.URL()+"/v1/payments/settle", pay, nil)
	replayPass := mintErr == nil && mintStatus == http.StatusOK &&
		signErr == nil &&
		firstErr == nil && firstStatus == http.StatusOK &&
		firstReceipt.PaymentID == pay.PaymentID &&
		replayErr != nil && replayStatus == http.StatusConflict &&
		strings.Contains(replayErr.Error(), sovereignty.ErrReplayDetected.Error())
	checks = append(checks, TrustCheck{
		Name:   "payment_replay_double_spend_rejected",
		Passed: replayPass,
		Detail: "initial settlement succeeded (HTTP 200) and duplicate EIP-712 PaymentID rejected with HTTP 409 ErrReplayDetected",
	})

	// 4. Unauthorized third-party key rejected.
	realKey, _ := sovereignty.GenerateUserDataKey()
	wrongKey, _ := sovereignty.GenerateUserDataKey()
	env, _ := sovereignty.EncryptUserData(domain, tenantKP, realKey, "snap-trust", []byte("sovereign-secret"), []string{community.Vault.URL()}, []string{community.OperatorID}, time.Now().UTC().Unix())
	_, err4 := sovereignty.DecryptUserData(domain, wrongKey, env)
	checks = append(checks, TrustCheck{
		Name:   "unauthorized_third_party_data_key_rejected",
		Passed: errors.Is(err4, sovereignty.ErrUnauthorizedDataAccess),
		Detail: "non-owner 32-byte key rejected by AES-256-GCM authentication",
	})

	// 5. Tampered encrypted vault ciphertext rejected (guaranteed bit-flip).
	tamperedEnv := env
	flippedHex, flipErr := flipFirstHexByteBit(env.CiphertextHex)
	tamperedEnv.CiphertextHex = flippedHex
	_, err5 := sovereignty.DecryptUserData(domain, realKey, tamperedEnv)
	checks = append(checks, TrustCheck{
		Name:   "tampered_encrypted_vault_ciphertext_rejected",
		Passed: flipErr == nil && errors.Is(err5, sovereignty.ErrUnauthorizedDataAccess),
		Detail: "ciphertext bit-flip rejected before plaintext release",
	})

	// 6. Tampered fork bundle rejected offline.
	tamperedFork := *validFork
	tamperedFork.OperatorID = "malicious-fork-operator"
	err6 := sovereignty.VerifyForkBundleOffline(domain, tamperedFork)
	checks = append(checks, TrustCheck{
		Name:   "tampered_fork_bundle_rejected",
		Passed: errors.Is(err6, sovereignty.ErrForkBundleCorrupted),
		Detail: "mutated NetworkForkBundle rejected offline on SHA-256 & secp256k1 check",
	})

	// 7. Blackout Hanzo host leak blocked at RoundTripper layer.
	err7 := guard.ProbeBlackoutDirect("https://api.hanzo.ai/v1/health")
	checks = append(checks, TrustCheck{
		Name:   "blackout_hanzo_host_leak_blocked",
		Passed: errors.Is(err7, sovereignty.ErrHanzoHostBlocked),
		Detail: "attempt to contact api.hanzo.ai during blackout rejected with ErrHanzoHostBlocked",
	})

	passed := 0
	for _, c := range checks {
		if c.Passed {
			passed++
		}
	}
	return TrustSection{
		Passed:        passed,
		Total:         len(checks),
		DetectionRate: float64(passed) / float64(len(checks)),
		Checks:        checks,
	}, nil
}

func computeCostModel(perf PerformanceSection) CostBreakdown {
	const (
		modeledTokensPerJob = 10_000
		modeledJobsPer1M    = 100.0
		ethPriceUSD         = 3000.0
		l1Gwei              = 20.0
		l2EffGwei           = 0.05
		directSettleGas     = 65_000.0
		batchAnchorGas      = 90_000.0 // 100 off-chain settlements anchored per L2 batch
		vcpuHourlyUSD       = 0.04
	)
	measuredTokensPerJob := perf.MeasuredTokensPerJob
	if measuredTokensPerJob <= 0 {
		measuredTokensPerJob = 128
	}
	measuredJobsPer1M := 1_000_000.0 / float64(measuredTokensPerJob)

	l1PerJob := directSettleGas * l1Gwei * 1e-9 * ethPriceUSD
	l2PerJob := directSettleGas * l2EffGwei * 1e-9 * ethPriceUSD
	batchedL2Per1M := batchAnchorGas * l2EffGwei * 1e-9 * ethPriceUSD

	// Normalize measured local CPU work using the actual measured token count per job,
	// keeping the modeled 10,000-token L1/L2 gas scenario explicitly separate.
	localUsPerJob := perf.OfflineReceiptVerify.MeanUs + perf.SovereignDataRecovery.MeanUs + perf.PeerAndModelDiscovery.MeanUs
	localSecPer1M := (localUsPerJob * measuredJobsPer1M) / 1e6
	localUSDPer1M := (localSecPer1M / 3600.0) * vcpuHourlyUSD

	return CostBreakdown{
		TokensPerJob:                 modeledTokensPerJob,
		MeasuredTokensPerJob:         measuredTokensPerJob,
		ETHPriceUSD:                  ethPriceUSD,
		L1DirectUSDPer1MTokens:       round6(l1PerJob * modeledJobsPer1M),
		L1DirectUSDPerJob:            round6(l1PerJob),
		L2DirectUSDPer1MTokens:       round6(l2PerJob * modeledJobsPer1M),
		L2DirectUSDPerJob:            round6(l2PerJob),
		CommunityBatchedL2USDPer1M:   round6(batchedL2Per1M),
		LocalVerifyAndDecryptUSD1M:   round6(localUSDPer1M),
		TotalRecommendedUSDPer1MTok:  round6(batchedL2Per1M + localUSDPer1M),
		VCPUHourlyRateUSD:            vcpuHourlyUSD,
		MeasuredLocalCPUsPer1MTokens: round6(localSecPer1M),
	}
}

func computeStats(samplesUs []float64, totalDur time.Duration) LatencyStats {
	if len(samplesUs) == 0 {
		return LatencyStats{}
	}
	cp := append([]float64(nil), samplesUs...)
	sort.Float64s(cp)
	var sum float64
	for _, v := range cp {
		sum += v
	}
	sec := totalDur.Seconds()
	if sec <= 0 {
		sec = sum / 1e6
	}
	return LatencyStats{
		Count:    len(cp),
		P50Us:    round3(percentile(cp, 0.50)),
		P95Us:    round3(percentile(cp, 0.95)),
		P99Us:    round3(percentile(cp, 0.99)),
		MeanUs:   round3(sum / float64(len(cp))),
		OpsPerS:  round2(float64(len(cp)) / sec),
		TotalSec: round4(sec),
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func sumUsDuration(samplesUs []float64) time.Duration {
	var sum float64
	for _, v := range samplesUs {
		sum += v
	}
	return time.Duration(sum * float64(time.Microsecond))
}

func postJSONStatus(client *http.Client, url string, in any, out any) (int, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return 0, err
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("POST %s HTTP %d: %s", url, resp.StatusCode, string(body))
	}
	if out != nil && len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

func postJSON(client *http.Client, url string, in any, out any) error {
	_, err := postJSONStatus(client, url, in, out)
	return err
}

func flipFirstHexByteBit(hexStr string) (string, error) {
	raw, err := hex.DecodeString(hexStr)
	if err != nil {
		return "", err
	}
	if len(raw) == 0 {
		return "", errors.New("empty ciphertext")
	}
	raw[0] ^= 0x01
	return hex.EncodeToString(raw), nil
}

func getJSON(client *http.Client, url string, out any) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s HTTP %d: %s", url, resp.StatusCode, string(body))
	}
	if out != nil && len(body) > 0 {
		return json.Unmarshal(body, out)
	}
	return nil
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }
func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }
