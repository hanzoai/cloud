package company

import (
	"context"
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/idv"
)

// idv.go wires the formation's founder-KYC seam to the SHARED identity-verification
// provider (clients/idv) — the ONE place the platform orchestrates a KYC/KYB
// provider. When no external provider is configured the honest manualKYC default
// stands (callback-driven, never auto-approves). When an operator names a real
// provider (Persona / Onfido / Stripe Identity) via CLOUD_IDV_PROVIDER, that provider
// drives founder verification through the SAME seam Hanzo Compliance uses for org-side
// onboarding — one provider, no duplication.

// resolveKYC returns the founder-KYC provider for a mount. It is FAIL-CLOSED: a
// named-but-misconfigured provider is an error (the mount fails) rather than a silent
// downgrade to manual, which would weaken the KYC gate unnoticed. An unset provider
// is the honest manualKYC default.
func resolveKYC(deps cloud.Deps) (KYCProvider, error) {
	p, err := idv.FromConfig(kmsGetter(deps), os.Getenv)
	if err != nil {
		return nil, err
	}
	if _, isManual := p.(idv.Manual); isManual {
		return manualKYC{}, nil // the formation's own honest, callback-driven default
	}
	return idvKYC{p: p}, nil
}

// kmsGetter adapts deps.KMS into the idv secret resolver. A nil KMS yields a resolver
// that errors, so a real provider needing a sealed key fails closed at mount.
func kmsGetter(deps cloud.Deps) idv.SecretFn {
	if deps.KMS == nil {
		return func(context.Context, string) ([]byte, error) {
			return nil, fmt.Errorf("KMS not available")
		}
	}
	return deps.KMS.GetSecret
}

// idvKYC adapts the shared idv.Provider to the company KYCProvider seam: it maps a
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
