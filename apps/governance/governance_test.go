// SPDX-License-Identifier: Apache-2.0 OR MIT

package governance

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock(start time.Time) *testClock {
	return &testClock{now: start.UTC()}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type testActor struct {
	member Member
	priv   ed25519.PrivateKey
}

func makeActor(t *testing.T, id, org string, power uint64, guardian bool) testActor {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return testActor{
		member: Member{
			ID:                  id,
			Org:                 org,
			PublicKeyHex:        hex.EncodeToString(pub),
			VotingPower:         power,
			IsEmergencyGuardian: guardian,
			Active:              true,
		},
		priv: priv,
	}
}

func testConfig(protocolID string) GovernanceConfig {
	return GovernanceConfig{
		ProtocolID:                    protocolID,
		NetworkVersion:                "1.0.0",
		VotingDelay:                   10 * time.Minute,
		VotingPeriod:                  2 * time.Hour,
		MinTimelockDelay:              1 * time.Hour,
		MinTimelockFloor:              30 * time.Minute,
		GracePeriod:                   24 * time.Hour,
		QuorumBps:                     2500, // 25%
		MinQuorumBpsFloor:             1000, // 10%
		ApprovalThresholdBps:          5001, // >50%
		ProposalThreshold:             100,
		MaxEmergencyDuration:          4 * time.Hour,
		EmergencyCooldown:             6 * time.Hour,
		TreasuryEpochDuration:         24 * time.Hour,
		TreasuryEpochCapMicroUSD:      10_000_000_000, // $10,000
		MaxSingleDisbursementMicroUSD: 6_000_000_000,  // $6,000
		AllowlistedRecipients: map[string]bool{
			"grant-builder-1": true,
		},
		NoAdminOverride: true,
	}
}

func defaultParams() map[string]ParameterEntry {
	minFee := 0.0
	maxFee := 1000.0
	minVerifiers := 3.0
	maxVerifiers := 64.0
	return map[string]ParameterEntry{
		"max_inference_fee_bps": {
			Key:         "max_inference_fee_bps",
			Value:       "250",
			ValueType:   "uint64",
			MinNumeric:  &minFee,
			MaxNumeric:  &maxFee,
			Description: "Maximum protocol inference fee in bps",
		},
		"min_verifier_committee": {
			Key:         "min_verifier_committee",
			Value:       "5",
			ValueType:   "uint64",
			MinNumeric:  &minVerifiers,
			MaxNumeric:  &maxVerifiers,
			Description: "Minimum verifier committee size",
		},
		"genesis_chain_tag": {
			Key:         "genesis_chain_tag",
			Value:       "open-hanzo-v1",
			ValueType:   "string",
			Immutable:   true,
			Description: "Immutable genesis identifier",
		},
	}
}

func setupDAO(t *testing.T) (*Engine, *testClock, map[string]testActor) {
	t.Helper()
	clk := newTestClock(time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC))
	actors := map[string]testActor{
		"alice":    makeActor(t, "alice", "org-a", 300, false),
		"bob":      makeActor(t, "bob", "org-b", 300, false),
		"carol":    makeActor(t, "carol", "org-c", 250, false),
		"dave":     makeActor(t, "dave", "org-d", 100, false),
		"eve":      makeActor(t, "eve", "org-e", 50, false), // below ProposalThreshold (100)
		"guardian": makeActor(t, "guardian", "org-sec", 100, true),
	}
	members := make([]Member, 0, len(actors))
	for _, a := range actors {
		members = append(members, a.member)
	}
	eng, err := NewEngine(
		testConfig("hanzo-open-gov-1"),
		members,
		defaultParams(),
		50_000_000_000, // $50,000 treasury
		WithClock(clk.Now),
		WithLedgerFile(filepath.Join(t.TempDir(), "governance.jsonl")),
	)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return eng, clk, actors
}

func signAndSubmit(t *testing.T, eng *Engine, a testActor, req ProposalRequest) (*Proposal, error) {
	t.Helper()
	req.ProposerID = a.member.ID
	req.Org = a.member.Org
	sig, err := SignProposal(eng.ProtocolID(), req, a.priv)
	if err != nil {
		t.Fatalf("SignProposal: %v", err)
	}
	req.Signature = sig
	return eng.SubmitProposal(req)
}

func signAndVote(t *testing.T, eng *Engine, a testActor, propID string, support VoteSupport, now time.Time, nonce string) (*VoteReceipt, error) {
	t.Helper()
	req := VoteRequest{
		ProposalID: propID,
		VoterID:    a.member.ID,
		Org:        a.member.Org,
		Support:    support,
		CastAt:     now,
		Nonce:      nonce,
	}
	sig, err := SignVote(eng.ProtocolID(), req, a.priv)
	if err != nil {
		t.Fatalf("SignVote: %v", err)
	}
	req.Signature = sig
	return eng.CastVote(req)
}

func signAndQueue(t *testing.T, eng *Engine, a testActor, propID string, now time.Time, nonce string) (*Proposal, error) {
	t.Helper()
	req := QueueRequest{
		ProposalID: propID,
		CallerID:   a.member.ID,
		Org:        a.member.Org,
		QueuedAt:   now,
		Nonce:      nonce,
	}
	sig, err := SignQueue(eng.ProtocolID(), req, a.priv)
	if err != nil {
		t.Fatalf("SignQueue: %v", err)
	}
	req.Signature = sig
	return eng.QueueProposal(req)
}

func signAndExecute(t *testing.T, eng *Engine, a testActor, propID string, now time.Time, nonce string) (*ExecutionReceipt, error) {
	t.Helper()
	req := ExecuteRequest{
		ProposalID: propID,
		ExecutorID: a.member.ID,
		Org:        a.member.Org,
		ExecutedAt: now,
		Nonce:      nonce,
	}
	sig, err := SignExecute(eng.ProtocolID(), req, a.priv)
	if err != nil {
		t.Fatalf("SignExecute: %v", err)
	}
	req.Signature = sig
	return eng.ExecuteProposal(req)
}

func TestMultiPartyDAOTimelockedParameterUpdate(t *testing.T) {
	eng, clk, actors := setupDAO(t)

	// 1. Submit proposal to change noncritical parameter max_inference_fee_bps: 250 -> 175.
	prop, err := signAndSubmit(t, eng, actors["alice"], ProposalRequest{
		ProposalID:  "prop-fee-175",
		Kind:        ProposalParameterUpdate,
		Title:       "Lower max inference fee to 175 bps",
		Description: "Community-approved fee reduction",
		Payload: ProposalPayload{
			ParameterUpdate: &ParameterUpdatePayload{Key: "max_inference_fee_bps", Value: "175"},
		},
		CreatedAt: clk.Now(),
		Nonce:     "alice-p1",
	})
	if err != nil {
		t.Fatalf("SubmitProposal: %v", err)
	}
	if prop.State != StatePending {
		t.Fatalf("initial state = %s, want pending", prop.State)
	}

	// Voting while Pending must fail.
	if _, err := signAndVote(t, eng, actors["alice"], prop.ID, VoteFor, clk.Now(), "alice-v0"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("vote during pending err = %v, want ErrInvalidState", err)
	}

	// 2. Advance into Active window and cast multi-party votes.
	clk.Advance(15 * time.Minute)
	if _, err := signAndVote(t, eng, actors["alice"], prop.ID, VoteFor, clk.Now(), "alice-v1"); err != nil {
		t.Fatalf("alice vote: %v", err)
	}
	if _, err := signAndVote(t, eng, actors["bob"], prop.ID, VoteFor, clk.Now(), "bob-v1"); err != nil {
		t.Fatalf("bob vote: %v", err)
	}
	if _, err := signAndVote(t, eng, actors["carol"], prop.ID, VoteAgainst, clk.Now(), "carol-v1"); err != nil {
		t.Fatalf("carol vote: %v", err)
	}

	// Double vote by Alice must fail.
	if _, err := signAndVote(t, eng, actors["alice"], prop.ID, VoteFor, clk.Now(), "alice-v2"); !errors.Is(err, ErrAlreadyVoted) {
		t.Fatalf("double vote err = %v, want ErrAlreadyVoted", err)
	}

	// Queueing before VotingPeriod ends must fail.
	if _, err := signAndQueue(t, eng, actors["dave"], prop.ID, clk.Now(), "dave-q0"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("early queue err = %v, want ErrInvalidState", err)
	}

	// 3. Advance past VotingPeriod; verify Succeeded (For=600, Against=250, Quorum=275 of 1100).
	clk.Advance(2*time.Hour + time.Second)
	got, err := eng.Proposal(prop.ID)
	if err != nil {
		t.Fatalf("Proposal: %v", err)
	}
	if got.State != StateSucceeded {
		t.Fatalf("state after vote = %s, want succeeded", got.State)
	}

	// 4. Permissionless queue by Dave (not proposer!).
	queued, err := signAndQueue(t, eng, actors["dave"], prop.ID, clk.Now(), "dave-q1")
	if err != nil {
		t.Fatalf("QueueProposal: %v", err)
	}
	if queued.State != StateQueued || queued.ETA == nil {
		t.Fatalf("queued state = %s, eta = %v", queued.State, queued.ETA)
	}

	// 5. Premature execution before MinTimelockDelay must be rejected.
	clk.Advance(30 * time.Minute)
	if _, err := signAndExecute(t, eng, actors["dave"], prop.ID, clk.Now(), "dave-e0"); !errors.Is(err, ErrTimelockNotReady) {
		t.Fatalf("premature execute err = %v, want ErrTimelockNotReady", err)
	}

	// 6. Advance past ETA and execute permissionlessly.
	clk.Advance(31 * time.Minute)
	receipt, err := signAndExecute(t, eng, actors["dave"], prop.ID, clk.Now(), "dave-e1")
	if err != nil {
		t.Fatalf("ExecuteProposal: %v", err)
	}
	if receipt.ResultHash == "" || receipt.LedgerSeq == 0 {
		t.Fatalf("invalid execution receipt: %+v", receipt)
	}

	// 7. Verify parameter updated and hash-chained JSONL audit ledger replays cleanly.
	param, ok := eng.Parameter("max_inference_fee_bps")
	if !ok || param.Value != "175" {
		t.Fatalf("parameter max_inference_fee_bps = %+v, want 175", param)
	}
	jsonl, err := eng.LedgerJSONL()
	if err != nil {
		t.Fatalf("LedgerJSONL: %v", err)
	}
	decoded, err := UnmarshalLedgerJSONL(jsonl)
	if err != nil {
		t.Fatalf("UnmarshalLedgerJSONL: %v", err)
	}
	if len(decoded) < 7 {
		t.Fatalf("expected >= 7 ledger entries, got %d", len(decoded))
	}
}

func TestNoAdminOverrideAndProtocolFloorsEnforced(t *testing.T) {
	// 1. Engine constructor refuses NoAdminOverride=false.
	cfg := testConfig("bad-net")
	cfg.NoAdminOverride = false
	a := makeActor(t, "alice", "org-a", 1000, false)
	if _, err := NewEngine(cfg, []Member{a.member}, defaultParams(), 0); !errors.Is(err, ErrAdminOverrideForbidden) {
		t.Fatalf("expected ErrAdminOverrideForbidden, got %v", err)
	}

	eng, clk, actors := setupDAO(t)

	// 2. Proposal attempting to install hanzo_admin_override is rejected.
	_, err := signAndSubmit(t, eng, actors["alice"], ProposalRequest{
		ProposalID: "prop-admin-override",
		Kind:       ProposalParameterUpdate,
		Title:      "Enable Hanzo admin override",
		Payload: ProposalPayload{
			ParameterUpdate: &ParameterUpdatePayload{Key: "hanzo_admin_override", Value: "true"},
		},
		CreatedAt: clk.Now(),
		Nonce:     "alice-admin-1",
	})
	if !errors.Is(err, ErrAdminOverrideForbidden) {
		t.Fatalf("admin override proposal err = %v, want ErrAdminOverrideForbidden", err)
	}

	// 3. Proposal attempting to lower quorum below MinQuorumBpsFloor (1000) is rejected.
	_, err = signAndSubmit(t, eng, actors["alice"], ProposalRequest{
		ProposalID: "prop-low-quorum",
		Kind:       ProposalParameterUpdate,
		Title:      "Drop quorum to 1%",
		Payload: ProposalPayload{
			ParameterUpdate: &ParameterUpdatePayload{Key: "quorum_bps", Value: "100"},
		},
		CreatedAt: clk.Now(),
		Nonce:     "alice-q-low",
	})
	if !errors.Is(err, ErrProtocolInvariant) {
		t.Fatalf("low quorum proposal err = %v, want ErrProtocolInvariant", err)
	}

	// 4. Proposal attempting to set timelock to 0 is rejected.
	_, err = signAndSubmit(t, eng, actors["alice"], ProposalRequest{
		ProposalID: "prop-zero-timelock",
		Kind:       ProposalParameterUpdate,
		Title:      "Disable timelock",
		Payload: ProposalPayload{
			ParameterUpdate: &ParameterUpdatePayload{Key: "min_timelock_delay_ms", Value: "0"},
		},
		CreatedAt: clk.Now(),
		Nonce:     "alice-tl-0",
	})
	if !errors.Is(err, ErrProtocolInvariant) {
		t.Fatalf("zero timelock proposal err = %v, want ErrProtocolInvariant", err)
	}

	// 5. Proposal exceeding parameter numeric bounds or modifying immutable parameter is rejected.
	_, err = signAndSubmit(t, eng, actors["alice"], ProposalRequest{
		ProposalID: "prop-oob-fee",
		Kind:       ProposalParameterUpdate,
		Title:      "Set fee to 9999 bps",
		Payload: ProposalPayload{
			ParameterUpdate: &ParameterUpdatePayload{Key: "max_inference_fee_bps", Value: "9999"},
		},
		CreatedAt: clk.Now(),
		Nonce:     "alice-oob",
	})
	if !errors.Is(err, ErrInvalidProposal) {
		t.Fatalf("out-of-bounds parameter err = %v, want ErrInvalidProposal", err)
	}

	_, err = signAndSubmit(t, eng, actors["alice"], ProposalRequest{
		ProposalID: "prop-immutable",
		Kind:       ProposalParameterUpdate,
		Title:      "Overwrite immutable genesis tag",
		Payload: ProposalPayload{
			ParameterUpdate: &ParameterUpdatePayload{Key: "genesis_chain_tag", Value: "hijacked"},
		},
		CreatedAt: clk.Now(),
		Nonce:     "alice-imm",
	})
	if !errors.Is(err, ErrProtocolInvariant) {
		t.Fatalf("immutable parameter err = %v, want ErrProtocolInvariant", err)
	}

	// 6. Proposer below ProposalThreshold (eve has 50 < 100) is rejected.
	_, err = signAndSubmit(t, eng, actors["eve"], ProposalRequest{
		ProposalID: "prop-eve-low-power",
		Kind:       ProposalParameterUpdate,
		Title:      "Spam proposal",
		Payload: ProposalPayload{
			ParameterUpdate: &ParameterUpdatePayload{Key: "max_inference_fee_bps", Value: "200"},
		},
		CreatedAt: clk.Now(),
		Nonce:     "eve-p1",
	})
	if !errors.Is(err, ErrInsufficientPower) {
		t.Fatalf("low power proposer err = %v, want ErrInsufficientPower", err)
	}
}

func TestSnapshotPreventsFlashLoanAndQuorumManipulation(t *testing.T) {
	eng, clk, actors := setupDAO(t)

	// Eve delegates 50 power to Dave BEFORE proposal creation -> Dave has 150 at snapshot #2.
	delReq := DelegationRequest{
		DelegatorID: "eve",
		DelegateeID: "dave",
		Org:         actors["eve"].member.Org,
		IssuedAt:    clk.Now(),
		Nonce:       "eve-del-1",
	}
	sig, err := SignDelegation(eng.ProtocolID(), delReq, actors["eve"].priv)
	if err != nil {
		t.Fatalf("SignDelegation: %v", err)
	}
	delReq.Signature = sig
	snap2, err := eng.Delegate(delReq)
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	if snap2.EffectivePower["dave"] != 150 || snap2.EffectivePower["eve"] != 0 {
		t.Fatalf("unexpected delegated power: dave=%d eve=%d", snap2.EffectivePower["dave"], snap2.EffectivePower["eve"])
	}

	// Submit proposal locked to snapshot #2.
	prop, err := signAndSubmit(t, eng, actors["dave"], ProposalRequest{
		ProposalID: "prop-flashloan-test",
		Kind:       ProposalParameterUpdate,
		Title:      "Test snapshot lock",
		Payload: ProposalPayload{
			ParameterUpdate: &ParameterUpdatePayload{Key: "min_verifier_committee", Value: "7"},
		},
		CreatedAt: clk.Now(),
		Nonce:     "dave-p1",
	})
	if err != nil {
		t.Fatalf("SubmitProposal: %v", err)
	}
	if prop.SnapshotID != 2 {
		t.Fatalf("snapshot_id = %d, want 2", prop.SnapshotID)
	}

	// Flash-loan attack AFTER proposal creation: Alice transfers 300 power to Eve,
	// attempting to give Eve post-snapshot voting power on prop-flashloan-test.
	xfer := TransferPowerRequest{
		FromMemberID: "alice",
		ToMemberID:   "eve",
		Amount:       300,
		Org:          actors["alice"].member.Org,
		IssuedAt:     clk.Now(),
		Nonce:        "alice-xfer-1",
	}
	xSig, err := SignTransferPower(eng.ProtocolID(), xfer, actors["alice"].priv)
	if err != nil {
		t.Fatalf("SignTransferPower: %v", err)
	}
	xfer.Signature = xSig
	snap3, err := eng.TransferVotingPower(xfer)
	if err != nil {
		t.Fatalf("TransferVotingPower: %v", err)
	}
	if snap3.SnapshotID != 3 {
		t.Fatalf("expected snapshot 3, got %d", snap3.SnapshotID)
	}

	// Advance into Active voting window.
	clk.Advance(15 * time.Minute)

	// Eve had 0 effective power at snapshot #2 (she delegated to Dave) -> MUST fail!
	if _, err := signAndVote(t, eng, actors["eve"], prop.ID, VoteFor, clk.Now(), "eve-v-flash"); !errors.Is(err, ErrInsufficientPower) {
		t.Fatalf("post-snapshot flash-loan voter err = %v, want ErrInsufficientPower", err)
	}

	// Dave casts For (150 weight at snapshot #2, below 275 quorum).
	rec, err := signAndVote(t, eng, actors["dave"], prop.ID, VoteFor, clk.Now(), "dave-v1")
	if err != nil {
		t.Fatalf("dave vote: %v", err)
	}
	if rec.Weight != 150 {
		t.Fatalf("dave weight = %d, want 150", rec.Weight)
	}

	// Carol casts Against (250 weight). Even though For+Against = 400 >= 275,
	// quorum requires For+Abstain >= 275 AND approval requires For/(For+Against) >= 50.01%.
	if _, err := signAndVote(t, eng, actors["carol"], prop.ID, VoteAgainst, clk.Now(), "carol-v1"); err != nil {
		t.Fatalf("carol vote: %v", err)
	}

	// End voting window -> proposal must be Defeated and cannot be queued.
	clk.Advance(2*time.Hour + time.Second)
	got, err := eng.Proposal(prop.ID)
	if err != nil {
		t.Fatalf("Proposal: %v", err)
	}
	if got.State != StateDefeated {
		t.Fatalf("state = %s, want defeated", got.State)
	}
	if _, err := signAndQueue(t, eng, actors["dave"], prop.ID, clk.Now(), "dave-q-defeated"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("queueing defeated proposal err = %v, want ErrInvalidState", err)
	}
}

func TestBoundedEmergencySafeguardsAndAbuseRejection(t *testing.T) {
	eng, clk, actors := setupDAO(t)

	// 1. Non-guardian cannot invoke emergency action.
	nonGuardReq := EmergencyActionRequest{
		ActionID:        "em-nanguard",
		GuardianID:      "alice",
		Org:             actors["alice"].member.Org,
		Kind:            EmergencyPauseSubsystem,
		TargetSubsystem: "treasury_disbursements",
		Duration:        time.Hour,
		Reason:          "unauthorized",
		IssuedAt:        clk.Now(),
		Nonce:           "alice-em-1",
	}
	nonGuardReq.Signature, _ = SignEmergencyAction(eng.ProtocolID(), nonGuardReq, actors["alice"].priv)
	if _, err := eng.TriggerEmergency(nonGuardReq); !errors.Is(err, ErrEmergencyAbuse) {
		t.Fatalf("non-guardian emergency err = %v, want ErrEmergencyAbuse", err)
	}

	// 2. Guardian attempting treasury drain via emergency action is REJECTED.
	drainReq := EmergencyActionRequest{
		ActionID:        "em-drain",
		GuardianID:      "guardian",
		Org:             actors["guardian"].member.Org,
		Kind:            EmergencyActionKind("drain_treasury"),
		TargetSubsystem: "treasury_disbursements",
		Duration:        time.Hour,
		Reason:          "steal funds",
		IssuedAt:        clk.Now(),
		Nonce:           "guard-drain-1",
	}
	drainReq.Signature, _ = SignEmergencyAction(eng.ProtocolID(), drainReq, actors["guardian"].priv)
	if _, err := eng.TriggerEmergency(drainReq); !errors.Is(err, ErrEmergencyAbuse) {
		t.Fatalf("emergency treasury drain err = %v, want ErrEmergencyAbuse", err)
	}

	// 3. Guardian attempting permanent / excessive pause (> MaxEmergencyDuration) is REJECTED.
	permReq := EmergencyActionRequest{
		ActionID:        "em-perm",
		GuardianID:      "guardian",
		Org:             actors["guardian"].member.Org,
		Kind:            EmergencyPauseSubsystem,
		TargetSubsystem: "treasury_disbursements",
		Duration:        100 * time.Hour,
		Reason:          "permanent lock",
		IssuedAt:        clk.Now(),
		Nonce:           "guard-perm-1",
	}
	permReq.Signature, _ = SignEmergencyAction(eng.ProtocolID(), permReq, actors["guardian"].priv)
	if _, err := eng.TriggerEmergency(permReq); !errors.Is(err, ErrEmergencyAbuse) {
		t.Fatalf("excessive emergency duration err = %v, want ErrEmergencyAbuse", err)
	}

	// 4. Guardian attempting to pause governance itself is REJECTED.
	govPauseReq := EmergencyActionRequest{
		ActionID:        "em-gov",
		GuardianID:      "guardian",
		Org:             actors["guardian"].member.Org,
		Kind:            EmergencyPauseSubsystem,
		TargetSubsystem: "governance",
		Duration:        time.Hour,
		Reason:          "block DAO",
		IssuedAt:        clk.Now(),
		Nonce:           "guard-gov-1",
	}
	govPauseReq.Signature, _ = SignEmergencyAction(eng.ProtocolID(), govPauseReq, actors["guardian"].priv)
	if _, err := eng.TriggerEmergency(govPauseReq); !errors.Is(err, ErrEmergencyAbuse) {
		t.Fatalf("pause governance err = %v, want ErrEmergencyAbuse", err)
	}

	// 5. Legitimate bounded 2h pause on treasury_disbursements succeeds and blocks queued treasury execution.
	validPause := EmergencyActionRequest{
		ActionID:        "em-treasury-1",
		GuardianID:      "guardian",
		Org:             actors["guardian"].member.Org,
		Kind:            EmergencyPauseSubsystem,
		TargetSubsystem: "treasury_disbursements",
		Duration:        2 * time.Hour,
		Reason:          "investigate anomalous payout",
		IssuedAt:        clk.Now(),
		Nonce:           "guard-ok-1",
	}
	validPause.Signature, _ = SignEmergencyAction(eng.ProtocolID(), validPause, actors["guardian"].priv)
	act, err := eng.TriggerEmergency(validPause)
	if err != nil {
		t.Fatalf("valid TriggerEmergency: %v", err)
	}
	if !eng.IsSubsystemPaused("treasury_disbursements") {
		t.Fatalf("expected treasury_disbursements to be paused")
	}

	// Advance past automatic expiry (2h + 1s): pause automatically expires with zero txs.
	clk.Advance(2*time.Hour + time.Second)
	if eng.IsSubsystemPaused("treasury_disbursements") {
		t.Fatalf("expected emergency pause %q to auto-expire", act.ActionID)
	}

	// 6. Rolling re-pause within EmergencyCooldown (6h) is REJECTED!
	rePause := EmergencyActionRequest{
		ActionID:        "em-treasury-2",
		GuardianID:      "guardian",
		Org:             actors["guardian"].member.Org,
		Kind:            EmergencyPauseSubsystem,
		TargetSubsystem: "treasury_disbursements",
		Duration:        time.Hour,
		Reason:          "rolling pause abuse",
		IssuedAt:        clk.Now(),
		Nonce:           "guard-re-1",
	}
	rePause.Signature, _ = SignEmergencyAction(eng.ProtocolID(), rePause, actors["guardian"].priv)
	if _, err := eng.TriggerEmergency(rePause); !errors.Is(err, ErrEmergencyCooldown) {
		t.Fatalf("rolling re-pause err = %v, want ErrEmergencyCooldown", err)
	}
}

func TestTreasuryCapsAndAllowlistEnforcement(t *testing.T) {
	eng, clk, actors := setupDAO(t)

	// 1. Unallowlisted recipient rejected at submission.
	_, err := signAndSubmit(t, eng, actors["alice"], ProposalRequest{
		ProposalID: "prop-unallowlisted",
		Kind:       ProposalTreasuryDisbursement,
		Title:      "Pay unallowlisted address",
		Payload: ProposalPayload{
			TreasuryDisbursement: &TreasuryDisbursementPayload{
				Recipient:      "attacker-wallet",
				AmountMicroUSD: 1_000_000_000,
				Memo:           "unauthorized payout",
			},
		},
		CreatedAt: clk.Now(),
		Nonce:     "alice-t-bad",
	})
	if !errors.Is(err, ErrTreasuryPolicy) {
		t.Fatalf("unallowlisted recipient err = %v, want ErrTreasuryPolicy", err)
	}

	// 2. Over single-disbursement cap ($6,000) rejected.
	_, err = signAndSubmit(t, eng, actors["alice"], ProposalRequest{
		ProposalID: "prop-over-single-cap",
		Kind:       ProposalTreasuryDisbursement,
		Title:      "Oversized payout",
		Payload: ProposalPayload{
			TreasuryDisbursement: &TreasuryDisbursementPayload{
				Recipient:      "grant-builder-1",
				AmountMicroUSD: 7_000_000_000,
				Memo:           "too big",
			},
		},
		CreatedAt: clk.Now(),
		Nonce:     "alice-t-big",
	})
	if !errors.Is(err, ErrTreasuryPolicy) {
		t.Fatalf("oversized disbursement err = %v, want ErrTreasuryPolicy", err)
	}

	// 3. Submit TWO concurrent valid $6,000 disbursements in the same epoch (epoch cap is $10,000).
	for i, pid := range []string{"prop-grant-1", "prop-grant-2"} {
		_, err := signAndSubmit(t, eng, actors["alice"], ProposalRequest{
			ProposalID: pid,
			Kind:       ProposalTreasuryDisbursement,
			Title:      fmt.Sprintf("Grant tranche %d", i+1),
			Payload: ProposalPayload{
				TreasuryDisbursement: &TreasuryDisbursementPayload{
					Recipient:      "grant-builder-1",
					AmountMicroUSD: 6_000_000_000,
					Memo:           "milestone payment",
				},
			},
			CreatedAt: clk.Now(),
			Nonce:     fmt.Sprintf("alice-grant-%d", i+1),
		})
		if err != nil {
			t.Fatalf("submit %s: %v", pid, err)
		}
	}

	clk.Advance(15 * time.Minute)
	for i, pid := range []string{"prop-grant-1", "prop-grant-2"} {
		if _, err := signAndVote(t, eng, actors["alice"], pid, VoteFor, clk.Now(), fmt.Sprintf("a-gv-%d", i)); err != nil {
			t.Fatalf("alice vote %s: %v", pid, err)
		}
		if _, err := signAndVote(t, eng, actors["bob"], pid, VoteFor, clk.Now(), fmt.Sprintf("b-gv-%d", i)); err != nil {
			t.Fatalf("bob vote %s: %v", pid, err)
		}
	}
	clk.Advance(2*time.Hour + time.Second)
	for i, pid := range []string{"prop-grant-1", "prop-grant-2"} {
		if _, err := signAndQueue(t, eng, actors["bob"], pid, clk.Now(), fmt.Sprintf("b-gq-%d", i)); err != nil {
			t.Fatalf("queue %s: %v", pid, err)
		}
	}
	clk.Advance(time.Hour + time.Second)

	// First $6,000 executes cleanly.
	if _, err := signAndExecute(t, eng, actors["bob"], "prop-grant-1", clk.Now(), "b-ge-1"); err != nil {
		t.Fatalf("execute grant-1: %v", err)
	}
	// Second $6,000 in SAME epoch exceeds $10,000 epoch cap -> REJECTED at execution time!
	if _, err := signAndExecute(t, eng, actors["bob"], "prop-grant-2", clk.Now(), "b-ge-2"); !errors.Is(err, ErrTreasuryPolicy) {
		t.Fatalf("second disbursement in same epoch err = %v, want ErrTreasuryPolicy", err)
	}

	tr := eng.Treasury()
	if tr.BalanceMicroUSD != 44_000_000_000 || len(tr.Disbursements) != 1 {
		t.Fatalf("unexpected treasury state: %+v", tr)
	}
}

func TestModelRegistryVerifierStandardsAndProtocolUpgrade(t *testing.T) {
	eng, clk, actors := setupDAO(t)

	runPass := func(pid string, kind ProposalKind, payload ProposalPayload, idx int) {
		t.Helper()
		if _, err := signAndSubmit(t, eng, actors["alice"], ProposalRequest{
			ProposalID: pid,
			Kind:       kind,
			Title:      pid,
			Payload:    payload,
			CreatedAt:  clk.Now(),
			Nonce:      fmt.Sprintf("a-p-%d", idx),
		}); err != nil {
			t.Fatalf("submit %s: %v", pid, err)
		}
		clk.Advance(15 * time.Minute)
		if _, err := signAndVote(t, eng, actors["alice"], pid, VoteFor, clk.Now(), fmt.Sprintf("a-v-%d", idx)); err != nil {
			t.Fatalf("alice vote %s: %v", pid, err)
		}
		if _, err := signAndVote(t, eng, actors["bob"], pid, VoteFor, clk.Now(), fmt.Sprintf("b-v-%d", idx)); err != nil {
			t.Fatalf("bob vote %s: %v", pid, err)
		}
		clk.Advance(2*time.Hour + time.Second)
		if _, err := signAndQueue(t, eng, actors["carol"], pid, clk.Now(), fmt.Sprintf("c-q-%d", idx)); err != nil {
			t.Fatalf("queue %s: %v", pid, err)
		}
		clk.Advance(time.Hour + time.Second)
		if _, err := signAndExecute(t, eng, actors["carol"], pid, clk.Now(), fmt.Sprintf("c-e-%d", idx)); err != nil {
			t.Fatalf("execute %s: %v", pid, err)
		}
	}

	// 1. Register verifier standard explicitly typed as cryptographic proof.
	runPass("prop-verifier-zk", ProposalVerifierStandard, ProposalPayload{
		VerifierStandard: &VerifierStandardPayload{
			ProfileID:          "zk-deterministic-v1",
			Method:             VerifyCryptographicProof,
			MinVerifiers:       5,
			QuorumThresholdBps: 6667,
			ChallengeWindowSec: 3600,
			MaxLatencyMs:       250,
			Description:        "Cryptographic proof verification tier",
		},
	}, 1)

	// 2. Register Apache-2.0 model bound to that verifier profile.
	digest := sha256.Sum256([]byte("open-weights-manifest-v1"))
	runPass("prop-model-zen", ProposalModelRegistry, ProposalPayload{
		ModelRegistry: &ModelRegistryPayload{
			ModelID:           "zen-open-7b",
			Version:           "1.0.0",
			DigestSHA256:      hex.EncodeToString(digest[:]),
			LicenseSPDX:       "Apache-2.0",
			VerifierProfileID: "zk-deterministic-v1",
			Active:            true,
		},
	}, 2)

	// 3. Execute protocol upgrade manifest.
	compat := sha256.Sum256([]byte("protocol-v1.1.0-manifest"))
	runPass("prop-upgrade-110", ProposalProtocolUpgrade, ProposalPayload{
		ProtocolUpgrade: &ProtocolUpgradePayload{
			TargetVersion:     "1.1.0",
			CompatibilityHash: hex.EncodeToString(compat[:]),
			ActivationEpoch:   12,
		},
	}, 3)

	if len(eng.VerifierStandards()) != 1 || len(eng.Models()) != 1 || eng.Config().NetworkVersion != "1.1.0" {
		t.Fatalf("unexpected registry/upgrade state: verifiers=%d models=%d ver=%s",
			len(eng.VerifierStandards()), len(eng.Models()), eng.Config().NetworkVersion)
	}
}

func TestSovereignForkabilityTamperRejectionAndCrossForkReplayDefense(t *testing.T) {
	eng, clk, actors := setupDAO(t)

	// Execute one governance proposal on the parent network.
	prop, err := signAndSubmit(t, eng, actors["alice"], ProposalRequest{
		ProposalID: "prop-parent-1",
		Kind:       ProposalParameterUpdate,
		Title:      "Parent fee adjustment",
		Payload: ProposalPayload{
			ParameterUpdate: &ParameterUpdatePayload{Key: "max_inference_fee_bps", Value: "180"},
		},
		CreatedAt: clk.Now(),
		Nonce:     "alice-parent-1",
	})
	if err != nil {
		t.Fatalf("submit parent proposal: %v", err)
	}
	clk.Advance(15 * time.Minute)
	if _, err := signAndVote(t, eng, actors["alice"], prop.ID, VoteFor, clk.Now(), "a-pv-1"); err != nil {
		t.Fatalf("vote: %v", err)
	}
	if _, err := signAndVote(t, eng, actors["bob"], prop.ID, VoteFor, clk.Now(), "b-pv-1"); err != nil {
		t.Fatalf("vote: %v", err)
	}
	clk.Advance(2*time.Hour + time.Second)
	if _, err := signAndQueue(t, eng, actors["carol"], prop.ID, clk.Now(), "c-pq-1"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	clk.Advance(time.Hour + time.Second)
	if _, err := signAndExecute(t, eng, actors["carol"], prop.ID, clk.Now(), "c-pe-1"); err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Export signed SovereignBundle and round-trip through JSON.
	bundle, err := eng.ExportSovereignBundle("alice", actors["alice"].priv)
	if err != nil {
		t.Fatalf("ExportSovereignBundle: %v", err)
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	var decoded SovereignBundle
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal bundle: %v", err)
	}
	if err := VerifySovereignBundle(&decoded); err != nil {
		t.Fatalf("VerifySovereignBundle: %v", err)
	}

	// Tamper detection: mutating treasury balance must fail verification.
	var tampered SovereignBundle
	_ = json.Unmarshal(raw, &tampered)
	tampered.State.Treasury.BalanceMicroUSD += 1
	if err := VerifySovereignBundle(&tampered); !errors.Is(err, ErrInvalidBundle) {
		t.Fatalf("tampered bundle err = %v, want ErrInvalidBundle", err)
	}

	// Fork onto a brand-new independent community network with zero Hanzo infra.
	forked, err := ImportSovereignBundle(&decoded,
		WithForkProtocolID("community-fork-alpha"),
		WithImportClock(clk.Now),
	)
	if err != nil {
		t.Fatalf("ImportSovereignBundle fork: %v", err)
	}
	parentProto, parentHash := forked.ParentLineage()
	if parentProto != "hanzo-open-gov-1" || parentHash != bundle.BundleHash {
		t.Fatalf("unexpected fork lineage: parent=%q hash=%q", parentProto, parentHash)
	}
	if p, ok := forked.Parameter("max_inference_fee_bps"); !ok || p.Value != "180" {
		t.Fatalf("forked engine lost parameter state: %+v", p)
	}

	// Cross-fork replay defense: a proposal signed for parent protocol MUST fail on forked network.
	parentSigned := ProposalRequest{
		ProposalID: "prop-cross-fork-replay",
		ProposerID: "alice",
		Org:        actors["alice"].member.Org,
		Kind:       ProposalParameterUpdate,
		Title:      "Cross-fork replay attempt",
		Payload: ProposalPayload{
			ParameterUpdate: &ParameterUpdatePayload{Key: "max_inference_fee_bps", Value: "120"},
		},
		CreatedAt: clk.Now(),
		Nonce:     "alice-replay-parent",
	}
	parentSigned.Signature, _ = SignProposal("hanzo-open-gov-1", parentSigned, actors["alice"].priv)
	if _, err := forked.SubmitProposal(parentSigned); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("cross-fork replay err = %v, want ErrInvalidSignature", err)
	}

	// Execute a brand-new post-fork proposal signed for community-fork-alpha.
	forkProp, err := signAndSubmit(t, forked, actors["bob"], ProposalRequest{
		ProposalID: "prop-fork-1",
		Kind:       ProposalParameterUpdate,
		Title:      "Independent fork fee schedule",
		Payload: ProposalPayload{
			ParameterUpdate: &ParameterUpdatePayload{Key: "max_inference_fee_bps", Value: "100"},
		},
		CreatedAt: clk.Now(),
		Nonce:     "bob-fork-1",
	})
	if err != nil {
		t.Fatalf("submit post-fork proposal: %v", err)
	}
	clk.Advance(15 * time.Minute)
	if _, err := signAndVote(t, forked, actors["bob"], forkProp.ID, VoteFor, clk.Now(), "b-fv-1"); err != nil {
		t.Fatalf("fork vote: %v", err)
	}
	if _, err := signAndVote(t, forked, actors["carol"], forkProp.ID, VoteFor, clk.Now(), "c-fv-1"); err != nil {
		t.Fatalf("fork vote: %v", err)
	}
	clk.Advance(2*time.Hour + time.Second)
	if _, err := signAndQueue(t, forked, actors["alice"], forkProp.ID, clk.Now(), "a-fq-1"); err != nil {
		t.Fatalf("fork queue: %v", err)
	}
	clk.Advance(time.Hour + time.Second)
	if _, err := signAndExecute(t, forked, actors["alice"], forkProp.ID, clk.Now(), "a-fe-1"); err != nil {
		t.Fatalf("fork execute: %v", err)
	}
	if p, ok := forked.Parameter("max_inference_fee_bps"); !ok || p.Value != "100" {
		t.Fatalf("post-fork parameter = %+v, want 100", p)
	}

	// Second-generation export from forked network must verify cleanly across lineage.
	gen2, err := forked.ExportSovereignBundle("bob", actors["bob"].priv)
	if err != nil {
		t.Fatalf("gen2 ExportSovereignBundle: %v", err)
	}
	if err := VerifySovereignBundle(gen2); err != nil {
		t.Fatalf("gen2 VerifySovereignBundle: %v", err)
	}
}

func TestCancelProposalAndExpiryAndConcurrency(t *testing.T) {
	eng, clk, actors := setupDAO(t)

	// 1. Unauthorized third-party cancel is rejected (no admin veto!).
	p1, err := signAndSubmit(t, eng, actors["alice"], ProposalRequest{
		ProposalID: "prop-cancel-test",
		Kind:       ProposalParameterUpdate,
		Title:      "Cancel authorization test",
		Payload: ProposalPayload{
			ParameterUpdate: &ParameterUpdatePayload{Key: "max_inference_fee_bps", Value: "210"},
		},
		CreatedAt: clk.Now(),
		Nonce:     "alice-c-p1",
	})
	if err != nil {
		t.Fatalf("submit p1: %v", err)
	}
	unauthCancel := CancelRequest{
		ProposalID: p1.ID,
		CallerID:   "guardian",
		Org:        actors["guardian"].member.Org,
		Reason:     "admin veto attempt",
		CanceledAt: clk.Now(),
		Nonce:      "guard-veto-1",
	}
	unauthCancel.Signature, _ = SignCancel(eng.ProtocolID(), unauthCancel, actors["guardian"].priv)
	if _, err := eng.CancelProposal(unauthCancel); !errors.Is(err, ErrUnauthorizedCancel) {
		t.Fatalf("guardian veto cancel err = %v, want ErrUnauthorizedCancel", err)
	}

	// Proposer can cancel while Pending/Active.
	selfCancel := CancelRequest{
		ProposalID: p1.ID,
		CallerID:   "alice",
		Org:        actors["alice"].member.Org,
		Reason:     "withdrawn by proposer",
		CanceledAt: clk.Now(),
		Nonce:      "alice-self-cancel-1",
	}
	selfCancel.Signature, _ = SignCancel(eng.ProtocolID(), selfCancel, actors["alice"].priv)
	canceled, err := eng.CancelProposal(selfCancel)
	if err != nil || canceled.State != StateCanceled {
		t.Fatalf("proposer cancel got state=%v err=%v", canceled, err)
	}

	// 2. GracePeriod expiry test.
	p2, err := signAndSubmit(t, eng, actors["bob"], ProposalRequest{
		ProposalID: "prop-expire-test",
		Kind:       ProposalParameterUpdate,
		Title:      "Grace expiry test",
		Payload: ProposalPayload{
			ParameterUpdate: &ParameterUpdatePayload{Key: "max_inference_fee_bps", Value: "220"},
		},
		CreatedAt: clk.Now(),
		Nonce:     "bob-exp-p1",
	})
	if err != nil {
		t.Fatalf("submit p2: %v", err)
	}
	clk.Advance(15 * time.Minute)
	if _, err := signAndVote(t, eng, actors["alice"], p2.ID, VoteFor, clk.Now(), "a-exp-v1"); err != nil {
		t.Fatalf("vote: %v", err)
	}
	if _, err := signAndVote(t, eng, actors["bob"], p2.ID, VoteFor, clk.Now(), "b-exp-v1"); err != nil {
		t.Fatalf("vote: %v", err)
	}
	clk.Advance(2*time.Hour + time.Second)
	if _, err := signAndQueue(t, eng, actors["bob"], p2.ID, clk.Now(), "b-exp-q1"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	// Advance past ETA (1h) + GracePeriod (24h).
	clk.Advance(26 * time.Hour)
	if _, err := signAndExecute(t, eng, actors["bob"], p2.ID, clk.Now(), "b-exp-e1"); !errors.Is(err, ErrProposalExpired) {
		t.Fatalf("expired execute err = %v, want ErrProposalExpired", err)
	}

	// 3. Concurrency + deep-copy safety under -race.
	p3, err := signAndSubmit(t, eng, actors["alice"], ProposalRequest{
		ProposalID: "prop-race-test",
		Kind:       ProposalParameterUpdate,
		Title:      "Race test",
		Payload: ProposalPayload{
			ParameterUpdate: &ParameterUpdatePayload{Key: "max_inference_fee_bps", Value: "199"},
		},
		CreatedAt: clk.Now(),
		Nonce:     "alice-race-p1",
	})
	if err != nil {
		t.Fatalf("submit p3: %v", err)
	}
	clk.Advance(15 * time.Minute)

	var wg sync.WaitGroup
	voterKeys := []string{"alice", "bob", "carol", "dave", "guardian"}
	for i, k := range voterKeys {
		wg.Add(1)
		go func(idx int, name string) {
			defer wg.Done()
			_, _ = signAndVote(t, eng, actors[name], p3.ID, VoteFor, clk.Now(), fmt.Sprintf("race-v-%d", idx))
			// Mutate returned deep copies to prove internal state cannot be corrupted.
			cfg := eng.Config()
			cfg.AllowlistedRecipients["evil"] = true
			snap := eng.CurrentSnapshot()
			snap.EffectivePower["evil"] = 999999
			_ = eng.Proposals()
			_ = eng.LedgerEntries()
		}(i, k)
	}
	wg.Wait()

	if eng.Config().AllowlistedRecipients["evil"] {
		t.Fatalf("Config() returned shallow-copied map")
	}
	if eng.CurrentSnapshot().EffectivePower["evil"] != 0 {
		t.Fatalf("CurrentSnapshot() returned shallow-copied map")
	}
	if err := VerifyLedger(eng.LedgerEntries()); err != nil {
		t.Fatalf("VerifyLedger after concurrent votes: %v", err)
	}
}
