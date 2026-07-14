// Package company mounts /v1/company — Hanzo Company, the Stripe-Atlas-class
// incorporation + fundraising product. It runs ONE formation state machine per
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

// KYC statuses for a founder. A founder is verified only when the idv seam reports
// success; the payment step cannot be reached until every founder is verified.
const (
	KYCPending  = "pending"
	KYCVerified = "verified"
	KYCFailed   = "failed"
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
	Name      string `json:"name"`
	Email     string `json:"email"`
	EquityBps int    `json:"equityBps"`
	KYCStatus string `json:"kycStatus"`
	KYCRef    string `json:"kycRef,omitempty"` // idv session reference
}

// Genesis is the cap-table equity genesis: a deterministic root of the founding
// allocation committed on-chain (chain is the source of truth; the indexer projects
// it for reads). Root is always computed; TxHash/Block are set only when the L1
// anchor is wired — otherwise Status reports the honest pending state.
type Genesis struct {
	Root    string `json:"root"`             // 0x… keccak root of the founding allocation
	TxHash  string `json:"txHash,omitempty"` // L1 transaction hash (empty until anchored)
	Block   uint64 `json:"block,omitempty"`
	ChainID int64  `json:"chainId,omitempty"`
	At      int64  `json:"at"`
	Status  string `json:"status"` // pending | anchored
	Note    string `json:"note,omitempty"`
}

// Filing is the state-of-incorporation filing record. A real filing is performed by
// a Delaware/Wyoming filing partner (see providers.go FilingProvider); until one is
// wired the status is honest ("manual"/"pending") and NO fabricated filing id is
// recorded.
type Filing struct {
	Provider string `json:"provider"`
	Ref      string `json:"ref,omitempty"`
	Status   string `json:"status"` // manual | submitted | filed | rejected
	Note     string `json:"note,omitempty"`
	At       int64  `json:"at,omitempty"`
}

// Formation is the one incorporation record per org. It is both the persisted row
// (store.go) and the value the machine transitions. Every field a guard reads is
// here, so a transition decision is a pure function of this struct.
type Formation struct {
	Org          string       `json:"org"`
	Structure    Structure    `json:"structure"`
	Jurisdiction Jurisdiction `json:"jurisdiction"`
	Name         string       `json:"name"`
	Stage        Stage        `json:"stage"`
	Founders     []Founder    `json:"founders"`

	Paid       bool   `json:"paid"`
	PaymentRef string `json:"paymentRef,omitempty"`

	DocumentIDs []string `json:"documentIds,omitempty"` // dataroom doc ids of generated formation docs
	Filing      *Filing  `json:"filing,omitempty"`

	Signed   bool   `json:"signed"`
	EsignRef string `json:"esignRef,omitempty"`

	Genesis *Genesis `json:"genesis,omitempty"`

	// SKIP path.
	AlreadyIncorporated bool     `json:"alreadyIncorporated"`
	Imported            bool     `json:"imported"`
	ImportedDocs        []string `json:"importedDocs,omitempty"` // dataroom doc ids ingested from Drive
	CapTableImported    bool     `json:"capTableImported"`

	CreatedAt int64 `json:"createdAt"`
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

// guardKYCVerified gates the payment step behind identity verification: there must
// be at least one founder and EVERY founder must be KYC-verified.
func guardKYCVerified(f *Formation) error {
	if len(f.Founders) == 0 {
		return fmt.Errorf("add at least one founder before KYC")
	}
	for _, fo := range f.Founders {
		if fo.KYCStatus != KYCVerified {
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
