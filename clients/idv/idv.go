// Package idv is the ONE identity/business verification seam the Hanzo cloud binary
// uses to orchestrate a KYC (individual) or KYB (business) check through an external
// provider. It is deliberately small and provider-agnostic so every consumer wires
// the SAME contract: company formation (founder KYC) and the compliance product
// (org-side onboarding KYB/KYC) both drive verifications through a idv.Provider, and
// a real provider (Persona, Onfido, Stripe Identity) is a config-driven swap for the
// honest human-in-the-loop default — no consumer branches on which is wired.
//
// THE BOUNDARY (a design invariant, not a comment). This package ORCHESTRATES a
// licensed verification provider and TRACKS what the provider reported. It never
// asserts a subject is "compliant" or "verified" on its own authority: the only
// terminal status it can produce is a status the PROVIDER returned. The default
// (Manual) provider NEVER auto-approves — it returns pending and waits for an
// out-of-band decision recorded by a human reviewer or a provider webhook. Every
// status in this package is provider-reported or pending, never platform-asserted.
//
// FAIL-CLOSED. Every path that cannot obtain a positive provider decision resolves
// to a NON-terminal status (pending/review) or an error — never to verified. A
// transport error, a non-2xx response, an unparseable body, or an unrecognized
// provider status all map to pending. A verification can only reach provider_verified
// through an explicit, recognized provider success token (see classify).
package idv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// Kind is the kind of subject a verification runs over.
type Kind string

const (
	KindIndividual Kind = "individual" // a natural person — KYC
	KindBusiness   Kind = "business"   // a legal entity — KYB
)

// ValidKind reports whether k is a known subject kind.
func ValidKind(k Kind) bool { return k == KindIndividual || k == KindBusiness }

// Subject is the identifying information a verification is started for. It is PII:
// callers custody it sealed at rest and never place it in logs, URLs, or audit
// records. The provider collects the sensitive documents (ID image, proof of
// address, incorporation papers) directly from the subject via the hosted flow —
// this seam carries only the minimum needed to open that flow.
type Subject struct {
	Kind  Kind   // individual (KYC) or business (KYB)
	Name  string // legal name of the person or entity
	Email string // contact for the hosted flow invitation
	// Ref is the caller's OWN opaque reference for this subject (an internal id),
	// echoed to the provider as the inquiry's external reference so a webhook can be
	// correlated without carrying PII. Never a government id or secret.
	Ref string
}

// Status is the honest lifecycle of a verification. Every value except
// StatusReviewerConfirmed is PROVIDER-REPORTED: pending (no decision yet), a settled
// provider decision (verified/rejected), a provider-flagged human-review state, or an
// aged-out prior result. There is deliberately no "compliant" value.
//
// StatusReviewerConfirmed is the ONE value that is NOT provider-reported: it is
// produced ONLY by a consumer's attributed, role-gated human reviewer (a manual
// pass), never by an idv Provider. No Provider in this package — Manual, REST, or the
// classify table — can EVER emit it, so the boundary "the only terminal status a
// provider produces is one the provider returned" is unchanged. A gate treats it as a
// pass exactly like StatusVerified (see Pass), but the two are distinct on the wire so
// a manual confirmation is never dressed up as a provider decision.
type Status string

const (
	StatusPending           Status = "pending"            // started; awaiting the subject and the provider
	StatusVerified          Status = "provider_verified"  // the provider reported a passing decision
	StatusRejected          Status = "provider_rejected"  // the provider reported a failing decision
	StatusReview            Status = "manual_review"      // the provider flagged the case for human review
	StatusExpired           Status = "expired"            // a prior provider result has aged out
	StatusReviewerConfirmed Status = "reviewer_confirmed" // a privileged human reviewer confirmed the subject (NOT provider-reported)
)

// Terminal reports whether s is a SETTLED decision — a provider verify/reject or an
// attributed reviewer confirmation. A pending or review status is not terminal.
func (s Status) Terminal() bool {
	return s == StatusVerified || s == StatusRejected || s == StatusReviewerConfirmed
}

// Pass reports whether s is a PASSING terminal decision — the ONE predicate a gate
// uses. It admits a provider verify OR an attributed reviewer confirmation, and
// NOTHING else: a rejection, a review flag, or a pending status never satisfies it, so
// fail-closed holds by construction.
func (s Status) Pass() bool { return s == StatusVerified || s == StatusReviewerConfirmed }

// Valid reports whether s is a known status value.
func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusVerified, StatusRejected, StatusReview, StatusExpired, StatusReviewerConfirmed:
		return true
	}
	return false
}

// Session is the result of starting a verification: the provider's reference for
// the check, a hosted URL the subject visits to complete it (empty for the manual
// provider, which has no hosted flow), and the INITIAL status — which is always
// non-terminal (a provider cannot decide before the subject has acted).
type Session struct {
	Ref       string
	VerifyURL string
	Status    Status
}

// Result is the current state of a verification when polled: the reference and the
// provider-reported status.
type Result struct {
	Ref    string
	Status Status
}

// Provider is the provider-agnostic verification seam. Start begins a verification
// for a subject and returns a Session; Check polls the current status. A real
// provider implements this over its REST API (see REST); Manual is the honest
// default. Name identifies the wired provider for audit and display.
type Provider interface {
	Name() string
	Start(ctx context.Context, org string, subj Subject) (Session, error)
	Check(ctx context.Context, org, ref string) (Result, error)
}

// Manual is the honest human-in-the-loop provider and the default when no external
// provider is configured. It orchestrates a real manual flow: Start records a
// reference and returns pending; the terminal decision is supplied out-of-band by a
// reviewer (or a provider webhook) and recorded by the consumer, NOT invented here.
// It NEVER returns a terminal status on its own — Check stays pending — so a gate
// that requires verification can never be satisfied by the mere absence of a
// provider.
type Manual struct{}

// Name identifies the manual provider.
func (Manual) Name() string { return "manual" }

// Start records a pending verification. It requires something to correlate the
// eventual decision to (an email or the caller's ref); it returns no hosted URL and
// ALWAYS the pending status — never verified.
func (Manual) Start(_ context.Context, _ string, subj Subject) (Session, error) {
	if strings.TrimSpace(subj.Email) == "" && strings.TrimSpace(subj.Ref) == "" {
		return Session{}, fmt.Errorf("idv: subject needs an email or ref to start a manual verification")
	}
	ref, err := genRef()
	if err != nil {
		return Session{}, err
	}
	return Session{Ref: ref, VerifyURL: "", Status: StatusPending}, nil
}

// Check reports pending. The manual provider holds no decision state of its own; the
// consumer's store is the source of truth for a recorded reviewer/webhook decision,
// and until one is recorded the verification is honestly pending — never verified.
func (Manual) Check(_ context.Context, _ string, ref string) (Result, error) {
	if strings.TrimSpace(ref) == "" {
		return Result{}, fmt.Errorf("idv: empty verification ref")
	}
	return Result{Ref: ref, Status: StatusPending}, nil
}

// genRef mints a prefixed, collision-resistant provider-session reference.
func genRef() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("idv: rng: %w", err)
	}
	return "idv_" + hex.EncodeToString(b[:]), nil
}
