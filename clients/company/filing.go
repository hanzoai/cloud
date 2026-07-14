package company

import (
	"context"
	"time"
)

// filing.go implements the state-of-incorporation filing seam. Forming a real
// company means filing the Certificate of Incorporation (C-Corp) or Articles of
// Organization (LLC/DAO-LLC) with the Secretary of State and paying the state fee.
// Hanzo Company does NOT do that itself — it is performed by a registered-agent /
// filing partner. This file ships the interface plus an HONEST stub that files
// nothing and fabricates no filing id.
//
// WHAT A REAL Delaware / Wyoming FILING INTEGRATION REQUIRES
//
// A production FilingProvider wraps a registered-agent / filing partner. The
// realistic options and what each needs:
//
//  1. A commercial filing/registered-agent API — e.g. CSC (cscglobal.com),
//     Cogency Global, LegalInc, or a Clerky/Firstbase-style aggregator. Integration
//     needs: partner API base URL + API key (custodied in KMS, never in code); a
//     mapping from our Structure/Jurisdiction to the partner's entity-type + state
//     codes; the formation payload (entity name, registered agent, incorporator,
//     authorized shares / membership interests, founder addresses); idempotency on
//     a per-formation key so a retry never double-files; and webhook/poll handling
//     for the async state response (filed | rejected + the state file number and
//     stamped certificate PDF, which is then ingested into the data room).
//
//  2. Direct state endpoints — Delaware Division of Corporations files through
//     approved e-filing channels (there is no open public API; access is via a
//     Delaware-registered agent). Wyoming's Secretary of State offers online
//     business filing but not a general REST API. Both realistically require the
//     partner in (1); a "direct" provider would still need registered-agent
//     credentials and document-submission automation.
//
//  In all cases the provider must: (a) reserve/confirm name availability, (b)
//  submit the signed formation instrument, (c) pay the state fee (DE C-Corp ~$89+,
//  WY LLC ~$100 — through the partner's billing), (d) return the state file number
//  and the stamped certificate, and (e) surface rejection reasons for correction.
//  None of that is faked here.

// filingStatus values.
const (
	filingManual    = "manual"    // no partner wired — a human/registered agent files out-of-band
	filingSubmitted = "submitted" // partner accepted the submission, awaiting the state
	filingFiled     = "filed"     // the state accepted the filing
	filingRejected  = "rejected"  // the state/partner rejected it
)

// stubFiling is the default FilingProvider: it records an honest "manual" status
// (formation documents are generated and the org must complete the state filing
// through a registered agent) and NEVER returns a fabricated filing id. A
// production deployment sets providerSet.filing to a real partner-backed provider.
type stubFiling struct{}

func (stubFiling) Name() string { return "manual" }

func (stubFiling) Submit(_ context.Context, f *Formation) (*Filing, error) {
	return &Filing{
		Provider: "manual",
		Status:   filingManual,
		Note: "No state-filing partner is configured. Formation documents were generated for signature; " +
			"the incorporation must be filed with the " + jurisdictionName(f.Jurisdiction) +
			" Secretary of State through a registered agent. Wire a FilingProvider to file automatically.",
		At: time.Now().Unix(),
	}, nil
}

func (stubFiling) Status(_ context.Context, ref string) (*Filing, error) {
	return &Filing{Provider: "manual", Status: filingManual,
		Note: "manual filing — status is tracked out-of-band with the registered agent", Ref: ref}, nil
}

// jurisdictionName renders a jurisdiction code for prose.
func jurisdictionName(j Jurisdiction) string {
	switch j {
	case JurisdictionDE:
		return "Delaware"
	case JurisdictionWY:
		return "Wyoming"
	default:
		return string(j)
	}
}
