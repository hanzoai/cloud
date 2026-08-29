package company

import (
	"context"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/idv"
)

// idv.go wires the formation's founder-KYC client to the SHARED identity-verification
// provider (clients/idv) — the ONE place the platform orchestrates a KYC/KYB
// provider. When no external provider is configured the honest manualKYC default
// stands (callback-driven, never auto-approves). When an operator names a real
// provider (Persona / Onfido / Stripe Identity) via CLOUD_IDV_PROVIDER, that provider
// drives founder verification through the SAME client Hanzo Compliance uses for org-side
// onboarding — one provider, no duplication.

// resolveKYC returns the founder-KYC provider for a mount. A named-but-misconfigured
// provider must never downgrade to manual — that would weaken the KYC gate unnoticed —
// so it resolves to brokenKYC, which refuses.
//
// It does not fail the MOUNT. Two of this app's ~24 routes read the provider
// (POST /v1/company/kyc and /kyc/refresh); the rest — the formation record, /tariff
// and /ein, the founders and documents, the fundraise reads — never touch it, and a
// KYC secret is no reason for them to answer 503. That mattered most for
// /kyc/decision, the human-in-the-loop path this package calls the only route to a
// pass when no real provider is wired: a mount refusal took down the one manual
// remedy for the provider's own misconfiguration.
//
// An unset provider is still the honest manualKYC default.
func resolveKYC(deps cloud.Deps) KYCProvider {
	p, err := idv.FromConfig(deps.Secret(), os.Getenv)
	if err != nil {
		return brokenKYC{err: err}
	}
	if _, isManual := p.(idv.Manual); isManual {
		return manualKYC{} // the formation's own honest, callback-driven default
	}
	return idvKYC{p: p}
}

// brokenKYC is what a misconfigured deployment gets. Every call carries the
// configuration error out, so the two ops that verify identity name it and the
// rest of the surface keeps serving.
//
// It is FAIL-CLOSED by construction, and the payment gate is what proves it: a
// founder only reaches StagePayment through guardKYCVerified, which demands a
// DecidedBy on every founder, and brokenKYC mints no reference and reports no
// status, so DecidedBy is never set. Refusing louder than manualKYC also matters —
// manual is a legitimate configuration, and answering "manual" here would report a
// misconfiguration as a working default.
type brokenKYC struct{ err error }

func (brokenKYC) Name() string { return "unavailable" }

func (b brokenKYC) Start(context.Context, string, Founder) (ref, verifyURL, status string, err error) {
	return "", "", "", b.err
}

func (b brokenKYC) Check(context.Context, string) (string, error) { return "", b.err }

// idvKYC adapts the shared idv.Provider to the company KYCProvider client: it maps a
// Founder to an idv.Subject and the provider's honest status vocabulary onto the
// formation's three-state KYC status, keeping the payment gate closed on anything but
// a provider-reported pass.
type idvKYC struct{ p idv.Provider }

func (k idvKYC) Name() string { return k.p.Name() }

func (k idvKYC) Start(ctx context.Context, org string, f Founder) (ref, verifyURL, status string, err error) {
	sess, err := k.p.Start(ctx, org, idv.Subject{Kind: idv.KindIndividual, Name: f.Name, Email: f.Email, Ref: f.Email})
	if err != nil {
		return "", "", "", err
	}
	// A start is never a decision — clamp a terminal status to pending so a
	// misbehaving provider cannot pass the payment gate at inquiry time.
	st := sess.Status
	if st.Terminal() {
		st = idv.StatusPending
	}
	return sess.Ref, sess.VerifyURL, companyKYCStatus(st), nil
}

func (k idvKYC) Check(ctx context.Context, ref string) (string, error) {
	// company's Check carries no org (the provider ref is globally unique); idv uses
	// org only for diagnostics, so an empty org is correct here.
	res, err := k.p.Check(ctx, "", ref)
	if err != nil {
		return "", err
	}
	return companyKYCStatus(res.Status), nil
}

// companyKYCStatus maps the shared idv status vocabulary onto the formation's
// three-state KYC status. Only a provider-reported PASS becomes verified; a rejection
// is failed; pending / review / expired all stay pending, so guardKYCVerified keeps
// the payment step closed until a real pass arrives.
func companyKYCStatus(s idv.Status) string {
	switch s {
	case idv.StatusVerified:
		return KYCVerified
	case idv.StatusRejected:
		return KYCFailed
	default:
		return KYCPending
	}
}
