// SPDX-License-Identifier: Apache-2.0 OR MIT

package escrow

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(unix int64) *fakeClock {
	return &fakeClock{now: time.Unix(unix, 0).UTC()}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type testActors struct {
	buyer      *KeyPair
	provider   *KeyPair
	challenger *KeyPair
}

func newTestActors(t *testing.T) testActors {
	t.Helper()
	buyer, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate buyer key: %v", err)
	}
	provider, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate provider key: %v", err)
	}
	challenger, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate challenger key: %v", err)
	}
	return testActors{buyer: buyer, provider: provider, challenger: challenger}
}

func mustSignQuoteAndFund(
	t *testing.T,
	eng *EscrowEngine,
	clk *fakeClock,
	actors testActors,
	jobID string,
	canonicalInput string,
	spec string,
	class VerificationClass,
	price, provBond, chalBond, slaPenalty uint64,
	windowSec int64,
) (*EscrowRecord, string, string) {
	t.Helper()
	now := clk.Now().Unix()
	q := JobQuote{
		JobID:               jobID,
		Buyer:               actors.buyer.Address(),
		Provider:            actors.provider.Address(),
		Asset:               "USDC",
		JobPriceAtomic:      price,
		ProviderBondAtomic:  provBond,
		ChallengeBondAtomic: chalBond,
		SLAPenaltyAtomic:    slaPenalty,
		ChallengerRewardBps: 5000, // 50% of slashed provider bond to valid challenger, 50% to buyer
		Deadline:            now + 60,
		ChallengeWindowSec:  windowSec,
		InputDigest:         ComputeInputDigest(canonicalInput),
		VerificationClass:   class,
		VerificationSpec:    spec,
		EstimatedTokens:     10_000,
		Nonce:               1,
		CreatedAt:           now,
	}
	if err := SignJobQuote(eng.Domain(), actors.provider, &q); err != nil {
		t.Fatalf("SignJobQuote: %v", err)
	}
	rec, err := eng.CreateQuote(q)
	if err != nil {
		t.Fatalf("CreateQuote: %v", err)
	}

	acc := QuoteAcceptance{
		JobID:          jobID,
		QuoteHash:      rec.Quote.QuoteHash,
		Buyer:          actors.buyer.Address(),
		JobPriceAtomic: price,
		AcceptedAt:     now,
	}
	if err := SignQuoteAcceptance(eng.Domain(), actors.buyer, &acc); err != nil {
		t.Fatalf("SignQuoteAcceptance: %v", err)
	}
	funded, err := eng.FundEscrow(acc)
	if err != nil {
		t.Fatalf("FundEscrow: %v", err)
	}

	var outDigest, execProof string
	if class != ClassNonDeterministicSampling {
		outDigest, execProof, err = ExecuteDeterministicKernel(spec, canonicalInput)
		if err != nil {
			t.Fatalf("ExecuteDeterministicKernel: %v", err)
		}
	}
	return funded, outDigest, execProof
}

func assertConserved(t *testing.T, eng *EscrowEngine) {
	t.Helper()
	rep := eng.VerifyConservation()
	if !rep.Conserved || rep.Drift != 0 || rep.DoublePayoutCount != 0 || rep.SubjectiveSlashes != 0 {
		t.Fatalf("conservation violated: %+v", rep)
	}
}

// TestHonestFulfillment verifies the full optimistic path:
// Quoted -> Funded -> Claimed -> challenge window expires -> single Settled payout + exact conservation.
func TestHonestFulfillment(t *testing.T) {
	clk := newFakeClock(1_700_000_000)
	eng := NewEscrowEngine(DefaultDomain(), clk, nil)
	actors := newTestActors(t)

	const (
		buyerInit = uint64(1_000_000)
		provInit  = uint64(500_000)
		price     = uint64(250_000)
		provBond  = uint64(100_000)
		chalBond  = uint64(50_000)
		slaPen    = uint64(40_000)
	)
	if err := eng.Mint(actors.buyer.Address(), buyerInit); err != nil {
		t.Fatal(err)
	}
	if err := eng.Mint(actors.provider.Address(), provInit); err != nil {
		t.Fatal(err)
	}
	assertConserved(t, eng)

	input := "prompt:deterministic-code-audit-v1"
	funded, outDigest, execProof := mustSignQuoteAndFund(
		t, eng, clk, actors, "job-honest-1", input, SpecHashChain64, ClassDeterministicHash,
		price, provBond, chalBond, slaPen, 30,
	)
	if funded.State != StateFunded {
		t.Fatalf("expected Funded, got %s", funded.State)
	}
	if eng.BalanceOf(actors.buyer.Address()) != buyerInit-price {
		t.Fatalf("buyer balance mismatch after fund")
	}
	if eng.BalanceOf(actors.provider.Address()) != provInit-provBond {
		t.Fatalf("provider balance mismatch after fund")
	}
	assertConserved(t, eng)

	// Provider completes honestly before deadline.
	clk.Advance(10 * time.Second)
	now := clk.Now().Unix()
	claim := CompletionClaim{
		JobID:             funded.JobID,
		QuoteHash:         funded.Quote.QuoteHash,
		Provider:          actors.provider.Address(),
		OutputDigest:      outDigest,
		ExecutionProof:    execProof,
		TokensProcessed:   10_000,
		CompletedAt:       now,
		ChallengeDeadline: now + 30,
	}
	if err := SignCompletionClaim(eng.Domain(), actors.provider, &claim); err != nil {
		t.Fatalf("SignCompletionClaim: %v", err)
	}
	claimed, err := eng.SubmitClaim(claim)
	if err != nil {
		t.Fatalf("SubmitClaim: %v", err)
	}
	if claimed.State != StateClaimed || claimed.ServiceProof == nil || !claimed.ServiceProof.OnTime || claimed.ComputationProof == nil {
		t.Fatalf("unexpected claimed record: %+v", claimed)
	}

	// Early finalize before challenge window expires must fail closed.
	if _, err := eng.Finalize(funded.JobID); !errors.Is(err, ErrChallengeWindowActive) {
		t.Fatalf("expected ErrChallengeWindowActive before window ends, got %v", err)
	}

	// Advance past challenge window -> single atomic release to provider.
	clk.Advance(31 * time.Second)
	payout, err := eng.Finalize(funded.JobID)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if payout.TerminalState != StateSettled || payout.ProviderPayoutAtomic != price+provBond {
		t.Fatalf("unexpected payout: %+v", payout)
	}

	// Second finalize MUST fail with ErrAlreadyFinalized (strict no double-release).
	if _, err := eng.Finalize(funded.JobID); !errors.Is(err, ErrAlreadyFinalized) {
		t.Fatalf("expected ErrAlreadyFinalized on second Finalize, got %v", err)
	}

	if got := eng.BalanceOf(actors.buyer.Address()); got != buyerInit-price {
		t.Fatalf("buyer final balance = %d, want %d", got, buyerInit-price)
	}
	if got := eng.BalanceOf(actors.provider.Address()); got != provInit+price {
		t.Fatalf("provider final balance = %d, want %d", got, provInit+price)
	}
	assertConserved(t, eng)
}

// TestMissedDeadlineAndWorkerCrash verifies that when a worker crashes mid-job and never submits
// a claim by Deadline, buyer atomically reclaims 100% of JobPriceAtomic + deterministic SLA penalty
// from ProviderBondAtomic, with zero frozen funds and late claims rejected.
func TestMissedDeadlineAndWorkerCrash(t *testing.T) {
	clk := newFakeClock(1_700_000_000)
	eng := NewEscrowEngine(DefaultDomain(), clk, nil)
	actors := newTestActors(t)

	const (
		buyerInit = uint64(1_000_000)
		provInit  = uint64(500_000)
		price     = uint64(300_000)
		provBond  = uint64(120_000)
		chalBond  = uint64(50_000)
		slaPen    = uint64(45_000)
	)
	_ = eng.Mint(actors.buyer.Address(), buyerInit)
	_ = eng.Mint(actors.provider.Address(), provInit)

	input := "prompt:worker-crash-recovery"
	funded, outDigest, execProof := mustSignQuoteAndFund(
		t, eng, clk, actors, "job-crash-1", input, SpecKeccak256Eval, ClassDeterministicHash,
		price, provBond, chalBond, slaPen, 30,
	)

	// Before deadline, buyer cannot refund yet.
	clk.Advance(30 * time.Second)
	if _, err := eng.RefundMissedDeadline(funded.JobID); !errors.Is(err, ErrDeadlineNotReached) {
		t.Fatalf("expected ErrDeadlineNotReached before deadline, got %v", err)
	}

	// Worker crashes and deadline passes.
	clk.Advance(35 * time.Second) // now = +65s > deadline (+60s)

	// A late claim after deadline must be rejected by objective SLA verification.
	lateClaim := CompletionClaim{
		JobID:             funded.JobID,
		QuoteHash:         funded.Quote.QuoteHash,
		Provider:          actors.provider.Address(),
		OutputDigest:      outDigest,
		ExecutionProof:    execProof,
		TokensProcessed:   10_000,
		CompletedAt:       clk.Now().Unix(),
		ChallengeDeadline: clk.Now().Unix() + 30,
	}
	_ = SignCompletionClaim(eng.Domain(), actors.provider, &lateClaim)
	if _, err := eng.SubmitClaim(lateClaim); !errors.Is(err, ErrDeadlineExceeded) {
		t.Fatalf("expected ErrDeadlineExceeded for late claim, got %v", err)
	}

	// Buyer atomically reclaims 100% of job price + deterministic SLA penalty from provider bond.
	payout, err := eng.RefundMissedDeadline(funded.JobID)
	if err != nil {
		t.Fatalf("RefundMissedDeadline: %v", err)
	}
	if payout.TerminalState != StateRefunded || payout.ProofDomain != ProofDomainServiceQuality {
		t.Fatalf("unexpected refund payout: %+v", payout)
	}
	if payout.BuyerPayoutAtomic != price+slaPen || payout.ProviderPayoutAtomic != provBond-slaPen || payout.ProviderSlashedAtomic != slaPen {
		t.Fatalf("unexpected SLA penalty split: %+v", payout)
	}

	// Second refund attempt must return ErrAlreadyFinalized.
	if _, err := eng.RefundMissedDeadline(funded.JobID); !errors.Is(err, ErrAlreadyFinalized) {
		t.Fatalf("expected ErrAlreadyFinalized on repeat refund, got %v", err)
	}

	if got := eng.BalanceOf(actors.buyer.Address()); got != buyerInit+slaPen {
		t.Fatalf("buyer final balance = %d, want %d", got, buyerInit+slaPen)
	}
	if got := eng.BalanceOf(actors.provider.Address()); got != provInit-slaPen {
		t.Fatalf("provider final balance = %d, want %d", got, provInit-slaPen)
	}
	assertConserved(t, eng)
}

// TestFalseCompletionClaimSlashed verifies that when a dishonest provider submits a bogus
// OutputDigest, an independent challenger submits objective recomputation evidence -> provider
// bond is slashed, buyer is refunded, and challenger receives their bond + reward.
func TestFalseCompletionClaimSlashed(t *testing.T) {
	clk := newFakeClock(1_700_000_000)
	eng := NewEscrowEngine(DefaultDomain(), clk, nil)
	actors := newTestActors(t)

	const (
		buyerInit = uint64(1_000_000)
		provInit  = uint64(500_000)
		chalInit  = uint64(200_000)
		price     = uint64(400_000)
		provBond  = uint64(100_000)
		chalBond  = uint64(50_000)
		slaPen    = uint64(30_000)
	)
	_ = eng.Mint(actors.buyer.Address(), buyerInit)
	_ = eng.Mint(actors.provider.Address(), provInit)
	_ = eng.Mint(actors.challenger.Address(), chalInit)

	input := "prompt:verifiable-hashchain-job"
	funded, _, _ := mustSignQuoteAndFund(
		t, eng, clk, actors, "job-false-claim-1", input, SpecHashChain64, ClassDeterministicHash,
		price, provBond, chalBond, slaPen, 60,
	)

	// Dishonest provider submits a fabricated OutputDigest instead of executing the kernel.
	clk.Advance(5 * time.Second)
	now := clk.Now().Unix()
	bogusClaim := CompletionClaim{
		JobID:             funded.JobID,
		QuoteHash:         funded.Quote.QuoteHash,
		Provider:          actors.provider.Address(),
		OutputDigest:      Keccak256Hex([]byte("fabricated-bogus-output")),
		ExecutionProof:    Keccak256Hex([]byte("fabricated-proof")),
		TokensProcessed:   10_000,
		CompletedAt:       now,
		ChallengeDeadline: now + 60,
	}
	if err := SignCompletionClaim(eng.Domain(), actors.provider, &bogusClaim); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubmitClaim(bogusClaim); err != nil {
		t.Fatalf("SubmitClaim: %v", err)
	}

	// Independent challenger posts bond + objective recomputation input.
	clk.Advance(5 * time.Second)
	sub := ChallengeSubmission{
		JobID:               funded.JobID,
		Challenger:          actors.challenger.Address(),
		ChallengeBondAtomic: chalBond,
		SubmittedAt:         clk.Now().Unix(),
		Evidence: ChallengeEvidence{
			ViolationType:  ViolationFalseCompletionClaim,
			CanonicalInput: input,
		},
	}
	if err := SignChallengeSubmission(eng.Domain(), actors.challenger, &sub); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.OpenChallenge(sub); err != nil {
		t.Fatalf("OpenChallenge: %v", err)
	}

	// ResolveChallenge independently recomputes kernel and slashes the dishonest provider.
	payout, err := eng.ResolveChallenge(funded.JobID)
	if err != nil {
		t.Fatalf("ResolveChallenge: %v", err)
	}
	if payout.TerminalState != StateSlashed || payout.ChallengeOutcome != ChallengeOutcomeUpheldSlashed {
		t.Fatalf("expected StateSlashed/UpheldSlashed, got %+v", payout)
	}
	if payout.ProofDomain != ProofDomainComputationIntegrity {
		t.Fatalf("expected computation_correctness proof domain, got %s", payout.ProofDomain)
	}

	expectedChalReward := provBond / 2 // 5000 bps
	expectedBuyerComp := provBond - expectedChalReward
	if payout.BuyerPayoutAtomic != price+expectedBuyerComp {
		t.Fatalf("buyer payout = %d, want %d", payout.BuyerPayoutAtomic, price+expectedBuyerComp)
	}
	if payout.ChallengerPayoutAtomic != chalBond+expectedChalReward {
		t.Fatalf("challenger payout = %d, want %d", payout.ChallengerPayoutAtomic, chalBond+expectedChalReward)
	}
	if payout.ProviderPayoutAtomic != 0 || payout.ProviderSlashedAtomic != provBond {
		t.Fatalf("provider should receive 0 and be slashed %d, got %+v", provBond, payout)
	}

	if got := eng.BalanceOf(actors.buyer.Address()); got != buyerInit+expectedBuyerComp {
		t.Fatalf("buyer balance = %d, want %d", got, buyerInit+expectedBuyerComp)
	}
	if got := eng.BalanceOf(actors.challenger.Address()); got != chalInit+expectedChalReward {
		t.Fatalf("challenger balance = %d, want %d", got, chalInit+expectedChalReward)
	}
	if got := eng.BalanceOf(actors.provider.Address()); got != provInit-provBond {
		t.Fatalf("provider balance = %d, want %d", got, provInit-provBond)
	}
	assertConserved(t, eng)
}

// TestMaliciousChallengerPenalized verifies economic griefing defense:
// when a malicious challenger opens an objective challenge against a valid, honest fulfillment,
// independent verification confirms the provider's output -> challenger bond is forfeited to the
// honest provider and the provider receives full payment + returned bond + forfeited challenger bond.
func TestMaliciousChallengerPenalized(t *testing.T) {
	clk := newFakeClock(1_700_000_000)
	eng := NewEscrowEngine(DefaultDomain(), clk, nil)
	actors := newTestActors(t)

	const (
		buyerInit = uint64(1_000_000)
		provInit  = uint64(500_000)
		chalInit  = uint64(200_000)
		price     = uint64(350_000)
		provBond  = uint64(100_000)
		chalBond  = uint64(75_000)
		slaPen    = uint64(25_000)
	)
	_ = eng.Mint(actors.buyer.Address(), buyerInit)
	_ = eng.Mint(actors.provider.Address(), provInit)
	_ = eng.Mint(actors.challenger.Address(), chalInit)

	input := "prompt:honest-deterministic-eval"
	funded, outDigest, execProof := mustSignQuoteAndFund(
		t, eng, clk, actors, "job-grief-1", input, SpecSeededTokenTransform, ClassDeterministicHash,
		price, provBond, chalBond, slaPen, 60,
	)

	clk.Advance(8 * time.Second)
	now := clk.Now().Unix()
	honestClaim := CompletionClaim{
		JobID:             funded.JobID,
		QuoteHash:         funded.Quote.QuoteHash,
		Provider:          actors.provider.Address(),
		OutputDigest:      outDigest,
		ExecutionProof:    execProof,
		TokensProcessed:   10_000,
		CompletedAt:       now,
		ChallengeDeadline: now + 60,
	}
	_ = SignCompletionClaim(eng.Domain(), actors.provider, &honestClaim)
	if _, err := eng.SubmitClaim(honestClaim); err != nil {
		t.Fatalf("SubmitClaim: %v", err)
	}

	// Malicious challenger files a false fraud challenge against the honest provider.
	clk.Advance(4 * time.Second)
	griefSub := ChallengeSubmission{
		JobID:               funded.JobID,
		Challenger:          actors.challenger.Address(),
		ChallengeBondAtomic: chalBond,
		SubmittedAt:         clk.Now().Unix(),
		Evidence: ChallengeEvidence{
			ViolationType:  ViolationFalseCompletionClaim,
			CanonicalInput: input,
		},
	}
	_ = SignChallengeSubmission(eng.Domain(), actors.challenger, &griefSub)
	if _, err := eng.OpenChallenge(griefSub); err != nil {
		t.Fatalf("OpenChallenge: %v", err)
	}

	payout, err := eng.ResolveChallenge(funded.JobID)
	if err != nil {
		t.Fatalf("ResolveChallenge: %v", err)
	}
	if payout.TerminalState != StateClaimed || payout.ChallengeOutcome != ChallengeOutcomeRejectedMalicious {
		t.Fatalf("expected StateClaimed/RejectedMalicious while dispute window remains open, got %+v", payout)
	}
	if payout.ProviderPayoutAtomic != chalBond || payout.ChallengerSlashedAtomic != chalBond || payout.ChallengerPayoutAtomic != 0 {
		t.Fatalf("expected malicious challenger bond forfeited to provider without premature escrow release, got %+v", payout)
	}

	// Finalize must still wait until ChallengeDeadline elapses.
	if _, err := eng.Finalize(funded.JobID); !errors.Is(err, ErrChallengeWindowActive) {
		t.Fatalf("expected ErrChallengeWindowActive before challenge deadline, got %v", err)
	}
	clk.Advance(60 * time.Second)
	finPayout, err := eng.Finalize(funded.JobID)
	if err != nil {
		t.Fatalf("Finalize after challenge window: %v", err)
	}
	if finPayout.TerminalState != StateSettled || finPayout.ProviderPayoutAtomic != price+provBond {
		t.Fatalf("expected StateSettled with price+provBond, got %+v", finPayout)
	}

	if got := eng.BalanceOf(actors.provider.Address()); got != provInit+price+chalBond {
		t.Fatalf("provider final balance = %d, want %d", got, provInit+price+chalBond)
	}
	if got := eng.BalanceOf(actors.challenger.Address()); got != chalInit-chalBond {
		t.Fatalf("challenger final balance = %d, want %d", got, chalInit-chalBond)
	}
	if got := eng.BalanceOf(actors.buyer.Address()); got != buyerInit-price {
		t.Fatalf("buyer final balance = %d, want %d", got, buyerInit-price)
	}
	assertConserved(t, eng)
}

// TestSubjectiveClaimRejected verifies the critical invariant from hanzoai/cloud#17:
// funds MUST NEVER be seized on a merely subjective quality claim.
func TestSubjectiveClaimRejected(t *testing.T) {
	clk := newFakeClock(1_700_000_000)
	eng := NewEscrowEngine(DefaultDomain(), clk, nil)
	actors := newTestActors(t)

	const (
		buyerInit = uint64(1_000_000)
		provInit  = uint64(500_000)
		chalInit  = uint64(200_000)
		price     = uint64(200_000)
		provBond  = uint64(80_000)
		chalBond  = uint64(40_000)
	)
	_ = eng.Mint(actors.buyer.Address(), buyerInit)
	_ = eng.Mint(actors.provider.Address(), provInit)
	_ = eng.Mint(actors.challenger.Address(), chalInit)

	input := "prompt:write-concise-summary"
	funded, outDigest, execProof := mustSignQuoteAndFund(
		t, eng, clk, actors, "job-subjective-1", input, SpecKeccak256Eval, ClassDeterministicHash,
		price, provBond, chalBond, 20_000, 45,
	)

	clk.Advance(5 * time.Second)
	now := clk.Now().Unix()
	claim := CompletionClaim{
		JobID:             funded.JobID,
		QuoteHash:         funded.Quote.QuoteHash,
		Provider:          actors.provider.Address(),
		OutputDigest:      outDigest,
		ExecutionProof:    execProof,
		TokensProcessed:   8_000,
		CompletedAt:       now,
		ChallengeDeadline: now + 45,
	}
	_ = SignCompletionClaim(eng.Domain(), actors.provider, &claim)
	if _, err := eng.SubmitClaim(claim); err != nil {
		t.Fatalf("SubmitClaim: %v", err)
	}

	// Attempt subjective quality challenge ("output tone was not helpful enough").
	subjSub := ChallengeSubmission{
		JobID:               funded.JobID,
		Challenger:          actors.challenger.Address(),
		ChallengeBondAtomic: chalBond,
		SubmittedAt:         clk.Now().Unix(),
		Evidence: ChallengeEvidence{
			ViolationType:  ViolationSubjectiveQuality,
			SubjectiveNote: "output style felt too terse and not helpful enough",
		},
	}
	_ = SignChallengeSubmission(eng.Domain(), actors.challenger, &subjSub)
	if _, err := eng.OpenChallenge(subjSub); !errors.Is(err, ErrSubjectiveSlashingForbidden) {
		t.Fatalf("expected ErrSubjectiveSlashingForbidden, got %v", err)
	}

	// Unknown / arbitrary violation types must also fail closed.
	unknownSub := subjSub
	unknownSub.Evidence.ViolationType = ViolationType("vibes_check_failed")
	_ = SignChallengeSubmission(eng.Domain(), actors.challenger, &unknownSub)
	if _, err := eng.OpenChallenge(unknownSub); !errors.Is(err, ErrUnknownViolationType) {
		t.Fatalf("expected ErrUnknownViolationType, got %v", err)
	}

	// Verify escrow remains cleanly in StateClaimed, challenger balance untouched, zero funds seized.
	rec, err := eng.GetEscrow(funded.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != StateClaimed || rec.Challenge != nil || rec.LockedChallengeBond != 0 {
		t.Fatalf("subjective rejection mutated escrow state: %+v", rec)
	}
	if got := eng.BalanceOf(actors.challenger.Address()); got != chalInit {
		t.Fatalf("challenger balance changed on rejected subjective claim: %d != %d", got, chalInit)
	}

	// Provider can still finalize honestly after challenge window expires.
	clk.Advance(46 * time.Second)
	payout, err := eng.Finalize(funded.JobID)
	if err != nil {
		t.Fatalf("Finalize after rejected subjective claim: %v", err)
	}
	if payout.TerminalState != StateSettled || payout.ProviderSlashedAtomic != 0 {
		t.Fatalf("unexpected payout after rejected subjective claim: %+v", payout)
	}
	assertConserved(t, eng)
}

// TestDoublePaymentAndConcurrentReleaseRace launches 16 goroutines simultaneously racing
// Finalize, RefundMissedDeadline, and ResolveChallenge against the same escrow under -race.
// Exactly 1 payout MUST succeed; all others MUST return ErrAlreadyFinalized (or invalid state),
// and VerifyConservation() MUST hold with 0 wei drift and 0 double payouts.
func TestDoublePaymentAndConcurrentReleaseRace(t *testing.T) {
	clk := newFakeClock(1_700_000_000)
	eng := NewEscrowEngine(DefaultDomain(), clk, nil)
	actors := newTestActors(t)

	const (
		buyerInit = uint64(5_000_000)
		provInit  = uint64(2_000_000)
		chalInit  = uint64(1_000_000)
		price     = uint64(500_000)
		provBond  = uint64(150_000)
		chalBond  = uint64(50_000)
	)
	_ = eng.Mint(actors.buyer.Address(), buyerInit)
	_ = eng.Mint(actors.provider.Address(), provInit)
	_ = eng.Mint(actors.challenger.Address(), chalInit)

	// Sub-case A: 16 goroutines racing Finalize / RefundMissedDeadline / ResolveChallenge on a Claimed job.
	t.Run("concurrent_finalize_race", func(t *testing.T) {
		funded, outDigest, execProof := mustSignQuoteAndFund(
			t, eng, clk, actors, "job-race-finalize", "race-input-A", SpecKeccak256Eval, ClassDeterministicHash,
			price, provBond, chalBond, 50_000, 15,
		)
		now := clk.Now().Unix()
		claim := CompletionClaim{
			JobID:             funded.JobID,
			QuoteHash:         funded.Quote.QuoteHash,
			Provider:          actors.provider.Address(),
			OutputDigest:      outDigest,
			ExecutionProof:    execProof,
			TokensProcessed:   10_000,
			CompletedAt:       now,
			ChallengeDeadline: now + 15,
		}
		_ = SignCompletionClaim(eng.Domain(), actors.provider, &claim)
		if _, err := eng.SubmitClaim(claim); err != nil {
			t.Fatal(err)
		}
		clk.Advance(20 * time.Second)

		const goroutines = 16
		var wg sync.WaitGroup
		start := make(chan struct{})
		var successes int64
		var alreadyFinalized int64

		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-start
				var err error
				switch idx % 3 {
				case 0:
					_, err = eng.Finalize(funded.JobID)
				case 1:
					_, err = eng.RefundMissedDeadline(funded.JobID)
				case 2:
					_, err = eng.ResolveChallenge(funded.JobID)
				}
				if err == nil {
					atomic.AddInt64(&successes, 1)
				} else if errors.Is(err, ErrAlreadyFinalized) {
					atomic.AddInt64(&alreadyFinalized, 1)
				}
			}(i)
		}
		close(start)
		wg.Wait()

		if successes != 1 {
			t.Fatalf("expected exactly 1 winning payout, got %d", successes)
		}
		// Follow-up calls after barrier must all observe ErrAlreadyFinalized.
		if _, err := eng.Finalize(funded.JobID); !errors.Is(err, ErrAlreadyFinalized) {
			t.Fatalf("post-race Finalize expected ErrAlreadyFinalized, got %v", err)
		}
		if _, err := eng.RefundMissedDeadline(funded.JobID); !errors.Is(err, ErrAlreadyFinalized) {
			t.Fatalf("post-race Refund expected ErrAlreadyFinalized, got %v", err)
		}
		if _, err := eng.ResolveChallenge(funded.JobID); !errors.Is(err, ErrAlreadyFinalized) {
			t.Fatalf("post-race Resolve expected ErrAlreadyFinalized, got %v", err)
		}
		rec, _ := eng.GetEscrow(funded.JobID)
		if rec.PayoutCount != 1 {
			t.Fatalf("expected PayoutCount == 1, got %d", rec.PayoutCount)
		}
		assertConserved(t, eng)
	})

	// Sub-case B: 16 goroutines racing ResolveChallenge / Finalize / Refund on a Challenged job.
	t.Run("concurrent_resolve_race", func(t *testing.T) {
		funded, _, _ := mustSignQuoteAndFund(
			t, eng, clk, actors, "job-race-resolve", "race-input-B", SpecHashChain64, ClassDeterministicHash,
			price, provBond, chalBond, 50_000, 30,
		)
		now := clk.Now().Unix()
		badClaim := CompletionClaim{
			JobID:             funded.JobID,
			QuoteHash:         funded.Quote.QuoteHash,
			Provider:          actors.provider.Address(),
			OutputDigest:      Keccak256Hex([]byte("bogus-concurrent")),
			ExecutionProof:    Keccak256Hex([]byte("bogus-proof")),
			TokensProcessed:   10_000,
			CompletedAt:       now,
			ChallengeDeadline: now + 30,
		}
		_ = SignCompletionClaim(eng.Domain(), actors.provider, &badClaim)
		if _, err := eng.SubmitClaim(badClaim); err != nil {
			t.Fatal(err)
		}
		sub := ChallengeSubmission{
			JobID:               funded.JobID,
			Challenger:          actors.challenger.Address(),
			ChallengeBondAtomic: chalBond,
			SubmittedAt:         now,
			Evidence: ChallengeEvidence{
				ViolationType:  ViolationFalseCompletionClaim,
				CanonicalInput: "race-input-B",
			},
		}
		_ = SignChallengeSubmission(eng.Domain(), actors.challenger, &sub)
		if _, err := eng.OpenChallenge(sub); err != nil {
			t.Fatal(err)
		}

		const goroutines = 16
		var wg sync.WaitGroup
		start := make(chan struct{})
		var successes int64
		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-start
				var err error
				switch idx % 3 {
				case 0:
					_, err = eng.ResolveChallenge(funded.JobID)
				case 1:
					_, err = eng.Finalize(funded.JobID)
				case 2:
					_, err = eng.RefundMissedDeadline(funded.JobID)
				}
				if err == nil {
					atomic.AddInt64(&successes, 1)
				}
			}(i)
		}
		close(start)
		wg.Wait()

		if successes != 1 {
			t.Fatalf("expected exactly 1 winning resolve payout, got %d", successes)
		}
		rec, _ := eng.GetEscrow(funded.JobID)
		if rec.PayoutCount != 1 || rec.State != StateSlashed {
			t.Fatalf("unexpected post-race challenged record: state=%s payoutCount=%d", rec.State, rec.PayoutCount)
		}
		assertConserved(t, eng)
	})
}

// TestEquivocationProofSlashed verifies that signing two conflicting CompletionClaims with
// distinct OutputDigests for the same JobID is independently slashable without re-executing the job.
func TestEquivocationProofSlashed(t *testing.T) {
	clk := newFakeClock(1_700_000_000)
	eng := NewEscrowEngine(DefaultDomain(), clk, nil)
	actors := newTestActors(t)

	_ = eng.Mint(actors.buyer.Address(), 1_000_000)
	_ = eng.Mint(actors.provider.Address(), 500_000)
	_ = eng.Mint(actors.challenger.Address(), 200_000)

	// Even on a non-deterministic sampling job, cryptographic equivocation IS objectively slashable!
	funded, _, _ := mustSignQuoteAndFund(
		t, eng, clk, actors, "job-equiv-1", "prompt:non-det-equivocation", SpecNonDeterministicLLM, ClassNonDeterministicSampling,
		200_000, 100_000, 40_000, 30_000, 60,
	)

	now := clk.Now().Unix()
	claimA := CompletionClaim{
		JobID:             funded.JobID,
		QuoteHash:         funded.Quote.QuoteHash,
		Provider:          actors.provider.Address(),
		OutputDigest:      Keccak256Hex([]byte("output-version-A")),
		ExecutionProof:    "trace-A",
		TokensProcessed:   5_000,
		CompletedAt:       now,
		ChallengeDeadline: now + 60,
	}
	_ = SignCompletionClaim(eng.Domain(), actors.provider, &claimA)
	if _, err := eng.SubmitClaim(claimA); err != nil {
		t.Fatal(err)
	}

	// Same provider signs a conflicting second output for the same JobID.
	claimB := claimA
	claimB.OutputDigest = Keccak256Hex([]byte("output-version-B"))
	_ = SignCompletionClaim(eng.Domain(), actors.provider, &claimB)

	sub := ChallengeSubmission{
		JobID:               funded.JobID,
		Challenger:          actors.challenger.Address(),
		ChallengeBondAtomic: 40_000,
		SubmittedAt:         now + 1,
		Evidence: ChallengeEvidence{
			ViolationType:    ViolationInvalidSignatureOrEquivocation,
			ConflictingClaim: &claimB,
		},
	}
	_ = SignChallengeSubmission(eng.Domain(), actors.challenger, &sub)
	if _, err := eng.OpenChallenge(sub); err != nil {
		t.Fatalf("OpenChallenge equivocation: %v", err)
	}
	payout, err := eng.ResolveChallenge(funded.JobID)
	if err != nil {
		t.Fatalf("ResolveChallenge equivocation: %v", err)
	}
	if payout.TerminalState != StateSlashed || payout.ProviderSlashedAtomic != 100_000 {
		t.Fatalf("expected equivocating provider to be slashed, got %+v", payout)
	}
	assertConserved(t, eng)
}

// TestNegativeBoundaryRegimes verifies all 3 required honest negative & boundary regimes:
//  1. zero_bond_griefing_negative
//  2. unverified_instant_release_negative
//  3. non_deterministic_llm_sampling_boundary
func TestNegativeBoundaryRegimes(t *testing.T) {
	// 1. DefaultPolicy rejects zero challenge bond; ablation policy allows free griefing.
	t.Run("zero_bond_griefing_negative", func(t *testing.T) {
		clk := newFakeClock(1_700_000_000)
		actors := newTestActors(t)

		safeEng := NewEscrowEngine(DefaultDomain(), clk, nil)
		q := JobQuote{
			JobID:               "job-zero-bond-safe",
			Buyer:               actors.buyer.Address(),
			Provider:            actors.provider.Address(),
			Asset:               "USDC",
			JobPriceAtomic:      100_000,
			ProviderBondAtomic:  50_000,
			ChallengeBondAtomic: 0, // forbidden under DefaultPolicy
			SLAPenaltyAtomic:    10_000,
			ChallengerRewardBps: 5000,
			Deadline:            clk.Now().Unix() + 60,
			ChallengeWindowSec:  30,
			InputDigest:         ComputeInputDigest("x"),
			VerificationClass:   ClassDeterministicHash,
			VerificationSpec:    SpecKeccak256Eval,
			Nonce:               1,
		}
		_ = SignJobQuote(safeEng.Domain(), actors.provider, &q)
		if _, err := safeEng.CreateQuote(q); !errors.Is(err, ErrChallengeBondRequired) {
			t.Fatalf("DefaultPolicy must reject 0 challenge bond, got %v", err)
		}

		// Ablation with RequireChallengeBond=false lets attacker grief with 0 loss!
		ablationPol := Policy{RequireChallengeBond: false, AllowInstantRelease: false, MinChallengeWindowSec: 1}
		griefEng := NewEscrowEngine(DefaultDomain(), clk, &ablationPol)
		_ = griefEng.Mint(actors.buyer.Address(), 500_000)
		_ = griefEng.Mint(actors.provider.Address(), 500_000)
		// Notice challenger has 0 balance and can still grief!
		funded, outD, proof := mustSignQuoteAndFund(
			t, griefEng, clk, actors, "job-zero-bond-ablation", "in", SpecKeccak256Eval, ClassDeterministicHash,
			100_000, 50_000, 0, 10_000, 30,
		)
		now := clk.Now().Unix()
		claim := CompletionClaim{
			JobID:             funded.JobID,
			QuoteHash:         funded.Quote.QuoteHash,
			Provider:          actors.provider.Address(),
			OutputDigest:      outD,
			ExecutionProof:    proof,
			CompletedAt:       now,
			ChallengeDeadline: now + 30,
		}
		_ = SignCompletionClaim(griefEng.Domain(), actors.provider, &claim)
		if _, err := griefEng.SubmitClaim(claim); err != nil {
			t.Fatal(err)
		}
		sub := ChallengeSubmission{
			JobID:               funded.JobID,
			Challenger:          actors.challenger.Address(),
			ChallengeBondAtomic: 0,
			SubmittedAt:         now,
			Evidence:            ChallengeEvidence{ViolationType: ViolationFalseCompletionClaim, CanonicalInput: "in"},
		}
		_ = SignChallengeSubmission(griefEng.Domain(), actors.challenger, &sub)
		if _, err := griefEng.OpenChallenge(sub); err != nil {
			t.Fatalf("ablation zero-bond griefing should open: %v", err)
		}
		payout, err := griefEng.ResolveChallenge(funded.JobID)
		if err != nil {
			t.Fatal(err)
		}
		if payout.ChallengerSlashedAtomic != 0 {
			t.Fatalf("expected 0 economic penalty under zero-bond ablation, got %d", payout.ChallengerSlashedAtomic)
		}
		assertConserved(t, griefEng)
	})

	// 2. Skipping challenge window (ChallengeWindowSec == 0) allows false claims to steal buyer funds.
	t.Run("unverified_instant_release_negative", func(t *testing.T) {
		clk := newFakeClock(1_700_000_000)
		actors := newTestActors(t)
		instantPol := Policy{RequireChallengeBond: true, AllowInstantRelease: true, MinChallengeWindowSec: 0}
		eng := NewEscrowEngine(DefaultDomain(), clk, &instantPol)
		_ = eng.Mint(actors.buyer.Address(), 500_000)
		_ = eng.Mint(actors.provider.Address(), 500_000)

		funded, _, _ := mustSignQuoteAndFund(
			t, eng, clk, actors, "job-instant-release", "in", SpecKeccak256Eval, ClassDeterministicHash,
			200_000, 50_000, 25_000, 10_000, 0,
		)
		now := clk.Now().Unix()
		bogus := CompletionClaim{
			JobID:             funded.JobID,
			QuoteHash:         funded.Quote.QuoteHash,
			Provider:          actors.provider.Address(),
			OutputDigest:      Keccak256Hex([]byte("stolen-instant")),
			ExecutionProof:    "bogus",
			CompletedAt:       now,
			ChallengeDeadline: now, // 0-second challenge window!
		}
		_ = SignCompletionClaim(eng.Domain(), actors.provider, &bogus)
		if _, err := eng.SubmitClaim(bogus); err != nil {
			t.Fatal(err)
		}
		// Dishonest provider immediately steals funds before any challenger can respond.
		payout, err := eng.Finalize(funded.JobID)
		if err != nil {
			t.Fatalf("instant release finalize: %v", err)
		}
		if payout.TerminalState != StateSettled || payout.ProviderPayoutAtomic != 250_000 {
			t.Fatalf("expected instant release theft to succeed in negative regime, got %+v", payout)
		}
		assertConserved(t, eng)
	})

	// 3. Non-deterministic LLM sampling boundary refuses raw bitwise output-equality slashing.
	t.Run("non_deterministic_llm_sampling_boundary", func(t *testing.T) {
		clk := newFakeClock(1_700_000_000)
		eng := NewEscrowEngine(DefaultDomain(), clk, nil)
		actors := newTestActors(t)
		_ = eng.Mint(actors.buyer.Address(), 500_000)
		_ = eng.Mint(actors.provider.Address(), 500_000)
		_ = eng.Mint(actors.challenger.Address(), 100_000)

		funded, _, _ := mustSignQuoteAndFund(
			t, eng, clk, actors, "job-nondet-llm", "prompt:creative-poem-temp-0.9", SpecNonDeterministicLLM, ClassNonDeterministicSampling,
			150_000, 50_000, 25_000, 15_000, 30,
		)
		now := clk.Now().Unix()
		claim := CompletionClaim{
			JobID:             funded.JobID,
			QuoteHash:         funded.Quote.QuoteHash,
			Provider:          actors.provider.Address(),
			OutputDigest:      Keccak256Hex([]byte("stochastic-sample-run-1")),
			ExecutionProof:    "sampling:temp=0.9",
			CompletedAt:       now,
			ChallengeDeadline: now + 30,
		}
		_ = SignCompletionClaim(eng.Domain(), actors.provider, &claim)
		if _, err := eng.SubmitClaim(claim); err != nil {
			t.Fatal(err)
		}

		sub := ChallengeSubmission{
			JobID:               funded.JobID,
			Challenger:          actors.challenger.Address(),
			ChallengeBondAtomic: 25_000,
			SubmittedAt:         now + 1,
			Evidence: ChallengeEvidence{
				ViolationType:  ViolationFalseCompletionClaim,
				CanonicalInput: "prompt:creative-poem-temp-0.9",
			},
		}
		_ = SignChallengeSubmission(eng.Domain(), actors.challenger, &sub)
		if _, err := eng.OpenChallenge(sub); !errors.Is(err, ErrNonDeterministicVerificationNeeded) {
			t.Fatalf("expected ErrNonDeterministicVerificationNeeded for raw bitwise challenge on temp>0 LLM job, got %v", err)
		}
		assertConserved(t, eng)
	})
}

// TestZIPHTTPServiceAndSovereignLedgerAudit tests the standalone /v1/escrow/* zip HTTP mount,
// JSONL ledger export, offline cryptographic audit, tamper detection, and full sovereign migration.
func TestZIPHTTPServiceAndSovereignLedgerAudit(t *testing.T) {
	clk := newFakeClock(1_700_000_000)
	eng := NewEscrowEngine(DefaultDomain(), clk, nil)
	actors := newTestActors(t)

	app := zip.New(zip.Config{})
	if err := Mount(app, nil, eng); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	postJSON := func(path string, body any, wantCode int) []byte {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		if resp.StatusCode != wantCode {
			t.Fatalf("POST %s status = %d, want %d; body=%s", path, resp.StatusCode, wantCode, buf.String())
		}
		return buf.Bytes()
	}

	// Mint buyer and provider via HTTP.
	postJSON("/v1/escrow/accounts/mint", MintIn{Address: actors.buyer.Address(), Amount: 1_000_000}, http.StatusOK)
	postJSON("/v1/escrow/accounts/mint", MintIn{Address: actors.provider.Address(), Amount: 500_000}, http.StatusOK)
	postJSON("/v1/escrow/accounts/mint", MintIn{Address: actors.challenger.Address(), Amount: 200_000}, http.StatusOK)

	// Create quote -> Fund -> Claim via HTTP.
	now := clk.Now().Unix()
	input := "http-e2e-compute-job"
	outD, proof, _ := ExecuteDeterministicKernel(SpecKeccak256Eval, input)
	q := JobQuote{
		JobID:               "job-http-1",
		Buyer:               actors.buyer.Address(),
		Provider:            actors.provider.Address(),
		Asset:               "USDC",
		JobPriceAtomic:      200_000,
		ProviderBondAtomic:  80_000,
		ChallengeBondAtomic: 40_000,
		SLAPenaltyAtomic:    20_000,
		ChallengerRewardBps: 5000,
		Deadline:            now + 60,
		ChallengeWindowSec:  30,
		InputDigest:         ComputeInputDigest(input),
		VerificationClass:   ClassDeterministicHash,
		VerificationSpec:    SpecKeccak256Eval,
		EstimatedTokens:     10_000,
		Nonce:               7,
		CreatedAt:           now,
	}
	_ = SignJobQuote(eng.Domain(), actors.provider, &q)
	postJSON("/v1/escrow/quotes", q, http.StatusOK)

	acc := QuoteAcceptance{
		JobID:          q.JobID,
		QuoteHash:      q.QuoteHash,
		Buyer:          actors.buyer.Address(),
		JobPriceAtomic: q.JobPriceAtomic,
		AcceptedAt:     now,
	}
	_ = SignQuoteAcceptance(eng.Domain(), actors.buyer, &acc)
	postJSON("/v1/escrow/fund", acc, http.StatusOK)

	claim := CompletionClaim{
		JobID:             q.JobID,
		QuoteHash:         q.QuoteHash,
		Provider:          actors.provider.Address(),
		OutputDigest:      outD,
		ExecutionProof:    proof,
		TokensProcessed:   10_000,
		CompletedAt:       now + 5,
		ChallengeDeadline: now + 35,
	}
	_ = SignCompletionClaim(eng.Domain(), actors.provider, &claim)
	postJSON("/v1/escrow/claim", claim, http.StatusOK)

	// Subjective quality challenge over HTTP MUST return HTTP 422 Unprocessable Entity.
	subj := ChallengeSubmission{
		JobID:               q.JobID,
		Challenger:          actors.challenger.Address(),
		ChallengeBondAtomic: 40_000,
		SubmittedAt:         now + 6,
		Evidence:            ChallengeEvidence{ViolationType: ViolationSubjectiveQuality, SubjectiveNote: "not pretty"},
	}
	_ = SignChallengeSubmission(eng.Domain(), actors.challenger, &subj)
	postJSON("/v1/escrow/challenge", subj, http.StatusUnprocessableEntity)

	// Finalize after challenge window expires.
	clk.Advance(40 * time.Second)
	postJSON("/v1/escrow/finalize", JobActionIn{JobID: q.JobID}, http.StatusOK)

	// Export JSONL settlement ledger over HTTP and audit completely offline.
	getReq := httptest.NewRequest(http.MethodGet, "/v1/escrow/ledger", nil)
	resp, err := app.Test(getReq, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("GET /v1/escrow/ledger: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/escrow/ledger status = %d, want 200", resp.StatusCode)
	}
	var ledOut LedgerExportOut
	if err := json.NewDecoder(resp.Body).Decode(&ledOut); err != nil {
		t.Fatalf("decode ledger export: %v", err)
	}

	audit, err := VerifyLedgerJSONLOffline(strings.NewReader(ledOut.JSONL))
	if err != nil || !audit.ChainValid || audit.SettledCount != 1 {
		t.Fatalf("offline ledger audit failed: audit=%+v err=%v", audit, err)
	}

	// Tampering with a single field in the JSONL ledger MUST be detected offline.
	tampered := strings.Replace(ledOut.JSONL, "200000", "200001", 1)
	if _, err := VerifyLedgerJSONLOffline(strings.NewReader(tampered)); !errors.Is(err, ErrLedgerTampered) {
		t.Fatalf("expected ErrLedgerTampered on modified ledger, got %v", err)
	}

	// Full sovereign snapshot export -> offline verify -> import into independent engine.
	var snapBuf bytes.Buffer
	if err := eng.ExportSnapshotJSONL(&snapBuf); err != nil {
		t.Fatalf("ExportSnapshotJSONL: %v", err)
	}
	migrated, err := ImportSnapshotJSONL(bytes.NewReader(snapBuf.Bytes()), clk)
	if err != nil {
		t.Fatalf("ImportSnapshotJSONL: %v", err)
	}
	if migrated.HeadHash() != eng.HeadHash() || migrated.BalanceOf(actors.provider.Address()) != eng.BalanceOf(actors.provider.Address()) {
		t.Fatalf("migrated engine state mismatch")
	}
	assertConserved(t, migrated)

	// Tampered snapshot must be rejected on import.
	tamperedSnap := strings.Replace(snapBuf.String(), "job-http-1", "job-http-X", 1)
	if _, err := ImportSnapshotJSONL(strings.NewReader(tamperedSnap), clk); !errors.Is(err, ErrLedgerTampered) {
		t.Fatalf("expected ErrLedgerTampered on tampered snapshot, got %v", err)
	}
}

func TestBackdatedClaimCannotShortenChallengeWindow(t *testing.T) {
	clk := newFakeClock(1_700_000_000)
	eng := NewEscrowEngine(DefaultDomain(), clk, nil)
	actors := newTestActors(t)
	_ = eng.Mint(actors.buyer.Address(), 1_000_000)
	_ = eng.Mint(actors.provider.Address(), 1_000_000)

	funded, _, _ := mustSignQuoteAndFund(
		t, eng, clk, actors, "job-backdated-1", "input-backdate", SpecKeccak256Eval, ClassDeterministicHash,
		200_000, 100_000, 50_000, 20_000, 60,
	)

	// Provider backdates CompletedAt by 120s so ChallengeDeadline == CompletedAt + 60 is ALREADY expired at receipt time.
	now := clk.Now().Unix()
	backdated := CompletionClaim{
		JobID:             funded.JobID,
		QuoteHash:         funded.Quote.QuoteHash,
		Provider:          actors.provider.Address(),
		OutputDigest:      Keccak256Hex([]byte("fraudulent-backdated")),
		ExecutionProof:    Keccak256Hex([]byte("fraudulent-proof")),
		CompletedAt:       now - 120,
		ChallengeDeadline: (now - 120) + 60,
	}
	if err := SignCompletionClaim(eng.Domain(), actors.provider, &backdated); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubmitClaim(backdated); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest for backdated claim with challengeDeadline < receipt+window, got %v", err)
	}

	// Claim anchored to receipt time (ChallengeDeadline >= now + 60) succeeds cleanly.
	valid := backdated
	valid.ChallengeDeadline = now + 60
	if err := SignCompletionClaim(eng.Domain(), actors.provider, &valid); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubmitClaim(valid); err != nil {
		t.Fatalf("expected claim with ChallengeDeadline >= receipt+window to succeed, got %v", err)
	}
	if _, err := eng.Finalize(funded.JobID); !errors.Is(err, ErrChallengeWindowActive) {
		t.Fatalf("expected ErrChallengeWindowActive immediately after claim, got %v", err)
	}
}

func TestProviderSelfChallengeCannotBypassDisputeWindow(t *testing.T) {
	clk := newFakeClock(1_700_000_000)
	eng := NewEscrowEngine(DefaultDomain(), clk, nil)
	actors := newTestActors(t)
	_ = eng.Mint(actors.buyer.Address(), 1_000_000)
	_ = eng.Mint(actors.provider.Address(), 1_000_000)
	_ = eng.Mint(actors.challenger.Address(), 1_000_000)

	input := "prompt:critical-deterministic-job"
	funded, _, _ := mustSignQuoteAndFund(
		t, eng, clk, actors, "job-self-chal-1", input, SpecHashChain64, ClassDeterministicHash,
		200_000, 100_000, 50_000, 20_000, 60,
	)

	// Dishonest provider submits a fraudulent OutputDigest on time.
	now := clk.Now().Unix()
	fraudClaim := CompletionClaim{
		JobID:             funded.JobID,
		QuoteHash:         funded.Quote.QuoteHash,
		Provider:          actors.provider.Address(),
		OutputDigest:      Keccak256Hex([]byte("bogus-output")),
		ExecutionProof:    Keccak256Hex([]byte("bogus-proof")),
		CompletedAt:       now,
		ChallengeDeadline: now + 60,
	}
	if err := SignCompletionClaim(eng.Domain(), actors.provider, &fraudClaim); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubmitClaim(fraudClaim); err != nil {
		t.Fatalf("SubmitClaim: %v", err)
	}

	// Provider attempts to bypass the dispute window by filing and resolving its own bogus missed_deadline challenge.
	clk.Advance(2 * time.Second)
	selfSub := ChallengeSubmission{
		JobID:               funded.JobID,
		Challenger:          actors.provider.Address(),
		ChallengeBondAtomic: 50_000,
		SubmittedAt:         clk.Now().Unix(),
		Evidence: ChallengeEvidence{
			ViolationType: ViolationMissedDeadline,
		},
	}
	if err := SignChallengeSubmission(eng.Domain(), actors.provider, &selfSub); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.OpenChallenge(selfSub); err != nil {
		t.Fatalf("OpenChallenge self missed_deadline: %v", err)
	}
	selfRes, err := eng.ResolveChallenge(funded.JobID)
	if err != nil {
		t.Fatalf("ResolveChallenge self missed_deadline: %v", err)
	}
	if selfRes.TerminalState != StateClaimed {
		t.Fatalf("rejected SLA challenge must preserve dispute window in StateClaimed, got %s", selfRes.TerminalState)
	}
	if _, err := eng.Finalize(funded.JobID); !errors.Is(err, ErrChallengeWindowActive) {
		t.Fatalf("expected ErrChallengeWindowActive after rejected self-challenge, got %v", err)
	}

	// Honest watcher still files ViolationFalseCompletionClaim within remaining dispute window and slashes the provider!
	clk.Advance(5 * time.Second)
	honestSub := ChallengeSubmission{
		JobID:               funded.JobID,
		Challenger:          actors.challenger.Address(),
		ChallengeBondAtomic: 50_000,
		SubmittedAt:         clk.Now().Unix(),
		Evidence: ChallengeEvidence{
			ViolationType:  ViolationFalseCompletionClaim,
			CanonicalInput: input,
		},
	}
	if err := SignChallengeSubmission(eng.Domain(), actors.challenger, &honestSub); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.OpenChallenge(honestSub); err != nil {
		t.Fatalf("honest OpenChallenge after rejected self-challenge failed: %v", err)
	}
	slashPayout, err := eng.ResolveChallenge(funded.JobID)
	if err != nil {
		t.Fatalf("honest ResolveChallenge: %v", err)
	}
	if slashPayout.TerminalState != StateSlashed || slashPayout.ProviderSlashedAtomic != 100_000 {
		t.Fatalf("expected fraudulent provider to be slashed, got %+v", slashPayout)
	}
	assertConserved(t, eng)
}

func TestMalformedChallengeEvidenceNeverFreezesFunds(t *testing.T) {
	clk := newFakeClock(1_700_000_000)
	eng := NewEscrowEngine(DefaultDomain(), clk, nil)
	actors := newTestActors(t)
	_ = eng.Mint(actors.buyer.Address(), 1_000_000)
	_ = eng.Mint(actors.provider.Address(), 1_000_000)
	_ = eng.Mint(actors.challenger.Address(), 1_000_000)

	input := "prompt:recoverable-funds"
	funded, outDigest, execProof := mustSignQuoteAndFund(
		t, eng, clk, actors, "job-malformed-ev-1", input, SpecKeccak256Eval, ClassDeterministicHash,
		200_000, 100_000, 50_000, 20_000, 30,
	)

	now := clk.Now().Unix()
	claim := CompletionClaim{
		JobID:             funded.JobID,
		QuoteHash:         funded.Quote.QuoteHash,
		Provider:          actors.provider.Address(),
		OutputDigest:      outDigest,
		ExecutionProof:    execProof,
		CompletedAt:       now,
		ChallengeDeadline: now + 30,
	}
	if err := SignCompletionClaim(eng.Domain(), actors.provider, &claim); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubmitClaim(claim); err != nil {
		t.Fatal(err)
	}

	// Empty CanonicalInput and mismatched CanonicalInput must be rejected by OpenChallenge
	// without transitioning to StateChallenged or freezing buyer/provider funds.
	for _, badInput := range []string{"", "wrong-canonical-input"} {
		badSub := ChallengeSubmission{
			JobID:               funded.JobID,
			Challenger:          actors.challenger.Address(),
			ChallengeBondAtomic: 50_000,
			SubmittedAt:         now + 1,
			Evidence: ChallengeEvidence{
				ViolationType:  ViolationFalseCompletionClaim,
				CanonicalInput: badInput,
			},
		}
		if err := SignChallengeSubmission(eng.Domain(), actors.challenger, &badSub); err != nil {
			t.Fatal(err)
		}
		if _, err := eng.OpenChallenge(badSub); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("expected ErrInvalidRequest for bad CanonicalInput %q, got %v", badInput, err)
		}
	}

	rec, err := eng.GetEscrow(funded.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != StateClaimed || rec.LockedChallengeBond != 0 {
		t.Fatalf("escrow mutated by invalid evidence: state=%s lockedChal=%d", rec.State, rec.LockedChallengeBond)
	}

	// Honest funds remain completely recoverable via Finalize once challenge window elapses.
	clk.Advance(35 * time.Second)
	payout, err := eng.Finalize(funded.JobID)
	if err != nil {
		t.Fatalf("Finalize after rejected malformed challenge: %v", err)
	}
	if payout.TerminalState != StateSettled || payout.ProviderPayoutAtomic != 300_000 {
		t.Fatalf("unexpected finalize payout: %+v", payout)
	}
	assertConserved(t, eng)
}

func TestZeroCompletedAtClaimRejectedWithoutMutation(t *testing.T) {
	clk := newFakeClock(1_700_000_000)
	eng := NewEscrowEngine(DefaultDomain(), clk, nil)
	actors := newTestActors(t)
	_ = eng.Mint(actors.buyer.Address(), 1_000_000)
	_ = eng.Mint(actors.provider.Address(), 1_000_000)

	funded, outDigest, execProof := mustSignQuoteAndFund(
		t, eng, clk, actors, "job-zero-ts-1", "input-zero-ts", SpecKeccak256Eval, ClassDeterministicHash,
		200_000, 100_000, 50_000, 20_000, 60,
	)

	now := clk.Now().Unix()
	zeroTS := CompletionClaim{
		JobID:             funded.JobID,
		QuoteHash:         funded.Quote.QuoteHash,
		Provider:          actors.provider.Address(),
		OutputDigest:      outDigest,
		ExecutionProof:    execProof,
		CompletedAt:       0,
		ChallengeDeadline: now + 60,
	}
	if err := SignCompletionClaim(eng.Domain(), actors.provider, &zeroTS); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubmitClaim(zeroTS); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest for CompletedAt==0 claim, got %v", err)
	}

	// Properly timestamped claim persists with valid signature and survives snapshot export/import.
	good := zeroTS
	good.CompletedAt = now
	if err := SignCompletionClaim(eng.Domain(), actors.provider, &good); err != nil {
		t.Fatal(err)
	}
	rec, err := eng.SubmitClaim(good)
	if err != nil {
		t.Fatalf("SubmitClaim good: %v", err)
	}
	if err := VerifyCompletionClaim(eng.Domain(), *rec.Claim); err != nil {
		t.Fatalf("persisted claim signature invalid: %v", err)
	}
	var buf bytes.Buffer
	if err := eng.ExportSnapshotJSONL(&buf); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSnapshotJSONL(bytes.NewReader(buf.Bytes()), clk); err != nil {
		t.Fatalf("ImportSnapshotJSONL failed: %v", err)
	}
}

func TestLargeBondRewardNoUint64Overflow(t *testing.T) {
	clk := newFakeClock(1_700_000_000)
	eng := NewEscrowEngine(DefaultDomain(), clk, nil)
	actors := newTestActors(t)

	const (
		price    = uint64(100_000_000_000_000_000)     // 10^17
		provBond = uint64(1_000_000_000_000_000_000)   // 10^18 (10^18 * 5000 overflows uint64!)
		chalBond = uint64(50_000_000_000_000_000)      // 5 * 10^16
		wantRew  = uint64(500_000_000_000_000_000)     // exact 50% of 10^18
	)
	_ = eng.Mint(actors.buyer.Address(), price)
	_ = eng.Mint(actors.provider.Address(), provBond)
	_ = eng.Mint(actors.challenger.Address(), chalBond)

	input := "prompt:large-bond-overflow"
	funded, _, _ := mustSignQuoteAndFund(
		t, eng, clk, actors, "job-overflow-reward-1", input, SpecKeccak256Eval, ClassDeterministicHash,
		price, provBond, chalBond, 100_000, 60,
	)

	now := clk.Now().Unix()
	fraud := CompletionClaim{
		JobID:             funded.JobID,
		QuoteHash:         funded.Quote.QuoteHash,
		Provider:          actors.provider.Address(),
		OutputDigest:      Keccak256Hex([]byte("wrong-large-bond")),
		ExecutionProof:    Keccak256Hex([]byte("wrong-proof")),
		CompletedAt:       now,
		ChallengeDeadline: now + 60,
	}
	if err := SignCompletionClaim(eng.Domain(), actors.provider, &fraud); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubmitClaim(fraud); err != nil {
		t.Fatal(err)
	}
	sub := ChallengeSubmission{
		JobID:               funded.JobID,
		Challenger:          actors.challenger.Address(),
		ChallengeBondAtomic: chalBond,
		SubmittedAt:         now + 1,
		Evidence: ChallengeEvidence{
			ViolationType:  ViolationFalseCompletionClaim,
			CanonicalInput: input,
		},
	}
	if err := SignChallengeSubmission(eng.Domain(), actors.challenger, &sub); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.OpenChallenge(sub); err != nil {
		t.Fatal(err)
	}
	payout, err := eng.ResolveChallenge(funded.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if payout.ChallengerPayoutAtomic != chalBond+wantRew {
		t.Fatalf("challenger payout = %d, want %d (overflow detected!)", payout.ChallengerPayoutAtomic, chalBond+wantRew)
	}
	if payout.BuyerPayoutAtomic != price+(provBond-wantRew) {
		t.Fatalf("buyer payout = %d, want %d", payout.BuyerPayoutAtomic, price+(provBond-wantRew))
	}
	assertConserved(t, eng)
}

