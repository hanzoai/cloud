// SPDX-License-Identifier: Apache-2.0 OR MIT

package governance

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ImportOption configures sovereign bundle import or community fork creation.
type ImportOption func(*importConfig)

type importConfig struct {
	forkProtocolID string
	clockFn        func() time.Time
	ledgerPath     string
}

// WithForkProtocolID creates an independent community fork with a new
// ProtocolID bound to the parent bundle hash. Future signatures are domain-
// separated to forkProtocolID so cross-fork replay is cryptographically blocked.
func WithForkProtocolID(forkProtocolID string) ImportOption {
	return func(c *importConfig) {
		c.forkProtocolID = strings.TrimSpace(forkProtocolID)
	}
}

// WithImportClock overrides the clock for the imported/forked engine.
func WithImportClock(fn func() time.Time) ImportOption {
	return func(c *importConfig) {
		c.clockFn = fn
	}
}

// WithImportLedgerFile configures an append-only JSONL file for the imported engine.
func WithImportLedgerFile(path string) ImportOption {
	return func(c *importConfig) {
		c.ledgerPath = path
	}
}

// ExportSovereignBundle produces a deterministic, Ed25519-signed, hash-chained
// snapshot of the entire governance state and audit ledger so any independent
// community can audit, migrate, or fork without Hanzo-operated infrastructure.
func (e *Engine) ExportSovereignBundle(exporterID string, priv ed25519.PrivateKey) (*SovereignBundle, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	exporter, ok := e.members[exporterID]
	if !ok || !exporter.Active {
		return nil, fmt.Errorf("%w: exporter %q", ErrUnknownMember, exporterID)
	}
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: invalid exporter private key", ErrInvalidSignature)
	}
	derivedPubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	if !strings.EqualFold(derivedPubHex, exporter.PublicKeyHex) {
		return nil, fmt.Errorf("%w: exporter private key does not match registered member public key", ErrInvalidSignature)
	}

	now := e.nowFn().UTC()
	state := e.buildSovereignStateLocked()
	stateDigest, err := ComputeStateDigest(state)
	if err != nil {
		return nil, err
	}
	ledgerEntries := e.ledger.Entries()
	ledgerHead := e.ledger.HeadHash()

	bundle := &SovereignBundle{
		BundleVersion:     BundleVersion,
		ProtocolID:        e.cfg.ProtocolID,
		NetworkVersion:    e.cfg.NetworkVersion,
		ParentProtocolID:  e.parentProtocolID,
		ParentBundleHash:  e.parentBundleHash,
		ExportedAt:        now,
		State:             state,
		Ledger:            ledgerEntries,
		LedgerHeadHash:    ledgerHead,
		StateDigest:       stateDigest,
		ExporterID:        exporter.ID,
		ExporterPubKeyHex: exporter.PublicKeyHex,
	}
	bundle.BundleHash = ComputeBundleHash(bundle)
	sig, err := SignSovereignBundle(bundle, priv)
	if err != nil {
		return nil, err
	}
	bundle.ExporterSignature = sig
	return bundle, nil
}

// buildSovereignStateLocked constructs a canonically sorted deep copy of state.
func (e *Engine) buildSovereignStateLocked() SovereignState {
	members := make([]Member, 0, len(e.members))
	for _, m := range e.members {
		members = append(members, m)
	}
	sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })

	snapshots := make([]PowerCheckpoint, 0, len(e.snapshots))
	for _, s := range e.snapshots {
		snapshots = append(snapshots, s.Clone())
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].SnapshotID < snapshots[j].SnapshotID })

	params := make([]ParameterEntry, 0, len(e.parameters))
	for _, p := range e.parameters {
		params = append(params, p.Clone())
	}
	sort.Slice(params, func(i, j int) bool { return params[i].Key < params[j].Key })

	models := make([]ModelRegistryEntry, 0, len(e.models))
	for _, m := range e.models {
		models = append(models, m)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ModelID < models[j].ModelID })

	verifiers := make([]VerifierStandardEntry, 0, len(e.verifierStandards))
	for _, v := range e.verifierStandards {
		verifiers = append(verifiers, v)
	}
	sort.Slice(verifiers, func(i, j int) bool { return verifiers[i].ProfileID < verifiers[j].ProfileID })

	proposals := make([]Proposal, 0, len(e.proposals))
	for _, p := range e.proposals {
		proposals = append(proposals, p.Clone())
	}
	sort.Slice(proposals, func(i, j int) bool { return proposals[i].ID < proposals[j].ID })

	emergencies := make([]EmergencyAction, 0, len(e.emergencyActions))
	for _, a := range e.emergencyActions {
		emergencies = append(emergencies, a.Clone())
	}
	sort.Slice(emergencies, func(i, j int) bool { return emergencies[i].ActionID < emergencies[j].ActionID })

	upgrades := make([]ProtocolUpgradeRecord, len(e.upgrades))
	copy(upgrades, e.upgrades)

	return SovereignState{
		Config:            e.cfg.Clone(),
		GenesisTime:       e.genesisTime.UTC(),
		Members:           members,
		Snapshots:         snapshots,
		Parameters:        params,
		Models:            models,
		VerifierStandards: verifiers,
		Proposals:         proposals,
		EmergencyActions:  emergencies,
		Treasury:          e.treasury.Clone(),
		Upgrades:          upgrades,
		UsedNonces:        e.snapshotSortedNoncesLocked(),
	}
}

// VerifySovereignBundle performs complete fail-closed cryptographic and
// structural verification of an exported SovereignBundle:
//  1. Version, config invariants (including NoAdminOverride == true), and metadata.
//  2. StateDigest and BundleHash integrity (catches any single-byte tampering).
//  3. Exporter membership and Ed25519 signature over BundleHash.
//  4. Complete AuditLedger hash-chain replay & LedgerHeadHash match.
//  5. Snapshot state-root verification for every voting checkpoint.
//  6. Re-verification of every proposal, vote, and emergency action Ed25519
//     signature against registered member public keys and valid lineage protocol IDs.
func VerifySovereignBundle(b *SovereignBundle) error {
	if b == nil {
		return fmt.Errorf("%w: nil bundle", ErrInvalidBundle)
	}
	if b.BundleVersion != BundleVersion {
		return fmt.Errorf("%w: unsupported bundle_version %q", ErrInvalidBundle, b.BundleVersion)
	}
	if b.ProtocolID == "" || b.ProtocolID != b.State.Config.ProtocolID {
		return fmt.Errorf("%w: protocol_id mismatch between header and config", ErrInvalidBundle)
	}
	if b.NetworkVersion != b.State.Config.NetworkVersion {
		return fmt.Errorf("%w: network_version mismatch between header and config", ErrInvalidBundle)
	}
	if err := b.State.Config.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidBundle, err)
	}

	// Recompute state digest and bundle hash.
	wantStateDigest, err := ComputeStateDigest(b.State)
	if err != nil {
		return fmt.Errorf("%w: compute state digest: %v", ErrInvalidBundle, err)
	}
	if b.StateDigest != wantStateDigest {
		return fmt.Errorf("%w: state_digest mismatch (state tampered)", ErrInvalidBundle)
	}
	wantBundleHash := ComputeBundleHash(b)
	if b.BundleHash != wantBundleHash {
		return fmt.Errorf("%w: bundle_hash mismatch (metadata tampered)", ErrInvalidBundle)
	}

	// Verify audit ledger hash chain and head hash.
	if err := VerifyLedger(b.Ledger); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidBundle, err)
	}
	wantHead := GenesisPrevHash
	if len(b.Ledger) > 0 {
		wantHead = b.Ledger[len(b.Ledger)-1].EntryHash
	}
	if b.LedgerHeadHash != wantHead {
		return fmt.Errorf("%w: ledger_head_hash mismatch", ErrInvalidBundle)
	}

	// Index members and verify exporter membership + signature.
	memberMap := make(map[string]Member, len(b.State.Members))
	var activeMemberPower uint64
	for _, m := range b.State.Members {
		if m.ID == "" {
			return fmt.Errorf("%w: empty member id", ErrInvalidBundle)
		}
		if _, dup := memberMap[m.ID]; dup {
			return fmt.Errorf("%w: duplicate member %q", ErrInvalidBundle, m.ID)
		}
		if _, err := DecodePublicKey(m.PublicKeyHex); err != nil {
			return fmt.Errorf("%w: member %q invalid pubkey: %v", ErrInvalidBundle, m.ID, err)
		}
		if m.Active {
			activeMemberPower += m.VotingPower
		}
		memberMap[m.ID] = m
	}
	if activeMemberPower == 0 {
		return fmt.Errorf("%w: bundle has zero active member voting power", ErrInvalidBundle)
	}
	exporter, ok := memberMap[b.ExporterID]
	if !ok {
		return fmt.Errorf("%w: exporter %q not in member set", ErrInvalidBundle, b.ExporterID)
	}
	if !strings.EqualFold(exporter.PublicKeyHex, b.ExporterPubKeyHex) {
		return fmt.Errorf("%w: exporter pubkey mismatch", ErrInvalidBundle)
	}
	if err := VerifySovereignBundleSignature(b); err != nil {
		return fmt.Errorf("%w: exporter signature invalid: %v", ErrInvalidBundle, err)
	}

	// Collect allowed protocol IDs from the verified audit ledger lineage.
	allowedProtocols := map[string]bool{b.ProtocolID: true}
	if b.ParentProtocolID != "" {
		allowedProtocols[b.ParentProtocolID] = true
	}
	for _, entry := range b.Ledger {
		if entry.ProtocolID != "" {
			allowedProtocols[entry.ProtocolID] = true
		}
	}

	// Verify every snapshot state root and power conservation invariant.
	if len(b.State.Snapshots) == 0 {
		return fmt.Errorf("%w: bundle contains zero voting snapshots", ErrInvalidBundle)
	}
	snapMap := make(map[uint64]PowerCheckpoint, len(b.State.Snapshots))
	for _, s := range b.State.Snapshots {
		if _, dup := snapMap[s.SnapshotID]; dup {
			return fmt.Errorf("%w: duplicate snapshot %d", ErrInvalidBundle, s.SnapshotID)
		}
		var sumPower uint64
		for _, w := range s.EffectivePower {
			sumPower += w
		}
		if sumPower != s.TotalSupply {
			return fmt.Errorf("%w: snapshot %d effective power sum %d != total_supply %d", ErrInvalidBundle, s.SnapshotID, sumPower, s.TotalSupply)
		}
		wantRoot := HashSnapshotStateRoot(s.SnapshotID, s.Timestamp, s.TotalSupply, s.EffectivePower, s.Delegations)
		if s.StateRoot != wantRoot {
			return fmt.Errorf("%w: snapshot %d state_root mismatch", ErrInvalidBundle, s.SnapshotID)
		}
		snapMap[s.SnapshotID] = s
	}

	// Verify every parameter key is not a forbidden admin override.
	for _, param := range b.State.Parameters {
		if forbiddenAdminParamKeys[strings.ToLower(strings.TrimSpace(param.Key))] {
			return fmt.Errorf("%w: forbidden parameter %q in bundle", ErrAdminOverrideForbidden, param.Key)
		}
	}

	// Re-verify every proposal signature, payload hash, and vote signature + tally.
	seenProps := make(map[string]bool, len(b.State.Proposals))
	for _, p := range b.State.Proposals {
		if seenProps[p.ID] {
			return fmt.Errorf("%w: duplicate proposal %q", ErrInvalidBundle, p.ID)
		}
		seenProps[p.ID] = true
		proposer, ok := memberMap[p.ProposerID]
		if !ok {
			return fmt.Errorf("%w: proposal %q references unknown proposer %q", ErrInvalidBundle, p.ID, p.ProposerID)
		}
		propProto := p.OriginProtocolID
		if propProto == "" {
			propProto = b.ProtocolID
		}
		if !allowedProtocols[propProto] {
			return fmt.Errorf("%w: proposal %q origin protocol %q not in verified lineage", ErrInvalidBundle, p.ID, propProto)
		}
		wantPayloadHash, err := HashPayload(p.Payload)
		if err != nil || wantPayloadHash != p.PayloadHash {
			return fmt.Errorf("%w: proposal %q payload_hash mismatch", ErrInvalidBundle, p.ID)
		}
		pReq := ProposalRequest{
			ProposalID:  p.ID,
			ProposerID:  p.ProposerID,
			Org:         p.Org,
			Kind:        p.Kind,
			Title:       p.Title,
			Description: p.Description,
			Payload:     p.Payload,
			CreatedAt:   p.CreatedAt,
			Nonce:       p.Nonce,
			Signature:   p.Signature,
		}
		if err := VerifyProposalSignature(propProto, pReq, proposer.PublicKeyHex); err != nil {
			return fmt.Errorf("%w: proposal %q signature invalid: %v", ErrInvalidBundle, p.ID, err)
		}

		snap, ok := snapMap[p.SnapshotID]
		if !ok {
			return fmt.Errorf("%w: proposal %q references missing snapshot %d", ErrInvalidBundle, p.ID, p.SnapshotID)
		}

		var calcFor, calcAgainst, calcAbstain uint64
		for voterID, v := range p.Votes {
			if voterID != v.VoterID || v.ProposalID != p.ID {
				return fmt.Errorf("%w: vote key mismatch on proposal %q", ErrInvalidBundle, p.ID)
			}
			voter, ok := memberMap[v.VoterID]
			if !ok {
				return fmt.Errorf("%w: vote on %q by unknown voter %q", ErrInvalidBundle, p.ID, v.VoterID)
			}
			voteProto := v.OriginProtocolID
			if voteProto == "" {
				voteProto = propProto
			}
			if !allowedProtocols[voteProto] {
				return fmt.Errorf("%w: vote by %q on %q has unauthorized protocol %q", ErrInvalidBundle, v.VoterID, p.ID, voteProto)
			}
			vReq := VoteRequest{
				ProposalID: v.ProposalID,
				VoterID:    v.VoterID,
				Org:        v.Org,
				Support:    v.Support,
				Reason:     v.Reason,
				CastAt:     v.CastAt,
				Nonce:      v.Nonce,
				Signature:  v.Signature,
			}
			if err := VerifyVoteSignature(voteProto, vReq, voter.PublicKeyHex); err != nil {
				return fmt.Errorf("%w: vote by %q on %q signature invalid: %v", ErrInvalidBundle, v.VoterID, p.ID, err)
			}
			wantWeight := snap.EffectivePower[v.VoterID]
			if v.Weight != wantWeight {
				return fmt.Errorf("%w: vote weight mismatch for %q on %q: got %d want %d", ErrInvalidBundle, v.VoterID, p.ID, v.Weight, wantWeight)
			}
			switch v.Support {
			case VoteFor:
				calcFor += v.Weight
			case VoteAgainst:
				calcAgainst += v.Weight
			case VoteAbstain:
				calcAbstain += v.Weight
			default:
				return fmt.Errorf("%w: invalid vote support %q", ErrInvalidBundle, v.Support)
			}
		}
		if calcFor != p.ForVotes || calcAgainst != p.AgainstVotes || calcAbstain != p.AbstainVotes {
			return fmt.Errorf("%w: proposal %q tally mismatch", ErrInvalidBundle, p.ID)
		}
	}

	// Re-verify every emergency action signature and safeguard bounds.
	seenActions := make(map[string]bool, len(b.State.EmergencyActions))
	for _, a := range b.State.EmergencyActions {
		if seenActions[a.ActionID] {
			return fmt.Errorf("%w: duplicate emergency action %q", ErrInvalidBundle, a.ActionID)
		}
		seenActions[a.ActionID] = true
		guardian, ok := memberMap[a.GuardianID]
		if !ok {
			return fmt.Errorf("%w: emergency action %q unknown guardian %q", ErrInvalidBundle, a.ActionID, a.GuardianID)
		}
		if a.Kind != EmergencyPauseSubsystem && a.Kind != EmergencyCircuitBreaker {
			return fmt.Errorf("%w: emergency action %q has forbidden kind %q", ErrEmergencyAbuse, a.ActionID, a.Kind)
		}
		if a.Duration <= 0 || a.Duration > MaxAllowedEmergencyDuration {
			return fmt.Errorf("%w: emergency action %q duration %s exceeds cap %s", ErrEmergencyAbuse, a.ActionID, a.Duration, MaxAllowedEmergencyDuration)
		}
		if forbiddenEmergencyTargets[strings.ToLower(strings.TrimSpace(a.TargetSubsystem))] {
			return fmt.Errorf("%w: emergency action %q targets forbidden subsystem %q", ErrEmergencyAbuse, a.ActionID, a.TargetSubsystem)
		}
		actProto := a.OriginProtocolID
		if actProto == "" {
			actProto = b.ProtocolID
		}
		if !allowedProtocols[actProto] {
			return fmt.Errorf("%w: emergency action %q origin protocol %q not in verified lineage", ErrInvalidBundle, a.ActionID, actProto)
		}
		eReq := EmergencyActionRequest{
			ActionID:        a.ActionID,
			GuardianID:      a.GuardianID,
			Org:             a.Org,
			Kind:            a.Kind,
			TargetSubsystem: a.TargetSubsystem,
			Duration:        a.Duration,
			Reason:          a.Reason,
			IssuedAt:        a.IssuedAt,
			Nonce:           a.Nonce,
			Signature:       a.Signature,
		}
		if err := VerifyEmergencySignature(actProto, eReq, guardian.PublicKeyHex); err != nil {
			return fmt.Errorf("%w: emergency action %q signature invalid: %v", ErrInvalidBundle, a.ActionID, err)
		}
	}

	return nil
}

// ImportSovereignBundle verifies a SovereignBundle and instantiates an
// independent Engine. When WithForkProtocolID(newID) is provided with a new
// protocol ID, the engine forks onto newID while preserving full verified
// lineage and hash-chained history.
func ImportSovereignBundle(b *SovereignBundle, opts ...ImportOption) (*Engine, error) {
	if err := VerifySovereignBundle(b); err != nil {
		return nil, err
	}

	var ic importConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&ic)
		}
	}

	cfg := b.State.Config.Clone()
	isFork := ic.forkProtocolID != "" && ic.forkProtocolID != b.ProtocolID
	parentProto := b.ParentProtocolID
	parentHash := b.ParentBundleHash
	if isFork {
		parentProto = b.ProtocolID
		parentHash = b.BundleHash
		cfg.ProtocolID = ic.forkProtocolID
	}

	nowFn := func() time.Time { return time.Now().UTC() }
	if ic.clockFn != nil {
		nowFn = ic.clockFn
	}

	e := &Engine{
		nowFn:             nowFn,
		ledgerPath:        ic.ledgerPath,
		cfg:               cfg,
		genesisTime:       b.State.GenesisTime.UTC(),
		parentProtocolID:  parentProto,
		parentBundleHash:  parentHash,
		ancestorProtocols: map[string]bool{cfg.ProtocolID: true, b.ProtocolID: true},
		members:           make(map[string]Member, len(b.State.Members)),
		snapshots:         make(map[uint64]PowerCheckpoint, len(b.State.Snapshots)),
		parameters:        make(map[string]ParameterEntry, len(b.State.Parameters)),
		models:            make(map[string]ModelRegistryEntry, len(b.State.Models)),
		verifierStandards: make(map[string]VerifierStandardEntry, len(b.State.VerifierStandards)),
		proposals:         make(map[string]*Proposal, len(b.State.Proposals)),
		emergencyActions:  make(map[string]*EmergencyAction, len(b.State.EmergencyActions)),
		treasury:          b.State.Treasury.Clone(),
		upgrades:          make([]ProtocolUpgradeRecord, len(b.State.Upgrades)),
		usedNonces:        make(map[string]map[string]bool, len(b.State.UsedNonces)),
		ledger:            NewAuditLedger(ic.ledgerPath),
	}
	if parentProto != "" {
		e.ancestorProtocols[parentProto] = true
	}

	for _, m := range b.State.Members {
		e.members[m.ID] = m
	}
	for _, s := range b.State.Snapshots {
		e.snapshots[s.SnapshotID] = s.Clone()
		if s.SnapshotID > e.currentSnapshotID {
			e.currentSnapshotID = s.SnapshotID
		}
	}
	for _, p := range b.State.Parameters {
		e.parameters[p.Key] = p.Clone()
	}
	for _, m := range b.State.Models {
		e.models[m.ModelID] = m
	}
	for _, v := range b.State.VerifierStandards {
		e.verifierStandards[v.ProfileID] = v
	}
	for _, p := range b.State.Proposals {
		cp := p.Clone()
		e.proposals[cp.ID] = &cp
	}
	for _, a := range b.State.EmergencyActions {
		cp := a.Clone()
		e.emergencyActions[cp.ActionID] = &cp
	}
	copy(e.upgrades, b.State.Upgrades)
	for memberID, nonces := range b.State.UsedNonces {
		set := make(map[string]bool, len(nonces))
		for _, n := range nonces {
			set[n] = true
		}
		e.usedNonces[memberID] = set
	}
	for _, p := range b.State.Proposals {
		if p.Nonce != "" {
			e.markNonceUsedLocked(p.ProposerID, p.Nonce)
		}
		for _, v := range p.Votes {
			if v.Nonce != "" {
				e.markNonceUsedLocked(v.VoterID, v.Nonce)
			}
		}
	}
	for _, a := range b.State.EmergencyActions {
		if a.Nonce != "" {
			e.markNonceUsedLocked(a.GuardianID, a.Nonce)
		}
	}

	if err := e.ledger.Replace(b.Ledger); err != nil {
		return nil, err
	}

	if isFork {
		now := e.nowFn().UTC()
		if _, err := e.ledger.Append(now, e.cfg.ProtocolID, "fork_genesis", "community", e.cfg.ProtocolID, map[string]any{
			"fork_protocol_id":   e.cfg.ProtocolID,
			"parent_protocol_id": e.parentProtocolID,
			"parent_bundle_hash": e.parentBundleHash,
			"inherited_snapshot": e.currentSnapshotID,
		}); err != nil {
			return nil, err
		}
	}

	return e, nil
}
