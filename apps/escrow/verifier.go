// SPDX-License-Identifier: Apache-2.0 OR MIT

package escrow

import (
	"fmt"
	"strings"
)

// Canonical deterministic verification specs shipped out-of-the-box so any
// independent verifier can recompute expected output digests without trusting Hanzo.
const (
	// SpecKeccak256Eval computes OutputDigest = Keccak256("compute:v1:keccak256:" || input || ":" || spec).
	SpecKeccak256Eval = "deterministic/keccak256-v1"

	// SpecHashChain64 runs 64 iterative Keccak-256 rounds over input to simulate verifiable sequential work.
	SpecHashChain64 = "deterministic/hashchain64-v1"

	// SpecSeededTokenTransform simulates a deterministic greedy/seed-pinned token execution commitment.
	SpecSeededTokenTransform = "deterministic/seeded-tokens-v1"

	// SpecNonDeterministicLLM marks temperature > 0 / non-deterministic floating-point sampling.
	// Raw bitwise output equality MUST NOT be used for slashing under this spec.
	SpecNonDeterministicLLM = "sampling/temperature-gt-0"
)

// ClassifyViolation maps a ViolationType to its ProofDomain and enforces the
// strict fail-closed objective-only slashing gate.
//
// Subjective quality claims are unconditionally rejected with ErrSubjectiveSlashingForbidden.
func ClassifyViolation(v ViolationType) (ProofDomain, error) {
	switch v {
	case ViolationMissedDeadline:
		return ProofDomainServiceQuality, nil
	case ViolationFalseCompletionClaim, ViolationInvalidSignatureOrEquivocation:
		return ProofDomainComputationIntegrity, nil
	case ViolationSubjectiveQuality:
		return ProofDomainSubjectiveForbidden, ErrSubjectiveSlashingForbidden
	default:
		return ProofDomainSubjectiveForbidden, fmt.Errorf("%w: %q", ErrUnknownViolationType, v)
	}
}

// ComputeInputDigest computes the canonical 32-byte input commitment from a raw input payload.
func ComputeInputDigest(canonicalInput string) string {
	return Keccak256Hex([]byte("escrow:input:v1:"), []byte(canonicalInput))
}

// ExecuteDeterministicKernel runs a self-contained, reproducible computation kernel
// and returns (outputDigest, executionProof). Any independent party can rerun this
// function offline to verify a provider's CompletionClaim without any trusted oracle.
func ExecuteDeterministicKernel(spec string, canonicalInput string) (outputDigest string, executionProof string, err error) {
	switch spec {
	case SpecKeccak256Eval:
		out := Keccak256Hex([]byte("compute:v1:keccak256:"), []byte(canonicalInput))
		proof := Keccak256Hex([]byte("proof:v1:keccak256:"), []byte(canonicalInput), []byte(out))
		return out, proof, nil
	case SpecHashChain64:
		cur := Keccak256Bytes([]byte("compute:v1:hashchain64:"), []byte(canonicalInput))
		checkpoint := cur
		for i := 0; i < 64; i++ {
			cur = Keccak256Bytes(cur[:], []byte{byte(i)})
			if i == 31 {
				checkpoint = cur
			}
		}
		out := Keccak256Hex(cur[:])
		proof := Keccak256Hex([]byte("proof:v1:hashchain64:mid:"), checkpoint[:], cur[:])
		return out, proof, nil
	case SpecSeededTokenTransform:
		out := Keccak256Hex([]byte("compute:v1:seeded-tokens:"), []byte(canonicalInput))
		proof := Keccak256Hex([]byte("proof:v1:seeded-tokens:trace:"), []byte(canonicalInput), []byte(out))
		return out, proof, nil
	case SpecNonDeterministicLLM:
		return "", "", ErrNonDeterministicVerificationNeeded
	default:
		return "", "", fmt.Errorf("%w: unknown deterministic verification spec %q", ErrInvalidRequest, spec)
	}
}

// ServiceGuaranteeVerifier evaluates ONLY objective Service Quality / SLA guarantees
// (deadline adherence, liveness, crash detection). It never inspects semantic output quality.
type ServiceGuaranteeVerifier interface {
	VerifySLA(quote JobQuote, claim *CompletionClaim, now int64) (ServiceGuaranteeProof, bool, string)
}

// DefaultServiceGuaranteeVerifier implements objective timestamp-based SLA verification.
type DefaultServiceGuaranteeVerifier struct{}

// VerifySLA returns (proof, violationConfirmed, reason).
// violationConfirmed is true iff the provider missed the objective Deadline.
func (DefaultServiceGuaranteeVerifier) VerifySLA(quote JobQuote, claim *CompletionClaim, now int64) (ServiceGuaranteeProof, bool, string) {
	if claim == nil {
		proof := ServiceGuaranteeProof{
			Deadline:         quote.Deadline,
			ClaimedAt:        0,
			OnTime:           false,
			LatencySeconds:   now - quote.CreatedAt,
			WorkerResponsive: false,
		}
		if now > quote.Deadline {
			return proof, true, fmt.Sprintf("objective SLA violation: no claim submitted by deadline %d (now=%d)", quote.Deadline, now)
		}
		return proof, false, "deadline not yet exceeded"
	}

	onTime := claim.CompletedAt <= quote.Deadline
	latency := claim.CompletedAt - quote.CreatedAt
	if latency < 0 {
		latency = 0
	}
	proof := ServiceGuaranteeProof{
		Deadline:         quote.Deadline,
		ClaimedAt:        claim.CompletedAt,
		OnTime:           onTime,
		LatencySeconds:   latency,
		WorkerResponsive: true,
	}
	if !onTime {
		return proof, true, fmt.Sprintf("objective SLA violation: completedAt %d > deadline %d", claim.CompletedAt, quote.Deadline)
	}
	return proof, false, "objective SLA met on time"
}

// ComputationVerifier evaluates ONLY objective Computation Correctness and Cryptographic Integrity.
// It is strictly decoupled from ServiceGuaranteeVerifier.
type ComputationVerifier interface {
	VerifyChallenge(domain EIP712Domain, quote JobQuote, claim *CompletionClaim, evidence ChallengeEvidence) (violationConfirmed bool, reason string, err error)
}

// DefaultComputationVerifier independently verifies deterministic recomputation proofs,
// quote-to-claim cryptographic binding, and double-output equivocation proofs without any oracle.
type DefaultComputationVerifier struct{}

// VerifyChallenge objectively adjudicates computation and signature/equivocation challenges.
// Returns (true, reason, nil) iff an objective protocol violation is cryptographically proved.
// Returns (false, reason, nil) iff the provider's claim is confirmed valid (malicious challenger!).
func (DefaultComputationVerifier) VerifyChallenge(
	domain EIP712Domain,
	quote JobQuote,
	claim *CompletionClaim,
	evidence ChallengeEvidence,
) (bool, string, error) {
	switch evidence.ViolationType {
	case ViolationSubjectiveQuality:
		return false, "", ErrSubjectiveSlashingForbidden

	case ViolationFalseCompletionClaim:
		if claim == nil {
			return false, "", fmt.Errorf("%w: no completion claim to verify", ErrInvalidRequest)
		}
		if quote.VerificationClass == ClassNonDeterministicSampling || quote.VerificationSpec == SpecNonDeterministicLLM {
			return false, "", ErrNonDeterministicVerificationNeeded
		}
		if evidence.CanonicalInput == "" {
			return false, "", fmt.Errorf("%w: canonicalInput is required for independent recomputation", ErrInvalidRequest)
		}
		// Independently verify that the supplied CanonicalInput matches the Quote's committed InputDigest.
		recomputedInputDigest := ComputeInputDigest(evidence.CanonicalInput)
		if !strings.EqualFold(recomputedInputDigest, quote.InputDigest) {
			return false, "", fmt.Errorf("%w: canonicalInput digest %s does not match quote inputDigest %s", ErrInvalidRequest, recomputedInputDigest, quote.InputDigest)
		}
		// Independently execute the deterministic verification kernel.
		expectedOut, expectedProof, err := ExecuteDeterministicKernel(quote.VerificationSpec, evidence.CanonicalInput)
		if err != nil {
			return false, "", err
		}
		if !strings.EqualFold(claim.OutputDigest, expectedOut) {
			return true, fmt.Sprintf("objective computation mismatch: claimed outputDigest %s != recomputed %s", claim.OutputDigest, expectedOut), nil
		}
		if !strings.EqualFold(claim.ExecutionProof, expectedProof) {
			return true, fmt.Sprintf("objective execution proof mismatch: claimed proof %s != recomputed %s", claim.ExecutionProof, expectedProof), nil
		}
		return false, "independent deterministic recomputation confirmed provider output and execution proof are valid", nil

	case ViolationInvalidSignatureOrEquivocation:
		if claim == nil {
			return false, "", fmt.Errorf("%w: no completion claim present", ErrInvalidRequest)
		}
		// Path A: Check if the stored claim itself violates quote binding or signature integrity.
		if !strings.EqualFold(claim.QuoteHash, quote.QuoteHash) || claim.JobID != quote.JobID || !SameAddress(claim.Provider, quote.Provider) {
			return true, "objective binding violation: claim parameters conflict with signed quote", nil
		}
		if err := VerifyCompletionClaim(domain, *claim); err != nil {
			return true, fmt.Sprintf("objective signature violation: %v", err), nil
		}

		// Path B: Verify cryptographic equivocation proof (two valid provider-signed claims for the same JobID with distinct OutputDigests).
		if evidence.ConflictingClaim != nil {
			other := *evidence.ConflictingClaim
			if other.JobID != quote.JobID || !strings.EqualFold(other.QuoteHash, quote.QuoteHash) || !SameAddress(other.Provider, quote.Provider) {
				return false, "conflicting claim does not match jobId/quoteHash/provider; challenger claim rejected", nil
			}
			if err := VerifyCompletionClaim(domain, other); err != nil {
				return false, "conflicting claim lacks valid provider EIP-712 signature; challenger claim rejected", nil
			}
			if !strings.EqualFold(other.OutputDigest, claim.OutputDigest) {
				return true, fmt.Sprintf("objective equivocation proved: provider signed two distinct outputDigests (%s vs %s) for job %s", claim.OutputDigest, other.OutputDigest, quote.JobID), nil
			}
			return false, "both signed claims have identical outputDigest; no equivocation occurred", nil
		}

		return false, "provider claim signature and quote binding verified valid; challenger claim rejected", nil

	default:
		return false, "", fmt.Errorf("%w: %q", ErrUnknownViolationType, evidence.ViolationType)
	}
}
