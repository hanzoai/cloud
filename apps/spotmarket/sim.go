// SPDX-License-Identifier: Apache-2.0 OR MIT

package spotmarket

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"time"
)

// SimConfig parameterizes a reproducible 100-provider HIP-1350 spot market
// benchmark run.
type SimConfig struct {
	Seed              int64 `json:"seed"`
	NumProviders      int   `json:"numProviders"`
	Epochs            int   `json:"epochs"`
	BaseOrdersPerStep int   `json:"baseOrdersPerStep"`
	BaseTimeUnix      int64 `json:"baseTimeUnix"`
}

// DefaultSimConfig returns the canonical HIP-1350 100-provider benchmark config.
func DefaultSimConfig() SimConfig {
	return SimConfig{
		Seed:              1350,
		NumProviders:      100,
		Epochs:            24,
		BaseOrdersPerStep: 10,
		BaseTimeUnix:      1760000000,
	}
}

// AttackVectorMetrics reports injected vs detected counts for one threat class,
// explicitly labeled with its VerificationClass.
type AttackVectorMetrics struct {
	AttackType        string            `json:"attackType"`
	VerificationClass VerificationClass `json:"verificationClass"`
	Injected          int               `json:"injected"`
	Detected          int               `json:"detected"`
	DetectionRatePct  float64           `json:"detectionRatePct"`
}

// RegimeMetrics captures all 5 HIP-1350 evaluation dimensions for a single
// simulation regime.
type RegimeMetrics struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	IsNegative  bool   `json:"isNegative"`

	// Workload counters
	ProvidersTotal      int   `json:"providersTotal"`
	OrdersSubmitted     int   `json:"ordersSubmitted"`
	OrdersCleared       int   `json:"ordersCleared"`
	OrdersCompleted     int   `json:"ordersCompleted"`
	OrdersRejected      int   `json:"ordersRejected"`
	LeasesAwarded       int   `json:"leasesAwarded"`
	FailoversTriggered  int   `json:"failoversTriggered"`
	FailoversRecovered  int   `json:"failoversRecovered"`
	RequestedTokens     int64 `json:"requestedTokens"`
	UsefulTokens        int64 `json:"usefulTokens"`
	PartialFillOrders   int   `json:"partialFillOrders"`
	SignatureVerifyOps  int64 `json:"signatureVerifyOps"`
	OfferChurnResignOps int64 `json:"offerChurnResignOps"`

	// Dimension 1: Cost ($/1M useful tokens including verification & settlement)
	GrossComputeMicroUSD        int64   `json:"grossComputeMicroUsd"`
	VerificationOverheadMicroUS int64   `json:"verificationOverheadMicroUsd"`
	SettlementOverheadMicroUSD  int64   `json:"settlementOverheadMicroUsd"`
	BuyerSlashedCompMicroUSD    int64   `json:"buyerSlashedCompMicroUsd"`
	RealizedCostPer1MUsefulUSD  float64 `json:"realizedCostPer1MUsefulUsd"`
	NetAfterSlashPer1MUsefulUSD float64 `json:"netAfterSlashPer1MUsefulUsd"`
	SavingsVsBaselinePct        float64 `json:"savingsVsBaselinePct"`
	LedgerDriftMicroUSD         int64   `json:"ledgerDriftMicroUsd"`

	// Dimension 2: Performance
	TTFTp50Ms             float64 `json:"ttftP50Ms"`
	TTFTp95Ms             float64 `json:"ttftP95Ms"`
	TTFTp99Ms             float64 `json:"ttftP99Ms"`
	LatencyP50Ms          float64 `json:"latencyP50Ms"`
	LatencyP95Ms          float64 `json:"latencyP95Ms"`
	LatencyP99Ms          float64 `json:"latencyP99Ms"`
	MeanThroughputTokSec  float64 `json:"meanThroughputTokPerSec"`
	ClearRatePct          float64 `json:"clearRatePct"`
	CompletionRatePct     float64 `json:"completionRatePct"`
	TokenFulfillRatePct   float64 `json:"tokenFulfillRatePct"`
	AdmissionRejectPct    float64 `json:"admissionRejectPct"`
	FailoverRecoveryPct   float64 `json:"failoverRecoveryPct"`
	AverageClearingPriceM float64 `json:"averageClearingPriceUsdPer1M"`

	// Dimension 3: Trust (explicit VerificationClass separation)
	AdversarialTotalInjected   int                   `json:"adversarialTotalInjected"`
	AdversarialTotalDetected   int                   `json:"adversarialTotalDetected"`
	AdversarialDetectionPct    float64               `json:"adversarialDetectionPct"`
	HardwareAttestedProviders  int                   `json:"hardwareAttestedProviders"`
	HardwareAttestationClaimed bool                  `json:"hardwareAttestationClaimed"`
	AttackBreakdown            []AttackVectorMetrics `json:"attackBreakdown"`

	// Dimension 4: Decentralization
	HanzoCalls                  int64   `json:"hanzoCalls"`
	HanzoControlPlaneDisabled   bool    `json:"hanzoControlPlaneDisabled"`
	CompletionWithoutHanzoPct   float64 `json:"completionWithoutHanzoPct"`
	ActiveWinningProviders      int     `json:"activeWinningProviders"`
	ProviderShareHHI            float64 `json:"providerShareHhi"`
	ZeroHanzoPreferenceVerified bool    `json:"zeroHanzoPreferenceVerified"`
}

// SovereigntyMetrics captures live signed export, verification, and cold-start
// fork migration measurements.
type SovereigntyMetrics struct {
	BundleBytes          int     `json:"bundleBytes"`
	ExportedOffers       int     `json:"exportedOffers"`
	ExportedLeases       int     `json:"exportedLeases"`
	ExportedReceipts     int     `json:"exportedReceipts"`
	ExportedPeers        int     `json:"exportedPeers"`
	ExportLatencyMs      float64 `json:"exportLatencyMs"`
	VerifyLatencyMs      float64 `json:"verifyLatencyMs"`
	ImportLatencyMs      float64 `json:"importLatencyMs"`
	TotalMigrationMs     float64 `json:"totalMigrationMs"`
	ForkLedgerDriftMicro int64   `json:"forkLedgerDriftMicroUsd"`
	PostForkClearOK      bool    `json:"postForkClearOk"`
	PostForkHanzoCalls   int64   `json:"postForkHanzoCalls"`
	SignatureVerified    bool    `json:"signatureVerified"`
}

// BenchmarkReport is the complete, deterministic artifact serialized to
// raw_results.json and rendered into score_table.md and README.md.
type BenchmarkReport struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Config          SimConfig          `json:"config"`
	FixedBaseline   RegimeMetrics      `json:"fixedBaseline"`
	PrimarySpot     RegimeMetrics      `json:"primarySpot"`
	NegativeRegimes []RegimeMetrics    `json:"negativeRegimes"`
	Sovereignty     SovereigntyMetrics `json:"sovereignty"`
}

type simProvider struct {
	id            string
	operatorClass string
	kp            *KeyPair
	hardware      GPUHardwareSpec
	location      LocationSpec
	models        []string
	basePrices    map[string]int64 // microUSD per 1M tokens
	baseTTFTMs    float64
	baseTokPerSec float64
	slots         int
	adversarial   bool
	advRole       string
	crashProb     float64
	nonce         uint64
	activeOfferID string
	offline       bool
}

type simOrderSpec struct {
	epoch            int
	indexInEpoch     int
	model            string
	requestedTokens  int64
	minVRAMGB        int
	maxPriceMicroUSD int64
	allowedRegions   []string
	allowedJurisd    []string
	maxTTFTMs        int64
	allowPartialFill bool
	fixedBasePrice   int64
	burstMultiplier  float64
	shockEpoch       bool
}

type regimeOpts struct {
	name                   string
	description            string
	isNegative             bool
	verifySignatures       bool
	enforceSlashing        bool
	hanzoKillSwitch        bool
	demandScale            float64
	maxSlotsPerProvider    int
	offerTTLSeconds        int64
	bidTTLSeconds          int64
	inFlightChurnExpirePct float64
	churnResignsPerEpoch   int
	captureSovereignty     bool
}

// RunBenchmarkSuite executes the fixed-price baseline, the primary 100-provider
// HIP-1350 decentralized spot market regime, 3 honest negative/boundary regimes,
// and the live sovereignty export/fork migration test.
func RunBenchmarkSuite(cfg SimConfig) (BenchmarkReport, error) {
	if cfg.NumProviders <= 0 {
		cfg.NumProviders = 100
	}
	if cfg.Epochs <= 0 {
		cfg.Epochs = 24
	}
	if cfg.BaseOrdersPerStep <= 0 {
		cfg.BaseOrdersPerStep = 10
	}
	if cfg.BaseTimeUnix <= 0 {
		cfg.BaseTimeUnix = 1760000000
	}

	orders := buildDemandWorkload(cfg, 1.0)
	baseline := simulateFixedPriceBaseline(cfg, orders)

	primary, sov, err := simulateSpotRegime(cfg, orders, regimeOpts{
		name:               "hip1350_decentralized_spot_100p",
		description:        "Primary 100-provider HIP-1350 reverse-auction spot market with EIP-2 low-S secp256k1 verification, collateral slashing, failover, and Hanzo kill-switch active (HanzoCalls == 0).",
		isNegative:         false,
		verifySignatures:   true,
		enforceSlashing:    true,
		hanzoKillSwitch:    true,
		demandScale:        1.0,
		offerTTLSeconds:    3600,
		bidTTLSeconds:      600,
		captureSovereignty: true,
	}, baseline.RealizedCostPer1MUsefulUSD)
	if err != nil {
		return BenchmarkReport{}, err
	}

	// Negative Regime 1: Unverified bids + no slashing control
	negUnverified, _, err := simulateSpotRegime(cfg, orders, regimeOpts{
		name:             "neg_unverified_no_slashing_control",
		description:      "Negative control: disables secp256k1 verification and collateral slashing under identical adversarial load so forged/malleated/Sybil/bait-and-switch bids win auctions and fail delivery.",
		isNegative:       true,
		verifySignatures: false,
		enforceSlashing:  false,
		hanzoKillSwitch:  true,
		demandScale:      1.0,
		offerTTLSeconds:  3600,
		bidTTLSeconds:    600,
	}, baseline.RealizedCostPer1MUsefulUSD)
	if err != nil {
		return BenchmarkReport{}, err
	}

	// Negative Regime 2: Demand spike exceeding total 100-provider fleet concurrent capacity
	saturatedOrders := buildDemandWorkload(cfg, 3.5)
	negSaturation, _, err := simulateSpotRegime(cfg, saturatedOrders, regimeOpts{
		name:                "neg_fleet_capacity_saturation",
		description:         "Boundary regime: 3.5x concurrent demand surge exceeding 100-provider single-slot fleet capacity, driving spot clearing prices toward buyer reserve ceilings and triggering graceful admission rejection with 0 ledger drift.",
		isNegative:          true,
		verifySignatures:    true,
		enforceSlashing:     true,
		hanzoKillSwitch:     true,
		demandScale:         3.5,
		maxSlotsPerProvider: 1,
		offerTTLSeconds:     3600,
		bidTTLSeconds:       600,
	}, baseline.RealizedCostPer1MUsefulUSD)
	if err != nil {
		return BenchmarkReport{}, err
	}

	// Negative Regime 3: Ultra-short 5s TTL with rapid offer churn & WAN gossip expiry
	negTTLChurn, _, err := simulateSpotRegime(cfg, orders, regimeOpts{
		name:                   "neg_ultra_short_ttl_churn",
		description:            "Boundary regime: ultra-short 5-second offer/bid TTL with continuous re-signing churn and WAN gossip propagation expiry, quantifying verification overhead inflation and clearing drop-offs.",
		isNegative:             true,
		verifySignatures:       true,
		enforceSlashing:        true,
		hanzoKillSwitch:        true,
		demandScale:            1.0,
		offerTTLSeconds:        5,
		bidTTLSeconds:          5,
		inFlightChurnExpirePct: 0.78,
		churnResignsPerEpoch:   6,
	}, baseline.RealizedCostPer1MUsefulUSD)
	if err != nil {
		return BenchmarkReport{}, err
	}

	return BenchmarkReport{
		ProtocolVersion: ProtocolVersion,
		Config:          cfg,
		FixedBaseline:   baseline,
		PrimarySpot:     primary,
		NegativeRegimes: []RegimeMetrics{negUnverified, negSaturation, negTTLChurn},
		Sovereignty:     sov,
	}, nil
}

func buildProviders(cfg SimConfig) ([]*simProvider, error) {
	rng := rand.New(rand.NewSource(cfg.Seed + 101))
	regions := []struct {
		region string
		jurisd string
	}{
		{"us-west", "US"},
		{"us-east", "US"},
		{"eu-central", "DE"},
		{"eu-central", "CH"},
		{"eu-north", "SE"},
		{"ap-northeast", "JP"},
		{"us-east", "CA"},
	}

	type hwTier struct {
		model        string
		vram         int
		interconnect string
		fp8          float64
		models       []string
		price70b     int64
		price32b     int64
		price8b      int64
		ttftMs       float64
		tokSec       float64
		slots        int
	}
	tiers := []hwTier{
		{
			model: "H100-SXM5-80GB", vram: 80, interconnect: "NVLink", fp8: 1979,
			models:   []string{"llama-3.3-70b", "qwen-2.5-32b", "llama-3.1-8b"},
			price70b: 540_000, price32b: 220_000, price8b: 85_000,
			ttftMs: 78, tokSec: 125, slots: 4,
		},
		{
			model: "A100-80GB", vram: 80, interconnect: "NVLink", fp8: 624,
			models:   []string{"llama-3.3-70b", "qwen-2.5-32b", "llama-3.1-8b"},
			price70b: 460_000, price32b: 195_000, price8b: 75_000,
			ttftMs: 112, tokSec: 88, slots: 4,
		},
		{
			model: "MI300X-192GB", vram: 192, interconnect: "InfinityFabric", fp8: 2610,
			models:   []string{"llama-3.3-70b", "qwen-2.5-32b", "llama-3.1-8b"},
			price70b: 490_000, price32b: 205_000, price8b: 80_000,
			ttftMs: 92, tokSec: 118, slots: 5,
		},
		{
			model: "L40S-48GB", vram: 48, interconnect: "PCIe-Gen5", fp8: 733,
			models:   []string{"qwen-2.5-32b", "llama-3.1-8b"},
			price70b: 0, price32b: 175_000, price8b: 65_000,
			ttftMs: 96, tokSec: 95, slots: 4,
		},
		{
			model: "RTX-4090-24GB", vram: 24, interconnect: "PCIe-Gen4", fp8: 330,
			models:   []string{"llama-3.1-8b"},
			price70b: 0, price32b: 0, price8b: 55_000,
			ttftMs: 88, tokSec: 108, slots: 3,
		},
	}

	providers := make([]*simProvider, 0, cfg.NumProviders)
	for i := 0; i < cfg.NumProviders; i++ {
		kp, err := DeriveKeyPair("sim-provider", uint64(cfg.Seed), uint64(i+1))
		if err != nil {
			return nil, err
		}

		// Fleet composition: 25% H100, 25% A100, 15% MI300X, 20% L40S, 15% RTX-4090
		var tier hwTier
		switch {
		case i < (cfg.NumProviders*25)/100:
			tier = tiers[0]
		case i < (cfg.NumProviders*50)/100:
			tier = tiers[1]
		case i < (cfg.NumProviders*65)/100:
			tier = tiers[2]
		case i < (cfg.NumProviders*85)/100:
			tier = tiers[3]
		default:
			tier = tiers[4]
		}

		opClass := "independent"
		if i%20 == 19 {
			opClass = "hanzo" // 5% Hanzo-labeled peers (zero preference, offline in kill-switch)
		} else if i%7 == 0 {
			opClass = "community"
		}

		loc := regions[i%len(regions)]
		// Price spread +/- 18% around tier median for realistic spot dispersion
		spread := 0.84 + rng.Float64()*0.34
		prices := make(map[string]int64)
		if tier.price70b > 0 {
			prices["llama-3.3-70b"] = int64(float64(tier.price70b) * spread)
		}
		if tier.price32b > 0 {
			prices["qwen-2.5-32b"] = int64(float64(tier.price32b) * spread)
		}
		if tier.price8b > 0 {
			prices["llama-3.1-8b"] = int64(float64(tier.price8b) * spread)
		}

		// 12% adversarial providers across distinct attack profiles
		adv := false
		advRole := ""
		if i%9 == 8 && opClass != "hanzo" {
			adv = true
			roles := []string{
				"bait_and_switch",
				"sybil_spam",
				"forged_and_malleated",
				"replay_and_overclaim",
			}
			advRole = roles[(i/9)%len(roles)]
		}

		// Honest provider mid-lease crash rate ~6-10% under shock, ~3% normal
		crashProb := 0.025 + rng.Float64()*0.045

		p := &simProvider{
			id:            fmt.Sprintf("prov-%03d", i+1),
			operatorClass: opClass,
			kp:            kp,
			hardware: GPUHardwareSpec{
				GPUModel:        tier.model,
				GPUCount:        1,
				AvailableVRAMGB: tier.vram,
				TotalVRAMGB:     tier.vram,
				Interconnect:    tier.interconnect,
				FP8TFLOPS:       tier.fp8,
			},
			location: LocationSpec{
				Region:         loc.region,
				Jurisdiction:   loc.jurisd,
				DatacenterTier: 2 + (i % 3),
			},
			models:        append([]string(nil), tier.models...),
			basePrices:    prices,
			baseTTFTMs:    tier.ttftMs * (0.85 + rng.Float64()*0.35),
			baseTokPerSec: tier.tokSec * (0.88 + rng.Float64()*0.25),
			slots:         tier.slots,
			adversarial:   adv,
			advRole:       advRole,
			crashProb:     crashProb,
		}
		providers = append(providers, p)
	}
	return providers, nil
}

func buildDemandWorkload(cfg SimConfig, demandScale float64) []simOrderSpec {
	rng := rand.New(rand.NewSource(cfg.Seed + 202))
	orders := make([]simOrderSpec, 0, int(float64(cfg.Epochs*cfg.BaseOrdersPerStep)*demandScale*1.5))

	for epoch := 0; epoch < cfg.Epochs; epoch++ {
		// Diurnal sine wave: trough ~0.7x, peak ~1.4x
		phase := float64(epoch) * 2.0 * math.Pi / float64(cfg.Epochs)
		diurnal := 1.0 + 0.35*math.Sin(phase-math.Pi/2)

		// Bursty traffic surges in epochs 7..9 and 17..19
		burst := 1.0
		if (epoch >= 7 && epoch <= 9) || (epoch >= 17 && epoch <= 19) {
			burst = 1.8
		}
		// Sudden capacity shock in epochs 12..14
		shock := epoch >= 12 && epoch <= 14

		count := int(math.Round(float64(cfg.BaseOrdersPerStep) * diurnal * burst * demandScale))
		if count < 4 {
			count = 4
		}

		for j := 0; j < count; j++ {
			roll := rng.Float64()
			var (
				model      string
				minVRAM    int
				fixedPrice int64
				reserveMax int64
				tokens     int64
				partial    bool
			)
			switch {
			case roll < 0.45:
				model = "llama-3.3-70b"
				minVRAM = 80
				fixedPrice = 1_200_000 // $1.20 / 1M tokens centralized baseline
				reserveMax = 920_000   // buyer reserve ceiling $0.92 / 1M tokens
				tokens = int64(350_000 + rng.Intn(750_000))
				partial = (j%4 == 0)
			case roll < 0.78:
				model = "qwen-2.5-32b"
				minVRAM = 48
				fixedPrice = 500_000 // $0.50 / 1M tokens centralized baseline
				reserveMax = 380_000
				tokens = int64(250_000 + rng.Intn(550_000))
				partial = (j%5 == 0)
			default:
				model = "llama-3.1-8b"
				minVRAM = 24
				fixedPrice = 200_000 // $0.20 / 1M tokens centralized baseline
				reserveMax = 155_000
				tokens = int64(200_000 + rng.Intn(400_000))
				partial = false
			}

			var allowedJurisd []string
			if j%6 == 1 {
				// 1/6 of orders enforce EU/Swiss data residency
				allowedJurisd = []string{"DE", "CH", "SE"}
			}
			var maxTTFT int64
			if j%5 == 2 {
				maxTTFT = 185
			}

			orders = append(orders, simOrderSpec{
				epoch:            epoch,
				indexInEpoch:     j,
				model:            model,
				requestedTokens:  tokens,
				minVRAMGB:        minVRAM,
				maxPriceMicroUSD: reserveMax,
				allowedJurisd:    allowedJurisd,
				maxTTFTMs:        maxTTFT,
				allowPartialFill: partial,
				fixedBasePrice:   fixedPrice,
				burstMultiplier:  burst,
				shockEpoch:       shock,
			})
		}
	}
	return orders
}

// computeGenerationLatencyMs returns total simulated execution latency in
// milliseconds: TTFT (ms) plus generation duration across an 8-way parallel
// batch converted from seconds to milliseconds (* 1000.0).
func computeGenerationLatencyMs(ttftMs float64, tokens int64, tokPerSec float64) float64 {
	if tokPerSec <= 0 || tokens <= 0 {
		return ttftMs
	}
	genSeconds := float64(tokens) / (tokPerSec * 8.0)
	return ttftMs + genSeconds*1000.0
}

func simulateFixedPriceBaseline(cfg SimConfig, orders []simOrderSpec) RegimeMetrics {
	rng := rand.New(rand.NewSource(cfg.Seed + 303))
	ttfts := make([]float64, 0, len(orders))
	lats := make([]float64, 0, len(orders))
	var sumTokSec float64
	var totalReqTokens, totalUsefulTokens, totalComputeMicro int64
	completed := 0

	for _, o := range orders {
		totalReqTokens += o.requestedTokens
		// Centralized hyperscaler experiences burst queuing delay & ~1.2% transient 503 drops
		if o.burstMultiplier > 1.5 && rng.Float64() < 0.015 {
			continue
		}
		completed++
		totalUsefulTokens += o.requestedTokens
		totalComputeMicro += ComputeTokenCostMicroUSD(o.requestedTokens, o.fixedBasePrice)

		baseTTFT := 135.0
		if o.model == "llama-3.3-70b" {
			baseTTFT = 165.0
		} else if o.model == "llama-3.1-8b" {
			baseTTFT = 95.0
		}
		queuePenalty := 0.0
		if o.burstMultiplier > 1.5 {
			queuePenalty = 85.0 + rng.Float64()*140.0
		}
		ttft := baseTTFT + queuePenalty + rng.Float64()*35.0
		tokSec := 78.0 + rng.Float64()*24.0
		if o.model == "llama-3.1-8b" {
			tokSec += 35.0
		}
		lat := computeGenerationLatencyMs(ttft, o.requestedTokens, tokSec)
		ttfts = append(ttfts, ttft)
		lats = append(lats, lat)
		sumTokSec += tokSec
	}

	realizedPer1M := 0.0
	if totalUsefulTokens > 0 {
		realizedPer1M = float64(totalComputeMicro) / float64(totalUsefulTokens)
	}
	meanTokSec := 0.0
	if len(ttfts) > 0 {
		meanTokSec = sumTokSec / float64(len(ttfts))
	}

	return RegimeMetrics{
		Name:                        "fixed_price_baseline",
		Description:                 "Centralized fixed-price inference baseline ($1.20/1M 70B, $0.50/1M 32B, $0.20/1M 8B) with single-operator dependency and burst queuing.",
		IsNegative:                  false,
		ProvidersTotal:              1,
		OrdersSubmitted:             len(orders),
		OrdersCleared:               completed,
		OrdersCompleted:             completed,
		OrdersRejected:              len(orders) - completed,
		LeasesAwarded:               completed,
		RequestedTokens:             totalReqTokens,
		UsefulTokens:                totalUsefulTokens,
		GrossComputeMicroUSD:        totalComputeMicro,
		RealizedCostPer1MUsefulUSD:  round4(realizedPer1M),
		NetAfterSlashPer1MUsefulUSD: round4(realizedPer1M),
		SavingsVsBaselinePct:        0.0,
		LedgerDriftMicroUSD:         0,
		TTFTp50Ms:                   round2(percentile(ttfts, 0.50)),
		TTFTp95Ms:                   round2(percentile(ttfts, 0.95)),
		TTFTp99Ms:                   round2(percentile(ttfts, 0.99)),
		LatencyP50Ms:                round2(percentile(lats, 0.50)),
		LatencyP95Ms:                round2(percentile(lats, 0.95)),
		LatencyP99Ms:                round2(percentile(lats, 0.99)),
		MeanThroughputTokSec:        round2(meanTokSec),
		ClearRatePct:                round2(pct(completed, len(orders))),
		CompletionRatePct:           round2(pct(completed, len(orders))),
		TokenFulfillRatePct:         round2(pctInt64(totalUsefulTokens, totalReqTokens)),
		AdmissionRejectPct:          round2(pct(len(orders)-completed, len(orders))),
		FailoverRecoveryPct:         0.0,
		AverageClearingPriceM:       round4(realizedPer1M),
		HardwareAttestedProviders:   0,
		HardwareAttestationClaimed:  false,
		HanzoCalls:                  int64(len(orders)),
		HanzoControlPlaneDisabled:   false,
		CompletionWithoutHanzoPct:   0.0,
		ActiveWinningProviders:      1,
		ProviderShareHHI:            1.0,
		ZeroHanzoPreferenceVerified: false,
	}
}

func simulateSpotRegime(cfg SimConfig, orders []simOrderSpec, opts regimeOpts, baselineCostPer1M float64) (RegimeMetrics, SovereigntyMetrics, error) {
	providers, err := buildProviders(cfg)
	if err != nil {
		return RegimeMetrics{}, SovereigntyMetrics{}, err
	}
	return simulateSpotRegimeWithProviders(cfg, providers, orders, opts, baselineCostPer1M)
}

func simulateSpotRegimeWithProviders(cfg SimConfig, providers []*simProvider, orders []simOrderSpec, opts regimeOpts, baselineCostPer1M float64) (RegimeMetrics, SovereigntyMetrics, error) {
	rng := rand.New(rand.NewSource(cfg.Seed + 404))

	mcfg := DefaultConfig()
	mcfg.VerifySignatures = opts.verifySignatures
	mcfg.EnforceSlashing = opts.enforceSlashing
	mcfg.HanzoControlPlaneDisabled = opts.hanzoKillSwitch
	market := NewMarket(mcfg)

	// Fund buyers & providers with large deterministic balances so accounting
	// reflects true demand/supply rather than artificial test underfunding.
	const buyerID = "buyer-fleet"
	if err := market.DepositBuyer(buyerID, 50_000_000_000); err != nil {
		return RegimeMetrics{}, SovereigntyMetrics{}, err
	}
	for _, p := range providers {
		if err := market.DepositProviderWithKey(p.id, p.kp.PubKeyHex, 5_000_000_000); err != nil {
			return RegimeMetrics{}, SovereigntyMetrics{}, err
		}
		// Register permissionless peer in gossip overlay (skip Hanzo peers when kill-switch active)
		if opts.hanzoKillSwitch && p.operatorClass == "hanzo" {
			p.offline = true
			continue
		}
		_ = market.RegisterPeer(PeerNode{
			PeerID:        "peer-" + p.id,
			Endpoint:      fmt.Sprintf("https://%s.spot.example:8443", p.id),
			PubKeyHex:     p.kp.PubKeyHex,
			Regions:       []string{p.location.Region},
			LastSeenUnix:  cfg.BaseTimeUnix,
			OperatorClass: p.operatorClass,
		})
	}

	// Track all 7 adversarial attack vectors explicitly with VerificationClass
	attackOrder := []struct {
		name  string
		class VerificationClass
	}{
		{"forged_signature", VerificationCryptoSignature},
		{"high_s_malleated", VerificationCryptoSignature},
		{"expired_offer_or_bid", VerificationCryptoSignature},
		{"replay_nonce", VerificationCryptoSignature},
		{"sybil_spam_bids", VerificationCryptoSignature},
		{"stale_capacity_overclaim", VerificationCryptoSignature},
		{"bait_and_switch_non_delivery", VerificationEconomicStake},
	}
	atkInjected := make(map[string]int)
	atkDetected := make(map[string]int)

	// Group orders by epoch
	ordersByEpoch := make(map[int][]simOrderSpec)
	for _, o := range orders {
		ordersByEpoch[o.epoch] = append(ordersByEpoch[o.epoch], o)
	}

	var (
		ordersCleared      int
		ordersRejected     int
		leasesAwarded      int
		partialFillOrders  int
		failoversTriggered int
		failoversRecovered int
		totalReqTokens     int64
		totalUsefulTokens  int64
		totalComputeMicro  int64
		totalSlashComp     int64
		totalSettleOverhd  int64
		churnResigns       int64
		clearingPriceSum   float64
		clearingPriceCount int
		ttfts              []float64
		lats               []float64
		sumTokSec          float64
		providerWins       = make(map[string]int64)
		orderRequested     = make(map[string]int64, len(orders))
		orderDelivered     = make(map[string]int64, len(orders))
		provByID           = make(map[string]*simProvider, len(providers))
	)
	for _, p := range providers {
		provByID[p.id] = p
	}

	// Keep one previously used signed bid per provider to craft authentic replay-nonce attacks
	lastValidBid := make(map[string]SignedBid)

	for epoch := 0; epoch < cfg.Epochs; epoch++ {
		epochNow := cfg.BaseTimeUnix + int64(epoch)*3600

		// Sudden capacity shock in epochs 12..14: 15% of non-Hanzo providers drop offline
		for idx, p := range providers {
			if opts.hanzoKillSwitch && p.operatorClass == "hanzo" {
				p.offline = true
				continue
			}
			if epoch >= 12 && epoch <= 14 && idx%7 == 2 {
				p.offline = true
			} else {
				p.offline = false
			}
		}

		// Publish fresh signed offers for online providers for this epoch
		epochOrders := ordersByEpoch[epoch]
		demandPressure := 1.0
		if opts.demandScale > 1.5 {
			// Under fleet saturation, providers raise spot quotes toward reserve ceiling
			demandPressure = 1.68
		} else if len(epochOrders) > cfg.BaseOrdersPerStep*14/10 {
			demandPressure = 1.12
		}

		publishOffers := func(now int64, suffix string) error {
			for _, p := range providers {
				if p.offline {
					continue
				}
				p.nonce++
				primaryPrice := p.basePrices["llama-3.3-70b"]
				if primaryPrice == 0 {
					primaryPrice = p.basePrices["qwen-2.5-32b"]
				}
				if primaryPrice == 0 {
					primaryPrice = p.basePrices["llama-3.1-8b"]
				}
				listPrice := int64(float64(primaryPrice) * demandPressure)
				slots := p.slots
				if opts.maxSlotsPerProvider > 0 && slots > opts.maxSlotsPerProvider {
					slots = opts.maxSlotsPerProvider
				}
				offerID := fmt.Sprintf("offer-%s-e%02d%s", p.id, epoch, suffix)
				offer := Offer{
					ProtocolVersion:   ProtocolVersion,
					OfferID:           offerID,
					ProviderID:        p.id,
					ProviderPubKeyHex: p.kp.PubKeyHex,
					OperatorClass:     p.operatorClass,
					Hardware:          p.hardware,
					SupportedModels:   p.models,
					Location:          p.location,
					Pricing: PricingSpec{
						MicroUSDPer1MTokens: listPrice,
						MicroUSDPerGPUHour:  listPrice * 4,
						MinBillableTokens:   1000,
					},
					SLO: ServiceCommitment{
						TargetTTFTMs:           int64(math.Ceil(p.baseTTFTMs*1.35)) + 25,
						MinThroughputTokPerSec: math.Floor(p.baseTokPerSec * 0.85),
						UptimeBPS:              9920,
						CollateralMicroUSD:     500_000,
						SlashBPSOnBreach:       5000,
					},
					AvailableSlots:   slots,
					IssuedAtUnix:     now,
					ExpiresAtUnix:    now + opts.offerTTLSeconds,
					Nonce:            p.nonce,
					AttestationClass: VerificationCryptoSignature,
				}
				so, err := SignOffer(p.kp.PrivateKey, offer)
				if err != nil {
					return err
				}
				if err := market.RegisterOffer(so, now); err != nil {
					return err
				}
				p.activeOfferID = offerID
			}
			return nil
		}

		if err := publishOffers(epochNow, ""); err != nil {
			return RegimeMetrics{}, SovereigntyMetrics{}, err
		}

		// In ultra-short TTL churn regime, simulate repeated offer refresh/re-signing
		for c := 0; c < opts.churnResignsPerEpoch; c++ {
			churnNow := epochNow + int64(c+1)*(opts.offerTTLSeconds-1)
			if err := publishOffers(churnNow, fmt.Sprintf("-c%02d", c)); err != nil {
				return RegimeMetrics{}, SovereigntyMetrics{}, err
			}
			churnResigns += int64(len(providers))
		}

		// Inject an expired offer attack once per epoch
		if len(providers) > 0 {
			ap := providers[0]
			ap.nonce++
			expOffer, _ := SignOffer(ap.kp.PrivateKey, Offer{
				ProtocolVersion:   ProtocolVersion,
				OfferID:           fmt.Sprintf("expired-offer-e%02d", epoch),
				ProviderID:        ap.id,
				ProviderPubKeyHex: ap.kp.PubKeyHex,
				OperatorClass:     ap.operatorClass,
				Hardware:          ap.hardware,
				SupportedModels:   ap.models,
				Location:          ap.location,
				Pricing:           PricingSpec{MicroUSDPer1MTokens: 100_000, MicroUSDPerGPUHour: 400_000},
				SLO:               ServiceCommitment{TargetTTFTMs: 80, MinThroughputTokPerSec: 100, UptimeBPS: 9900, SlashBPSOnBreach: 5000},
				AvailableSlots:    2,
				IssuedAtUnix:      epochNow - 120,
				ExpiresAtUnix:     epochNow - 10,
				Nonce:             ap.nonce,
				AttestationClass:  VerificationCryptoSignature,
			})
			atkInjected["expired_offer_or_bid"]++
			if err := market.RegisterOffer(expOffer, epochNow); err != nil {
				atkDetected["expired_offer_or_bid"]++
			}
		}

		// Execute orders in waves (simulating concurrent slots occupancy per wave)
		waveStep := 12
		if opts.demandScale > 1.5 && len(epochOrders) > 0 {
			// In saturation mode, all epoch orders arrive concurrently against limited fleet slots
			waveStep = len(epochOrders)
		}
		for waveStart := 0; waveStart < len(epochOrders); waveStart += waveStep {
			waveEnd := waveStart + waveStep
			if waveEnd > len(epochOrders) {
				waveEnd = len(epochOrders)
			}
			wave := epochOrders[waveStart:waveEnd]
			waveNow := epochNow + int64(opts.churnResignsPerEpoch)*(opts.offerTTLSeconds-1)
			if opts.churnResignsPerEpoch == 0 {
				waveNow = epochNow + int64(waveStart)*2
			}

			type activeExec struct {
				spec     simOrderSpec
				lease    Lease
				provider *simProvider
			}
			var activeWave []activeExec

			for _, spec := range wave {
				totalReqTokens += spec.requestedTokens
				orderID := fmt.Sprintf("ord-e%02d-%03d", spec.epoch, spec.indexInEpoch)
				orderRequested[orderID] = spec.requestedTokens
				order := Order{
					OrderID:                orderID,
					BuyerID:                buyerID,
					Model:                  spec.model,
					RequestedTokens:        spec.requestedTokens,
					MinVRAMGB:              spec.minVRAMGB,
					MaxMicroUSDPer1MTokens: spec.maxPriceMicroUSD,
					AllowedJurisdictions:   spec.allowedJurisd,
					MaxTTFTMs:              spec.maxTTFTMs,
					AllowPartialFill:       spec.allowPartialFill,
					EscrowMicroUSD:         ComputeTokenCostMicroUSD(spec.requestedTokens, spec.maxPriceMicroUSD),
					CreatedAtUnix:          waveNow,
					ExpiresAtUnix:          waveNow + 300,
					RankingWeights:         DefaultRankingWeights(),
				}
				if err := market.SubmitOrder(order, waveNow); err != nil {
					ordersRejected++
					continue
				}

				// Eligible providers submit reverse-auction bids (subset of ~14 peers per order for realistic gossip propagation)
				biddersSubmitted := 0
				startIdx := (spec.epoch*17 + spec.indexInEpoch*11) % len(providers)
				for k := 0; k < len(providers) && biddersSubmitted < 16; k++ {
					p := providers[(startIdx+k)%len(providers)]
					if p.offline || p.activeOfferID == "" {
						continue
					}
					basePrice, supports := p.basePrices[spec.model]
					if !supports {
						continue
					}
					if len(spec.allowedJurisd) > 0 && !containsString(spec.allowedJurisd, p.location.Jurisdiction) {
						continue
					}

					// Adversarial provider behaviors
					if p.adversarial {
						switch p.advRole {
						case "forged_and_malleated":
							// 1. Forged signature bid (lowball price signed by wrong key)
							p.nonce++
							wrongKP, _ := DeriveKeyPair("attacker-wrong-key", uint64(cfg.Seed), uint64(spec.epoch*1000+spec.indexInEpoch+k))
							forgedBid, _ := SignBid(wrongKP.PrivateKey, Bid{
								ProtocolVersion:        ProtocolVersion,
								BidID:                  fmt.Sprintf("bid-forged-%s-%s", orderID, p.id),
								OrderID:                orderID,
								OfferID:                p.activeOfferID,
								ProviderID:             p.id,
								ProviderPubKeyHex:      p.kp.PubKeyHex, // claims victim pubkey
								BidMicroUSDPer1MTokens: basePrice / 3,
								OfferedTokens:          spec.requestedTokens,
								CommittedVRAMGB:        p.hardware.AvailableVRAMGB,
								BondMicroUSD:           200_000,
								IssuedAtUnix:           waveNow,
								ExpiresAtUnix:          waveNow + opts.bidTTLSeconds,
								Nonce:                  p.nonce,
							})
							atkInjected["forged_signature"]++
							if err := market.SubmitBid(forgedBid, waveNow); err != nil {
								atkDetected["forged_signature"]++
							}

							// 2. High-S EIP-2 malleated signature bid
							p.nonce++
							legitLowball, _ := SignBid(p.kp.PrivateKey, Bid{
								ProtocolVersion:        ProtocolVersion,
								BidID:                  fmt.Sprintf("bid-highs-%s-%s", orderID, p.id),
								OrderID:                orderID,
								OfferID:                p.activeOfferID,
								ProviderID:             p.id,
								ProviderPubKeyHex:      p.kp.PubKeyHex,
								BidMicroUSDPer1MTokens: basePrice / 3,
								OfferedTokens:          spec.requestedTokens,
								CommittedVRAMGB:        p.hardware.AvailableVRAMGB,
								BondMicroUSD:           200_000,
								IssuedAtUnix:           waveNow,
								ExpiresAtUnix:          waveNow + opts.bidTTLSeconds,
								Nonce:                  p.nonce,
							})
							malHex, _ := MalleateSignatureHighS(legitLowball.SignatureHex)
							legitLowball.SignatureHex = malHex
							atkInjected["high_s_malleated"]++
							if err := market.SubmitBid(legitLowball, waveNow); err != nil {
								atkDetected["high_s_malleated"]++
							}
							continue

						case "sybil_spam":
							// First submit an unbonded/lowball spam flood of 3 bids on the same order
							for s := 0; s < 3; s++ {
								p.nonce++
								bond := int64(200_000)
								if s > 0 {
									bond = 0 // unbonded spam
								}
								spamBid, _ := SignBid(p.kp.PrivateKey, Bid{
									ProtocolVersion:        ProtocolVersion,
									BidID:                  fmt.Sprintf("bid-sybil-%s-%s-%d", orderID, p.id, s),
									OrderID:                orderID,
									OfferID:                p.activeOfferID,
									ProviderID:             p.id,
									ProviderPubKeyHex:      p.kp.PubKeyHex,
									BidMicroUSDPer1MTokens: basePrice / 2,
									OfferedTokens:          spec.requestedTokens,
									CommittedVRAMGB:        p.hardware.AvailableVRAMGB,
									BondMicroUSD:           bond,
									IssuedAtUnix:           waveNow,
									ExpiresAtUnix:          waveNow + opts.bidTTLSeconds,
									Nonce:                  p.nonce,
								})
								if s == 0 {
									_ = market.SubmitBid(spamBid, waveNow)
								} else {
									atkInjected["sybil_spam_bids"]++
									if err := market.SubmitBid(spamBid, waveNow); err != nil {
										atkDetected["sybil_spam_bids"]++
									}
								}
							}
							continue

						case "replay_and_overclaim":
							// 1. Replay a previously used nonce if available
							if prev, ok := lastValidBid[p.id]; ok {
								replayBid, _ := SignBid(p.kp.PrivateKey, Bid{
									ProtocolVersion:        ProtocolVersion,
									BidID:                  fmt.Sprintf("bid-replay-%s-%s", orderID, p.id),
									OrderID:                orderID,
									OfferID:                p.activeOfferID,
									ProviderID:             p.id,
									ProviderPubKeyHex:      p.kp.PubKeyHex,
									BidMicroUSDPer1MTokens: basePrice / 2,
									OfferedTokens:          spec.requestedTokens,
									CommittedVRAMGB:        p.hardware.AvailableVRAMGB,
									BondMicroUSD:           200_000,
									IssuedAtUnix:           waveNow,
									ExpiresAtUnix:          waveNow + opts.bidTTLSeconds,
									Nonce:                  prev.Bid.Nonce, // REPLAY!
								})
								atkInjected["replay_nonce"]++
								if err := market.SubmitBid(replayBid, waveNow); err != nil {
									atkDetected["replay_nonce"]++
								}
							}
							// 2. Stale capacity over-claim (claiming 4x VRAM beyond physical GPU spec)
							p.nonce++
							overBid, _ := SignBid(p.kp.PrivateKey, Bid{
								ProtocolVersion:        ProtocolVersion,
								BidID:                  fmt.Sprintf("bid-overclaim-%s-%s", orderID, p.id),
								OrderID:                orderID,
								OfferID:                p.activeOfferID,
								ProviderID:             p.id,
								ProviderPubKeyHex:      p.kp.PubKeyHex,
								BidMicroUSDPer1MTokens: basePrice / 2,
								OfferedTokens:          spec.requestedTokens,
								CommittedVRAMGB:        p.hardware.AvailableVRAMGB * 4,
								BondMicroUSD:           200_000,
								IssuedAtUnix:           waveNow,
								ExpiresAtUnix:          waveNow + opts.bidTTLSeconds,
								Nonce:                  p.nonce,
							})
							atkInjected["stale_capacity_overclaim"]++
							if err := market.SubmitBid(overBid, waveNow); err != nil {
								atkDetected["stale_capacity_overclaim"]++
							}
							// Seed a valid nonce once so subsequent orders trigger replay test
							if _, hasPrev := lastValidBid[p.id]; !hasPrev {
								p.nonce++
								seedBid, _ := SignBid(p.kp.PrivateKey, Bid{
									ProtocolVersion:        ProtocolVersion,
									BidID:                  fmt.Sprintf("bid-seed-%s-%s", orderID, p.id),
									OrderID:                orderID,
									OfferID:                p.activeOfferID,
									ProviderID:             p.id,
									ProviderPubKeyHex:      p.kp.PubKeyHex,
									BidMicroUSDPer1MTokens: basePrice,
									OfferedTokens:          spec.requestedTokens,
									CommittedVRAMGB:        p.hardware.AvailableVRAMGB,
									BondMicroUSD:           ComputeTokenCostMicroUSD(spec.requestedTokens, basePrice)/2 + 1000,
									IssuedAtUnix:           waveNow,
									ExpiresAtUnix:          waveNow + opts.bidTTLSeconds,
									Nonce:                  p.nonce,
								})
								if err := market.SubmitBid(seedBid, waveNow); err == nil {
									lastValidBid[p.id] = seedBid
								}
							}
							continue

						case "bait_and_switch":
							// Cryptographically valid ultra-low bid with real bond -> wins auction, then tries price inflation/non-delivery at settlement!
							p.nonce++
							lowPrice := (basePrice * 62) / 100
							bidVal := ComputeTokenCostMicroUSD(spec.requestedTokens, lowPrice)
							bond := (bidVal*int64(mcfg.MinBondBPS) + 9999) / 10000
							if bond < 10_000 {
								bond = 10_000
							}
							bsBid, _ := SignBid(p.kp.PrivateKey, Bid{
								ProtocolVersion:        ProtocolVersion,
								BidID:                  fmt.Sprintf("bid-bait-%s-%s", orderID, p.id),
								OrderID:                orderID,
								OfferID:                p.activeOfferID,
								ProviderID:             p.id,
								ProviderPubKeyHex:      p.kp.PubKeyHex,
								BidMicroUSDPer1MTokens: lowPrice,
								OfferedTokens:          spec.requestedTokens,
								CommittedVRAMGB:        p.hardware.AvailableVRAMGB,
								BondMicroUSD:           bond,
								IssuedAtUnix:           waveNow,
								ExpiresAtUnix:          waveNow + opts.bidTTLSeconds,
								Nonce:                  p.nonce,
							})
							_ = market.SubmitBid(bsBid, waveNow)
							biddersSubmitted++
							continue
						}
					}

					// Honest reverse-auction bid (competitive discount in normal periods, surge in saturation)
					p.nonce++
					priceMult := demandPressure * (0.90 + rng.Float64()*0.14)
					if spec.shockEpoch {
						priceMult *= 1.14
					}
					bidPrice := int64(float64(basePrice) * priceMult)
					offeredTok := spec.requestedTokens
					if spec.allowPartialFill && k%3 == 0 {
						// Partial capacity bid: offers 55% of requested tokens so Enso splits across 2 leases
						offeredTok = (spec.requestedTokens * 55) / 100
					}
					bidVal := ComputeTokenCostMicroUSD(offeredTok, bidPrice)
					bond := (bidVal*int64(mcfg.MinBondBPS) + 9999) / 10000
					if bond < 1000 {
						bond = 1000
					}

					// In ultra-short TTL regime, some bids expire in-flight before clearing
					ttl := opts.bidTTLSeconds
					if opts.inFlightChurnExpirePct > 0 && rng.Float64() < opts.inFlightChurnExpirePct {
						ttl = 1
					}

					sb, err := SignBid(p.kp.PrivateKey, Bid{
						ProtocolVersion:        ProtocolVersion,
						BidID:                  fmt.Sprintf("bid-%s-%s", orderID, p.id),
						OrderID:                orderID,
						OfferID:                p.activeOfferID,
						ProviderID:             p.id,
						ProviderPubKeyHex:      p.kp.PubKeyHex,
						BidMicroUSDPer1MTokens: bidPrice,
						OfferedTokens:          offeredTok,
						CommittedVRAMGB:        p.hardware.AvailableVRAMGB,
						BondMicroUSD:           bond,
						IssuedAtUnix:           waveNow,
						ExpiresAtUnix:          waveNow + ttl,
						Nonce:                  p.nonce,
					})
					if err != nil {
						return RegimeMetrics{}, SovereigntyMetrics{}, err
					}
					if err := market.SubmitBid(sb, waveNow); err == nil {
						biddersSubmitted++
					}
				}

				clearNow := waveNow
				if opts.inFlightChurnExpirePct > 0 {
					// Advance clock by 2 seconds during gossip propagation so 1s-TTL bids expire in-flight
					clearNow = waveNow + 2
				}
				leases, _, err := market.ClearOrder(orderID, clearNow)
				if err != nil || len(leases) == 0 {
					ordersRejected++
					continue
				}
				ordersCleared++
				leasesAwarded += len(leases)
				if len(leases) > 1 {
					partialFillOrders++
				}

				for _, l := range leases {
					activeWave = append(activeWave, activeExec{
						spec:     spec,
						lease:    l,
						provider: provByID[l.ProviderID],
					})
				}
			}

			// Unified lease execution model shared identically by both primary
			// and failover replacement executions so replacement providers are
			// subject to the exact same adversarial roles, crash probabilities,
			// SLA enforcement, and millisecond latency calculations.
			executeLease := func(l Lease, spec simOrderSpec, p *simProvider, settleTS int64, isFailover bool) (succeeded bool, isAdversarial bool, err error) {
				if p.adversarial {
					if p.advRole == "bait_and_switch" {
						atkInjected["bait_and_switch_non_delivery"]++
					}
					rep := DeliveryReport{
						LeaseID:                     l.LeaseID,
						DeliveredTokens:             0,
						ObservedTTFTMs:              480.0,
						ObservedLatencyMs:           1200.0,
						ObservedTokPerSec:           5.0,
						Completed:                   false,
						FailureReason:               "adversarial_" + p.advRole + "_non_delivery",
						ReportedMicroUSDPer1MTokens: l.AgreedMicroUSDPer1MTokens * 3,
						BaitAndSwitchPriceAttempted: true,
					}
					rcpt, settleErr := market.SettleLease(l.LeaseID, rep, settleTS)
					if settleErr != nil {
						return false, true, settleErr
					}
					totalComputeMicro += rcpt.ProviderPayoutMicroUSD
					totalSlashComp += rcpt.SlashedBondMicroUSD
					totalSettleOverhd += rcpt.VerificationOverheadMicroUSD
					if p.advRole == "bait_and_switch" && rcpt.Status == LeaseSlashedBaitSwitch && rcpt.ProviderPayoutMicroUSD == 0 && rcpt.SlashedBondMicroUSD > 0 {
						atkDetected["bait_and_switch_non_delivery"]++
					}
					return false, true, nil
				}

				crashRate := p.crashProb
				if spec.shockEpoch {
					crashRate *= 1.8
				}
				if rng.Float64() < crashRate {
					partialDeliv := (l.AllocatedTokens * int64(15+rng.Intn(35))) / 100
					crashTTFT := p.baseTTFTMs * 1.4
					crashTokSec := p.baseTokPerSec * 0.4
					rep := DeliveryReport{
						LeaseID:           l.LeaseID,
						DeliveredTokens:   partialDeliv,
						ObservedTTFTMs:    crashTTFT,
						ObservedLatencyMs: computeGenerationLatencyMs(crashTTFT, partialDeliv, crashTokSec),
						ObservedTokPerSec: crashTokSec,
						Completed:         false,
						FailureReason:     "mid_lease_gpu_crash",
					}
					rcpt, settleErr := market.SettleLease(l.LeaseID, rep, settleTS)
					if settleErr != nil {
						return false, false, settleErr
					}
					totalUsefulTokens += rcpt.DeliveredTokens
					orderDelivered[l.OrderID] += rcpt.DeliveredTokens
					totalComputeMicro += rcpt.ProviderPayoutMicroUSD
					totalSlashComp += rcpt.SlashedBondMicroUSD
					totalSettleOverhd += rcpt.VerificationOverheadMicroUSD
					return false, false, nil
				}

				// Normal honest execution (primary or failover replacement)
				var ttft float64
				if isFailover {
					ttft = p.baseTTFTMs + 15.0 + rng.Float64()*15.0
				} else {
					ttft = p.baseTTFTMs*(0.92+rng.Float64()*0.18) + (spec.burstMultiplier-1.0)*18.0
				}
				tokSec := p.baseTokPerSec * (0.94 + rng.Float64()*0.12)
				lat := computeGenerationLatencyMs(ttft, l.AllocatedTokens, tokSec)
				rcpt, settleErr := market.SettleLease(l.LeaseID, DeliveryReport{
					LeaseID:           l.LeaseID,
					DeliveredTokens:   l.AllocatedTokens,
					ObservedTTFTMs:    ttft,
					ObservedLatencyMs: lat,
					ObservedTokPerSec: tokSec,
					Completed:         true,
				}, settleTS)
				if settleErr != nil {
					return false, false, settleErr
				}
				totalUsefulTokens += rcpt.DeliveredTokens
				orderDelivered[l.OrderID] += rcpt.DeliveredTokens
				totalComputeMicro += rcpt.ProviderPayoutMicroUSD
				totalSlashComp += rcpt.SlashedBondMicroUSD
				totalSettleOverhd += rcpt.VerificationOverheadMicroUSD
				clearingPriceSum += float64(l.AgreedMicroUSDPer1MTokens) / 1_000_000.0
				clearingPriceCount++
				ttfts = append(ttfts, ttft)
				lats = append(lats, lat)
				sumTokSec += tokSec
				providerWins[l.ProviderID]++
				return rcpt.DeliveredTokens >= l.AllocatedTokens, false, nil
			}

			// Execute & settle all leases in the wave
			settleNow := waveNow + 4
			for _, ex := range activeWave {
				ok, wasAdv, execErr := executeLease(ex.lease, ex.spec, ex.provider, settleNow, false)
				if execErr != nil {
					return RegimeMetrics{}, SovereigntyMetrics{}, execErr
				}
				if ok {
					continue
				}
				// Automatic failover on crashes, and on adversarial non-delivery
				// when slashing/recovery is active.
				if wasAdv && !opts.enforceSlashing {
					continue
				}
				failoversTriggered++
				foLease, foErr := market.FailoverLease(ex.lease.LeaseID, settleNow)
				if foErr != nil || foLease == nil {
					continue
				}
				leasesAwarded++
				foProv := provByID[foLease.ProviderID]
				if foProv == nil {
					foProv = findProvider(providers, foLease.ProviderID)
				}
				foOK, _, foExecErr := executeLease(*foLease, ex.spec, foProv, settleNow+1, true)
				if foExecErr != nil {
					return RegimeMetrics{}, SovereigntyMetrics{}, foExecErr
				}
				if foOK {
					failoversRecovered++
				}
			}
		}

		// Verify exact ledger conservation at the end of EVERY epoch
		if err := market.CheckConservation(); err != nil {
			return RegimeMetrics{}, SovereigntyMetrics{}, fmt.Errorf("epoch %d conservation failure: %w", epoch, err)
		}
	}

	// Sovereignty export / verify / cold-start fork benchmark on primary regime
	var sov SovereigntyMetrics
	if opts.captureSovereignty {
		var err error
		sov, err = runSovereigntyMigrationCheck(market, providers, cfg)
		if err != nil {
			return RegimeMetrics{}, SovereigntyMetrics{}, err
		}
	}

	// Verify neutral zero-Hanzo-preference parity directly
	parityOK := verifyZeroHanzoParity(cfg.Seed)

	ledgerSnap := market.LedgerSnapshot()
	verifyCostMicro := market.TotalVerificationCostMicroUSD()

	// Realized cost per 1M useful tokens includes raw compute + cryptographic verification + settlement overhead
	totalRealizedMicro := totalComputeMicro + verifyCostMicro + totalSettleOverhd
	netAfterSlashMicro := totalRealizedMicro - totalSlashComp
	if netAfterSlashMicro < 0 {
		netAfterSlashMicro = 0
	}

	realizedPer1M := 0.0
	netAfterSlashPer1M := 0.0
	if totalUsefulTokens > 0 {
		realizedPer1M = float64(totalRealizedMicro) / float64(totalUsefulTokens)
		netAfterSlashPer1M = float64(netAfterSlashMicro) / float64(totalUsefulTokens)
	}
	savingsPct := 0.0
	if baselineCostPer1M > 0 && realizedPer1M > 0 {
		savingsPct = ((baselineCostPer1M - realizedPer1M) / baselineCostPer1M) * 100.0
	}

	// Attack breakdown & overall detection rate
	breakdown := make([]AttackVectorMetrics, 0, len(attackOrder))
	totalAtkIn := 0
	totalAtkDet := 0
	for _, a := range attackOrder {
		inj := atkInjected[a.name]
		det := atkDetected[a.name]
		totalAtkIn += inj
		totalAtkDet += det
		breakdown = append(breakdown, AttackVectorMetrics{
			AttackType:        a.name,
			VerificationClass: a.class,
			Injected:          inj,
			Detected:          det,
			DetectionRatePct:  round2(pct(det, inj)),
		})
	}

	// Compute provider share HHI (Herfindahl-Hirschman Index: lower = more decentralized)
	hhi := 0.0
	var totalWins int64
	for _, w := range providerWins {
		totalWins += w
	}
	if totalWins > 0 {
		for _, w := range providerWins {
			share := float64(w) / float64(totalWins)
			hhi += share * share
		}
	}

	// Calculate order completion strictly from actual delivered tokens per order,
	// separate from auction clearing.
	ordersCompleted := 0
	for orderID, req := range orderRequested {
		if req > 0 && orderDelivered[orderID] >= req {
			ordersCompleted++
		}
	}

	meanTokSec := 0.0
	if len(ttfts) > 0 {
		meanTokSec = sumTokSec / float64(len(ttfts))
	}
	avgClearing := 0.0
	if clearingPriceCount > 0 {
		avgClearing = clearingPriceSum / float64(clearingPriceCount)
	}
	compWithoutHanzo := 0.0
	if market.HanzoCalls() == 0 && ordersCompleted > 0 {
		compWithoutHanzo = 100.0
	}

	metrics := RegimeMetrics{
		Name:                        opts.name,
		Description:                 opts.description,
		IsNegative:                  opts.isNegative,
		ProvidersTotal:              len(providers),
		OrdersSubmitted:             len(orders),
		OrdersCleared:               ordersCleared,
		OrdersCompleted:             ordersCompleted,
		OrdersRejected:              ordersRejected,
		LeasesAwarded:               leasesAwarded,
		FailoversTriggered:          failoversTriggered,
		FailoversRecovered:          failoversRecovered,
		RequestedTokens:             totalReqTokens,
		UsefulTokens:                totalUsefulTokens,
		PartialFillOrders:           partialFillOrders,
		SignatureVerifyOps:          market.SignatureVerifications(),
		OfferChurnResignOps:         churnResigns,
		GrossComputeMicroUSD:        totalComputeMicro,
		VerificationOverheadMicroUS: verifyCostMicro,
		SettlementOverheadMicroUSD:  totalSettleOverhd,
		BuyerSlashedCompMicroUSD:    totalSlashComp,
		RealizedCostPer1MUsefulUSD:  round4(realizedPer1M),
		NetAfterSlashPer1MUsefulUSD: round4(netAfterSlashPer1M),
		SavingsVsBaselinePct:        round2(savingsPct),
		LedgerDriftMicroUSD:         ledgerSnap.ConservationDrift,
		TTFTp50Ms:                   round2(percentile(ttfts, 0.50)),
		TTFTp95Ms:                   round2(percentile(ttfts, 0.95)),
		TTFTp99Ms:                   round2(percentile(ttfts, 0.99)),
		LatencyP50Ms:                round2(percentile(lats, 0.50)),
		LatencyP95Ms:                round2(percentile(lats, 0.95)),
		LatencyP99Ms:                round2(percentile(lats, 0.99)),
		MeanThroughputTokSec:        round2(meanTokSec),
		ClearRatePct:                round2(pct(ordersCleared, len(orders))),
		CompletionRatePct:           round2(pct(ordersCompleted, len(orders))),
		TokenFulfillRatePct:         round2(pctInt64(totalUsefulTokens, totalReqTokens)),
		AdmissionRejectPct:          round2(pct(ordersRejected, len(orders))),
		FailoverRecoveryPct:         round2(pct(failoversRecovered, failoversTriggered)),
		AverageClearingPriceM:       round4(avgClearing),
		AdversarialTotalInjected:    totalAtkIn,
		AdversarialTotalDetected:    totalAtkDet,
		AdversarialDetectionPct:     round2(pct(totalAtkDet, totalAtkIn)),
		HardwareAttestedProviders:   0,
		HardwareAttestationClaimed:  false,
		AttackBreakdown:             breakdown,
		HanzoCalls:                  market.HanzoCalls(),
		HanzoControlPlaneDisabled:   opts.hanzoKillSwitch,
		CompletionWithoutHanzoPct:   compWithoutHanzo,
		ActiveWinningProviders:      len(providerWins),
		ProviderShareHHI:            round4(hhi),
		ZeroHanzoPreferenceVerified: parityOK,
	}

	return metrics, sov, nil
}

func runSovereigntyMigrationCheck(market *Market, providers []*simProvider, cfg SimConfig) (SovereigntyMetrics, error) {
	exporterKP, err := DeriveKeyPair("sovereignty-exporter", uint64(cfg.Seed), 1)
	if err != nil {
		return SovereigntyMetrics{}, err
	}
	migNow := cfg.BaseTimeUnix + int64(cfg.Epochs-1)*3600 + 60

	t0 := time.Now()
	bundle, err := market.ExportBundle(exporterKP.PrivateKey, migNow)
	if err != nil {
		return SovereigntyMetrics{}, err
	}
	rawBytes, err := json.Marshal(bundle)
	if err != nil {
		return SovereigntyMetrics{}, err
	}
	exportMs := float64(time.Since(t0).Microseconds()) / 1000.0

	t1 := time.Now()
	var decoded MarketExportBundle
	if err := json.Unmarshal(rawBytes, &decoded); err != nil {
		return SovereigntyMetrics{}, err
	}
	if err := VerifyExportBundle(decoded); err != nil {
		return SovereigntyMetrics{}, err
	}
	verifyMs := float64(time.Since(t1).Microseconds()) / 1000.0

	t2 := time.Now()
	forked, err := ImportBundle(decoded, DefaultConfig(), migNow)
	if err != nil {
		return SovereigntyMetrics{}, err
	}
	importMs := float64(time.Since(t2).Microseconds()) / 1000.0

	// Prove the forked market can clear and settle a brand-new order completely offline
	var healthyProv *simProvider
	for _, p := range providers {
		if !p.offline && !p.adversarial && p.activeOfferID != "" && p.basePrices["llama-3.3-70b"] > 0 {
			healthyProv = p
			break
		}
	}
	postForkOK := false
	if healthyProv != nil {
		ord := Order{
			OrderID:                "post-fork-verify-order",
			BuyerID:                "buyer-fleet",
			Model:                  "llama-3.3-70b",
			RequestedTokens:        250_000,
			MinVRAMGB:              80,
			MaxMicroUSDPer1MTokens: 900_000,
			EscrowMicroUSD:         ComputeTokenCostMicroUSD(250_000, 900_000),
			CreatedAtUnix:          migNow,
			ExpiresAtUnix:          migNow + 300,
			RankingWeights:         DefaultRankingWeights(),
		}
		if err := forked.SubmitOrder(ord, migNow); err == nil {
			healthyProv.nonce++
			sb, _ := SignBid(healthyProv.kp.PrivateKey, Bid{
				ProtocolVersion:        ProtocolVersion,
				BidID:                  "post-fork-verify-bid",
				OrderID:                ord.OrderID,
				OfferID:                healthyProv.activeOfferID,
				ProviderID:             healthyProv.id,
				ProviderPubKeyHex:      healthyProv.kp.PubKeyHex,
				BidMicroUSDPer1MTokens: 450_000,
				OfferedTokens:          250_000,
				CommittedVRAMGB:        80,
				BondMicroUSD:           60_000,
				IssuedAtUnix:           migNow,
				ExpiresAtUnix:          migNow + 300,
				Nonce:                  healthyProv.nonce,
			})
			if err := forked.SubmitBid(sb, migNow); err == nil {
				if leases, _, err := forked.ClearOrder(ord.OrderID, migNow); err == nil && len(leases) == 1 {
					if rcpt, err := forked.SettleLease(leases[0].LeaseID, DeliveryReport{
						LeaseID:           leases[0].LeaseID,
						DeliveredTokens:   250_000,
						ObservedTTFTMs:    healthyProv.baseTTFTMs,
						ObservedLatencyMs: computeGenerationLatencyMs(healthyProv.baseTTFTMs, 250_000, healthyProv.baseTokPerSec),
						ObservedTokPerSec: healthyProv.baseTokPerSec,
						Completed:         true,
					}, migNow+5); err == nil && rcpt.Status == LeaseCompleted {
						postForkOK = forked.CheckConservation() == nil
					}
				}
			}
		}
	}

	forkSnap := forked.LedgerSnapshot()
	return SovereigntyMetrics{
		BundleBytes:          len(rawBytes),
		ExportedOffers:       len(bundle.Gossip.ActiveOffers),
		ExportedLeases:       len(bundle.Leases),
		ExportedReceipts:     len(bundle.Receipts),
		ExportedPeers:        len(bundle.Gossip.Peers),
		ExportLatencyMs:      round2(exportMs),
		VerifyLatencyMs:      round2(verifyMs),
		ImportLatencyMs:      round2(importMs),
		TotalMigrationMs:     round2(exportMs + verifyMs + importMs),
		ForkLedgerDriftMicro: forkSnap.ConservationDrift,
		PostForkClearOK:      postForkOK,
		PostForkHanzoCalls:   forked.HanzoCalls(),
		SignatureVerified:    true,
	}, nil
}

func verifyZeroHanzoParity(seed int64) bool {
	kpA, err := DeriveKeyPair("parity-a", uint64(seed), 1)
	if err != nil {
		return false
	}
	kpB, err := DeriveKeyPair("parity-b", uint64(seed), 2)
	if err != nil {
		return false
	}
	now := int64(1760000000)
	mkCand := func(kp *KeyPair, provID, bidID, opClass string) Candidate {
		offer, _ := SignOffer(kp.PrivateKey, Offer{
			ProtocolVersion:   ProtocolVersion,
			OfferID:           "offer-" + provID,
			ProviderID:        provID,
			ProviderPubKeyHex: kp.PubKeyHex,
			OperatorClass:     opClass,
			Hardware:          GPUHardwareSpec{GPUModel: "H100-SXM5-80GB", GPUCount: 1, AvailableVRAMGB: 80, TotalVRAMGB: 80},
			SupportedModels:   []string{"llama-3.3-70b"},
			Location:          LocationSpec{Region: "eu-central", Jurisdiction: "DE", DatacenterTier: 3},
			Pricing:           PricingSpec{MicroUSDPer1MTokens: 500_000, MicroUSDPerGPUHour: 2_000_000},
			SLO:               ServiceCommitment{TargetTTFTMs: 90, MinThroughputTokPerSec: 110, UptimeBPS: 9950, SlashBPSOnBreach: 5000},
			AvailableSlots:    4,
			IssuedAtUnix:      now,
			ExpiresAtUnix:     now + 3600,
			Nonce:             1,
			AttestationClass:  VerificationCryptoSignature,
		})
		bid, _ := SignBid(kp.PrivateKey, Bid{
			ProtocolVersion:        ProtocolVersion,
			BidID:                  bidID,
			OrderID:                "parity-order",
			OfferID:                offer.Offer.OfferID,
			ProviderID:             provID,
			ProviderPubKeyHex:      kp.PubKeyHex,
			BidMicroUSDPer1MTokens: 480_000,
			OfferedTokens:          500_000,
			CommittedVRAMGB:        80,
			BondMicroUSD:           100_000,
			IssuedAtUnix:           now,
			ExpiresAtUnix:          now + 600,
			Nonce:                  1,
		})
		return Candidate{
			SignedOffer: offer,
			SignedBid:   bid,
			Telemetry: ProviderTelemetry{
				ProviderID:          provID,
				MeasuredTTFTMs:      92.5,
				ReliabilityBPS:      9750,
				ObservedSampleCount: 25,
			},
			FreeSlots: 4,
		}
	}

	ord := Order{
		OrderID:                "parity-order",
		BuyerID:                "buyer",
		Model:                  "llama-3.3-70b",
		RequestedTokens:        500_000,
		MinVRAMGB:              80,
		MaxMicroUSDPer1MTokens: 700_000,
		CreatedAtUnix:          now,
		ExpiresAtUnix:          now + 600,
		RankingWeights:         DefaultRankingWeights(),
	}

	r1, _ := RankCandidates(ord, []Candidate{
		mkCand(kpA, "prov-a", "bid-01", "hanzo"),
		mkCand(kpB, "prov-b", "bid-02", "independent"),
	}, now, true)
	r2, _ := RankCandidates(ord, []Candidate{
		mkCand(kpA, "prov-a", "bid-01", "independent"),
		mkCand(kpB, "prov-b", "bid-02", "hanzo"),
	}, now, true)

	if len(r1) != 2 || len(r2) != 2 {
		return false
	}
	return r1[0].CompositeScore == r1[1].CompositeScore &&
		r2[0].CompositeScore == r2[1].CompositeScore &&
		r1[0].CompositeScore == r2[0].CompositeScore &&
		r1[0].Candidate.SignedBid.Bid.BidID == "bid-01" &&
		r2[0].Candidate.SignedBid.Bid.BidID == "bid-01"
}

func findProvider(providers []*simProvider, id string) *simProvider {
	for _, p := range providers {
		if p.id == id {
			return p
		}
	}
	return providers[0]
}

func percentile(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	cp := append([]float64(nil), vals...)
	sort.Float64s(cp)
	idx := int(math.Ceil(p*float64(len(cp)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return cp[idx]
}

func pct(num, den int) float64 {
	if den <= 0 {
		return 0
	}
	return (float64(num) / float64(den)) * 100.0
}

func pctInt64(num, den int64) float64 {
	if den <= 0 {
		return 0
	}
	return (float64(num) / float64(den)) * 100.0
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

func round4(v float64) float64 {
	return math.Round(v*10000) / 10000
}
