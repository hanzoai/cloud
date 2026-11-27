// SPDX-License-Identifier: Apache-2.0 OR MIT

package escrow

import (
	"encoding/json"
	"fmt"
	"math"
	"math/bits"
	"sort"
	"strings"
	"sync"
)

const genesisLedgerHash = "0x0000000000000000000000000000000000000000000000000000000000000000"

// EscrowEngine is a thread-safe, deterministic, host-independent compute escrow
// and dispute settlement state machine.
type EscrowEngine struct {
	mu                  sync.RWMutex
	domain              EIP712Domain
	policy              Policy
	clock               Clock
	slaVerifier         ServiceGuaranteeVerifier
	computationVerifier ComputationVerifier

	initialMintedSupply uint64
	balances            map[string]uint64
	escrows             map[string]*EscrowRecord
	escrowOrder         []string
	ledger              []LedgerEntry
	headHash            string
	subjectiveSlashes   uint64
}

// NewEscrowEngine constructs a standalone EscrowEngine using DefaultPolicy unless overridden.
func NewEscrowEngine(domain EIP712Domain, clock Clock, policy *Policy) *EscrowEngine {
	if domain.Name == "" {
		domain = DefaultDomain()
	}
	if clock == nil {
		clock = RealClock{}
	}
	pol := DefaultPolicy()
	if policy != nil {
		pol = *policy
	}
	return &EscrowEngine{
		domain:              domain,
		policy:              pol,
		clock:               clock,
		slaVerifier:         DefaultServiceGuaranteeVerifier{},
		computationVerifier: DefaultComputationVerifier{},
		balances:            make(map[string]uint64),
		escrows:             make(map[string]*EscrowRecord),
		headHash:            genesisLedgerHash,
	}
}

// Domain returns the EIP-712 signing domain of the engine.
func (e *EscrowEngine) Domain() EIP712Domain {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.domain
}

// Policy returns the active safety and economic policy.
func (e *EscrowEngine) Policy() Policy {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.policy
}

// HeadHash returns the current tip hash of the hash-chained settlement ledger.
func (e *EscrowEngine) HeadHash() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.headHash
}

// EscrowCount returns the number of registered compute escrows.
func (e *EscrowEngine) EscrowCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.escrows)
}

// Mint credits atomic settlement units to an account on the local test-chain ledger
// and increments initialMintedSupply so VerifyConservation tracks exact supply.
func (e *EscrowEngine) Mint(address string, amount uint64) error {
	if amount == 0 {
		return fmt.Errorf("%w: mint amount must be > 0", ErrInvalidRequest)
	}
	norm, err := NormalizeAddress(address)
	if err != nil {
		return err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	newSupply, ok := addUint64Checked(e.initialMintedSupply, amount)
	if !ok {
		return fmt.Errorf("%w: supply overflow", ErrInvalidRequest)
	}
	newBal, ok := addUint64Checked(e.balances[norm], amount)
	if !ok {
		return fmt.Errorf("%w: balance overflow", ErrInvalidRequest)
	}
	e.initialMintedSupply = newSupply
	e.balances[norm] = newBal
	e.appendLedgerLocked("AccountMinted", "", norm, amount, "", "minted atomic settlement units")
	return nil
}

// BalanceOf returns an account's unlocked balance in atomic units.
func (e *EscrowEngine) BalanceOf(address string) uint64 {
	norm, err := NormalizeAddress(address)
	if err != nil {
		return 0
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.balances[norm]
}

// LockedOf returns the total atomic units currently locked across active escrows for address.
func (e *EscrowEngine) LockedOf(address string) uint64 {
	norm, err := NormalizeAddress(address)
	if err != nil {
		return 0
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	var total uint64
	for _, rec := range e.escrows {
		if rec.Quote.Buyer == norm {
			total += rec.LockedBuyerAtomic
		}
		if rec.Quote.Provider == norm {
			total += rec.LockedProviderBond
		}
		if rec.Challenge != nil && rec.Challenge.Challenger == norm {
			total += rec.LockedChallengeBond
		}
	}
	return total
}

// CreateQuote validates and registers a provider-signed JobQuote in StateQuoted.
func (e *EscrowEngine) CreateQuote(q JobQuote) (*EscrowRecord, error) {
	if strings.TrimSpace(q.JobID) == "" {
		return nil, fmt.Errorf("%w: empty jobId", ErrInvalidRequest)
	}
	buyer, err := NormalizeAddress(q.Buyer)
	if err != nil {
		return nil, err
	}
	provider, err := NormalizeAddress(q.Provider)
	if err != nil {
		return nil, err
	}
	if buyer == provider {
		return nil, fmt.Errorf("%w: buyer and provider must be distinct addresses", ErrInvalidRequest)
	}
	if q.Asset == "" {
		q.Asset = "USDC"
	}
	if q.JobPriceAtomic == 0 {
		return nil, fmt.Errorf("%w: jobPriceAtomic must be > 0", ErrInvalidRequest)
	}
	if q.ProviderBondAtomic == 0 {
		return nil, fmt.Errorf("%w: providerBondAtomic must be > 0", ErrInvalidRequest)
	}
	if q.SLAPenaltyAtomic > q.ProviderBondAtomic {
		return nil, fmt.Errorf("%w: slaPenaltyAtomic exceeds providerBondAtomic", ErrInvalidRequest)
	}
	if q.ChallengerRewardBps > 10000 {
		return nil, fmt.Errorf("%w: challengerRewardBps must be <= 10000", ErrInvalidRequest)
	}
	if q.Deadline <= 0 {
		return nil, fmt.Errorf("%w: deadline must be positive unix seconds", ErrInvalidRequest)
	}
	if _, err := decodeBytes32(q.InputDigest); err != nil {
		return nil, err
	}
	switch q.VerificationClass {
	case ClassDeterministicHash, ClassVerifiableProof, ClassNonDeterministicSampling:
	default:
		return nil, fmt.Errorf("%w: invalid verificationClass %q", ErrInvalidRequest, q.VerificationClass)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.policy.RequireChallengeBond && q.ChallengeBondAtomic == 0 {
		return nil, ErrChallengeBondRequired
	}
	if !e.policy.AllowInstantRelease && q.ChallengeWindowSec < e.policy.MinChallengeWindowSec {
		return nil, fmt.Errorf("%w: challengeWindowSec %d < minimum %d", ErrInvalidRequest, q.ChallengeWindowSec, e.policy.MinChallengeWindowSec)
	}
	if q.ChallengeWindowSec < 0 {
		return nil, fmt.Errorf("%w: challengeWindowSec must be >= 0", ErrInvalidRequest)
	}
	if _, exists := e.escrows[q.JobID]; exists {
		return nil, ErrQuoteAlreadyExists
	}

	q.Buyer = buyer
	q.Provider = provider
	if err := VerifyJobQuote(e.domain, q); err != nil {
		return nil, err
	}
	sh, _ := QuoteStructHash(q)
	digest := e.domain.TypedDataDigest(sh)
	q.QuoteHash = fmt.Sprintf("0x%x", digest[:])
	if q.CreatedAt == 0 {
		q.CreatedAt = e.clock.Now().Unix()
	}

	rec := &EscrowRecord{
		JobID: q.JobID,
		State: StateQuoted,
		Quote: q,
	}
	e.escrows[q.JobID] = rec
	e.escrowOrder = append(e.escrowOrder, q.JobID)
	e.appendLedgerLocked("QuoteCreated", q.JobID, q.Provider, q.JobPriceAtomic, StateQuoted, q.QuoteHash)
	return cloneEscrowRecord(rec), nil
}

// FundEscrow verifies buyer's EIP-712 QuoteAcceptance and atomically locks
// JobPriceAtomic from buyer and ProviderBondAtomic from provider (Quoted -> Funded).
func (e *EscrowEngine) FundEscrow(acc QuoteAcceptance) (*EscrowRecord, error) {
	if strings.TrimSpace(acc.JobID) == "" {
		return nil, fmt.Errorf("%w: empty jobId", ErrInvalidRequest)
	}
	normBuyer, err := NormalizeAddress(acc.Buyer)
	if err != nil {
		return nil, err
	}
	acc.Buyer = normBuyer

	e.mu.Lock()
	defer e.mu.Unlock()

	rec, ok := e.escrows[acc.JobID]
	if !ok {
		return nil, ErrEscrowNotFound
	}
	if rec.Finalized || rec.State.IsTerminal() {
		return nil, ErrAlreadyFinalized
	}
	if rec.State != StateQuoted {
		return nil, fmt.Errorf("%w: expected Quoted, got %s", ErrInvalidStateTransition, rec.State)
	}
	if !SameAddress(acc.Buyer, rec.Quote.Buyer) {
		return nil, fmt.Errorf("%w: acceptance buyer mismatch", ErrSignerMismatch)
	}
	if !strings.EqualFold(acc.QuoteHash, rec.Quote.QuoteHash) {
		return nil, fmt.Errorf("%w: acceptance quoteHash mismatch", ErrInvalidRequest)
	}
	if acc.JobPriceAtomic != rec.Quote.JobPriceAtomic {
		return nil, fmt.Errorf("%w: acceptance price mismatch", ErrInvalidRequest)
	}
	if err := VerifyQuoteAcceptance(e.domain, acc); err != nil {
		return nil, err
	}

	now := e.clock.Now().Unix()
	if now > rec.Quote.Deadline {
		return nil, ErrDeadlineExceeded
	}
	if e.balances[rec.Quote.Buyer] < rec.Quote.JobPriceAtomic {
		return nil, fmt.Errorf("%w: buyer balance %d < jobPrice %d", ErrInsufficientBalance, e.balances[rec.Quote.Buyer], rec.Quote.JobPriceAtomic)
	}
	if e.balances[rec.Quote.Provider] < rec.Quote.ProviderBondAtomic {
		return nil, fmt.Errorf("%w: provider balance %d < providerBond %d", ErrInsufficientBalance, e.balances[rec.Quote.Provider], rec.Quote.ProviderBondAtomic)
	}

	// Atomic lock of buyer payment + provider commitment bond.
	e.balances[rec.Quote.Buyer] -= rec.Quote.JobPriceAtomic
	e.balances[rec.Quote.Provider] -= rec.Quote.ProviderBondAtomic
	rec.LockedBuyerAtomic = rec.Quote.JobPriceAtomic
	rec.LockedProviderBond = rec.Quote.ProviderBondAtomic
	accCopy := acc
	rec.Acceptance = &accCopy
	rec.FundedAt = now
	rec.State = StateFunded

	e.appendLedgerLocked("EscrowFunded", rec.JobID, rec.Quote.Buyer, rec.LockedBuyerAtomic+rec.LockedProviderBond, StateFunded, "buyer payment and provider bond locked")
	return cloneEscrowRecord(rec), nil
}

// SubmitClaim verifies provider's EIP-712 CompletionClaim, checks objective SLA deadline,
// records separated ServiceGuaranteeProof and ComputationProof, and transitions Funded -> Claimed.
func (e *EscrowEngine) SubmitClaim(claim CompletionClaim) (*EscrowRecord, error) {
	if strings.TrimSpace(claim.JobID) == "" {
		return nil, fmt.Errorf("%w: empty jobId", ErrInvalidRequest)
	}
	normProv, err := NormalizeAddress(claim.Provider)
	if err != nil {
		return nil, err
	}
	claim.Provider = normProv
	if _, err := decodeBytes32(claim.OutputDigest); err != nil {
		return nil, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	rec, ok := e.escrows[claim.JobID]
	if !ok {
		return nil, ErrEscrowNotFound
	}
	if rec.Finalized || rec.State.IsTerminal() {
		return nil, ErrAlreadyFinalized
	}
	if rec.State != StateFunded {
		return nil, fmt.Errorf("%w: expected Funded, got %s", ErrInvalidStateTransition, rec.State)
	}
	if !SameAddress(claim.Provider, rec.Quote.Provider) {
		return nil, fmt.Errorf("%w: claim provider mismatch", ErrSignerMismatch)
	}
	if !strings.EqualFold(claim.QuoteHash, rec.Quote.QuoteHash) {
		return nil, fmt.Errorf("%w: claim quoteHash mismatch", ErrInvalidRequest)
	}
	if claim.CompletedAt <= 0 {
		return nil, fmt.Errorf("%w: claim completedAt must be > 0", ErrInvalidRequest)
	}
	if err := VerifyCompletionClaim(e.domain, claim); err != nil {
		return nil, err
	}

	now := e.clock.Now().Unix()
	// Objective Service Quality (SLA) deadline check.
	slaProof, missed, _ := e.slaVerifier.VerifySLA(rec.Quote, &claim, now)
	if missed || now > rec.Quote.Deadline {
		return nil, ErrDeadlineExceeded
	}
	if rec.Quote.ChallengeWindowSec > 0 &&
		(claim.CompletedAt > math.MaxInt64-rec.Quote.ChallengeWindowSec || now > math.MaxInt64-rec.Quote.ChallengeWindowSec) {
		return nil, fmt.Errorf("%w: challengeDeadline overflow", ErrInvalidRequest)
	}
	minChallengeDeadline := claim.CompletedAt + rec.Quote.ChallengeWindowSec
	if fromReceipt := now + rec.Quote.ChallengeWindowSec; fromReceipt > minChallengeDeadline {
		minChallengeDeadline = fromReceipt
	}
	if claim.ChallengeDeadline < minChallengeDeadline {
		return nil, fmt.Errorf("%w: challengeDeadline %d < required minimum %d (receipt=%d, completedAt=%d, window=%d)",
			ErrInvalidRequest, claim.ChallengeDeadline, minChallengeDeadline, now, claim.CompletedAt, rec.Quote.ChallengeWindowSec)
	}

	claimCopy := claim
	rec.Claim = &claimCopy
	rec.ServiceProof = &slaProof
	rec.ComputationProof = &ComputationProof{
		VerificationClass: rec.Quote.VerificationClass,
		InputDigest:       rec.Quote.InputDigest,
		OutputDigest:      claim.OutputDigest,
		ExecutionProof:    claim.ExecutionProof,
		TokensProcessed:   claim.TokensProcessed,
	}
	rec.State = StateClaimed

	e.appendLedgerLocked("CompletionClaimed", rec.JobID, rec.Quote.Provider, rec.LockedBuyerAtomic, StateClaimed, claim.OutputDigest)
	return cloneEscrowRecord(rec), nil
}

// OpenChallenge validates and opens an objective protocol dispute (Claimed -> Challenged,
// or overdue Funded -> Challenged for ViolationMissedDeadline).
//
// Subjective quality claims are unconditionally rejected with ErrSubjectiveSlashingForbidden
// before locking any funds or mutating escrow state.
func (e *EscrowEngine) OpenChallenge(sub ChallengeSubmission) (*EscrowRecord, error) {
	if strings.TrimSpace(sub.JobID) == "" {
		return nil, fmt.Errorf("%w: empty jobId", ErrInvalidRequest)
	}
	// Fail-closed objective-only slashing gate BEFORE any state mutation.
	domain, err := ClassifyViolation(sub.Evidence.ViolationType)
	if err != nil {
		return nil, err
	}
	sub.Evidence.ProofDomain = domain

	normChallenger, err := NormalizeAddress(sub.Challenger)
	if err != nil {
		return nil, err
	}
	sub.Challenger = normChallenger

	e.mu.Lock()
	defer e.mu.Unlock()

	rec, ok := e.escrows[sub.JobID]
	if !ok {
		return nil, ErrEscrowNotFound
	}
	if rec.Finalized || rec.State.IsTerminal() {
		return nil, ErrAlreadyFinalized
	}
	if sub.Evidence.ViolationType == ViolationFalseCompletionClaim {
		if rec.Quote.VerificationClass == ClassNonDeterministicSampling || rec.Quote.VerificationSpec == SpecNonDeterministicLLM {
			return nil, ErrNonDeterministicVerificationNeeded
		}
		if sub.Evidence.CanonicalInput == "" {
			return nil, fmt.Errorf("%w: canonicalInput is required for independent recomputation", ErrInvalidRequest)
		}
		recomputedInputDigest := ComputeInputDigest(sub.Evidence.CanonicalInput)
		if !strings.EqualFold(recomputedInputDigest, rec.Quote.InputDigest) {
			return nil, fmt.Errorf("%w: canonicalInput digest %s does not match quote inputDigest %s", ErrInvalidRequest, recomputedInputDigest, rec.Quote.InputDigest)
		}
		if _, _, err := ExecuteDeterministicKernel(rec.Quote.VerificationSpec, sub.Evidence.CanonicalInput); err != nil {
			return nil, err
		}
	}

	now := e.clock.Now().Unix()
	switch rec.State {
	case StateClaimed:
		if now > rec.Claim.ChallengeDeadline {
			return nil, ErrChallengeWindowClosed
		}
	case StateFunded:
		if sub.Evidence.ViolationType != ViolationMissedDeadline {
			return nil, fmt.Errorf("%w: only missed_deadline challenge allowed before claim", ErrInvalidStateTransition)
		}
		if now <= rec.Quote.Deadline {
			return nil, ErrDeadlineNotReached
		}
	default:
		return nil, fmt.Errorf("%w: cannot challenge escrow in state %s", ErrInvalidStateTransition, rec.State)
	}

	if e.policy.RequireChallengeBond && sub.ChallengeBondAtomic == 0 {
		return nil, ErrChallengeBondRequired
	}
	if sub.ChallengeBondAtomic != rec.Quote.ChallengeBondAtomic {
		return nil, fmt.Errorf("%w: challengeBondAtomic %d != required %d", ErrInvalidRequest, sub.ChallengeBondAtomic, rec.Quote.ChallengeBondAtomic)
	}
	if err := VerifyChallengeSubmission(e.domain, sub); err != nil {
		return nil, err
	}
	if e.balances[sub.Challenger] < sub.ChallengeBondAtomic {
		return nil, fmt.Errorf("%w: challenger balance %d < challengeBond %d", ErrInsufficientBalance, e.balances[sub.Challenger], sub.ChallengeBondAtomic)
	}

	// Lock challenger anti-griefing bond.
	e.balances[sub.Challenger] -= sub.ChallengeBondAtomic
	rec.LockedChallengeBond = sub.ChallengeBondAtomic
	subCopy := sub
	rec.Challenge = &subCopy
	rec.State = StateChallenged

	e.appendLedgerLocked("ChallengeOpened", rec.JobID, sub.Challenger, sub.ChallengeBondAtomic, StateChallenged, string(sub.Evidence.ViolationType))
	return cloneEscrowRecord(rec), nil
}

// mulDivUint64 computes floor((a * b) / denom) using exact 128-bit arithmetic
// without overflowing uint64 when the quotient fits in uint64.
func mulDivUint64(a, b, denom uint64) uint64 {
	hi, lo := bits.Mul64(a, b)
	q, _ := bits.Div64(hi, lo, denom)
	return q
}

// ResolveChallenge independently verifies objective challenge evidence and
// adjudicates the open challenge:
//   - Objective violation confirmed -> StateSlashed (provider slashed, buyer refunded, challenger rewarded)
//   - Allegation rejected while dispute window remains open -> challenger bond forfeited to provider
//     and escrow returns to StateClaimed (or StateFunded if unclaimed) so legitimate challenges remain possible
//   - Allegation rejected after dispute window elapsed -> StateSettled (payment, provider bond, and
//     forfeited challenger bond released atomically to provider)
func (e *EscrowEngine) ResolveChallenge(jobID string) (*PayoutRecord, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	rec, ok := e.escrows[jobID]
	if !ok {
		return nil, ErrEscrowNotFound
	}
	if rec.Finalized || rec.State.IsTerminal() {
		return nil, ErrAlreadyFinalized
	}
	if rec.State != StateChallenged || rec.Challenge == nil {
		return nil, fmt.Errorf("%w: expected Challenged, got %s", ErrInvalidStateTransition, rec.State)
	}

	now := e.clock.Now().Unix()
	ev := rec.Challenge.Evidence
	proofDomain, err := ClassifyViolation(ev.ViolationType)
	if err != nil {
		return nil, err
	}

	var violationConfirmed bool
	var reason string
	switch proofDomain {
	case ProofDomainServiceQuality:
		slaProof, missed, r := e.slaVerifier.VerifySLA(rec.Quote, rec.Claim, now)
		rec.ServiceProof = &slaProof
		violationConfirmed = missed
		reason = r
	case ProofDomainComputationIntegrity:
		violationConfirmed, reason, err = e.computationVerifier.VerifyChallenge(e.domain, rec.Quote, rec.Claim, ev)
		if err != nil {
			// Invalid or unverifiable challenger evidence must never permanently
			// wedge funds in StateChallenged; reject the allegation cleanly.
			violationConfirmed = false
			reason = fmt.Sprintf("challenge evidence invalid; allegation rejected: %v", err)
		}
	default:
		return nil, ErrSubjectiveSlashingForbidden
	}

	lockedBuyer := rec.LockedBuyerAtomic
	lockedProv := rec.LockedProviderBond
	lockedChal := rec.LockedChallengeBond

	var payout PayoutRecord
	if violationConfirmed {
		// Provider objectively violated protocol: slash 100% of ProviderBond.
		challengerReward := mulDivUint64(lockedProv, uint64(rec.Quote.ChallengerRewardBps), 10000)
		buyerBondCompensation := lockedProv - challengerReward

		buyerTotal := lockedBuyer + buyerBondCompensation
		challengerTotal := lockedChal + challengerReward

		e.balances[rec.Quote.Buyer] += buyerTotal
		e.balances[rec.Challenge.Challenger] += challengerTotal

		rec.LockedBuyerAtomic = 0
		rec.LockedProviderBond = 0
		rec.LockedChallengeBond = 0
		rec.State = StateSlashed
		rec.Finalized = true
		rec.PayoutCount++

		payout = PayoutRecord{
			JobID:                  rec.JobID,
			TerminalState:          StateSlashed,
			ChallengeOutcome:       ChallengeOutcomeUpheldSlashed,
			ProofDomain:            proofDomain,
			SettledAt:              now,
			BuyerPayoutAtomic:      buyerTotal,
			ProviderPayoutAtomic:   0,
			ChallengerPayoutAtomic: challengerTotal,
			ProviderSlashedAtomic:  lockedProv,
			Reason:                 reason,
		}
		payout.ReceiptHash = computePayoutHash(payout)
		payoutCopy := payout
		rec.Payout = &payoutCopy
		e.appendLedgerLocked("EscrowSlashed", rec.JobID, rec.Quote.Buyer, payout.BuyerPayoutAtomic+payout.ChallengerPayoutAtomic, StateSlashed, payout.Reason)
		return &payoutCopy, nil
	}

	// Challenge allegation rejected: forfeit challenger bond to provider.
	if rec.Claim == nil {
		// Unclaimed funded escrow: forfeit challenger bond and return to StateFunded.
		e.balances[rec.Quote.Provider] += lockedChal
		rec.LockedChallengeBond = 0
		rec.Challenge = nil
		rec.State = StateFunded

		payout = PayoutRecord{
			JobID:                   rec.JobID,
			TerminalState:           StateFunded,
			ChallengeOutcome:        ChallengeOutcomeRejectedMalicious,
			ProofDomain:             proofDomain,
			SettledAt:               now,
			ProviderPayoutAtomic:    lockedChal,
			ChallengerSlashedAtomic: lockedChal,
			Reason:                  reason,
		}
		payout.ReceiptHash = computePayoutHash(payout)
		payoutCopy := payout
		e.appendLedgerLocked("ChallengeRejected", rec.JobID, rec.Quote.Provider, lockedChal, StateFunded, payout.Reason)
		return &payoutCopy, nil
	}

	if now < rec.Claim.ChallengeDeadline {
		// Dispute window is still open: forfeit the unfounded challenger's bond
		// to the provider, but keep buyer payment and provider bond locked in
		// StateClaimed so other objective challenges remain possible until
		// ChallengeDeadline elapses.
		e.balances[rec.Quote.Provider] += lockedChal
		rec.LockedChallengeBond = 0
		rec.Challenge = nil
		rec.State = StateClaimed

		payout = PayoutRecord{
			JobID:                   rec.JobID,
			TerminalState:           StateClaimed,
			ChallengeOutcome:        ChallengeOutcomeRejectedMalicious,
			ProofDomain:             proofDomain,
			SettledAt:               now,
			ProviderPayoutAtomic:    lockedChal,
			ChallengerSlashedAtomic: lockedChal,
			Reason:                  reason,
		}
		payout.ReceiptHash = computePayoutHash(payout)
		payoutCopy := payout
		e.appendLedgerLocked("ChallengeRejected", rec.JobID, rec.Quote.Provider, lockedChal, StateClaimed, payout.Reason)
		return &payoutCopy, nil
	}

	// Dispute window has elapsed and the challenge was rejected: settle atomically.
	providerTotal := lockedBuyer + lockedProv + lockedChal
	e.balances[rec.Quote.Provider] += providerTotal

	rec.LockedBuyerAtomic = 0
	rec.LockedProviderBond = 0
	rec.LockedChallengeBond = 0
	rec.State = StateSettled
	rec.Finalized = true
	rec.PayoutCount++

	payout = PayoutRecord{
		JobID:                   rec.JobID,
		TerminalState:           StateSettled,
		ChallengeOutcome:        ChallengeOutcomeRejectedMalicious,
		ProofDomain:             proofDomain,
		SettledAt:               now,
		BuyerPayoutAtomic:       0,
		ProviderPayoutAtomic:    providerTotal,
		ChallengerPayoutAtomic:  0,
		ChallengerSlashedAtomic: lockedChal,
		Reason:                  reason,
	}
	payout.ReceiptHash = computePayoutHash(payout)
	payoutCopy := payout
	rec.Payout = &payoutCopy
	e.appendLedgerLocked("EscrowSettled", rec.JobID, rec.Quote.Provider, payout.ProviderPayoutAtomic, StateSettled, payout.Reason)
	return &payoutCopy, nil
}

// Finalize releases payment + bond to the provider in a single atomic transition (Claimed -> Settled)
// once the optimistic challenge window has elapsed with no open challenge.
func (e *EscrowEngine) Finalize(jobID string) (*PayoutRecord, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	rec, ok := e.escrows[jobID]
	if !ok {
		return nil, ErrEscrowNotFound
	}
	if rec.Finalized || rec.State.IsTerminal() {
		return nil, ErrAlreadyFinalized
	}
	if rec.State != StateClaimed || rec.Claim == nil {
		return nil, fmt.Errorf("%w: expected Claimed, got %s", ErrInvalidStateTransition, rec.State)
	}

	now := e.clock.Now().Unix()
	if now < rec.Claim.ChallengeDeadline {
		return nil, ErrChallengeWindowActive
	}

	providerTotal := rec.LockedBuyerAtomic + rec.LockedProviderBond
	e.balances[rec.Quote.Provider] += providerTotal

	rec.LockedBuyerAtomic = 0
	rec.LockedProviderBond = 0
	rec.LockedChallengeBond = 0
	rec.State = StateSettled
	rec.Finalized = true
	rec.PayoutCount++

	payout := PayoutRecord{
		JobID:                rec.JobID,
		TerminalState:        StateSettled,
		ProofDomain:          ProofDomainComputationIntegrity,
		SettledAt:            now,
		ProviderPayoutAtomic: providerTotal,
		Reason:               "optimistic challenge window elapsed unchallenged; payment and bond released to provider",
	}
	payout.ReceiptHash = computePayoutHash(payout)
	payoutCopy := payout
	rec.Payout = &payoutCopy

	e.appendLedgerLocked("EscrowSettled", rec.JobID, rec.Quote.Provider, providerTotal, StateSettled, payout.Reason)
	return &payoutCopy, nil
}

// RefundMissedDeadline handles worker crash / missed deadline recovery (Funded -> Refunded).
// Once Deadline has elapsed with no valid CompletionClaim, buyer atomically reclaims 100% of
// JobPriceAtomic plus deterministic SLAPenaltyAtomic from ProviderBondAtomic.
func (e *EscrowEngine) RefundMissedDeadline(jobID string) (*PayoutRecord, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	rec, ok := e.escrows[jobID]
	if !ok {
		return nil, ErrEscrowNotFound
	}
	if rec.Finalized || rec.State.IsTerminal() {
		return nil, ErrAlreadyFinalized
	}
	if rec.State != StateFunded {
		return nil, fmt.Errorf("%w: expected Funded for missed-deadline refund, got %s", ErrInvalidStateTransition, rec.State)
	}

	now := e.clock.Now().Unix()
	slaProof, missed, reason := e.slaVerifier.VerifySLA(rec.Quote, nil, now)
	if !missed {
		return nil, ErrDeadlineNotReached
	}

	penalty := rec.Quote.SLAPenaltyAtomic
	if penalty > rec.LockedProviderBond {
		penalty = rec.LockedProviderBond
	}
	buyerTotal := rec.LockedBuyerAtomic + penalty
	providerReturn := rec.LockedProviderBond - penalty

	e.balances[rec.Quote.Buyer] += buyerTotal
	if providerReturn > 0 {
		e.balances[rec.Quote.Provider] += providerReturn
	}

	rec.ServiceProof = &slaProof
	rec.LockedBuyerAtomic = 0
	rec.LockedProviderBond = 0
	rec.LockedChallengeBond = 0
	rec.State = StateRefunded
	rec.Finalized = true
	rec.PayoutCount++

	payout := PayoutRecord{
		JobID:                 rec.JobID,
		TerminalState:         StateRefunded,
		ProofDomain:           ProofDomainServiceQuality,
		SettledAt:             now,
		BuyerPayoutAtomic:     buyerTotal,
		ProviderPayoutAtomic:  providerReturn,
		ProviderSlashedAtomic: penalty,
		Reason:                reason,
	}
	payout.ReceiptHash = computePayoutHash(payout)
	payoutCopy := payout
	rec.Payout = &payoutCopy

	e.appendLedgerLocked("EscrowRefunded", rec.JobID, rec.Quote.Buyer, buyerTotal, StateRefunded, reason)
	return &payoutCopy, nil
}

// GetEscrow returns a deep copy of an escrow record by JobID.
func (e *EscrowEngine) GetEscrow(jobID string) (*EscrowRecord, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	rec, ok := e.escrows[jobID]
	if !ok {
		return nil, ErrEscrowNotFound
	}
	return cloneEscrowRecord(rec), nil
}

// ListEscrows returns deep copies of all escrows in deterministic creation order.
func (e *EscrowEngine) ListEscrows() []*EscrowRecord {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]*EscrowRecord, 0, len(e.escrowOrder))
	for _, id := range e.escrowOrder {
		if rec, ok := e.escrows[id]; ok {
			out = append(out, cloneEscrowRecord(rec))
		}
	}
	return out
}

// VerifyConservation checks the global atomic conservation invariant:
// sum(balances) + sum(locked_escrows) == initialMintedSupply and PayoutCount <= 1 for every escrow.
func (e *EscrowEngine) VerifyConservation() ConservationReport {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var totalBal uint64
	for _, b := range e.balances {
		totalBal += b
	}
	var totalLocked uint64
	var doublePayouts uint64
	for _, rec := range e.escrows {
		totalLocked += rec.LockedBuyerAtomic + rec.LockedProviderBond + rec.LockedChallengeBond
		if rec.PayoutCount > 1 {
			doublePayouts++
		}
		if rec.State.IsTerminal() && (rec.LockedBuyerAtomic != 0 || rec.LockedProviderBond != 0 || rec.LockedChallengeBond != 0) {
			doublePayouts++
		}
	}
	accounted := totalBal + totalLocked
	var drift int64
	if accounted >= e.initialMintedSupply {
		drift = int64(accounted - e.initialMintedSupply)
	} else {
		drift = -int64(e.initialMintedSupply - accounted)
	}
	return ConservationReport{
		InitialMintedSupply: e.initialMintedSupply,
		TotalAccountBalance: totalBal,
		TotalLockedEscrow:   totalLocked,
		TotalAccounted:      accounted,
		Drift:               drift,
		DoublePayoutCount:   doublePayouts,
		SubjectiveSlashes:   e.subjectiveSlashes,
		Conserved:           drift == 0 && doublePayouts == 0 && e.subjectiveSlashes == 0,
	}
}

func (e *EscrowEngine) appendLedgerLocked(eventType, jobID, account string, amount uint64, state EscrowState, detail string) {
	entry := LedgerEntry{
		Seq:       uint64(len(e.ledger) + 1),
		Timestamp: e.clock.Now().Unix(),
		EventType: eventType,
		JobID:     jobID,
		Account:   account,
		Amount:    amount,
		State:     state,
		Detail:    detail,
		PrevHash:  e.headHash,
	}
	entry.EntryHash = computeLedgerEntryHash(entry)
	e.headHash = entry.EntryHash
	e.ledger = append(e.ledger, entry)
}

func computeLedgerEntryHash(entry LedgerEntry) string {
	canon := struct {
		Seq       uint64      `json:"seq"`
		Timestamp int64       `json:"timestamp"`
		EventType string      `json:"eventType"`
		JobID     string      `json:"jobId,omitempty"`
		Account   string      `json:"account,omitempty"`
		Amount    uint64      `json:"amount,omitempty"`
		State     EscrowState `json:"state,omitempty"`
		Detail    string      `json:"detail,omitempty"`
		PrevHash  string      `json:"prevHash"`
	}{
		Seq:       entry.Seq,
		Timestamp: entry.Timestamp,
		EventType: entry.EventType,
		JobID:     entry.JobID,
		Account:   entry.Account,
		Amount:    entry.Amount,
		State:     entry.State,
		Detail:    entry.Detail,
		PrevHash:  entry.PrevHash,
	}
	raw, _ := json.Marshal(canon)
	return Keccak256Hex([]byte("escrow:ledger:v1:"), raw)
}

func computePayoutHash(p PayoutRecord) string {
	p.ReceiptHash = ""
	raw, _ := json.Marshal(p)
	return Keccak256Hex([]byte("escrow:payout:v1:"), raw)
}

func addUint64Checked(a, b uint64) (uint64, bool) {
	if math.MaxUint64-a < b {
		return 0, false
	}
	return a + b, true
}

func cloneEscrowRecord(rec *EscrowRecord) *EscrowRecord {
	if rec == nil {
		return nil
	}
	cp := *rec
	if rec.Acceptance != nil {
		a := *rec.Acceptance
		cp.Acceptance = &a
	}
	if rec.Claim != nil {
		c := *rec.Claim
		cp.Claim = &c
	}
	if rec.ServiceProof != nil {
		sp := *rec.ServiceProof
		cp.ServiceProof = &sp
	}
	if rec.ComputationProof != nil {
		comp := *rec.ComputationProof
		cp.ComputationProof = &comp
	}
	if rec.Challenge != nil {
		ch := *rec.Challenge
		if ch.Evidence.ConflictingClaim != nil {
			cc := *ch.Evidence.ConflictingClaim
			ch.Evidence.ConflictingClaim = &cc
		}
		cp.Challenge = &ch
	}
	if rec.Payout != nil {
		p := *rec.Payout
		cp.Payout = &p
	}
	return &cp
}

func sortedBalanceKeys(m map[string]uint64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
