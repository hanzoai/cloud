package company

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud"
)

// providers.go declares the provider SEAMS the formation machine drives, plus the
// honest stubs used when a real provider is not wired. Every external dependency
// (identity verification, billing, document storage, e-signature, cap table,
// on-chain anchor, state filing, org upgrade) is behind a narrow interface so the
// machine composes them the same way in production and in tests — and so a seam
// with no real backend fails HONESTLY (records nothing false) rather than faking a
// result.

// KYCProvider is the identity-verification seam (the clients/idv seam in the
// product spec). Start begins verification for one founder and returns a provider
// reference plus, for a hosted flow, a URL the founder visits; Check reports the
// current status. A real provider (Persona, Stripe Identity, Onfido, …) implements
// this; manualKYC is the honest default.
type KYCProvider interface {
	Start(ctx context.Context, org string, f Founder) (ref, verifyURL, status string, err error)
	Check(ctx context.Context, ref string) (status string, err error)
	Name() string
}

// Charger is the one-time billing seam: it authorizes and records the $999
// formation fee against the org's ledger. It returns a payment reference on
// success, or a metering error (mapped by the handler to 402/503) when funds are
// insufficient or billing is unavailable.
type Charger interface {
	Charge(ctx context.Context, org string, amountCents int64, memo string) (ref string, err error)
}

// DocumentSink stores a document for an org and returns its dataroom document id.
// It is how both generated formation docs and imported corporate docs land in the
// tenant's data room.
type DocumentSink interface {
	Ingest(ctx context.Context, org, name, contentType string, data []byte) (docID string, err error)
}

// Signer is one e-signature recipient.
type Signer struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Esign is the e-signature seam. Request creates a signature request over the named
// documents for the given signers and returns a provider reference; Status reports
// completion. Formation docs and fundraising SAFEs/notes both ride this seam.
type Esign interface {
	Request(ctx context.Context, org string, docIDs []string, signers []Signer) (ref string, err error)
	Status(ctx context.Context, org, ref string) (complete bool, err error)
	Name() string
}

// Stakeholder is the cap-table stakeholder shape the CapTable seam accepts. It
// mirrors the captable bundle's stakeholders.add contract.
type Stakeholder struct {
	Name                string `json:"name"`
	Email               string `json:"email"`
	StakeholderType     string `json:"stakeholderType"`     // INDIVIDUAL | INSTITUTION
	CurrentRelationship string `json:"currentRelationship"` // FOUNDER | INVESTOR | EMPLOYEE …
	InstitutionName     string `json:"institutionName,omitempty"`
}

// RoundInput is a fundraising round the CapTable seam records.
type RoundInput struct {
	Name              string  `json:"name"`
	RoundType         string  `json:"roundType"` // PRICED | SAFE | CONVERTIBLE_NOTE
	TargetAmount      float64 `json:"targetAmount"`
	PreMoneyValuation float64 `json:"preMoneyValuation,omitempty"`
	PricePerShare     float64 `json:"pricePerShare,omitempty"`
	ShareClassID      string  `json:"shareClassId,omitempty"`
}

// CapTable is the cap-table seam. SetIncorporation records the entity kind on the
// canonical captable company row (the "org upgraded to company" fact at the cap
// table layer); SeedFounders writes the founding allocation; RecordRound records a
// fundraising round. All are org-scoped.
type CapTable interface {
	SetIncorporation(ctx context.Context, org, companyName, incType, country, state string) error
	SeedFounders(ctx context.Context, org, companyName string, founders []Founder) error
	AddStakeholders(ctx context.Context, org string, holders []Stakeholder) (inserted int, err error)
	RecordRound(ctx context.Context, org string, r RoundInput) (roundID string, err error)
}

// EquityAnchor commits the cap-table equity genesis on-chain. Anchor computes a
// deterministic root of the founding allocation and, when the L1 wiring is present,
// commits it via a KMS-signed transaction (chain is the source of truth; the
// indexer projects it for reads). Root is always returned; TxHash/Block are set
// only when Configured().
type EquityAnchor interface {
	Anchor(ctx context.Context, f *Formation) (*Genesis, error)
	Configured() bool
}

// FilingProvider is the state-of-incorporation filing seam (Delaware / Wyoming).
// Submit files the formation with the state; Status polls it. No provider is wired
// by default — the stub records an honest "manual" status and never fabricates a
// filing id. See filing.go for exactly what a real integration requires.
type FilingProvider interface {
	Submit(ctx context.Context, f *Formation) (*Filing, error)
	Status(ctx context.Context, ref string) (*Filing, error)
	Name() string
}

// OrgUpgrader records the "this org is now a company" fact. The wired implementation
// stamps the incorporation on the canonical captable company row; reflecting it onto
// the IAM Organization is a documented follow-on (see adapters.go).
type OrgUpgrader interface {
	MarkCompany(ctx context.Context, f *Formation) error
}

// providerSet is the bundle of seams a mounted company service holds. build() wires
// the real implementations; tests substitute fakes field by field.
type providerSet struct {
	kyc      KYCProvider
	charge   Charger
	docs     DocumentSink
	esign    Esign
	captable CapTable
	anchor   EquityAnchor
	filing   FilingProvider
	upgrade  OrgUpgrader
	google   GoogleReader
}

// formationFeeCents is the one-time formation fee — $999. It is the billing SKU
// price charged on the commerce metering rail (ResourceMeter). Ops may override per
// deployment via CLOUD_COMPANY_FEE_CENTS, but the product default is fixed here.
const formationFeeCents int64 = 99900

// ---- Charger: the real, commerce-metering-backed implementation ----

// resourceCharger charges the formation fee through the shared ResourceMeter (the
// ONE per-org commerce billing client every resource-create handler already uses).
// It gates on the org's balance, then records the debit. When billing is not
// configured the gate allows and the debit is a no-op — identical to every other
// Hanzo resource, so dev/test are never blocked but production bills.
type resourceCharger struct {
	bill *cloud.ResourceMeter
}

func (rc resourceCharger) Charge(ctx context.Context, org string, amountCents int64, memo string) (string, error) {
	if rc.bill == nil {
		return "", fmt.Errorf("company: billing not available")
	}
	// Gate first: an unfunded org is refused BEFORE the formation proceeds
	// (ErrInsufficientBalance → 402, unreachable commerce → 503, both via
	// cloud.DenyResource at the handler).
	if err := rc.bill.Gate(ctx, org, "", false, "company-formation", amountCents); err != nil {
		return "", err
	}
	ref, err := genID("pay")
	if err != nil {
		return "", err
	}
	// Record the debit on the org's own ledger (fire-and-forget; the formation
	// already advanced, mirroring every ResourceMeter caller).
	rc.bill.Meter(org, "", "company-formation", amountCents, ref, "")
	return ref, nil
}

// ---- KYC: the honest manual stub ----

// manualKYC is the default identity-verification provider: it records a pending
// verification per founder and expects an out-of-band decision (an admin/manual
// review, or a real idv provider's webhook) to POST the result to
// /v1/company/kyc/callback. It NEVER auto-approves — verification is a real gate.
// A production deployment replaces this with a hosted idv provider by setting the
// providerSet.kyc field at mount.
type manualKYC struct{}

func (manualKYC) Name() string { return "manual" }

func (manualKYC) Start(_ context.Context, org string, f Founder) (ref, verifyURL, status string, err error) {
	if strings.TrimSpace(f.Email) == "" {
		return "", "", "", fmt.Errorf("founder email required for KYC")
	}
	ref, err = genID("kyc")
	if err != nil {
		return "", "", "", err
	}
	// No hosted URL for the manual provider; a real provider returns its own.
	return ref, "", KYCPending, nil
}

func (manualKYC) Check(_ context.Context, ref string) (string, error) {
	// The manual provider holds no state; status transitions arrive via the
	// callback endpoint. Report pending until then.
	if ref == "" {
		return "", fmt.Errorf("empty kyc ref")
	}
	return KYCPending, nil
}

// ---- Esign: the honest stub ----

// stubEsign records a signature request reference but performs NO real signing —
// the clients/esign subsystem is a goja bundle whose create→recipients→fields→send
// sequence has no in-process Go seam today (see company.go docs / the gap list).
// Completion arrives via /v1/company/esign/complete (a manual/webhook signal). It
// never reports a request complete on its own.
type stubEsign struct{}

func (stubEsign) Name() string { return "stub" }

func (stubEsign) Request(_ context.Context, org string, docIDs []string, signers []Signer) (string, error) {
	if len(docIDs) == 0 {
		return "", fmt.Errorf("no documents to sign")
	}
	if len(signers) == 0 {
		return "", fmt.Errorf("no signers")
	}
	return genID("esign")
}

func (stubEsign) Status(_ context.Context, org, ref string) (bool, error) {
	// Completion is driven by the explicit complete signal; the stub never
	// self-completes.
	return false, nil
}
