// Package compliance mounts the ORG-SCOPED compliance operations surface
// (/v1/compliance): the company's own KYC/KYB onboarding verification, accreditation
// STATE TRACKING, and a compliance-scoped read of the tamper-evident audit trail
// (the SOC 2 posture surface). It is the platform TOOLING a company's compliance
// team uses to orchestrate licensed verification providers and keep an evidence
// trail — with professionals in the loop.
//
// THE BOUNDARY (a design invariant baked into the data model and every response).
// This subsystem ORCHESTRATES providers and TRACKS what they report. It never
// asserts that a subject, or the org, "is compliant": there is no such value in the
// model. A verification status is exactly what the licensed provider reported
// (provider_verified / provider_rejected / manual_review) or pending; an
// accreditation record states who asserted or confirmed what, by which method — not
// a platform certification. The legal/regulatory determination belongs to the
// registered entity and its counsel, never to this platform. Every status-bearing
// response carries Disclaimer to keep that honest on the wire.
//
// WHAT IT COMPOSES (DRY — it forks none of these):
//   - clients/idv        the ONE identity/business verification seam (Persona /
//     Onfido / Stripe Identity behind a fail-closed interface;
//     the honest Manual provider by default).
//   - audit.Recorder     the ONE tamper-evident audit plane (deps.Audit). Every
//     privileged action (start / decide / callback) is recorded
//     there, referencing opaque ids ONLY — never subject PII.
//   - cek                encryption at rest for the per-deployment store, so subject
//     PII (name/email) is sealed on disk.
package compliance

import "github.com/hanzoai/cloud/clients/idv"

// Disclaimer is attached to every status-bearing response. It is the boundary
// invariant made visible on the wire: statuses are provider-reported, never a
// platform assertion of legal or regulatory compliance.
const Disclaimer = "Hanzo Compliance orchestrates licensed verification providers and records what they report. " +
	"It does not determine or certify legal or regulatory compliance — that determination belongs to the registered " +
	"entity and its counsel. Statuses are provider-reported or pending, never a platform assertion."

// SubjectKind is the kind of party under verification (individual → KYC, business →
// KYB). It mirrors idv.Kind so the seam and the record speak one vocabulary.
type SubjectKind = idv.Kind

// Subject is a party the org is verifying as part of its own onboarding/compliance —
// a team member, vendor, customer, or counterparty. It is the ONE place subject PII
// (name/email) lives; the store seals it at rest and it is returned only to the
// owning org. Checks and accreditation records reference a subject by opaque id, so
// nothing downstream (audit, logs, other records) needs to carry the PII.
type Subject struct {
	ID        string      `json:"id"`
	Org       string      `json:"org"`
	Kind      SubjectKind `json:"kind"`
	Ref       string      `json:"ref,omitempty"` // the org's own opaque external id for this subject
	Email     string      `json:"email,omitempty"`
	Name      string      `json:"name,omitempty"`
	CreatedAt int64       `json:"createdAt"`
	UpdatedAt int64       `json:"updatedAt"`
}

// Check is one KYC/KYB verification of a subject through the idv provider. Status is
// idv.Status — provider-reported or pending, never platform-asserted. DecidedBy
// records WHO settled a terminal status: the provider name (a hosted decision) or an
// org reviewer's user id (a recorded manual decision).
type Check struct {
	ID          string      `json:"id"`
	Org         string      `json:"org"`
	SubjectID   string      `json:"subjectId"`
	Kind        SubjectKind `json:"kind"`
	Provider    string      `json:"provider"`
	ProviderRef string      `json:"providerRef,omitempty"`
	VerifyURL   string      `json:"verifyUrl,omitempty"`
	Status      idv.Status  `json:"status"`
	DecidedBy   string      `json:"decidedBy,omitempty"`
	CreatedAt   int64       `json:"createdAt"`
	UpdatedAt   int64       `json:"updatedAt"`
	DecidedAt   int64       `json:"decidedAt,omitempty"`
}

// Accreditation method — HOW an accreditation state was established. Tracking only.
type AccreditationMethod string

const (
	MethodSelfAttested     AccreditationMethod = "self_attested"      // the subject asserted it
	MethodThirdPartyLetter AccreditationMethod = "third_party_letter" // a CPA/attorney letter on file
	MethodProviderVerified AccreditationMethod = "provider_verified"  // a licensed verifier confirmed it
)

// Accreditation basis — the CATEGORY of qualification (never the underlying PII
// figures, which are not stored).
type AccreditationBasis string

const (
	BasisIncome   AccreditationBasis = "income"
	BasisNetWorth AccreditationBasis = "net_worth"
	BasisLicense  AccreditationBasis = "professional_license"
	BasisEntity   AccreditationBasis = "entity"
)

// Accreditation status — the honest TRACKED state. There is no platform "accredited:
// true": the state is what was asserted or what a reviewer/provider confirmed.
type AccreditationStatus string

const (
	AccAsserted          AccreditationStatus = "asserted"           // recorded as asserted by the subject
	AccProviderVerified  AccreditationStatus = "provider_verified"  // a licensed verifier confirmed it
	AccReviewerConfirmed AccreditationStatus = "reviewer_confirmed" // an org reviewer confirmed the evidence
	AccRejected          AccreditationStatus = "rejected"           // a reviewer/provider rejected it
	AccExpired           AccreditationStatus = "expired"            // a prior confirmation has aged out
)

// Accreditation is a TRACKED accreditation-state record — an evidence entry the org
// keeps, NOT a platform certification. The underlying figures (income, net worth)
// are never stored; only the method, category, and who recorded what state. Evidence
// documents live in the org's sealed data room (EvidenceDocID references one).
type Accreditation struct {
	ID            string              `json:"id"`
	Org           string              `json:"org"`
	SubjectID     string              `json:"subjectId"`
	Method        AccreditationMethod `json:"method"`
	Basis         AccreditationBasis  `json:"basis"`
	Status        AccreditationStatus `json:"status"`
	EvidenceDocID string              `json:"evidenceDocId,omitempty"`
	ReviewerSub   string              `json:"reviewerSub,omitempty"` // org user who recorded a decision
	Note          string              `json:"note,omitempty"`        // non-PII operator note
	ExpiresAt     int64               `json:"expiresAt,omitempty"`
	CreatedAt     int64               `json:"createdAt"`
	UpdatedAt     int64               `json:"updatedAt"`
}

// validAccMethod / validAccBasis / validAccStatus gate the enums at the boundary.
func validAccMethod(m AccreditationMethod) bool {
	switch m {
	case MethodSelfAttested, MethodThirdPartyLetter, MethodProviderVerified:
		return true
	}
	return false
}

func validAccBasis(b AccreditationBasis) bool {
	switch b {
	case BasisIncome, BasisNetWorth, BasisLicense, BasisEntity:
		return true
	}
	return false
}

func validAccStatus(s AccreditationStatus) bool {
	switch s {
	case AccAsserted, AccProviderVerified, AccReviewerConfirmed, AccRejected, AccExpired:
		return true
	}
	return false
}
