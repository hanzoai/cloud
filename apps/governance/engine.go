// SPDX-License-Identifier: Apache-2.0 OR MIT

package governance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// forbiddenAdminParamKeys are parameter names permanently blocked from being
// registered or modified so no Hanzo-admin override can ever be introduced.
var forbiddenAdminParamKeys = map[string]bool{
	"admin_override":           true,
	"hanzo_admin_override":     true,
	"hanzo_admin_bypass":       true,
	"no_admin_override":        true,
	"superuser_bypass":         true,
	"root_admin":               true,
	"min_quorum_bps_floor":     true,
	"min_timelock_floor":       true,
	"min_timelock_floor_ms":    true,
	"emergency_admin_override": true,
}

// forbiddenEmergencyTargets are subsystems that a guardian can NEVER pause,
// ensuring the DAO always retains sovereign power to vote, ratify, lift pauses,
// or fork the network.
var forbiddenEmergencyTargets = map[string]bool{
	"":           true,
	"governance": true,
	"voting":     true,
	"timelock":   true,
	"fork":       true,
	"export":     true,
	"quorum":     true,
	"all":        true,
	"*":          true,
}

// Option customizes Engine construction.
type Option func(*Engine)

// WithClock overrides the clock used for timestamps, voting windows, and timelocks.
func WithClock(fn func() time.Time) Option {
	return func(e *Engine) {
		if fn != nil {
			e.nowFn = fn
		}
	}
}

// WithLedgerFile mirrors every appended audit ledger entry to an append-only
// JSONL file on disk.
func WithLedgerFile(path string) Option {
	return func(e *Engine) {
		e.ledgerPath = path
	}
}

// Engine implements the thread-safe HIP-1352 open governance, timelock,
// treasury, bounded emergency safeguard, and sovereign forkability state machine.
type Engine struct {
	mu                sync.RWMutex
	nowFn             func() time.Time
	ledgerPath        string
	cfg               GovernanceConfig
	genesisTime       time.Time
	parentProtocolID  string
	parentBundleHash  string
	ancestorProtocols map[string]bool

	members           map[string]Member
	snapshots         map[uint64]PowerCheckpoint
	currentSnapshotID uint64
	parameters        map[string]ParameterEntry
	models            map[string]ModelRegistryEntry
	verifierStandards map[string]VerifierStandardEntry
	proposals         map[string]*Proposal
	emergencyActions  map[string]*EmergencyAction
	treasury          TreasuryState
	upgrades          []ProtocolUpgradeRecord
	usedNonces        map[string]map[string]bool
	ledger            *AuditLedger
}

// NewEngine constructs a new sovereign governance engine from genesis state.
// Every genesis member must carry a valid Ed25519 public key and total active
// voting power must be non-zero.
func NewEngine(
	cfg GovernanceConfig,
	genesisMembers []Member,
	genesisParams map[string]ParameterEntry,
	treasuryBalanceMicroUSD uint64,
	opts ...Option,
) (*Engine, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	e := &Engine{
		nowFn:             func() time.Time { return time.Now().UTC() },
		cfg:               cfg.Clone(),
		ancestorProtocols: map[string]bool{cfg.ProtocolID: true},
		members:           make(map[string]Member, len(genesisMembers)),
		snapshots:         make(map[uint64]PowerCheckpoint),
		parameters:        make(map[string]ParameterEntry, len(genesisParams)),
		models:            make(map[string]ModelRegistryEntry),
		verifierStandards: make(map[string]VerifierStandardEntry),
		proposals:         make(map[string]*Proposal),
		emergencyActions:  make(map[string]*EmergencyAction),
		treasury: TreasuryState{
			BalanceMicroUSD: treasuryBalanceMicroUSD,
			EpochSpent:      make(map[uint64]uint64),
			Disbursements:   make([]TreasuryDisbursementRecord, 0),
		},
		upgrades:   make([]ProtocolUpgradeRecord, 0),
		usedNonces: make(map[string]map[string]bool),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}
	e.ledger = NewAuditLedger(e.ledgerPath)
	now := e.nowFn().UTC()
	e.genesisTime = now

	if len(genesisMembers) == 0 {
		return nil, fmt.Errorf("%w: at least one genesis member is required", ErrInvalidConfig)
	}
	var totalPower uint64
	for _, m := range genesisMembers {
		if strings.TrimSpace(m.ID) == "" {
			return nil, fmt.Errorf("%w: genesis member id cannot be empty", ErrInvalidConfig)
		}
		if _, dup := e.members[m.ID]; dup {
			return nil, fmt.Errorf("%w: duplicate genesis member %q", ErrInvalidConfig, m.ID)
		}
		if _, err := DecodePublicKey(m.PublicKeyHex); err != nil {
			return nil, fmt.Errorf("%w: member %q: %v", ErrInvalidConfig, m.ID, err)
		}
		if m.JoinedAt.IsZero() {
			m.JoinedAt = now
		} else {
			m.JoinedAt = m.JoinedAt.UTC()
		}
		e.members[m.ID] = m
		if m.Active {
			if math.MaxUint64-totalPower < m.VotingPower {
				return nil, fmt.Errorf("%w: total voting power overflow", ErrInvalidConfig)
			}
			totalPower += m.VotingPower
		}
	}
	if totalPower == 0 {
		return nil, fmt.Errorf("%w: total active genesis voting power must be positive", ErrInvalidConfig)
	}
	for _, m := range e.members {
		if m.DelegateTo != "" {
			target, ok := e.members[m.DelegateTo]
			if !ok || !target.Active {
				return nil, fmt.Errorf("%w: member %q delegates to unknown/inactive member %q", ErrInvalidConfig, m.ID, m.DelegateTo)
			}
		}
	}

	for key, param := range genesisParams {
		canonicalKey := strings.TrimSpace(key)
		if canonicalKey == "" {
			return nil, fmt.Errorf("%w: empty genesis parameter key", ErrInvalidConfig)
		}
		if forbiddenAdminParamKeys[strings.ToLower(canonicalKey)] {
			return nil, fmt.Errorf("%w: genesis parameter %q is forbidden", ErrAdminOverrideForbidden, canonicalKey)
		}
		cp := param.Clone()
		cp.Key = canonicalKey
		if cp.ValueType == "" {
			cp.ValueType = "string"
		}
		if err := validateParameterValue(cp, cp.Value); err != nil {
			return nil, fmt.Errorf("%w: genesis parameter %q: %v", ErrInvalidConfig, canonicalKey, err)
		}
		if cp.UpdatedAt.IsZero() {
			cp.UpdatedAt = now
		} else {
			cp.UpdatedAt = cp.UpdatedAt.UTC()
		}
		e.parameters[canonicalKey] = cp
	}

	// Capture genesis checkpoint #1.
	snap, err := e.captureSnapshotLocked(now)
	if err != nil {
		return nil, err
	}

	// Record genesis entry in hash-chained audit ledger.
	if _, err := e.ledger.Append(now, e.cfg.ProtocolID, "genesis", "protocol", e.cfg.ProtocolID, map[string]any{
		"protocol_id":        e.cfg.ProtocolID,
		"network_version":    e.cfg.NetworkVersion,
		"no_admin_override":  e.cfg.NoAdminOverride,
		"member_count":       len(e.members),
		"total_supply":       snap.TotalSupply,
		"snapshot_id":        snap.SnapshotID,
		"snapshot_root":      snap.StateRoot,
		"treasury_micro_usd": e.treasury.BalanceMicroUSD,
	}); err != nil {
		return nil, err
	}

	return e, nil
}

// Config returns a deep copy of the active governance configuration.
func (e *Engine) Config() GovernanceConfig {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cfg.Clone()
}

// ProtocolID returns the engine's current protocol identifier.
func (e *Engine) ProtocolID() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cfg.ProtocolID
}

// CurrentSnapshot returns a deep copy of the latest voting power checkpoint.
func (e *Engine) CurrentSnapshot() PowerCheckpoint {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.snapshots[e.currentSnapshotID].Clone()
}

// Snapshot returns a deep copy of a specific historical voting power checkpoint.
func (e *Engine) Snapshot(id uint64) (PowerCheckpoint, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	s, ok := e.snapshots[id]
	if !ok {
		return PowerCheckpoint{}, false
	}
	return s.Clone(), true
}

// Members returns a deterministically sorted copy of all registered members.
func (e *Engine) Members() []Member {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Member, 0, len(e.members))
	for _, m := range e.members {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Parameters returns a deterministically sorted deep copy of the parameter registry.
func (e *Engine) Parameters() []ParameterEntry {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]ParameterEntry, 0, len(e.parameters))
	for _, p := range e.parameters {
		out = append(out, p.Clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Parameter looks up a single parameter by key.
func (e *Engine) Parameter(key string) (ParameterEntry, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	p, ok := e.parameters[key]
	if !ok {
		return ParameterEntry{}, false
	}
	return p.Clone(), true
}

// Models returns a deterministically sorted slice of governed model registry entries.
func (e *Engine) Models() []ModelRegistryEntry {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]ModelRegistryEntry, 0, len(e.models))
	for _, m := range e.models {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModelID < out[j].ModelID })
	return out
}

// VerifierStandards returns a deterministically sorted slice of verifier standards.
func (e *Engine) VerifierStandards() []VerifierStandardEntry {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]VerifierStandardEntry, 0, len(e.verifierStandards))
	for _, v := range e.verifierStandards {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProfileID < out[j].ProfileID })
	return out
}

// Treasury returns a deep copy of the current treasury state.
func (e *Engine) Treasury() TreasuryState {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.treasury.Clone()
}

// EmergencyActions returns a deterministically sorted deep copy of all emergency actions.
func (e *Engine) EmergencyActions() []EmergencyAction {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]EmergencyAction, 0, len(e.emergencyActions))
	for _, a := range e.emergencyActions {
		out = append(out, a.Clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ActionID < out[j].ActionID })
	return out
}

// IsSubsystemPaused reports whether targetSubsystem has a live, non-expired,
// non-lifted emergency safeguard active at the engine's current clock time.
func (e *Engine) IsSubsystemPaused(targetSubsystem string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.activeEmergencyLocked(targetSubsystem, e.nowFn().UTC()) != nil
}

// LedgerEntries returns a deep copy of all hash-chained audit ledger records.
func (e *Engine) LedgerEntries() []LedgerEntry {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.ledger.Entries()
}

// LedgerHeadHash returns the current SHA-256 head hash of the audit ledger.
func (e *Engine) LedgerHeadHash() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.ledger.HeadHash()
}

// LedgerJSONL returns the canonical newline-delimited JSON audit ledger.
func (e *Engine) LedgerJSONL() ([]byte, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.ledger.MarshalJSONL()
}

// Proposal returns a deep copy of a proposal with its live evaluated state.
func (e *Engine) Proposal(id string) (*Proposal, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	p, ok := e.proposals[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrProposalNotFound, id)
	}
	cp := p.Clone()
	cp.State = evaluateProposalStateLocked(p, e.nowFn().UTC())
	return &cp, nil
}

// Proposals returns a deterministically sorted list of all proposals with live state.
func (e *Engine) Proposals() []Proposal {
	e.mu.RLock()
	defer e.mu.RUnlock()
	now := e.nowFn().UTC()
	out := make([]Proposal, 0, len(e.proposals))
	for _, p := range e.proposals {
		cp := p.Clone()
		cp.State = evaluateProposalStateLocked(p, now)
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Delegate updates a member's voting power delegation and captures a new
// immutable checkpoint. Existing proposals remain bound to their original
// historical snapshots so delegation cannot manipulate active votes.
func (e *Engine) Delegate(req DelegationRequest) (PowerCheckpoint, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := validateNonce(req.Nonce); err != nil {
		return PowerCheckpoint{}, err
	}
	if req.IssuedAt.IsZero() {
		return PowerCheckpoint{}, fmt.Errorf("%w: issued_at is required", ErrInvalidSignature)
	}
	delegator, err := e.requireActiveMemberLocked(req.DelegatorID, req.Org)
	if err != nil {
		return PowerCheckpoint{}, err
	}
	if e.isNonceUsedLocked(req.DelegatorID, req.Nonce) {
		return PowerCheckpoint{}, ErrNonceReplay
	}
	if err := VerifyDelegationSignature(e.cfg.ProtocolID, req, delegator.PublicKeyHex); err != nil {
		return PowerCheckpoint{}, err
	}

	targetID := strings.TrimSpace(req.DelegateeID)
	if targetID != "" && targetID != req.DelegatorID {
		target, ok := e.members[targetID]
		if !ok || !target.Active {
			return PowerCheckpoint{}, fmt.Errorf("%w: delegatee %q", ErrUnknownMember, targetID)
		}
	}
	if targetID == req.DelegatorID {
		targetID = "" // self-delegation normalized to empty
	}

	now := e.nowFn().UTC()
	delegator.DelegateTo = targetID
	e.members[delegator.ID] = delegator

	snap, err := e.captureSnapshotLocked(now)
	if err != nil {
		return PowerCheckpoint{}, err
	}
	e.markNonceUsedLocked(req.DelegatorID, req.Nonce)

	if _, err := e.ledger.Append(now, e.cfg.ProtocolID, "delegation_updated", req.DelegatorID, targetID, map[string]any{
		"delegator_id": req.DelegatorID,
		"delegatee_id": targetID,
		"snapshot_id":  snap.SnapshotID,
		"nonce":        req.Nonce,
	}); err != nil {
		return PowerCheckpoint{}, err
	}
	return snap.Clone(), nil
}

// TransferVotingPower executes a signed voting power transfer between active
// members and captures a new checkpoint. Because open proposals read strictly
// from their creation-time SnapshotID, post-snapshot transfers cannot inflate
// voting weight on active proposals.
func (e *Engine) TransferVotingPower(req TransferPowerRequest) (PowerCheckpoint, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := validateNonce(req.Nonce); err != nil {
		return PowerCheckpoint{}, err
	}
	if req.IssuedAt.IsZero() {
		return PowerCheckpoint{}, fmt.Errorf("%w: issued_at is required", ErrInvalidSignature)
	}
	if req.Amount == 0 || req.FromMemberID == req.ToMemberID {
		return PowerCheckpoint{}, fmt.Errorf("%w: invalid transfer amount or recipient", ErrInvalidProposal)
	}
	sender, err := e.requireActiveMemberLocked(req.FromMemberID, req.Org)
	if err != nil {
		return PowerCheckpoint{}, err
	}
	receiver, ok := e.members[req.ToMemberID]
	if !ok || !receiver.Active {
		return PowerCheckpoint{}, fmt.Errorf("%w: recipient %q", ErrUnknownMember, req.ToMemberID)
	}
	if e.isNonceUsedLocked(req.FromMemberID, req.Nonce) {
		return PowerCheckpoint{}, ErrNonceReplay
	}
	if err := VerifyTransferPowerSignature(e.cfg.ProtocolID, req, sender.PublicKeyHex); err != nil {
		return PowerCheckpoint{}, err
	}
	if sender.VotingPower < req.Amount {
		return PowerCheckpoint{}, fmt.Errorf("%w: sender has %d, need %d", ErrInsufficientPower, sender.VotingPower, req.Amount)
	}
	if math.MaxUint64-receiver.VotingPower < req.Amount {
		return PowerCheckpoint{}, fmt.Errorf("%w: receiver voting power overflow", ErrInvalidProposal)
	}

	now := e.nowFn().UTC()
	sender.VotingPower -= req.Amount
	receiver.VotingPower += req.Amount
	e.members[sender.ID] = sender
	e.members[receiver.ID] = receiver

	snap, err := e.captureSnapshotLocked(now)
	if err != nil {
		return PowerCheckpoint{}, err
	}
	e.markNonceUsedLocked(req.FromMemberID, req.Nonce)

	if _, err := e.ledger.Append(now, e.cfg.ProtocolID, "voting_power_transferred", req.FromMemberID, req.ToMemberID, map[string]any{
		"from_member_id": req.FromMemberID,
		"to_member_id":   req.ToMemberID,
		"amount":         req.Amount,
		"snapshot_id":    snap.SnapshotID,
		"nonce":          req.Nonce,
	}); err != nil {
		return PowerCheckpoint{}, err
	}
	return snap.Clone(), nil
}

// SubmitProposal validates, authenticates, and creates a snapshot-locked
// governance proposal.
func (e *Engine) SubmitProposal(req ProposalRequest) (*Proposal, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if strings.TrimSpace(req.ProposalID) == "" || len(req.ProposalID) > 128 {
		return nil, fmt.Errorf("%w: proposal_id must be 1..128 characters", ErrInvalidProposal)
	}
	if _, exists := e.proposals[req.ProposalID]; exists {
		return nil, fmt.Errorf("%w: duplicate proposal_id %q", ErrInvalidProposal, req.ProposalID)
	}
	if strings.TrimSpace(req.Title) == "" || len(req.Title) > 256 {
		return nil, fmt.Errorf("%w: title must be 1..256 characters", ErrInvalidProposal)
	}
	if req.CreatedAt.IsZero() {
		return nil, fmt.Errorf("%w: created_at is required", ErrInvalidProposal)
	}
	if err := validateNonce(req.Nonce); err != nil {
		return nil, err
	}

	proposer, err := e.requireActiveMemberLocked(req.ProposerID, req.Org)
	if err != nil {
		return nil, err
	}
	if e.isNonceUsedLocked(req.ProposerID, req.Nonce) {
		return nil, ErrNonceReplay
	}
	if err := VerifyProposalSignature(e.cfg.ProtocolID, req, proposer.PublicKeyHex); err != nil {
		return nil, err
	}

	snap := e.snapshots[e.currentSnapshotID]
	proposerPower := snap.EffectivePower[req.ProposerID]
	if proposerPower < e.cfg.ProposalThreshold {
		return nil, fmt.Errorf("%w: proposer %q has %d delegated power, threshold is %d", ErrInsufficientPower, req.ProposerID, proposerPower, e.cfg.ProposalThreshold)
	}

	now := e.nowFn().UTC()
	if err := e.validatePayloadLocked(req.Kind, req.Payload, now, false); err != nil {
		return nil, err
	}

	payloadHash, err := HashPayload(req.Payload)
	if err != nil {
		return nil, err
	}

	quorumReq := computeQuorum(snap.TotalSupply, e.cfg.QuorumBps)
	votingStarts := now.Add(e.cfg.VotingDelay)
	votingEnds := votingStarts.Add(e.cfg.VotingPeriod)
	initialState := StatePending
	if e.cfg.VotingDelay == 0 {
		initialState = StateActive
	}

	prop := &Proposal{
		ID:               req.ProposalID,
		ProposerID:       req.ProposerID,
		Org:              req.Org,
		OriginProtocolID: e.cfg.ProtocolID,
		Kind:             req.Kind,
		Title:            req.Title,
		Description:      req.Description,
		Payload:          req.Payload.Clone(),
		PayloadHash:      payloadHash,
		SnapshotID:       snap.SnapshotID,
		SnapshotSupply:   snap.TotalSupply,
		QuorumRequired:   quorumReq,
		ApprovalBps:      e.cfg.ApprovalThresholdBps,
		CreatedAt:        req.CreatedAt.UTC(),
		VotingStartsAt:   votingStarts,
		VotingEndsAt:     votingEnds,
		State:            initialState,
		Votes:            make(map[string]VoteRecord),
		Nonce:            req.Nonce,
		Signature:        req.Signature,
	}

	e.proposals[prop.ID] = prop
	e.markNonceUsedLocked(req.ProposerID, req.Nonce)

	if _, err := e.ledger.Append(now, e.cfg.ProtocolID, "proposal_submitted", req.ProposerID, prop.ID, map[string]any{
		"proposal_id":     prop.ID,
		"kind":            prop.Kind,
		"title":           prop.Title,
		"payload_hash":    prop.PayloadHash,
		"snapshot_id":     prop.SnapshotID,
		"quorum_required": prop.QuorumRequired,
		"voting_starts":   prop.VotingStartsAt,
		"voting_ends":     prop.VotingEndsAt,
		"nonce":           req.Nonce,
	}); err != nil {
		delete(e.proposals, prop.ID)
		return nil, err
	}

	cp := prop.Clone()
	return &cp, nil
}

// CastVote verifies the voter's Ed25519 signature, binds voting weight strictly
// to the proposal's historical snapshot, rejects double votes, and appends the
// ballot to the audit ledger.
func (e *Engine) CastVote(req VoteRequest) (*VoteReceipt, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := validateNonce(req.Nonce); err != nil {
		return nil, err
	}
	if req.CastAt.IsZero() {
		return nil, fmt.Errorf("%w: cast_at is required", ErrInvalidSignature)
	}
	if req.Support != VoteFor && req.Support != VoteAgainst && req.Support != VoteAbstain {
		return nil, fmt.Errorf("%w: unsupported vote choice %q", ErrInvalidProposal, req.Support)
	}

	voter, err := e.requireActiveMemberLocked(req.VoterID, req.Org)
	if err != nil {
		return nil, err
	}
	if e.isNonceUsedLocked(req.VoterID, req.Nonce) {
		return nil, ErrNonceReplay
	}
	if err := VerifyVoteSignature(e.cfg.ProtocolID, req, voter.PublicKeyHex); err != nil {
		return nil, err
	}

	prop, ok := e.proposals[req.ProposalID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrProposalNotFound, req.ProposalID)
	}

	now := e.nowFn().UTC()
	state := evaluateProposalStateLocked(prop, now)
	if state != StateActive {
		return nil, fmt.Errorf("%w: proposal %q is %s, not active", ErrInvalidState, prop.ID, state)
	}
	if _, dup := prop.Votes[req.VoterID]; dup {
		return nil, fmt.Errorf("%w: member %q on proposal %q", ErrAlreadyVoted, req.VoterID, prop.ID)
	}

	snap, ok := e.snapshots[prop.SnapshotID]
	if !ok {
		return nil, fmt.Errorf("%w: missing snapshot %d", ErrInvalidState, prop.SnapshotID)
	}
	weight := snap.EffectivePower[req.VoterID]
	if weight == 0 {
		return nil, fmt.Errorf("%w: member %q has zero voting power at snapshot %d", ErrInsufficientPower, req.VoterID, prop.SnapshotID)
	}

	switch req.Support {
	case VoteFor:
		if math.MaxUint64-prop.ForVotes < weight {
			return nil, fmt.Errorf("%w: vote tally overflow", ErrInvalidState)
		}
		prop.ForVotes += weight
	case VoteAgainst:
		if math.MaxUint64-prop.AgainstVotes < weight {
			return nil, fmt.Errorf("%w: vote tally overflow", ErrInvalidState)
		}
		prop.AgainstVotes += weight
	case VoteAbstain:
		if math.MaxUint64-prop.AbstainVotes < weight {
			return nil, fmt.Errorf("%w: vote tally overflow", ErrInvalidState)
		}
		prop.AbstainVotes += weight
	}

	prop.State = StateActive
	prop.Votes[req.VoterID] = VoteRecord{
		ProposalID:       prop.ID,
		VoterID:          req.VoterID,
		Org:              req.Org,
		OriginProtocolID: e.cfg.ProtocolID,
		Support:          req.Support,
		Weight:           weight,
		Reason:           req.Reason,
		CastAt:           req.CastAt.UTC(),
		Nonce:            req.Nonce,
		Signature:        req.Signature,
	}
	e.markNonceUsedLocked(req.VoterID, req.Nonce)

	entry, err := e.ledger.Append(now, e.cfg.ProtocolID, "vote_cast", req.VoterID, prop.ID, map[string]any{
		"proposal_id": prop.ID,
		"voter_id":    req.VoterID,
		"support":     req.Support,
		"weight":      weight,
		"snapshot_id": prop.SnapshotID,
		"nonce":       req.Nonce,
	})
	if err != nil {
		return nil, err
	}

	return &VoteReceipt{
		ProposalID: prop.ID,
		VoterID:    req.VoterID,
		Support:    req.Support,
		Weight:     weight,
		SnapshotID: prop.SnapshotID,
		RecordedAt: now,
		LedgerSeq:  entry.Seq,
		LedgerHash: entry.EntryHash,
	}, nil
}

// QueueProposal enqueues a Succeeded proposal into the mandatory timelock.
// Any active member may crank a succeeded proposal into the timelock.
func (e *Engine) QueueProposal(req QueueRequest) (*Proposal, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := validateNonce(req.Nonce); err != nil {
		return nil, err
	}
	if req.QueuedAt.IsZero() {
		return nil, fmt.Errorf("%w: queued_at is required", ErrInvalidSignature)
	}
	caller, err := e.requireActiveMemberLocked(req.CallerID, req.Org)
	if err != nil {
		return nil, err
	}
	if e.isNonceUsedLocked(req.CallerID, req.Nonce) {
		return nil, ErrNonceReplay
	}
	if err := VerifyQueueSignature(e.cfg.ProtocolID, req, caller.PublicKeyHex); err != nil {
		return nil, err
	}

	prop, ok := e.proposals[req.ProposalID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrProposalNotFound, req.ProposalID)
	}
	now := e.nowFn().UTC()
	state := evaluateProposalStateLocked(prop, now)
	if state != StateSucceeded {
		return nil, fmt.Errorf("%w: proposal %q is %s, must be succeeded before queueing", ErrInvalidState, prop.ID, state)
	}

	queuedAt := now
	eta := now.Add(e.cfg.MinTimelockDelay)
	expiresAt := eta.Add(e.cfg.GracePeriod)
	prop.QueuedAt = &queuedAt
	prop.ETA = &eta
	prop.ExpiresAt = &expiresAt
	prop.State = StateQueued
	e.markNonceUsedLocked(req.CallerID, req.Nonce)

	if _, err := e.ledger.Append(now, e.cfg.ProtocolID, "proposal_queued", req.CallerID, prop.ID, map[string]any{
		"proposal_id": prop.ID,
		"queued_at":   queuedAt,
		"eta":         eta,
		"expires_at":  expiresAt,
		"nonce":       req.Nonce,
	}); err != nil {
		return nil, err
	}

	cp := prop.Clone()
	return &cp, nil
}

// ExecuteProposal executes a Queued proposal strictly after MinTimelockDelay
// has elapsed and before GracePeriod expires. Any active member may execute.
func (e *Engine) ExecuteProposal(req ExecuteRequest) (*ExecutionReceipt, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := validateNonce(req.Nonce); err != nil {
		return nil, err
	}
	if req.ExecutedAt.IsZero() {
		return nil, fmt.Errorf("%w: executed_at is required", ErrInvalidSignature)
	}
	executor, err := e.requireActiveMemberLocked(req.ExecutorID, req.Org)
	if err != nil {
		return nil, err
	}
	if e.isNonceUsedLocked(req.ExecutorID, req.Nonce) {
		return nil, ErrNonceReplay
	}
	if err := VerifyExecuteSignature(e.cfg.ProtocolID, req, executor.PublicKeyHex); err != nil {
		return nil, err
	}

	prop, ok := e.proposals[req.ProposalID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrProposalNotFound, req.ProposalID)
	}
	now := e.nowFn().UTC()
	if prop.State != StateQueued || prop.ETA == nil || prop.ExpiresAt == nil {
		state := evaluateProposalStateLocked(prop, now)
		return nil, fmt.Errorf("%w: proposal %q is %s, must be queued", ErrInvalidState, prop.ID, state)
	}
	if now.Before(*prop.ETA) {
		return nil, fmt.Errorf("%w: proposal %q ready at %s, current time %s", ErrTimelockNotReady, prop.ID, prop.ETA.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	}
	if now.After(*prop.ExpiresAt) {
		prop.State = StateExpired
		return nil, fmt.Errorf("%w: proposal %q expired at %s", ErrProposalExpired, prop.ID, prop.ExpiresAt.Format(time.RFC3339Nano))
	}

	// Check bounded emergency pause on target subsystem.
	// ProposalEmergencyRatification is NEVER blocked by a pause so the DAO can
	// always override or lift any emergency action.
	if targetSub := proposalTargetSubsystem(prop.Kind); targetSub != "" {
		if active := e.activeEmergencyLocked(targetSub, now); active != nil {
			return nil, fmt.Errorf("%w: subsystem %q paused by %q until %s", ErrSubsystemPaused, targetSub, active.ActionID, active.ExpiresAt.Format(time.RFC3339))
		}
	}

	// Re-validate payload invariants at execution time (defense in depth).
	if err := e.validatePayloadLocked(prop.Kind, prop.Payload, now, true); err != nil {
		return nil, err
	}

	resultObj, err := e.applyProposalLocked(prop, req.ExecutorID, now)
	if err != nil {
		return nil, err
	}

	resultRaw, err := json.Marshal(resultObj)
	if err != nil {
		return nil, fmt.Errorf("marshal execution result: %w", err)
	}
	resSum := sha256.Sum256(resultRaw)
	resultHash := hex.EncodeToString(resSum[:])

	executedAt := now
	prop.ExecutedAt = &executedAt
	prop.State = StateExecuted
	e.markNonceUsedLocked(req.ExecutorID, req.Nonce)

	entry, err := e.ledger.Append(now, e.cfg.ProtocolID, "proposal_executed", req.ExecutorID, prop.ID, map[string]any{
		"proposal_id":  prop.ID,
		"kind":         prop.Kind,
		"payload_hash": prop.PayloadHash,
		"result_hash":  resultHash,
		"result":       resultObj,
		"nonce":        req.Nonce,
	})
	if err != nil {
		return nil, err
	}

	// Stamp ledger seq onto the latest treasury disbursement if applicable.
	if prop.Kind == ProposalTreasuryDisbursement && prop.Payload.TreasuryDisbursement.AmountMicroUSD > 0 {
		if n := len(e.treasury.Disbursements); n > 0 && e.treasury.Disbursements[n-1].ProposalID == prop.ID {
			e.treasury.Disbursements[n-1].LedgerSeq = entry.Seq
		}
	}

	receipt := &ExecutionReceipt{
		ProposalID:  prop.ID,
		ExecutorID:  req.ExecutorID,
		ExecutedAt:  executedAt,
		PayloadHash: prop.PayloadHash,
		ResultHash:  resultHash,
		LedgerSeq:   entry.Seq,
		LedgerHash:  entry.EntryHash,
	}
	prop.ExecutionReceipt = receipt
	cp := *receipt
	return &cp, nil
}

// CancelProposal allows ONLY the original proposer (while Pending/Active) OR
// any active member if the proposer's live voting power has dropped below
// ProposalThreshold to cancel an unexecuted proposal (OpenZeppelin Governor rule).
func (e *Engine) CancelProposal(req CancelRequest) (*Proposal, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := validateNonce(req.Nonce); err != nil {
		return nil, err
	}
	if req.CanceledAt.IsZero() {
		return nil, fmt.Errorf("%w: canceled_at is required", ErrInvalidSignature)
	}
	caller, err := e.requireActiveMemberLocked(req.CallerID, req.Org)
	if err != nil {
		return nil, err
	}
	if e.isNonceUsedLocked(req.CallerID, req.Nonce) {
		return nil, ErrNonceReplay
	}
	if err := VerifyCancelSignature(e.cfg.ProtocolID, req, caller.PublicKeyHex); err != nil {
		return nil, err
	}

	prop, ok := e.proposals[req.ProposalID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrProposalNotFound, req.ProposalID)
	}
	now := e.nowFn().UTC()
	state := evaluateProposalStateLocked(prop, now)
	if state == StateExecuted || state == StateCanceled || state == StateExpired || state == StateDefeated {
		return nil, fmt.Errorf("%w: cannot cancel proposal %q in state %s", ErrInvalidState, prop.ID, state)
	}

	// Authorization rule: proposer can cancel while Pending/Active; anyone can
	// cancel if proposer's current delegated voting power fell below threshold.
	// No Hanzo admin override exists!
	currentSnap := e.snapshots[e.currentSnapshotID]
	proposerCurrentPower := currentSnap.EffectivePower[prop.ProposerID]
	canCancel := (req.CallerID == prop.ProposerID && (state == StatePending || state == StateActive)) ||
		(proposerCurrentPower < e.cfg.ProposalThreshold)
	if !canCancel {
		return nil, fmt.Errorf("%w: caller %q cannot cancel active/queued proposal of %q (current power %d >= threshold %d)",
			ErrUnauthorizedCancel, req.CallerID, prop.ProposerID, proposerCurrentPower, e.cfg.ProposalThreshold)
	}

	canceledAt := now
	prop.CanceledAt = &canceledAt
	prop.CancelReason = req.Reason
	prop.State = StateCanceled
	e.markNonceUsedLocked(req.CallerID, req.Nonce)

	if _, err := e.ledger.Append(now, e.cfg.ProtocolID, "proposal_canceled", req.CallerID, prop.ID, map[string]any{
		"proposal_id": prop.ID,
		"reason":      req.Reason,
		"nonce":       req.Nonce,
	}); err != nil {
		return nil, err
	}

	cp := prop.Clone()
	return &cp, nil
}

// TriggerEmergency invokes a narrow, time-bounded emergency pause or circuit
// breaker by an authorized Emergency Guardian.
//
// Strictly forbidden: draining treasury, modifying quorum/timelock, pausing
// governance/voting/forking, exceeding MaxEmergencyDuration, or chaining pauses
// inside EmergencyCooldown.
func (e *Engine) TriggerEmergency(req EmergencyActionRequest) (*EmergencyAction, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if strings.TrimSpace(req.ActionID) == "" || len(req.ActionID) > 128 {
		return nil, fmt.Errorf("%w: action_id must be 1..128 characters", ErrEmergencyAbuse)
	}
	if _, exists := e.emergencyActions[req.ActionID]; exists {
		return nil, fmt.Errorf("%w: duplicate action_id %q", ErrEmergencyAbuse, req.ActionID)
	}
	if err := validateNonce(req.Nonce); err != nil {
		return nil, err
	}
	if req.IssuedAt.IsZero() {
		return nil, fmt.Errorf("%w: issued_at is required", ErrInvalidSignature)
	}

	guardian, err := e.requireActiveMemberLocked(req.GuardianID, req.Org)
	if err != nil {
		return nil, err
	}
	if !guardian.IsEmergencyGuardian {
		return nil, fmt.Errorf("%w: member %q is not an authorized emergency guardian", ErrEmergencyAbuse, req.GuardianID)
	}
	if e.isNonceUsedLocked(req.GuardianID, req.Nonce) {
		return nil, ErrNonceReplay
	}
	if err := VerifyEmergencySignature(e.cfg.ProtocolID, req, guardian.PublicKeyHex); err != nil {
		return nil, err
	}

	if req.Kind != EmergencyPauseSubsystem && req.Kind != EmergencyCircuitBreaker {
		return nil, fmt.Errorf("%w: emergency action kind %q is forbidden (only pause_subsystem or circuit_breaker allowed)", ErrEmergencyAbuse, req.Kind)
	}
	target := strings.ToLower(strings.TrimSpace(req.TargetSubsystem))
	if forbiddenEmergencyTargets[target] {
		return nil, fmt.Errorf("%w: target subsystem %q cannot be paused by emergency guardian", ErrEmergencyAbuse, req.TargetSubsystem)
	}
	if req.Duration <= 0 || req.Duration > e.cfg.MaxEmergencyDuration || req.Duration > MaxAllowedEmergencyDuration {
		return nil, fmt.Errorf("%w: duration %s violates bounded emergency ceiling %s", ErrEmergencyAbuse, req.Duration, e.cfg.MaxEmergencyDuration)
	}
	if strings.TrimSpace(req.Reason) == "" {
		return nil, fmt.Errorf("%w: emergency reason is required", ErrEmergencyAbuse)
	}

	now := e.nowFn().UTC()

	// Reject overlapping pause or rolling re-pause within cooldown window.
	for _, existing := range e.emergencyActions {
		if existing.TargetSubsystem != target {
			continue
		}
		if existing.ActiveAt(now) {
			return nil, fmt.Errorf("%w: subsystem %q already has active emergency action %q", ErrEmergencyAbuse, target, existing.ActionID)
		}
		endedAt := existing.ExpiresAt
		if existing.LiftedAt != nil && existing.LiftedAt.Before(endedAt) {
			endedAt = *existing.LiftedAt
		}
		if !existing.Ratified && now.Sub(endedAt) < e.cfg.EmergencyCooldown {
			return nil, fmt.Errorf("%w: subsystem %q is in cooldown until %s", ErrEmergencyCooldown, target, endedAt.Add(e.cfg.EmergencyCooldown).Format(time.RFC3339))
		}
	}

	action := &EmergencyAction{
		ActionID:         req.ActionID,
		GuardianID:       req.GuardianID,
		Org:              req.Org,
		OriginProtocolID: e.cfg.ProtocolID,
		Kind:             req.Kind,
		TargetSubsystem:  target,
		Duration:         req.Duration,
		Reason:           req.Reason,
		IssuedAt:         req.IssuedAt.UTC(),
		ExpiresAt:        now.Add(req.Duration),
		Ratified:         false,
		Nonce:            req.Nonce,
		Signature:        req.Signature,
	}
	e.emergencyActions[action.ActionID] = action
	e.markNonceUsedLocked(req.GuardianID, req.Nonce)

	if _, err := e.ledger.Append(now, e.cfg.ProtocolID, "emergency_triggered", req.GuardianID, action.ActionID, map[string]any{
		"action_id":        action.ActionID,
		"kind":             action.Kind,
		"target_subsystem": action.TargetSubsystem,
		"duration_ms":      action.Duration.Milliseconds(),
		"expires_at":       action.ExpiresAt,
		"reason":           action.Reason,
		"nonce":            req.Nonce,
	}); err != nil {
		delete(e.emergencyActions, action.ActionID)
		return nil, err
	}

	cp := action.Clone()
	return &cp, nil
}

// ── internal helpers ─────────────────────────────────────────────────────────

func (e *Engine) requireActiveMemberLocked(id, org string) (Member, error) {
	m, ok := e.members[id]
	if !ok || !m.Active {
		return Member{}, fmt.Errorf("%w: %q", ErrUnknownMember, id)
	}
	if m.Org != "" && org != m.Org {
		return Member{}, fmt.Errorf("%w: org mismatch for member %q", ErrUnknownMember, id)
	}
	return m, nil
}

func (e *Engine) isNonceUsedLocked(memberID, nonce string) bool {
	if set, ok := e.usedNonces[memberID]; ok {
		return set[nonce]
	}
	return false
}

func (e *Engine) markNonceUsedLocked(memberID, nonce string) {
	set, ok := e.usedNonces[memberID]
	if !ok {
		set = make(map[string]bool)
		e.usedNonces[memberID] = set
	}
	set[nonce] = true
}

func validateNonce(nonce string) error {
	if strings.TrimSpace(nonce) == "" || len(nonce) > 128 {
		return fmt.Errorf("%w: nonce must be 1..128 non-empty characters", ErrInvalidSignature)
	}
	return nil
}

func (e *Engine) captureSnapshotLocked(ts time.Time) (PowerCheckpoint, error) {
	effective := make(map[string]uint64, len(e.members))
	delegations := make(map[string]string, len(e.members))
	var totalSupply uint64

	for _, m := range e.members {
		if !m.Active {
			continue
		}
		if math.MaxUint64-totalSupply < m.VotingPower {
			return PowerCheckpoint{}, fmt.Errorf("%w: total voting power overflow", ErrInvalidConfig)
		}
		totalSupply += m.VotingPower

		target := m.ID
		if m.DelegateTo != "" {
			if d, ok := e.members[m.DelegateTo]; ok && d.Active {
				target = m.DelegateTo
				delegations[m.ID] = m.DelegateTo
			}
		}
		if math.MaxUint64-effective[target] < m.VotingPower {
			return PowerCheckpoint{}, fmt.Errorf("%w: delegated power overflow for %q", ErrInvalidConfig, target)
		}
		effective[target] += m.VotingPower
	}

	nextID := e.currentSnapshotID + 1
	root := HashSnapshotStateRoot(nextID, ts, totalSupply, effective, delegations)
	snap := PowerCheckpoint{
		SnapshotID:     nextID,
		Timestamp:      ts.UTC(),
		TotalSupply:    totalSupply,
		EffectivePower: effective,
		Delegations:    delegations,
		StateRoot:      root,
	}
	e.snapshots[nextID] = snap
	e.currentSnapshotID = nextID
	return snap, nil
}

func evaluateProposalStateLocked(p *Proposal, at time.Time) ProposalState {
	switch p.State {
	case StateExecuted, StateCanceled, StateExpired:
		return p.State
	case StateQueued:
		if p.ExpiresAt != nil && at.After(*p.ExpiresAt) {
			return StateExpired
		}
		return StateQueued
	}
	if at.Before(p.VotingStartsAt) {
		return StatePending
	}
	if !at.After(p.VotingEndsAt) {
		return StateActive
	}
	// Voting window closed: evaluate quorum (For + Abstain) and approval (For / (For + Against)).
	if !meetsQuorum(p.ForVotes, p.AbstainVotes, p.QuorumRequired) {
		return StateDefeated
	}
	if !meetsApproval(p.ForVotes, p.AgainstVotes, p.ApprovalBps) {
		return StateDefeated
	}
	return StateSucceeded
}

// computeQuorum returns ceil(totalSupply * quorumBps / 10000) without overflow.
func computeQuorum(totalSupply, quorumBps uint64) uint64 {
	num := new(big.Int).Mul(new(big.Int).SetUint64(totalSupply), new(big.Int).SetUint64(quorumBps))
	num.Add(num, big.NewInt(9999))
	num.Div(num, big.NewInt(10000))
	if !num.IsUint64() {
		return math.MaxUint64
	}
	return num.Uint64()
}

func meetsQuorum(forVotes, abstainVotes, required uint64) bool {
	sum := new(big.Int).Add(new(big.Int).SetUint64(forVotes), new(big.Int).SetUint64(abstainVotes))
	return sum.Cmp(new(big.Int).SetUint64(required)) >= 0
}

func meetsApproval(forVotes, againstVotes, approvalBps uint64) bool {
	if forVotes == 0 {
		return false
	}
	lhs := new(big.Int).Mul(new(big.Int).SetUint64(forVotes), big.NewInt(10000))
	total := new(big.Int).Add(new(big.Int).SetUint64(forVotes), new(big.Int).SetUint64(againstVotes))
	rhs := new(big.Int).Mul(total, new(big.Int).SetUint64(approvalBps))
	return lhs.Cmp(rhs) >= 0
}

func proposalTargetSubsystem(kind ProposalKind) string {
	switch kind {
	case ProposalTreasuryDisbursement:
		return "treasury_disbursements"
	case ProposalModelRegistry:
		return "model_registry"
	case ProposalVerifierStandard:
		return "verifier_standards"
	case ProposalParameterUpdate:
		return "parameter_updates"
	case ProposalProtocolUpgrade:
		return "protocol_upgrades"
	case ProposalEmergencyRatification:
		return "" // DAO ratification can NEVER be paused.
	default:
		return ""
	}
}

func (e *Engine) activeEmergencyLocked(targetSubsystem string, now time.Time) *EmergencyAction {
	target := strings.ToLower(strings.TrimSpace(targetSubsystem))
	for _, a := range e.emergencyActions {
		if a.TargetSubsystem == target && a.ActiveAt(now) {
			return a
		}
	}
	return nil
}

func (e *Engine) epochIndexLocked(now time.Time) uint64 {
	if now.Before(e.genesisTime) {
		return 0
	}
	return uint64(now.Sub(e.genesisTime) / e.cfg.TreasuryEpochDuration)
}

func (e *Engine) validatePayloadLocked(kind ProposalKind, payload ProposalPayload, now time.Time, checkLiveBalances bool) error {
	// Ensure exactly one payload arm is populated and matches kind.
	populated := 0
	if payload.ParameterUpdate != nil {
		populated++
	}
	if payload.ModelRegistry != nil {
		populated++
	}
	if payload.VerifierStandard != nil {
		populated++
	}
	if payload.TreasuryDisbursement != nil {
		populated++
	}
	if payload.EmergencyRatification != nil {
		populated++
	}
	if payload.ProtocolUpgrade != nil {
		populated++
	}
	if populated != 1 {
		return fmt.Errorf("%w: proposal payload must set exactly one typed arm, got %d", ErrInvalidProposal, populated)
	}

	switch kind {
	case ProposalParameterUpdate:
		if payload.ParameterUpdate == nil {
			return fmt.Errorf("%w: missing parameter_update payload", ErrInvalidProposal)
		}
		return e.validateParameterUpdateLocked(payload.ParameterUpdate)

	case ProposalModelRegistry:
		if payload.ModelRegistry == nil {
			return fmt.Errorf("%w: missing model_registry payload", ErrInvalidProposal)
		}
		m := payload.ModelRegistry
		if strings.TrimSpace(m.ModelID) == "" || strings.TrimSpace(m.Version) == "" {
			return fmt.Errorf("%w: model_id and version are required", ErrInvalidProposal)
		}
		if len(m.DigestSHA256) != 64 {
			return fmt.Errorf("%w: digest_sha256 must be 64 hex chars", ErrInvalidProposal)
		}
		if _, err := hex.DecodeString(m.DigestSHA256); err != nil {
			return fmt.Errorf("%w: digest_sha256 invalid hex", ErrInvalidProposal)
		}
		if strings.TrimSpace(m.LicenseSPDX) == "" {
			return fmt.Errorf("%w: license_spdx is required for open model registry", ErrInvalidProposal)
		}
		if strings.TrimSpace(m.VerifierProfileID) == "" {
			return fmt.Errorf("%w: verifier_profile_id is required", ErrInvalidProposal)
		}
		if len(e.verifierStandards) > 0 {
			if _, ok := e.verifierStandards[m.VerifierProfileID]; !ok {
				return fmt.Errorf("%w: unknown verifier_profile_id %q", ErrInvalidProposal, m.VerifierProfileID)
			}
		}
		return nil

	case ProposalVerifierStandard:
		if payload.VerifierStandard == nil {
			return fmt.Errorf("%w: missing verifier_standard payload", ErrInvalidProposal)
		}
		v := payload.VerifierStandard
		if strings.TrimSpace(v.ProfileID) == "" {
			return fmt.Errorf("%w: profile_id is required", ErrInvalidProposal)
		}
		switch v.Method {
		case VerifyCryptographicProof, VerifyHardwareAttestation, VerifyEconomicIncentive, VerifyHybridDeterministic:
		default:
			return fmt.Errorf("%w: invalid verification method %q", ErrInvalidProposal, v.Method)
		}
		if v.MinVerifiers == 0 {
			return fmt.Errorf("%w: min_verifiers must be >= 1", ErrInvalidProposal)
		}
		if v.QuorumThresholdBps < 5001 || v.QuorumThresholdBps > 10000 {
			return fmt.Errorf("%w: verifier quorum_threshold_bps must be in [5001, 10000]", ErrInvalidProposal)
		}
		if v.ChallengeWindowSec == 0 {
			return fmt.Errorf("%w: challenge_window_sec must be positive", ErrInvalidProposal)
		}
		return nil

	case ProposalTreasuryDisbursement:
		if payload.TreasuryDisbursement == nil {
			return fmt.Errorf("%w: missing treasury_disbursement payload", ErrInvalidProposal)
		}
		td := payload.TreasuryDisbursement
		if strings.TrimSpace(td.Recipient) == "" {
			return fmt.Errorf("%w: recipient is required", ErrTreasuryPolicy)
		}
		if strings.TrimSpace(td.Memo) == "" {
			return fmt.Errorf("%w: disbursement memo is required", ErrTreasuryPolicy)
		}
		if td.AllowlistAdd {
			if td.AmountMicroUSD != 0 {
				return fmt.Errorf("%w: allowlist_add proposals must carry amount_micro_usd == 0 (separate allowlist from payout)", ErrTreasuryPolicy)
			}
			return nil
		}
		if td.AmountMicroUSD == 0 {
			return fmt.Errorf("%w: amount_micro_usd must be positive", ErrTreasuryPolicy)
		}
		if !e.cfg.AllowlistedRecipients[td.Recipient] {
			return fmt.Errorf("%w: recipient %q is not allowlisted", ErrTreasuryPolicy, td.Recipient)
		}
		if e.cfg.MaxSingleDisbursementMicroUSD > 0 && td.AmountMicroUSD > e.cfg.MaxSingleDisbursementMicroUSD {
			return fmt.Errorf("%w: amount %d exceeds max_single_disbursement_micro_usd %d", ErrTreasuryPolicy, td.AmountMicroUSD, e.cfg.MaxSingleDisbursementMicroUSD)
		}
		if e.cfg.TreasuryEpochCapMicroUSD > 0 && td.AmountMicroUSD > e.cfg.TreasuryEpochCapMicroUSD {
			return fmt.Errorf("%w: amount %d exceeds treasury_epoch_cap_micro_usd %d", ErrTreasuryPolicy, td.AmountMicroUSD, e.cfg.TreasuryEpochCapMicroUSD)
		}
		if checkLiveBalances {
			if td.AmountMicroUSD > e.treasury.BalanceMicroUSD {
				return fmt.Errorf("%w: amount %d exceeds available treasury balance %d", ErrTreasuryPolicy, td.AmountMicroUSD, e.treasury.BalanceMicroUSD)
			}
			epoch := e.epochIndexLocked(now)
			spent := e.treasury.EpochSpent[epoch]
			if math.MaxUint64-spent < td.AmountMicroUSD {
				return fmt.Errorf("%w: epoch spend overflow", ErrTreasuryPolicy)
			}
			if e.cfg.TreasuryEpochCapMicroUSD > 0 && spent+td.AmountMicroUSD > e.cfg.TreasuryEpochCapMicroUSD {
				return fmt.Errorf("%w: epoch %d spend %d + %d exceeds cap %d", ErrTreasuryPolicy, epoch, spent, td.AmountMicroUSD, e.cfg.TreasuryEpochCapMicroUSD)
			}
		}
		return nil

	case ProposalEmergencyRatification:
		if payload.EmergencyRatification == nil {
			return fmt.Errorf("%w: missing emergency_ratification payload", ErrInvalidProposal)
		}
		er := payload.EmergencyRatification
		if strings.TrimSpace(er.ActionID) == "" {
			return fmt.Errorf("%w: action_id is required", ErrInvalidProposal)
		}
		if _, ok := e.emergencyActions[er.ActionID]; !ok {
			return fmt.Errorf("%w: unknown emergency action %q", ErrInvalidProposal, er.ActionID)
		}
		if !er.Lift {
			if er.ExtendDuration <= 0 || er.ExtendDuration > MaxAllowedEmergencyDuration {
				return fmt.Errorf("%w: ratified extension must be in (0, %s]", ErrProtocolInvariant, MaxAllowedEmergencyDuration)
			}
		}
		return nil

	case ProposalProtocolUpgrade:
		if payload.ProtocolUpgrade == nil {
			return fmt.Errorf("%w: missing protocol_upgrade payload", ErrInvalidProposal)
		}
		u := payload.ProtocolUpgrade
		if strings.TrimSpace(u.TargetVersion) == "" {
			return fmt.Errorf("%w: target_version is required", ErrInvalidProposal)
		}
		if len(u.CompatibilityHash) != 64 {
			return fmt.Errorf("%w: compatibility_hash must be 64 hex chars", ErrInvalidProposal)
		}
		if _, err := hex.DecodeString(u.CompatibilityHash); err != nil {
			return fmt.Errorf("%w: compatibility_hash invalid hex", ErrInvalidProposal)
		}
		return nil

	default:
		return fmt.Errorf("%w: unknown proposal kind %q", ErrInvalidProposal, kind)
	}
}

func (e *Engine) validateParameterUpdateLocked(pu *ParameterUpdatePayload) error {
	key := strings.TrimSpace(pu.Key)
	if key == "" {
		return fmt.Errorf("%w: parameter key is required", ErrInvalidProposal)
	}
	lower := strings.ToLower(key)
	if forbiddenAdminParamKeys[lower] {
		return fmt.Errorf("%w: parameter %q cannot be modified", ErrAdminOverrideForbidden, key)
	}

	// Built-in governance config parameters subject to immutable protocol floors.
	switch lower {
	case "quorum_bps":
		v, err := strconv.ParseUint(pu.Value, 10, 64)
		if err != nil {
			return fmt.Errorf("%w: quorum_bps must be uint64", ErrInvalidProposal)
		}
		if v < e.cfg.MinQuorumBpsFloor || v > 10000 {
			return fmt.Errorf("%w: quorum_bps %d violates floor [%d, 10000]", ErrProtocolInvariant, v, e.cfg.MinQuorumBpsFloor)
		}
		return nil
	case "approval_threshold_bps":
		v, err := strconv.ParseUint(pu.Value, 10, 64)
		if err != nil {
			return fmt.Errorf("%w: approval_threshold_bps must be uint64", ErrInvalidProposal)
		}
		if v < 5001 || v > 10000 {
			return fmt.Errorf("%w: approval_threshold_bps %d must be in [5001, 10000]", ErrProtocolInvariant, v)
		}
		return nil
	case "min_timelock_delay_ms":
		v, err := strconv.ParseInt(pu.Value, 10, 64)
		if err != nil || v <= 0 {
			return fmt.Errorf("%w: min_timelock_delay_ms must be positive int64", ErrProtocolInvariant)
		}
		d := time.Duration(v) * time.Millisecond
		if d < e.cfg.MinTimelockFloor {
			return fmt.Errorf("%w: min_timelock_delay %s below immutable floor %s", ErrProtocolInvariant, d, e.cfg.MinTimelockFloor)
		}
		return nil
	case "max_emergency_duration_ms":
		v, err := strconv.ParseInt(pu.Value, 10, 64)
		if err != nil || v <= 0 {
			return fmt.Errorf("%w: max_emergency_duration_ms must be positive int64", ErrProtocolInvariant)
		}
		d := time.Duration(v) * time.Millisecond
		if d > MaxAllowedEmergencyDuration {
			return fmt.Errorf("%w: max_emergency_duration %s exceeds hard cap %s", ErrProtocolInvariant, d, MaxAllowedEmergencyDuration)
		}
		return nil
	case "emergency_cooldown_ms":
		v, err := strconv.ParseInt(pu.Value, 10, 64)
		if err != nil || v <= 0 {
			return fmt.Errorf("%w: emergency_cooldown_ms must be positive int64", ErrProtocolInvariant)
		}
		return nil
	}

	param, ok := e.parameters[key]
	if !ok {
		return fmt.Errorf("%w: unknown parameter %q (must be registered in parameter schema)", ErrInvalidProposal, key)
	}
	if param.Immutable {
		return fmt.Errorf("%w: parameter %q is immutable", ErrProtocolInvariant, key)
	}
	return validateParameterValue(param, pu.Value)
}

func validateParameterValue(param ParameterEntry, val string) error {
	switch param.ValueType {
	case "uint64":
		v, err := strconv.ParseUint(val, 10, 64)
		if err != nil {
			return fmt.Errorf("%w: %q must be uint64", ErrInvalidProposal, param.Key)
		}
		return checkNumericBounds(param, float64(v))
	case "int64":
		v, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return fmt.Errorf("%w: %q must be int64", ErrInvalidProposal, param.Key)
		}
		return checkNumericBounds(param, float64(v))
	case "float64":
		v, err := strconv.ParseFloat(val, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("%w: %q must be finite float64", ErrInvalidProposal, param.Key)
		}
		return checkNumericBounds(param, v)
	case "bool":
		if val != "true" && val != "false" {
			return fmt.Errorf("%w: %q must be true or false", ErrInvalidProposal, param.Key)
		}
		return nil
	case "string":
		if len(val) > 1024 {
			return fmt.Errorf("%w: %q exceeds 1024 bytes", ErrInvalidProposal, param.Key)
		}
		return nil
	default:
		return fmt.Errorf("%w: unsupported parameter type %q", ErrInvalidProposal, param.ValueType)
	}
}

func checkNumericBounds(param ParameterEntry, v float64) error {
	if param.MinNumeric != nil && v < *param.MinNumeric {
		return fmt.Errorf("%w: %q value %v below min %v", ErrInvalidProposal, param.Key, v, *param.MinNumeric)
	}
	if param.MaxNumeric != nil && v > *param.MaxNumeric {
		return fmt.Errorf("%w: %q value %v above max %v", ErrInvalidProposal, param.Key, v, *param.MaxNumeric)
	}
	return nil
}

func (e *Engine) applyProposalLocked(prop *Proposal, executorID string, now time.Time) (map[string]any, error) {
	switch prop.Kind {
	case ProposalParameterUpdate:
		pu := prop.Payload.ParameterUpdate
		lower := strings.ToLower(strings.TrimSpace(pu.Key))
		switch lower {
		case "quorum_bps":
			v, _ := strconv.ParseUint(pu.Value, 10, 64)
			e.cfg.QuorumBps = v
		case "approval_threshold_bps":
			v, _ := strconv.ParseUint(pu.Value, 10, 64)
			e.cfg.ApprovalThresholdBps = v
		case "min_timelock_delay_ms":
			v, _ := strconv.ParseInt(pu.Value, 10, 64)
			e.cfg.MinTimelockDelay = time.Duration(v) * time.Millisecond
		case "max_emergency_duration_ms":
			v, _ := strconv.ParseInt(pu.Value, 10, 64)
			e.cfg.MaxEmergencyDuration = time.Duration(v) * time.Millisecond
		case "emergency_cooldown_ms":
			v, _ := strconv.ParseInt(pu.Value, 10, 64)
			e.cfg.EmergencyCooldown = time.Duration(v) * time.Millisecond
		default:
			entry := e.parameters[pu.Key]
			oldVal := entry.Value
			entry.Value = pu.Value
			entry.UpdatedAt = now
			entry.UpdatedBy = prop.ID
			e.parameters[pu.Key] = entry
			return map[string]any{
				"key":       pu.Key,
				"old_value": oldVal,
				"new_value": pu.Value,
			}, nil
		}
		return map[string]any{
			"config_key": lower,
			"new_value":  pu.Value,
		}, nil

	case ProposalModelRegistry:
		mr := prop.Payload.ModelRegistry
		entry := ModelRegistryEntry{
			ModelID:           mr.ModelID,
			Version:           mr.Version,
			DigestSHA256:      mr.DigestSHA256,
			LicenseSPDX:       mr.LicenseSPDX,
			VerifierProfileID: mr.VerifierProfileID,
			Active:            mr.Active,
			UpdatedAt:         now,
			ProposalID:        prop.ID,
		}
		e.models[mr.ModelID] = entry
		return map[string]any{
			"model_id":     entry.ModelID,
			"version":      entry.Version,
			"license_spdx": entry.LicenseSPDX,
			"active":       entry.Active,
		}, nil

	case ProposalVerifierStandard:
		vs := prop.Payload.VerifierStandard
		entry := VerifierStandardEntry{
			ProfileID:          vs.ProfileID,
			Method:             vs.Method,
			MinVerifiers:       vs.MinVerifiers,
			QuorumThresholdBps: vs.QuorumThresholdBps,
			ChallengeWindowSec: vs.ChallengeWindowSec,
			MaxLatencyMs:       vs.MaxLatencyMs,
			Description:        vs.Description,
			UpdatedAt:          now,
			ProposalID:         prop.ID,
		}
		e.verifierStandards[vs.ProfileID] = entry
		return map[string]any{
			"profile_id":    entry.ProfileID,
			"method":        entry.Method,
			"min_verifiers": entry.MinVerifiers,
		}, nil

	case ProposalTreasuryDisbursement:
		td := prop.Payload.TreasuryDisbursement
		if td.AllowlistAdd {
			if e.cfg.AllowlistedRecipients == nil {
				e.cfg.AllowlistedRecipients = map[string]bool{}
			}
			e.cfg.AllowlistedRecipients[td.Recipient] = true
			return map[string]any{
				"recipient":  td.Recipient,
				"allowlist":  true,
				"memo":       td.Memo,
			}, nil
		}
		epoch := e.epochIndexLocked(now)
		e.treasury.BalanceMicroUSD -= td.AmountMicroUSD
		e.treasury.EpochSpent[epoch] += td.AmountMicroUSD
		rec := TreasuryDisbursementRecord{
			ProposalID:     prop.ID,
			Recipient:      td.Recipient,
			AmountMicroUSD: td.AmountMicroUSD,
			Memo:           td.Memo,
			EpochIndex:     epoch,
			ExecutedAt:     now,
			ExecutorID:     executorID,
		}
		e.treasury.Disbursements = append(e.treasury.Disbursements, rec)
		return map[string]any{
			"recipient":         rec.Recipient,
			"amount_micro_usd":  rec.AmountMicroUSD,
			"epoch_index":       rec.EpochIndex,
			"remaining_balance": e.treasury.BalanceMicroUSD,
		}, nil

	case ProposalEmergencyRatification:
		er := prop.Payload.EmergencyRatification
		action := e.emergencyActions[er.ActionID]
		if er.Lift {
			lifted := now
			action.LiftedAt = &lifted
			action.Ratified = true
			action.RatifiedByProp = prop.ID
			return map[string]any{
				"action_id": action.ActionID,
				"lifted_at": lifted,
			}, nil
		}
		action.Ratified = true
		action.RatifiedByProp = prop.ID
		action.ExpiresAt = now.Add(er.ExtendDuration)
		return map[string]any{
			"action_id":      action.ActionID,
			"ratified":       true,
			"new_expires_at": action.ExpiresAt,
		}, nil

	case ProposalProtocolUpgrade:
		u := prop.Payload.ProtocolUpgrade
		rec := ProtocolUpgradeRecord{
			TargetVersion:     u.TargetVersion,
			CompatibilityHash: u.CompatibilityHash,
			ActivationEpoch:   u.ActivationEpoch,
			ReleaseNotesURL:   u.ReleaseNotesURL,
			ProposalID:        prop.ID,
			ActivatedAt:       now,
		}
		e.cfg.NetworkVersion = u.TargetVersion
		e.upgrades = append(e.upgrades, rec)
		return map[string]any{
			"target_version":     rec.TargetVersion,
			"compatibility_hash": rec.CompatibilityHash,
			"activation_epoch":   rec.ActivationEpoch,
		}, nil
	}
	return nil, fmt.Errorf("%w: unsupported proposal kind %q", ErrInvalidProposal, prop.Kind)
}

// snapshotSortedNonces returns a deterministic deep copy of usedNonces.
func (e *Engine) snapshotSortedNoncesLocked() map[string][]string {
	out := make(map[string][]string, len(e.usedNonces))
	for memberID, set := range e.usedNonces {
		list := make([]string, 0, len(set))
		for n := range set {
			list = append(list, n)
		}
		sort.Strings(list)
		out[memberID] = list
	}
	return out
}

// Upgrades returns a copy of scheduled/executed protocol upgrades.
func (e *Engine) Upgrades() []ProtocolUpgradeRecord {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return slices.Clone(e.upgrades)
}

// ParentLineage returns parent protocol ID and parent bundle hash if this engine
// was created by forking a SovereignBundle.
func (e *Engine) ParentLineage() (parentProtocolID, parentBundleHash string) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.parentProtocolID, e.parentBundleHash
}

// copyMapHelper ensures map cloning stays safe for external callers.
var _ = maps.Clone[map[string]bool]
