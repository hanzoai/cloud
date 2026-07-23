package legal

import (
	"context"
	"fmt"
)

// providers.go declares the two external seams a generated document rides to
// execution — e-signature and state/agency filing — plus the honest stubs used when
// no real provider is wired. Both mirror the company formation seams exactly (ONE way
// across the platform): a narrow interface, an honest default that fabricates nothing,
// and a config-driven real provider as a swap. A seam with no real backend records an
// honest state and never fakes a completed signature or a filed record.

// Signer is one e-signature recipient.
type Signer struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Esign is the e-signature seam. Request opens a signature request over a document for
// the given signers and returns a provider reference; Status reports completion. A
// real provider (DocuSign, Dropbox Sign, or the in-house clients/sign bundle)
// implements this; stubEsign is the honest default.
type Esign interface {
	Name() string
	Request(ctx context.Context, org, docID, title string, signers []Signer) (ref string, err error)
	Status(ctx context.Context, org, ref string) (complete bool, err error)
}

// stubEsign records a signature-request reference but performs NO real signing. It
// never reports a request complete on its own — completion arrives via the explicit
// /sign/complete signal (a reviewer action or a provider webhook). This is honest, not
// fake: it tracks a real request the org fulfils out-of-band until a real provider is
// wired.
type stubEsign struct{}

func (stubEsign) Name() string { return "manual" }

func (stubEsign) Request(_ context.Context, org, docID, title string, signers []Signer) (string, error) {
	if docID == "" {
		return "", fmt.Errorf("legal: no document to sign")
	}
	if len(signers) == 0 {
		return "", fmt.Errorf("legal: no signers")
	}
	return genID("esign")
}

func (stubEsign) Status(_ context.Context, org, ref string) (bool, error) {
	// Completion is driven by the explicit signal; the stub never self-completes.
	return false, nil
}

// Filer is the state/agency filing seam. Submit records a filing of the named
// documents; a real partner (Clerky, Firstbase, CSC) returns the state file number.
// stubFiler is the honest default: it records a "manual" status ("file through your
// registered agent") and fabricates no filing id.
type Filer interface {
	Name() string
	Submit(ctx context.Context, org, jurisdiction string, docIDs []string) (FilingStatus, string, error)
}

// stubFiler records an honest manual filing. It never returns a fabricated filing id.
type stubFiler struct{}

func (stubFiler) Name() string { return "manual" }

func (stubFiler) Submit(_ context.Context, org, jurisdiction string, docIDs []string) (FilingStatus, string, error) {
	if len(docIDs) == 0 {
		return "", "", fmt.Errorf("legal: no documents to file")
	}
	note := "No filing partner is configured. The documents were generated for signature; file them with the " +
		"relevant state or agency through your registered agent. Wire a Filer to file automatically."
	return FilingManual, note, nil
}
