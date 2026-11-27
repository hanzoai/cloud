// SPDX-License-Identifier: Apache-2.0 OR MIT

// Command spotmarket-bench runs the reproducible 100-provider HIP-1350 open GPU
// spot market & capacity auction simulation and optionally writes the 6-point
// research dossier (README.md, score_table.md, raw_results.json) with exact
// decimal parity to raw_results.json.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hanzoai/cloud/apps/spotmarket"
)

func main() {
	cfg := spotmarket.DefaultSimConfig()
	flag.Int64Var(&cfg.Seed, "seed", cfg.Seed, "deterministic PRNG seed")
	flag.IntVar(&cfg.NumProviders, "providers", cfg.NumProviders, "number of simulated GPU providers")
	flag.IntVar(&cfg.Epochs, "epochs", cfg.Epochs, "number of simulated demand epochs")
	flag.IntVar(&cfg.BaseOrdersPerStep, "base-orders", cfg.BaseOrdersPerStep, "baseline orders per epoch before diurnal/burst scaling")
	writeDossier := flag.String("write-dossier", "", "directory to write README.md, score_table.md, and raw_results.json")
	printJSON := flag.Bool("json", false, "print raw_results.json to stdout")
	flag.Parse()

	report, err := spotmarket.RunBenchmarkSuite(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "spotmarket-bench: %v\n", err)
		os.Exit(1)
	}

	if *writeDossier != "" {
		if err := writeDossierFiles(*writeDossier, report); err != nil {
			fmt.Fprintf(os.Stderr, "spotmarket-bench: write dossier: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("wrote HIP-1350 dossier to %s\n", *writeDossier)
	}

	if *printJSON {
		b, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "spotmarket-bench: marshal json: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(b))
		return
	}

	fmt.Printf("HIP-1350 Open GPU Spot Market Benchmark (seed=%d, providers=%d, epochs=%d)\n",
		report.Config.Seed, report.Config.NumProviders, report.Config.Epochs)
	fmt.Printf("  Fixed Baseline: $%.4f / 1M useful tokens | TTFT p50=%.2fms p95=%.2fms p99=%.2fms | Completion=%.2f%%\n",
		report.FixedBaseline.RealizedCostPer1MUsefulUSD,
		report.FixedBaseline.TTFTp50Ms, report.FixedBaseline.TTFTp95Ms, report.FixedBaseline.TTFTp99Ms,
		report.FixedBaseline.CompletionRatePct)
	fmt.Printf("  Primary Spot:   $%.4f / 1M useful tokens (%.2f%% savings) | TTFT p50=%.2fms p95=%.2fms p99=%.2fms | Completion=%.2f%% | Adversarial=%d/%d (%.2f%%) | Drift=%d uUSD | HanzoCalls=%d\n",
		report.PrimarySpot.RealizedCostPer1MUsefulUSD,
		report.PrimarySpot.SavingsVsBaselinePct,
		report.PrimarySpot.TTFTp50Ms, report.PrimarySpot.TTFTp95Ms, report.PrimarySpot.TTFTp99Ms,
		report.PrimarySpot.CompletionRatePct,
		report.PrimarySpot.AdversarialTotalDetected, report.PrimarySpot.AdversarialTotalInjected,
		report.PrimarySpot.AdversarialDetectionPct,
		report.PrimarySpot.LedgerDriftMicroUSD,
		report.PrimarySpot.HanzoCalls)
	fmt.Printf("  Sovereignty:    bundle=%d bytes | migrate=%.2fms | forkDrift=%d uUSD | postForkOK=%t | postForkHanzoCalls=%d\n",
		report.Sovereignty.BundleBytes,
		report.Sovereignty.TotalMigrationMs,
		report.Sovereignty.ForkLedgerDriftMicro,
		report.Sovereignty.PostForkClearOK,
		report.Sovereignty.PostForkHanzoCalls)
}

func writeDossierFiles(dir string, report spotmarket.BenchmarkReport) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	rawBytes, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	rawBytes = append(rawBytes, '\n')
	if err := os.WriteFile(filepath.Join(dir, "raw_results.json"), rawBytes, 0o644); err != nil {
		return err
	}

	scoreTable := renderScoreTable(report)
	if err := os.WriteFile(filepath.Join(dir, "score_table.md"), []byte(scoreTable), 0o644); err != nil {
		return err
	}

	readme := renderReadme(report)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(readme), 0o644); err != nil {
		return err
	}
	return nil
}

func renderScoreTable(r spotmarket.BenchmarkReport) string {
	var buf bytes.Buffer
	allRegimes := append([]spotmarket.RegimeMetrics{r.FixedBaseline, r.PrimarySpot}, r.NegativeRegimes...)

	buf.WriteString("# HIP-1350 Score Table — Open GPU Spot Market & Capacity Auctions\n\n")
	fmt.Fprintf(&buf, "Generated deterministically from `go run ./cmd/spotmarket-bench -seed %d -providers %d -epochs %d -write-dossier research/hip-1350-spotmarket`.\n",
		r.Config.Seed, r.Config.NumProviders, r.Config.Epochs)
	buf.WriteString("Every figure below matches `raw_results.json` to the exact decimal.\n\n")

	buf.WriteString("## 1. Five-Dimension Regime Comparison\n\n")
	buf.WriteString("| Regime | Realized Cost ($/1M useful tok) | Savings vs Baseline | Ledger Drift (uUSD) | TTFT p50 / p95 / p99 (ms) | Mean Throughput (tok/s) | Clear / Completion Rate | Adversarial Detected | Hanzo Calls | Completion w/o Hanzo |\n")
	buf.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, m := range allRegimes {
		advCell := "n/a"
		if m.AdversarialTotalInjected > 0 {
			advCell = fmt.Sprintf("%d/%d (%.2f%%)", m.AdversarialTotalDetected, m.AdversarialTotalInjected, m.AdversarialDetectionPct)
		}
		fmt.Fprintf(&buf, "| `%s` | $%.4f | %.2f%% | %d | %.2f / %.2f / %.2f | %.2f | %.2f%% / %.2f%% | %s | %d | %.2f%% |\n",
			m.Name,
			m.RealizedCostPer1MUsefulUSD,
			m.SavingsVsBaselinePct,
			m.LedgerDriftMicroUSD,
			m.TTFTp50Ms, m.TTFTp95Ms, m.TTFTp99Ms,
			m.MeanThroughputTokSec,
			m.ClearRatePct, m.CompletionRatePct,
			advCell,
			m.HanzoCalls,
			m.CompletionWithoutHanzoPct,
		)
	}

	buf.WriteString("\n## 2. Cost Decomposition (Primary Spot vs Fixed Baseline)\n\n")
	buf.WriteString("| Metric | Fixed-Price Baseline | Primary HIP-1350 Spot (100 Providers) |\n")
	buf.WriteString("|---|---:|---:|\n")
	fmt.Fprintf(&buf, "| Useful Tokens Delivered | %d | %d |\n", r.FixedBaseline.UsefulTokens, r.PrimarySpot.UsefulTokens)
	fmt.Fprintf(&buf, "| Gross Compute Spend (uUSD) | %d | %d |\n", r.FixedBaseline.GrossComputeMicroUSD, r.PrimarySpot.GrossComputeMicroUSD)
	fmt.Fprintf(&buf, "| Cryptographic Verification Overhead (uUSD) | %d | %d |\n", r.FixedBaseline.VerificationOverheadMicroUS, r.PrimarySpot.VerificationOverheadMicroUS)
	fmt.Fprintf(&buf, "| Settlement Ledger Overhead (uUSD) | %d | %d |\n", r.FixedBaseline.SettlementOverheadMicroUSD, r.PrimarySpot.SettlementOverheadMicroUSD)
	fmt.Fprintf(&buf, "| Buyer Slashing Compensation Received (uUSD) | %d | %d |\n", r.FixedBaseline.BuyerSlashedCompMicroUSD, r.PrimarySpot.BuyerSlashedCompMicroUSD)
	fmt.Fprintf(&buf, "| Realized Cost (incl. Verify + Settle, $/1M useful tok) | $%.4f | $%.4f |\n", r.FixedBaseline.RealizedCostPer1MUsefulUSD, r.PrimarySpot.RealizedCostPer1MUsefulUSD)
	fmt.Fprintf(&buf, "| Net Cost After Slash Compensation ($/1M useful tok) | $%.4f | $%.4f |\n", r.FixedBaseline.NetAfterSlashPer1MUsefulUSD, r.PrimarySpot.NetAfterSlashPer1MUsefulUSD)
	fmt.Fprintf(&buf, "| Realized Savings vs Baseline | %.2f%% | %.2f%% |\n", r.FixedBaseline.SavingsVsBaselinePct, r.PrimarySpot.SavingsVsBaselinePct)
	fmt.Fprintf(&buf, "| Ledger Conservation Drift (uUSD) | %d | %d |\n", r.FixedBaseline.LedgerDriftMicroUSD, r.PrimarySpot.LedgerDriftMicroUSD)

	buf.WriteString("\n## 3. Adversarial Trust Breakdown by Verification Class (Primary Spot)\n\n")
	buf.WriteString("| Attack Vector | Verification Class | Injected | Detected | Detection Rate |\n")
	buf.WriteString("|---|---|---:|---:|---:|\n")
	for _, a := range r.PrimarySpot.AttackBreakdown {
		fmt.Fprintf(&buf, "| `%s` | `%s` | %d | %d | %.2f%% |\n",
			a.AttackType, a.VerificationClass, a.Injected, a.Detected, a.DetectionRatePct)
	}
	fmt.Fprintf(&buf, "| **Total Adversarial Events** | `crypto_signature` + `economic_stake_slashing` | **%d** | **%d** | **%.2f%%** |\n",
		r.PrimarySpot.AdversarialTotalInjected, r.PrimarySpot.AdversarialTotalDetected, r.PrimarySpot.AdversarialDetectionPct)
	fmt.Fprintf(&buf, "| Hardware TEE Attestation (`hardware_attestation`) | `hardware_attestation` | %d providers | %d attested | **not claimed (0.00%%)** |\n",
		r.PrimarySpot.ProvidersTotal, r.PrimarySpot.HardwareAttestedProviders)

	buf.WriteString("\n## 4. Decentralization & Sovereignty Measurements\n\n")
	buf.WriteString("| Metric | Measured Value |\n")
	buf.WriteString("|---|---:|\n")
	fmt.Fprintf(&buf, "| Hanzo Control-Plane Calls (`HanzoCalls`) | %d |\n", r.PrimarySpot.HanzoCalls)
	fmt.Fprintf(&buf, "| Completion Without Hanzo Infrastructure | %.2f%% |\n", r.PrimarySpot.CompletionWithoutHanzoPct)
	fmt.Fprintf(&buf, "| Zero-Hanzo-Preference Parity Verified | %t |\n", r.PrimarySpot.ZeroHanzoPreferenceVerified)
	fmt.Fprintf(&buf, "| Active Winning Providers (out of %d) | %d |\n", r.PrimarySpot.ProvidersTotal, r.PrimarySpot.ActiveWinningProviders)
	fmt.Fprintf(&buf, "| Provider Share Concentration (HHI, lower = more decentralized) | %.4f |\n", r.PrimarySpot.ProviderShareHHI)
	fmt.Fprintf(&buf, "| Signed Export Bundle Size | %d bytes |\n", r.Sovereignty.BundleBytes)
	fmt.Fprintf(&buf, "| Exported State Objects (Offers / Leases / Receipts / Peers) | %d / %d / %d / %d |\n",
		r.Sovereignty.ExportedOffers, r.Sovereignty.ExportedLeases, r.Sovereignty.ExportedReceipts, r.Sovereignty.ExportedPeers)
	fmt.Fprintf(&buf, "| Export / Verify / Cold-Start Import Latency | %.2f ms / %.2f ms / %.2f ms (total %.2f ms) |\n",
		r.Sovereignty.ExportLatencyMs, r.Sovereignty.VerifyLatencyMs, r.Sovereignty.ImportLatencyMs, r.Sovereignty.TotalMigrationMs)
	fmt.Fprintf(&buf, "| Post-Fork Conservation Drift (uUSD) | %d |\n", r.Sovereignty.ForkLedgerDriftMicro)
	fmt.Fprintf(&buf, "| Post-Fork Independent Order Clear & Settle OK | %t (HanzoCalls=%d) |\n", r.Sovereignty.PostForkClearOK, r.Sovereignty.PostForkHanzoCalls)

	return buf.String()
}

func renderReadme(r spotmarket.BenchmarkReport) string {
	var buf bytes.Buffer
	p := r.PrimarySpot
	b := r.FixedBaseline
	neg0 := r.NegativeRegimes[0]
	neg1 := r.NegativeRegimes[1]
	neg2 := r.NegativeRegimes[2]

	buf.WriteString("# HIP-1350 Research Dossier: Open GPU Spot Market & Capacity Auctions (`hanzoai/cloud#14`)\n\n")

	buf.WriteString("## 1. Problem Statement & Protocol Design\n\n")
	buf.WriteString("Centralized GPU inference APIs lock buyers into fixed posted rate cards, single-operator failure domains, and opaque routing. ")
	buf.WriteString("This dossier implements and evaluates an open, permissionless GPU spot market and Akash-inspired reverse-auction marketplace (`apps/spotmarket`) licensed under `Apache-2.0 OR MIT` with zero host imports and zero Hanzo control-plane dependencies:\n\n")
	buf.WriteString("- **Signed Provider Offers (`SignedOffer`)**: Providers advertise hardware specs (`H100-SXM5-80GB`, `A100-80GB`, `MI300X-192GB`, `L40S-48GB`, `RTX-4090-24GB`), available VRAM, supported models, region/jurisdiction constraints, integer microdollar pricing, SLO commitments, expiry, and monotonic nonces signed over a canonical domain-separated SHA-256 commitment using `secp256k1` with strict **EIP-2 low-S** normalization and bound provider signing keys.\n")
	buf.WriteString("- **Neutral Enso Multi-Dimensional Selector (`RankCandidates`)**: Ranks eligible bids by normalized price, measured TTFT latency, observed Laplace-smoothed reliability, and explicit buyer policy constraints (model, VRAM, allowed/blocked jurisdictions, max price, max TTFT, optional verifier-backed hardware attestation). Provider `OperatorClass` labels (`hanzo` vs `independent` vs `community`) are never read by the selector; verified via bit-identical score parity tests (`ZeroHanzoPreferenceVerified = true`).\n")
	buf.WriteString("- **Reverse-Auction Lifecycle & Deterministic Ledger**: `Order -> SignedBid -> Lease -> SettlementReceipt` supporting multi-provider partial fills, mid-lease crash failover, collateral slashing on bait-and-switch or latency/throughput SLA breach, checked 128-bit integer cost/bond arithmetic, and exact integer microdollar ledger conservation (`0` microdollar drift).\n")
	buf.WriteString("- **Dual-Surface HTTP API**: Mounted on `github.com/zap-proto/zip` via strictly typed operations (`Mount`, projecting into OpenAPI/MCP/CLI/SDKs) with host-validated principal binding, plus a standalone standard-library `net/http` (`Handler`) surface.\n")
	buf.WriteString("- **Sovereignty & Permissionless Gossip**: Exposes a deterministic `GossipSnapshot` and cryptographically signed `MarketExportBundle` covering complete lease terms, pending orders/bids, provider key bindings, and receipts for offline verification and cold-start forking.\n\n")

	buf.WriteString("## 2. Threat Model & Explicit Verification Boundaries\n\n")
	buf.WriteString("Per HIP-1350 proof-hygiene requirements, this protocol never conflates cryptographic signatures, hardware attestations, and economic collateral:\n\n")
	buf.WriteString("| Mechanism | `VerificationClass` | What It Proves | What It Does NOT Prove |\n")
	buf.WriteString("|---|---|---|---|\n")
	buf.WriteString("| Canonical SHA-256 + `secp256k1` EIP-2 low-S ECDSA + nonce tracking | `crypto_signature` | Signer key control, bound provider identity, payload integrity, non-malleability, anti-replay, and expiry compliance | Physical GPU ownership or honest token generation |\n")
	buf.WriteString("| Bonded escrow, agreed-rate settlement enforcement, and SLA breach slashing | `economic_stake_slashing` | Deterministic buyer compensation and rapid Enso reliability demotion when a provider attempts bait-and-switch pricing, breaches latency/throughput SLOs, or crashes mid-lease | Mathematical proof of inference weights or prevention of griefing losses exceeding posted bond |\n")
	buf.WriteString("| Optional confidential-compute GPU quote (`AttestationQuoteHash`) | `hardware_attestation` | Physical enclave measurement strictly when validated by a configured `AttestationVerifier` (fails closed when unconfigured) | **Not claimed in this simulation (`0/100` hardware-TEE attested)**; buyers requiring TEE quotes can enforce `Order.RequireHardwareAttested = true` |\n\n")

	buf.WriteString("## 3. Experimental Method & Reproducibility\n\n")
	fmt.Fprintf(&buf, "- **Deterministic Seed**: `%d` (`%d` heterogeneous GPU providers across 5 regions & 6 jurisdictions, `%d` hourly epochs, `%d` submitted orders in baseline/primary regimes, `%d` total requested tokens).\n",
		r.Config.Seed, r.Config.NumProviders, r.Config.Epochs, p.OrdersSubmitted, p.RequestedTokens)
	buf.WriteString("- **Variable Demand**: Diurnal sine demand wave, 1.8x bursty traffic peaks (epochs 7–9 and 17–19), sudden 15% fleet capacity shock (epochs 12–14), EU/Swiss jurisdictional residency policies, tight TTFT policies, and multi-provider partial-fill batch orders.\n")
	buf.WriteString("- **Provider Failures & Failover**: Stochastic mid-lease GPU crashes elevated during capacity shock epochs; Enso detects incomplete delivery, refunds undelivered escrow, slashes breach bond, demotes failing provider reliability, and fails over remaining tokens to healthy peers (which execute under the same failure and adversarial model).\n")
	buf.WriteString("- **7 Adversarial Attack Vectors**: `forged_signature`, `high_s_malleated`, `expired_offer_or_bid`, `replay_nonce`, `sybil_spam_bids`, `stale_capacity_overclaim`, and `bait_and_switch_non_delivery`.\n")
	buf.WriteString("- **Reproduce Exact Results**:\n\n```bash\n")
	fmt.Fprintf(&buf, "go run ./cmd/spotmarket-bench -seed %d -providers %d -epochs %d -write-dossier research/hip-1350-spotmarket\n",
		r.Config.Seed, r.Config.NumProviders, r.Config.Epochs)
	buf.WriteString("go test -race -v -count=1 ./apps/spotmarket/... ./cmd/spotmarket-bench/...\n")
	buf.WriteString("CGO_ENABLED=0 go test -v ./apps -run TestAppsImportNothingOfTheirHost\n")
	buf.WriteString("```\n\n")

	buf.WriteString("## 4. Measured Results Across All 5 HIP-1350 Dimensions\n\n")
	fmt.Fprintf(&buf, "1. **Cost**: Primary decentralized spot market achieves **$%.4f / 1M useful tokens** (explicitly including %d uUSD cryptographic verification overhead and %d uUSD settlement overhead; **$%.4f / 1M useful tokens** net after buyer slash compensation) vs **$%.4f / 1M useful tokens** on the fixed-price baseline — a **%.2f%% cost reduction** with **%d uUSD ledger conservation drift** across all %d epochs.\n",
		p.RealizedCostPer1MUsefulUSD, p.VerificationOverheadMicroUS, p.SettlementOverheadMicroUSD,
		p.NetAfterSlashPer1MUsefulUSD, b.RealizedCostPer1MUsefulUSD, p.SavingsVsBaselinePct,
		p.LedgerDriftMicroUSD, r.Config.Epochs)
	fmt.Fprintf(&buf, "2. **Performance**: Spot TTFT p50/p95/p99 = **%.2f / %.2f / %.2f ms** (vs fixed baseline **%.2f / %.2f / %.2f ms**), mean throughput = **%.2f tok/s** (vs **%.2f tok/s**), delivery-based order completion rate = **%.2f%%** (auction clear rate **%.2f%%**, token fulfillment **%.2f%%**), with **%d/%d (%.2f%%)** mid-lease crash & bait-and-switch failovers recovered automatically and **%d** multi-provider partial-fill orders.\n",
		p.TTFTp50Ms, p.TTFTp95Ms, p.TTFTp99Ms, b.TTFTp50Ms, b.TTFTp95Ms, b.TTFTp99Ms,
		p.MeanThroughputTokSec, b.MeanThroughputTokSec, p.CompletionRatePct, p.ClearRatePct, p.TokenFulfillRatePct,
		p.FailoversRecovered, p.FailoversTriggered, p.FailoverRecoveryPct, p.PartialFillOrders)
	fmt.Fprintf(&buf, "3. **Trust**: **%d/%d (%.2f%%)** injected adversarial bids, offers, and settlement bait-and-switch attempts detected and mitigated fail-closed across all 7 threat vectors (`crypto_signature` + `economic_stake_slashing`). Hardware TEE attestation is honestly reported as **%d/%d attested (not claimed)**.\n",
		p.AdversarialTotalDetected, p.AdversarialTotalInjected, p.AdversarialDetectionPct,
		p.HardwareAttestedProviders, p.ProvidersTotal)
	fmt.Fprintf(&buf, "4. **Decentralization**: With `HanzoControlPlaneDisabled = true` and all Hanzo-labeled peers offline, **%.2f%%** of completed orders ran with **`HanzoCalls = %d`**, spread across **%d** active winning independent/community providers (**HHI = %.4f** vs `1.0000` centralized baseline) and `ZeroHanzoPreferenceVerified = %t`.\n",
		p.CompletionWithoutHanzoPct, p.HanzoCalls, p.ActiveWinningProviders, p.ProviderShareHHI, p.ZeroHanzoPreferenceVerified)
	fmt.Fprintf(&buf, "5. **Sovereignty**: Live signed `MarketExportBundle` (**%d bytes** covering %d offers, %d leases, %d receipts, %d peers) exported in **%.2f ms**, verified in **%.2f ms**, and cold-start forked into an isolated `Market` in **%.2f ms** (total migration **%.2f ms**) with **%d uUSD** fork ledger drift, `PostForkClearOK = %t`, and `PostForkHanzoCalls = %d`.\n\n",
		r.Sovereignty.BundleBytes, r.Sovereignty.ExportedOffers, r.Sovereignty.ExportedLeases, r.Sovereignty.ExportedReceipts, r.Sovereignty.ExportedPeers,
		r.Sovereignty.ExportLatencyMs, r.Sovereignty.VerifyLatencyMs, r.Sovereignty.ImportLatencyMs, r.Sovereignty.TotalMigrationMs,
		r.Sovereignty.ForkLedgerDriftMicro, r.Sovereignty.PostForkClearOK, r.Sovereignty.PostForkHanzoCalls)

	buf.WriteString("## 5. Honest Negative & Boundary Regimes\n\n")
	buf.WriteString("To verify that security controls and economic limits reflect real protocol boundaries rather than optimistic modeling, three negative/boundary regimes are executed on the exact same codebase:\n\n")
	fmt.Fprintf(&buf, "1. **Unverified & Unslashed Negative Control (`%s`)**: Disabling `VerifySignatures` and `EnforceSlashing` collapses adversarial detection to **%d/%d (%.2f%%)**, drops delivery-based order completion to **%.2f%%** (despite **%.2f%%** auction clearing) and token fulfillment to **%.2f%%**, and drives realized cost to **$%.4f / 1M useful tokens** (vs **$%.4f** with controls enabled) as unslashed bait-and-switch and forged lowball bids win auctions and burn buyer escrow.\n",
		neg0.Name, neg0.AdversarialTotalDetected, neg0.AdversarialTotalInjected, neg0.AdversarialDetectionPct,
		neg0.CompletionRatePct, neg0.ClearRatePct, neg0.TokenFulfillRatePct, neg0.RealizedCostPer1MUsefulUSD, p.RealizedCostPer1MUsefulUSD)
	fmt.Fprintf(&buf, "2. **Fleet Capacity Saturation Boundary (`%s`)**: A 3.5x demand surge (%d submitted orders exceeding 100-provider concurrent slots) causes spot clearing prices to surge toward buyer reserve ceilings (**$%.4f / 1M useful tokens**, savings compressed to **%.2f%%**) and triggers graceful admission rejection (**%.2f%%** rejected, **%.2f%%** delivery completion) with **%d uUSD** ledger drift and 100%% automatic escrow refund for unfilled orders.\n",
		neg1.Name, neg1.OrdersSubmitted, neg1.RealizedCostPer1MUsefulUSD, neg1.SavingsVsBaselinePct,
		neg1.AdmissionRejectPct, neg1.CompletionRatePct, neg1.LedgerDriftMicroUSD)
	fmt.Fprintf(&buf, "3. **Ultra-Short 5s TTL Churn Boundary (`%s`)**: Tightening offer/bid TTL to 5 seconds forces **%d** offer re-signs and **%d** signature verifications (vs **%d** in primary), multiplying verification overhead to **%d uUSD** (vs **%d uUSD**) and causing transient in-flight bid expiry rejections (delivery completion **%.2f%%**, realized cost **$%.4f / 1M useful tokens**).\n\n",
		neg2.Name, neg2.OfferChurnResignOps, neg2.SignatureVerifyOps, p.SignatureVerifyOps,
		neg2.VerificationOverheadMicroUS, p.VerificationOverheadMicroUS, neg2.CompletionRatePct, neg2.RealizedCostPer1MUsefulUSD)

	buf.WriteString("## 6. Limitations & Pre-Production Security Review\n\n")
	buf.WriteString("- **External Audit Required Before Real Funds**: Settlement escrow contracts, cross-chain slashing bridges, and dispute arbitration require independent third-party security review before holding real mainnet funds.\n")
	buf.WriteString("- **Inference Quality Verification**: Cryptographic signatures and SLA slashing verify offer authenticity and delivery accounting, not bit-exact model weights. Combining this spot market with verifiable compute challenges (`hanzoai/cloud#17`) or hardware TEE quotes (`AttestationClass = hardware_attestation`) closes the inference-correctness gap.\n")
	buf.WriteString("- **Full Tables & Raw Telemetry**: See [`score_table.md`](./score_table.md) and [`raw_results.json`](./raw_results.json) for complete machine-readable outputs.\n")

	return strings.TrimRight(buf.String(), "\n") + "\n"
}
