// Package company is incorporation end to end: pick a structure, add founders, pay,
// file, and e-sign.
//
// It mounts /v1/company — Hanzo Company: incorporation and fundraising, end to end,
// fundraising product. It runs ONE formation state machine per
// org: choose a structure (C-Corp / LLC / DAO-LLC) → add founders + KYC → pay the
// one-time $999 fee → generate formation documents → e-sign them → record the cap
// table's equity genesis on-chain → upgrade the org to a "company". An
// already-incorporated org SKIPS straight to the import path (corporate docs →
// dataroom, cap-table spreadsheet → captable) and lands at the same "company"
// terminal.
//
// This file is the machine: the domain model (Formation, Founder, Genesis) and the
// PURE transition logic. It performs no I/O — every guard is a total function of a
// *Formation, so the whole lifecycle (legal transitions, the payment gate, the skip
// path) is unit-testable without a store, a clock, or a network. company.go layers
// the per-org SQLite store, the provider seams (KYC, billing, esign, dataroom,
// captable, on-chain anchor, state filing), and the HTTP surface on top.
package company

import (
	"fmt"
	"strings"
)

// Stage is one state of the formation machine. The happy (formation) path runs
// structure → founders → payment → documents → esign → genesis → company; the SKIP
// path runs structure → import → company. Terminal is company.
type Stage string

const (
	StageStructure Stage = "structure" // initial: structure/jurisdiction/name being chosen
	StageFounders  Stage = "founders"  // founders added, KYC in flight
	StagePayment   Stage = "payment"   // the one-time $999 formation fee
	StageDocuments Stage = "documents" // formation documents generated
	StageEsign     Stage = "esign"     // documents out for signature
	StageGenesis   Stage = "genesis"   // cap-table equity genesis recorded on-chain
	StageCompany   Stage = "company"   // terminal: org is an incorporated company
	StageImport    Stage = "import"    // SKIP path: importing an existing company
)

// Structure is the legal entity a formation creates.
type Structure string

const (
	StructureCCorp  Structure = "c-corp"
	StructureLLC    Structure = "llc"
	StructureDAOLLC Structure = "dao-llc"
)

// validStructures is the closed vocabulary; anything else is rejected at the write
// boundary so an unknown structure can never reach document generation.
var validStructures = map[Structure]bool{
	StructureCCorp: true, StructureLLC: true, StructureDAOLLC: true,
}

// Jurisdiction is the state of formation. Hanzo Company supports Delaware and
// Wyoming — the two jurisdictions the state-filing partner seam targets.
type Jurisdiction string

const (
	JurisdictionDE Jurisdiction = "DE"
	JurisdictionWY Jurisdiction = "WY"
)

var validJurisdictions = map[Jurisdiction]bool{
	JurisdictionDE: true, JurisdictionWY: true,
}

// KYC statuses for a founder. A founder reaches a PASSING status by exactly two
// paths, never a client assertion: a real idv provider reports a pass (KYCVerified),
// or a privileged reviewer confirms the founder out-of-band (KYCReviewerConfirmed).
// The two are distinct so a manual confirmation is never dressed up as a provider
// decision. The payment step cannot be reached until every founder passes (kycPass).
const (
	KYCPending           = "pending"
	KYCVerified          = "verified"           // a real idv provider reported a pass
	KYCReviewerConfirmed = "reviewer_confirmed" // a privileged reviewer confirmed the founder (not provider-reported)
	KYCFailed            = "failed"
)

// incorporationType maps a Structure to the captable company row's
// incorporation_type value, so the canonical cap table records the same entity the
// formation created.
func (s Structure) incorporationType() string {
	switch s {
	case StructureCCorp:
		return "c-corp"
	case StructureLLC, StructureDAOLLC:
		return "llc"
	default:
		return ""
	}
}

// Founder is one founding stakeholder. EquityBps is the founder's ownership in
// basis points (1% == 100 bps); the founders' shares seed the cap-table genesis.
type Founder struct {
	// Name is the founder's full legal name, as it appears on the formation documents.
	Name string `json:"name"`
	// Email is the founder's email, and the key a KYC decision addresses a founder
	// by — POST /v1/company/kyc/decision matches on it.
	Email string `json:"email"`
	// EquityBps is the founder's ownership in basis points, 0–10000 (1% == 100 bps,
	// so 10000 is the whole company). The founders' shares seed the cap-table genesis.
	EquityBps int `json:"equityBps"`
	// KYCStatus is the founder's identity-verification state: pending, verified (a
	// real idv provider reported a pass), reviewer_confirmed (a privileged reviewer
	// confirmed out-of-band) or failed. The payment stage is unreachable until every
	// founder passes.
	KYCStatus string `json:"kycStatus"`
	// KYCRef is the idv provider's session reference for this founder.
	KYCRef string `json:"kycRef,omitempty"`
	// DecidedBy is who settled a terminal KYC status: the provider name, or a
	// reviewer's user id.
	DecidedBy string `json:"decidedBy,omitempty"`
}

// Genesis is the cap-table equity genesis: a deterministic root of the founding
// allocation committed on-chain (chain is the source of truth; the indexer projects
// it for reads). Root is always computed; TxHash/Block are set only when the L1
// anchor is wired — otherwise Status reports the honest pending state.
type Genesis struct {
	// Root is the 0x-prefixed keccak256 root of the founding allocation. It is
	// ALWAYS computed, whether or not the on-chain anchor is wired, because the root
	// is the tamper-evident witness.
	Root string `json:"root"`
	// TxHash is the L1 transaction hash of the anchoring commit. Empty until anchored.
	TxHash string `json:"txHash,omitempty"`
	// Block is the L1 block the anchoring transaction landed in. Set only once the
	// receipt has been read; absent otherwise.
	Block uint64 `json:"block,omitempty"`
	// ChainID is the EVM chain the root is committed to — the Hanzo L1 by default.
	ChainID int64 `json:"chainId,omitempty"`
	// At is the unix second the genesis root was computed.
	At int64 `json:"at"`
	// Status is pending (root computed, not yet on-chain) or anchored (committed).
	Status string `json:"status"`
	// Note explains an unanchored genesis honestly — anchor wiring absent, or the
	// submit error — rather than reporting a commit that did not happen.
	Note string `json:"note,omitempty"`
}

// Filing is the state-of-incorporation filing record. A real filing is performed by
// a Delaware/Wyoming filing partner (see providers.go FilingProvider); until one is
// wired the status is honest ("manual"/"pending") and NO fabricated filing id is
// recorded.
type Filing struct {
	// Provider is the filing partner that performed the filing, or "manual" when no
	// partner is wired.
	Provider string `json:"provider"`
	// Ref is the partner's or the state's filing reference. Empty when nothing was
	// actually filed — no filing id is ever fabricated.
	Ref string `json:"ref,omitempty"`
	// Status is manual (no partner wired — a registered agent files out-of-band),
	// submitted (the partner accepted it, awaiting the state), filed (the state
	// accepted it) or rejected.
	Status string `json:"status"`
	// Note explains a filing Hanzo did not perform itself: what remains to be done
	// and by whom.
	Note string `json:"note,omitempty"`
	// At is the unix second the filing record was written.
	At int64 `json:"at,omitempty"`
}

// Formation is the one incorporation record per org. It is both the persisted row
// (store.go) and the value the machine transitions. Every field a guard reads is
// here, so a transition decision is a pure function of this struct.
type Formation struct {
	// Org is the owning org — the tenant key, and the reason there is exactly one
	// formation per org.
	Org string `json:"org"`
	// Structure is the legal entity being formed: c-corp, llc or dao-llc.
	Structure Structure `json:"structure"`
	// Jurisdiction is the state of formation: DE or WY.
	Jurisdiction Jurisdiction `json:"jurisdiction"`
	// Name is the company name the entity is being formed under.
	Name string `json:"name"`
	// Stage is the machine's current state: structure, founders, payment, documents,
	// esign or genesis on the formation path, import on the skip path, and company
	// at the terminal.
	Stage Stage `json:"stage"`
	// Founders is every founding stakeholder, with its equity split and KYC state.
	Founders []Founder `json:"founders"`

	// Paid reports whether the one-time formation fee has been charged.
	Paid bool `json:"paid"`
	// PaymentRef is the billing reference recorded for the charged formation fee on
	// the org's own ledger.
	PaymentRef string `json:"paymentRef,omitempty"`

	// DocumentIDs are the data room ids of the GENERATED formation documents.
	DocumentIDs []string `json:"documentIds,omitempty"`
	// Filing is the state-of-incorporation filing record, once documents exist.
	Filing *Filing `json:"filing,omitempty"`

	// Signed reports whether the formation documents have come back signed — the
	// e-signature provider's answer, which a real provider's webhook drives.
	Signed bool `json:"signed"`
	// EsignRef is the e-signature provider's reference for the signature request.
	EsignRef string `json:"esignRef,omitempty"`

	// Genesis is the cap-table equity genesis, once recorded.
	Genesis *Genesis `json:"genesis,omitempty"`

	// ---- the SKIP path: an org that already has an entity ----

	// AlreadyIncorporated declares an org that already has a legal entity, which
	// takes the import path (structure → import → company) instead of forming one.
	AlreadyIncorporated bool `json:"alreadyIncorporated"`
	// Imported reports whether the existing company's corporate documents have been
	// ingested into the org's data room.
	Imported bool `json:"imported"`
	// ImportedDocs are the data room ids of the documents ingested from Drive.
	ImportedDocs []string `json:"importedDocs,omitempty"`
	// CapTableImported reports whether the existing company's cap table has been
	// imported onto the canonical cap table.
	CapTableImported bool `json:"capTableImported"`

	// CreatedAt is the unix second the formation was opened.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is the unix second of the most recent write to the formation.
	UpdatedAt int64 `json:"updatedAt"`
}

// transition is one legal edge of the machine plus its guard — the predicate that
// must hold for the edge to be traversable. The guard returns nil to allow, or an
// error naming exactly what is incomplete (which handlers surface as 409/422).
type transition struct {
	from  Stage
	to    Stage
	guard func(*Formation) error
}

// transitions is the ENTIRE machine — the single source of truth for what may
// follow what and under which condition. Read top-to-bottom: the formation path,
// then the skip path. There is no edge that is not listed here, so an illegal jump
// (e.g. structure → company, or payment before KYC) is refused by construction.
var transitions = []transition{
	// Formation path.
	{StageStructure, StageFounders, guardStructureChosen},
	{StageFounders, StagePayment, guardKYCVerified},
	{StagePayment, StageDocuments, guardPaid},
	{StageDocuments, StageEsign, guardDocumentsGenerated},
	{StageEsign, StageGenesis, guardSigned},
	{StageGenesis, StageCompany, guardGenesisRecorded},

	// SKIP path (already-incorporated org): jump straight to import, then finish.
	{StageStructure, StageImport, guardSkipRequested},
	{StageImport, StageCompany, guardImported},
}

// errIllegalTransition is returned by Advance for an edge that does not exist in the
// transition table; a guard returns a descriptive error for an edge that exists but
// is not yet satisfied. Handlers map the former to 409 and the latter to 422.
var errIllegalTransition = fmt.Errorf("company: illegal transition")

// Advance moves f to the target stage. It refuses any edge not in the transition
// table (errIllegalTransition) and any edge whose guard is unsatisfied (the guard's
// own error). On success it sets f.Stage = to and returns nil. It is PURE: it reads
// and writes only f, never a store or clock, so the caller persists f afterward.
func Advance(f *Formation, to Stage) error {
	for _, t := range transitions {
		if t.from != f.Stage || t.to != to {
			continue
		}
		if err := t.guard(f); err != nil {
			return err
		}
		f.Stage = to
		return nil
	}
	return fmt.Errorf("%w: %s → %s", errIllegalTransition, f.Stage, to)
}

// NextStages returns the stages reachable from f's current stage (regardless of
// whether their guards are satisfied yet) — the machine's out-edges, for the UI to
// render "what's next".
func NextStages(f *Formation) []Stage {
	var out []Stage
	for _, t := range transitions {
		if t.from == f.Stage {
			out = append(out, t.to)
		}
	}
	return out
}

// ---- guards (pure predicates over a *Formation) ----

func guardStructureChosen(f *Formation) error {
	if !validStructures[f.Structure] {
		return fmt.Errorf("choose a structure (c-corp, llc, or dao-llc) first")
	}
	if !validJurisdictions[f.Jurisdiction] {
		return fmt.Errorf("choose a jurisdiction (DE or WY) first")
	}
	if strings.TrimSpace(f.Name) == "" {
		return fmt.Errorf("a proposed company name is required")
	}
	return nil
}

// kycPass reports whether a founder's KYC is a passing terminal decision the payment
// gate accepts: a real provider pass (KYCVerified) or an attributed reviewer
// confirmation (KYCReviewerConfirmed), AND settled by a named decider. A raw,
// unattributed "verified" — exactly the shape a client forge would leave — never
// passes, so the gate is fail-closed against a forged status.
func kycPass(f Founder) bool {
	if f.DecidedBy == "" {
		return false
	}
	return f.KYCStatus == KYCVerified || f.KYCStatus == KYCReviewerConfirmed
}

// guardKYCVerified gates the payment step behind identity verification: there must
// be at least one founder and EVERY founder must PASS (kycPass) — a provider-reported
// pass or an attributed reviewer confirmation, never a client-asserted status.
func guardKYCVerified(f *Formation) error {
	if len(f.Founders) == 0 {
		return fmt.Errorf("add at least one founder before KYC")
	}
	for _, fo := range f.Founders {
		if !kycPass(fo) {
			return fmt.Errorf("founder %q is not KYC-verified (status %q)", fo.Email, fo.KYCStatus)
		}
	}
	return nil
}

// guardPaid is THE payment gate: documents cannot be generated until the one-time
// $999 formation fee is settled.
func guardPaid(f *Formation) error {
	if !f.Paid {
		return fmt.Errorf("the $999 formation fee has not been paid")
	}
	return nil
}

func guardDocumentsGenerated(f *Formation) error {
	if len(f.DocumentIDs) == 0 {
		return fmt.Errorf("formation documents have not been generated")
	}
	return nil
}

func guardSigned(f *Formation) error {
	if !f.Signed {
		return fmt.Errorf("formation documents have not been signed")
	}
	return nil
}

// guardGenesisRecorded requires the cap-table equity genesis root to be computed
// (and, when the L1 anchor is wired, committed on-chain). The root itself is the
// tamper-evident witness; an unwired chain is an honest pending anchor, not a block
// on incorporation.
func guardGenesisRecorded(f *Formation) error {
	if f.Genesis == nil || f.Genesis.Root == "" {
		return fmt.Errorf("the cap-table equity genesis has not been recorded")
	}
	return nil
}

// guardSkipRequested allows the skip only for an org that has declared it is already
// incorporated.
func guardSkipRequested(f *Formation) error {
	if !f.AlreadyIncorporated {
		return fmt.Errorf("skip is only available to an already-incorporated org (set alreadyIncorporated)")
	}
	return nil
}

// guardImported requires the existing company's corporate documents AND cap table to
// have been imported before the org is upgraded to a company.
func guardImported(f *Formation) error {
	if !f.CapTableImported {
		return fmt.Errorf("import the cap table before completing")
	}
	if !f.Imported {
		return fmt.Errorf("import at least one corporate document before completing")
	}
	return nil
}
