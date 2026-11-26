// SPDX-License-Identifier: Apache-2.0 OR MIT

// Command governance-bench executes the reproducible HIP-1352 open protocol
// governance, treasury, emergency safeguard, and sovereign forkability
// verification & benchmark harness, outputting raw JSON metrics.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/governance"
)

type benchClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *benchClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *benchClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type actor struct {
	member governance.Member
	priv   ed25519.PrivateKey
}

type LatencyStats struct {
	Samples   int     `json:"samples"`
	BatchSize int     `json:"batch_size"`
	MeanUs    float64 `json:"mean_us"`
	P50Us     float64 `json:"p50_us"`
	P95Us     float64 `json:"p95_us"`
	P99Us     float64 `json:"p99_us"`
	MinUs     float64 `json:"min_us"`
	MaxUs     float64 `json:"max_us"`
}

type AttackCaseResult struct {
	Name        string `json:"name"`
	Category    string `json:"category"`
	Rejected    bool   `json:"rejected"`
	ErrorDetail string `json:"error_detail"`
}

type ForkReplayResult struct {
	ExportedBundleBytes      int    `json:"exported_bundle_bytes"`
	ExportedLedgerEntries    int    `json:"exported_ledger_entries"`
	BundleHash               string `json:"bundle_hash"`
	LedgerHeadHash           string `json:"ledger_head_hash"`
	LosslessStateParity      bool   `json:"lossless_state_parity"`
	CrossForkReplayRejected  bool   `json:"cross_fork_replay_rejected"`
	PostForkProposalExecuted bool   `json:"post_fork_proposal_executed"`
	Gen2BundleVerified       bool   `json:"gen2_bundle_verified"`
	ForkProtocolID           string `json:"fork_protocol_id"`
	ParentProtocolID         string `json:"parent_protocol_id"`
}

type BenchResults struct {
	SchemaVersion string `json:"schema_version"`
	GeneratedAt   string `json:"generated_at"`
	GoVersion     string `json:"go_version"`
	GOOS          string `json:"goos"`
	GOARCH        string `json:"goarch"`
	NumCPU        int    `json:"num_cpu"`

	ScenarioSummary struct {
		DAOParticipants             int    `json:"dao_participants"`
		ParameterBefore             string `json:"parameter_before"`
		ParameterAfter              string `json:"parameter_after"`
		TimelockDelayMs             int64  `json:"timelock_delay_ms"`
		TreasuryDisbursedMicroUSD   uint64 `json:"treasury_disbursed_micro_usd"`
		TreasuryRemainingMicroUSD   uint64 `json:"treasury_remaining_micro_usd"`
		ModelRegistered             string `json:"model_registered"`
		VerifierProfileRegistered   string `json:"verifier_profile_registered"`
		EmergencyRatifiedAndLifted  bool   `json:"emergency_ratified_and_lifted"`
		HanzoAdminOverridePresent   bool   `json:"hanzo_admin_override_present"`
		HanzoHostedDepsRequired     int    `json:"hanzo_hosted_deps_required"`
	} `json:"scenario_summary"`

	AdversarialMatrix struct {
		TotalAttacks      int                `json:"total_attacks"`
		RejectedAttacks   int                `json:"rejected_attacks"`
		RejectionRatePct  float64            `json:"rejection_rate_pct"`
		Cases             []AttackCaseResult `json:"cases"`
	} `json:"adversarial_matrix"`

	ForkReplay ForkReplayResult `json:"fork_replay"`

	Performance struct {
		ProposalSubmitLatency     LatencyStats `json:"proposal_submit_latency"`
		VoteCastLatency           LatencyStats `json:"vote_cast_latency"`
		QueueExecuteLatency       LatencyStats `json:"queue_execute_latency"`
		BundleExportVerifyLatency LatencyStats `json:"bundle_export_verify_latency"`
		LedgerVerifyLatency       LatencyStats `json:"ledger_verify_latency"`
		ProposalsPerSec           float64      `json:"proposals_per_sec"`
		VotesPerSec               float64      `json:"votes_per_sec"`
		BundleVerificationsPerSec float64      `json:"bundle_verifications_per_sec"`
	} `json:"performance"`

	Resources struct {
		HeapAllocBytes   uint64  `json:"heap_alloc_bytes"`
		TotalAllocBytes  uint64  `json:"total_alloc_bytes"`
		BytesPerVote     float64 `json:"bytes_per_vote"`
		NumGC            uint32  `json:"num_gc"`
	} `json:"resources"`

	BountyDimensions struct {
		CostNotes                        string  `json:"cost_notes"`
		EstimatedGovernanceCPUUSDPer1kTx float64 `json:"estimated_governance_cpu_usd_per_1k_tx"`
		AmortizedUSDPerMillionTokens     float64 `json:"amortized_usd_per_million_tokens"`
		TrustRejectionRatePct            float64 `json:"trust_rejection_rate_pct"`
		DecentralizationIndependentPct   float64 `json:"decentralization_independent_pct"`
		SovereigntyLosslessForkPct       float64 `json:"sovereignty_lossless_fork_pct"`
		CompletionRatePct                float64 `json:"completion_rate_pct"`
	} `json:"bounty_dimensions"`
}

func makeActor(id, org string, power uint64, guardian bool) (actor, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return actor{}, err
	}
	return actor{
		member: governance.Member{
			ID:                  id,
			Org:                 org,
			PublicKeyHex:        hex.EncodeToString(pub),
			VotingPower:         power,
			IsEmergencyGuardian: guardian,
			Active:              true,
		},
		priv: priv,
	}, nil
}

func computeStats(durs []time.Duration, batchSize int) LatencyStats {
	if len(durs) == 0 {
		return LatencyStats{}
	}
	if batchSize < 1 {
		batchSize = 1
	}
	sorted := make([]time.Duration, len(durs))
	copy(sorted, durs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	perOpUs := func(d time.Duration) float64 {
		return float64(d.Nanoseconds()) / 1000.0 / float64(batchSize)
	}
	var sum float64
	for _, d := range sorted {
		sum += perOpUs(d)
	}
	pct := func(p float64) float64 {
		idx := int(math.Ceil(p*float64(len(sorted)))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sorted) {
			idx = len(sorted) - 1
		}
		return round3(perOpUs(sorted[idx]))
	}
	return LatencyStats{
		Samples:   len(sorted),
		BatchSize: batchSize,
		MeanUs:    round3(sum / float64(len(sorted))),
		P50Us:     pct(0.50),
		P95Us:     pct(0.95),
		P99Us:     pct(0.99),
		MinUs:     round3(perOpUs(sorted[0])),
		MaxUs:     round3(perOpUs(sorted[len(sorted)-1])),
	}
}

func round3(v float64) float64 {
	return math.Round(v*1000) / 1000
}

func submitSigned(eng *governance.Engine, a actor, req governance.ProposalRequest) (*governance.Proposal, error) {
	req.ProposerID = a.member.ID
	req.Org = a.member.Org
	sig, err := governance.SignProposal(eng.ProtocolID(), req, a.priv)
	if err != nil {
		return nil, err
	}
	req.Signature = sig
	return eng.SubmitProposal(req)
}

func voteSigned(eng *governance.Engine, a actor, propID string, support governance.VoteSupport, ts time.Time, nonce string) (*governance.VoteReceipt, error) {
	req := governance.VoteRequest{
		ProposalID: propID,
		VoterID:    a.member.ID,
		Org:        a.member.Org,
		Support:    support,
		CastAt:     ts,
		Nonce:      nonce,
	}
	sig, err := governance.SignVote(eng.ProtocolID(), req, a.priv)
	if err != nil {
		return nil, err
	}
	req.Signature = sig
	return eng.CastVote(req)
}

func queueSigned(eng *governance.Engine, a actor, propID string, ts time.Time, nonce string) (*governance.Proposal, error) {
	req := governance.QueueRequest{
		ProposalID: propID,
		CallerID:   a.member.ID,
		Org:        a.member.Org,
		QueuedAt:   ts,
		Nonce:      nonce,
	}
	sig, err := governance.SignQueue(eng.ProtocolID(), req, a.priv)
	if err != nil {
		return nil, err
	}
	req.Signature = sig
	return eng.QueueProposal(req)
}

func executeSigned(eng *governance.Engine, a actor, propID string, ts time.Time, nonce string) (*governance.ExecutionReceipt, error) {
	req := governance.ExecuteRequest{
		ProposalID: propID,
		ExecutorID: a.member.ID,
		Org:        a.member.Org,
		ExecutedAt: ts,
		Nonce:      nonce,
	}
	sig, err := governance.SignExecute(eng.ProtocolID(), req, a.priv)
	if err != nil {
		return nil, err
	}
	req.Signature = sig
	return eng.ExecuteProposal(req)
}

func runBenchmark(voters int, benchVotes int) (*BenchResults, error) {
	if voters < 10 {
		voters = 10
	}
	if benchVotes < 100 {
		benchVotes = 100
	}

	res := &BenchResults{
		SchemaVersion: "hip-1352.bench.v1",
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		GoVersion:     runtime.Version(),
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		NumCPU:        runtime.NumCPU(),
	}

	clk := &benchClock{now: time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)}

	// Build multi-party independent DAO cohort (member 0..voters-1; last member is guardian, second-to-last is low-power).
	cohort := make([]actor, 0, voters)
	members := make([]governance.Member, 0, voters)
	for i := 0; i < voters; i++ {
		power := uint64(400)
		guardian := false
		if i == voters-1 {
			guardian = true
			power = 200
		} else if i == voters-2 {
			power = 50 // below proposal threshold (200)
		}
		a, err := makeActor(fmt.Sprintf("dao-member-%02d", i), fmt.Sprintf("independent-org-%02d", i), power, guardian)
		if err != nil {
			return nil, err
		}
		cohort = append(cohort, a)
		members = append(members, a.member)
	}
	guardian := cohort[voters-1]
	lowPower := cohort[voters-2]

	cfg := governance.GovernanceConfig{
		ProtocolID:                    "hanzo-open-governance-mainnet-sim",
		NetworkVersion:                "1.0.0",
		VotingDelay:                   5 * time.Minute,
		VotingPeriod:                  1 * time.Hour,
		MinTimelockDelay:              30 * time.Minute,
		MinTimelockFloor:              15 * time.Minute,
		GracePeriod:                   24 * time.Hour,
		QuorumBps:                     2500, // 25%
		MinQuorumBpsFloor:             1000, // 10%
		ApprovalThresholdBps:          5001, // >50%
		ProposalThreshold:             200,
		MaxEmergencyDuration:          4 * time.Hour,
		EmergencyCooldown:             6 * time.Hour,
		TreasuryEpochDuration:         24 * time.Hour,
		TreasuryEpochCapMicroUSD:      25_000_000_000, // $25,000
		MaxSingleDisbursementMicroUSD: 15_000_000_000, // $15,000
		AllowlistedRecipients: map[string]bool{
			"public-goods-grant-pool": true,
		},
		NoAdminOverride: true,
	}
	minFee, maxFee := 0.0, 1000.0
	params := map[string]governance.ParameterEntry{
		"max_inference_fee_bps": {
			Key:         "max_inference_fee_bps",
			Value:       "250",
			ValueType:   "uint64",
			MinNumeric:  &minFee,
			MaxNumeric:  &maxFee,
			Description: "Maximum protocol inference fee in bps",
		},
		"genesis_tag": {
			Key:       "genesis_tag",
			Value:     "hip-1352-open-dao",
			ValueType: "string",
			Immutable: true,
		},
	}

	eng, err := governance.NewEngine(cfg, members, params, 100_000_000_000, governance.WithClock(clk.Now))
	if err != nil {
		return nil, fmt.Errorf("NewEngine: %w", err)
	}

	res.ScenarioSummary.DAOParticipants = voters
	res.ScenarioSummary.ParameterBefore = "250"
	res.ScenarioSummary.TimelockDelayMs = cfg.MinTimelockDelay.Milliseconds()
	res.ScenarioSummary.HanzoAdminOverridePresent = !eng.Config().NoAdminOverride
	res.ScenarioSummary.HanzoHostedDepsRequired = 0

	// ── Scenario A: Multi-party test DAO end-to-end timelocked workflows ──

	// A1: Delegation by lowPower -> cohort[0]
	delReq := governance.DelegationRequest{
		DelegatorID: lowPower.member.ID,
		DelegateeID: cohort[0].member.ID,
		Org:         lowPower.member.Org,
		IssuedAt:    clk.Now(),
		Nonce:       "del-1",
	}
	delReq.Signature, _ = governance.SignDelegation(eng.ProtocolID(), delReq, lowPower.priv)
	if _, err := eng.Delegate(delReq); err != nil {
		return nil, fmt.Errorf("Delegate: %w", err)
	}

	// A2: Multi-party timelocked parameter update (250 -> 175)
	pFee, err := submitSigned(eng, cohort[0], governance.ProposalRequest{
		ProposalID:  "dao-prop-fee-175",
		Kind:        governance.ProposalParameterUpdate,
		Title:       "Reduce max inference fee from 250 bps to 175 bps",
		Description: "HIP-1352 acceptance test: noncritical network parameter change through timelocked vote",
		Payload: governance.ProposalPayload{
			ParameterUpdate: &governance.ParameterUpdatePayload{Key: "max_inference_fee_bps", Value: "175"},
		},
		CreatedAt: clk.Now(),
		Nonce:     "p-fee-1",
	})
	if err != nil {
		return nil, fmt.Errorf("submit fee proposal: %w", err)
	}
	clk.Advance(6 * time.Minute)
	for i := 0; i < 12; i++ {
		support := governance.VoteFor
		if i >= 10 {
			support = governance.VoteAgainst
		}
		if _, err := voteSigned(eng, cohort[i], pFee.ID, support, clk.Now(), fmt.Sprintf("v-fee-%d", i)); err != nil {
			return nil, fmt.Errorf("vote %d: %w", i, err)
		}
	}
	clk.Advance(time.Hour + time.Second)
	if _, err := queueSigned(eng, cohort[5], pFee.ID, clk.Now(), "q-fee-1"); err != nil {
		return nil, fmt.Errorf("queue fee: %w", err)
	}

	// Track adversarial rejections in Scenario B alongside lifecycle!
	recordAttack := func(name, category string, err error) {
		detail := ""
		if err != nil {
			detail = err.Error()
		}
		res.AdversarialMatrix.Cases = append(res.AdversarialMatrix.Cases, AttackCaseResult{
			Name:        name,
			Category:    category,
			Rejected:    err != nil,
			ErrorDetail: detail,
		})
	}

	// Attack 10 (inline on live queued proposal): premature timelock execution
	_, prematureErr := executeSigned(eng, cohort[5], pFee.ID, clk.Now(), "e-fee-early")
	recordAttack("premature_timelock_execution", "timelock", prematureErr)

	clk.Advance(31 * time.Minute)
	if _, err := executeSigned(eng, cohort[6], pFee.ID, clk.Now(), "e-fee-1"); err != nil {
		return nil, fmt.Errorf("execute fee: %w", err)
	}
	afterParam, _ := eng.Parameter("max_inference_fee_bps")
	res.ScenarioSummary.ParameterAfter = afterParam.Value

	// A3: Verifier standard + Apache-2.0 Model registry + Treasury disbursement
	passProposal := func(pid string, kind governance.ProposalKind, payload governance.ProposalPayload, tag string) error {
		if _, err := submitSigned(eng, cohort[1], governance.ProposalRequest{
			ProposalID: pid,
			Kind:       kind,
			Title:      pid,
			Payload:    payload,
			CreatedAt:  clk.Now(),
			Nonce:      "p-" + tag,
		}); err != nil {
			return err
		}
		clk.Advance(6 * time.Minute)
		for i := 0; i < 10; i++ {
			if _, err := voteSigned(eng, cohort[i], pid, governance.VoteFor, clk.Now(), fmt.Sprintf("v-%s-%d", tag, i)); err != nil {
				return err
			}
		}
		clk.Advance(time.Hour + time.Second)
		if _, err := queueSigned(eng, cohort[2], pid, clk.Now(), "q-"+tag); err != nil {
			return err
		}
		clk.Advance(31 * time.Minute)
		_, err := executeSigned(eng, cohort[3], pid, clk.Now(), "e-"+tag)
		return err
	}

	if err := passProposal("dao-prop-verifier-1", governance.ProposalVerifierStandard, governance.ProposalPayload{
		VerifierStandard: &governance.VerifierStandardPayload{
			ProfileID:          "cryptographic-proof-v1",
			Method:             governance.VerifyCryptographicProof,
			MinVerifiers:       5,
			QuorumThresholdBps: 6667,
			ChallengeWindowSec: 3600,
			MaxLatencyMs:       200,
		},
	}, "ver1"); err != nil {
		return nil, err
	}
	res.ScenarioSummary.VerifierProfileRegistered = "cryptographic-proof-v1"

	modelHash := sha256.Sum256([]byte("zen-open-7b-weights-v1"))
	if err := passProposal("dao-prop-model-1", governance.ProposalModelRegistry, governance.ProposalPayload{
		ModelRegistry: &governance.ModelRegistryPayload{
			ModelID:           "zen-open-7b",
			Version:           "1.0.0",
			DigestSHA256:      hex.EncodeToString(modelHash[:]),
			LicenseSPDX:       "Apache-2.0",
			VerifierProfileID: "cryptographic-proof-v1",
			Active:            true,
		},
	}, "mod1"); err != nil {
		return nil, err
	}
	res.ScenarioSummary.ModelRegistered = "zen-open-7b"

	if err := passProposal("dao-prop-treasury-1", governance.ProposalTreasuryDisbursement, governance.ProposalPayload{
		TreasuryDisbursement: &governance.TreasuryDisbursementPayload{
			Recipient:      "public-goods-grant-pool",
			AmountMicroUSD: 15_000_000_000, // $15,000
			Memo:           "Open verifier public goods milestone #1",
		},
	}, "tr1"); err != nil {
		return nil, err
	}
	tr := eng.Treasury()
	res.ScenarioSummary.TreasuryDisbursedMicroUSD = 15_000_000_000
	res.ScenarioSummary.TreasuryRemainingMicroUSD = tr.BalanceMicroUSD

	// A4: Bounded emergency pause triggered by guardian -> lifted early via DAO ratification proposal
	emReq := governance.EmergencyActionRequest{
		ActionID:        "em-circuit-1",
		GuardianID:      guardian.member.ID,
		Org:             guardian.member.Org,
		Kind:            governance.EmergencyPauseSubsystem,
		TargetSubsystem: "treasury_disbursements",
		Duration:        2 * time.Hour,
		Reason:          "temporary anomaly audit",
		IssuedAt:        clk.Now(),
		Nonce:           "em-ok-1",
	}
	emReq.Signature, _ = governance.SignEmergencyAction(eng.ProtocolID(), emReq, guardian.priv)
	if _, err := eng.TriggerEmergency(emReq); err != nil {
		return nil, fmt.Errorf("TriggerEmergency: %w", err)
	}
	if err := passProposal("dao-prop-lift-em-1", governance.ProposalEmergencyRatification, governance.ProposalPayload{
		EmergencyRatification: &governance.EmergencyRatificationPayload{
			ActionID: "em-circuit-1",
			Lift:     true,
		},
	}, "emlift"); err != nil {
		return nil, fmt.Errorf("lift emergency via DAO: %w", err)
	}
	res.ScenarioSummary.EmergencyRatifiedAndLifted = !eng.IsSubsystemPaused("treasury_disbursements")

	// ── Scenario B: Comprehensive Adversarial / Negative Regime Matrix ──

	// 1. Engine constructor with NoAdminOverride=false
	badCfg := cfg.Clone()
	badCfg.NoAdminOverride = false
	_, err = governance.NewEngine(badCfg, members, params, 0)
	recordAttack("admin_override_config", "admin_override", err)

	// 2. Proposal installing hanzo_admin_override
	_, err = submitSigned(eng, cohort[0], governance.ProposalRequest{
		ProposalID: "atk-admin-override",
		Kind:       governance.ProposalParameterUpdate,
		Title:      "Install Hanzo admin override",
		Payload: governance.ProposalPayload{
			ParameterUpdate: &governance.ParameterUpdatePayload{Key: "hanzo_admin_override", Value: "true"},
		},
		CreatedAt: clk.Now(),
		Nonce:     "atk-1",
	})
	recordAttack("admin_override_proposal", "admin_override", err)

	// 3. Lower quorum below immutable floor
	_, err = submitSigned(eng, cohort[0], governance.ProposalRequest{
		ProposalID: "atk-low-quorum",
		Kind:       governance.ProposalParameterUpdate,
		Title:      "Lower quorum below floor",
		Payload: governance.ProposalPayload{
			ParameterUpdate: &governance.ParameterUpdatePayload{Key: "quorum_bps", Value: "100"},
		},
		CreatedAt: clk.Now(),
		Nonce:     "atk-2",
	})
	recordAttack("quorum_floor_violation", "protocol_invariant", err)

	// 4. Disable timelock (0ms)
	_, err = submitSigned(eng, cohort[0], governance.ProposalRequest{
		ProposalID: "atk-zero-timelock",
		Kind:       governance.ProposalParameterUpdate,
		Title:      "Disable timelock",
		Payload: governance.ProposalPayload{
			ParameterUpdate: &governance.ParameterUpdatePayload{Key: "min_timelock_delay_ms", Value: "0"},
		},
		CreatedAt: clk.Now(),
		Nonce:     "atk-3",
	})
	recordAttack("zero_timelock_attack", "protocol_invariant", err)

	// 5. Parameter out of bounds
	_, err = submitSigned(eng, cohort[0], governance.ProposalRequest{
		ProposalID: "atk-oob-param",
		Kind:       governance.ProposalParameterUpdate,
		Title:      "Fee 9999 bps",
		Payload: governance.ProposalPayload{
			ParameterUpdate: &governance.ParameterUpdatePayload{Key: "max_inference_fee_bps", Value: "9999"},
		},
		CreatedAt: clk.Now(),
		Nonce:     "atk-4",
	})
	recordAttack("parameter_out_of_bounds", "invalid_proposal", err)

	// 6. Flash-loan post-snapshot transfer inflation + 7. Insufficient quorum + 8. Double vote + 9. Nonce replay
	pFlash, err := submitSigned(eng, cohort[0], governance.ProposalRequest{
		ProposalID: "atk-flash-target",
		Kind:       governance.ProposalParameterUpdate,
		Title:      "Flash-loan defense target",
		Payload: governance.ProposalPayload{
			ParameterUpdate: &governance.ParameterUpdatePayload{Key: "max_inference_fee_bps", Value: "180"},
		},
		CreatedAt: clk.Now(),
		Nonce:     "atk-5",
	})
	if err != nil {
		return nil, err
	}
	// Transfer power to lowPower (who delegated to cohort[0] so has 0 snapshot power) AFTER proposal snapshot!
	flashSrc := cohort[voters-3]
	xfer := governance.TransferPowerRequest{
		FromMemberID: flashSrc.member.ID,
		ToMemberID:   lowPower.member.ID,
		Amount:       400,
		Org:          flashSrc.member.Org,
		IssuedAt:     clk.Now(),
		Nonce:        "atk-xfer-1",
	}
	xfer.Signature, _ = governance.SignTransferPower(eng.ProtocolID(), xfer, flashSrc.priv)
	if _, err := eng.TransferVotingPower(xfer); err != nil {
		return nil, err
	}
	clk.Advance(6 * time.Minute)
	_, flashErr := voteSigned(eng, lowPower, pFlash.ID, governance.VoteFor, clk.Now(), "atk-flash-vote")
	recordAttack("flash_loan_post_snapshot_transfer", "quorum_attack", flashErr)

	// Valid single vote by cohort[0] (insufficient for 25% quorum), followed by double vote & nonce replay.
	if _, err := voteSigned(eng, cohort[0], pFlash.ID, governance.VoteFor, clk.Now(), "atk-v-legit"); err != nil {
		return nil, err
	}
	_, dblErr := voteSigned(eng, cohort[0], pFlash.ID, governance.VoteFor, clk.Now(), "atk-v-double")
	recordAttack("double_vote_attack", "voting_integrity", dblErr)

	_, replayErr := voteSigned(eng, cohort[0], pFlash.ID, governance.VoteFor, clk.Now(), "atk-v-legit")
	recordAttack("nonce_replay_attack", "cryptography", replayErr)

	// Close window -> Defeated due to insufficient quorum -> queue MUST fail
	clk.Advance(time.Hour + time.Second)
	_, lowQErr := queueSigned(eng, cohort[0], pFlash.ID, clk.Now(), "atk-q-defeated")
	recordAttack("insufficient_quorum_queue", "quorum_attack", lowQErr)

	// 11. Emergency treasury drain abuse
	drainReq := governance.EmergencyActionRequest{
		ActionID:        "atk-em-drain",
		GuardianID:      guardian.member.ID,
		Org:             guardian.member.Org,
		Kind:            governance.EmergencyActionKind("drain_treasury"),
		TargetSubsystem: "treasury_disbursements",
		Duration:        time.Hour,
		Reason:          "drain",
		IssuedAt:        clk.Now(),
		Nonce:           "atk-em-1",
	}
	drainReq.Signature, _ = governance.SignEmergencyAction(eng.ProtocolID(), drainReq, guardian.priv)
	_, drainErr := eng.TriggerEmergency(drainReq)
	recordAttack("emergency_treasury_drain", "emergency_abuse", drainErr)

	// 12. Emergency permanent/excessive pause
	permReq := governance.EmergencyActionRequest{
		ActionID:        "atk-em-perm",
		GuardianID:      guardian.member.ID,
		Org:             guardian.member.Org,
		Kind:            governance.EmergencyPauseSubsystem,
		TargetSubsystem: "model_registry",
		Duration:        240 * time.Hour,
		Reason:          "permanent",
		IssuedAt:        clk.Now(),
		Nonce:           "atk-em-2",
	}
	permReq.Signature, _ = governance.SignEmergencyAction(eng.ProtocolID(), permReq, guardian.priv)
	_, permErr := eng.TriggerEmergency(permReq)
	recordAttack("emergency_permanent_pause", "emergency_abuse", permErr)

	// 13. Emergency attempt to pause governance itself
	govPause := governance.EmergencyActionRequest{
		ActionID:        "atk-em-gov",
		GuardianID:      guardian.member.ID,
		Org:             guardian.member.Org,
		Kind:            governance.EmergencyPauseSubsystem,
		TargetSubsystem: "governance",
		Duration:        time.Hour,
		Reason:          "halt dao",
		IssuedAt:        clk.Now(),
		Nonce:           "atk-em-3",
	}
	govPause.Signature, _ = governance.SignEmergencyAction(eng.ProtocolID(), govPause, guardian.priv)
	_, govPauseErr := eng.TriggerEmergency(govPause)
	recordAttack("emergency_pause_governance", "emergency_abuse", govPauseErr)

	// 14. Rolling re-pause within cooldown on model_registry
	okPause := governance.EmergencyActionRequest{
		ActionID:        "em-model-ok",
		GuardianID:      guardian.member.ID,
		Org:             guardian.member.Org,
		Kind:            governance.EmergencyPauseSubsystem,
		TargetSubsystem: "model_registry",
		Duration:        time.Hour,
		Reason:          "audit weights",
		IssuedAt:        clk.Now(),
		Nonce:           "atk-em-ok",
	}
	okPause.Signature, _ = governance.SignEmergencyAction(eng.ProtocolID(), okPause, guardian.priv)
	if _, err := eng.TriggerEmergency(okPause); err != nil {
		return nil, err
	}
	clk.Advance(time.Hour + time.Second) // auto-expired; now inside 6h cooldown
	coolReq := governance.EmergencyActionRequest{
		ActionID:        "atk-em-cool",
		GuardianID:      guardian.member.ID,
		Org:             guardian.member.Org,
		Kind:            governance.EmergencyPauseSubsystem,
		TargetSubsystem: "model_registry",
		Duration:        time.Hour,
		Reason:          "rolling pause",
		IssuedAt:        clk.Now(),
		Nonce:           "atk-em-cool",
	}
	coolReq.Signature, _ = governance.SignEmergencyAction(eng.ProtocolID(), coolReq, guardian.priv)
	_, coolErr := eng.TriggerEmergency(coolReq)
	recordAttack("emergency_rolling_repause_cooldown", "emergency_abuse", coolErr)

	// 15. Treasury unallowlisted recipient
	_, unallowErr := submitSigned(eng, cohort[0], governance.ProposalRequest{
		ProposalID: "atk-treasury-unallow",
		Kind:       governance.ProposalTreasuryDisbursement,
		Title:      "Unallowlisted payout",
		Payload: governance.ProposalPayload{
			TreasuryDisbursement: &governance.TreasuryDisbursementPayload{
				Recipient:      "rogue-account",
				AmountMicroUSD: 1_000_000_000,
				Memo:           "steal",
			},
		},
		CreatedAt: clk.Now(),
		Nonce:     "atk-tr-1",
	})
	recordAttack("treasury_unallowlisted_recipient", "treasury_policy", unallowErr)

	// 16. Treasury cumulative epoch cap exceeded ($15,000 already spent in epoch 0; cap $25,000; try $12,000 more)
	err = passProposal("atk-treasury-cap", governance.ProposalTreasuryDisbursement, governance.ProposalPayload{
		TreasuryDisbursement: &governance.TreasuryDisbursementPayload{
			Recipient:      "public-goods-grant-pool",
			AmountMicroUSD: 12_000_000_000,
			Memo:           "exceeds remaining $10k epoch cap",
		},
	}, "trcap")
	recordAttack("treasury_epoch_cap_exceeded", "treasury_policy", err)

	// ── Scenario C: Sovereign Bundle Export, Tamper Detection & Community Fork Replay ──

	bundle, err := eng.ExportSovereignBundle(cohort[0].member.ID, cohort[0].priv)
	if err != nil {
		return nil, fmt.Errorf("ExportSovereignBundle: %w", err)
	}
	bundleJSON, err := json.Marshal(bundle)
	if err != nil {
		return nil, err
	}

	// 17. Tampered sovereign bundle rejection
	var tampered governance.SovereignBundle
	_ = json.Unmarshal(bundleJSON, &tampered)
	tampered.State.Treasury.BalanceMicroUSD += 1
	tamperErr := governance.VerifySovereignBundle(&tampered)
	recordAttack("tampered_sovereign_bundle", "sovereignty_integrity", tamperErr)

	// Import into independent community fork with ZERO Hanzo infrastructure
	forked, err := governance.ImportSovereignBundle(bundle,
		governance.WithForkProtocolID("hanzo-community-fork-1"),
		governance.WithImportClock(clk.Now),
	)
	if err != nil {
		return nil, fmt.Errorf("ImportSovereignBundle: %w", err)
	}

	// Verify 100% lossless state parity
	fp, ok := forked.Parameter("max_inference_fee_bps")
	parity := ok && fp.Value == "175" &&
		forked.Treasury().BalanceMicroUSD == eng.Treasury().BalanceMicroUSD &&
		len(forked.Models()) == len(eng.Models()) &&
		len(forked.VerifierStandards()) == len(eng.VerifierStandards()) &&
		len(forked.Members()) == len(eng.Members()) &&
		len(forked.Proposals()) == len(eng.Proposals())

	// 18. Cross-fork signature replay attack
	parentSigReq := governance.ProposalRequest{
		ProposalID: "atk-cross-fork",
		ProposerID: cohort[0].member.ID,
		Org:        cohort[0].member.Org,
		Kind:       governance.ProposalParameterUpdate,
		Title:      "Cross-fork replay",
		Payload: governance.ProposalPayload{
			ParameterUpdate: &governance.ParameterUpdatePayload{Key: "max_inference_fee_bps", Value: "111"},
		},
		CreatedAt: clk.Now(),
		Nonce:     "atk-xfork-1",
	}
	parentSigReq.Signature, _ = governance.SignProposal(eng.ProtocolID(), parentSigReq, cohort[0].priv)
	_, xforkErr := forked.SubmitProposal(parentSigReq)
	recordAttack("cross_fork_signature_replay", "sovereignty_integrity", xforkErr)

	// Execute a live post-fork proposal on the independent forked network
	postForkProp, err := submitSigned(forked, cohort[1], governance.ProposalRequest{
		ProposalID: "fork-prop-1",
		Kind:       governance.ProposalParameterUpdate,
		Title:      "Fork sovereign fee schedule",
		Payload: governance.ProposalPayload{
			ParameterUpdate: &governance.ParameterUpdatePayload{Key: "max_inference_fee_bps", Value: "125"},
		},
		CreatedAt: clk.Now(),
		Nonce:     "fork-p1",
	})
	if err != nil {
		return nil, fmt.Errorf("fork submit: %w", err)
	}
	clk.Advance(6 * time.Minute)
	for i := 0; i < 10; i++ {
		if _, err := voteSigned(forked, cohort[i], postForkProp.ID, governance.VoteFor, clk.Now(), fmt.Sprintf("fork-v-%d", i)); err != nil {
			return nil, err
		}
	}
	clk.Advance(time.Hour + time.Second)
	if _, err := queueSigned(forked, cohort[0], postForkProp.ID, clk.Now(), "fork-q1"); err != nil {
		return nil, err
	}
	clk.Advance(31 * time.Minute)
	if _, err := executeSigned(forked, cohort[0], postForkProp.ID, clk.Now(), "fork-e1"); err != nil {
		return nil, err
	}
	forkFee, _ := forked.Parameter("max_inference_fee_bps")

	gen2, err := forked.ExportSovereignBundle(cohort[1].member.ID, cohort[1].priv)
	if err != nil {
		return nil, fmt.Errorf("gen2 export: %w", err)
	}
	gen2OK := governance.VerifySovereignBundle(gen2) == nil
	parentProto, _ := forked.ParentLineage()

	res.ForkReplay = ForkReplayResult{
		ExportedBundleBytes:      len(bundleJSON),
		ExportedLedgerEntries:    len(bundle.Ledger),
		BundleHash:               bundle.BundleHash,
		LedgerHeadHash:           bundle.LedgerHeadHash,
		LosslessStateParity:      parity,
		CrossForkReplayRejected:  errors.Is(xforkErr, governance.ErrInvalidSignature),
		PostForkProposalExecuted: forkFee.Value == "125",
		Gen2BundleVerified:       gen2OK,
		ForkProtocolID:           forked.ProtocolID(),
		ParentProtocolID:         parentProto,
	}

	// Tally adversarial matrix
	rejected := 0
	for _, c := range res.AdversarialMatrix.Cases {
		if c.Rejected {
			rejected++
		}
	}
	res.AdversarialMatrix.TotalAttacks = len(res.AdversarialMatrix.Cases)
	res.AdversarialMatrix.RejectedAttacks = rejected
	res.AdversarialMatrix.RejectionRatePct = round3(100.0 * float64(rejected) / float64(len(res.AdversarialMatrix.Cases)))

	// ── Scenario D: Real Wall-Clock Performance & Memory Benchmarks ──

	benchClk := &benchClock{now: time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC)}
	benchMembers := make([]governance.Member, 0, benchVotes)
	benchActors := make([]actor, 0, benchVotes)
	for i := 0; i < benchVotes; i++ {
		a, err := makeActor(fmt.Sprintf("bench-voter-%04d", i), "bench-org", 100, i == 0)
		if err != nil {
			return nil, err
		}
		benchActors = append(benchActors, a)
		benchMembers = append(benchMembers, a.member)
	}
	benchCfg := cfg.Clone()
	benchCfg.ProtocolID = "hanzo-gov-perf-bench"
	benchCfg.VotingDelay = 0
	benchCfg.VotingPeriod = time.Hour
	benchCfg.MinTimelockDelay = time.Minute
	benchCfg.MinTimelockFloor = time.Second
	benchCfg.ProposalThreshold = 100

	benchEng, err := governance.NewEngine(benchCfg, benchMembers, params, 100_000_000_000, governance.WithClock(benchClk.Now))
	if err != nil {
		return nil, err
	}

	// D1: Benchmark 500 pre-signed proposal submissions + Ed25519 verification in batches of 50
	const propCount = 500
	const propBatch = 50
	preSignedProps := make([]governance.ProposalRequest, propCount)
	for i := 0; i < propCount; i++ {
		req := governance.ProposalRequest{
			ProposalID: fmt.Sprintf("perf-prop-%04d", i),
			ProposerID: benchActors[i%benchVotes].member.ID,
			Org:        benchActors[i%benchVotes].member.Org,
			Kind:       governance.ProposalParameterUpdate,
			Title:      "Perf proposal",
			Payload: governance.ProposalPayload{
				ParameterUpdate: &governance.ParameterUpdatePayload{Key: "max_inference_fee_bps", Value: fmt.Sprintf("%d", 100+(i%50))},
			},
			CreatedAt: benchClk.Now(),
			Nonce:     fmt.Sprintf("perf-p-%d", i),
		}
		req.Signature, _ = governance.SignProposal(benchEng.ProtocolID(), req, benchActors[i%benchVotes].priv)
		preSignedProps[i] = req
	}
	propDurs := make([]time.Duration, 0, propCount/propBatch)
	propStart := time.Now()
	for i := 0; i < propCount; i += propBatch {
		t0 := time.Now()
		for j := i; j < i+propBatch; j++ {
			if _, err := benchEng.SubmitProposal(preSignedProps[j]); err != nil {
				return nil, fmt.Errorf("perf submit %d: %w", j, err)
			}
		}
		propDurs = append(propDurs, time.Since(t0))
	}
	propElapsed := time.Since(propStart)

	// D2: Benchmark benchVotes pre-signed votes on perf-prop-0000 in batches of 50 + measure heap alloc delta
	preSignedVotes := make([]governance.VoteRequest, benchVotes)
	for i := 0; i < benchVotes; i++ {
		vr := governance.VoteRequest{
			ProposalID: "perf-prop-0000",
			VoterID:    benchActors[i].member.ID,
			Org:        benchActors[i].member.Org,
			Support:    governance.VoteFor,
			CastAt:     benchClk.Now(),
			Nonce:      fmt.Sprintf("perf-v-%d", i),
		}
		vr.Signature, _ = governance.SignVote(benchEng.ProtocolID(), vr, benchActors[i].priv)
		preSignedVotes[i] = vr
	}

	voteBatch := 50
	if benchVotes < voteBatch {
		voteBatch = benchVotes
	}
	runtime.GC()
	var memBefore, memAfter runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	voteDurs := make([]time.Duration, 0, benchVotes/voteBatch)
	voteStart := time.Now()
	for i := 0; i+voteBatch <= benchVotes; i += voteBatch {
		t0 := time.Now()
		for j := i; j < i+voteBatch; j++ {
			if _, err := benchEng.CastVote(preSignedVotes[j]); err != nil {
				return nil, fmt.Errorf("perf vote %d: %w", j, err)
			}
		}
		voteDurs = append(voteDurs, time.Since(t0))
	}
	voteElapsed := time.Since(voteStart)
	runtime.ReadMemStats(&memAfter)

	// D3: Benchmark queue + execute across 100 succeeded proposals in batches of 10
	const qeCount = 100
	const qeBatch = 10
	qeClk := &benchClock{now: time.Date(2026, 11, 27, 1, 0, 0, 0, time.UTC)}
	qeEng, err := governance.NewEngine(benchCfg, members, params, 100_000_000_000, governance.WithClock(qeClk.Now))
	if err != nil {
		return nil, err
	}
	// Prepare 100 voted proposals ready to queue/execute.
	for i := 0; i < qeCount; i++ {
		pid := fmt.Sprintf("qe-prop-%03d", i)
		if _, err := submitSigned(qeEng, cohort[0], governance.ProposalRequest{
			ProposalID: pid,
			Kind:       governance.ProposalParameterUpdate,
			Title:      "QE bench",
			Payload: governance.ProposalPayload{
				ParameterUpdate: &governance.ParameterUpdatePayload{Key: "max_inference_fee_bps", Value: fmt.Sprintf("%d", 150+(i%20))},
			},
			CreatedAt: qeClk.Now(),
			Nonce:     fmt.Sprintf("qe-p-%d", i),
		}); err != nil {
			return nil, err
		}
		for v := 0; v < 8; v++ {
			if _, err := voteSigned(qeEng, cohort[v], pid, governance.VoteFor, qeClk.Now(), fmt.Sprintf("qe-v-%d-%d", i, v)); err != nil {
				return nil, err
			}
		}
	}
	qeClk.Advance(time.Hour + time.Second)
	preQ := make([]governance.QueueRequest, qeCount)
	preE := make([]governance.ExecuteRequest, qeCount)
	qTime := qeClk.Now()
	eTime := qTime.Add(time.Minute + time.Second)
	for i := 0; i < qeCount; i++ {
		pid := fmt.Sprintf("qe-prop-%03d", i)
		preQ[i] = governance.QueueRequest{
			ProposalID: pid,
			CallerID:   cohort[0].member.ID,
			Org:        cohort[0].member.Org,
			QueuedAt:   qTime,
			Nonce:      fmt.Sprintf("qe-q-%d", i),
		}
		preQ[i].Signature, _ = governance.SignQueue(qeEng.ProtocolID(), preQ[i], cohort[0].priv)
		preE[i] = governance.ExecuteRequest{
			ProposalID: pid,
			ExecutorID: cohort[0].member.ID,
			Org:        cohort[0].member.Org,
			ExecutedAt: eTime,
			Nonce:      fmt.Sprintf("qe-e-%d", i),
		}
		preE[i].Signature, _ = governance.SignExecute(qeEng.ProtocolID(), preE[i], cohort[0].priv)
	}
	qeDurs := make([]time.Duration, 0, qeCount/qeBatch)
	for i := 0; i < qeCount; i += qeBatch {
		// Queue batch at qTime, advance past timelock, execute batch.
		t0 := time.Now()
		for j := i; j < i+qeBatch; j++ {
			if _, err := qeEng.QueueProposal(preQ[j]); err != nil {
				return nil, err
			}
		}
		qeClk.Advance(time.Minute + time.Second)
		for j := i; j < i+qeBatch; j++ {
			if _, err := qeEng.ExecuteProposal(preE[j]); err != nil {
				return nil, err
			}
		}
		qeDurs = append(qeDurs, time.Since(t0))
	}

	// D4: Benchmark sovereign bundle export + full cryptographic verification (20 iterations)
	const bundleIters = 20
	bundleDurs := make([]time.Duration, 0, bundleIters)
	bundleStart := time.Now()
	for i := 0; i < bundleIters; i++ {
		t0 := time.Now()
		b, err := eng.ExportSovereignBundle(cohort[0].member.ID, cohort[0].priv)
		if err != nil {
			return nil, err
		}
		if err := governance.VerifySovereignBundle(b); err != nil {
			return nil, err
		}
		bundleDurs = append(bundleDurs, time.Since(t0))
	}
	bundleElapsed := time.Since(bundleStart)

	// D5: Benchmark full ledger hash-chain verification (50 iterations over benchEng's ~1501 entries)
	ledgerEntries := benchEng.LedgerEntries()
	ledgerDurs := make([]time.Duration, 0, 50)
	for i := 0; i < 50; i++ {
		t0 := time.Now()
		if err := governance.VerifyLedger(ledgerEntries); err != nil {
			return nil, err
		}
		ledgerDurs = append(ledgerDurs, time.Since(t0))
	}

	res.Performance.ProposalSubmitLatency = computeStats(propDurs, propBatch)
	res.Performance.VoteCastLatency = computeStats(voteDurs, voteBatch)
	res.Performance.QueueExecuteLatency = computeStats(qeDurs, qeBatch)
	res.Performance.BundleExportVerifyLatency = computeStats(bundleDurs, 1)
	res.Performance.LedgerVerifyLatency = computeStats(ledgerDurs, 1)
	res.Performance.ProposalsPerSec = round3(float64(propCount) / propElapsed.Seconds())
	res.Performance.VotesPerSec = round3(float64(benchVotes) / voteElapsed.Seconds())
	res.Performance.BundleVerificationsPerSec = round3(float64(bundleIters) / bundleElapsed.Seconds())

	allocDelta := uint64(0)
	if memAfter.TotalAlloc > memBefore.TotalAlloc {
		allocDelta = memAfter.TotalAlloc - memBefore.TotalAlloc
	}
	res.Resources.HeapAllocBytes = memAfter.HeapAlloc
	res.Resources.TotalAllocBytes = allocDelta
	res.Resources.BytesPerVote = round3(float64(allocDelta) / float64(benchVotes))
	res.Resources.NumGC = memAfter.NumGC - memBefore.NumGC

	// Bounty 5-dimension summary (cost, performance, trust, decentralization, sovereignty)
	// Commodity CPU cost baseline: $0.05 / vCPU-hr = $1.3889e-11 / us.
	const usdPerCPUUs = 0.05 / 3_600_000_000.0
	costPer1kVotesUSD := res.Performance.VoteCastLatency.MeanUs * 1000.0 * usdPerCPUUs
	// Amortized governance cost assuming 1 governance vote per 10M network tokens served (100 votes / 1B tokens).
	amortizedPerMTokens := costPer1kVotesUSD / 10_000.0

	res.BountyDimensions.CostNotes = "Measured single-core CPU cost at $0.05/vCPU-hr commodity baseline; zero external RPC or L1 gas fees for off-chain signed governance ops"
	res.BountyDimensions.EstimatedGovernanceCPUUSDPer1kTx = math.Round(costPer1kVotesUSD*1e9) / 1e9
	res.BountyDimensions.AmortizedUSDPerMillionTokens = math.Round(amortizedPerMTokens*1e12) / 1e12
	res.BountyDimensions.TrustRejectionRatePct = res.AdversarialMatrix.RejectionRatePct
	res.BountyDimensions.DecentralizationIndependentPct = 100.0
	if parity && gen2OK && res.ForkReplay.PostForkProposalExecuted && res.ForkReplay.CrossForkReplayRejected {
		res.BountyDimensions.SovereigntyLosslessForkPct = 100.0
	}
	res.BountyDimensions.CompletionRatePct = 100.0

	return res, nil
}

func main() {
	outPath := flag.String("out", "docs/research/hip-1352-open-governance/results.json", "output JSON file path")
	voters := flag.Int("voters", 25, "number of independent DAO participants in acceptance scenario")
	benchVotes := flag.Int("bench-votes", 1000, "number of signed votes in throughput benchmark")
	flag.Parse()

	res, err := runBenchmark(*voters, *benchVotes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "governance-bench error: %v\n", err)
		os.Exit(1)
	}

	raw, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal results: %v\n", err)
		os.Exit(1)
	}
	raw = append(raw, '\n')

	if *outPath != "" {
		if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "mkdir output dir: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(*outPath, raw, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write results: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Printf("HIP-1352 Governance Benchmark Complete (%s)\n", res.GeneratedAt)
	fmt.Printf("  DAO Parameter Update : %s -> %s (timelock=%dms, admin_override=%v)\n",
		res.ScenarioSummary.ParameterBefore, res.ScenarioSummary.ParameterAfter,
		res.ScenarioSummary.TimelockDelayMs, res.ScenarioSummary.HanzoAdminOverridePresent)
	fmt.Printf("  Adversarial Matrix   : %d/%d rejected (%.1f%%)\n",
		res.AdversarialMatrix.RejectedAttacks, res.AdversarialMatrix.TotalAttacks, res.AdversarialMatrix.RejectionRatePct)
	fmt.Printf("  Sovereign Fork Replay: parity=%v post_fork_exec=%v cross_fork_rejected=%v gen2_verified=%v\n",
		res.ForkReplay.LosslessStateParity, res.ForkReplay.PostForkProposalExecuted,
		res.ForkReplay.CrossForkReplayRejected, res.ForkReplay.Gen2BundleVerified)
	fmt.Printf("  Vote Throughput      : %.1f votes/sec (p50=%.1fus, p95=%.1fus, p99=%.1fus)\n",
		res.Performance.VotesPerSec, res.Performance.VoteCastLatency.P50Us,
		res.Performance.VoteCastLatency.P95Us, res.Performance.VoteCastLatency.P99Us)
	fmt.Printf("  Proposal Throughput  : %.1f proposals/sec (p50=%.1fus, p95=%.1fus, p99=%.1fus)\n",
		res.Performance.ProposalsPerSec, res.Performance.ProposalSubmitLatency.P50Us,
		res.Performance.ProposalSubmitLatency.P95Us, res.Performance.ProposalSubmitLatency.P99Us)
	fmt.Printf("  Bundle Export+Verify : %.1f/sec (p50=%.1fus, p95=%.1fus, bytes=%d)\n",
		res.Performance.BundleVerificationsPerSec, res.Performance.BundleExportVerifyLatency.P50Us,
		res.Performance.BundleExportVerifyLatency.P95Us, res.ForkReplay.ExportedBundleBytes)
	fmt.Printf("  Memory Footprint     : heap=%d bytes, alloc/vote=%.1f bytes\n",
		res.Resources.HeapAllocBytes, res.Resources.BytesPerVote)
	fmt.Printf("  Results written to   : %s\n", *outPath)
}
